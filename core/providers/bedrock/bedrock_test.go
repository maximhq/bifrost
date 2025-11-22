package bedrock_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBedrock(t *testing.T) {
	t.Parallel()
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping Bedrock embedding: AWS credentials not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:    schemas.Bedrock,
		ChatModel:   "anthropic.claude-3-5-sonnet-20240620-v1:0",
		VisionModel: "claude-sonnet-4",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Bedrock, Model: "claude-3.7-sonnet"},
		},
		TextModel:      "mistral.mistral-7b-instruct-v0:2", // Bedrock Claude doesn't support text completion
		EmbeddingModel: "cohere.embed-v4:0",
		ReasoningModel: "claude-sonnet-4",
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false, // Not supported for Claude
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // Direct Image URL is not supported for Bedrock
			ImageBase64:           true,
			MultipleImages:        false, // Direct Image URL is not supported for Bedrock
			CompleteEnd2End:       true,
			Embedding:             true,
			Reasoning:             true,
			ListModels:            true,
		},
	}

	t.Run("BedrockTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
