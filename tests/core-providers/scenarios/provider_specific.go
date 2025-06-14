package scenarios

import (
	"context"
	config "core-providers-test/config"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunProviderSpecificTest executes the provider-specific test scenario
func RunProviderSpecificTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.ProviderSpecific {
		t.Logf("Provider-specific tests not configured for provider %s", config.Provider)
		return
	}

	t.Run("ProviderSpecific", func(t *testing.T) {
		// This would contain provider-specific tests
		// For now, we'll do a basic functionality test
		messages := []schemas.BifrostMessage{
			CreateBasicChatMessage("Test provider-specific functionality. What makes you unique?"),
		}

		var params *schemas.ModelParameters
		if config.CustomParams != nil {
			params = config.CustomParams
		} else {
			params = &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(150),
			}
		}

		request := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params:    params,
			Fallbacks: config.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Provider-specific test failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")

		t.Logf("✅ Provider-specific result for %s: %s", config.Provider, content)
	})
}
