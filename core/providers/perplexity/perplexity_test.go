package perplexity_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPerplexity(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("PERPLEXITY_API_KEY")) == "" {
		t.Skip("Skipping Perplexity tests because PERPLEXITY_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Perplexity,
		ChatModel:      "sonar-pro",
		TextModel:      "", // Perplexity doesn't support text completion
		EmbeddingModel: "", // Perplexity doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			TextCompletion:         false, // Not supported
			SimpleChat:             true,
			CompletionStream:       true,
			MultiTurnConversation:  true,
			ToolCalls:              false,
			MultipleToolCalls:      false,
			End2EndToolCalling:     false,
			AutomaticFunctionCall:  false,
			ImageURL:               false, // Not supported yet
			ImageBase64:            false, // Not supported yet
			MultipleImages:         false, // Not supported yet
			CompleteEnd2End:        false,
			FileBase64:             false,
			FileURL:                false,
			Embedding:              false, // Not supported yet
			ListModels:             false,
			PassThroughExtraParams: true,
		},
	}

	t.Run("PerplexityTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
