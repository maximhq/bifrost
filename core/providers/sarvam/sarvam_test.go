package sarvam_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSarvam(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("SARVAM_API_KEY")) == "" {
		t.Skip("Skipping Sarvam tests because SARVAM_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Sarvam,
		ChatModel: "sarvam-30b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Sarvam, Model: "sarvam-30b"},
			{Provider: schemas.Sarvam, Model: "sarvam-105b"},
		},
		EmbeddingModel: "", // Sarvam doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			MultiTurnConversation: true,
			ListModels:            true,
			// CompletionStream is left off: Sarvam's chat models are reasoning models that
			// emit 500+ reasoning chunks, tripping the harness's 500-chunk safety cap.
			// Streaming is delegated to the shared OpenAI handler and verified manually.
		},
	}

	t.Run("SarvamTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
