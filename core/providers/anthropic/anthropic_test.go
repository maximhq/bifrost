package anthropic_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAnthropic(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		t.Skip("Skipping Anthropic tests because ANTHROPIC_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Anthropic,
		ChatModel: "claude-sonnet-4-5",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-7-sonnet-20250219"},
			{Provider: schemas.Anthropic, Model: "claude-sonnet-4-20250514"},
		},
		VisionModel:        "claude-3-7-sonnet-20250219", // Same model supports vision
		ReasoningModel:     "claude-opus-4-5",
		PromptCachingModel: "claude-sonnet-4-20250514",
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
			WebSearchTool:         true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			FileBase64:            true,
			FileURL:               true,
			CompleteEnd2End:       true,
			Embedding:             false,
			Reasoning:             true,
			PromptCaching:         true,
			ListModels:            true,
			BatchCreate:           true,
			BatchList:             true,
			BatchRetrieve:         true,
			BatchCancel:           true,
			BatchResults:          true,
			FileUpload:            true,
			FileList:              true,
			FileRetrieve:          true,
			FileDelete:            true,
			FileContent:           false,
			FileBatchInput:        false, // Anthropic batch API only supports inline requests, not file-based input
			CountTokens:           true,
			StructuredOutputs:     true, // Structured outputs with nullable enum support
		},
	}

	t.Run("AnthropicTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
