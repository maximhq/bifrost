package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/require"
)

// TestReloadOfARowDeletedMeanwhileDropsItsRecord checks the rule for a write's own refresh when
// the row is gone by the time it is read: the row was deleted meanwhile, so the record is dropped
// and the reload answers an error (configstore.ErrNotFound for a pricing override; the routing
// rule test has no routing plugin, whose absence is the error it returns).
func TestReloadOfARowDeletedMeanwhileDropsItsRecord(t *testing.T) {
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	config.ModelCatalog = catalogForTest(t, nil)
	server := &BifrostHTTPServer{Config: config}

	enabled := true
	provider, model := "openai", "gpt-test"
	rule := &tables.TableRoutingRule{ID: "rule-a", Name: "rule-a", Enabled: &enabled, CelExpression: "true", Scope: "global",
		Targets: []tables.TableRoutingTarget{{Provider: &provider, Model: &model, Weight: 1.0}}}
	require.NoError(t, config.ConfigStore.CreateRoutingRule(ctx, rule))
	_ = server.ReloadRoutingRule(ctx, "rule-a")
	_, held := config.LiveStore.Collections.RoutingRules.Get("rule-a")
	require.True(t, held)
	require.NoError(t, config.ConfigStore.DeleteRoutingRule(ctx, "rule-a"))
	require.Error(t, server.ReloadRoutingRule(ctx, "rule-a"))
	_, held = config.LiveStore.Collections.RoutingRules.Get("rule-a")
	require.False(t, held, "a routing rule whose row is gone is dropped")

	override := &tables.TablePricingOverride{
		ID: "o1", Name: "o1", ScopeKind: "provider", ProviderID: &provider, MatchType: "exact", Pattern: "gpt-4o",
		RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest}, PricingPatchJSON: `{"input_cost_per_token":11}`,
	}
	require.NoError(t, config.ConfigStore.CreatePricingOverride(ctx, override))
	require.NoError(t, server.UpsertPricingOverride(ctx, override))
	require.NoError(t, config.ConfigStore.DeletePricingOverride(ctx, "o1"))
	require.ErrorIs(t, server.UpsertPricingOverride(ctx, override), configstore.ErrNotFound)
	_, held = config.LiveStore.Collections.PricingOverrides.Get("o1")
	require.False(t, held, "a pricing override whose row is gone is dropped")
}
