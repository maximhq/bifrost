package requesty_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestRequesty runs a comprehensive set of tests against the Requesty provider.
// It checks various scenarios including chat, streaming, tool calls, image handling, and embeddings.
// It skips tests if REQUESTY_API_KEY is not set.
func TestRequesty(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("REQUESTY_API_KEY")) == "" {
		t.Skip("Skipping Requesty tests because REQUESTY_API_KEY is not set")
	}

	// Setup the test environment and client
	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Define the comprehensive test configuration for Requesty
	// Note: Requesty does not support text completion, so that scenario is set to false.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Requesty,
		ChatModel:      "google/gemma-4-31b-it", // Vision+Tools+Think
		TextModel:      "",                      // Requesty doesn't support text completion
		EmbeddingModel: "openai/text-embedding-3-small",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Requesty, Model: "nvidia/nemotron-3-nano-30b-a3b"}, // Tools+Think
		},
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false, // Not verified yet
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,  // gemma-4-31b-it supports Vision
			ImageBase64:           true,  // gemma-4-31b-it supports Vision
			MultipleImages:        false, // Not verified yet
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
		},
	}

	t.Run("RequestyTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
