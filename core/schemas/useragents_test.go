package schemas

import "testing"

func TestDetectAppFromUserAgent(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		want      string
	}{
		{name: "claude cli versioned", userAgent: "claude-cli/2.1.168 (external, cli)", want: "Claude Code"},
		{name: "claude code contains", userAgent: "external claude-code/1.0", want: "Claude Code"},
		{name: "claude desktop", userAgent: "claude-desktop/1.2.3", want: "Claude Desktop"},
		{name: "codex cli", userAgent: "codex-cli/0.1.0", want: "Codex"},
		{name: "codex tui", userAgent: "codex-tui/0.1.0", want: "Codex"},
		{name: "cursor", userAgent: "Cursor/0.47", want: "Cursor"},
		{name: "gemini", userAgent: "gemini-cli/1.0", want: "Gemini CLI"},
		{name: "qwen", userAgent: "qwen-code/1.0", want: "Qwen Code"},
		{name: "opencode", userAgent: "opencode/1.0", want: "OpenCode"},
		{name: "windsurf", userAgent: "Windsurf/1.0", want: "Windsurf"},
		{name: "kilo before cline", userAgent: "kilo-cline/1.0", want: "Kilo Code"},
		{name: "roo before cline", userAgent: "roo-cline/1.0", want: "Roo Code"},
		{name: "cline", userAgent: "cline/3.0.0", want: "Cline"},
		{name: "unknown", userAgent: "custom-client/1.0", want: "Other"},
		{name: "empty", userAgent: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectAppFromUserAgent(tt.userAgent); got != tt.want {
				t.Fatalf("DetectAppFromUserAgent(%q) = %q, want %q", tt.userAgent, got, tt.want)
			}
		})
	}
}
