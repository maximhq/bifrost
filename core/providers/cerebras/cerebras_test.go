package cerebras_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCerebras(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("CEREBRAS_API_KEY")) == "" {
		t.Skip("Skipping Cerebras tests because CEREBRAS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Cerebras,
		ChatModel: "llama-3.3-70b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Cerebras, Model: "llama3.1-8b"},
			{Provider: schemas.Cerebras, Model: "gpt-oss-120b"},
		},
		TextModel:      "llama3.1-8b",
		EmbeddingModel: "", // Cerebras doesn't support embedding
		ReasoningModel: "gpt-oss-120b",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			Reasoning:             true,
		},
	}

	t.Run("CerebrasTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
