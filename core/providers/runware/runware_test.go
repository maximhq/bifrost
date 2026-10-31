package runware_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestRunware(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("RUNWARE_API_KEY")) == "" {
		t.Skip("Skipping Runware tests because RUNWARE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider: schemas.Runware,
		// Text inference runs through Runware's OpenAI-compatible /v1/chat/completions endpoint.
		ChatModel:      "anthropic:claude@haiku-4.5",
		VisionModel:    "google:gemini@3-flash", // Anthropic models reject image parts through Runware
		ReasoningModel: "minimax:m2.7@0",
		// Runware rejects a function schema whose properties object is empty or absent, unlike
		// OpenAI, so the empty/nil-schema tool tests cannot pass against it.
		SkipEmptyToolSchemas: true,
		ImageGenerationModel: "runware:101@1",
		ImageEditModel:       "runware:102@1",             // FLUX Fill: supports seedImage + maskImage (inpainting)
		VideoGenerationModel: "klingai:kling-video@3-pro", // set a video model; flip the scenarios below on to exercise it
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			// Runware returns several tool calls on the Chat Completions stream, but the
			// chat-to-responses stream conversion only reads delta.ToolCalls[0], so the Responses
			// stream surfaces one. Same reason Wafer leaves this off.
			MultipleToolCallsStreaming: false,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   true,
			ImageBase64:                true,
			MultipleImages:             true,
			CompleteEnd2End:            true,
			Reasoning:                  true,
			ImageGeneration:            true,
			ImageEdit:                  true,
			VideoGeneration:            false,
			VideoRetrieve:              false,
			VideoDownload:              false,
		},
	}

	t.Run("RunwareTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
