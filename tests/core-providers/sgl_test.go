package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSGL(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.SGL,
		ChatModel:      "qwen/qwen2.5-0.5b-instruct",
		VisionModel:    "Qwen/Qwen2.5-VL-7B-Instruct",
		TextModel:      "qwen/qwen2.5-0.5b-instruct",
		EmbeddingModel: "Alibaba-NLP/gte-Qwen2-1.5B-instruct",
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             true,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
