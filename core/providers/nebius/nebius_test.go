package nebius_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestNebius(t *testing.T) {
	t.Parallel()
	if os.Getenv("NEBIUS_API_KEY") == "" {
		t.Skip("Skipping Nebius tests because NEBIUS_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:  schemas.Nebius,
		ChatModel: "openai/gpt-oss-120b",
		TextModel: "openai/gpt-oss-120b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Nebius, Model: "meta-llama/Meta-Llama-3.1-8B-Instruct-fast"},
		},
		EmbeddingModel:       "BAAI/bge-en-icl",
		ImageGenerationModel: "black-forest-labs/flux-schnell",
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            false,
			CompletionStream:      false,
			MultiTurnConversation: false,
			ToolCalls:             false,
			ToolCallsStreaming:    false,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			ImageGeneration:       true,
			ImageGenerationStream: false,
			MultipleImages:        false,
			CompleteEnd2End:       false,
			Embedding:             false, // Nebius supports embeddings
			ListModels:            false,
		},
	}

	t.Run("NebiusTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
