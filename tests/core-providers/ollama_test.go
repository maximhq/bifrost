package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOllama(t *testing.T) {
	t.Parallel()
	if os.Getenv("OLLAMA_BASE_URL") == "" {
		t.Skip("Skipping Ollama tests because OLLAMA_BASE_URL is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.Ollama,
		ChatModel:      "llama3.1:latest",
		TextModel:      "", // Ollama doesn't support text completion in newer models
		EmbeddingModel: "", // Ollama doesn't support embedding
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported
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
		},
	}

	t.Run("OllamaTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
