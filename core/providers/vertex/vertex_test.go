package vertex_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestVertex(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("VERTEX_API_KEY")) == "" && (strings.TrimSpace(os.Getenv("VERTEX_PROJECT_ID")) == "" || strings.TrimSpace(os.Getenv("VERTEX_CREDENTIALS")) == "") {
		t.Skip("Skipping Vertex tests because VERTEX_API_KEY is not set and VERTEX_PROJECT_ID or VERTEX_CREDENTIALS is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	rerankModel := strings.TrimSpace(os.Getenv("VERTEX_RERANK_MODEL"))

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Vertex,
		ChatModel:            "google/gemini-2.0-flash-001",
		VisionModel:          "claude-sonnet-4-5",
		TextModel:            "", // Vertex doesn't support text completion in newer models
		EmbeddingModel:       "text-multilingual-embedding-002",
		RerankModel:          rerankModel,
		ReasoningModel:       "claude-4.5-haiku",
		ImageGenerationModel: "gemini-2.5-flash-image",
		ImageEditModel:       "imagen-3.0-capability-001",
		Scenarios: llmtests.TestScenarios{
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
			ImageBase64:           true,
			ImageGeneration:       true,
			ImageGenerationStream: false,
			ImageEdit:             true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			FileBase64:            true,
			Embedding:             true,
			Rerank:                rerankModel != "",
			Reasoning:             true,
			ListModels:            false,
			CountTokens:           true,
			StructuredOutputs:     true, // Structured outputs with nullable enum support
		},
	}

	t.Run("VertexTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
