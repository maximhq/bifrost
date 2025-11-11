package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestMistral(t *testing.T) {
	t.Parallel()
	if os.Getenv("MISTRAL_API_KEY") == "" {
		t.Skip("Skipping Mistral tests because MISTRAL_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.Mistral,
		ChatModel: "mistral-medium-2508",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Mistral, Model: "mistral-small-2503"},
		},
		VisionModel:    "pixtral-12b-latest",
		EmbeddingModel: "codestral-embed",
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            false,
		},
	}

	t.Run("MistralTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
