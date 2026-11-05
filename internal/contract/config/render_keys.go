package config

import (
	"strconv"
	"strings"
)

func renderTOMLKeyPart(key string) string {
	if isBareTOMLKey(key) {
		return key
	}
	return strconv.Quote(key)
}

func renderTOMLTablePath(parts ...string) string {
	rendered := make([]string, 0, len(parts))
	for _, part := range parts {
		rendered = append(rendered, renderTOMLKeyPart(part))
	}
	return strings.Join(rendered, ".")
}

func isBareTOMLKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
