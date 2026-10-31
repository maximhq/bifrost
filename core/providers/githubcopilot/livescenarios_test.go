package githubcopilot_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestGithubCopilot is the entrypoint for `make test-core PROVIDER=githubcopilot`.
//
// It lives in the external test package because llmtests imports core, and core imports
// this provider. An internal test would close that cycle.
//
// Copilot has no free tier and no anonymous access, so this needs GITHUB_COPILOT_API_KEY: a
// fine-grained PAT with the Copilot Requests permission from an account with a Copilot seat,
// or a Copilot API token together with GITHUB_COPILOT_API_URL. Without it the test skips,
// matching how every other live provider suite in this repo is gated.
func TestGithubCopilot(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("GITHUB_COPILOT_API_KEY")) == "" {
		t.Skip("Skipping GitHub Copilot tests: set GITHUB_COPILOT_API_KEY to a fine-grained PAT " +
			"with the Copilot Requests permission, or to a Copilot API token with GITHUB_COPILOT_API_URL")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Model availability varies by plan tier and organization policy, so two operators with
	// valid credentials can see different catalogs. Confirm these against a live
	// GET /models before treating a failure here as a Bifrost bug.
	//
	// These are picked from GitHub's supported-models list rather than invented. gpt-4o is
	// deliberately not used: it no longer appears there and is not selectable in Copilot,
	// so it would fail for accounts whose credentials are perfectly valid.
	// https://docs.github.com/en/copilot/reference/ai-models/supported-models
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.GithubCopilot,
		ChatModel: "gpt-5.5",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.GithubCopilot, Model: "gpt-5.5"},
			{Provider: schemas.GithubCopilot, Model: "gpt-5.4-mini"},
		},
		TextModel:      "gpt-5.5",
		EmbeddingModel: "text-embedding-3-small",
		ReasoningModel: "gpt-5.5",
		VisionModel:    "gpt-5.5",
		Scenarios: llmtests.TestScenarios{
			// Copilot has no text completions endpoint; everything goes through chat.
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
			// Vision exercises the copilot-vision-request header, which Copilot requires on
			// image turns and rejects on text-only ones.
			ImageURL:        true,
			ImageBase64:     true,
			MultipleImages:  true,
			CompleteEnd2End: true,
			Embedding:       true,
			ListModels:      true,
			Reasoning:       false,
		},
	}

	t.Run("GithubCopilotTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})

	// Claude models route to Copilot's /v1/messages endpoint rather than /chat/completions,
	// so they get their own run. Copilot ids use dots ("claude-sonnet-4.6"), not dashes.
	claudeConfig := llmtests.ComprehensiveTestConfig{
		Provider:    schemas.GithubCopilot,
		ChatModel:   "claude-sonnet-4.6",
		VisionModel: "claude-sonnet-4.6",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			End2EndToolCalling:    true,
			ImageBase64:           true,
		},
	}
	t.Run("GithubCopilotClaudeTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, claudeConfig)
	})
}
