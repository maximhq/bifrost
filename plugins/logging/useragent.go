package logging

import "strings"

// userAgentPattern maps a substring pattern to a classified label.
type userAgentPattern struct {
	pattern string
	label   string
}

// knownUserAgents is the list of known AI coding tool user-agent patterns.
// Order matters: first match wins. More specific patterns should come first.
var knownUserAgents = []userAgentPattern{
	{pattern: "claude-code", label: "claude-code"},
	{pattern: "Claude Code", label: "claude-code"},
	{pattern: "ClaudeCode", label: "claude-code"},
	{pattern: "codex-cli", label: "codex"},
	{pattern: "Codex CLI", label: "codex"},
	{pattern: "codex", label: "codex"},
	{pattern: "opencode", label: "opencode"},
	{pattern: "cursor", label: "cursor"},
	{pattern: "Cursor", label: "cursor"},
	{pattern: "windsurf", label: "windsurf"},
	{pattern: "Windsurf", label: "windsurf"},
	{pattern: "aider", label: "aider"},
	{pattern: "continue", label: "continue"},
	{pattern: "Continue", label: "continue"},
	{pattern: "cline", label: "cline"},
	{pattern: "Cline", label: "cline"},
	{pattern: "copilot", label: "copilot"},
	{pattern: "GitHub Copilot", label: "copilot"},
}

// ClassifyUserAgent classifies a raw User-Agent string into a known label.
// Returns the classified label, or "custom" for unrecognized agents.
// Returns empty string if the user-agent is empty.
func ClassifyUserAgent(ua string) string {
	if ua == "" {
		return ""
	}
	for _, p := range knownUserAgents {
		if strings.Contains(ua, p.pattern) {
			return p.label
		}
	}
	return "custom"
}
