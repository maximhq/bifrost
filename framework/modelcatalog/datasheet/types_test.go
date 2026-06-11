package datasheet

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Entry.UnmarshalJSON — per-query rerank pricing
// ---------------------------------------------------------------------------

func TestEntryUnmarshalInputCostPerQuery(t *testing.T) {
	var entry Entry
	err := sonic.Unmarshal([]byte(`{
		"provider": "cohere",
		"mode": "rerank",
		"input_cost_per_query": 0.002,
		"input_cost_per_token": 0,
		"output_cost_per_token": 0
	}`), &entry)

	require.NoError(t, err)
	require.NotNil(t, entry.SearchContextCostPerQuery)
	assert.Equal(t, 0.002, *entry.SearchContextCostPerQuery)
}

func TestEntryUnmarshalTieredSearchContextCostTakesPrecedence(t *testing.T) {
	var entry Entry
	err := sonic.Unmarshal([]byte(`{
		"provider": "perplexity",
		"mode": "chat",
		"input_cost_per_query": 0.002,
		"search_context_cost_per_query": {"search_context_size_medium": 0.01}
	}`), &entry)

	require.NoError(t, err)
	require.NotNil(t, entry.SearchContextCostPerQuery)
	assert.Equal(t, 0.01, *entry.SearchContextCostPerQuery)
}
