package azure_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAzure(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("AZURE_API_KEY")) == "" {
		t.Skip("Skipping Azure tests because AZURE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Azure,
		ChatModel:      "gpt-4o-backup",
		VisionModel:    "gpt-4o",
		ChatAudioModel: "gpt-4o-mini-audio-preview",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Azure, Model: "gpt-4o-backup"},
		},
		TextModel:            "", // Azure doesn't support text completion in newer models
		EmbeddingModel:       "text-embedding-ada-002",
		ReasoningModel:       "claude-opus-4-5",
		SpeechSynthesisModel: "gpt-4o-mini-tts",
		TranscriptionModel:   "whisper",
		ImageGenerationModel: "gpt-image-1",
		ImageEditModel:       "gpt-image-1",

		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
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
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
			Reasoning:             true,
			ChatAudio:             true,
			Transcription:         true,
			TranscriptionStream:   false, // Not properly supported yet by Azure
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			StructuredOutputs:     true,  // Structured outputs with nullable enum support
			ImageGeneration:       false, // Skipped for Azure
			ImageGenerationStream: false, // Skipped for Azure
			ImageEdit:             false, // Model not deployed on Azure endpoint
			ImageEditStream:       false, // Model not deployed on Azure endpoint
			ImageVariation:        false, // Not supported by Azure
		},
		DisableParallelFor: []string{"Transcription"}, // Azure Whisper has 3 calls/minute quota
	}

	t.Run("AzureTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
