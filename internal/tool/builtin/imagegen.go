package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/attachment"
	"reasonix/internal/netclient"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

// ImageGenSpec is the host-injected image endpoint generate_image talks to. The
// host resolves it from one configured [[providers]] entry, so the tool reuses
// that entry's base_url, API key, and network proxy instead of introducing a
// second credential store (see [imagegen] in reasonix.toml).
type ImageGenSpec struct {
	BaseURL string
	APIKey  string
	Model   string
	Size    string
	OutDir  string
	Proxy   netclient.ProxySpec
}

// Configured reports whether the host resolved an endpoint for this session. An
// unresolved spec keeps the tool unavailable rather than failing at call time.
func (s ImageGenSpec) Configured() bool {
	return strings.TrimSpace(s.BaseURL) != "" && strings.TrimSpace(s.Model) != ""
}

const (
	imageGenTimeout = 120 * time.Second
	// imageGenMaxBytes caps one encoded image, matching the provider-side inline
	// image limit.
	imageGenMaxBytes = 32 << 20
	// defaultImageGenDir is workspace-relative and covered by .gitignore.
	defaultImageGenDir  = ".reasonix/generated"
	imageGenEndpointAPI = "/images/generations"
)

type imageGenArgs struct {
	Prompt string `json:"prompt"`
	Size   string `json:"size"`
	Model  string `json:"model"`
	Path   string `json:"path"`
}

// imageGen generates an image through an OpenAI-compatible /images/generations
// endpoint and saves it inside the workspace. The image rides the structured
// image channel (see tool.ImageTool) so an image-capable conversation model sees
// what it produced; the saved path is what the user opens, e.g. via present.
type imageGen struct {
	workDir string
	roots   []string
	rootSet *sandbox.WritableRootSet
	guard   SessionDataGuard
	spec    ImageGenSpec
}

func init() { tool.RegisterBuiltin(imageGen{}) }

func (imageGen) Name() string { return "generate_image" }

func (imageGen) Description() string {
	return "Generate an image from a text prompt through the image endpoint configured under [imagegen] and save it as a file in the workspace. Returns the saved path and attaches the image for image-capable models. Use this for text-to-image requests instead of trying to draw anything yourself."
}

