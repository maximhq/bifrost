package openrouter_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenRouter(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
		t.Skip("Skipping OpenRouter tests because OPENROUTER_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:       schemas.OpenRouter,
		ChatModel:      "openai/gpt-4o",
		VisionModel:    "openai/gpt-4o",
		TextModel:      "google/gemini-2.5-flash",
		EmbeddingModel: "",
		ReasoningModel: "openai/gpt-oss-120b",
		Scenarios: testutil.TestScenarios{
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
			CompleteEnd2End:       false, // OpenRouter's responses API is in Beta
			Reasoning:             true,
			ListModels:            true,
		},
	}

	t.Run("OpenRouterTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
