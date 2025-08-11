package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCustomProvider(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Cleanup()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  "openai-custom",
		ChatModel: "llama-3.3-70b-versatile",
		TextModel: "", // OpenAI doesn't support text completion in newer models
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-7-sonnet-20250219"},
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
