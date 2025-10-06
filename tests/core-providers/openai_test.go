package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAI(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:             schemas.OpenAI,
		TextModel:            "gpt-3.5-turbo-instruct",
		ChatModel:            "gpt-4o-mini",
		VisionModel:          "gpt-4o",
		EmbeddingModel:       "text-embedding-3-small",
		TranscriptionModel:   "whisper-1",
		SpeechSynthesisModel: "gpt-4o-mini-tts",
		ReasoningModel:       "gpt-5",
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   true,
			Embedding:             true,
			Reasoning:             true,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
