package fireworks_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestFireworks(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("FIREWORKS_API_KEY")) == "" {
		t.Skip("Skipping Fireworks tests because FIREWORKS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:                schemas.Fireworks,
		ChatModel:               "accounts/fireworks/models/llama-v3p1-70b-instruct",
		Fallbacks:               []schemas.Fallback{},
		TextModel:               "accounts/fireworks/models/llama-v3p1-70b-instruct",
		TextCompletionFallbacks: []schemas.Fallback{},
		EmbeddingModel:          "",
		ReasoningModel:          "",
		TranscriptionModel:      "",
		SpeechSynthesisModel:    "",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			FileBase64:            false,
			FileURL:               false,
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			Reasoning:             false,
			Transcription:         false,
			SpeechSynthesis:       false,
		},
	}
	t.Run("FireworksTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
