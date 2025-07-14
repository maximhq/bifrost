package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBedrock(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Cleanup()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.Bedrock,
		ChatModel: "anthropic.claude-3-sonnet-20240229-v1:0",
		TextModel: "", // Bedrock Claude doesn't support text completion
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported for Claude
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           true,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
