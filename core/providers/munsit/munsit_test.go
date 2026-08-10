package munsit_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestMunsit(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MUNSIT_API_KEY")) == "" {
		t.Skip("Skipping Munsit tests because MUNSIT_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	hasRealtimeAgent := true

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Munsit,
		SpeechSynthesisModel: "faseeh-v1-preview",
		TranscriptionModel:   "munsit",
		RealtimeModel:        "faseeh-v1-preview",
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

	t.Run("MunsitTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
