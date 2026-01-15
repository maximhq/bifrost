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
		Provider: schemas.OpenAI,
		// Test multiple text completion models
		TextModels: []string{"gpt-3.5-turbo-instruct"},
		// Test multiple chat models - all must pass for test to succeed
		ChatModels: []string{
			"gpt-4o-mini",
			"gpt-4o",
		},
		PromptCachingModels: []string{"gpt-4.1"},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o"},
		},
		VisionModels:        []string{"gpt-4o"},
		EmbeddingModels:     []string{"text-embedding-3-small"},
		TranscriptionModels: []string{"gpt-4o-transcribe"},
		TranscriptionFallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "whisper-1"},
		},
		SpeechSynthesisModels: []string{"gpt-4o-mini-tts"},
		ReasoningModels:       []string{"o1"},
		ChatAudioModels:       []string{"gpt-4o-mini-audio-preview"},
		Scenarios: testutil.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			WebSearchTool:         true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			FileBase64:            true,
			FileURL:               true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   true,
			Embedding:             true,
			Reasoning:             true,
			ListModels:            true,
			BatchCreate:           true,
			BatchList:             true,
			BatchRetrieve:         true,
			BatchCancel:           true,
			BatchResults:          true,
			FileUpload:            true,
			FileList:              true,
			FileRetrieve:          true,
			FileDelete:            true,
			FileContent:           true,
			FileBatchInput:        true,
			CountTokens:           true,
			ChatAudio:             true,
			StructuredOutputs:     true, // Structured outputs with nullable enum support
		},
	}

	t.Run("OpenAITests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
