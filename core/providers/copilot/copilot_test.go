package copilot_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestCopilot(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("GITHUB_COPILOT_TOKEN")) == "" {
		t.Skip("Skipping Copilot tests because GITHUB_COPILOT_TOKEN is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Copilot,
		ChatModel: "gpt-4o",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Copilot, Model: "gpt-4o-mini"},
		},
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			ListModels:            true,
		},
	}

	t.Run("CopilotTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})

	client.Shutdown()
}
