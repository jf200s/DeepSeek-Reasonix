package boot

import (
	"fmt"
	"slices"
	"strings"

	"reasonix/internal/contract/tool"
	"reasonix/internal/tools/ptc"
	"reasonix/internal/tools/runcode"
	"reasonix/internal/tools/script"
)

// ptcRunCodeDisabled is the refusal for a PTC session whose one entry point the
// user's own [tools].disabled list removed. That is an explicit contradiction
// between two settings, unlike a tool the composition or the read-only posture
// never admitted: those run without PTC rather than failing the whole build.
func ptcRunCodeDisabled() error {
	return fmt.Errorf("[tools.ptc] is enabled but %s is disabled in [tools].disabled; remove it from that list or turn PTC off", runcode.Name)
}

func (b *builder) ptcRunCodeExcludedByUser() bool {
	return slices.Contains(b.cfg.UserDisabledTools(), runcode.Name)
}

// attachPTC offers run_code when [tools.ptc] asks for it, registering the tool
// and its system-prompt contract before the executor is built. The visible
// surface is narrowed later by narrowPTC, because controller() re-derives it
// from the preset at its end. A registry that excludes run_code (composition,
// read-only posture) simply runs without PTC; only the user's disabled list refuses.
func (b *builder) attachPTC() error {
	// A reused prompt may carry a contract from the build it came from (model
	// switch, provider rebuild, resumed composition). Strip it first, so appending
	// does not grow a copy per build or advertise a tool this build does not offer.
	b.prompt.prompt = stripPTCContract(b.prompt.prompt)

	cfg := b.cfg.Tools.PTC
	if !cfg.Enabled || b.tools.reg == nil {
		return nil
	}
	// A disabled run_code makes PTC impossible before the registry is asked: the
	// merged list includes project removals and DisableTools drops it later. Only
	// the user's own list is an explicit contradiction worth failing the build.
	if slices.Contains(b.cfg.DisabledTools(), runcode.Name) {
		if b.ptcRunCodeExcludedByUser() {
			return ptcRunCodeDisabled()
		}
		return nil
	}
	var pinned map[string]bool
	if b.opts.Observe == nil {
		pinned = pinnedMCPServers(b.tools.mcp.alwaysLoad, b.tools.mcpSchemaKnown)
	}
	tools := ptcTools(b.tools.reg, b.cfg.DisabledTools(), alwaysLoadedMCPTools(b.tools.reg, pinned))
	var rc tool.Tool = runcode.New(tools, ptc.Limits{
		Timeout:         cfg.Timeout(),
		MaxCalls:        cfg.MaxCalls,
		MaxProgramBytes: cfg.MaxProgramBytes,
		MaxOutputBytes:  cfg.MaxOutputBytes,
		MaxParallel:     cfg.MaxParallel,
	})
	if cfg.ExclusiveMode() {
		// run_code is the only tool the model sees here, so the plan gate must
		// admit it for research to happen at all; every call the program makes
		// is checked again on its own and writers stay refused while planning.
		rc = planSafeRunCode{Tool: rc}
	}
	b.tools.reg.Add(rc)
	if _, ok := b.tools.reg.Get(runcode.Name); !ok {
		if b.ptcRunCodeExcludedByUser() {
			return ptcRunCodeDisabled()
		}
		return nil
	}
	// The contract is part of the system prompt, not a per-turn message, so it
	// is byte-stable for the whole session and does not churn the prompt cache.
	b.prompt.prompt = appendPTCContract(b.prompt.prompt, ptc.Contract(tools))
	return nil
}

// narrowPTC runs after controller(), the last place the unified preset surface
// is applied, and makes run_code visible again where that surface hid it (the
// only tool in exclusive mode). A session that never admitted run_code skips
// narrowing; an explicit [tools].disabled entry is refused instead.
func (b *builder) narrowPTC() error {
	cfg := b.cfg.Tools.PTC
	if !cfg.Enabled || b.tools.reg == nil {
		return nil
	}
	if _, ok := b.tools.reg.Get(runcode.Name); !ok {
		if b.ptcRunCodeExcludedByUser() {
			return ptcRunCodeDisabled()
		}
		return nil
	}
	if cfg.ExclusiveMode() {
		b.tools.reg.SetProviderVisibleToolsExclusive([]string{runcode.Name})
	} else {
		b.tools.reg.AddProviderVisibleTools(runcode.Name)
	}
	return nil
}

// planSafeRunCode declares run_code admissible during Plan mode. Exclusive
// mode leaves it the model's only tool, and its nested calls are phase-checked
// one by one, so a writer inside the program is still refused.
type planSafeRunCode struct{ tool.Tool }

func (planSafeRunCode) PlanModeSafe() bool { return true }

// ptcTools collects what a program may call: every registered tool except
// run_code and run_script, whose nested calls are refused anyway. Advertising
// a tool a program can never call would invite the model to try it, so the
// user's disabled names are skipped. MCP tools follow the provider surface:
// only an always-loaded, authorized server's tools are named.
func ptcTools(reg *tool.Registry, disabled, mcpVisible []string) []ptc.Tool {
	mcp := make(map[string]bool, len(mcpVisible))
	for _, name := range mcpVisible {
		mcp[name] = true
	}
	names := reg.AllNames()
	out := make([]ptc.Tool, 0, len(names))
	for _, name := range names {
		if name == runcode.Name || name == script.Name || slices.Contains(disabled, name) {
			continue
		}
		if strings.HasPrefix(name, tool.MCPNamePrefix) && !mcp[name] {
			continue
		}
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		_, proxy := t.(tool.CallResolver)
		out = append(out, ptc.Tool{
			Name:           name,
			Description:    t.Description(),
			Schema:         t.Schema(),
			ReadOnly:       t.ReadOnly(),
			ParallelUnsafe: proxy,
		})
	}
	return out
}

// The contract lives between these markers so a prompt that already carries
// one can be cleaned before it is reused or told anew.
const (
	ptcContractStart = "<!-- ptc-contract:start -->"
	ptcContractEnd   = "<!-- ptc-contract:end -->"
)

// stripPTCContract removes every contract block, including a malformed one
// whose end marker is missing (a truncated prompt cannot be repaired in place).
func stripPTCContract(prompt string) string {
	for {
		start := strings.Index(prompt, ptcContractStart)
		if start < 0 {
			return prompt
		}
		end := strings.Index(prompt[start:], ptcContractEnd)
		if end < 0 {
			return strings.TrimRight(prompt[:start], "\n")
		}
		// The block is appended at the end of the prompt with a blank line
		// before it; drop that separator with the block.
		prompt = strings.TrimRight(prompt[:start], "\n") + prompt[start+end+len(ptcContractEnd):]
	}
}

func appendPTCContract(prompt, contract string) string {
	prompt = stripPTCContract(prompt)
	if strings.TrimSpace(contract) == "" {
		return prompt
	}
	return strings.TrimRight(prompt, "\n") + "\n\n" + ptcContractStart + "\n" + contract + "\n" + ptcContractEnd
}
