package kimi_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestKimi(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("KIMI_API_KEY")) == "" {
		t.Skip("Skipping Kimi tests because KIMI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Kimi,
		ChatModel: "kimi-k2.6",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Kimi, Model: "kimi-k2.6"},
			{Provider: schemas.Kimi, Model: "kimi-k3"},
		},
		TextModel:      "kimi-k2.6",
		EmbeddingModel: "", // Kimi doesn't support embeddings
		ReasoningModel: "kimi-k3",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			// Kimi vision accepts base64/ms:// refs only (no public image URLs);
			// exercise vision during dogfooding instead.
			ImageURL:        false,
			ImageBase64:     false,
			MultipleImages:  false,
			CompleteEnd2End: true,
			Embedding:       false,
			ListModels:      true,
			// The Reasoning scenario runs via the Responses API, which Kimi does
			// not expose — reasoning_effort routing is covered by unit tests and
			// dogfood smoke tests instead.
			Reasoning:              false,
			PassThroughExtraParams: true,
		},
	}

	t.Run("KimiTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
