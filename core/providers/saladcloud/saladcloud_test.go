package saladcloud_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSaladCloud(t *testing.T) {
	t.Parallel()
	if os.Getenv("SALAD_CLOUD_API_KEY") == "" {
		t.Skip("Skipping SaladCloud tests because SALAD_CLOUD_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.SaladCloud,
		ChatModel: "qwen3.5-9b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.SaladCloud, Model: "qwen3.6-27b"},
		},
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false,
			TextCompletionStream:       false,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ListModels:                 true,
			Embedding:                  false,
			ImageGeneration:            false,
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
		},
	}

	t.Run("SaladCloudTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
