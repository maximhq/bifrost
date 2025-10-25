package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAI(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping OpenAI tests because OPENAI_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.OpenAI,
		TextModel: "gpt-3.5-turbo-instruct",
		ChatModel: "gpt-4o-mini",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o"},
		},
		VisionModel:        "gpt-4o",
		EmbeddingModel:     "text-embedding-3-small",
		TranscriptionModel: "gpt-4o-transcribe",
		TranscriptionFallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "whisper-1"},
		},
		SpeechSynthesisModel: "gpt-4o-mini-tts",
		ReasoningModel:       "gpt-5",
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   true,
			Embedding:             true,
			Reasoning:             true,
			ListModels:            true,
		},
	}

	t.Run("OpenAITests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
