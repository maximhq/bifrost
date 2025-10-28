package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCohere(t *testing.T) {
	if os.Getenv("COHERE_API_KEY") == "" {
		t.Skip("Skipping Cohere tests because COHERE_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.Cohere,
		ChatModel:      "command-a-03-2025",
		VisionModel:    "command-a-vision-07-2025", // Cohere's latest vision model
		TextModel:      "",                         // Cohere focuses on chat
		EmbeddingModel: "embed-v4.0",
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not typical for Cohere
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,  // May not support automatic
			ImageURL:              false, // Supported by c4ai-aya-vision-8b model
			ImageBase64:           true,  // Supported by c4ai-aya-vision-8b model
			MultipleImages:        false, // Supported by c4ai-aya-vision-8b model
			CompleteEnd2End:       false,
			Embedding:             true,
			Reasoning:             true,
			ListModels:            true,
		},
	}

	t.Run("CohereTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
