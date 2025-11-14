package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAzure(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Azure tests because Azure.")

	if os.Getenv("AZURE_API_KEY") == "" {
		t.Skip("Skipping Azure tests because AZURE_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:    schemas.Azure,
		ChatModel:   "gpt-4o-backup",
		VisionModel: "gpt-4o",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Azure, Model: "gpt-4o-backup"},
		},
		TextModel:      "", // Azure OpenAI doesn't support text completion in newer models
		EmbeddingModel: "text-embedding-ada-002",
		ReasoningModel: "o1",
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
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
			Reasoning:             true,
		},
	}

	// Disable embedding if embeddings key is not provided
	if os.Getenv("AZURE_EMB_API_KEY") == "" {
		t.Logf("AZURE_EMB_API_KEY not set; disabling Azure embedding tests")
		testConfig.EmbeddingModel = ""
		testConfig.Scenarios.Embedding = false
	}

	t.Run("AzureTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
