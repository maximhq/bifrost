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
		{name: "claude desktop electron", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Claude/1.11187.1 Chrome/146.0.7680.216 Electron/41.6.1 Safari/537.36", want: "Claude Desktop"},
		{name: "fasthttp api", userAgent: "fasthttp", want: "API"},
		{name: "codex cli", userAgent: "codex-cli/0.1.0", want: "Codex CLI"},
		{name: "codex tui", userAgent: "codex-tui/0.1.0", want: "Codex CLI"},
		{name: "codex tui terminal capture", userAgent: "codex-tui/0.137.0 (Mac OS 14.1.0; arm64) iTerm.app/3.6.6 (codex-tui; 0.137.0)", want: "Codex CLI"},
		{name: "claude cowork runtime capture", userAgent: "claude-cli/2.1.170 (external, local-agent, agent-sdk/0.3.170)", want: "Claude Code"},
		{name: "codex desktop mac", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Codex/1.0 Electron/41.0", want: "Codex Desktop"},
		{name: "codex desktop native", userAgent: "Codex Desktop/0.142.2 (Mac OS 14.1.0; arm64) unknown (Codex Desktop; 26.623.30605)", want: "Codex Desktop"},
		{name: "codex desktop windows", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Codex/1.0 Electron/41.0", want: "Codex Desktop"},
		{name: "codex desktop linux", userAgent: "Mozilla/5.0 (X11; Linux x86_64) Codex/1.0 Electron/41.0", want: "Codex Desktop"},
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

func TestAppKeyFromName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "claude code", in: "Claude Code", want: "claude-code"},
		{name: "custom app", in: " Internal Claude Wrapper ", want: "internal-claude-wrapper"},
		{name: "collapsed spaces", in: "Gemini   CLI", want: "gemini-cli"},
		{name: "other ignored", in: UserAgentAppOther, want: ""},
		{name: "empty ignored", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AppKeyFromName(tt.in); got != tt.want {
				t.Fatalf("AppKeyFromName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatchUserAgent(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		pattern   string
		matchType UserAgentMappingMatchType
		want      bool
	}{
		{name: "contains", userAgent: "claude-cli/2.1.168 (external, cli)", pattern: "CLI/2.1", matchType: UserAgentMappingMatchTypeContains, want: true},
		{name: "starts with", userAgent: "claude-cli/2.1.168", pattern: "Claude-CLI", matchType: UserAgentMappingMatchTypeStartsWith, want: true},
		{name: "exact", userAgent: "Cursor/1.0", pattern: "cursor/1.0", matchType: UserAgentMappingMatchTypeExact, want: true},
		{name: "regex", userAgent: "custom-client/42", pattern: `custom-client/\d+`, matchType: UserAgentMappingMatchTypeRegex, want: true},
		{name: "invalid regex", userAgent: "custom-client/42", pattern: `[`, matchType: UserAgentMappingMatchTypeRegex, want: false},
		{name: "unknown match type", userAgent: "custom-client/42", pattern: "custom", matchType: UserAgentMappingMatchType("bad"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchUserAgent(tt.userAgent, tt.pattern, tt.matchType); got != tt.want {
				t.Fatalf("MatchUserAgent(%q, %q, %q) = %v, want %v", tt.userAgent, tt.pattern, tt.matchType, got, tt.want)
			}
		})
	}
}
