package openrouter_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenRouter(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
		t.Skip("Skipping OpenRouter tests because OPENROUTER_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.OpenRouter,
		ChatModel:            "openai/gpt-4.1",
		VisionModel:          "openai/gpt-4o",
		TextModel:            "google/gemini-2.5-flash",
		EmbeddingModel:       "qwen/qwen3-embedding-4b",
		ReasoningModel:       "openai/gpt-oss-120b",
		TranscriptionModel:   "openai/gpt-4o-mini-transcribe",
		SpeechSynthesisModel: "openai/gpt-audio-mini",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         false, // OpenRouter's responses API is in Beta
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false, // OpenRouter's responses API is in Beta
			ImageBase64:                false, // OpenRouter's responses API is in Beta
			MultipleImages:             false, // OpenRouter's responses API is in Beta
			FileBase64:                 false, // Responses API times out (300s+) with file input
			FileURL:                    false, // Responses API times out (300s+) with file input
			CompleteEnd2End:            false, // OpenRouter's responses API is in Beta
			Reasoning:                  true,
			ListModels:                 true,
			StructuredOutputs:          true, // Structured outputs with nullable enum support
			Embedding:                  true,
			SpeechSynthesis:            true,  // Supported via OpenAI-compatible /v1/audio/speech
			SpeechSynthesisStream:      false, // Streaming not offered by upstream OpenRouter API
			Transcription:              true,  // Supported via OpenAI-compatible /v1/audio/transcriptions
			TranscriptionStream:        false, // Streaming not offered by upstream OpenRouter API
		},
	}

	t.Run("OpenRouterTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
