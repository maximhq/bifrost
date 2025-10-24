package tests

import (
	"context"
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGroq(t *testing.T) {
	if os.Getenv("GROQ_API_KEY") == "" {
		t.Skip("Skipping Groq tests because GROQ_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.Groq,
		ChatModel: "llama-3.3-70b-versatile",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Groq, Model: "openai/gpt-oss-120b"},
		},
		TextModel: "llama-3.3-70b-versatile", // Use same model for text completion (via conversion)
		TextCompletionFallbacks: []schemas.Fallback{
			{Provider: schemas.Groq, Model: "openai/gpt-oss-20b"},
		},
		EmbeddingModel: "", // Groq doesn't support embedding
		Scenarios: config.TestScenarios{
			TextCompletion:        true, // Supported via chat completion conversion
			TextCompletionStream:  true, // Supported via chat completion streaming conversion
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
		},
	}

	ctx = context.WithValue(ctx, schemas.BifrostContextKey("x-litellm-fallback"), "true")

	t.Run("GroqTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
