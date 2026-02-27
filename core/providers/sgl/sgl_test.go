package sgl_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSGL(t *testing.T) {
	t.Parallel()
	if os.Getenv("SGL_BASE_URL") == "" {
		t.Skip("Skipping SGL tests because SGL_BASE_URL is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.SGL,
		ChatModel:      "qwen/qwen2.5-0.5b-instruct",
		VisionModel:    "Qwen/Qwen2.5-VL-7B-Instruct",
		TextModel:      "qwen/qwen2.5-0.5b-instruct",
		EmbeddingModel: "Alibaba-NLP/gte-Qwen2-1.5B-instruct",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
		},
	}

	t.Run("SGLTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
