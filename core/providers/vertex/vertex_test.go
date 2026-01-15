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
		Provider:        schemas.Vertex,
		ChatModels:      []string{"google/gemini-2.0-flash-001"},
		VisionModels:    []string{"claude-sonnet-4-5"},
		TextModels:      []string{""}, // Vertex doesn't support text completion in newer models
		EmbeddingModels: []string{"text-multilingual-embedding-002"},
		ReasoningModels: []string{"claude-opus-4-5"},
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
			ImageURL:              false,
			ImageBase64:           true,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			FileBase64:            true,
			Embedding:             true,
			Reasoning:             true,
			ListModels:            false,
			CountTokens:           true,
			StructuredOutputs:     true, // Structured outputs with nullable enum support
		},
	}

	t.Run("VertexTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
