package minimax_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestMinimax is the live provider-suite entry point used by
// `make test-core PROVIDER=minimax`. It lives in the external test package to
// avoid the import cycle created when llmtests imports the core package, which
// in turn imports this provider.
func TestMiniMax(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")) == "" {
		t.Skip("Skipping MiniMax tests because MINIMAX_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.MiniMax,
		ChatModel:            "MiniMax-M3",
		SpeechSynthesisModel: "speech-2.8-turbo",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.MiniMax, Model: "MiniMax-M2.7"},
		},
		SkipEmptyToolSchemas: true,
		Scenarios: llmtests.TestScenarios{
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			SpeechSynthesis:            true,
			SpeechSynthesisStream:      true,
			ListModels:                 true,
		},
	}

	t.Run("MiniMaxTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
