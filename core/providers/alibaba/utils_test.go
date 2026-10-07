package alibaba

import "testing"

func TestDeriveAnthropicBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		openAIBase   string
		wantMessages string // full messages URL
	}{
		{
			name:         "legacy shared international (Singapore)",
			openAIBase:   "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
			wantMessages: "https://dashscope-intl.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "legacy shared cn-beijing",
			openAIBase:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
			wantMessages: "https://dashscope.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "legacy shared us-east-1",
			openAIBase:   "https://dashscope-us.aliyuncs.com/compatible-mode/v1",
			wantMessages: "https://dashscope-us.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "workspace-dedicated host",
			openAIBase:   "https://ws-abc123.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
			wantMessages: "https://ws-abc123.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "Token Plan Team international",
			openAIBase:   "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
			wantMessages: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "Coding Plan China host",
			openAIBase:   "https://coding.dashscope.aliyuncs.com/v1",
			wantMessages: "https://coding.dashscope.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "Coding Plan international host",
			openAIBase:   "https://coding-intl.dashscope.aliyuncs.com/v1",
			wantMessages: "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "trailing slash is trimmed",
			openAIBase:   "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/",
			wantMessages: "https://dashscope-intl.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "custom base falls back to appending /apps/anthropic",
			openAIBase:   "https://proxy.example.com/qwen",
			wantMessages: "https://proxy.example.com/qwen/apps/anthropic/v1/messages",
		},
		{
			// Suffix semantics only hold on Alibaba's own hosts: a custom host
			// ending in /compatible-mode/v1 must NOT have it rewritten away.
			name:         "custom base ending in /compatible-mode/v1 keeps its path",
			openAIBase:   "https://proxy.example.com/compatible-mode/v1",
			wantMessages: "https://proxy.example.com/compatible-mode/v1/apps/anthropic/v1/messages",
		},
		{
			name:         "custom base ending in /v1 keeps its path",
			openAIBase:   "https://proxy.example.com/v1",
			wantMessages: "https://proxy.example.com/v1/apps/anthropic/v1/messages",
		},
		{
			// The aliyuncs.com gate must respect the domain boundary — a host
			// whose name merely CONTAINS aliyuncs.com is a foreign host.
			name:         "lookalike host ending in aliyuncs.com.<foreign domain> keeps its path",
			openAIBase:   "https://dashscope-intl.aliyuncs.com.evil.example/compatible-mode/v1",
			wantMessages: "https://dashscope-intl.aliyuncs.com.evil.example/compatible-mode/v1/apps/anthropic/v1/messages",
		},
		{
			// Scheme-less values must take the custom-host fallback even when
			// the remainder parses to a known Alibaba host.
			name:         "scheme-less base takes the custom-host fallback",
			openAIBase:   "//dashscope-intl.aliyuncs.com/compatible-mode/v1",
			wantMessages: "//dashscope-intl.aliyuncs.com/compatible-mode/v1/apps/anthropic/v1/messages",
		},
		{
			// use_anthropic_endpoints with a base_url already set to the mount
			// itself must not append /apps/anthropic a second time.
			name:         "Token Plan host with the Anthropic mount as base is idempotent",
			openAIBase:   "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic",
			wantMessages: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "Anthropic mount as base with trailing slash is idempotent",
			openAIBase:   "https://dashscope-intl.aliyuncs.com/apps/anthropic/",
			wantMessages: "https://dashscope-intl.aliyuncs.com/apps/anthropic/v1/messages",
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
