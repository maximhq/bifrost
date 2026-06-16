package opencode

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestResolveRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		providerKey schemas.ModelProvider
		modelID     string
		wantAdapter adapterKind
		wantPath    string
		wantAuth    authStyle
		wantMatch   routeMatchKind
		wantClass   string
	}{
		{
			name:        "zen exact responses model",
			providerKey: schemas.OpencodeZen,
			modelID:     "gpt-5-nano",
			wantAdapter: adapterOpenAIResponses,
			wantPath:    "/v1/responses",
			wantAuth:    authStyleBearer,
			wantMatch:   routeMatchExact,
		},
		{
			name:        "zen exact anthropic model",
			providerKey: schemas.OpencodeZen,
			modelID:     "claude-haiku-4-5",
			wantAdapter: adapterAnthropicMessages,
			wantPath:    "/v1/messages",
			wantAuth:    authStyleAnthropicKey,
			wantMatch:   routeMatchExact,
		},
		{
			name:        "zen exact gemini model",
			providerKey: schemas.OpencodeZen,
			modelID:     "gemini-3-flash",
			wantAdapter: adapterGeminiNative,
			wantPath:    "/v1/models/gemini-3-flash",
			wantAuth:    authStyleGeminiGateway,
			wantMatch:   routeMatchExact,
		},
		{
			name:        "go exact anthropic override beats class",
			providerKey: schemas.OpencodeGo,
			modelID:     "minimax-m2.7",
			wantAdapter: adapterAnthropicMessages,
			wantPath:    "/v1/messages",
			wantAuth:    authStyleAnthropicKey,
			wantMatch:   routeMatchExact,
		},
		{
			name:        "go class fallback for future qwen",
			providerKey: schemas.OpencodeGo,
			modelID:     "qwen4.1-max",
			wantAdapter: adapterAnthropicMessages,
			wantPath:    "/v1/messages",
			wantAuth:    authStyleAnthropicKey,
			wantMatch:   routeMatchClass,
			wantClass:   "qwen",
		},
		{
			name:        "zen default chat fallback",
			providerKey: schemas.OpencodeZen,
			modelID:     "mystery-model-1",
			wantAdapter: adapterOpenAIChat,
			wantPath:    "/v1/chat/completions",
			wantAuth:    authStyleBearer,
			wantMatch:   routeMatchDefault,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := resolveRoute(tc.providerKey, tc.modelID)
			if got.Adapter != tc.wantAdapter {
				t.Fatalf("adapter = %q, want %q", got.Adapter, tc.wantAdapter)
			}
			if got.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", got.Path, tc.wantPath)
			}
			if got.Auth != tc.wantAuth {
				t.Fatalf("auth = %q, want %q", got.Auth, tc.wantAuth)
			}
			if got.MatchedBy != tc.wantMatch {
				t.Fatalf("matchedBy = %q, want %q", got.MatchedBy, tc.wantMatch)
			}
			if got.ClassPrefix != tc.wantClass {
				t.Fatalf("classPrefix = %q, want %q", got.ClassPrefix, tc.wantClass)
			}
		})
	}
}
