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
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:           schemas.Azure,
		ChatModel:          "gpt-4o",
		PromptCachingModel: "gpt-4o",
		VisionModel:        "gpt-4o",
		ChatAudioModel:     "gpt-4o-mini-audio-preview",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Azure, Model: "gpt-4o"},
		},
		TextModel:            "", // Azure doesn't support text completion in newer models
		EmbeddingModel:       "text-embedding-ada-002",
		ReasoningModel:       "claude-opus-4-5",
		SpeechSynthesisModel: "gpt-4o-mini-tts",
		TranscriptionModel:   "whisper",
		ImageGenerationModel: "gpt-image-1",
		ImageEditModel:       "gpt-image-1",
		VideoGenerationModel: "sora-2",
		PassthroughModel:     "gpt-4o",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false, // Not supported
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   true,
			ImageBase64:                true,
			MultipleImages:             true,
			CompleteEnd2End:            true,
			Embedding:                  true,
			ListModels:                 true,
			Reasoning:                  true,
			ChatAudio:                  false,
			Transcription:              false, // Disabled for azure because of 3 calls/minute quota
			TranscriptionStream:        false, // Not properly supported yet by Azure
			SpeechSynthesis:            false, // Disabled for azure because of 3 calls/minute quota
			SpeechSynthesisStream:      false, // Disabled for azure because of 3 calls/minute quota
			StructuredOutputs:          true,  // Structured outputs with nullable enum support
			PromptCaching:              true,
			ImageGeneration:            false, // Skipped for Azure
			ImageGenerationStream:      false, // Skipped for Azure
			ImageEdit:                  false, // Model not deployed on Azure endpoint
			ImageEditStream:            false, // Model not deployed on Azure endpoint
			ImageVariation:             false, // Not supported by Azure
			VideoGeneration:            false, // disabled for now because of long running operations
			VideoDownload:              false,
			VideoRetrieve:              false,
			VideoRemix:                 false,
			VideoList:                  false,
			VideoDelete:                false,
			InterleavedThinking:          true,
			PassthroughAPI:               true,
			EagerInputStreaming:          true, // fine-grained-tool-streaming-2025-05-14 (Beta on Azure Foundry)
			ServerToolsViaOpenAIEndpoint: true, // web_search / web_fetch / code_execution on Azure per Table 20
		},
		DisableParallelFor: []string{"Transcription"}, // Azure Whisper has 3 calls/minute quota
	}

	t.Run("AzureTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
