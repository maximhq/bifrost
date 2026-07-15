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

// TestEntryUnmarshalInputCostPerQuery verifies that the rerank datasheet key
// input_cost_per_query is parsed into its own first-class pricing option.
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
	require.NotNil(t, entry.InputCostPerQuery)
	assert.InDelta(t, 0.002, *entry.InputCostPerQuery, 1e-9)
	// The per-query rerank rate stays on its own field and never folds onto
	// the web-search rate.
	assert.Nil(t, entry.SearchContextCostPerQuery)
}

// TestEntryUnmarshalTieredSearchContextCost verifies the tiered
// search_context_cost_per_query object still collapses onto its single field
// and is unaffected by input_cost_per_query being present.
func TestEntryUnmarshalTieredSearchContextCost(t *testing.T) {
	var entry Entry
	err := sonic.Unmarshal([]byte(`{
		"provider": "cohere",
		"mode": "rerank",
		"input_cost_per_query": 0.002,
		"search_context_cost_per_query": {"search_context_size_medium": 0.01}
	}`), &entry)

	require.NoError(t, err)
	require.NotNil(t, entry.SearchContextCostPerQuery)
	assert.InDelta(t, 0.01, *entry.SearchContextCostPerQuery, 1e-9)
	require.NotNil(t, entry.InputCostPerQuery)
	assert.InDelta(t, 0.002, *entry.InputCostPerQuery, 1e-9)
}

// TestEntryPricingRoundTripInputCostPerQuery verifies input_cost_per_query
// survives the Entry -> TableModelPricing -> Entry conversion round trip.
func TestEntryPricingRoundTripInputCostPerQuery(t *testing.T) {
	var entry Entry
	err := sonic.Unmarshal([]byte(`{
		"provider": "cohere",
		"mode": "rerank",
		"input_cost_per_query": 0.002
	}`), &entry)
	require.NoError(t, err)

	pricing := convertEntryToTablePricing("cohere/rerank-v3.5", entry)
	require.NotNil(t, pricing.InputCostPerQuery)
	assert.InDelta(t, 0.002, *pricing.InputCostPerQuery, 1e-9)

	roundTripped := convertTablePricingToEntry(&pricing)
	require.NotNil(t, roundTripped.InputCostPerQuery)
	assert.InDelta(t, 0.002, *roundTripped.InputCostPerQuery, 1e-9)
}
