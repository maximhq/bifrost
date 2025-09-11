package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunProviderSpecificTest executes the provider-specific test scenario
func RunProviderSpecificTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ProviderSpecific {
		t.Logf("Provider-specific tests not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ProviderSpecific", func(t *testing.T) {
		// This would contain provider-specific tests
		// For now, we'll do a basic functionality test
		messages := []schemas.ChatMessage{
			CreateBasicChatMessage("Test provider-specific functionality. What makes you unique?"),
		}

		// Initialize with default parameters and merge with custom parameters
		defaultParams := &schemas.ModelParameters{
			MaxTokens: bifrost.Ptr(150),
		}
		params := MergeModelParameters(defaultParams, testConfig.CustomParams)

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params:    params,
			Fallbacks: testConfig.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Provider-specific test failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")

		t.Logf("✅ Provider-specific result for %s: %s", testConfig.Provider, content)
	})
}
