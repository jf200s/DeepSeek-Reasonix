package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"reasonix/internal/base/testenv"
)

func TestPTCIsReadFromTheUserFile(t *testing.T) {
	isolateUserConfigHome(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[tools.ptc]\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Tools.PTC.Enabled {
		t.Fatalf("[tools.ptc].enabled did not survive Load: %+v", cfg.Tools.PTC)
	}
}

func TestPTCConfigDecodesAndDefaults(t *testing.T) {
	c := decodedTools(t, `
[tools.ptc]
enabled = true
timeout_seconds = 30
max_calls = 7
max_parallel = 2
`)
	if !c.Tools.PTC.Enabled {
		t.Fatal("enabled did not decode")
	}
	if got := c.Tools.PTC.Timeout().Seconds(); got != 30 {
		t.Fatalf("Timeout = %vs, want 30s", got)
	}
	if c.Tools.PTC.MaxCalls != 7 || c.Tools.PTC.MaxParallel != 2 {
		t.Fatalf("limits = %+v", c.Tools.PTC)
	}
	// Nil Exclusive means the default, which is the narrow surface.
	if !c.Tools.PTC.ExclusiveMode() {
		t.Fatal("ExclusiveMode defaulted off, want on")
	}
	off := decodedTools(t, "[tools.ptc]\nexclusive = false\n")
	if off.Tools.PTC.ExclusiveMode() {
		t.Fatal("exclusive = false did not decode")
	}
}

// A save renders every field: a missing renderer line silently drops the
// setting on the next unrelated save, which is how the remote book lost fields.
func TestPTCRoundTripsThroughSave(t *testing.T) {
	c := Default()
	c.Tools.PTC.Enabled = true
	exclusive := false
	c.Tools.PTC.Exclusive = &exclusive
	timeout := 45
	c.Tools.PTC.TimeoutSeconds = &timeout
	c.Tools.PTC.MaxCalls = 11
	c.Tools.PTC.MaxProgramBytes = 2048
	c.Tools.PTC.MaxOutputBytes = 4096
	c.Tools.PTC.MaxParallel = 3

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// The section is user-only, so a user-scope save is the one that must carry it.
	if err := c.SaveToScope(path, RenderScopeUser); err != nil {
		t.Fatalf("SaveToScope: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "[tools.ptc]") {
		t.Fatalf("saved file has no [tools.ptc] section:\n%s", body)
	}
	var raw struct {
		Tools struct {
			PTC PTCConfig `toml:"ptc"`
		} `toml:"tools"`
	}
	if _, err := toml.Decode(string(body), &raw); err != nil {
		t.Fatalf("saved file does not parse: %v", err)
	}
	got := raw.Tools.PTC
	if !got.Enabled || got.ExclusiveMode() || got.Timeout() != 45*time.Second || got.MaxCalls != 11 ||
		got.MaxProgramBytes != 2048 || got.MaxOutputBytes != 4096 || got.MaxParallel != 3 {
		t.Fatalf("round trip = %+v", got)
	}
}

// A project file must not widen the program surface. The decode writes through
// the user's *bool and *int fields in place, so the held grant has to be an
// independent copy: with a shallow one, exclusive = false and a huge timeout
// would look like the user's own values and be accepted silently.
func TestProjectCannotChangePTC(t *testing.T) {
	cfg, _ := loadScoped(t, `
[tools.ptc]
enabled = true
exclusive = true
timeout_seconds = 120
`, `
[tools.ptc]
exclusive = false
timeout_seconds = 99999
`)
	if !cfg.Tools.PTC.Enabled {
		t.Fatal("enabled = false, want the user's true kept")
	}
	if !cfg.Tools.PTC.ExclusiveMode() {
		t.Fatal("exclusive = false, want the user's true kept")
	}
	if got := cfg.Tools.PTC.Timeout(); got != 120*time.Second {
		t.Fatalf("timeout = %v, want the user's 120s", got)
	}
	if !slices.Contains(ignoredKeys(cfg), "tools.ptc") {
		t.Fatalf("ignored = %v, want tools.ptc reported", ignoredKeys(cfg))
	}
	var reported bool
	for _, ig := range cfg.IgnoredProjectSettings() {
		if ig.Key != "tools.ptc" {
			continue
		}
		reported = true
		for _, want := range []string{"exclusive=false", "timeout_seconds=99999"} {
			if !strings.Contains(ig.Value, want) {
				t.Fatalf("ignored value = %q, want the project's actual %s", ig.Value, want)
			}
		}
	}
	if !reported {
		t.Fatal("no tools.ptc ignore record")
	}
}

// The section is user-only, so only the user file may carry it: a project file
// that renders it would write a line LoadForRoot always discards.
func TestPTCSectionIsRenderedForUserScopeOnly(t *testing.T) {
	c := Default()
	c.Tools.PTC.Enabled = true
	user := RenderTOMLForScope(c, RenderScopeUser)
	if !strings.Contains(user, "[tools.ptc]") || !strings.Contains(user, "user file only") {
		t.Fatalf("user render is missing the marked [tools.ptc] section:\n%s", user)
	}
	if project := RenderTOMLForScope(c, RenderScopeProject); strings.Contains(project, "[tools.ptc]") {
		t.Fatalf("project render writes a user-only section:\n%s", project)
	}
}
