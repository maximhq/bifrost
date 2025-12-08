package gemini_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGemini(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" {
		t.Skip("Skipping Gemini tests because GEMINI_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:             schemas.Gemini,
		ChatModel:            "gemini-2.0-flash",
		VisionModel:          "gemini-2.0-flash",
		EmbeddingModel:       "text-embedding-004",
		TranscriptionModel:   "gemini-2.5-flash",
		SpeechSynthesisModel: "gemini-2.5-flash-preview-tts",
		SpeechSynthesisFallbacks: []schemas.Fallback{
			{Provider: schemas.Gemini, Model: "gemini-2.5-pro-preview-tts"},
		},
		ReasoningModel: "gemini-3-pro-preview",
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
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
			Reasoning:             true,
			ListModels:            true,
		},
	}

	t.Run("GeminiTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
