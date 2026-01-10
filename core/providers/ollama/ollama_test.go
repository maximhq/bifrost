package ollama_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestOllama runs comprehensive tests against a local or remote Ollama instance.
//
// Environment variables:
//   - OLLAMA_BASE_URL: Required. The base URL of the Ollama instance (e.g., "http://localhost:11434")
//   - OLLAMA_API_KEY: Optional. API key for authenticated Ollama Cloud instances
//   - OLLAMA_MODEL: Optional. Model to test with (default: "llama3.2:latest")
//   - OLLAMA_EMBEDDING_MODEL: Optional. Embedding model to test with (default: "nomic-embed-text:latest")
//
// The tests use Ollama's native API endpoints:
//   - /api/chat for chat completion
//   - /api/embed for embeddings
//   - /api/tags for listing models
func TestOllama(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")) == "" {
		t.Skip("Skipping Ollama tests because OLLAMA_BASE_URL is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	// Get model names from environment or use defaults
	chatModel := os.Getenv("OLLAMA_MODEL")
	if chatModel == "" {
		chatModel = "llama3.2:latest"
	}

	embeddingModel := os.Getenv("OLLAMA_EMBEDDING_MODEL")
	if embeddingModel == "" {
		embeddingModel = "nomic-embed-text:latest"
	}

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:       schemas.Ollama,
		ChatModel:      chatModel,
		TextModel:      "", // Text completion uses chat endpoint in native API
		EmbeddingModel: embeddingModel,
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false, // Not supported - use chat instead
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // Ollama expects base64 images
			ImageBase64:           true,  // Multimodal models support base64 images
			MultipleImages:        false,
			FileBase64:            false,
			FileURL:               false,
			CompleteEnd2End:       true,
			Embedding:             true, // Native API supports embeddings
			ListModels:            true,
		},
	}

	t.Run("OllamaTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}

// TestOllamaCloud tests Ollama Cloud with API key authentication.
// This test is separate to allow testing against Ollama Cloud specifically.
//
// Environment variables:
//   - OLLAMA_CLOUD_URL: Required. The Ollama Cloud URL
//   - OLLAMA_API_KEY: Required. API key for Ollama Cloud
//   - OLLAMA_CLOUD_MODEL: Optional. Model to test with
func TestOllamaCloud(t *testing.T) {
	t.Parallel()
	cloudURL := os.Getenv("OLLAMA_CLOUD_URL")
	apiKey := os.Getenv("OLLAMA_API_KEY")

	if cloudURL == "" || apiKey == "" {
		t.Skip("Skipping Ollama Cloud tests because OLLAMA_CLOUD_URL or OLLAMA_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	// Get model name from environment or use default
	chatModel := os.Getenv("OLLAMA_CLOUD_MODEL")
	if chatModel == "" {
		chatModel = "llama3.2:latest"
	}

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:       schemas.Ollama,
		ChatModel:      chatModel,
		TextModel:      "",
		EmbeddingModel: "",
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false, // May not be supported in cloud
			End2EndToolCalling:    true,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false, // May not be available
			ListModels:            true,
		},
	}

	t.Run("OllamaCloudTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
