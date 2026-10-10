package boot

import "strings"

// SideChatParent names the conversation a read-only companion was opened from.
// Its facts are split by how stable they are: the owner's identity enters the
// companion's cache-stable system prompt, while what the owner is currently
// doing rides the session-context snapshot, which is host-authored and replaced
// per turn. Nothing here may mutate the owner's own prefix or tool schema.
type SideChatParent struct {
	Title     string
	SessionID string
	Goal      string
	Context   string
}

// appendSideChatCompanionPolicy adds the stable half: which conversation this
// companion belongs to, and the boundary that comes with it. It is applied once
// per session, so the prefix stays byte-stable across turns.
func appendSideChatCompanionPolicy(sysPrompt string, parent *SideChatParent) string {
	title, sessionID := sideChatParentIdentity(parent)
	if title == "" && sessionID == "" {
		return sysPrompt
	}
	var b strings.Builder
	b.WriteString("# Side conversation\n\n")
	b.WriteString("This session is the read-only companion of the conversation ")
	b.WriteString(describeSideChatParent(title, sessionID))
	b.WriteString(". It shares that conversation's working directory and standing instructions, ")
	b.WriteString("but not its transcript: the user asks you separately, so treat only what they quote or ")
	b.WriteString("describe as context. You cannot change the workspace; when a change is needed, say so ")
	b.WriteString("and let the user make it in the conversation you belong to.")
	return sysPrompt + "\n\n" + b.String()
}

// sideChatCompanionContextBlock is the volatile half. It is rendered into the
// session-context snapshot rather than the system prompt, so a changing owner
// goal never rewrites the cacheable prefix.
func sideChatCompanionContextBlock(parent *SideChatParent) string {
	if parent == nil {
		return ""
	}
	goal, context := strings.TrimSpace(parent.Goal), strings.TrimSpace(parent.Context)
	if goal == "" && context == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nCompanion session: you are the read-only companion of the conversation ")
	b.WriteString(describeSideChatParent(sideChatParentIdentity(parent)))
	b.WriteString(".")
	if goal != "" {
		b.WriteString("\nThat conversation's current goal: " + goal)
	}
	if context != "" {
		b.WriteString("\nThat conversation's recent activity:\n" + context)
	}
	return b.String()
}

func sideChatParentIdentity(parent *SideChatParent) (string, string) {
	if parent == nil {
		return "", ""
	}
	return strings.TrimSpace(parent.Title), strings.TrimSpace(parent.SessionID)
}

// describeSideChatParent renders "<title>" (session <id>) with whichever half is
// known, so a companion still names its owner when only one was available.
func describeSideChatParent(title, sessionID string) string {
	switch {
	case title != "" && sessionID != "":
		return `"` + title + `" (session ` + sessionID + `)`
	case title != "":
		return `"` + title + `"`
	case sessionID != "":
		return "session " + sessionID
	default:
		return "an unnamed conversation"
	}
}
