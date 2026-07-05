package minimax_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestMinimax(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")) == "" {
		t.Skip("Skipping Minimax tests because MINIMAX_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Minimax,
		ChatModel: "MiniMax-M3",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Minimax, Model: "MiniMax-M3"},
			{Provider: schemas.Minimax, Model: "MiniMax-M2.5"},
		},
		TextModel:      "MiniMax-M3",
		EmbeddingModel: "", // Minimax doesn't support embedding
		ReasoningModel: "MiniMax-M3",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
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

	t.Run("MinimaxTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
