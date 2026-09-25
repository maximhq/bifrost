package datasheet

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// web_search_cost_per_request is the single-valued form of the per-search rate and
// takes precedence over the tiered search_context_cost_per_query object.
func TestEntryUnmarshal_WebSearchCostPerRequest(t *testing.T) {
	t.Run("single value wins over tiered object", func(t *testing.T) {
		var e Entry
		require.NoError(t, sonic.UnmarshalString(`{
			"provider": "openai", "mode": "chat",
			"web_search_cost_per_request": 0.025,
			"search_context_cost_per_query": {
				"search_context_size_low": 0.01,
				"search_context_size_medium": 0.01,
				"search_context_size_high": 0.01
			}
		}`, &e))
		require.NotNil(t, e.SearchContextCostPerQuery)
		assert.Equal(t, 0.025, *e.SearchContextCostPerQuery)
	})

	t.Run("single value alone", func(t *testing.T) {
		var e Entry
		require.NoError(t, sonic.UnmarshalString(`{
			"provider": "openai", "mode": "chat",
			"web_search_cost_per_request": 0.025
		}`, &e))
		require.NotNil(t, e.SearchContextCostPerQuery)
		assert.Equal(t, 0.025, *e.SearchContextCostPerQuery)
	})

	t.Run("falls back to tiered object", func(t *testing.T) {
		var e Entry
		require.NoError(t, sonic.UnmarshalString(`{
			"provider": "anthropic", "mode": "chat",
			"search_context_cost_per_query": {
				"search_context_size_low": 0.008,
				"search_context_size_medium": 0.01,
				"search_context_size_high": 0.014
			}
		}`, &e))
		require.NotNil(t, e.SearchContextCostPerQuery)
		assert.Equal(t, 0.01, *e.SearchContextCostPerQuery)
	})

	t.Run("neither form leaves the rate unset", func(t *testing.T) {
		var e Entry
		require.NoError(t, sonic.UnmarshalString(`{"provider":"openai","mode":"chat"}`, &e))
		assert.Nil(t, e.SearchContextCostPerQuery)
	})
}
