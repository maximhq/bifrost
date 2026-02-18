package sapaicore_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestSAPAICore(t *testing.T) {
	t.Parallel()

	// Check for required environment variables
	requiredEnvVars := []string{
		"SAP_AI_CORE_CLIENT_ID",
		"SAP_AI_CORE_CLIENT_SECRET",
		"SAP_AI_CORE_AUTH_URL",
		"SAP_AI_CORE_BASE_URL",
		"SAP_AI_CORE_RESOURCE_GROUP",
	}

	for _, envVar := range requiredEnvVars {
		if strings.TrimSpace(os.Getenv(envVar)) == "" {
			t.Skipf("Skipping SAP AI Core tests because %s is not set", envVar)
		}
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.SAPAICore,
		ChatModel: "gpt-4o", // OpenAI backend through SAP AI Core
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.SAPAICore, Model: "gpt-4o-mini"},
		},
		EmbeddingModel: "text-embedding-3-small",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			Embedding:             true,
			ListModels:            true,
		},
	}

	t.Run("SAPAICoreTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
