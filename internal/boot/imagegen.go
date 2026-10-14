package boot

import (
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/netclient"
	"reasonix/internal/tool/builtin"
)

// imageGenSpecFromConfig resolves [imagegen] against the configured provider
// entries. generate_image reuses the referenced entry's base_url and resolved
// API key, so the image endpoint needs no separate credential. An unset or
// unknown provider leaves the zero spec, which keeps the tool hidden.
func imageGenSpecFromConfig(cfg *config.Config, proxySpec netclient.ProxySpec) builtin.ImageGenSpec {
	want := strings.TrimSpace(cfg.ImageGen.Provider)
	if want == "" {
		return builtin.ImageGenSpec{}
	}
	for i := range cfg.Providers {
		entry := &cfg.Providers[i]
		if !strings.EqualFold(strings.TrimSpace(entry.Name), want) {
			continue
		}
		model := strings.TrimSpace(cfg.ImageGen.Model)
		if model == "" {
			model = entry.DefaultModel()
		}
		return builtin.ImageGenSpec{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey(),
			Model:   model,
			Size:    strings.TrimSpace(cfg.ImageGen.Size),
			OutDir:  strings.TrimSpace(cfg.ImageGen.OutputDir),
			Proxy:   proxySpec,
		}
	}
	return builtin.ImageGenSpec{}
}
