package boot

import (
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/netclient"
)

func TestImageGenSpecFromConfigReusesProviderEntry(t *testing.T) {
	t.Setenv("SILICONFLOW_API_KEY", "test-key")
	cfg := &config.Config{
		ImageGen: config.ImageGenConfig{Provider: "siliconflow", Size: "1024x1024", OutputDir: "gen/out"},
		Providers: []config.ProviderEntry{{
			Name:      "siliconflow",
			Kind:      "openai",
			BaseURL:   "https://api.siliconflow.cn/v1",
			APIKeyEnv: "SILICONFLOW_API_KEY",
			Models:    []string{"Tongyi-MAI/Z-Image-Turbo"},
			Default:   "Tongyi-MAI/Z-Image-Turbo",
		}},
	}
	spec := imageGenSpecFromConfig(cfg, netclient.ProxySpec{Mode: netclient.ModeAuto})
	if spec.BaseURL != "https://api.siliconflow.cn/v1" {
		t.Fatalf("BaseURL = %q", spec.BaseURL)
	}
	if spec.Model != "Tongyi-MAI/Z-Image-Turbo" {
		t.Fatalf("Model = %q", spec.Model)
	}
	if spec.Size != "1024x1024" || spec.OutDir != "gen/out" {
		t.Fatalf("Size/OutDir = %q/%q", spec.Size, spec.OutDir)
	}
	if !spec.Configured() {
		t.Fatal("spec should report configured")
	}
}

func TestImageGenSpecFromConfigEmptyOrUnknownProvider(t *testing.T) {
	cfg := &config.Config{Providers: []config.ProviderEntry{{Name: "siliconflow", BaseURL: "https://example.test/v1", Models: []string{"m"}}}}
	if spec := imageGenSpecFromConfig(cfg, netclient.ProxySpec{}); spec.Configured() {
		t.Fatalf("unset provider must stay unconfigured: %+v", spec)
	}
	cfg.ImageGen.Provider = "missing"
	if spec := imageGenSpecFromConfig(cfg, netclient.ProxySpec{}); spec.Configured() {
		t.Fatalf("unknown provider must stay unconfigured: %+v", spec)
	}
}
