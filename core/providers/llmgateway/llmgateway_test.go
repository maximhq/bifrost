package llmgateway_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestLLMGateway(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("LLMGATEWAY_API_KEY")) == "" {
		t.Skip("Skipping LLM Gateway tests because LLMGATEWAY_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.LLMGateway,
		ChatModel: "deepseek-v4-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.LLMGateway, Model: "deepseek-v4-flash"},
		},
		TextModel:      "deepseek-v4-flash",
		EmbeddingModel: "", // LLM Gateway doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // LLM Gateway only exposes /v1/chat/completions
			TextCompletionStream:  false,
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
			Reasoning:             false,
		},
	}

	t.Run("LLMGatewayTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
