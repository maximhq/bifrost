package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBedrock(t *testing.T) {
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping Bedrock embedding: AWS credentials not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.Bedrock,
		ChatModel:      "claude-sonnet-4",
		VisionModel:    "claude-sonnet-4",
		TextModel:      "mistral.mistral-7b-instruct-v0:2", // Bedrock Claude doesn't support text completion
		EmbeddingModel: "amazon.titan-embed-text-v2:0",
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
			Embedding:             true,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
