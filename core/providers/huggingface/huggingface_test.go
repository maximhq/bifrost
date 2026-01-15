package huggingface_test

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestHuggingface(t *testing.T) {
	t.Parallel()
	if os.Getenv("HUGGING_FACE_API_KEY") == "" {
		t.Skip("Skipping HuggingFace tests because HUGGING_FACE_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:              schemas.HuggingFace,
		ChatModels:            []string{"sambanova/meta-llama/Llama-3.1-8B-Instruct"},
		VisionModels:          []string{"cohere/CohereLabs/aya-vision-32b"},
		EmbeddingModels:       []string{"sambanova/intfloat/e5-mistral-7b-instruct"},
		TranscriptionModels:   []string{"fal-ai/openai/whisper-large-v3"},
		SpeechSynthesisModels: []string{"fal-ai/hexgrad/Kokoro-82M"},
		SpeechSynthesisFallbacks: []schemas.Fallback{
			{Provider: schemas.HuggingFace, Model: "fal-ai/ResembleAI/chatterbox"},
		},
		ReasoningModels: []string{"groq/openai/gpt-oss-120b"},
		Scenarios: testutil.TestScenarios{
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
		},
	}

	t.Run("HuggingFaceTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
