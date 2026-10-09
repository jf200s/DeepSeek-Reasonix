package boot

import (
	"reasonix/internal/agent"
	"reasonix/internal/tool"
)

// readOnlySessionRegistry narrows reg to the read-only session tool set when
// readOnly, keeping research tools plus the read-only shell wrapper and dropping
// writer, workflow and meta tools. It returns reg untouched otherwise.
func readOnlySessionRegistry(reg *tool.Registry, readOnly bool) *tool.Registry {
	if !readOnly {
		return reg
	}
	return agent.ReadOnlySubagentToolRegistry(reg, nil)
}
