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
		Provider:             schemas.HuggingFace,
		ChatModel:            "groq/openai/gpt-oss-120b",
		VisionModel:          "ovhcloud/Qwen/Qwen2.5-VL-72B-Instruct",
		EmbeddingModel:       "nebius/Qwen/Qwen3-Embedding-8B",
		TranscriptionModel:   "fal-ai/openai/whisper-large-v3",
		SpeechSynthesisModel: "fal-ai/hexgrad/Kokoro-82M",
		SpeechSynthesisFallbacks: []schemas.Fallback{
			{Provider: schemas.HuggingFace, Model: "fal-ai/hexgrad/Kokoro-82M"},
		},
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
			Transcription:         false,
			TranscriptionStream:   false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: false,
			Reasoning:             false,
			ListModels:            true,
		},
	}

	t.Run("HuggingFaceTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
