package elevenlabs_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestElevenlabs(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")) == "" {
		t.Skip("Skipping Elevenlabs tests because ELEVENLABS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	realtimeAgentID := os.Getenv("ELEVENLABS_AGENT_ID")
	hasRealtimeAgent := strings.TrimSpace(realtimeAgentID) != ""

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Elevenlabs,
		SpeechSynthesisModel: "eleven_turbo_v2_5",
		TranscriptionModel:   "scribe_v1",
		RealtimeModel:        realtimeAgentID,
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
			ListModels:            false,
			Realtime:              hasRealtimeAgent,
		},
	}

	t.Run("ElevenlabsTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
