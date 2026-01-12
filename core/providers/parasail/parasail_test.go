package parasail_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestParasail(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("PARASAIL_API_KEY")) == "" {
		t.Skip("Skipping Parasail tests because PARASAIL_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Parasail,
		ChatModel:      "parasail-llama-33-70b-fp8",
		TextModel:      "", // Parasail doesn't support text completion
		EmbeddingModel: "", // Parasail doesn't support embedding
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

	t.Run("ParasailTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
