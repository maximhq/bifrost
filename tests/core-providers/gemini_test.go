package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGemini(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("Skipping Gemini tests because GEMINI_API_KEY is not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := config.ComprehensiveTestConfig{
		Provider:             schemas.Gemini,
		ChatModel:            "gemini-2.0-flash",
		VisionModel:          "gemini-2.0-flash",
		EmbeddingModel:       "text-embedding-004",
		TranscriptionModel:   "gemini-2.5-flash",
		SpeechSynthesisModel: "gemini-2.5-flash-preview-tts",
		SpeechSynthesisFallbacks: []schemas.Fallback{
			{Provider: schemas.Gemini, Model: "gemini-2.5-pro-preview-tts"},
		},
		ReasoningModel: "gemini-2.5-pro",
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           true,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             true,
			Transcription:         false,
			TranscriptionStream:   false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Reasoning:             false, //TODO: Supported but lost since we map Gemini's responses via chat completions, fix is a native Gemini handler or reasoning support in chat completions
		},
	}

	t.Run("GeminiTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
