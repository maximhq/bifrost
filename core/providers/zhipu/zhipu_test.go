package zhipu_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestZhipu(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ZHIPU_API_KEY")) == "" {
		t.Skip("Skipping Zhipu tests because ZHIPU_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Zhipu,
		ChatModel: "glm-4.7-flash", // free tier
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Zhipu, Model: "glm-4.7-flash"},
			{Provider: schemas.Zhipu, Model: "glm-4.5-flash"},
		},
		TextModel:      "glm-4.7-flash",
		EmbeddingModel: "", // Zhipu embeddings are not offered on api.z.ai intl
		ReasoningModel: "glm-5.2",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			// The Reasoning scenario runs via the Responses API, which Zhipu does
			// not expose — reasoning_effort routing is covered by unit tests and
			// dogfood smoke tests instead.
			Reasoning:              false,
			PassThroughExtraParams: true,
		},
	}

	t.Run("ZhipuTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
