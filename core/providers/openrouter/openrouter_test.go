package openrouter_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenRouter(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
		t.Skip("Skipping OpenRouter tests because OPENROUTER_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.OpenRouter,
		ChatModel:      "openai/gpt-4.1",
		VisionModel:    "openai/gpt-4o",
		TextModel:      "google/gemini-2.5-flash",
		EmbeddingModel: "",
		ReasoningModel: "openai/gpt-oss-120b",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    false, // OpenRouter's responses API is in Beta
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // OpenRouter's responses API is in Beta
			ImageBase64:           false, // OpenRouter's responses API is in Beta
			MultipleImages:        false, // OpenRouter's responses API is in Beta
			FileBase64:            true,
			FileURL:               true,
			CompleteEnd2End:       false, // OpenRouter's responses API is in Beta
			Reasoning:             true,
			ListModels:            true,
			StructuredOutputs:     true, // Structured outputs with nullable enum support
		},
	}

	t.Run("OpenRouterTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
