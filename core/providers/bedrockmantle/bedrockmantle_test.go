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
// batch, files, image edit/variation, text completion) is an unsupported stub.
//
// CountTokens runs against ChatModel, which is a Claude id: the count_tokens path lives on the
// native-Anthropic surface only, so pointing this scenario at a gpt-oss/Gemma model would
// (correctly) return an unsupported-operation error.
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
			CountTokens:                true, // native-Anthropic /anthropic/v1/messages/count_tokens

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
			ImageEdit:      false,
			ImageVariation: false,
		},
	}

	t.Run("BedrockMantleTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// TestBedrockMantleOpenAICompatible exercises the OpenAI-compatible surface, which
// TestBedrockMantle does not reach: every model role there is a Claude id, so the
// whole "v1" vs "openai/v1" base-path split ran untested — which is how a frontier
// generation came to be routed to the path that rejects it.
//
// The model carries a "us-west-2/" addressing prefix because mantle serves Astra in
// that region only, while the key's region (AWS_REGION, default us-east-1) is where
// the Claude scenarios run. resolveRegion consults the model prefix ahead of the key,
// so both functions run from one invocation with no region switch.
func TestBedrockMantleOpenAICompatible(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" || strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) == "" {
		t.Skip("Skipping Bedrock Mantle OpenAI-compatible tests because AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	const astra = "us-west-2/openai.gpt-6-astra"

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.BedrockMantle,
		ChatModel:      astra,
		VisionModel:    astra,
		ReasoningModel: astra,
		// A different region and generation, so the fallback also covers the
		// prefix being honoured per attempt rather than once per key.
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.BedrockMantle, Model: "us-east-1/openai.gpt-5.6-sol"},
		},
		// Astra's chat stream carries no reasoning content to assert on — 120-136
		// chunks with no reasoning indicators across ten attempts — while its
		// Responses stream carries it. Same shape as the tools split below.
		SkipChatReasoning: true,
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			CompleteEnd2End:       false,
			StructuredOutputs:     true,
			ImageBase64:           true,
			Reasoning:             true,

			// Astra rejects function tools on the chat surface and says so:
			// "Function tools with reasoning_effort are not supported for
			// gpt-6-astra in /v1/chat/completions. To use function tools, use
			// /v1/responses." Its thinking is always on, so there is no
			// effort-free chat request to fall back to. Every tool scenario drives
			// the chat and Responses variants off one flag, so the Responses half
			// (which passes) goes dark with them until a datasheet row marks the
			// chat endpoint unsupported here and markForConversion routes these to
			// /v1/responses.
			ToolCalls:                  false,
			ToolCallsStreaming:         false,
			MultipleToolCalls:          false,
			MultipleToolCallsStreaming: false,
			End2EndToolCalling:         false,
			AutomaticFunctionCall:      false,
			EagerInputStreaming:        false,

			// CountTokens lives on the native-Anthropic surface only.
			CountTokens: false,
			// Bedrock lists explicit prompt caching for the GPT-5.6 family, not this one.
			PromptCaching: false,
			// InterleavedThinking is an Anthropic-surface capability.
			InterleavedThinking: false,
			// ListModels is provider-scoped, so TestBedrockMantle already covers it.
			ListModels: false,
		},
	}

	t.Run("BedrockMantleOpenAICompatibleTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
