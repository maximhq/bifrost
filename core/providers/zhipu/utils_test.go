package zhipu

import "testing"

func TestDeriveAnthropicBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		openAIBase   string
		wantMessages string // full messages URL
	}{
		{
			name:         "General API international",
			openAIBase:   "https://api.z.ai/api/paas/v4",
			wantMessages: "https://api.z.ai/api/anthropic/v1/messages",
		},
		{
			name:         "General API China",
			openAIBase:   "https://open.bigmodel.cn/api/paas/v4",
			wantMessages: "https://open.bigmodel.cn/api/anthropic/v1/messages",
		},
		{
			name:         "Coding Plan international",
			openAIBase:   "https://api.z.ai/api/coding/paas/v4",
			wantMessages: "https://api.z.ai/api/anthropic/v1/messages",
		},
		{
			name:         "Coding Plan China",
			openAIBase:   "https://open.bigmodel.cn/api/coding/paas/v4",
			wantMessages: "https://open.bigmodel.cn/api/anthropic/v1/messages",
		},
		{
			name:         "trailing slash is trimmed",
			openAIBase:   "https://api.z.ai/api/paas/v4/",
			wantMessages: "https://api.z.ai/api/anthropic/v1/messages",
		},
		{
			name:         "custom base falls back to appending /anthropic",
			openAIBase:   "https://proxy.example.com/zhipu",
			wantMessages: "https://proxy.example.com/zhipu/anthropic/v1/messages",
		},
		{
			// Suffix semantics only hold on Zhipu's own hosts: a custom host
			// ending in a recognized suffix must NOT have it rewritten away.
			name:         "custom base ending in /coding/paas/v4 keeps its path",
			openAIBase:   "https://proxy.example.com/api/coding/paas/v4",
			wantMessages: "https://proxy.example.com/api/coding/paas/v4/anthropic/v1/messages",
		},
		{
			name:         "custom base ending in /paas/v4 keeps its path",
			openAIBase:   "https://proxy.example.com/api/paas/v4",
			wantMessages: "https://proxy.example.com/api/paas/v4/anthropic/v1/messages",
		},
		{
			// The known-host gate is exact-match: a host whose name merely
			// contains api.z.ai is a foreign host.
			name:         "lookalike host containing api.z.ai keeps its path",
			openAIBase:   "https://api.z.ai.proxy.example.com/api/paas/v4",
			wantMessages: "https://api.z.ai.proxy.example.com/api/paas/v4/anthropic/v1/messages",
		},
		{
			// Scheme-less values must take the custom-host fallback even when
			// the remainder parses to a known Zhipu host.
			name:         "scheme-less base takes the custom-host fallback",
			openAIBase:   "//api.z.ai/api/paas/v4",
			wantMessages: "//api.z.ai/api/paas/v4/anthropic/v1/messages",
		},
		{
			// use_anthropic_endpoints with a base_url already set to the mount
			// itself must not append /anthropic a second time.
			name:         "Anthropic mount as base is idempotent",
			openAIBase:   "https://api.z.ai/api/anthropic",
			wantMessages: "https://api.z.ai/api/anthropic/v1/messages",
		},
		{
			name:         "Anthropic mount as base with trailing slash is idempotent",
			openAIBase:   "https://open.bigmodel.cn/api/anthropic/",
			wantMessages: "https://open.bigmodel.cn/api/anthropic/v1/messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := deriveAnthropicBaseURL(tt.openAIBase)
			full := base + anthropicMessagesPath
			if full != tt.wantMessages {
				t.Errorf("messages URL for %q = %q, want %q", tt.openAIBase, full, tt.wantMessages)
			}
		})
	}
}
