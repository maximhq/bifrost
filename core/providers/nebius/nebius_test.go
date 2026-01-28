package nebius_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestNebius(t *testing.T) {
	t.Parallel()
	if os.Getenv("NEBIUS_API_KEY") == "" {
		t.Skip("Skipping Nebius tests because NEBIUS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Nebius,
		ChatModel: "openai/gpt-oss-120b",
		TextModel: "openai/gpt-oss-120b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Nebius, Model: "meta-llama/Meta-Llama-3.1-8B-Instruct-fast"},
		},
		EmbeddingModel:       "BAAI/bge-en-icl",
		ImageGenerationModel: "black-forest-labs/flux-schnell",
		Scenarios: llmtests.TestScenarios{
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
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			ImageGeneration:       true,
			CompleteEnd2End:       true,
			ImageGenerationStream: false,
			Embedding:             true, // Nebius supports embeddings
			ListModels:            true,
		},
	}

	t.Run("NebiusTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
