package config

// ImageGenConfig points the generate_image built-in at an image endpoint. It
// reuses one [[providers]] entry — base_url, API key, and the network proxy —
// instead of adding a second place to store credentials. It lives here rather
// than in config.go so the main config file stays inside its size budget.
type ImageGenConfig struct {
	// Provider names a configured [[providers]] entry to reuse. Empty leaves
	// generate_image unavailable.
	Provider string `toml:"provider"`
	// Model is the default image model; empty falls back to the referenced
	// entry's default model.
	Model string `toml:"model"`
	// Size is the default image size, e.g. 1024x1024; empty leaves the choice
	// to the endpoint.
	Size string `toml:"size"`
	// OutputDir is where generated images are written, relative to the
	// workspace root. Empty means .reasonix/generated.
	OutputDir string `toml:"output_dir"`
}
