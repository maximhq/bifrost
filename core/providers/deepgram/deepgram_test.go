package deepgram_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestDeepgram(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("DEEPGRAM_API_KEY")) == "" {
		t.Skip("Skipping Deepgram tests because DEEPGRAM_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	hasRealtimeAgent := false

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Deepgram,
		SpeechSynthesisModel: "aura-2-andromeda-en",
		TranscriptionModel:   "nova-3",
		// RealtimeModel:        realtimeAgentID,
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            false,
			CompletionStream:      false,
			MultiTurnConversation: false,
			ToolCalls:             false,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   false,
			Embedding:             false,
			Reasoning:             false,
			ListModels:            true,
			Realtime:              hasRealtimeAgent,
		},
	}

	t.Run("DeepgramTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
