package openai_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAI(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
		t.Skip("Skipping OpenAI tests because OPENAI_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:           schemas.OpenAI,
		TextModel:          "gpt-3.5-turbo-instruct",
		ChatModel:          "gpt-4o-mini",
		PromptCachingModel: "gpt-4.1",
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
		ReasoningModel:       "o1",
		ImageGenerationModel: "gpt-image-1",
		ChatAudioModel:       "gpt-4o-mini-audio-preview",
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            false,
			CompletionStream:      false,
			MultiTurnConversation: false,
			ToolCalls:             false,
			ToolCallsStreaming:    false,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			FileBase64:            false,
			FileURL:               false,
			CompleteEnd2End:       false,
			SpeechSynthesis:       false,
			SpeechSynthesisStream: false,
			Transcription:         false,
			TranscriptionStream:   false,
			Embedding:             false,
			Reasoning:             false,
			ListModels:            false,
			ImageGeneration:       true,
			ImageGenerationStream: true,
			BatchCreate:           false,
			BatchList:             false,
			BatchRetrieve:         false,
			BatchCancel:           false,
			BatchResults:          false,
			FileUpload:            false,
			FileList:              false,
			FileRetrieve:          false,
			FileDelete:            false,
			FileContent:           false,
			FileBatchInput:        false,
			CountTokens:           false,
			ChatAudio:             false,
			StructuredOutputs:     false, // Structured outputs with nullable enum support
		},
	}

	t.Run("OpenAITests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
