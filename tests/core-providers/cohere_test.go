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
	defer client.Cleanup()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.Cohere,
		ChatModel: "command-a-03-2025",
		TextModel: "", // Cohere focuses on chat
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not typical for Cohere
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: false, // May not support automatic
			ImageURL:              false, // Check if supported
			ImageBase64:           false, // Check if supported
			MultipleImages:        false, // Check if supported
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
