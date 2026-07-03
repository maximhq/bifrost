package edenai_test

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/edenai"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestEdenAILiveChatCompletion performs a real chat completion against the Eden AI
// API through the Bifrost provider. It is skipped unless EDENAI_API_KEY is set,
// so CI stays hermetic.
func TestEdenAILiveChatCompletion(t *testing.T) {
	apiKey := os.Getenv("EDENAI_API_KEY")
	if apiKey == "" {
		t.Skip("EDENAI_API_KEY not set; skipping live Eden AI test")
	}

	config := &schemas.ProviderConfig{}
	config.CheckAndSetDefaults()
	provider, err := edenai.NewEdenAIProvider(config, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewEdenAIProvider failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar(apiKey), Models: schemas.WhiteList{"*"}}
	content := "Reply with exactly: BIFROST-EDEN-OK"
	request := &schemas.BifrostChatRequest{
		Provider: schemas.EdenAI,
		Model:    "openai/gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}},
		},
	}

	resp, bifErr := provider.ChatCompletion(ctx, key, request)
	if bifErr != nil {
		msg := ""
		if bifErr.Error != nil {
			msg = bifErr.Error.Message
		}
		t.Fatalf("live ChatCompletion returned error: %s", msg)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("expected at least one choice in the response, got %+v", resp)
	}
	t.Logf("Eden AI live chat completion succeeded via Bifrost provider (%d choice(s))", len(resp.Choices))
}
