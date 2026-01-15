package xai_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestXAI(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("XAI_API_KEY")) == "" {
		t.Skip("Skipping XAI tests because XAI_API_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:        schemas.XAI,
		ChatModels:      []string{"grok-4-0709"},
		ReasoningModels: []string{"grok-3-mini"},
		TextModels:      []string{"grok-3"},
		VisionModels:    []string{"grok-2-vision-1212"},
		EmbeddingModels: []string{""}, // XAI doesn't support embedding
		Scenarios: testutil.TestScenarios{
			TextCompletion:        true,
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
			FileBase64:            false,
			FileURL:               false,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Reasoning:             true,
			Embedding:             false,
			ListModels:            true,
		},
	}

	t.Run("XAITests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
