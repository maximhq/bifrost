package openai_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAI(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
		t.Skip("Skipping OpenAI tests because OPENAI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:           schemas.OpenAI,
		TextModel:          "gpt-3.5-turbo-instruct",
		ChatModel:          "gpt-4o",
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
		ReasoningModel:       "o4-mini", // o4-mini properly returns both reasoning items and message output
		ImageGenerationModel: "gpt-image-1",
		ImageEditModel:       "gpt-image-1",
		ImageVariationModel:  "dall-e-2",
		VideoGenerationModel: "sora-2",
		ChatAudioModel:       "gpt-4o-mini-audio-preview",
		Scenarios: llmtests.TestScenarios{
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
			ImageGeneration:       true,
			ImageGenerationStream: true,
			ImageEdit:             true,
			ImageEditStream:       true,
			ImageVariation:        true,
			VideoGeneration:       false, // disabled for now because of long running operations
			VideoRetrieve:         false,
			VideoRemix:            false,
			VideoDownload:         false,
			VideoList:             false,
			VideoDelete:           false,
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
			ContainerCreate:       true,
			ContainerList:         true,
			ContainerRetrieve:     true,
			ContainerDelete:       true,
			ContainerFileCreate:   true,
			ContainerFileList:     true,
			ContainerFileRetrieve: true,
			ContainerFileContent:  true,
			ContainerFileDelete:   true,
		},
	}

	t.Run("OpenAITests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
