package integrations

import (
	"testing"
)

func TestIsCLIToken_ExactMatch(t *testing.T) {
	tests := []struct {
		ua    string
		token string
		want  bool
	}{
		{"cursor/1.2.3", "cursor", true},
		{"codex/0.1.2501.0", "codex", true},
		{"n8n/1.60.1", "n8n", true},
	}
	for _, tt := range tests {
		if got := isCLIToken(tt.ua, tt.token); got != tt.want {
			t.Errorf("isCLIToken(%q, %q) = %v, want %v", tt.ua, tt.token, got, tt.want)
		}
	}
}

func TestIsCLIToken_AfterSeparator(t *testing.T) {
	tests := []struct {
		ua    string
		token string
		want  bool
	}{
		{"Mozilla/5.0 cursor/1.0", "cursor", true},
		{"some-app/1.0 codex/2.0", "codex", true},
		{"my-agent/1.0 n8n/1.60", "n8n", true},
	}
	for _, tt := range tests {
		if got := isCLIToken(tt.ua, tt.token); got != tt.want {
			t.Errorf("isCLIToken(%q, %q) = %v, want %v", tt.ua, tt.token, got, tt.want)
		}
	}
}

// TestIsCLIToken_FalsePositivePrevention verifies the loop-based fix (coderabbitai review):
// when the token appears as a substring first and then standalone, it must still return true.
func TestIsCLIToken_FalsePositivePrevention(t *testing.T) {
	tests := []struct {
		ua    string
		token string
		want  bool
	}{
		// False positives - must NOT match
		{"precursor/2.0", "cursor", false},
		{"my-cursor-editor/1.0", "cursor", false},
		{"agent-cursor/2.0", "cursor", false},
		{"vs-codex-plugin/1.0", "codex", false},
		{"somecodex/1.0", "codex", false},
		{"plugin-n8n/1.0", "n8n", false},
		// Key regression from coderabbitai: token appears after an invalid match first
		{"precursor/2.0 cursor/1.0", "cursor", true},
		{"vs-codex-plugin/1.0 codex/0.1", "codex", true},
		{"plugin-n8n/1.0 n8n/2.0", "n8n", true},
	}
	for _, tt := range tests {
		if got := isCLIToken(tt.ua, tt.token); got != tt.want {
			t.Errorf("isCLIToken(%q, %q) = %v, want %v", tt.ua, tt.token, got, tt.want)
		}
	}
}

func TestIsCLIToken_CaseInsensitive(t *testing.T) {
	// DetectCLIUserAgent lowercases before calling isCLIToken
	if !isCLIToken("cursor/1.0", "cursor") {
		t.Error("isCLIToken should match lowercase cursor")
	}
}

func TestIsCLIToken_NotFound(t *testing.T) {
	if isCLIToken("python-requests/2.28.0", "cursor") {
		t.Error("isCLIToken should not match when token is absent")
	}
}

func TestDetectCLIUserAgent_KnownAgents(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		want      string
	}{
		{"claude-cli exact", "claude-cli/1.0.8", "claude-cli"},
		{"claude-cli with extra info", "claude-cli/1.0.8 (linux; amd64)", "claude-cli"},
		{"gemini-cli", "gemini-cli/0.1.12", "gemini-cli"},
		{"qwen-cli", "qwen-cli/2.0", "qwen-cli"},
		{"cursor", "cursor/1.2.3", "cursor"},
		{"cursor uppercase", "Cursor/1.2.3", "cursor"},
		{"codex", "codex/0.1.2501.0", "codex"},
		{"n8n", "n8n/1.60.1", "n8n"},
		{"n8n with extra", "n8n/1.60.1 (linux)", "n8n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectCLIUserAgentFromString(tt.userAgent)
			if got != tt.want {
				t.Errorf("detectCLIUserAgentFromString(%q) = %q, want %q", tt.userAgent, got, tt.want)
			}
		})
	}
}

func TestDetectCLIUserAgent_FalsePositives(t *testing.T) {
	falseUAs := []string{
		"precursor/2.0",
		"my-cursor-editor/1.0",
		"agent-cursor/2.0",
		"vs-codex-plugin/1.0",
		"somecodex/1.0",
		"plugin-n8n/1.0",
		"python-requests/2.28.0",
		"Go-http-client/1.1",
		"",
	}
	for _, ua := range falseUAs {
		t.Run(ua, func(t *testing.T) {
			got := detectCLIUserAgentFromString(ua)
			if got != "" {
				t.Errorf("detectCLIUserAgentFromString(%q) = %q, want empty string (no false positive)", ua, got)
			}
		})
	}
}

func TestDetectCLIUserAgent_Priority(t *testing.T) {
	// claude-cli takes priority over cursor check since it's checked first
	got := detectCLIUserAgentFromString("claude-cli/1.0 cursor/1.2")
	if got != "claude-cli" {
		t.Errorf("expected claude-cli to take priority, got %q", got)
	}
}

// detectCLIUserAgentFromString is a test helper that runs the same switch logic
// as DetectCLIUserAgent but accepts a plain string (no fasthttp dependency in tests).
func detectCLIUserAgentFromString(userAgent string) string {
	ua := lowerString(userAgent)
	switch {
	case contains(ua, "claude-cli"):
		return "claude-cli"
	case contains(ua, "gemini-cli"):
		return "gemini-cli"
	case contains(ua, "qwen-cli"):
		return "qwen-cli"
	case isCLIToken(ua, "cursor"):
		return "cursor"
	case isCLIToken(ua, "codex"):
		return "codex"
	case isCLIToken(ua, "n8n"):
		return "n8n"
	}
	return ""
}

func lowerString(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}()
}
