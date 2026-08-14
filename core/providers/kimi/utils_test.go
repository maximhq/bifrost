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