func (imageGen) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "prompt":{"type":"string","description":"Text description of the image to generate"},
  "size":{"type":"string","description":"Image size as WIDTHxHEIGHT, e.g. 1024x1024; optional, defaults to the configured size"},
  "model":{"type":"string","description":"Image model override for this call; optional"},
  "path":{"type":"string","description":"Output file path, relative to the workspace; optional, defaults to a generated name under the configured output directory"}
},
"required":["prompt"]
}`)
}

func (imageGen) ReadOnly() bool { return false }

// PlanModeSafe is false: the tool performs an outbound network call and writes
// an image file, which plan mode must not do silently.
func (imageGen) PlanModeSafe() bool { return false }

// ProviderVisible hides the tool while no endpoint is configured, so an
// unconfigured session sees no unusable schema.
func (g imageGen) ProviderVisible(context.Context) bool { return g.spec.Configured() }

func (g imageGen) DeclareWriteAccess(args json.RawMessage) (tool.WriteAccessDeclaration, error) {
	var p imageGenArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.WriteAccessDeclaration{}, fmt.Errorf("invalid args: %w", err)
	}
	return declareParentWriteDirs(g.workDir, g.outputPath(p.Path))
}

func (g imageGen) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	text, _, err := g.ExecuteWithImages(ctx, args)
	return text, err
}

func (g imageGen) ExecuteWithImages(ctx context.Context, args json.RawMessage) (string, []string, error) {
	var p imageGenArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", nil, fmt.Errorf("invalid args: %w", err)
	}
	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return "", nil, fmt.Errorf("prompt is required")
	}
	if !g.spec.Configured() {
		return "", nil, fmt.Errorf("image generation is not configured: set [imagegen] provider to the name of a configured [[providers]] entry in reasonix.toml")
	}
	requestCtx, cancel := context.WithTimeout(ctx, imageGenTimeout)
	defer cancel()

	raw, err := g.requestImage(requestCtx, p, prompt)
	if err != nil {
		return "", nil, err
	}

	policy := attachment.DefaultPolicy()
	policy.MaxBytes = imageGenMaxBytes
	mime, width, height, err := attachment.ValidateImage(raw, "", policy)
	if err != nil {
		return "", nil, fmt.Errorf("image endpoint returned an unusable image: %w", err)
	}

	target := g.outputPath(p.Path)
	if err := confineWrite(ctx, effectiveWriteRoots(ctx, g.rootSet, g.roots), g.guard, ManagedConfigPaths{}, target); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", nil, fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		return "", nil, fmt.Errorf("write %s: %w", target, err)
	}

	display := target
	if rel, relErr := filepath.Rel(g.workDir, target); g.workDir != "" && relErr == nil && filepath.IsLocal(rel) {
		display = filepath.ToSlash(rel)
	}
	text := fmt.Sprintf("[image: %s, %dx%d] %s\nGenerated image saved to %s; call present with this path to hand it to the user.",
		mime, width, height, display, display)
	return text, []string{attachment.DataURL(mime, raw)}, nil
}

// requestImage performs the generation call and returns the encoded image. A
// gateway that mirrors the OpenAI route but keeps its native image_size
// parameter (SiliconFlow) rejects size, so that one 400 is retried once.
func (g imageGen) requestImage(ctx context.Context, p imageGenArgs, prompt string) ([]byte, error) {
	model := strings.TrimSpace(p.Model)
	if model == "" {
		model = g.spec.Model
	}
	size := strings.TrimSpace(p.Size)
	if size == "" {
		size = g.spec.Size
	}
	body := map[string]any{"model": model, "prompt": prompt}
	if size != "" {
		body["size"] = size
	}

	raw, err := g.postImages(ctx, body)
	if err != nil && size != "" {
		var httpErr *imageGenHTTPError
		if errors.As(err, &httpErr) && strings.Contains(httpErr.body, "image_size") {
			delete(body, "size")
			body["image_size"] = size
			raw, err = g.postImages(ctx, body)
		}
	}
	if err != nil {
		return nil, err
	}

	ref, err := parseImageGenResponse(raw)
	if err != nil {
		return nil, err
	}
	if ref.b64 != "" {
		encoded := base64.NewDecoder(base64.StdEncoding, strings.NewReader(stripBase64Whitespace(ref.b64)))
		data, err := io.ReadAll(io.LimitReader(encoded, imageGenMaxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("decode generated image: %w", err)
		}
		if len(data) > imageGenMaxBytes {
			return nil, fmt.Errorf("generated image exceeds %d bytes", imageGenMaxBytes)
		}
		return data, nil
	}
	return g.downloadImage(ctx, ref.url)
}

func (g imageGen) postImages(ctx context.Context, body map[string]any) (json.RawMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(g.spec.BaseURL), "/") + imageGenEndpointAPI
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(g.spec.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client, err := netclient.NewHTTPClient(g.spec.Proxy, netclient.TransportOptions{})
	if err != nil {
		return nil, fmt.Errorf("image client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("generate_image request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imageGenMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, &imageGenHTTPError{status: resp.StatusCode, body: string(raw)}
	}
	return json.RawMessage(raw), nil
}

// downloadImage fetches a generated-image URL returned by the endpoint. The URL
// comes from the remote response, so it goes through the same SSRF guard as
// web_fetch and the session's configured proxy.
func (g imageGen) downloadImage(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build image download: %w", err)
	}
	client := &http.Client{
		Timeout: imageGenTimeout,
		Transport: webFetchRoundTripper{proxyURLFor: func(req *http.Request) (string, error) {
			return proxyURLForRequest(g.spec.Proxy, req)
		}},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download generated image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("download generated image: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, imageGenMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download generated image: %w", err)
	}
	if len(data) > imageGenMaxBytes {
		return nil, fmt.Errorf("generated image exceeds %d bytes", imageGenMaxBytes)
	}
	return data, nil
}

// outputPath resolves the caller's path, defaulting to a timestamped name under
// the configured output directory so repeated calls never overwrite one image.
func (g imageGen) outputPath(path string) string {
	if p := strings.TrimSpace(path); p != "" {
		return resolveIn(g.workDir, p)
	}
	dir := strings.TrimSpace(g.spec.OutDir)
	if dir == "" {
		dir = defaultImageGenDir
	}
	return resolveIn(g.workDir, filepath.ToSlash(filepath.Join(dir, fmt.Sprintf("generated-%d.png", time.Now().UnixNano()))))
}

type imageGenReference struct {
	b64 string
	url string
}

// parseImageGenResponse accepts both response shapes seen in the wild: the
// OpenAI data[].b64_json / data[].url form and the images[].url wrapper some
// gateways return (SiliconFlow).
func parseImageGenResponse(raw []byte) (imageGenReference, error) {
	var payload struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
		Images []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return imageGenReference{}, fmt.Errorf("image endpoint returned invalid JSON: %w", err)
	}
	for _, item := range payload.Data {
		if b64 := strings.TrimSpace(item.B64JSON); b64 != "" {
			return imageGenReference{b64: b64}, nil
		}
		if url := strings.TrimSpace(item.URL); url != "" {
			return imageGenReference{url: url}, nil
		}
	}
	for _, item := range payload.Images {
		if url := strings.TrimSpace(item.URL); url != "" {
			return imageGenReference{url: url}, nil
		}
		if b64 := strings.TrimSpace(item.B64JSON); b64 != "" {
			return imageGenReference{b64: b64}, nil
		}
	}
	var failure struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &failure) == nil && strings.TrimSpace(failure.Error.Message) != "" {
		return imageGenReference{}, fmt.Errorf("image endpoint reported an error: %s", failure.Error.Message)
	}
	return imageGenReference{}, fmt.Errorf("image endpoint returned no image data")
}

// stripBase64Whitespace removes line breaks some gateways insert into b64_json.
func stripBase64Whitespace(s string) string {
	return strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(s)
}

type imageGenHTTPError struct {
	status int
	body   string
}

func (e *imageGenHTTPError) Error() string {
	detail := strings.TrimSpace(e.body)
	if runes := []rune(detail); len(runes) > 400 {
		detail = string(runes[:400])
	}
	if detail == "" {
		return fmt.Sprintf("image endpoint returned HTTP %d", e.status)
	}
	return fmt.Sprintf("image endpoint returned HTTP %d: %s", e.status, detail)
}
