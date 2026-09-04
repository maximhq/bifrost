package warp

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Warp speaks OpenAI to Bifrost's compatibility mount whatever provider is
// configured. Declaring the configured provider instead makes that provider's
// implementation build its own path - Anthropic asks for /v1/messages, which
// under /openai is not a route and comes back as "Method Not Allowed".
func TestWarpAccountSpeaksOpenAIRegardlessOfConfiguredProvider(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Bedrock, schemas.Vertex, schemas.OpenAI} {
		account := &warpAccount{config: &schemas.WarpConfig{Provider: provider, Model: "some-model"}}
		declared, err := account.GetConfiguredProviders()
		require.NoError(t, err)
		require.Equal(t, []schemas.ModelProvider{schemas.OpenAI}, declared,
			"%s must still be reached over the OpenAI wire format", provider)
	}
}

// The configured provider is not lost, it moves into the model string - which is
// what actually routes the request once it reaches Bifrost.
func TestWarpModelCarriesConfiguredProvider(t *testing.T) {
	require.Equal(t, "anthropic/claude-sonnet-5",
		modelForRequest(&schemas.WarpConfig{Provider: schemas.Anthropic, Model: "claude-sonnet-5"}))
	// An operator who typed the qualified form gets exactly what they typed.
	require.Equal(t, "vertex/gemini-2.5-pro",
		modelForRequest(&schemas.WarpConfig{Provider: schemas.Anthropic, Model: "vertex/gemini-2.5-pro"}))
}
