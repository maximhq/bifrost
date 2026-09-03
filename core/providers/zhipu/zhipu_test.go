package zhipu_test

import (
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/zhipu"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestNewZhipuProviderDefaultBaseURL(t *testing.T) {
	t.Parallel()

	provider, err := zhipu.NewZhipuProvider(&schemas.ProviderConfig{}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewZhipuProvider: %v", err)
	}
	if got := provider.GetProviderKey(); got != schemas.Zhipu {
		t.Fatalf("GetProviderKey() = %q, want %q", got, schemas.Zhipu)
	}
}

func TestZhipu(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ZHIPU_API_KEY")) == "" {
		t.Skip("Skipping Zhipu tests because ZHIPU_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Zhipu,
		ChatModel: "glm-5.3",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Zhipu, Model: "glm-5.3"},
			{Provider: schemas.Zhipu, Model: "glm-4.6"},
		},
		TextModel:            "",
		EmbeddingModel:       "embedding-3",
		ReasoningModel:       "glm-5.3",
		ImageGenerationModel: "cogview-4",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false,
			TextCompletionStream:       false,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
			CompleteEnd2End:            true,
			Embedding:                  true,
			ListModels:                 true,
			Reasoning:                  true,
			ImageGeneration:            true,
		},
	}

	t.Run("ZhipuTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
