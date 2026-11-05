package ptc

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// contractBudget is the prompt budget for the rendered contract. It is a
// character count: the text is ASCII apart from any tool names or descriptions,
// so bytes and runes agree closely enough for a stable degradation decision.
const contractBudget = 24_000

const contractHeader = "Run a synchronous JavaScript function body (plain JavaScript, no TypeScript, no imports, no `await`).\n" +
	"Only log(...) / console.log(...) output and the returned value enter the conversation.\n" +
	"Every tool call here is permission-checked, approved and observed exactly like a direct call.\n" +
	"tools.parallel(calls, concurrency?) runs read-only tools concurrently; call writers and proxy tools sequentially.\n" +
	"Promise callbacks run before the program returns, after its synchronous body finishes.\n" +
	"\n" +
	"tools:\n"

const contractHelpers = "tools.tryCall(name, args?) -> {ok: boolean, output: string, error: string}\n" +
	"tools.parallel(calls, concurrency?) -> {ok: boolean, output: string, error: string}[]\n"

// Contract renders the deterministic tool API shown to the model: sorted names
// and fixed parts, so the same tool set always produces the same bytes.
//
// When the full text does not fit the budget it degrades to signatures, then
// required names, then names only; trailing tools are omitted as a last resort.
func Contract(tools []Tool) string {
	sorted := contractTools(tools)
	for level := levelFull; level <= levelNames; level++ {
		if s := renderContract(sorted, level); len(s) <= contractBudget {
			return s
		}
	}
	return renderTruncated(sorted)
}

type contractLevel int

const (
	levelFull contractLevel = iota
	levelSignatures
	levelRequired
	levelNames
)

// contractTools copies tools, drops blank and duplicate names (first wins) and
// sorts by name.
func contractTools(tools []Tool) []Tool {
	seen := make(map[string]bool, len(tools))
	kept := make([]Tool, 0, len(tools))
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		t.Name = name
		kept = append(kept, t)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	return kept
}

func renderContract(tools []Tool, level contractLevel) string {
	var b strings.Builder
	b.WriteString(contractHeader)
	for _, t := range tools {
		b.WriteString(toolLine(t, level))
		b.WriteByte('\n')
	}
	b.WriteString(contractHelpers)
	return b.String()
}

func toolLine(t Tool, level contractLevel) string {
	ref := toolRef(t.Name)
	if level == levelNames {
		return ref
	}
	if level == levelRequired {
		return ref + "(" + requiredNames(t.Schema) + ") -> string"
	}
	line := ref + "(" + signature(t.Schema) + ") -> string"
	if level == levelFull {
		if desc := firstSentence(t.Description); desc != "" {
			line += " // " + desc
		}
	}
	return line
}

// toolRef renders tools.name for a JS identifier and tools["name"] otherwise,
// so a tool name that is not an identifier still produces valid syntax.
func toolRef(name string) string {
	if isIdentifier(name) {
		return "tools." + name
	}
	quoted, err := json.Marshal(name)
	if err != nil {
		return "tools." + name
	}
	return "tools[" + string(quoted) + "]"
}

func isIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

type toolSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func parseSchema(raw json.RawMessage) toolSchema {
	var s toolSchema
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return toolSchema{}
	}
	return s
}

// orderedProperties lists required properties first, each group alphabetical,
// so the signature is deterministic regardless of map iteration order.
func orderedProperties(s toolSchema) []string {
	required := make(map[string]bool, len(s.Required))
	for _, name := range s.Required {
		required[name] = true
	}
	req := make([]string, 0, len(s.Required))
	opt := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		if required[name] {
			req = append(req, name)
			continue
		}
		opt = append(opt, name)
	}
	sort.Strings(req)
	sort.Strings(opt)
	return append(req, opt...)
}

func signature(raw json.RawMessage) string {
	s := parseSchema(raw)
	if len(s.Properties) == 0 {
		return ""
	}
	required := make(map[string]bool, len(s.Required))
	for _, name := range s.Required {
		required[name] = true
	}
	parts := make([]string, 0, len(s.Properties))
	for _, name := range orderedProperties(s) {
		part := name + ": " + typeName(s.Properties[name], 1)
		if !required[name] {
			part = name + "?: " + typeName(s.Properties[name], 1)
		}
		parts = append(parts, part)
	}
	return "{" + strings.Join(parts, "; ") + "}"
}

func requiredNames(raw json.RawMessage) string {
	s := parseSchema(raw)
	var names []string
	for _, name := range s.Required {
		if _, ok := s.Properties[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// typeName renders a JSON Schema type in the model-facing signature language.
// Depth is capped at 2: anything deeper is "unknown".
func typeName(raw json.RawMessage, depth int) string {
	if depth > 2 || len(raw) == 0 {
		return "unknown"
	}
	var node struct {
		Type  json.RawMessage   `json:"type"`
		Enum  []json.RawMessage `json:"enum"`
		Items json.RawMessage   `json:"items"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return "unknown"
	}
	if len(node.Enum) > 0 {
		return enumName(node.Enum)
	}
	switch scalarType(node.Type) {
	case "array":
		if len(node.Items) == 0 {
			return "unknown[]"
		}
		return typeName(node.Items, depth+1) + "[]"
	case "string", "number", "integer", "boolean", "object":
		return scalarType(node.Type)
	default:
		return "unknown"
	}
}

func scalarType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return strings.Join(many, " | ")
	}
	return ""
}

func enumName(values []json.RawMessage) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, string(v))
	}
	return strings.Join(parts, " | ")
}

// firstSentence returns the first sentence of desc on one line, or "".
func firstSentence(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return ""
	}
	cut := len(desc)
	for i := 0; i < len(desc); i++ {
		if desc[i] == '\n' {
			cut = i
			break
		}
		if desc[i] == '.' && (i+1 == len(desc) || desc[i+1] == ' ' || desc[i+1] == '\n') {
			cut = i + 1
			break
		}
	}
	return strings.Join(strings.Fields(desc[:cut]), " ")
}

// renderTruncated is the last resort: names only, dropping trailing tools and
// naming how many were left out, so the budget is never exceeded.
func renderTruncated(tools []Tool) string {
	lines := make([]string, len(tools))
	for i, t := range tools {
		lines[i] = toolRef(t.Name) + "\n"
	}
	for n := len(lines); n >= 0; n-- {
		var b strings.Builder
		b.WriteString(contractHeader)
		for _, line := range lines[:n] {
			b.WriteString(line)
		}
		if omitted := len(lines) - n; omitted > 0 {
			fmt.Fprintf(&b, "// ... %d more tool(s) omitted ...\n", omitted)
		}
		b.WriteString(contractHelpers)
		if b.Len() <= contractBudget {
			return b.String()
		}
	}
	return contractHeader + contractHelpers
}
