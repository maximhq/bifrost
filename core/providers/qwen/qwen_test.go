package qwen_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestQwen(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("QWEN_API_KEY")) == "" {
		t.Skip("Skipping Qwen tests because QWEN_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Qwen,
		ChatModel: "qwen3.7-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Qwen, Model: "qwen3.7-flash"},
			{Provider: schemas.Qwen, Model: "qwen3.7-plus"},
		},
		TextModel:      "", // Qwen's OpenAI-compatible endpoint exposes chat completions only
		EmbeddingModel: "text-embedding-v4",
		ReasoningModel: "qwen3.8-max",
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
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
			CompleteEnd2End:            true,
			Embedding:                  true,
			ListModels:                 true,
			Reasoning:                  true,
		},
	}

	t.Run("QwenTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
