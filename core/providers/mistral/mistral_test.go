package mistral_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestMistral(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MISTRAL_API_KEY")) == "" {
		t.Skip("Skipping Mistral tests because MISTRAL_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Mistral,
		ChatModel: "mistral-medium-2508",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Mistral, Model: "mistral-small-2503"},
		},
		VisionModel:         "pixtral-12b-latest",
		EmbeddingModel:      "codestral-embed",
		TranscriptionModel:  "voxtral-mini-latest", // Mistral's audio transcription model
		ExternalTTSProvider: schemas.OpenAI,
		ExternalTTSModel:    "gpt-4o-mini-tts",
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
			FileBase64:            false, // supports documents url
			FileURL:               false, // bifrost limitation: native mistral api converter needed
			CompleteEnd2End:       true,
			Embedding:             true,
			Transcription:         true,
			TranscriptionStream:   true,
			ListModels:            true,
			Reasoning:             false, // Not supported right now because we are not using native mistral converters
		},
	}

	t.Run("MistralTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
