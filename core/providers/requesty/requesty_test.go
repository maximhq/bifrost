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
		ChatModel:      "openai/gpt-4o-mini",
		TextModel:      "", // Requesty doesn't support text completion
		EmbeddingModel: "", // Requesty doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false, // Not supported yet
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // Not supported yet
			ImageBase64:           false, // Not supported yet
			MultipleImages:        false, // Not supported yet
			CompleteEnd2End:       true,
			Embedding:             false, // Not supported yet
			ListModels:            true,
		},
	}

	t.Run("RequestyTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
