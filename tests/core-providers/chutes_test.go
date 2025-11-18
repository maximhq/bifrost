package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestChutes(t *testing.T) {
	t.Parallel()
	if os.Getenv("CHUTES_API_KEY") == "" {
		t.Skip("Skipping Chutes tests because CHUTES_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.Chutes,
		ChatModel: "chutes/deepseek-ai/DeepSeek-R1", // Actual Chutes.ai model
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Chutes, Model: "chutes/Qwen/Qwen3-32B"},
		},
		TextModel:      "chutes/deepseek-ai/DeepSeek-R1",
		EmbeddingModel: "chutes/deepseek-ai/DeepSeek-R1", // Chutes.ai supports embeddings
		Scenarios: config.TestScenarios{
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
			Embedding:             true,
			ListModels:            true,
		},
	}

	t.Run("ChutesTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}