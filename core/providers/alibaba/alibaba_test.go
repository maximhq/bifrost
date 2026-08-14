package alibaba_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAlibaba(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ALIBABA_API_KEY")) == "" {
		t.Skip("Skipping Alibaba tests because ALIBABA_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Alibaba,
		ChatModel: "qwen-flash", // cheap; thinking OFF by default (thinking-on models burn tight harness token caps on reasoning)
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Alibaba, Model: "qwen-flash"},
			{Provider: schemas.Alibaba, Model: "qwen-turbo"},
		},
		TextModel:      "qwen-flash",
		EmbeddingModel: "text-embedding-v4",
		ReasoningModel: "qwen3.7-plus", // Responses mount: accepts reasoning.effort high/xhigh (qwen3.6-flash 400s on them — vendor budget-mapping quirk)
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			// DashScope's /responses mount cannot ingest tool results: its agent
			// backend rewrites function_call_output items to role:"tool" messages and
			// then rejects them ("tool must be one of user,assistant,system,function"),
			// while the outer layer rejects role:"function". Dual-API tool-continuation
			// scenarios would fail on the Responses leg for every model — vendor gap.
			CompleteEnd2End:        false,
			End2EndToolCalling:     false,
			Embedding:              true,
			ListModels:             true,
			Reasoning:              true,
			PassThroughExtraParams: true,
		},
	}

	t.Run("AlibabaTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
