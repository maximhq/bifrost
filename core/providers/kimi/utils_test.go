package kimi

import "testing"

func TestDeriveAnthropicBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		openAIBase   string
		wantBase     string
		wantMessages string // full messages URL
	}{
		{
			name:         "Open Platform international",
			openAIBase:   "https://api.moonshot.ai/v1",
			wantBase:     "https://api.moonshot.ai/anthropic",
			wantMessages: "https://api.moonshot.ai/anthropic/v1/messages",
		},
		{
			name:         "Open Platform China",
			openAIBase:   "https://api.moonshot.cn/v1",
			wantBase:     "https://api.moonshot.cn/anthropic",
			wantMessages: "https://api.moonshot.cn/anthropic/v1/messages",
		},
		{
			name:         "Kimi Code subscription mount shares the OpenAI base",
			openAIBase:   "https://api.kimi.com/coding/v1",
			wantBase:     "https://api.kimi.com/coding/v1",
			wantMessages: "https://api.kimi.com/coding/v1/messages",
		},
		{
			name:         "trailing slash is trimmed",
			openAIBase:   "https://api.moonshot.ai/v1/",
			wantBase:     "https://api.moonshot.ai/anthropic",
			wantMessages: "https://api.moonshot.ai/anthropic/v1/messages",
		},
		{
			name:         "custom base without /v1 falls back to appending /anthropic",
			openAIBase:   "https://proxy.example.com/kimi",
			wantBase:     "https://proxy.example.com/kimi/anthropic",
			wantMessages: "https://proxy.example.com/kimi/anthropic/v1/messages",
		},
		{
			// Suffix semantics only hold on Kimi's own hosts: a custom host
			// ending in /v1 must NOT have it rewritten away.
			name:         "custom base ending in /v1 keeps its path",
			openAIBase:   "https://proxy.example.com/v1",
			wantBase:     "https://proxy.example.com/v1/anthropic",
			wantMessages: "https://proxy.example.com/v1/anthropic/v1/messages",
		},
		{
			// Likewise for the Kimi Code suffix shape on a foreign host: the
			// base is not assumed to serve the Anthropic mount at its root.
			name:         "custom base ending in /coding/v1 keeps its path",
			openAIBase:   "https://proxy.example.com/coding/v1",
			wantBase:     "https://proxy.example.com/coding/v1/anthropic",
			wantMessages: "https://proxy.example.com/coding/v1/anthropic/v1/messages",
		},
		{
			// Scheme-less values must take the custom-host fallback even when
			// the remainder parses to a known Kimi host.
			name:         "scheme-less base takes the custom-host fallback",
			openAIBase:   "//api.kimi.com/v1",
			wantBase:     "//api.kimi.com/v1/anthropic",
			wantMessages: "//api.kimi.com/v1/anthropic/v1/messages",
		},
		{
			// use_anthropic_endpoints with a base_url already set to the mount
			// itself must not append /anthropic a second time.
			name:         "Open Platform Anthropic mount as base is idempotent",
			openAIBase:   "https://api.moonshot.ai/anthropic",
			wantBase:     "https://api.moonshot.ai/anthropic",
			wantMessages: "https://api.moonshot.ai/anthropic/v1/messages",
		},
		{
			name:         "custom base already ending in /anthropic keeps its path",
			openAIBase:   "https://proxy.example.com/anthropic",
			wantBase:     "https://proxy.example.com/anthropic",
			wantMessages: "https://proxy.example.com/anthropic/v1/messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := deriveAnthropicBaseURL(tt.openAIBase)
			if base != tt.wantBase {
				t.Errorf("deriveAnthropicBaseURL(%q) = %q, want %q", tt.openAIBase, base, tt.wantBase)
			}
			full := base + anthropicMessagesPathFor(base)
			if full != tt.wantMessages {
				t.Errorf("messages URL for %q = %q, want %q", tt.openAIBase, full, tt.wantMessages)
			}
		})
	}
}
