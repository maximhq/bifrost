package requesty_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestRequesty(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("REQUESTY_API_KEY")) == "" {
		t.Skip("Skipping Requesty tests because REQUESTY_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

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
