package main

import (
	"core-providers-test/config"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAzure(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
		return
	}
	defer cancel()
	defer client.Cleanup()

	config := config.ComprehensiveTestConfig{
		Provider:  schemas.Azure,
		ChatModel: "gpt-4o",
		TextModel: "", // Azure OpenAI doesn't support text completion in newer models
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	}

	runAllComprehensiveTests(t, client, ctx, config)
}
