package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pteTestVKValue = "sk-bf-pte"

// newPluginForEndpoints builds a governance plugin holding one virtual key with a budget and a rate
// limit, granted the named endpoints.
func newPluginForEndpoints(t testing.TB, endpoints ...string) (*GovernancePlugin, *LocalGovernanceStore) {
	t.Helper()
	return newPluginForEndpointsWith(t, nil, endpoints...)
}

// newPluginForEndpointsWith is newPluginForEndpoints with model configs on the key, for the
// cases that check they never reach a relay call.
func newPluginForEndpointsWith(t testing.TB, modelConfigs []configstoreTables.TableModelConfig, endpoints ...string) (*GovernancePlugin, *LocalGovernanceStore) {
	t.Helper()
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("pte-budget", 100.0, 0.0, "1d")
	rl := buildRateLimit("pte-rl", 1_000_000, 1_000)
	vk := buildVirtualKeyWithBudget("vk-pte", pteTestVKValue, "pte-vk", budget)
	vk.RateLimit = rl
	vk.RateLimitID = &rl.ID
	for _, name := range endpoints {
		vk.RelayGrants = append(vk.RelayGrants, configstoreTables.TableVirtualKeyRelayGrant{VirtualKeyID: vk.ID, RelayName: name})
	}
	cfg := &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		Budgets:      []configstoreTables.TableBudget{*budget},
		RateLimits:   []configstoreTables.TableRateLimit{*rl},
		ModelConfigs: modelConfigs,
	}
	for _, mc := range modelConfigs {
		if mc.Budget != nil {
			cfg.Budgets = append(cfg.Budgets, *mc.Budget)
		}
		if mc.RateLimit != nil {
			cfg.RateLimits = append(cfg.RateLimits, *mc.RateLimit)
		}
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, cfg, nil, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin, store
}

// A key's own budget and rate limit live in a key-scoped all-models, all-providers model config
// since the model-config migration, so a relay call must answer to that tier; a provider- or
// model-specific config prices models and must not reach a call that runs none.
func TestRelayCallsAnswerToMigratedKeyLimitsOnly(t *testing.T) {
	keyWide := buildRateLimit("key-wide-rl", 1_000_000, 1_000)
	migrated := buildModelConfig("mc-key-all", configstoreTables.ModelConfigAllModels, nil, nil, keyWide)
	migrated.Scope = configstoreTables.ModelConfigScopeVirtualKey
	migrated.ScopeID = schemas.Ptr("vk-pte")
	openai := "openai"
	exhausted := buildRateLimit("openai-rl", 1_000_000, 0) // zero requests left, but openai-only
	providerOnly := buildModelConfig("mc-openai-all", configstoreTables.ModelConfigAllModels, &openai, nil, exhausted)
	providerOnly.Scope = configstoreTables.ModelConfigScopeVirtualKey
	providerOnly.ScopeID = schemas.Ptr("vk-pte")
	p, store := newPluginForEndpointsWith(t, []configstoreTables.TableModelConfig{*migrated, *providerOnly}, "tavily")
	ctx := a2aVKCtx(pteTestVKValue)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-pte-mc")
	_, short, err := p.PreRelayHook(ctx, endpointReq("tavily"))
	require.NoError(t, err)
	require.Nil(t, short, "an exhausted provider-specific limit does not refuse a relay call")
	_, _, err = p.PostRelayHook(ctx, &schemas.BifrostRelayResponse{RelayName: "tavily", StatusCode: 200}, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return store.GetGovernanceData(context.Background()).RateLimits["key-wide-rl"].RequestCurrentUsage == 1
	}, 2*time.Second, 20*time.Millisecond, "the key-wide tier counts the call")
	assert.EqualValues(t, 0, store.GetGovernanceData(context.Background()).RateLimits["openai-rl"].RequestCurrentUsage, "the provider-specific tier is untouched")
	ids := limitIDsOf(ctx.Grant().Limits().RateLimits())
	assert.Contains(t, ids, "key-wide-rl")
	assert.NotContains(t, ids, "openai-rl")
}

// BenchmarkRelayHooks measures the governance cost of one relay call on the request path: the
// pre-hook (identity, limits, grant check) and the post-hook (the usage update it queues).
func BenchmarkRelayHooks(b *testing.B) {
	p, _ := newPluginForEndpoints(b, "tavily")
	req := endpointReq("tavily")
	resp := &schemas.BifrostRelayResponse{RelayName: "tavily", StatusCode: 200, Usage: &schemas.RelayUsage{Cost: 0.001}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := a2aVKCtx(pteTestVKValue)
		ctx.SetValue(schemas.BifrostContextKeyRequestID, "bench")
		if _, short, err := p.PreRelayHook(ctx, req); err != nil || short != nil {
			b.Fatalf("pre-hook: %v %v", err, short)
		}
		if _, _, err := p.PostRelayHook(ctx, resp, nil); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	p.wg.Wait()
}

func endpointReq(name string) *schemas.BifrostRelayRequest {
	return &schemas.BifrostRelayRequest{RelayName: name, Method: "POST", Path: "/search", Billable: true, RouteIndex: -1}
}

func TestPreRelayHookGrants(t *testing.T) {
	p, _ := newPluginForEndpoints(t, "tavily")

	_, short, err := p.PreRelayHook(a2aVKCtx(pteTestVKValue), endpointReq("tavily"))
	require.NoError(t, err)
	assert.Nil(t, short, "a granted endpoint is allowed")

	ctx := a2aVKCtx(pteTestVKValue)
	_, short, err = p.PreRelayHook(ctx, endpointReq("textract"))
	require.NoError(t, err)
	require.NotNil(t, short)
	require.NotNil(t, short.Error.StatusCode)
	assert.Equal(t, 403, *short.Error.StatusCode, "an ungranted endpoint is refused")
	assert.Equal(t, string(DecisionRelayBlocked), *short.Error.Type)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey))

	none, _ := newPluginForEndpoints(t)
	_, short, err = none.PreRelayHook(a2aVKCtx(pteTestVKValue), endpointReq("tavily"))
	require.NoError(t, err)
	require.NotNil(t, short, "a key with no endpoint grant is denied by default")

	_, short, err = none.PreRelayHook(anonymousA2ACtx(), endpointReq("tavily"))
	require.NoError(t, err)
	assert.Nil(t, short, "a request with no credential and no mandatory key is unrestricted, as for MCP tools")
}

