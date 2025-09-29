package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCohere(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.Cohere,
		ChatModel:      "command-a-03-2025",
		VisionModel:    "command-a-vision-07-2025", // Cohere's latest vision model
		TextModel:      "",                         // Cohere focuses on chat
		EmbeddingModel: "embed-v4.0",
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not typical for Cohere
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,  // May not support automatic
			ImageURL:              false, // Supported by c4ai-aya-vision-8b model
			ImageBase64:           true,  // Supported by c4ai-aya-vision-8b model
			MultipleImages:        true,  // Supported by c4ai-aya-vision-8b model
			CompleteEnd2End:       false,
			Embedding:             true,
			Reasoning:             true,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
