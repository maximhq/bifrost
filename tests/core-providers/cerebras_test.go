package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCerebras(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.Cerebras,
		ChatModel:      "llama-3.3-70b",
		TextModel:      "llama3.1-8b",
		EmbeddingModel: "", // Cerebras doesn't support embedding
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
