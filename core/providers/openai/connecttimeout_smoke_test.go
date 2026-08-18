package openai

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression for issue #5986: the full provider path against a blackholed
// base_url (192.0.2.1 is TEST-NET-1, never routed) must fail within the
// connect timeout instead of hanging for the full request timeout.
func TestOpenAIConnectTimeoutBoundsRequest(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "http://192.0.2.1:81",
			DefaultRequestTimeoutInSeconds: 60,
			ConnectTimeoutInSeconds:        2,
		},
	}, nil)

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	start := time.Now()
	_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("sk-test")}, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	})
	elapsed := time.Since(start)

	if bifrostErr == nil {
		t.Fatal("expected error against blackholed base_url")
	}
	t.Logf("failed in %v with: %s", elapsed, bifrostErr.Error.Message)
	if elapsed > 15*time.Second {
		t.Fatalf("took %v; connect timeout did not bound the request", elapsed)
	}
}
