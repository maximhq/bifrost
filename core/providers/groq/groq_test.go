package groq_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGroq(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("GROQ_API_KEY")) == "" {
		t.Skip("Skipping Groq tests because GROQ_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:  schemas.Groq,
		ChatModel: "llama-3.3-70b-versatile",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Groq, Model: "openai/gpt-oss-120b"},
		},
		TextModel: "llama-3.3-70b-versatile",
		TextCompletionFallbacks: []schemas.Fallback{
			{Provider: schemas.Groq, Model: "openai/gpt-oss-20b"},
		},
		EmbeddingModel: "", // Groq doesn't support embedding
		ReasoningModel: "openai/gpt-oss-120b",
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
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
			FileBase64:            false, // Not supported
			FileURL:               false, // Not supported
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			Reasoning:             true,
		},
	}
	t.Run("GroqTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
