package main

import (
	"core-providers-test/config"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestVertex(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
		return
	}
	defer cancel()
	defer client.Cleanup()

	config := config.ComprehensiveTestConfig{
		Provider:  schemas.Vertex,
		ChatModel: "google/gemini-2.0-flash-001",
		TextModel: "", // Vertex doesn't support text completion in newer models
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
			{Provider: schemas.Anthropic, Model: "claude-3-7-sonnet-20250219"},
		},
	}

	runAllComprehensiveTests(t, client, ctx, config)
}
