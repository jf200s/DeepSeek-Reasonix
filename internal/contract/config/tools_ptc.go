package config

import (
	"fmt"
	"time"
)

// PTCConfig configures programmatic tool calling (the run_code tool). The zero
// value is off, so a configuration that never asked for it keeps the ordinary
// one-tool-per-call surface.
type PTCConfig struct {
	// Enabled offers run_code. Off by default: a program is a second way to
	// spend the same permissions, and it should be chosen, not inherited.
	Enabled bool `toml:"enabled"`
	// Exclusive hides every other tool from the model, so the only way to call
	// one is from inside a program. Nil means the default, true.
	Exclusive *bool `toml:"exclusive"`
	// TimeoutSeconds bounds one program; 0 takes the default, and the kernel
	// caps it.
	TimeoutSeconds *int `toml:"timeout_seconds"`
	// MaxCalls bounds host tool calls in one program; 0 takes the default.
	MaxCalls int `toml:"max_calls"`
	// MaxProgramBytes bounds the program source; 0 takes the default.
	MaxProgramBytes int `toml:"max_program_bytes"`
	// MaxOutputBytes bounds what the program may send back to the model; 0
	// takes the default.
	MaxOutputBytes int `toml:"max_output_bytes"`
	// MaxParallel bounds tools.parallel; 0 takes the default, and the kernel
	// caps it.
	MaxParallel int `toml:"max_parallel"`
}

// clonePTCConfig copies c so the two share no pointers. BurntSushi/toml
// decodes a project reasonix.toml onto the values the user's decode left, and
// a non-nil *bool or *int is written through in place; heldScope must keep the
// user's grant independent of that write or a project could narrow it silently.
func clonePTCConfig(c PTCConfig) PTCConfig {
	out := c
	if c.Exclusive != nil {
		v := *c.Exclusive
		out.Exclusive = &v
	}
	if c.TimeoutSeconds != nil {
		v := *c.TimeoutSeconds
		out.TimeoutSeconds = &v
	}
	return out
}

// describePTCConfig names the effective values a project file set, so the
// ignored-value warning shows what was refused rather than a placeholder.
func describePTCConfig(c PTCConfig) string {
	return fmt.Sprintf("enabled=%t exclusive=%t timeout_seconds=%d max_calls=%d max_program_bytes=%d max_output_bytes=%d max_parallel=%d",
		c.Enabled, c.ExclusiveMode(), c.TimeoutSecondsValue(), c.MaxCalls, c.MaxProgramBytes, c.MaxOutputBytes, c.MaxParallel)
}

// ExclusiveMode reports whether run_code is the only tool the model sees.
func (c PTCConfig) ExclusiveMode() bool {
	if c.Exclusive == nil {
		return true
	}
	return *c.Exclusive
}

// Timeout is how long one program may run. A zero value asks for the default
// rather than "no limit": an unbounded program would hold the turn open.
func (c PTCConfig) Timeout() time.Duration {
	if c.TimeoutSeconds == nil || *c.TimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(*c.TimeoutSeconds) * time.Second
}

// TimeoutSecondsValue is the configured value for rendering, with the default
// filled in so the sample file never shows a misleading 0.
func (c PTCConfig) TimeoutSecondsValue() int {
	if c.TimeoutSeconds == nil || *c.TimeoutSeconds <= 0 {
		return 120
	}
	return *c.TimeoutSeconds
}