func TestPostRelayHookChargesOverallLimits(t *testing.T) {
	p, store := newPluginForEndpoints(t, "tavily")
	ctx := a2aVKCtx(pteTestVKValue)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-pte-1")
	_, short, err := p.PreRelayHook(ctx, endpointReq("tavily"))
	require.NoError(t, err)
	require.Nil(t, short)

	resp := &schemas.BifrostRelayResponse{RelayName: "tavily", StatusCode: 200, Usage: &schemas.RelayUsage{Cost: 0.033}}
	_, _, err = p.PostRelayHook(ctx, resp, nil)
	require.NoError(t, err)

	// An uncounted call (no usage: a route that is not billable) charges nothing, but it is one
	// request against the rate limits, as a free MCP tool execution is.
	ctx2 := a2aVKCtx(pteTestVKValue)
	ctx2.SetValue(schemas.BifrostContextKeyRequestID, "req-pte-2")
	_, _, err = p.PreRelayHook(ctx2, endpointReq("tavily"))
	require.NoError(t, err)
	_, _, err = p.PostRelayHook(ctx2, &schemas.BifrostRelayResponse{RelayName: "tavily", StatusCode: 200}, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		data := store.GetGovernanceData(context.Background())
		return data.Budgets["pte-budget"].CurrentUsage > 0
	}, 2*time.Second, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	data := store.GetGovernanceData(context.Background())
	assert.InDelta(t, 0.033, data.Budgets["pte-budget"].CurrentUsage, 1e-9, "the formula cost lands on the key's budget")
	assert.EqualValues(t, 2, data.RateLimits["pte-rl"].RequestCurrentUsage, "both replies count a request; only the counted one carries a cost")
	assert.Equal(t, []string{"pte-budget"}, ctx.Value(schemas.BifrostContextKeyGovernanceBudgetIDs), "the charged budgets are recorded for the log and job rows")
	assert.Equal(t, []string{"pte-rl"}, limitIDsOf(ctx2.Grant().Limits().RateLimits()), "an uncounted call still answers to the rate limits settled on its grant")

	// A stream the caller hung up on still answered, so it counts and is charged.
	ctxCancel := a2aVKCtx(pteTestVKValue)
	ctxCancel.SetValue(schemas.BifrostContextKeyRequestID, "req-pte-cancel")
	_, _, err = p.PreRelayHook(ctxCancel, endpointReq("tavily"))
	require.NoError(t, err)
	_, _, err = p.PostRelayHook(ctxCancel, &schemas.BifrostRelayResponse{RelayName: "tavily", StatusCode: 200, Streamed: true, Usage: &schemas.RelayUsage{Cost: 0.001}},
		&schemas.BifrostError{Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return store.GetGovernanceData(context.Background()).RateLimits["pte-rl"].RequestCurrentUsage == 3
	}, 2*time.Second, 20*time.Millisecond, "an abandoned stream still counts one request")

	// A call that never got a reply (the upstream failed) is not a request the caller made.
	ctx3 := a2aVKCtx(pteTestVKValue)
	ctx3.SetValue(schemas.BifrostContextKeyRequestID, "req-pte-3")
	_, _, err = p.PreRelayHook(ctx3, endpointReq("tavily"))
	require.NoError(t, err)
	_, _, err = p.PostRelayHook(ctx3, nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "dial failed"}})
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 3, store.GetGovernanceData(context.Background()).RateLimits["pte-rl"].RequestCurrentUsage)
}

func TestReportEndpointUsageIsIdempotent(t *testing.T) {
	p, store := newPluginForEndpoints(t, "textract")
	report := jobaccounting.RelayUsageReport{RequestID: "settle-1", RelayName: "textract", Cost: 0.15, BudgetIDs: []string{"pte-budget"}, RateLimitIDs: []string{"pte-rl"}}
	require.NoError(t, p.ReportRelayUsage(context.Background(), report))
	require.NoError(t, p.ReportRelayUsage(context.Background(), report))
	data := store.GetGovernanceData(context.Background())
	assert.InDelta(t, 0.15, data.Budgets["pte-budget"].CurrentUsage, 1e-9, "a retried settlement charges once")
	assert.EqualValues(t, 0, data.RateLimits["pte-rl"].RequestCurrentUsage, "a settlement counts no request; the start call did")
}
