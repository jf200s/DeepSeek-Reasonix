package config

import (
	"fmt"
	"strings"
)

// renderImageGenConfig writes the [imagegen] section. The section points
// generate_image at one configured [[providers]] entry so the endpoint's
// base_url, API key, and the network proxy apply without a second credential
// store; an empty provider keeps the tool unavailable.
func renderImageGenConfig(b *strings.Builder, cfg ImageGenConfig) {
	b.WriteString("[imagegen]\n")
	if cfg.Provider == "" {
		b.WriteString("# provider = \"siliconflow\"   # name of a configured [[providers]] entry; empty = generate_image is unavailable\n")
	} else {
		fmt.Fprintf(b, "provider = %q\n", cfg.Provider)
	}
	if cfg.Model == "" {
		b.WriteString("# model    = \"Tongyi-MAI/Z-Image-Turbo\"   # default image model; empty = the referenced entry's default\n")
	} else {
		fmt.Fprintf(b, "model    = %q\n", cfg.Model)
	}
	if cfg.Size == "" {
		b.WriteString("# size     = \"1024x1024\"   # default image size; empty = let the endpoint choose\n")
	} else {
		fmt.Fprintf(b, "size     = %q\n", cfg.Size)
	}
	if cfg.OutputDir == "" {
		b.WriteString("# output_dir = \".reasonix/generated\"   # workspace-relative directory for generated images\n")
	} else {
		fmt.Fprintf(b, "output_dir = %q\n", cfg.OutputDir)
	}
	b.WriteString("\n")
}
