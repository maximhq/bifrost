package orcarouter_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOrcaRouter(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ORCAROUTER_API_KEY")) == "" {
		t.Skip("Skipping OrcaRouter tests because ORCAROUTER_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.OrcaRouter,
		ChatModel:      "openai/gpt-5.5",
		VisionModel:    "google/gemini-3.5-flash",
		TextModel:      "deepseek/deepseek-v4-pro",
		EmbeddingModel: "openai/text-embedding-3-large",
		ReasoningModel: "deepseek/deepseek-v4-pro",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  false, // OrcaRouter's /v1/responses returns 500 when tool_choice is an object (upstream gateway bug)
			ToolCallsStreaming:         false, // OrcaRouter's responses API is in Beta
			MultipleToolCalls:          false, // object tool_choice -> OrcaRouter 500 (upstream gateway bug)
			MultipleToolCallsStreaming: false, // OrcaRouter's responses API is in Beta
			End2EndToolCalling:         false, // object tool_choice -> OrcaRouter 500 (upstream gateway bug)
			AutomaticFunctionCall:      false, // object tool_choice -> OrcaRouter 500 (upstream gateway bug)
			ImageURL:                   false, // OrcaRouter's responses API is in Beta
			ImageBase64:                false, // OrcaRouter's responses API is in Beta
			MultipleImages:             false, // OrcaRouter's responses API is in Beta
			FileBase64:                 true,
			FileURL:                    true,
			CompleteEnd2End:            false, // OrcaRouter's responses API is in Beta
			Reasoning:                  true,
			ListModels:                 true,
			StructuredOutputs:          false, // json_schema tool_choice -> OrcaRouter 500 (upstream gateway bug)
			Embedding:                  true,
		},
	}

	t.Run("OrcaRouterTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
