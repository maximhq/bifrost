package huggingface_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestHuggingface(t *testing.T) {
	t.Parallel()
	if os.Getenv("HUGGING_FACE_API_KEY") == "" {
		t.Skip("Skipping HuggingFace tests because HUGGING_FACE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.HuggingFace,
		ChatModel:            "groq/meta-llama/Llama-3.3-70B-Instruct",
		VisionModel:          "cohere/CohereLabs/aya-vision-32b",
		EmbeddingModel:       "sambanova/intfloat/e5-mistral-7b-instruct",
		TranscriptionModel:   "fal-ai/openai/whisper-large-v3",
		SpeechSynthesisModel: "fal-ai/hexgrad/Kokoro-82M",
		SpeechSynthesisFallbacks: []schemas.Fallback{
			{Provider: schemas.HuggingFace, Model: "fal-ai/ResembleAI/chatterbox"},
		},
		ReasoningModel:       "groq/openai/gpt-oss-120b",
		ImageGenerationModel: "fal-ai/fal-ai/flux/dev",
		ImageEditModel:       "fal-ai/fal-ai/flux-2/edit",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             true,
			Transcription:         true,
			TranscriptionStream:   false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: false,
			Reasoning:             true,
			ListModels:            true,
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
			ImageGeneration:       true,
			ImageGenerationStream: true,
			ImageEdit:             true,
			ImageEditStream:       true,
		},
	}

	t.Run("HuggingFaceTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
