package sarvam_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSarvam(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("SARVAM_API_KEY")) == "" {
		t.Skip("Skipping Sarvam tests because SARVAM_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Sarvam,
		ChatModel: "sarvam-30b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Sarvam, Model: "sarvam-30b"},
			{Provider: schemas.Sarvam, Model: "sarvam-105b"},
		},
		EmbeddingModel: "", // Sarvam doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			MultiTurnConversation: true,
			ListModels:            true,
			// CompletionStream also gates RunResponsesStreamTest. Sarvam's chat
			// models are reasoning models with verbose default-on reasoning
			// (~1400 chunks observed live for the harness's own long-form prompt) -
			// the shared harness raises its generic safety cap specifically for
			// schemas.Sarvam in chat_completion_stream.go/responses_stream.go
			// instead of skipping these scenarios.
			CompletionStream:      true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			AutomaticFunctionCall: true,
			// MultipleToolCalls/MultipleToolCallsStreaming are left off: Sarvam
			// models sometimes only call one of several offered tools in a single
			// turn (observed live), a model-capability limitation rather than a
			// mapping bug.
			// End2EndToolCalling/CompleteEnd2End are left off: Sarvam is stricter
			// than OpenAI about requiring `tools` to be re-sent on a follow-up
			// request carrying tool-result messages ("Tool messages found but no
			// tools provided") - see ISSUES.md.
		},
	}

	t.Run("SarvamTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
