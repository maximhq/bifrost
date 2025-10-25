package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCerebras(t *testing.T) {
	if os.Getenv("CEREBRAS_API_KEY") == "" {
		t.Skip("Skipping Cerebras tests because CEREBRAS_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.Cerebras,
		ChatModel: "llama-3.3-70b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Cerebras, Model: "llama3.1-8b"},
			{Provider: schemas.Cerebras, Model: "gpt-oss-120b"},
		},
		TextModel:      "llama3.1-8b",
		EmbeddingModel: "", // Cerebras doesn't support embedding
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  true,
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
			ListModels:            true,
		},
	}

	t.Run("CerebrasTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
