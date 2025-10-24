package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAnthropic(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("Skipping Anthropic tests because ANTHROPIC_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.Anthropic,
		ChatModel: "claude-sonnet-4-20250514",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-7-sonnet-20250219"},
			{Provider: schemas.Anthropic, Model: "claude-sonnet-4-20250514"},
		},
		VisionModel: "claude-3-7-sonnet-20250219", // Same model supports vision
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             false,
			Reasoning:             true,
		},
	}

	t.Run("AnthropicTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
