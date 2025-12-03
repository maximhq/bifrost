package vertex_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestVertex(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("VERTEX_API_KEY")) == "" && (strings.TrimSpace(os.Getenv("VERTEX_PROJECT_ID")) == "" || strings.TrimSpace(os.Getenv("VERTEX_CREDENTIALS")) == "") {
		t.Skip("Skipping Vertex tests because VERTEX_API_KEY is not set and VERTEX_PROJECT_ID or VERTEX_CREDENTIALS is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:       schemas.Vertex,
		ChatModel:      "google/gemini-2.0-flash-001",
		VisionModel:    "google/gemini-2.0-flash-001",
		TextModel:      "", // Vertex doesn't support text completion in newer models
		EmbeddingModel: "text-multilingual-embedding-002",
		ReasoningModel: "claude-4.5-haiku",
		Scenarios: testutil.TestScenarios{
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
			Reasoning:             false, // Not supported right now because we are not using native gemini converters
			ListModels:            false,
		},
	}

	t.Run("VertexTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
