package xai_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestXAI(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("XAI_API_KEY")) == "" {
		t.Skip("Skipping XAI tests because XAI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.XAI,
		ChatModel:            "grok-4-0709",
		ReasoningModel:       "grok-3-mini",
		TextModel:            "grok-3",
		VisionModel:          "grok-2-vision-1212",
		EmbeddingModel:       "", // XAI doesn't support embedding
		ImageGenerationModel: "grok-2-image",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			ImageGeneration:       true,
			ImageGenerationStream: false,
			FileBase64:            false,
			FileURL:               false,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Reasoning:             true,
			Embedding:             false,
			ListModels:            true,
		},
	}

	t.Run("XAITests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
