package mcp

import (
	"strings"
	"unicode"
)

// parseToolName parses the tool name to be JavaScript-compatible.
// It converts spaces to hyphens, removes invalid characters, and ensures
// the name starts with a valid JavaScript identifier character.
func parseToolName(toolName string) string {
	if toolName == "" {
		return ""
	}

	var result strings.Builder
	runes := []rune(toolName)

	// Process first character - must be letter, underscore, or dollar sign
	if len(runes) > 0 {
		first := runes[0]
		if unicode.IsLetter(first) || first == '_' || first == '$' {
			result.WriteRune(unicode.ToLower(first))
		} else {
			// If first char is invalid, prefix with underscore
			result.WriteRune('_')
			if unicode.IsDigit(first) {
				result.WriteRune(first)
			}
		}
	}

	// Process remaining characters
	for i := 1; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' {
			result.WriteRune(unicode.ToLower(r))
		} else if unicode.IsSpace(r) || r == '-' {
			// Replace spaces and existing hyphens with single hyphen
			// Avoid consecutive hyphens
			if result.Len() > 0 && result.String()[result.Len()-1] != '-' {
				result.WriteRune('-')
			}
		}
		// Skip other invalid characters
	}

	parsed := result.String()

	// Remove trailing hyphens
	parsed = strings.TrimRight(parsed, "-")

	// Ensure we have at least one character
	// Should never happen, but just in case
	if parsed == "" {
		return "tool"
	}

	return parsed
}
