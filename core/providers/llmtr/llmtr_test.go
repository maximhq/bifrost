package llmtr_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/llmtr"
	"github.com/maximhq/bifrost/core/schemas"
)

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)                   {}
func (nopLogger) Info(string, ...any)                    {}
func (nopLogger) Warn(string, ...any)                    {}
func (nopLogger) Error(string, ...any)                   {}
func (nopLogger) Fatal(string, ...any)                   {}
func (nopLogger) SetLevel(schemas.LogLevel)              {}
func (nopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (nopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestNewLlmtrProviderDefaultBaseURL(t *testing.T) {
	t.Parallel()
	provider, err := llmtr.NewLlmtrProvider(&schemas.ProviderConfig{}, nopLogger{})
	if err != nil {
		t.Fatalf("NewLlmtrProvider: %v", err)
	}
	if provider.GetProviderKey() != schemas.Llmtr {
		t.Fatalf("GetProviderKey = %q, want %q", provider.GetProviderKey(), schemas.Llmtr)
	}
}

func TestLlmtr(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("LLMTR_API_KEY")) == "" {
		t.Skip("Skipping LLMTR tests because LLMTR_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Llmtr,
		ChatModel:      "minimax/minimax-m3.1-flash",
		TextModel:      "",
		EmbeddingModel: "",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			Reasoning:             false,
		},
	}

	t.Run("LlmtrTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
