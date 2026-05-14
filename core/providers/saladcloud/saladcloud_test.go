package saladcloud_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSaladcloud(t *testing.T) {
	if os.Getenv("SALAD_CLOUD_API_KEY") == "" {
		t.Skip("Skipping SaladCloud tests because SALAD_CLOUD_API_KEY is not set")
	}
	previousSkipParallel, hadSkipParallel := os.LookupEnv("SKIP_PARALLEL_TESTS")
	if err := os.Setenv("SKIP_PARALLEL_TESTS", "true"); err != nil {
		t.Fatalf("failed to disable parallel tests: %v", err)
	}
	t.Cleanup(func() {
		if hadSkipParallel {
			_ = os.Setenv("SKIP_PARALLEL_TESTS", previousSkipParallel)
			return
		}
		_ = os.Unsetenv("SKIP_PARALLEL_TESTS")
	})

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.SaladCloud,
		ChatModel:      "qwen3.6-35b-a3b",
		VisionModel:    "qwen3.6-35b-a3b",
		ReasoningModel: "qwen3.6-35b-a3b",
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
			ImageURL:                   true,
			ImageBase64:                false, // Salad accepts smaller base64 data URLs, but returns 503 for the shared 1.16MB PNG fixture
			MultipleImages:             false, // Depends on the same large base64 fixture
			Reasoning:                  true,
			StructuredOutputs:          false,
		},
	}

	t.Run("SaladCloudTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
