package bedrockmantle_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestBedrockMantle runs the comprehensive harness against the bedrock_mantle provider.
//
// It is gated on AWS credentials (the SigV4 path needs them). The Claude scenarios exercise the
// native-Anthropic Messages surface; gpt-oss exercises the OpenAI-compatible surface. Only the
// operations the provider actually implements are enabled — everything else (embeddings, rerank,
// batch, files, image edit/variation, count tokens, text completion) is an unsupported stub.
//
// The model ids live in the harness account (GetKeysForProvider, case BedrockMantle); if a model
// is reported as not found, tune the aliases there to whatever the mantle endpoints accept.
func TestBedrockMantle(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" || strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) == "" {
		t.Skip("Skipping Bedrock Mantle tests because AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:           schemas.BedrockMantle,
		ChatModel:          "anthropic.claude-haiku-4-5",
		PromptCachingModel: "anthropic.claude-opus-4-8",
		VisionModel:        "anthropic.claude-haiku-4-5",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.BedrockMantle, Model: "anthropic.claude-opus-4-8"},
		},
		ReasoningModel:           "anthropic.claude-opus-4-8",
		InterleavedThinkingModel: "anthropic.claude-opus-4-8",
		Scenarios: llmtests.TestScenarios{
			// Supported: chat + responses surfaces (native-Anthropic and OpenAI-compatible).
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageBase64:                true, // Claude vision (native-Anthropic)
			CompleteEnd2End:            true,
			ListModels:                 true,
			Reasoning:                  true,
			InterleavedThinking:        true,
			EagerInputStreaming:        true,
			StructuredOutputs:          true,
			PromptCaching:              true,

			// Unsupported by the mantle provider (unsupported-operation stubs).
			TextCompletion: false,
			ImageURL:       false, // native-Anthropic does not accept image URLs
			MultipleImages: false,
			FileBase64:     false,
			FileURL:        false,
			Embedding:      false,
			Rerank:         false,
			BatchCreate:    false,
			BatchList:      false,
			BatchRetrieve:  false,
			BatchCancel:    false,
			BatchResults:   false,
			FileUpload:     false,
			FileList:       false,
			FileRetrieve:   false,
			FileDelete:     false,
			FileContent:    false,
			FileBatchInput: false,
			CountTokens:    false,
			ImageEdit:      false,
			ImageVariation: false,
		},
	}

	t.Run("BedrockMantleTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
