package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/require"
)

// TestPricingOverrideWritesFollowTheDatabase checks that a pricing override write brings the row
// into the live store, which the model catalog prices from, and a delete drops it.
func TestPricingOverrideWritesFollowTheDatabase(t *testing.T) {
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	config.ModelCatalog = catalogForTest(t, nil)
	server := &BifrostHTTPServer{Config: config}
	providerID := "openai"
	override := &tables.TablePricingOverride{
		ID: "o1", Name: "o1", ScopeKind: "provider", ProviderID: &providerID, MatchType: "exact", Pattern: "gpt-4o",
		RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest}, PricingPatchJSON: `{"input_cost_per_token":11}`,
	}
	require.NoError(t, config.ConfigStore.CreatePricingOverride(ctx, override))

	require.NoError(t, server.UpsertPricingOverride(ctx, override))
	record, ok := config.LiveStore.Collections.PricingOverrides.Get("o1")
	require.True(t, ok)
	require.Equal(t, "gpt-4o", record.Pattern)

	require.NoError(t, config.ConfigStore.DeletePricingOverride(ctx, "o1"))
	require.NoError(t, server.DeletePricingOverride(ctx, "o1"))
	_, ok = config.LiveStore.Collections.PricingOverrides.Get("o1")
	require.False(t, ok)
}
