package nebius_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestNebius runs comprehensive tests for the Nebius Token Factory provider.
// Tests are skipped if NEBIUS_API_KEY environment variable is not set.
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

	// Configure test scenarios for Nebius Token Factory
	// Nebius provides access to various LLM models via OpenAI-compatible API
	testConfig := testutil.ComprehensiveTestConfig{
		Provider:       schemas.Nebius,
		ChatModel:      "meta-llama/Llama-3.3-70B-Instruct", // Primary chat model
		VisionModel:    "",                                  // Vision models may not be available
		TextModel:      "",                                  // Text completion may not be supported
		EmbeddingModel: "",                                  // Embedding model if available
		ReasoningModel: "",                                  // Reasoning model if available
		Scenarios: testutil.TestScenarios{
			// Core chat functionality - should be supported
			SimpleChat:            true,
			MultiTurnConversation: true,

			// Streaming - should be supported via OpenAI-compatible API
			CompletionStream: false, // Text completion streaming

			// Tool calling - depends on model support
			ToolCalls:             true,
			ToolCallsStreaming:    false, // May not be fully supported
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: false, // May not be supported

			// Vision capabilities - depends on available models
			ImageURL:       false,
			ImageBase64:    false,
			MultipleImages: false,

			// Text completion - may not be supported
			TextCompletion: false,

			// Other features
			CompleteEnd2End: false,
			Reasoning:       false, // Reasoning may not be available
			ListModels:      true,  // Should be supported
		},
	}

	t.Run("NebiusTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}

// TestNebiusWithEmbeddings tests embedding functionality if available.
// This test is separate as embedding support may vary.
func TestNebiusWithEmbeddings(t *testing.T) {
	t.Parallel()
	if os.Getenv("NEBIUS_API_KEY") == "" {
		t.Skip("Skipping Nebius embedding tests because NEBIUS_API_KEY is not set")
	}

	// Skip if embedding model is not configured
	embeddingModel := os.Getenv("NEBIUS_EMBEDDING_MODEL")
	if embeddingModel == "" {
		t.Skip("Skipping Nebius embedding tests because NEBIUS_EMBEDDING_MODEL is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:       schemas.Nebius,
		EmbeddingModel: embeddingModel,
		Scenarios: testutil.TestScenarios{
			// Only test embedding
			SimpleChat: false,
		},
	}

	t.Run("NebiusEmbeddingTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
