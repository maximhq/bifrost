package schemas

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func warpConfigWithModels() *WarpConfig {
	return &WarpConfig{
		Enabled: true, Provider: OpenAI, Model: "gpt-4o", APIKeyID: "key-default",
		AdditionalModels: []WarpModel{
			{Provider: Anthropic, Model: "claude-sonnet-5", APIKeyID: "key-anthropic"},
			{Provider: OpenAI, Model: "gpt-4o-mini"},
		},
		MaxIterations: 5,
	}
}

// The default leads, so a switcher that lists Models() in order shows the
// model a request with no selection actually runs on first.
func TestWarpConfigModelsListsTheDefaultFirst(t *testing.T) {
	require.Equal(t, []WarpModel{
		{Provider: OpenAI, Model: "gpt-4o", APIKeyID: "key-default"},
		{Provider: Anthropic, Model: "claude-sonnet-5", APIKeyID: "key-anthropic"},
		{Provider: OpenAI, Model: "gpt-4o-mini"},
	}, warpConfigWithModels().Models())

	// A draft with no default yet has no default to list.
	draft := &WarpConfig{AdditionalModels: []WarpModel{{Provider: Anthropic, Model: "claude-sonnet-5"}}}
	require.Equal(t, []WarpModel{{Provider: Anthropic, Model: "claude-sonnet-5"}}, draft.Models())

	var missing *WarpConfig
	require.Empty(t, missing.Models())
}

func TestWarpConfigForModelSelectsAnExposedModel(t *testing.T) {
	config := warpConfigWithModels()

	// No selection is the default, and the same config rather than a copy.
	selected, ok := config.ForModel("", "")
	require.True(t, ok)
	require.Same(t, config, selected)

	selected, ok = config.ForModel(Anthropic, "claude-sonnet-5")
	require.True(t, ok)
	require.Equal(t, Anthropic, selected.Provider)
	require.Equal(t, "claude-sonnet-5", selected.Model)
	require.Equal(t, "key-anthropic", selected.APIKeyID)
	require.Equal(t, 5, selected.MaxIterations, "everything but the model carries over")

	// The entry's own key replaces the default's, an empty one included: the
	// default's pinned key belongs to a different model's provider pool.
	selected, ok = config.ForModel(OpenAI, "gpt-4o-mini")
	require.True(t, ok)
	require.Empty(t, selected.APIKeyID)

	// Selecting must not move the stored default.
	require.Equal(t, OpenAI, config.Provider)
	require.Equal(t, "gpt-4o", config.Model)
	require.Equal(t, "key-default", config.APIKeyID)
}

// The pair is client-sent. Anything the operator did not expose has to be
// refused, including a real model under the wrong provider and half a pair.
func TestWarpConfigForModelRefusesAnUnexposedModel(t *testing.T) {
	config := warpConfigWithModels()
	for name, pair := range map[string]WarpModel{
		"unknown model":        {Provider: OpenAI, Model: "gpt-5"},
		"model under another":  {Provider: Anthropic, Model: "gpt-4o"},
		"provider without one": {Provider: OpenAI},
		"model without one":    {Model: "gpt-4o"},
	} {
		t.Run(name, func(t *testing.T) {
			selected, ok := config.ForModel(pair.Provider, pair.Model)
			require.False(t, ok)
			require.Nil(t, selected)
		})
	}

	var missing *WarpConfig
	_, ok := missing.ForModel("", "")
	require.False(t, ok)
}
