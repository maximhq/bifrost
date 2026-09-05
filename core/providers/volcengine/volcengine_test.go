package volcengine_test

import (
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/volcengine"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestNewVolcengineProviderDefaultBaseURL(t *testing.T) {
	t.Parallel()

	provider, err := volcengine.NewVolcengineProvider(&schemas.ProviderConfig{}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewVolcengineProvider: %v", err)
	}
	if got := provider.GetProviderKey(); got != schemas.Volcengine {
		t.Fatalf("GetProviderKey() = %q, want %q", got, schemas.Volcengine)
	}
}

func TestVolcengine(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("VOLCENGINE_API_KEY")) == "" {
		t.Skip("Skipping Volcengine tests because VOLCENGINE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Volcengine,
		ChatModel: "doubao-seed-2-1-turbo-260628",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Volcengine, Model: "doubao-seed-2-1-turbo-260628"},
			{Provider: schemas.Volcengine, Model: "doubao-seed-2-1-pro-260628"},
		},
		TextModel:            "",
		EmbeddingModel:       "doubao-embedding-large",
		ReasoningModel:       "doubao-seed-2-1-pro-260628",
		ImageGenerationModel: "doubao-seedream-4-5-251128",
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

	t.Run("VolcengineTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
