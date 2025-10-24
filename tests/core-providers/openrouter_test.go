package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenRouter(t *testing.T) {
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("Skipping OpenRouter tests because OPENROUTER_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.OpenRouter,
		ChatModel:      "openai/gpt-4o",
		VisionModel:    "openai/gpt-4o",
		TextModel:      "google/gemini-2.5-flash",
		EmbeddingModel: "",
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    false, // OpenRouter's responses API is in Beta
			AutomaticFunctionCall: true,
			ImageURL:              false, // OpenRouter's responses API is in Beta
			ImageBase64:           false, // OpenRouter's responses API is in Beta
			MultipleImages:        false, // OpenRouter's responses API is in Beta
			CompleteEnd2End:       false, // OpenRouter's responses API is in Beta
			ListModels:            true,
		},
	}

	t.Run("OpenRouterTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
