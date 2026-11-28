package lib

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/featureflags"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/stretchr/testify/require"
)

// configContentJSON declares one entry of each content kind config.json can hold that a boot test
// can build without a running dependency: every list bootConfig keeps, governance's lists one by
// one.
const configContentJSON = `"providers": {"openai": {"keys": [
		{"id": "k1", "name": "first", "value": "sk-1", "models": ["*"], "weight": 1}
	]}},
	"governance": {
		"budgets": [{"id": "budget-team", "max_limit": 50, "reset_duration": "1d", "team_id": "team-1"}],
		"rate_limits": [
			{"id": "rl-mc", "request_max_limit": 10, "request_reset_duration": "1m"},
			{"id": "rl-provider", "request_max_limit": 100, "request_reset_duration": "1m"}
		],
		"customers": [{"id": "customer-1", "name": "acme"}],
		"teams": [{"id": "team-1", "name": "platform"}],
		"virtual_keys": [{"id": "vk-1", "name": "app", "value": "sk-bf-app", "is_active": true, "team_id": "team-1",
			"provider_configs": [{"provider": "openai", "weight": 1, "allowed_models": ["*"]}]}],
		"model_configs": [{"id": "mc-1", "model_name": "gpt-4o", "provider": "openai", "rate_limit_id": "rl-mc"}],
		"providers": [{"name": "openai", "rate_limit_id": "rl-provider"}],
		"pricing_overrides": [{"id": "po-1", "name": "discount", "scope_kind": "global", "match_type": "exact", "pattern": "gpt-4o",
			"request_types": ["chat_completion"], "pricing_patch": "{\"input_cost_per_token\": 0.000001}"}]
	},
	"mcp": {
		"client_configs": [{"name": "notes", "connection_type": "http", "connection_string": "http://localhost:3001/mcp", "auth_type": "headers"}],
		"virtual_mcps": [{"name": "notes-bundle", "tools": [{"mcp_client_name": "notes", "tool_names": ["*"]}]}]
	},
	"webhooks": [{"name": "jobs", "url": "https://example.com/hook", "events": ["async_job.completed"]}],
	"agents": [{"name": "helper", "agent_card_url": "https://example.com/.well-known/agent-card.json"}],
	"relays": [{"name": "search", "base_url": "https://search.example.com", "auth": {"type": "none"}}],
	"model_routers": [{"id": "support", "name": "Support",
		"keywords": {"simple_keywords": ["hello"], "medium_keywords": ["refactor"], "complex_keywords": ["design"]}}],
	"datasheet_overrides": [{"provider": "openai", "model": "gpt-4o", "mode": "chat", "pricing_patch": {"input_cost_per_token": 0.000001}}],
	"feature_flags": {"flags": {"config-content-flag": {"enabled": true}}}`

// TestConfigJSONContentPerMode pins, in one place, which config.json content kinds each mode
// loads. With a config store every kind does, through the database. Without one only providers
// and governance reach the live store, routing rules excepted, and MCP clients, virtual MCPs,
// webhooks, agents, relays, model routers and routing rules are skipped as their loaders have
// always done. Relays have no collection, so they are read from the config store the relay
// manager is built from. Datasheet overrides and feature flags are tables their owners fill in
// both modes, read through the model catalog and the feature flag store. It also pins that the
// boot struct is gone once LoadConfig returns and that the governance settings stay on Config
// while its entity lists do not. A change to what a mode loads changes this table.
func TestConfigJSONContentPerMode(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	withStore, fileOnly := bootBothModes(t, configContentJSON)

	for _, tc := range []struct {
		mode   string
		config *Config
		stored bool // whether the kinds skipped without a config store are loaded
	}{{"config store", withStore, true}, {"file only", fileOnly, false}} {
		c := tc.config.LiveStore.Collections
		provider, providerLoaded := c.Providers.Get(string(schemas.OpenAI))
		_, key := c.VirtualKeys.Get("vk-1")
		_, team := c.Teams.Get("team-1")
		_, customer := c.Customers.Get("customer-1")
		_, budget := c.Budgets.Get("budget-team")
		_, rateLimit := c.RateLimits.Get("rl-mc")
		_, modelConfig := c.ModelConfigs.Get("mc-1")
		_, pricingOverride := c.PricingOverrides.Get("po-1")

		require.True(t, providerLoaded, "%s: providers load", tc.mode)
		require.True(t, key && team && customer && budget && rateLimit && modelConfig && pricingOverride, "%s: governance loads", tc.mode)
		require.Equal(t, "rl-provider", provider.RateLimitID, "%s: provider governance loads", tc.mode)

		want := 0
		if tc.stored {
			want = 1
		}
		_, agent := c.Agents.Get("helper")
		_, modelRouter := c.ModelRouters.Get("support")
		require.Equal(t, want, countRecords(c.MCPClients), "%s: MCP clients load only with a config store", tc.mode)
		require.Equal(t, want, countRecords(c.VirtualMCPs), "%s: virtual MCPs load only with a config store", tc.mode)
		require.Equal(t, want, countRecords(c.WebhookEndpoints), "%s: webhooks load only with a config store", tc.mode)
		require.Equal(t, tc.stored, agent, "%s: agents load only with a config store", tc.mode)
		require.Equal(t, tc.stored, modelRouter, "%s: model routers load only with a config store", tc.mode)
		relays := 0
		if tc.config.ConfigStore != nil {
			stored, err := tc.config.ConfigStore.ListRelays(ctx)
			require.NoError(t, err)
			relays = len(stored)
		}
		require.Equal(t, want, relays, "%s: relays load only with a config store", tc.mode)

		require.NotNil(t, tc.config.ModelCatalog.FindDatasheetOverride("openai", "gpt-4o", "chat"),
			"%s: datasheet overrides load in both modes", tc.mode)
		flag, err := tc.config.FeatureFlags.Status("config-content-flag")
		require.NoError(t, err, "%s: feature flags load in both modes", tc.mode)
		require.True(t, flag.Enabled, tc.mode)
		require.Equal(t, featureflags.SourceFile, flag.Source, tc.mode)

		require.Nil(t, tc.config.boot, "%s: the boot struct is dropped once the live store is loaded", tc.mode)
		g := tc.config.GovernanceConfig
		require.NotNil(t, g, "%s: the governance settings stay on Config", tc.mode)
		require.Empty(t, g.VirtualKeys, "%s: governance lists live only in the boot struct", tc.mode)
		require.Empty(t, g.Teams, tc.mode)
		require.Empty(t, g.Customers, tc.mode)
		require.Empty(t, g.Budgets, tc.mode)
		require.Empty(t, g.RateLimits, tc.mode)
		require.Empty(t, g.ModelConfigs, tc.mode)
		require.Empty(t, g.Providers, tc.mode)
		require.Empty(t, g.RoutingRules, tc.mode)
		require.Empty(t, g.PricingOverrides, tc.mode)
	}
}

// countRecords returns how many records c holds.
func countRecords[T any](c *livestore.Collection[T]) int {
	n := 0
	c.Range(func(*T) bool { n++; return true })
	return n
}
