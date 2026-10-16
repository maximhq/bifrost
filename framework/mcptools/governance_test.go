package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// GovernanceReaderStub embeds the GovernanceReader interface without implementing
// it, so a fake that only overrides the methods it needs panics on anything new.
type GovernanceReaderStub struct {
	GovernanceReader
}

// fakeGovernanceReader is describe_virtual_key's one dependency. lookup is
// keyed on id, mirroring GetVirtualKey's own "not found" contract: a missing
// key returns configstore.ErrNotFound, not a nil, nil pair.
type fakeGovernanceReader struct {
	GovernanceReaderStub
	byID             map[string]*tables.TableVirtualKey
	teams            map[string]*tables.TableTeam
	customers        map[string]*tables.TableCustomer
	budgets          map[string]*tables.TableBudget
	providers        []tables.TableProvider
	providerKeys     map[schemas.ModelProvider][]schemas.Key
	providerCfgs     map[schemas.ModelProvider]configstore.ProviderConfig
	mcpClients       map[string]*tables.TableMCPClient
	virtualMCPs      map[uint]*tables.TableVirtualMCP
	vmcpVKs          map[uint][]string
	routing          map[string]*tables.TableRoutingRule
	rateLimits       map[string]*tables.TableRateLimit
	modelConfigs     map[string]*tables.TableModelConfig
	plugins          map[string]*tables.TablePlugin
	pricing          map[string]*tables.TablePricingOverride
	webhooks         map[string]*tables.TableWebhookEndpoint
	flags            map[string]tables.TableFeatureFlag
	oauthTokens      []tables.TableMCPOauthToken
	headerCreds      []tables.TableMCPPerUserHeaderCredential
	sawSessionParams []string
	err              error
	sawContext       context.Context
	sawID            string
	clientConfigErr  error
}

func (f *fakeGovernanceReader) GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error) {
	f.sawContext = ctx
	f.sawID = id
	if f.err != nil {
		return nil, f.err
	}
	vk, ok := f.byID[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return vk, nil
}

func TestDescribeVirtualKeyReportsUnavailableWithoutAGovernanceReader(t *testing.T) {
	_, err := runTool(t, "describe_virtual_key", &Deps{}, map[string]any{"virtual_key_id": "vk-1"})
	require.ErrorContains(t, err, "not available")
}

func TestDescribeVirtualKeyRequiresAnID(t *testing.T) {
	deps := &Deps{Governance: &fakeGovernanceReader{}}
	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "  "})
	require.ErrorContains(t, err, "virtual_key_id")
}

func TestDescribeVirtualKeyReportsUnknownID(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{}}
	deps := &Deps{Governance: fake}
	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-missing"})
	require.ErrorContains(t, err, "vk-missing")
	require.ErrorContains(t, err, "describe_filter_space")
}

// The caller's context is what carries queryscope's row-level filter into the
// store - GetVirtualKey narrows to rows the caller may see the same way every
// LogReader method does. Losing it here would return any key to anyone who
// asked, the same failure mode LogReader's own tools guard against.
func TestDescribeVirtualKeyPassesCallerContextToStore(t *testing.T) {
	type scopeKey struct{}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{
		"vk-1": {ID: "vk-1", Name: "prod"},
	}}
	deps := &Deps{Governance: fake}
	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	_, err := runToolCtx(t, ctx, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Equal(t, "caller-scope", fake.sawContext.Value(scopeKey{}))
	require.Equal(t, "vk-1", fake.sawID)
}

// The result must never carry the key's own secret value, its rotation
// history, or any provider credential beneath it - only the budget/limit/
// provider shape describeVirtualKey hand-picks. This is the regression test
// for that: a row deliberately carrying secret-shaped data in every field
// describeVirtualKey does not touch, asserting none of it survives.
func TestDescribeVirtualKeyNeverLeaksSecretFields(t *testing.T) {
	teamID := "team-1"
	expires := time.Now().Add(24 * time.Hour)
	vk := &tables.TableVirtualKey{
		ID:                "vk-1",
		Name:              "prod",
		Description:       "production traffic",
		TeamID:            &teamID,
		ExpiresAt:         &expires,
		Value:             schemas.SecretVar{Val: "sk-super-secret-value"},
		PreviousValueHash: "leftover-hash",
		Budgets: []tables.TableBudget{
			{ID: "budget-1", MaxLimit: 100, CurrentUsage: 42, ResetDuration: "1M", LastReset: time.Now()},
		},
		RateLimit: &tables.TableRateLimit{ID: "rl-1", TokenMaxLimit: int64Ptr(1000), TokenCurrentUsage: 250},
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				AllowedModels: []string{"gpt-4o"},
				Keys: []tables.TableKey{
					{ID: 1, Name: "prod-openai-key", Value: schemas.SecretVar{Val: "sk-should-never-appear"}},
				},
			},
		},
	}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}}
	deps := &Deps{Governance: fake}

	result, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)

	out, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "vk-1", out["id"])
	require.Equal(t, "prod", out["name"])
	require.Equal(t, "team-1", out["team_id"])

	budgets, ok := out["budgets"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, budgets, 1)
	require.InDelta(t, 100.0, budgets[0]["max_limit_usd"], 0.001)
	require.InDelta(t, 42.0, budgets[0]["current_usage_usd"], 0.001)

	rateLimit, ok := out["rate_limit"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, int64(1000), rateLimit["token_max_limit"])
	require.NotContains(t, rateLimit, "request_max_limit", "an unset limit family must not read as a limit of zero")

	providers, ok := out["providers"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, providers, 1)
	require.Equal(t, "openai", providers[0]["provider"])
	require.Equal(t, []string{"gpt-4o"}, providers[0]["allowed_models"])
	require.NotContains(t, providers[0], "keys", "no provider key detail, secret or otherwise, may reach the model")

	// The bounded-result serialization is the last line of defense; walking
	// the returned value directly is what proves the secret was never placed
	// there in the first place, not merely stripped afterward.
	serialized := boundToolResult(result)
	require.NotContains(t, serialized, "sk-super-secret-value")
	require.NotContains(t, serialized, "sk-should-never-appear")
	require.NotContains(t, serialized, "leftover-hash")
	require.NotContains(t, serialized, "prod-openai-key", "not even a key's name belongs in a chat tool result")
}

// A budget under an active override must report the effective cap, not the
// raw one the override has already changed - the same distinction the
// dashboard itself makes (see TableBudget.EffectiveMaxLimit).
func TestDescribeVirtualKeyBudgetReportsEffectiveLimitUnderOverride(t *testing.T) {
	vk := &tables.TableVirtualKey{
		ID:   "vk-1",
		Name: "prod",
		Budgets: []tables.TableBudget{
			{
				ID: "budget-1", MaxLimit: 100, CurrentUsage: 10, ResetDuration: "1M", LastReset: time.Now(),
				OverrideAmount: 50, OverrideMode: tables.BudgetOverrideModeForever,
			},
		},
	}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}}
	deps := &Deps{Governance: fake}

	result, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)

	budgets := result.(map[string]any)["budgets"].([]map[string]any)
	require.InDelta(t, 150.0, budgets[0]["max_limit_usd"], 0.001, "override amount must be folded into the reported cap")
	require.Equal(t, true, budgets[0]["override_active"])
}

func int64Ptr(v int64) *int64 { return &v }

// A key with nothing configured used to come back with no "budgets" or
// "rate_limit" field at all, and the model read the missing field as a failed
// lookup ("Warp couldn't retrieve a configured budget"). Both are always
// present now, empty or null, and the result says in words that nothing caps
// the key itself and what may still apply.
func TestDescribeVirtualKeySaysWhenNothingCapsTheKey(t *testing.T) {
	bu := "bu-1"
	vk := &tables.TableVirtualKey{ID: "vk-bare", Name: "bare", BusinessUnitID: &bu}
	deps := &Deps{Governance: &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-bare": vk}}}

	out := directMap(t, mustRunTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-bare"}))
	budgets, ok := out["budgets"].([]map[string]any)
	require.True(t, ok, "budgets must be present even when empty")
	require.Empty(t, budgets)
	rateLimit, present := out["rate_limit"]
	require.True(t, present, "rate_limit must be present even when null")
	require.Nil(t, rateLimit)
	require.Equal(t, "bu-1", out["business_unit_id"])
	require.Contains(t, out["guidance"], "No budget or rate limit is configured on this key itself")
	require.Contains(t, out["guidance"], "team, customer or business unit")
	require.NotContains(t, out, "access_profile_managed")

	// Only one of the two missing names just that one.
	vk.RateLimit = &tables.TableRateLimit{ID: "rl-1", RequestMaxLimit: int64Ptr(1000)}
	out = directMap(t, mustRunTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-bare"}))
	require.Contains(t, out["guidance"], "No budget is configured")
}

// The key row is not where a managed key's cap lives: on an enterprise
// deployment an access profile hands a user a bare key and keeps the budgets
// and rate limit on the profile, and the dashboard overlays them through a
// resolver Warp was never given. Asked about such a key, Warp reported no
// budget while the dashboard showed $450 at 85%. The decorator is that
// overlay; what it puts on the row is what the tool reports, with where it
// came from, who owns the key, and - when the deployment can answer it -
// where the person's full limits are.
func TestDescribeVirtualKeyReportsWhatTheDecoratorOverlays(t *testing.T) {
	vk := &tables.TableVirtualKey{ID: "vk-managed", Name: "vrinda-key"}
	decorator := func(_ context.Context, vk *tables.TableVirtualKey) ([]string, error) {
		vk.IsAccessProfileManaged = true
		vk.Budgets = []tables.TableBudget{{ID: "b-profile", MaxLimit: 250, OverrideAmount: 200, OverrideMode: tables.BudgetOverrideModeForever, CurrentUsage: 380.28, ResetDuration: "1M", LastReset: time.Now()}}
		vk.RateLimit = &tables.TableRateLimit{ID: "rl-profile", RequestMaxLimit: int64Ptr(1000), RequestCurrentUsage: 2}
		vk.AssignedUser = &tables.AssignedUser{ID: "u-vrinda", Name: "Vrinda"}
		return []string{"access profile admin"}, nil
	}
	reader := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-managed": vk}}

	for _, userLimits := range []bool{false, true} {
		deps := &Deps{Governance: reader, VKDecorator: decorator}
		if userLimits {
			deps.UserGovernance = &fakeUserGovernanceReader{}
		}
		out := directMap(t, mustRunTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-managed"}))
		require.Equal(t, true, out["access_profile_managed"])
		budgets := out["budgets"].([]map[string]any)
		require.Len(t, budgets, 1)
		require.InDelta(t, 450.0, budgets[0]["max_limit_usd"], 0.001, "override folded in")
		require.InDelta(t, 380.28, budgets[0]["current_usage_usd"], 0.001)
		require.InDelta(t, 69.72, budgets[0]["remaining_usd"], 0.001)
		require.Equal(t, int64(1000), out["rate_limit"].(map[string]any)["request_max_limit"])
		require.Equal(t, map[string]any{"id": "u-vrinda", "name": "Vrinda"}, out["assigned_user"])
		guidance, _ := out["guidance"].(string)
		require.Contains(t, guidance, "managed by an access profile (access profile admin)")
		require.Contains(t, guidance, "owner is user u-vrinda")
		require.Equal(t, userLimits, strings.Contains(guidance, UserLimitsToolName), "userLimits=%v: %s", userLimits, guidance)
	}

	// A decorator that cannot resolve the governance fails the call: a bare
	// row projected in its place is the wrong answer with a straight face.
	failing := func(context.Context, *tables.TableVirtualKey) ([]string, error) {
		return nil, errors.New("profile store down")
	}
	_, err := runTool(t, "describe_virtual_key", &Deps{Governance: reader, VKDecorator: failing}, map[string]any{"virtual_key_id": "vk-managed"})
	require.ErrorContains(t, err, "could not resolve what governs virtual key")
}

// remaining is what the question is actually about, and a model doing the
// subtraction itself got it wrong often enough. It never goes below zero: an
// overspent budget has nothing left, not a negative amount.
func TestBudgetSummaryReportsRemaining(t *testing.T) {
	require.InDelta(t, 30.0, budgetSummary(tables.TableBudget{MaxLimit: 100, CurrentUsage: 70})["remaining_usd"], 0.001)
	require.InDelta(t, 0.0, budgetSummary(tables.TableBudget{MaxLimit: 100, CurrentUsage: 130})["remaining_usd"], 0.001)
}

type fakeUserGovernanceReader struct {
	byID  map[string]*UserGovernance
	err   error
	sawID string
}

func (f *fakeUserGovernanceReader) DescribeUserGovernance(_ context.Context, id string) (*UserGovernance, error) {
	f.sawID = id
	if f.err != nil {
		return nil, f.err
	}
	gov, ok := f.byID[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return gov, nil
}

// "How much budget do I have left" is a question about a person: on a
// deployment with access profiles the cap sits on the user and every key they
// hold inherits it. describe_user_limits projects that per profile, with the
// same budget and rate-limit summaries a key gets, and says so when nothing
// governs the person at all.
func TestDescribeUserLimits(t *testing.T) {
	expires := time.Now().Add(48 * time.Hour)
	reader := &fakeUserGovernanceReader{byID: map[string]*UserGovernance{
		"u-vrinda": {
			UserID: "u-vrinda", Name: "Vrinda",
			Profiles: []UserGovernanceProfile{{
				Name: "admin", Active: true, ExpiresAt: &expires,
				Budgets:   []tables.TableBudget{{ID: "b-global", MaxLimit: 450, CurrentUsage: 380.28, ResetDuration: "1M", LastReset: time.Now()}},
				RateLimit: &tables.TableRateLimit{ID: "rl-global", RequestMaxLimit: int64Ptr(1000), RequestCurrentUsage: 2, RequestResetDuration: strPtr("1h")},
				Providers: []UserGovernanceProvider{{
					Provider:      "databricks",
					AllowedModels: []string{"dbrx"},
					Budgets:       []tables.TableBudget{{ID: "b-databricks", MaxLimit: 20, ResetDuration: "1M"}},
					ModelBudgets:  []UserGovernanceModelBudget{{Model: "dbrx", Budgets: []tables.TableBudget{{ID: "b-dbrx", MaxLimit: 5}}}},
				}},
			}},
		},
		"u-free": {UserID: "u-free", Name: "Nobody"},
	}}
	deps := &Deps{UserGovernance: reader}

	out := directMap(t, mustRunTool(t, UserLimitsToolName, deps, map[string]any{"user_id": " u-vrinda "}))
	require.Equal(t, "u-vrinda", reader.sawID, "the id is trimmed before the lookup")
	require.Equal(t, "Vrinda", out["name"])
	profiles := out["profiles"].([]map[string]any)
	require.Len(t, profiles, 1)
	profile := profiles[0]
	require.Equal(t, "admin", profile["name"])
	require.Equal(t, true, profile["active"])
	require.Equal(t, expires, profile["expires_at"])
	budgets := profile["budgets"].([]map[string]any)
	require.InDelta(t, 69.72, budgets[0]["remaining_usd"], 0.001)
	rateLimit := profile["rate_limit"].(map[string]any)
	require.Equal(t, int64(1000), rateLimit["request_max_limit"])
	require.Equal(t, "1h", rateLimit["request_reset_duration"])
	providers := profile["providers"].([]map[string]any)
	require.Len(t, providers, 1)
	require.Equal(t, "databricks", providers[0]["provider"])
	require.Equal(t, []string{"dbrx"}, providers[0]["allowed_models"])
	require.InDelta(t, 20.0, providers[0]["budgets"].([]map[string]any)[0]["remaining_usd"], 0.001)
	models := providers[0]["model_budgets"].([]map[string]any)
	require.Equal(t, "dbrx", models[0]["model"])
	require.Contains(t, out["guidance"], "remaining_usd is max_limit_usd minus current_usage_usd")

	// Nobody governs this person at the user level, and the result says so
	// rather than handing over an empty list to be read as a failed lookup.
	out = directMap(t, mustRunTool(t, UserLimitsToolName, deps, map[string]any{"user_id": "u-free"}))
	require.Empty(t, out["profiles"])
	require.Contains(t, out["guidance"], "No access profile is attached to this user")

	// A user the caller cannot see and one that does not exist read the same.
	_, err := runTool(t, UserLimitsToolName, deps, map[string]any{"user_id": "u-ghost"})
	require.ErrorContains(t, err, `no user with id "u-ghost" that you can see`)
	_, err = runTool(t, UserLimitsToolName, deps, map[string]any{"user_id": ""})
	require.ErrorContains(t, err, "user_id is required")
}

// describe_user_limits is always declared - the server's tool list is fixed,
// so one /mcp client sees the same tools whatever the deployment - and a
// deployment without per-user governance answers that it has none rather than
// panicking on the missing reader or reading as a user with no limits.
func TestDescribeUserLimitsReportsUnavailableWithoutAReader(t *testing.T) {
	tool, declared := toolByName(buildTools(), UserLimitsToolName)
	require.True(t, declared)
	require.True(t, tool.noLogs, "it reads governance, not the log store")

	_, err := runTool(t, UserLimitsToolName, &Deps{}, map[string]any{"user_id": "u-1"})
	require.ErrorContains(t, err, "not available on this deployment")
}

// directMap is the tool's own return value, untouched by serialization, so
// the typed shapes describeVirtualKey builds can be asserted as built.
func directMap(t *testing.T, result any) map[string]any {
	t.Helper()
	out, ok := result.(map[string]any)
	require.True(t, ok, "result is %T", result)
	return out
}

func mustRunTool(t *testing.T, name string, deps *Deps, args map[string]any) any {
	t.Helper()
	out, err := runTool(t, name, deps, args)
	require.NoError(t, err)
	return out
}

func strPtr(v string) *string { return &v }

// fakeVirtualKeyFinder is a governance reader that also searches by name the
// way the store does: a substring match on the key's name (the real search
// also matches the owning team's or customer's name, which the resolver has
// to filter back out), capped by the page limit with the full match count
// reported alongside, as the store's total is.
type fakeVirtualKeyFinder struct {
	fakeGovernanceReader
	keys      []tables.TableVirtualKey
	sawSearch string
	sawLimit  int
}

func (f *fakeVirtualKeyFinder) GetVirtualKeysPaginated(_ context.Context, params configstore.VirtualKeyQueryParams) ([]tables.TableVirtualKey, int64, error) {
	f.sawSearch, f.sawLimit = params.Search, params.Limit
	var out []tables.TableVirtualKey
	for _, key := range f.keys {
		if strings.Contains(strings.ToLower(key.Name), strings.ToLower(params.Search)) {
			out = append(out, key)
		}
	}
	total := int64(len(out))
	if params.Limit > 0 && len(out) > params.Limit {
		out = out[:params.Limit]
	}
	return out, total, nil
}

// The store's search matches team and customer names as well as the key's,
// and the resolver reads one page of it. With more matches than the page
// holds, the exact key can sit beyond the page - and the resolver then said
// the key did not exist, when it had simply not looked far enough. A truncated
// search has to say so, or the model relays a non-existence it never checked.
func TestDescribeVirtualKeySaysWhenTheNameSearchWasTruncated(t *testing.T) {
	var keys []tables.TableVirtualKey
	for i := 0; i < virtualKeyNameSearchLimit; i++ {
		keys = append(keys, tables.TableVirtualKey{ID: fmt.Sprintf("vk-%d", i), Name: fmt.Sprintf("prod-%d", i)})
	}
	// Sorted after a full page of near misses, so the page never reaches it.
	keys = append(keys, tables.TableVirtualKey{ID: "vk-exact", Name: "prod"})
	byID := map[string]*tables.TableVirtualKey{}
	for i := range keys {
		byID[keys[i].ID] = &keys[i]
	}
	finder := &fakeVirtualKeyFinder{fakeGovernanceReader: fakeGovernanceReader{byID: byID}, keys: keys}
	deps := &Deps{Governance: finder}

	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"name": "prod"})
	require.ErrorContains(t, err, fmt.Sprintf("first %d of %d", virtualKeyNameSearchLimit, virtualKeyNameSearchLimit+1))
	require.ErrorContains(t, err, "may exist beyond that page")
	require.NotContains(t, err.Error(), "does not exist", "a page that ran out is not evidence of absence")

	// A page that held every match keeps the plain not-found answer.
	_, err = runTool(t, "describe_virtual_key", deps, map[string]any{"name": "ghost"})
	require.ErrorContains(t, err, `no virtual key named "ghost" that you can see`)
	require.NotContains(t, err.Error(), "beyond that page")
}

// A key that exists but has no traffic is not in describe_filter_space, and
// asked about its budget Warp said the key did not exist. describe_virtual_key
// now takes the key's exact name and resolves it through the store's own
// scoped search, so a key is found whether or not it has ever been used, and
// the failure modes say what was found rather than "no such key".
func TestDescribeVirtualKeyFindsAKeyByExactName(t *testing.T) {
	keys := []tables.TableVirtualKey{
		{ID: "vk-1", Name: "warp-verify-budgeted", Budgets: []tables.TableBudget{{ID: "b-1", MaxLimit: 50}}},
		{ID: "vk-2", Name: "warp-verify-budgeted-old"},
		{ID: "vk-3", Name: "Twin"},
		{ID: "vk-4", Name: "twin"},
	}
	byID := map[string]*tables.TableVirtualKey{}
	for i := range keys {
		byID[keys[i].ID] = &keys[i]
	}
	finder := &fakeVirtualKeyFinder{fakeGovernanceReader: fakeGovernanceReader{byID: byID}, keys: keys}
	deps := &Deps{Governance: finder}

	// Exact name, any case, resolves to the one key and describes it in full.
	out := directMap(t, mustRunTool(t, "describe_virtual_key", deps, map[string]any{"name": " Warp-Verify-Budgeted "}))
	require.Equal(t, "vk-1", out["id"])
	require.Equal(t, "Warp-Verify-Budgeted", finder.sawSearch, "trimmed before the search; the store lowercases it itself")
	require.Equal(t, virtualKeyNameSearchLimit, finder.sawLimit)
	require.Equal(t, "vk-1", finder.sawID, "the full read goes through GetVirtualKey, so scope and decorator apply")
	require.Len(t, out["budgets"], 1)

	// The id wins when both are given.
	out = directMap(t, mustRunTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-2", "name": "warp-verify-budgeted"}))
	require.Equal(t, "vk-2", out["id"])

	// A partial match is not a match, and the near misses are named.
	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"name": "warp-verify"})
	require.ErrorContains(t, err, `no virtual key named exactly "warp-verify"`)
	require.ErrorContains(t, err, "warp-verify-budgeted, warp-verify-budgeted-old")

	// Nothing near it either.
	_, err = runTool(t, "describe_virtual_key", deps, map[string]any{"name": "ghost"})
	require.ErrorContains(t, err, `no virtual key named "ghost" that you can see`)

	// Two keys with the same name is a question for the id.
	_, err = runTool(t, "describe_virtual_key", deps, map[string]any{"name": "twin"})
	require.ErrorContains(t, err, `2 virtual keys are named "twin" (vk-3, vk-4)`)

	// Neither is a request for nothing.
	_, err = runTool(t, "describe_virtual_key", deps, map[string]any{})
	require.ErrorContains(t, err, "virtual_key_id or name is required")

	// Every GovernanceReader searches by name - GetVirtualKeysPaginated is on
	// the interface - so there is no id-only fallback left to report.
	out = directMap(t, mustRunTool(t, "describe_virtual_key", &Deps{Governance: &fakeGovernanceReader{byID: byID}}, map[string]any{"name": "warp-verify-budgeted"}))
	require.Equal(t, "vk-1", out["id"])
}

// A budget amount has to say what it is an amount of. The summary used to
// carry bare numbers under max_limit, current_usage and remaining, and nothing
// in the tool text or the prompt said they were dollars - so a model reading
// "max_limit": 5000 reported "5,000 units per month". The unit now rides on
// the key, right beside the number, and the text around it says the same.
func TestBudgetAmountsNameTheirCurrency(t *testing.T) {
	summary := budgetSummary(tables.TableBudget{ID: "b-1", MaxLimit: 5000, CurrentUsage: 120.5, ResetDuration: "1M"})
	require.InDelta(t, 5000.0, summary["max_limit_usd"], 0.001)
	require.InDelta(t, 120.5, summary["current_usage_usd"], 0.001)
	require.InDelta(t, 4879.5, summary["remaining_usd"], 0.001)
	for _, bare := range []string{"max_limit", "current_usage", "remaining"} {
		require.NotContains(t, summary, bare, "a unitless amount is what got read as \"units\"")
	}

	// Both tools hand out the same summary, so both say what the amounts are.
	virtualKey, ok := toolByName(buildTools(), "describe_virtual_key")
	require.True(t, ok)
	require.Contains(t, virtualKey.description, "US dollars")
	require.Contains(t, describeUserLimitsTool().description, "US dollars")

	// And the result says it again, where the model reads the number.
	governed := describeUserGovernance(&UserGovernance{UserID: "u-1", Profiles: []UserGovernanceProfile{{Name: "admin", Active: true}}})
	require.Contains(t, governed["guidance"], "US dollars")
}

// Metadata is operator-set attribution (cost center, owner, ...) the model needs to answer
// "whose key is this"; it is reported when present and left out when the key has none.
func TestDescribeVirtualKeyReportsMetadata(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{
		"vk-1": {ID: "vk-1", Name: "prod", Metadata: map[string]string{"cost_center": "cc-42"}},
		"vk-2": {ID: "vk-2", Name: "dev"},
	}}
	deps := &Deps{Governance: fake}

	result, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	out, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]string{"cost_center": "cc-42"}, out["metadata"])

	result, err = runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-2"})
	require.NoError(t, err)
	out, ok = result.(map[string]any)
	require.True(t, ok)
	require.NotContains(t, out, "metadata")
}

func (f *fakeGovernanceReader) GetVirtualKeysPaginated(ctx context.Context, params configstore.VirtualKeyQueryParams) ([]tables.TableVirtualKey, int64, error) {
	f.sawContext = ctx
	out := make([]tables.TableVirtualKey, 0, len(f.byID))
	for _, vk := range f.byID {
		// Filtered like the store, so tenant walks see only the keys asked for.
		if params.TeamID != "" && (vk.TeamID == nil || *vk.TeamID != params.TeamID) {
			continue
		}
		if params.CustomerID != "" && (vk.CustomerID == nil || *vk.CustomerID != params.CustomerID) {
			continue
		}
		out = append(out, *vk)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetTeam(ctx context.Context, id string, _ ...*gorm.DB) (*tables.TableTeam, error) {
	if f.teams == nil {
		return nil, configstore.ErrNotFound
	}
	team, ok := f.teams[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return team, nil
}

func (f *fakeGovernanceReader) GetTeamsPaginated(ctx context.Context, params configstore.TeamsQueryParams) ([]tables.TableTeam, int64, error) {
	out := make([]tables.TableTeam, 0, len(f.teams))
	for _, team := range f.teams {
		if params.CustomerID != "" && (team.CustomerID == nil || *team.CustomerID != params.CustomerID) {
			continue
		}
		out = append(out, *team)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetCustomer(ctx context.Context, id string, _ ...*gorm.DB) (*tables.TableCustomer, error) {
	if f.customers == nil {
		return nil, configstore.ErrNotFound
	}
	customer, ok := f.customers[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return customer, nil
}

func (f *fakeGovernanceReader) GetCustomersPaginated(ctx context.Context, params configstore.CustomersQueryParams) ([]tables.TableCustomer, int64, error) {
	out := make([]tables.TableCustomer, 0, len(f.customers))
	for _, customer := range f.customers {
		out = append(out, *customer)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error) {
	if f.budgets == nil {
		return nil, configstore.ErrNotFound
	}
	budget, ok := f.budgets[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return budget, nil
}

func (f *fakeGovernanceReader) GetBudgets(ctx context.Context) ([]tables.TableBudget, error) {
	out := make([]tables.TableBudget, 0, len(f.budgets))
	for _, budget := range f.budgets {
		out = append(out, *budget)
	}
	return out, nil
}

func (f *fakeGovernanceReader) GetProviders(ctx context.Context) ([]tables.TableProvider, error) {
	return f.providers, nil
}

func (f *fakeGovernanceReader) GetProviderKeys(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
	if f.providerKeys == nil {
		return nil, configstore.ErrNotFound
	}
	keys, ok := f.providerKeys[provider]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return keys, nil
}

func (f *fakeGovernanceReader) GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	if f.providerCfgs == nil {
		return nil, configstore.ErrNotFound
	}
	cfg, ok := f.providerCfgs[provider]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return &cfg, nil
}

func (f *fakeGovernanceReader) GetMCPClientByID(ctx context.Context, id string) (*tables.TableMCPClient, error) {
	if f.mcpClients == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.mcpClients[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetMCPClientsPaginated(ctx context.Context, params configstore.MCPClientsQueryParams) ([]tables.TableMCPClient, int64, error) {
	out := make([]tables.TableMCPClient, 0, len(f.mcpClients))
	for _, row := range f.mcpClients {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetVirtualMCPByID(ctx context.Context, id uint) (*tables.TableVirtualMCP, error) {
	if f.virtualMCPs == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.virtualMCPs[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetVirtualMCPsPaginated(ctx context.Context, params configstore.VirtualMCPsQueryParams) ([]tables.TableVirtualMCP, int64, error) {
	out := make([]tables.TableVirtualMCP, 0, len(f.virtualMCPs))
	for _, row := range f.virtualMCPs {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetVirtualKeyIDsForVirtualMCP(ctx context.Context, vmcpID uint) ([]string, error) {
	return f.vmcpVKs[vmcpID], nil
}

func (f *fakeGovernanceReader) GetRoutingRule(ctx context.Context, id string) (*tables.TableRoutingRule, error) {
	if f.routing == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.routing[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetRoutingRulesPaginated(ctx context.Context, params configstore.RoutingRulesQueryParams) ([]tables.TableRoutingRule, int64, error) {
	out := make([]tables.TableRoutingRule, 0, len(f.routing))
	for _, row := range f.routing {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetRateLimit(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableRateLimit, error) {
	if f.rateLimits == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.rateLimits[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error) {
	out := make([]tables.TableRateLimit, 0, len(f.rateLimits))
	for _, row := range f.rateLimits {
		out = append(out, *row)
	}
	return out, nil
}

func (f *fakeGovernanceReader) GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error) {
	if f.modelConfigs == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.modelConfigs[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetModelConfig(ctx context.Context, scope string, scopeID *string, modelName string, provider *string) (*tables.TableModelConfig, error) {
	for _, row := range f.modelConfigs {
		if row.Scope == scope && derefString(row.ScopeID) == derefString(scopeID) && row.ModelName == modelName &&
			(row.Provider == nil) == (provider == nil) && derefString(row.Provider) == derefString(provider) {
			return row, nil
		}
	}
	return nil, configstore.ErrNotFound
}

func (f *fakeGovernanceReader) GetModelConfigsPaginated(ctx context.Context, params configstore.ModelConfigsQueryParams) ([]tables.TableModelConfig, int64, error) {
	out := make([]tables.TableModelConfig, 0, len(f.modelConfigs))
	for _, row := range f.modelConfigs {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error) {
	if f.plugins == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.plugins[name]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error) {
	out := make([]*tables.TablePlugin, 0, len(f.plugins))
	for _, row := range f.plugins {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeGovernanceReader) GetPricingOverrideByID(ctx context.Context, id string) (*tables.TablePricingOverride, error) {
	if f.pricing == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.pricing[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetPricingOverridesPaginated(ctx context.Context, params configstore.PricingOverridesQueryParams) ([]tables.TablePricingOverride, int64, error) {
	out := make([]tables.TablePricingOverride, 0, len(f.pricing))
	for _, row := range f.pricing {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) GetWebhookEndpointByID(ctx context.Context, id string) (*tables.TableWebhookEndpoint, error) {
	if f.webhooks == nil {
		return nil, configstore.ErrNotFound
	}
	row, ok := f.webhooks[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeGovernanceReader) GetWebhookEndpointsPaginated(ctx context.Context, params configstore.WebhookEndpointsQueryParams) ([]tables.TableWebhookEndpoint, int64, error) {
	out := make([]tables.TableWebhookEndpoint, 0, len(f.webhooks))
	for _, row := range f.webhooks {
		out = append(out, *row)
	}
	return out, int64(len(out)), nil
}

func (f *fakeGovernanceReader) ListFeatureFlags(ctx context.Context) ([]tables.TableFeatureFlag, error) {
	out := make([]tables.TableFeatureFlag, 0, len(f.flags))
	for _, row := range f.flags {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeGovernanceReader) ListOauthUserTokens(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPOauthToken, error) {
	f.sawSessionParams = append(f.sawSessionParams, "oauth")
	return f.oauthTokens, nil
}

func (f *fakeGovernanceReader) ListMCPPerUserHeaderCredentials(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPPerUserHeaderCredential, error) {
	f.sawSessionParams = append(f.sawSessionParams, "headers")
	return f.headerCreds, nil
}

// The secret of a virtual key is in two answers only, create and rotate, and
// comes from the route's response; no read and no update carries it.
func TestVirtualKeySecretIsReturnedByCreateAndRotateOnly(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{
		"vk-1": {ID: "vk-1", Name: "ops", Value: *schemas.NewSecretVar("sk-bf-stored")},
	}}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"message":"ok","virtual_key":{"id":"vk-1","name":"ops","value":"sk-bf-from-route","is_active":true}}`
	}}
	deps := &Deps{Governance: fake, Admin: admin}

	created, err := runTool(t, "create_virtual_key", deps, map[string]any{"name": "ops"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/governance/virtual-keys", body: map[string]any{
		"name": "ops", "allow_all_providers": true,
	}}, admin.calls[0])
	require.Equal(t, "sk-bf-from-route", created.(map[string]any)["value"])
	require.Contains(t, created.(map[string]any)["note"], "only time")

	rotated, err := runTool(t, "rotate_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/governance/virtual-keys/vk-1/rotate"}, admin.calls[1])
	require.Equal(t, "sk-bf-from-route", rotated.(map[string]any)["value"])

	updated, err := runTool(t, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1", "name": "renamed"})
	require.NoError(t, err)
	require.NotContains(t, boundToolResult(updated), "sk-bf-")

	deactivated, err := runTool(t, "deactivate_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/governance/virtual-keys/vk-1", body: map[string]any{"is_active": false}}, admin.calls[3])
	require.NotContains(t, boundToolResult(deactivated), "sk-bf-")

	listed, err := runTool(t, "list_virtual_keys", deps, map[string]any{})
	require.NoError(t, err)
	require.NotContains(t, boundToolResult(listed), "sk-bf-")

	// A route's refusal reaches the caller in the route's words: a key whose
	// live copy was not reloaded is reported by the route as not done.
	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 500, `{"error":{"message":"Virtual key updated in database but failed to reload in-memory state"}}`
	}}
	_, err = runTool(t, "deactivate_virtual_key", &Deps{Governance: fake, Admin: refused}, map[string]any{"virtual_key_id": "vk-1"})
	require.EqualError(t, err, "Virtual key updated in database but failed to reload in-memory state")
}

// A key's rate limit lives on its key-scoped "*" model config since governance
// moved there, and the route replaces that tier's rate limit with exactly what
// it is sent. The tool merged a partial edit over the key row's legacy rate
// limit - usually empty now - so changing the request limit wiped the token
// limit. It now merges over the tier, falling back to the legacy row, and
// refuses rate-limit edits on a key an access profile manages, whose limits are
// the profile's and are not the key's to change.
func TestUpdateVirtualKeyMergesOverTheKeysModelConfigTier(t *testing.T) {
	tokens, tokenWindow := int64(5000), "1h"
	requests, requestWindow := int64(10), "1m"
	fake := &fakeGovernanceReader{
		byID: map[string]*tables.TableVirtualKey{
			"vk-1":       {ID: "vk-1", Name: "k"},
			"vk-legacy":  {ID: "vk-legacy", Name: "l", RateLimit: &tables.TableRateLimit{ID: "rl-l", RequestMaxLimit: &requests, RequestResetDuration: &requestWindow}},
			"vk-managed": {ID: "vk-managed", Name: "m"},
		},
		modelConfigs: map[string]*tables.TableModelConfig{
			"mc-tier": {ID: "mc-tier", ModelName: tables.ModelConfigAllModels, Scope: tables.ModelConfigScopeVirtualKey, ScopeID: strPtr("vk-1"),
				RateLimit: &tables.TableRateLimit{ID: "rl-tier", TokenMaxLimit: &tokens, TokenResetDuration: &tokenWindow}},
			"mc-openai": {ID: "mc-openai", ModelName: tables.ModelConfigAllModels, Provider: strPtr("openai"), Scope: tables.ModelConfigScopeVirtualKey, ScopeID: strPtr("vk-1"),
				RateLimit: &tables.TableRateLimit{ID: "rl-openai", RequestMaxLimit: &requests, RequestResetDuration: &requestWindow}},
		},
	}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) { return 200, `{"virtual_key":{"id":"vk-1","name":"k"}}` }}
	decorator := func(_ context.Context, vk *tables.TableVirtualKey) ([]string, error) {
		if vk.ID == "vk-managed" {
			vk.IsAccessProfileManaged = true
			return []string{"access profile admin"}, nil
		}
		return nil, nil
	}
	deps := &Deps{Governance: fake, Admin: admin, VKDecorator: decorator}

	_, err := runTool(t, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1", "request_max_limit": 100.0, "request_reset_duration": "1m"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"token_max_limit": 5000.0, "token_reset_duration": "1h", "request_max_limit": 100.0, "request_reset_duration": "1m",
	}, admin.calls[0].body["rate_limit"], "the tier's token limit survives a request-limit edit; the provider tier is not the key's")

	_, err = runTool(t, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-legacy", "token_max_limit": 1.0, "token_reset_duration": "1d"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"token_max_limit": 1.0, "token_reset_duration": "1d", "request_max_limit": 10.0, "request_reset_duration": "1m",
	}, admin.calls[1].body["rate_limit"], "a key with no tier yet merges over its legacy rate limit, which the route adopts")

	_, err = runTool(t, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-managed", "request_max_limit": 1.0})
	require.ErrorContains(t, err, "managed by an access profile")
	require.Len(t, admin.calls, 2, "a managed key's limits are never sent")

	// Anything else on a managed key is still the key's own.
	_, err = runTool(t, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-managed", "name": "renamed"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"name": "renamed"}, admin.calls[2].body)
}

// Budgets and rate limits are set on their owner. The routes replace the rate
// limit with what they are sent, so the limits the caller did not name are
// sent as stored; budgets are the full set and are sent as given, [] included.
func TestOwnerToolsSendBudgetsAndTheWholeRateLimit(t *testing.T) {
	tokens, window := int64(1000), "1h"
	fake := &fakeGovernanceReader{
		teams:      map[string]*tables.TableTeam{"team-1": {ID: "team-1", Name: "ops", RateLimitID: ptr("rl-1")}},
		customers:  map[string]*tables.TableCustomer{"cust-1": {ID: "cust-1", Name: "acme"}},
		rateLimits: map[string]*tables.TableRateLimit{"rl-1": {ID: "rl-1", TokenMaxLimit: &tokens, TokenResetDuration: &window}},
	}
	admin := governanceAdmin()
	deps := &Deps{Governance: fake, Admin: admin}
	budgets := []any{map[string]any{"max_limit": 50.0, "reset_duration": "1d"}, map[string]any{"max_limit": 900.0, "reset_duration": "1M"}}

	_, err := runTool(t, "update_team", deps, map[string]any{"team_id": "team-1", "budgets": budgets, "request_max_limit": float64(10), "request_reset_duration": "1m"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/governance/teams/team-1", body: map[string]any{
		"budgets": budgets,
		"rate_limit": map[string]any{
			"token_max_limit": float64(1000), "token_reset_duration": "1h",
			"request_max_limit": float64(10), "request_reset_duration": "1m",
		},
	}}, admin.calls[0])

	_, err = runTool(t, "update_customer", deps, map[string]any{"customer_id": "cust-1", "budgets": []any{}})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/governance/customers/cust-1", body: map[string]any{"budgets": []any{}}}, admin.calls[1])

	_, err = runTool(t, "update_team", deps, map[string]any{"team_id": "team-1", "name": "platform"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"name": "platform"}, admin.calls[2].body, "naming no limit sends none, which the route leaves alone")

	_, err = runTool(t, "create_team", deps, map[string]any{"name": "new", "budgets": budgets, "token_max_limit": float64(5), "token_reset_duration": "1h"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/governance/teams", body: map[string]any{
		"name": "new", "budgets": budgets,
		"rate_limit": map[string]any{"token_max_limit": float64(5), "token_reset_duration": "1h"},
	}}, admin.calls[3])

	_, err = runTool(t, "create_customer", deps, map[string]any{"name": "globex"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/governance/customers", body: map[string]any{"name": "globex"}}, admin.calls[4])

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 409, `{"error":{"message":"this team is governed by access profile \"gold\", so it cannot have budgets or a rate limit of its own - edit the access profile instead"}}`
	}}
	_, err = runTool(t, "update_team", &Deps{Governance: fake, Admin: refused}, map[string]any{"team_id": "team-1", "budgets": budgets})
	require.ErrorContains(t, err, "governed by access profile")
	require.Equal(t, 409, adminStatus(err))

	_, err = runTool(t, "update_team", deps, map[string]any{"team_id": "team-missing", "name": "x"})
	require.ErrorContains(t, err, `no team with id "team-missing"`)
	require.Len(t, admin.calls, 5)
}

// The tools that wrote budgets and rate limits as rows of their own are gone:
// the admin API has no such write, only the owner's.
func TestBudgetAndRateLimitWritesAreNotDeclared(t *testing.T) {
	for _, name := range []string{"create_budget", "update_budget", "create_rate_limit", "update_rate_limit"} {
		_, ok := toolByName(buildTools(), name)
		require.False(t, ok, "%s must not be hosted", name)
	}
}

// A stdio client is a command this process would run. The tool does not offer
// it, and refuses it before anything is sent.
func TestStdioMCPClientIsRefused(t *testing.T) {
	admin := &fakeAdmin{}
	_, err := runTool(t, "add_mcp_client", &Deps{Governance: &fakeGovernanceReader{}, Admin: admin}, map[string]any{
		"name": "shell", "connection_type": "stdio", "connection_string": "npx foo",
	})
	require.ErrorContains(t, err, "stdio")
	require.Empty(t, admin.calls)
}

// add_mcp_client chooses the client's id, because the route answers without
// one, and allows every tool unless the caller narrows the list.
func TestAddMCPClientCallsTheAdminRoute(t *testing.T) {
	fake := &fakeGovernanceReader{}
	admin := &fakeAdmin{respond: func(call adminCall) (int, string) {
		id, _ := call.body["client_id"].(string)
		fake.mcpClients = map[string]*tables.TableMCPClient{id: {ClientID: id, Name: "github", ConnectionType: "http", ToolsToExecute: schemas.WhiteList{"*"}}}
		return 200, `{"status":"success"}`
	}}
	result, err := runTool(t, "add_mcp_client", &Deps{Governance: fake, Admin: admin}, map[string]any{
		"name": "github", "connection_type": "http", "connection_string": "https://mcp.example.com",
	})
	require.NoError(t, err)
	call := admin.only(t)
	require.Equal(t, "POST", call.method)
	require.Equal(t, "/api/mcp/client", call.path)
	id, _ := call.body["client_id"].(string)
	require.NotEmpty(t, id)
	require.Equal(t, map[string]any{
		"client_id": id, "name": "github", "connection_type": "http",
		"connection_string": "https://mcp.example.com", "tools_to_execute": []any{"*"},
	}, call.body)
	out := result.(map[string]any)
	require.Equal(t, id, out["client_id"])
	require.NotContains(t, boundToolResult(result), "mcp.example.com", "the connection string is stored and never returned")

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 400, `{"error":{"message":"Invalid client name: \"bifrostmcp\" is reserved for Bifrost's built-in MCP server"}}`
	}}
	_, err = runTool(t, "add_mcp_client", &Deps{Governance: fake, Admin: refused}, map[string]any{
		"name": "bifrostmcp", "connection_type": "http", "connection_string": "https://example.com",
	})
	require.ErrorContains(t, err, "reserved")
}

func TestListProviderKeysNeverReturnsSecret(t *testing.T) {
	fake := &fakeGovernanceReader{providerKeys: map[schemas.ModelProvider][]schemas.Key{
		"openai": {{ID: "k1", Name: "prod", Value: *schemas.NewSecretVar("sk-never-leak")}},
	}}
	deps := &Deps{Governance: fake}
	result, err := runTool(t, "list_provider_keys", deps, map[string]any{"provider": "openai"})
	require.NoError(t, err)
	require.NotContains(t, boundToolResult(result), "sk-never-leak")
}

func TestGetHealthSkipsPingsWhenDisabled(t *testing.T) {
	result, err := runTool(t, "get_health", &Deps{DisableDBPings: true}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "ok", out["status"])
	require.Equal(t, "disabled", out["components"].(map[string]string)["config_store"])
}

func TestGetVersionReportsUnknownWhenUnset(t *testing.T) {
	result, err := runTool(t, "get_version", &Deps{}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "unknown", result.(map[string]any)["version"])
}

func TestGetSessionReportsUnknownID(t *testing.T) {
	_, err := runTool(t, "get_session", &Deps{LogManager: &fakeLogReader{}}, map[string]any{"session_id": "missing"})
	require.ErrorContains(t, err, "missing")
}

func TestQueryMCPLogsReturnsProjectedRows(t *testing.T) {
	fake := &fakeLogReader{mcpSearchResult: &logstore.MCPToolLogSearchResult{
		Logs: []logstore.MCPToolLog{{ID: "m1", ToolName: "echo", Status: "success"}},
	}}
	result, err := runTool(t, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{}})
	require.NoError(t, err)
	rows := result.(map[string]any)["rows"].([]map[string]any)
	require.Equal(t, "m1", rows[0]["id"])
	require.Equal(t, "echo", rows[0]["tool_name"])
}

// The virtual MCP tools are the virtual MCP routes' requests. The numeric id
// goes in the path, attach and detach carry no body, and what an update did
// not name is not sent, which the route reads as "leave alone".
func TestVirtualMCPToolsCallTheAdminRoutes(t *testing.T) {
	fake := &fakeGovernanceReader{vmcpVKs: map[uint][]string{7: {"vk-1"}}}
	admin := &fakeAdmin{respond: func(call adminCall) (int, string) {
		if strings.Contains(call.path, "/virtual-keys/") {
			return 200, `{"success":true}`
		}
		return 200, `{"virtual_mcp":{"id":7,"name":"ops-bundle","endpoint_slug":"ops-bundle","enabled":true,` +
			`"tools":[{"mcp_client_id":"c-1","tool_names":["*"]}],"virtual_key_ids":["vk-1"],"created_at":"2026-09-28 10:00:00"}}`
	}}
	deps := &Deps{Governance: fake, Admin: admin}
	tools := []any{map[string]any{"mcp_client_id": "c-1", "tool_names": []any{"*"}}}

	created, err := runTool(t, "create_virtual_mcp", deps, map[string]any{"name": "ops-bundle", "tools": tools})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/mcp/virtual-mcps", body: map[string]any{"name": "ops-bundle", "tools": tools}}, admin.calls[0])
	out := created.(map[string]any)
	require.Equal(t, uint(7), out["id"])
	require.Equal(t, "ops-bundle", out["endpoint_slug"])

	_, err = runTool(t, "update_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(7), "enabled": false})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/mcp/virtual-mcps/7", body: map[string]any{"enabled": false}}, admin.calls[1])

	attached, err := runTool(t, "attach_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(7), "virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/mcp/virtual-mcps/7/virtual-keys/vk-1"}, admin.calls[2])
	require.Equal(t, []string{"vk-1"}, attached.(map[string]any)["virtual_key_ids"])

	_, err = runTool(t, "detach_virtual_mcp", deps, map[string]any{"virtual_mcp_id": float64(7), "virtual_key_id": "vk/../x"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "DELETE", path: "/api/mcp/virtual-mcps/7/virtual-keys/vk%2F..%2Fx"}, admin.calls[3])

	refused := &fakeAdmin{respond: func(adminCall) (int, string) { return 404, `{"error":{"message":"virtual key not found"}}` }}
	_, err = runTool(t, "attach_virtual_mcp", &Deps{Governance: fake, Admin: refused}, map[string]any{"virtual_mcp_id": float64(7), "virtual_key_id": "vk-typo"})
	require.EqualError(t, err, "virtual key not found")
}

// update_mcp_client sends the route what the caller named: the route patches,
// so absent is "leave alone" and [] is "no tools". A null list is refused
// because the route could not tell it from an absent one.
func TestUpdateMCPClientCallsTheAdminRoute(t *testing.T) {
	fake := &fakeGovernanceReader{mcpClients: map[string]*tables.TableMCPClient{
		"c-1": {ClientID: "c-1", Name: "github", ToolsToExecute: schemas.WhiteList{"*"}},
	}}
	admin := &fakeAdmin{}
	deps := &Deps{Governance: fake, Admin: admin}
	_, err := runTool(t, "update_mcp_client", deps, map[string]any{"client_id": "c-1", "disabled": true, "tools_to_execute": []any{"search"}})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/mcp/client/c-1", body: map[string]any{
		"disabled": true, "tools_to_execute": []any{"search"},
	}}, admin.calls[0])

	_, err = runTool(t, "update_mcp_client", deps, map[string]any{"client_id": "c-1", "tools_to_execute": []any{}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"tools_to_execute": []any{}}, admin.calls[1].body)

	_, err = runTool(t, "update_mcp_client", deps, map[string]any{"client_id": "c-1", "tools_to_execute": nil})
	require.ErrorContains(t, err, "tools_to_execute must be an array")
	require.Len(t, admin.calls, 2)

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 500, `{"error":{"message":"Failed to update MCP client: dial boom"}}`
	}}
	_, err = runTool(t, "update_mcp_client", &Deps{Governance: fake, Admin: refused}, map[string]any{"client_id": "c-1", "disabled": false})
	require.EqualError(t, err, "Failed to update MCP client: dial boom")

	_, err = runTool(t, "reconnect_mcp_client", deps, map[string]any{"client_id": "c-1"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/mcp/client/c-1/reconnect"}, admin.calls[2])
}

// A key's secret goes in through create_provider_key and never comes back: the
// route answers with the key redacted, and that is what the caller is shown.
func TestCreateProviderKeyCallsTheAdminRoute(t *testing.T) {
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"id":"key-1","name":"k","value":"sk-****","weight":1,"enabled":true,"models":[]}`
	}}
	result, err := runTool(t, "create_provider_key", &Deps{Admin: admin}, map[string]any{"provider": "openai", "name": "k", "value": "sk-secret"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/providers/openai/keys", body: map[string]any{
		"name": "k", "value": "sk-secret", "weight": 1.0,
	}}, admin.only(t))
	require.NotContains(t, boundToolResult(result), "sk-secret")
	require.Equal(t, "key-1", result.(map[string]any)["key"].(map[string]any)["id"])

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 400, `{"error":{"message":"ollama_key_config.url is required for Ollama keys"}}`
	}}
	_, err = runTool(t, "create_provider_key", &Deps{Admin: refused}, map[string]any{"provider": "ollama", "name": "k", "value": "x"})
	require.EqualError(t, err, "ollama_key_config.url is required for Ollama keys")
}

// The key route is a patch: a field the body leaves out keeps its stored
// value. update_provider_key sent the whole stored key back (redacted), which
// re-sent every placeholder, status and config the caller never touched. It
// now sends only what the caller named - plus the name, which the route does
// not carry over when it is absent - and no stored secret ever leaves.
func TestUpdateProviderKeySendsOnlyTheNamedFields(t *testing.T) {
	enabled := true
	fake := &fakeGovernanceReader{providerKeys: map[schemas.ModelProvider][]schemas.Key{
		"openai": {{ID: "key-1", Name: "k", Value: *schemas.NewSecretVar("sk-stored-secret"), Weight: 0.7, Enabled: &enabled, Models: schemas.WhiteList{"gpt-4o"}, Status: "success"}},
	}}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"id":"key-1","name":"k","weight":0.7,"enabled":false,"models":["gpt-4o"]}`
	}}
	deps := &Deps{Governance: fake, Admin: admin}
	result, err := runTool(t, "update_provider_key", deps, map[string]any{"provider": "openai", "key_id": "key-1", "enabled": false})
	require.NoError(t, err)
	call := admin.only(t)
	require.Equal(t, "PUT", call.method)
	require.Equal(t, "/api/providers/openai/keys/key-1", call.path)
	require.Equal(t, map[string]any{"enabled": false, "name": "k"}, call.body)
	require.Equal(t, false, result.(map[string]any)["key"].(map[string]any)["enabled"])

	// A replacement secret, a rename and a zero weight are sent as given.
	_, err = runTool(t, "update_provider_key", deps, map[string]any{"provider": "openai", "key_id": "key-1", "value": "sk-new", "name": "renamed", "weight": 0.0})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"value": "sk-new", "name": "renamed", "weight": 0.0}, admin.calls[1].body)

	_, err = runTool(t, "update_provider_key", deps, map[string]any{"provider": "openai", "key_id": "key-1", "weight": -1.0})
	require.ErrorContains(t, err, "weight")
	_, err = runTool(t, "update_provider_key", deps, map[string]any{"provider": "openai", "key_id": "key-missing", "weight": 0.5})
	require.ErrorContains(t, err, `no key "key-missing" on provider "openai"`)
	require.Len(t, admin.calls, 2)
}

// A provider key is more than a value: Bedrock authenticates with AWS
// credentials and Vertex with a project and region, and those (or a custom
// provider built on them) take no value at all. create_provider_key now takes
// those configs field by field and leaves value optional for them; weight 0
// (never selected) is allowed, as the route allows it. OAuth-minted keys and
// any key config naming an endpoint are refused before dispatch: the route
// only accepts a dial target from an authenticated admin session.
func TestCreateProviderKeyTakesProviderKeyConfigs(t *testing.T) {
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"id":"key-1","name":"k","weight":0}`
	}}
	deps := &Deps{Admin: admin}
	_, err := runTool(t, "create_provider_key", deps, map[string]any{
		"provider": "bedrock", "name": "aws", "weight": 0.0,
		"bedrock_key_config": map[string]any{"access_key": "AKIA", "secret_key": "s3cr3t", "region": "us-east-1", "unknown": "x"},
	})
	require.ErrorContains(t, err, "unknown")
	_, err = runTool(t, "create_provider_key", deps, map[string]any{
		"provider": "bedrock", "name": "aws", "weight": 0.0,
		"bedrock_key_config": map[string]any{"access_key": "AKIA", "secret_key": "s3cr3t", "region": "us-east-1"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"name": "aws", "weight": 0.0,
		"bedrock_key_config": map[string]any{"access_key": "AKIA", "secret_key": "s3cr3t", "region": "us-east-1"},
	}, admin.calls[0].body)

	_, err = runTool(t, "create_provider_key", deps, map[string]any{
		"provider": "vertex", "name": "gcp", "vertex_key_config": map[string]any{"project_id": "p-1", "region": "us-central1"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"project_id": "p-1", "region": "us-central1"}, admin.calls[1].body["vertex_key_config"])
	require.NotContains(t, admin.calls[1].body, "value")

	for _, args := range []map[string]any{
		{"provider": "openai", "name": "k", "oauth_key_config": map[string]any{"token_url": "https://idp/token"}},
		{"provider": "azure", "name": "k", "value": "x", "azure_key_config": map[string]any{"endpoint": "https://x"}},
		{"provider": "openai", "name": "k", "value": "x", "weight": -0.5},
		{"provider": "openai", "name": "k"},
	} {
		_, err := runTool(t, "create_provider_key", deps, args)
		require.Error(t, err, "%v", args)
	}
	require.Len(t, admin.calls, 2)
	tool, _ := toolByName(buildTools(), "create_provider_key")
	require.Contains(t, tool.schemaJSON, `"minimum": 0`)
	require.Contains(t, tool.description, "dashboard")
}

// pluginAdminRoute stands in for the plugin routes over fake's plugins: create
// stores the body, update replaces enabled and merges config over the stored
// one at the top level, as the routes do.
func pluginAdminRoute(fake *fakeGovernanceReader) *fakeAdmin {
	return &fakeAdmin{respond: func(call adminCall) (int, string) {
		if fake.plugins == nil {
			fake.plugins = map[string]*tables.TablePlugin{}
		}
		name, _ := call.body["name"].(string)
		if call.method == "PUT" {
			name = strings.TrimPrefix(call.path, "/api/plugins/")
		}
		plugin := fake.plugins[name]
		if plugin == nil {
			plugin = &tables.TablePlugin{Name: name}
			fake.plugins[name] = plugin
		}
		plugin.Enabled, _ = call.body["enabled"].(bool)
		if incoming, ok := call.body["config"].(map[string]any); ok {
			merged := map[string]any{}
			if stored, ok := plugin.Config.(map[string]any); ok {
				maps.Copy(merged, stored)
			}
			maps.Copy(merged, incoming)
			plugin.Config = merged
		}
		return 200, `{"message":"ok"}`
	}}
}

// update_plugin sends the route only the config keys the caller named - the
// route merges them over the stored config - and sends enabled, placement and
// order as stored when the caller did not name them, because the route
// replaces those rather than patching them.
func TestUpdatePluginSendsStoredFlagsAndNamedConfig(t *testing.T) {
	placement := schemas.PluginPlacementPostBuiltin
	order := 3
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
		"otel": {Name: "otel", Enabled: true, Placement: &placement, Order: &order, Config: map[string]any{"service_name": "old", "plugin_span_filter": "llm"}},
	}}
	admin := pluginAdminRoute(fake)
	_, err := runTool(t, "update_plugin", &Deps{Governance: fake, Admin: admin}, map[string]any{"name": "otel", "config": map[string]any{"service_name": "new"}})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/plugins/otel", body: map[string]any{
		"enabled": true, "placement": "post_builtin", "order": float64(3), "config": map[string]any{"service_name": "new"},
	}}, admin.only(t))

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 400, `{"error":{"message":"Invalid plugin configuration: service_name: invalid"}}`
	}}
	_, err = runTool(t, "update_plugin", &Deps{Governance: fake, Admin: refused}, map[string]any{"name": "otel", "config": map[string]any{"service_name": "::"}})
	require.EqualError(t, err, "Invalid plugin configuration: service_name: invalid")

	// The route creates a plugin it does not find; a mistyped name must not.
	_, err = runTool(t, "update_plugin", &Deps{Governance: fake, Admin: admin}, map[string]any{"name": "otle", "enabled": false})
	require.ErrorContains(t, err, `no plugin named "otle"`)
	require.Len(t, admin.calls, 1)
}

// The plugin routes refuse, with 403, a change to where a plugin sends data
// or which env./vault. secrets it reads when the request was not genuinely
// authenticated - and the in-process dispatcher runs every request that way.
// So such a change can never succeed from here; the tools refuse it before
// dispatch with a message that says where it can be made, and keep sending
// changes that leave those values as stored.
func TestPluginToolsRefuseEndpointAndSecretReferenceChanges(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
		"otel": {Name: "otel", Enabled: true, Config: map[string]any{
			"collector_url": "https://collector.example.com",
			"headers":       map[string]any{"x-key": "env.OTEL_KEY"},
			"service_name":  "gw",
		}},
	}}
	admin := pluginAdminRoute(fake)
	deps := &Deps{Governance: fake, Admin: admin}

	for _, tc := range []struct {
		tool   string
		config map[string]any
		path   string
	}{
		{"update_plugin", map[string]any{"collector_url": "https://evil.example.com"}, "collector_url"},
		{"update_plugin", map[string]any{"Collector_URL": "https://evil.example.com"}, "Collector_URL"},
		{"update_plugin", map[string]any{"headers": map[string]any{"x-key": "env.OTEL_KEY", "x-leak": "env.OPENAI_API_KEY"}}, "headers.x-leak"},
		{"update_plugin", map[string]any{"profiles": []any{map[string]any{"token": map[string]any{"type": "vault", "ref": "secret/x"}}}}, "profiles[0].token"},
		{"create_plugin", map[string]any{"push_gateway_url": "https://push.example.com"}, "push_gateway_url"},
	} {
		_, err := runTool(t, tc.tool, deps, map[string]any{"name": "otel", "config": tc.config})
		require.ErrorContains(t, err, tc.path, "%s %v", tc.tool, tc.config)
		require.ErrorContains(t, err, "dashboard", "the refusal says where it can be done")
	}
	require.Empty(t, admin.calls, "a change the route would refuse is never sent")

	// Echoing the stored values, or editing anything else, still goes through.
	_, err := runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "config": map[string]any{
		"collector_url": "https://collector.example.com", "headers": map[string]any{"x-key": "env.OTEL_KEY"}, "service_name": "renamed",
	}})
	require.NoError(t, err)
	require.Len(t, admin.calls, 1)

	tool, _ := toolByName(buildTools(), "update_plugin")
	require.Contains(t, tool.description, "env.")
}

// A path cannot be sent over MCP, and the route clears a path it is not sent,
// so a plugin loaded from one is not updated from here at all.
func TestUpdatePluginRefusesPluginLoadedFromPath(t *testing.T) {
	path := "/opt/plugins/custom.so"
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{"custom": {Name: "custom", Enabled: true, IsCustom: true, Path: &path}}}
	admin := pluginAdminRoute(fake)
	_, err := runTool(t, "update_plugin", &Deps{Governance: fake, Admin: admin}, map[string]any{"name": "custom", "enabled": false})
	require.ErrorContains(t, err, "loaded from a path")
	require.Empty(t, admin.calls)
	require.True(t, fake.plugins["custom"].Enabled)
}

func TestDeleteToolsAreNotDeclared(t *testing.T) {
	for _, name := range []string{
		"delete_provider_key",
		"delete_mcp_client",
		"delete_virtual_mcp",
		"delete_routing_rule",
		"delete_rate_limit",
		"delete_model_config",
		"delete_plugin",
		"delete_pricing_override",
		"delete_webhook",
		"delete_virtual_key",
		"delete_team",
		"delete_customer",
	} {
		_, ok := toolByName(buildTools(), name)
		require.False(t, ok, "%s must not be hosted", name)
	}
}

// The routing tools are the routing routes' requests: what the caller named is
// what is sent, under the route's own field names, and the rule the route
// answers with is what the caller is shown. Validation is the route's.
func TestRoutingRuleToolsCallTheAdminRoutes(t *testing.T) {
	target := []any{map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0}}
	admin := &fakeAdmin{respond: func(call adminCall) (int, string) {
		return 200, `{"message":"ok","rule":{"id":"rr-1","name":"prod-split","scope":"global","priority":0,"enabled":true,` +
			`"cel_expression":"","targets":[{"provider":"openai","model":"gpt-4o","weight":1}],` +
			`"fallbacks":["anthropic/claude-sonnet-4",{"provider":"openai","model":"gpt-4o","key_id":"key-1"}]}}`
	}}
	deps := &Deps{Admin: admin}

	result, err := runTool(t, "create_routing_rule", deps, map[string]any{
		"name": "prod-split", "targets": target, "cel_expression": "", "fallbacks": []any{"anthropic/claude-sonnet-4", "azure/"},
	})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/routing/rules", body: map[string]any{
		"name": "prod-split", "targets": target, "cel_expression": "", "fallbacks": []any{"anthropic/claude-sonnet-4", "azure/"},
	}}, admin.calls[0])
	out := result.(map[string]any)
	require.Equal(t, "rr-1", out["id"])
	require.Equal(t, "prod-split", out["name"])
	// A key pin set elsewhere is shown as it is stored, not flattened to a string.
	fallbacks, err := json.Marshal(out["fallbacks"])
	require.NoError(t, err)
	require.JSONEq(t, `["anthropic/claude-sonnet-4", {"provider": "openai", "model": "gpt-4o", "key_id": "key-1"}]`, string(fallbacks))

	// An update sends only what was named: absent keeps the stored value, and
	// an explicit empty cel_expression is the instruction to clear it.
	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": "rr-1", "name": "renamed"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/routing/rules/rr-1", body: map[string]any{"name": "renamed"}}, admin.calls[1])

	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": "rr-1", "cel_expression": ""})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"cel_expression": ""}, admin.calls[2].body)

	// The id is never part of the body, and never more than one path segment.
	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": "../config", "enabled": false})
	require.NoError(t, err)
	require.Equal(t, "/api/routing/rules/..%2Fconfig", admin.calls[3].path)
	require.Equal(t, map[string]any{"enabled": false}, admin.calls[3].body)

	_, err = runTool(t, "create_routing_rule", deps, map[string]any{"name": "r"})
	require.ErrorContains(t, err, "targets is required")
	// The route would store this as a global rule; the caller asked for less.
	_, err = runTool(t, "create_routing_rule", deps, map[string]any{"name": "r", "targets": target, "scope_id": "team-1"})
	require.ErrorContains(t, err, "scope_id must be omitted when scope is global")
	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"name": "r"})
	require.ErrorContains(t, err, "rule_id")
	require.Len(t, admin.calls, 4, "a call missing a required argument is never sent")

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 400, `{"error":{"message":"invalid CEL expression: syntax error"}}`
	}}
	_, err = runTool(t, "create_routing_rule", &Deps{Admin: refused}, map[string]any{"name": "r", "targets": target, "cel_expression": "model !! x"})
	require.EqualError(t, err, "invalid CEL expression: syntax error")
}

// Targets carry a per-target streaming first-token deadline (ttft_timeout_ms).
// The tools did not know it: create could not set one, describe did not show
// one, and update - which replaces the whole target list when targets is
// sent - silently dropped every stored deadline. Targets are now built field
// by field with the deadline, and an updated target that does not name one
// keeps the deadline of the stored target with the same provider, model and
// key; 0 clears it. Weights must be above 0, as the route requires.
func TestRoutingRuleTargetsCarryTheirTTFTDeadline(t *testing.T) {
	ms := func(v int) *int { return &v }
	fake := &fakeGovernanceReader{routing: map[string]*tables.TableRoutingRule{
		"rr-1": {ID: "rr-1", Name: "r", Targets: []tables.TableRoutingTarget{
			{Provider: strPtr("openai"), Model: strPtr("gpt-4o"), Weight: 0.5, TTFTTimeoutMs: ms(1500)},
			{Provider: strPtr("anthropic"), Model: strPtr("claude-sonnet"), Weight: 0.5, TTFTTimeoutMs: ms(3000)},
		}},
	}}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"rule":{"id":"rr-1","name":"r","targets":[{"provider":"openai","model":"gpt-4o","weight":1,"ttft_timeout_ms":1500}]}}`
	}}
	deps := &Deps{Governance: fake, Admin: admin}

	described, err := runTool(t, "describe_routing_rule", deps, map[string]any{"rule_id": "rr-1"})
	require.NoError(t, err)
	require.Equal(t, 1500, described.(map[string]any)["targets"].([]map[string]any)[0]["ttft_timeout_ms"])

	out, err := runTool(t, "create_routing_rule", deps, map[string]any{"name": "r", "targets": []any{
		map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0, "ttft_timeout_ms": 1500.0},
	}})
	require.NoError(t, err)
	require.Equal(t, []any{map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0, "ttft_timeout_ms": 1500.0}}, admin.calls[0].body["targets"])
	require.Equal(t, 1500, out.(map[string]any)["targets"].([]map[string]any)[0]["ttft_timeout_ms"])

	// Reweighting the stored targets without naming deadlines keeps them;
	// an explicit 0 clears one, and a new target has none.
	_, err = runTool(t, "update_routing_rule", deps, map[string]any{"rule_id": "rr-1", "targets": []any{
		map[string]any{"provider": "OpenAI", "model": "gpt-4o", "weight": 0.6},
		map[string]any{"provider": "anthropic", "model": "claude-sonnet", "weight": 0.2, "ttft_timeout_ms": 0.0},
		map[string]any{"provider": "gemini", "model": "gemini-2.5-pro", "weight": 0.2},
	}})
	require.NoError(t, err)
	require.Equal(t, []any{
		map[string]any{"provider": "OpenAI", "model": "gpt-4o", "weight": 0.6, "ttft_timeout_ms": 1500.0},
		map[string]any{"provider": "anthropic", "model": "claude-sonnet", "weight": 0.2, "ttft_timeout_ms": 0.0},
		map[string]any{"provider": "gemini", "model": "gemini-2.5-pro", "weight": 0.2},
	}, admin.calls[1].body["targets"])

	for _, bad := range []map[string]any{
		{"provider": "openai", "weight": 0.0},
		{"provider": "openai", "weight": 1.0, "ttft_timeout_ms": -1.0},
		{"provider": "openai", "weight": 1.0, "extra": "x"},
	} {
		_, err = runTool(t, "create_routing_rule", deps, map[string]any{"name": "r", "targets": []any{bad}})
		require.Error(t, err, "%v", bad)
	}
	require.Len(t, admin.calls, 2, "a rejected target is never sent")

	tool, _ := toolByName(buildTools(), "create_routing_rule")
	require.Contains(t, tool.schemaJSON, `"exclusiveMinimum": 0`)
	require.Contains(t, tool.description, "409")
}

// describe must not drop a key pin set elsewhere (the UI or config.json) by
// flattening the fallback to a "provider/model" string.
func TestDescribeRoutingRuleKeepsFallbackKeyPins(t *testing.T) {
	fake := &fakeGovernanceReader{routing: map[string]*tables.TableRoutingRule{
		"rr-1": {ID: "rr-1", Name: "r", ParsedFallbacks: []tables.RoutingFallback{
			{Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o-mini"}},
			{Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o", KeyID: "key-1"}},
		}},
	}}
	result, err := runTool(t, "describe_routing_rule", &Deps{Governance: fake}, map[string]any{"rule_id": "rr-1"})
	require.NoError(t, err)
	body, err := json.Marshal(result.(map[string]any)["fallbacks"])
	require.NoError(t, err)
	require.JSONEq(t, `["openai/gpt-4o-mini", {"provider": "openai", "model": "gpt-4o", "key_id": "key-1"}]`, string(body))
}

// The model config tools take a budget and a rate limit as flat arguments and
// send them the way the route takes them: nested, under the route's names.
func TestCreateModelConfigCallsTheAdminRoute(t *testing.T) {
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"message":"ok","model_config":{"id":"mc-1","model_name":"gpt-4o","provider":"openai","scope":"global",` +
			`"rate_limit":{"id":"rl-1","token_max_limit":1000,"token_reset_duration":"1h"},` +
			`"budgets":[{"id":"b-1","max_limit":50,"reset_duration":"1d"}]}}`
	}}
	result, err := runTool(t, "create_model_config", &Deps{Governance: &fakeGovernanceReader{}, Admin: admin}, map[string]any{
		"model_name":           "gpt-4o",
		"provider":             "openai",
		"max_limit":            50.0,
		"reset_duration":       "1d",
		"token_max_limit":      float64(1000),
		"token_reset_duration": "1h",
	})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/governance/model-configs", body: map[string]any{
		"model_name": "gpt-4o", "provider": "openai", "scope": "global",
		"budgets":    []any{map[string]any{"max_limit": 50.0, "reset_duration": "1d"}},
		"rate_limit": map[string]any{"token_max_limit": float64(1000), "token_reset_duration": "1h"},
	}}, admin.only(t))
	out := result.(map[string]any)
	require.Equal(t, "mc-1", out["id"])
	require.Equal(t, "global", out["scope"])
	require.Len(t, out["budgets"], 1)

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 400, `{"error":{"message":"invalid reset duration format: soon"}}`
	}}
	_, err = runTool(t, "create_model_config", &Deps{Governance: &fakeGovernanceReader{}, Admin: refused}, map[string]any{
		"model_name": "gpt-4o", "max_limit": 50.0, "reset_duration": "soon",
	})
	require.EqualError(t, err, "invalid reset duration format: soon")
}

// The model config route replaces the rate limit with what it is sent, so
// update_model_config sends the limits the caller did not name as stored; a
// call naming no limit sends no rate limit at all, which the route leaves alone.
func TestUpdateModelConfigSendsTheWholeRateLimit(t *testing.T) {
	tokens, window := int64(1000), "1h"
	fake := &fakeGovernanceReader{
		modelConfigs: map[string]*tables.TableModelConfig{"mc-1": {ID: "mc-1", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeGlobal, RateLimitID: ptr("rl-1")}},
		rateLimits:   map[string]*tables.TableRateLimit{"rl-1": {ID: "rl-1", TokenMaxLimit: &tokens, TokenResetDuration: &window}},
	}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"message":"ok","model_config":{"id":"mc-1","model_name":"gpt-4o","scope":"global"}}`
	}}
	deps := &Deps{Governance: fake, Admin: admin}

	_, err := runTool(t, "update_model_config", deps, map[string]any{
		"model_config_id": "mc-1", "request_max_limit": float64(10), "request_reset_duration": "1m",
	})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/governance/model-configs/mc-1", body: map[string]any{
		"rate_limit": map[string]any{
			"token_max_limit": float64(1000), "token_reset_duration": "1h",
			"request_max_limit": float64(10), "request_reset_duration": "1m",
		},
	}}, admin.calls[0])

	_, err = runTool(t, "update_model_config", deps, map[string]any{"model_config_id": "mc-1", "model_name": "gpt-4o-mini"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"model_name": "gpt-4o-mini"}, admin.calls[1].body)

	_, err = runTool(t, "update_model_config", deps, map[string]any{"model_config_id": "mc-missing", "model_name": "x"})
	require.ErrorContains(t, err, `no model config with id "mc-missing"`)
	require.Len(t, admin.calls, 2)
}

// Plugin config is free-form and routinely carries provider or backend
// credentials; describe_plugin must not hand them to an MCP caller.
func TestDescribePluginRedactsCredentialConfig(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
		"otel": {Name: "otel", Enabled: true, Config: map[string]any{
			"collector_url": "https://otel.example.com",
			"api_key":       "sk-must-not-leak",
			"max_tokens":    float64(512),
			"headers":       map[string]any{"Authorization": "Bearer also-secret", "x-team": "ops"},
			"password":      "env.OTEL_PASSWORD",
		}},
	}}
	result, err := runTool(t, "describe_plugin", &Deps{Governance: fake}, map[string]any{"name": "otel"})
	require.NoError(t, err)
	serialized := boundToolResult(result)
	require.NotContains(t, serialized, "sk-must-not-leak")
	require.NotContains(t, serialized, "also-secret")
	config := result.(map[string]any)["config"].(map[string]any)
	require.Equal(t, "https://otel.example.com", config["collector_url"])
	require.Equal(t, float64(512), config["max_tokens"])
	require.Equal(t, "ops", config["headers"].(map[string]any)["x-team"])
	require.Equal(t, "env.OTEL_PASSWORD", config["password"])
}

// The plugin loader executes whatever file path it is given, so MCP callers
// must not be able to choose one.
func TestPluginToolsRejectPath(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{"custom": {Name: "custom"}}}
	admin := pluginAdminRoute(fake)
	deps := &Deps{Governance: fake, Admin: admin}
	_, err := runTool(t, "create_plugin", deps, map[string]any{"name": "evil", "path": "/tmp/evil.so"})
	require.ErrorContains(t, err, "path cannot be set over MCP")
	_, err = runTool(t, "update_plugin", deps, map[string]any{"name": "custom", "path": "../../evil.so"})
	require.ErrorContains(t, err, "path cannot be set over MCP")
	require.Nil(t, fake.plugins["custom"].Path)
	require.NotContains(t, fake.plugins, "evil")
	require.Empty(t, admin.calls, "a call naming a path is never sent")
}

// The provider route takes concurrency and buffer size as a pair and replaces
// the network config with what it is sent, so update_provider sends the half
// of the pair the caller did not name, and the network config, as stored.
// Whether the pair is valid is the route's answer.
func TestUpdateProviderSendsStoredConfig(t *testing.T) {
	fake := &fakeGovernanceReader{providerCfgs: map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {
			ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 4, BufferSize: 10},
			NetworkConfig:            &schemas.NetworkConfig{BaseURL: "https://gateway.example.com", MaxRetries: 3},
			Description:              "discovery failed: 401",
		},
	}}
	admin := &fakeAdmin{}
	result, err := runTool(t, "update_provider", &Deps{Governance: fake, Admin: admin}, map[string]any{"provider": "openai", "concurrency": float64(8)})
	require.NoError(t, err)
	call := admin.only(t)
	require.Equal(t, "PUT", call.method)
	require.Equal(t, "/api/providers/openai", call.path)
	require.Equal(t, map[string]any{"concurrency": float64(8), "buffer_size": float64(10)}, call.body["concurrency_and_buffer_size"])
	network := call.body["network_config"].(map[string]any)
	require.Equal(t, "https://gateway.example.com", network["base_url"])
	require.Equal(t, float64(3), network["max_retries"])
	require.NotContains(t, call.body, "description", "description is reserved for discovery errors")
	require.NotContains(t, call.body, "keys", "keys have routes of their own")
	require.Equal(t, map[string]any{"provider": "openai", "concurrency": 8, "buffer_size": 10}, result)

	refused := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 400, `{"error":{"message":"Concurrency must be less than or equal to buffer size"}}`
	}}
	_, err = runTool(t, "update_provider", &Deps{Governance: fake, Admin: refused}, map[string]any{"provider": "openai", "concurrency": float64(20)})
	require.EqualError(t, err, "Concurrency must be less than or equal to buffer size")
	require.Equal(t, float64(20), refused.only(t).body["concurrency_and_buffer_size"].(map[string]any)["concurrency"])

	// The route creates a provider it does not find; a mistyped name must not.
	_, err = runTool(t, "update_provider", &Deps{Governance: fake, Admin: admin}, map[string]any{"provider": "opnai", "concurrency": float64(8)})
	require.ErrorContains(t, err, `no provider named "opnai"`)
	require.Len(t, admin.calls, 1)
}

// The provider route builds the new config from the stored one but takes
// send_back_raw_request and send_back_raw_response only from the body, so a
// body without them switched both off. update_provider sends them as stored.
func TestUpdateProviderKeepsSendBackRawFlags(t *testing.T) {
	fake := &fakeGovernanceReader{providerCfgs: map[schemas.ModelProvider]configstore.ProviderConfig{
		"openai": {
			ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 4, BufferSize: 10},
			SendBackRawRequest:       true,
			SendBackRawResponse:      true,
		},
		"anthropic": {ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 4, BufferSize: 10}},
	}}
	admin := &fakeAdmin{}
	_, err := runTool(t, "update_provider", &Deps{Governance: fake, Admin: admin}, map[string]any{"provider": "openai", "buffer_size": float64(20)})
	require.NoError(t, err)
	require.Equal(t, true, admin.calls[0].body["send_back_raw_request"])
	require.Equal(t, true, admin.calls[0].body["send_back_raw_response"])

	_, err = runTool(t, "update_provider", &Deps{Governance: fake, Admin: admin}, map[string]any{"provider": "anthropic", "buffer_size": float64(20)})
	require.NoError(t, err)
	require.Equal(t, false, admin.calls[1].body["send_back_raw_request"])
	require.Equal(t, false, admin.calls[1].body["send_back_raw_response"])
}

// GetProviders does not load a provider's keys, so key_count read from its
// rows was 0 for every provider. The count comes from the provider's keys.
func TestListProvidersCountsKeys(t *testing.T) {
	fake := &fakeGovernanceReader{
		providers: []tables.TableProvider{{Name: "openai"}, {Name: "ollama", Status: "error", Description: "connection refused"}},
		providerKeys: map[schemas.ModelProvider][]schemas.Key{
			"openai": {{ID: "k-1", Value: *schemas.NewSecretVar("sk-secret")}, {ID: "k-2"}},
		},
	}
	result, err := runTool(t, "list_providers", &Deps{Governance: fake}, map[string]any{})
	require.NoError(t, err)
	rows := result.(map[string]any)["providers"].([]map[string]any)
	require.Len(t, rows, 2)
	require.Equal(t, 2, rows[0]["key_count"])
	require.Equal(t, 0, rows[1]["key_count"], "a keyless provider has none")
	require.Equal(t, "connection refused", rows[1]["description"])
	require.NotContains(t, boundToolResult(result), "sk-secret")
}

// A shared http, sse or openapi client keeps its tools in the live MCP
// manager; the stored row's discovered-tools column is only filled for some
// clients, so the tool answered [] for clients that plainly had tools. The
// live list is read first, and the stored one is the fallback.
func TestListMCPClientToolsReadsTheLiveManager(t *testing.T) {
	fake := &fakeGovernanceReader{mcpClients: map[string]*tables.TableMCPClient{
		"c-live":   {ClientID: "c-live", Name: "github", ConnectionType: "http"},
		"c-stored": {ClientID: "c-stored", Name: "local", DiscoveredToolsJSON: `{"echo":{"type":"function"}}`},
	}}
	live := func(_ context.Context, clientID string) ([]string, bool) {
		if clientID == "c-live" {
			return []string{"create_issue", "search_code"}, true
		}
		return nil, false
	}
	deps := &Deps{Governance: fake, MCPTools: live}

	result, err := runTool(t, "list_mcp_client_tools", deps, map[string]any{"client_id": "c-live"})
	require.NoError(t, err)
	require.Equal(t, []string{"create_issue", "search_code"}, result.(map[string]any)["tools"])
	require.Equal(t, 2, result.(map[string]any)["returned"])

	result, err = runTool(t, "list_mcp_client_tools", deps, map[string]any{"client_id": "c-stored"})
	require.NoError(t, err)
	require.Equal(t, []string{"echo"}, result.(map[string]any)["tools"])

	result, err = runTool(t, "list_mcp_client_tools", &Deps{Governance: fake}, map[string]any{"client_id": "c-stored"})
	require.NoError(t, err)
	require.Equal(t, []string{"echo"}, result.(map[string]any)["tools"], "no live manager: the stored list")
}

// A keyless provider (ollama, a self-hosted endpoint) runs discovery without a
// key and records the outcome on the provider itself, so the refresh answer -
// its keys - was an empty list that said nothing about whether discovery
// worked. The provider's own status and error are now reported.
func TestRefreshProviderModelsReportsAKeylessProvidersStatus(t *testing.T) {
	fake := &fakeGovernanceReader{providerCfgs: map[schemas.ModelProvider]configstore.ProviderConfig{
		"ollama": {Status: "failed", Description: "dial tcp 127.0.0.1:11434: connection refused"},
	}}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) { return 200, `{"keys":[],"total":0}` }}
	result, err := runTool(t, "refresh_provider_models", &Deps{Governance: fake, Admin: admin}, map[string]any{"provider": "ollama"})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "failed", out["status"])
	require.Equal(t, "dial tcp 127.0.0.1:11434: connection refused", out["description"])
	require.Empty(t, out["keys"])
}

func TestProviderToolsCallTheAdminRoutes(t *testing.T) {
	admin := &fakeAdmin{respond: func(call adminCall) (int, string) {
		if strings.HasSuffix(call.path, "/keys/key-1/refresh-models") {
			return 200, `{"id":"key-1","name":"k","weight":1,"status":"success"}`
		}
		return 200, `{"keys":[{"id":"key-1","name":"k","weight":1,"status":"success"}],"total":1}`
	}}
	deps := &Deps{Admin: admin}

	result, err := runTool(t, "add_provider", deps, map[string]any{"provider": "anthropic"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/providers", body: map[string]any{"provider": "anthropic"}}, admin.calls[0])
	require.Equal(t, map[string]any{"provider": "anthropic", "live": true}, result)

	result, err = runTool(t, "refresh_provider_models", deps, map[string]any{"provider": "openai"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/providers/openai/refresh-models"}, admin.calls[1])
	require.Len(t, result.(map[string]any)["keys"], 1)

	result, err = runTool(t, "refresh_provider_models", deps, map[string]any{"provider": "openai", "key_id": "key-1"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/providers/openai/keys/key-1/refresh-models"}, admin.calls[2])
	require.EqualValues(t, "success", result.(map[string]any)["key"].(map[string]any)["status"])
}

// The pricing tools send the route what the caller named, under the route's
// names; the scope ids an update does not expose are never in the body, where
// a null would clear them.
func TestPricingOverrideToolsCallTheAdminRoutes(t *testing.T) {
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"message":"ok","pricing_override":{"id":"po-1","name":"cheap","scope_kind":"global","match_type":"exact","pattern":"gpt-4o","pricing_patch":"{\"input_cost_per_token\":0.000001}"}}`
	}}
	deps := &Deps{Admin: admin}
	patch := map[string]any{"input_cost_per_token": 0.000001}

	result, err := runTool(t, "create_pricing_override", deps, map[string]any{
		"name": "cheap", "scope_kind": "global", "match_type": "exact", "pattern": "gpt-4o", "patch": patch,
		"request_types": []any{"chat_completion"},
	})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/governance/pricing-overrides", body: map[string]any{
		"name": "cheap", "scope_kind": "global", "match_type": "exact", "pattern": "gpt-4o", "patch": patch,
		"request_types": []any{"chat_completion"},
	}}, admin.calls[0])
	out := result.(map[string]any)
	require.Equal(t, "po-1", out["id"])
	require.Equal(t, map[string]any{"input_cost_per_token": 0.000001}, out["patch"])

	_, err = runTool(t, "update_pricing_override", deps, map[string]any{"override_id": "po-1", "pattern": "gpt-*", "match_type": "wildcard"})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/governance/pricing-overrides/po-1", body: map[string]any{
		"pattern": "gpt-*", "match_type": "wildcard",
	}}, admin.calls[1])

	_, err = runTool(t, "create_pricing_override", deps, map[string]any{"name": "cheap", "scope_kind": "global", "match_type": "exact", "pattern": "gpt-4o", "request_types": []any{"chat_completion"}})
	require.ErrorContains(t, err, "patch is required")
	_, err = runTool(t, "create_pricing_override", deps, map[string]any{"name": "cheap", "scope_kind": "global", "match_type": "exact", "pattern": "gpt-4o", "patch": patch})
	require.ErrorContains(t, err, "request_types is required")
	require.Len(t, admin.calls, 2)
}

// create_webhook is the one place the signing secret is shown, and it comes
// from the route's response, not from a row the tool holds.
func TestCreateWebhookCallsTheAdminRoute(t *testing.T) {
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 201, `{"endpoint":{"id":"wh-1","name":"alerts","url":"https://example.com/hook","events":["async_job.completed"],"disabled":false},"secret":"whsec_test"}`
	}}
	result, err := runTool(t, "create_webhook", &Deps{Admin: admin}, map[string]any{
		"name":   "alerts",
		"url":    "https://example.com/hook",
		"events": []any{string(tables.WebhookEventAsyncJobCompleted)},
	})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "POST", path: "/api/webhooks", body: map[string]any{
		"name": "alerts", "url": "https://example.com/hook", "events": []any{"async_job.completed"},
	}}, admin.only(t))
	out := result.(map[string]any)
	require.Equal(t, "wh-1", out["id"])
	require.Equal(t, "whsec_test", out["secret"])
	require.Contains(t, out["note"], "only time the signing secret is returned")

	_, err = runTool(t, "create_webhook", &Deps{Admin: admin}, map[string]any{"name": "alerts", "url": "https://example.com/hook"})
	require.ErrorContains(t, err, "events")
	require.Len(t, admin.calls, 1)
}

// The webhook routes refuse (403) a private-network endpoint registered or
// re-pointed without a genuine admin session, which the in-process dispatcher
// never has. allow_private_network is no longer offered, and re-pointing an
// endpoint that already allows private delivery is refused before dispatch.
func TestWebhookToolsRefusePrivateNetworkChanges(t *testing.T) {
	fake := &fakeGovernanceReader{webhooks: map[string]*tables.TableWebhookEndpoint{
		"wh-private": {ID: "wh-private", Name: "internal", URL: "http://10.0.0.5/hook", AllowPrivateNetwork: true,
			Events: []tables.WebhookEvent{tables.WebhookEventAsyncJobCompleted}},
	}}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"id":"wh-private","name":"renamed","url":"http://10.0.0.5/hook","allow_private_network":true}`
	}}
	deps := &Deps{Governance: fake, Admin: admin}

	for _, name := range []string{"create_webhook", "update_webhook"} {
		tool, _ := toolByName(buildTools(), name)
		require.NotContains(t, tool.schemaJSON, "allow_private_network")
		require.Contains(t, tool.description, "private")
	}
	_, err := runTool(t, "create_webhook", deps, map[string]any{"name": "x", "url": "http://10.0.0.6/hook", "events": []any{"async_job.completed"}, "allow_private_network": true})
	require.ErrorContains(t, err, "allow_private_network")
	_, err = runTool(t, "update_webhook", deps, map[string]any{"webhook_id": "wh-private", "allow_private_network": true})
	require.ErrorContains(t, err, "allow_private_network")
	_, err = runTool(t, "update_webhook", deps, map[string]any{"webhook_id": "wh-private", "url": "http://10.0.0.7/hook"})
	require.ErrorContains(t, err, "private-network")
	require.Empty(t, admin.calls)

	// Turning private delivery off is open to anyone, so it is sent.
	_, err = runTool(t, "update_webhook", deps, map[string]any{"webhook_id": "wh-private", "url": "https://public.example.com/hook", "allow_private_network": false})
	require.NoError(t, err)
	require.Equal(t, false, admin.only(t).body["allow_private_network"])
	admin.calls = nil

	// Keeping the stored private URL is allowed by the route, so it is sent.
	_, err = runTool(t, "update_webhook", deps, map[string]any{"webhook_id": "wh-private", "name": "renamed"})
	require.NoError(t, err)
	require.Equal(t, true, admin.only(t).body["allow_private_network"])
}

// The webhook route replaces the whole endpoint and clears what is left out,
// so update_webhook sends the stored endpoint with the patch applied: delivery
// tuning and custom headers, which the tool does not expose, must survive an
// edit to something else, and no header value may leave the process.
func TestUpdateWebhookSendsTheWholeEndpoint(t *testing.T) {
	fake := &fakeGovernanceReader{webhooks: map[string]*tables.TableWebhookEndpoint{
		"wh-1": {
			ID: "wh-1", Name: "alerts", URL: "https://example.com/hook",
			Events:     []tables.WebhookEvent{tables.WebhookEventAsyncJobCompleted},
			Headers:    map[string]schemas.SecretVar{"X-Token": *schemas.NewSecretVar("header-secret-value")},
			MaxRetries: 7, AttemptTimeoutSeconds: 20, IncludeResponse: true,
		},
	}}
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"id":"wh-1","name":"alerts","url":"https://example.com/hook","events":["async_job.completed"],"disabled":true,"include_response":true}`
	}}
	result, err := runTool(t, "update_webhook", &Deps{Governance: fake, Admin: admin}, map[string]any{
		"webhook_id": "wh-1", "disabled": true, "include_response": nil,
	})
	require.NoError(t, err)
	call := admin.only(t)
	require.Equal(t, "PUT", call.method)
	require.Equal(t, "/api/webhooks/wh-1", call.path)
	require.Equal(t, "alerts", call.body["name"])
	require.Equal(t, true, call.body["disabled"])
	require.Equal(t, true, call.body["include_response"], "a null argument leaves the stored value in place")
	require.Equal(t, float64(7), call.body["max_retries"])
	require.Equal(t, float64(20), call.body["attempt_timeout_seconds"])
	require.Contains(t, call.body, "headers")
	encoded, err := json.Marshal(call.body)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "header-secret-value")
	require.NotContains(t, call.body, "webhook_id")
	require.Equal(t, true, result.(map[string]any)["disabled"])

	_, err = runTool(t, "update_webhook", &Deps{Governance: fake, Admin: admin}, map[string]any{"webhook_id": "wh-missing", "disabled": true})
	require.ErrorContains(t, err, `no webhook with id "wh-missing"`)
	require.Len(t, admin.calls, 1)
}

// update_feature_flag is the admin route's request and answer: the flag id in
// the path, enabled in the body, and the route's refusals - a locked or
// unregistered flag - returned in its own words.
func TestUpdateFeatureFlagCallsTheAdminRoute(t *testing.T) {
	admin := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 200, `{"id":"warp","display_name":"Warp","enabled":true,"default":false,"source":"db","registered":true,"updated_at":7}`
	}}
	result, err := runTool(t, "update_feature_flag", &Deps{Admin: admin}, map[string]any{"id": "warp", "enabled": true})
	require.NoError(t, err)
	require.Equal(t, adminCall{method: "PUT", path: "/api/feature-flags/warp", body: map[string]any{"enabled": true}}, admin.only(t))
	out := result.(map[string]any)
	require.Equal(t, "warp", out["id"])
	require.Equal(t, true, out["enabled"])
	require.Equal(t, "db", out["source"])

	locked := &fakeAdmin{respond: func(adminCall) (int, string) {
		return 409, `{"error":{"message":"Feature flag is locked by config.json / Helm"}}`
	}}
	_, err = runTool(t, "update_feature_flag", &Deps{Admin: locked}, map[string]any{"id": "warp", "enabled": false})
	require.EqualError(t, err, "Feature flag is locked by config.json / Helm")
}

// A write tool has nothing of its own to write with: without a dispatcher it
// reports itself unavailable, and the config store it can read is left alone.
func TestUpdateFeatureFlagNeedsTheDispatcher(t *testing.T) {
	fake := &fakeGovernanceReader{}
	_, err := runTool(t, "update_feature_flag", &Deps{Governance: fake}, map[string]any{"id": "not-a-flag", "enabled": true})
	require.ErrorIs(t, err, errAdminUnavailable)
	require.Empty(t, fake.flags)
}

// describe_plugin redacts secrets, so a caller that edits the described config
// and sends it back must not overwrite the stored secrets with the placeholder.
func TestUpdatePluginKeepsSecretsBehindRedactionPlaceholder(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
		"otel": {Name: "otel", Config: map[string]any{
			"api_key":      "sk-stored",
			"headers":      map[string]any{"Authorization": "Bearer stored"},
			"service_name": "old",
		}},
	}}
	deps := &Deps{Governance: fake, Admin: pluginAdminRoute(fake)}
	_, err := runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "config": map[string]any{
		"api_key":      schemas.RedactedAttrValue,
		"headers":      map[string]any{"Authorization": schemas.RedactedAttrValue},
		"service_name": "new",
	}})
	require.NoError(t, err)
	config := fake.plugins["otel"].Config.(map[string]any)
	require.Equal(t, "sk-stored", config["api_key"])
	require.Equal(t, "Bearer stored", config["headers"].(map[string]any)["Authorization"])
	require.Equal(t, "new", config["service_name"])

	_, err = runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "config": map[string]any{
		"password": schemas.RedactedAttrValue,
	}})
	require.ErrorContains(t, err, "config.password is the redaction placeholder")
}

// Index-based restoration is only safe when the array lines up with the
// stored one; a shortened array would shift secrets onto the wrong entries.
func TestUpdatePluginRejectsPlaceholderInResizedArray(t *testing.T) {
	stored := func() *fakeGovernanceReader {
		return &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{
			"otel": {Name: "otel", Config: map[string]any{"backends": []any{
				map[string]any{"name": "a", "token": "tok-a"},
				map[string]any{"name": "b", "token": "tok-b"},
			}}},
		}}
	}
	fake := stored()
	_, err := runTool(t, "update_plugin", &Deps{Governance: fake, Admin: pluginAdminRoute(fake)}, map[string]any{"name": "otel", "config": map[string]any{
		"backends": []any{map[string]any{"name": "b", "token": schemas.RedactedAttrValue}},
	}})
	require.ErrorContains(t, err, "config.backends has 1 items but the stored value has 2")
	require.Equal(t, "tok-a", fake.plugins["otel"].Config.(map[string]any)["backends"].([]any)[0].(map[string]any)["token"])

	// A resized array with real values only is an ordinary replacement.
	fake = stored()
	_, err = runTool(t, "update_plugin", &Deps{Governance: fake, Admin: pluginAdminRoute(fake)}, map[string]any{"name": "otel", "config": map[string]any{
		"backends": []any{map[string]any{"name": "c", "token": "tok-c"}},
	}})
	require.NoError(t, err)

	// Same length still restores by index.
	fake = stored()
	_, err = runTool(t, "update_plugin", &Deps{Governance: fake, Admin: pluginAdminRoute(fake)}, map[string]any{"name": "otel", "config": map[string]any{
		"backends": []any{
			map[string]any{"name": "a", "token": schemas.RedactedAttrValue},
			map[string]any{"name": "b2", "token": schemas.RedactedAttrValue},
		},
	}})
	require.NoError(t, err)
	backends := fake.plugins["otel"].Config.(map[string]any)["backends"].([]any)
	require.Equal(t, "tok-b", backends[1].(map[string]any)["token"])
}

// A plugin is registered to be used, so create sends it enabled unless the
// caller says otherwise; the route would read a missing flag as off.
func TestPluginToolsSendEnabledFlag(t *testing.T) {
	fake := &fakeGovernanceReader{plugins: map[string]*tables.TablePlugin{"otel": {Name: "otel", Enabled: true}}}
	admin := pluginAdminRoute(fake)
	deps := &Deps{Governance: fake, Admin: admin}

	_, err := runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "enabled": false})
	require.NoError(t, err)
	_, err = runTool(t, "update_plugin", deps, map[string]any{"name": "otel", "enabled": true})
	require.NoError(t, err)
	_, err = runTool(t, "create_plugin", deps, map[string]any{"name": "cache", "enabled": false})
	require.NoError(t, err)
	result, err := runTool(t, "create_plugin", deps, map[string]any{"name": "jsonparser"})
	require.NoError(t, err)

	sent := make([]any, 0, len(admin.calls))
	for _, call := range admin.calls {
		sent = append(sent, call.method+" "+call.path+" "+fmt.Sprint(call.body["enabled"]))
	}
	require.Equal(t, []any{"PUT /api/plugins/otel false", "PUT /api/plugins/otel true", "POST /api/plugins false", "POST /api/plugins true"}, sent)
	require.Equal(t, "jsonparser", result.(map[string]any)["name"])
	require.Equal(t, true, result.(map[string]any)["enabled"])
}

func TestListMCPAuthSessionsOmitsTokens(t *testing.T) {
	fake := &fakeGovernanceReader{
		oauthTokens: []tables.TableMCPOauthToken{{
			SessionID:    "sess-replayable",
			ID:           "tok-1",
			AuthMode:     "user",
			MCPClientID:  "client-1",
			Status:       "active",
			AccessToken:  "secret-access",
			RefreshToken: "secret-refresh",
		}},
	}
	deps := &Deps{Governance: fake}
	result, err := runTool(t, "list_mcp_auth_sessions", deps, map[string]any{})
	require.NoError(t, err)
	serialized := boundToolResult(result)
	require.NotContains(t, serialized, "secret-access")
	require.NotContains(t, serialized, "secret-refresh")
	// The session id selects the credential, so it is a secret too.
	require.NotContains(t, serialized, "sess-replayable")
	require.Equal(t, true, result.(map[string]any)["sessions"].([]map[string]any)[0]["session_bound"])
	rows := result.(map[string]any)["sessions"].([]map[string]any)
	require.Equal(t, "oauth", rows[0]["kind"])
	require.Equal(t, true, rows[0]["has_refresh"])
}

// Both tables were loaded whole and the tokens put first, so on a deployment
// with more OAuth tokens than the limit the header credentials never showed:
// the answer read as "nobody has header credentials". Rows are now merged
// newest first, each kind's total is reported, offset pages through them, and
// kind reads one table only.
func TestListMCPAuthSessionsKeepsBothKinds(t *testing.T) {
	base := time.Now()
	fake := &fakeGovernanceReader{}
	for i := 0; i < 15; i++ {
		fake.oauthTokens = append(fake.oauthTokens, tables.TableMCPOauthToken{ID: fmt.Sprintf("tok-%02d", i), CreatedAt: base.Add(-time.Duration(2*i+1) * time.Minute)})
	}
	for i := 0; i < 5; i++ {
		fake.headerCreds = append(fake.headerCreds, tables.TableMCPPerUserHeaderCredential{ID: fmt.Sprintf("hdr-%02d", i), CreatedAt: base.Add(-time.Duration(2*i) * time.Minute)})
	}
	deps := &Deps{Governance: fake}

	result, err := runTool(t, "list_mcp_auth_sessions", deps, map[string]any{"limit": 10.0})
	require.NoError(t, err)
	out := result.(map[string]any)
	kinds := map[string]int{}
	for _, row := range out["sessions"].([]map[string]any) {
		kinds[row["kind"].(string)]++
	}
	require.Equal(t, map[string]int{"oauth": 5, "headers": 5}, kinds, "the newest ten, whichever table they are in")
	require.Equal(t, 15, out["oauth_total"])
	require.Equal(t, 5, out["headers_total"])
	require.Equal(t, 20, out["total"])
	require.Equal(t, true, out["has_more"])

	result, err = runTool(t, "list_mcp_auth_sessions", deps, map[string]any{"limit": 10.0, "offset": 10.0})
	require.NoError(t, err)
	require.Len(t, result.(map[string]any)["sessions"], 10)
	require.Equal(t, false, result.(map[string]any)["has_more"])

	fake.sawSessionParams = nil
	result, err = runTool(t, "list_mcp_auth_sessions", deps, map[string]any{"kind": "headers"})
	require.NoError(t, err)
	require.Equal(t, []string{"headers"}, fake.sawSessionParams, "kind reads one table")
	require.Len(t, result.(map[string]any)["sessions"], 5)
}

// The catalog is keyed by the providers it knows: a custom provider ("acme",
// built on openai) and a key alias ("smart" for gpt-4o) are names the model
// sees in traffic, and describe_model answered "no catalog entry" for both.
// It now looks a custom provider up by its base type and an alias by the model
// it stands for, and says so in the result.
func TestDescribeModelResolvesCustomProvidersAndAliases(t *testing.T) {
	contextLength := 128000
	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest([]tables.TableModelPricing{{Model: "gpt-4o", Provider: "openai", Mode: "chat", ContextLength: &contextLength}})
	catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)
	catalog.SetKeyConfigForProvider("acme", []schemas.Key{{ID: "k-1", Aliases: schemas.KeyAliases{"smart": {ModelID: "gpt-4o"}}}})
	fake := &fakeGovernanceReader{providerCfgs: map[schemas.ModelProvider]configstore.ProviderConfig{
		"acme": {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}},
	}}
	deps := &Deps{Governance: fake, ModelCatalog: catalog}

	out := directMap(t, mustRunTool(t, "describe_model", deps, map[string]any{"provider": "acme", "model": "gpt-4o"}))
	require.Equal(t, "acme", out["provider"])
	require.Equal(t, "openai", out["base_provider"])
	require.Equal(t, 128000, out["context_length"])

	out = directMap(t, mustRunTool(t, "describe_model", deps, map[string]any{"provider": "acme", "model": "smart"}))
	require.Equal(t, "smart", out["name"])
	require.Equal(t, "gpt-4o", out["alias_of"])
	require.Equal(t, 128000, out["context_length"])

	out = directMap(t, mustRunTool(t, "describe_model", deps, map[string]any{"provider": "openai", "model": "gpt-4o"}))
	require.NotContains(t, out, "base_provider")
	require.NotContains(t, out, "alias_of")

	_, err := runTool(t, "describe_model", deps, map[string]any{"provider": "acme", "model": "ghost"})
	require.ErrorContains(t, err, "no catalog entry for acme/ghost")
}

func TestGetWarpConfigUnavailableWithoutStore(t *testing.T) {
	_, err := runTool(t, "get_warp_config", &Deps{}, map[string]any{})
	require.ErrorContains(t, err, "not available")
}

// warpAdminRoute stands in for PUT /api/warp/config: it records the body and
// saves it to store the way the route would, or refuses with refusal.
type warpAdminRoute struct {
	*fakeAdmin
	refusal string
}

func newWarpAdminRoute(store *fakeWarpStore) *warpAdminRoute {
	route := &warpAdminRoute{fakeAdmin: &fakeAdmin{}}
	route.respond = func(call adminCall) (int, string) {
		if route.refusal != "" {
			return 400, `{"error":{"message":"` + route.refusal + `"}}`
		}
		row := &tables.TableWarpConfig{}
		if store.row != nil {
			copied := *store.row
			row = &copied
		}
		row.Enabled, _ = call.body["enabled"].(bool)
		row.Provider, _ = call.body["provider"].(string)
		row.Model, _ = call.body["model"].(string)
		if dimension, ok := call.body["embedding_dimension"].(float64); ok {
			row.EmbeddingDimension = int(dimension)
		}
		if namespace, ok := call.body["log_vector_store_namespace"].(string); ok {
			row.LogVectorStoreNamespace = namespace
		}
		store.row = row
		return 200, `{}`
	}
	return route
}

// update_warp_config saves through the dashboard's route, sending the stored
// settings with the patch applied and naming only the embedding fields the
// caller set, so the rest keep their stored values instead of resetting.
func TestUpdateWarpConfigPersists(t *testing.T) {
	suffix := "be brief"
	store := &fakeWarpStore{row: &tables.TableWarpConfig{
		Provider: "anthropic", Model: "old", HistoryRetentionDays: 30, SystemPromptSuffix: &suffix,
		EmbeddingModel: "text-embedding-3-small",
	}}
	saver := newWarpAdminRoute(store)
	deps := &Deps{Warp: store, Admin: saver}
	result, err := runTool(t, "update_warp_config", deps, map[string]any{
		"enabled":  true,
		"provider": "openai",
		"model":    "gpt-4o",
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, true, out["enabled"])
	require.Equal(t, "openai", store.row.Provider)
	body := saver.calls[0].body
	require.Equal(t, "PUT", saver.calls[0].method)
	require.Equal(t, "/api/warp/config", saver.calls[0].path)
	require.Equal(t, float64(30), body["history_retention_days"], "settings the tool does not expose are carried, not wiped")
	require.Equal(t, "be brief", body["system_prompt_suffix"])
	require.NotContains(t, body, "embedding_model", "unset embedding fields are left to the stored row")

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"embedding_model": "text-embedding-3-large"})
	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-large", saver.calls[1].body["embedding_model"])

	saver.refusal = "invalid warp config: base_url must be an absolute http or https URL"
	_, err = runTool(t, "update_warp_config", deps, map[string]any{"base_url": "ftp://x"})
	require.ErrorContains(t, err, "base_url must be")

	_, err = runTool(t, "update_warp_config", &Deps{Warp: store}, map[string]any{"enabled": false})
	require.ErrorIs(t, err, errAdminUnavailable)
}

// Changing the embedding model can also change the vector dimension, and Warp
// then requires a new namespace; both are settable here, validated and sent
// only when named, and reported back.
func TestUpdateWarpConfigEmbeddingSpace(t *testing.T) {
	newDeps := func() (*Deps, *fakeWarpStore, *warpAdminRoute) {
		store := &fakeWarpStore{row: &tables.TableWarpConfig{
			EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
			EmbeddingDimension: 1536, LogVectorStoreNamespace: "warp_logs",
		}}
		saver := newWarpAdminRoute(store)
		return &Deps{Warp: store, Admin: saver}, store, saver
	}

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"embedding_dimension": float64(0)}, "embedding_dimension must be at least 1"},
		{map[string]any{"embedding_dimension": "3072"}, "embedding_dimension must be"},
		{map[string]any{"log_vector_store_namespace": "  "}, "log_vector_store_namespace must not be empty"},
	} {
		deps, _, saver := newDeps()
		_, err := runTool(t, "update_warp_config", deps, tc.args)
		require.ErrorContains(t, err, tc.want)
		require.Empty(t, saver.calls, "a rejected value is never sent")
	}

	deps, store, saver := newDeps()
	result, err := runTool(t, "update_warp_config", deps, map[string]any{
		"embedding_model":            "text-embedding-3-large",
		"embedding_dimension":        float64(3072),
		"log_vector_store_namespace": "warp_logs_large",
	})
	require.NoError(t, err)
	require.Equal(t, float64(3072), saver.calls[0].body["embedding_dimension"])
	require.Equal(t, "warp_logs_large", saver.calls[0].body["log_vector_store_namespace"])
	out := result.(map[string]any)
	require.Equal(t, 3072, out["embedding_dimension"])
	require.Equal(t, "warp_logs_large", out["log_vector_store_namespace"])
	require.Equal(t, "warp_logs_large", store.row.LogVectorStoreNamespace)

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"enabled": false})
	require.NoError(t, err)
	require.NotContains(t, saver.calls[1].body, "embedding_dimension", "unnamed, the stored dimension is left to the stored row")
	require.NotContains(t, saver.calls[1].body, "log_vector_store_namespace")
}

// The route replaces additional_models whole on every write - omitted means
// none - so an update that did not mention them wiped the models the panel
// could switch to. The stored list is forwarded unless the caller replaces it,
// get_warp_config reports it, and an explicit [] is how to clear it.
func TestUpdateWarpConfigKeepsAdditionalModels(t *testing.T) {
	stored := `[{"provider":"anthropic","model":"claude-sonnet","api_key_id":"key-1"},{"provider":"openai","model":"gpt-4o"}]`
	store := &fakeWarpStore{row: &tables.TableWarpConfig{Provider: "openai", Model: "gpt-4o-mini", AdditionalModels: &stored}}
	saver := newWarpAdminRoute(store)
	deps := &Deps{Warp: store, Admin: saver}

	got, err := runTool(t, "get_warp_config", deps, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, []map[string]any{
		{"provider": "anthropic", "model": "claude-sonnet", "api_key_id": "key-1"},
		{"provider": "openai", "model": "gpt-4o"},
	}, got.(map[string]any)["additional_models"])

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"enabled": true})
	require.NoError(t, err)
	require.Equal(t, []any{
		map[string]any{"provider": "anthropic", "model": "claude-sonnet", "api_key_id": "key-1"},
		map[string]any{"provider": "openai", "model": "gpt-4o"},
	}, saver.calls[0].body["additional_models"], "an update that does not name them keeps them")

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"additional_models": []any{
		map[string]any{"provider": "gemini", "model": "gemini-2.5-pro"},
	}})
	require.NoError(t, err)
	require.Equal(t, []any{map[string]any{"provider": "gemini", "model": "gemini-2.5-pro"}}, saver.calls[1].body["additional_models"])

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"additional_models": []any{}})
	require.NoError(t, err)
	require.Equal(t, []any{}, saver.calls[2].body["additional_models"], "an explicit empty list clears them")

	_, err = runTool(t, "update_warp_config", deps, map[string]any{"additional_models": []any{map[string]any{"model": "x"}}})
	require.ErrorContains(t, err, "provider")
	require.Len(t, saver.calls, 3)
}

// A null semantic_search_threshold is "not set", like an absent one.
func TestUpdateWarpConfigIgnoresNullThreshold(t *testing.T) {
	store := &fakeWarpStore{row: &tables.TableWarpConfig{SemanticSearchThreshold: 0.8}}
	saver := newWarpAdminRoute(store)
	_, err := runTool(t, "update_warp_config", &Deps{Warp: store, Admin: saver}, map[string]any{
		"semantic_search_threshold": nil, "enabled": false,
	})
	require.NoError(t, err)
	require.NotContains(t, saver.calls[0].body, "semantic_search_threshold")

	_, err = runTool(t, "update_warp_config", &Deps{Warp: store, Admin: saver}, map[string]any{"semantic_search_threshold": 0.5})
	require.NoError(t, err)
	require.Equal(t, 0.5, saver.calls[1].body["semantic_search_threshold"])
}

func TestListWebhooksNeverReturnsSecret(t *testing.T) {
	secret := schemas.NewSecretVar("whsec_must_not_leak")
	fake := &fakeGovernanceReader{webhooks: map[string]*tables.TableWebhookEndpoint{
		"wh-1": {ID: "wh-1", Name: "alerts", URL: "https://example.com/hook", Secret: secret, Events: []tables.WebhookEvent{tables.WebhookEventAsyncJobCompleted}},
	}}
	result, err := runTool(t, "list_webhooks", &Deps{Governance: fake}, map[string]any{})
	require.NoError(t, err)
	require.NotContains(t, boundToolResult(result), "whsec_must_not_leak")
}

type fakeWarpStore struct {
	row *tables.TableWarpConfig
}

func (f *fakeWarpStore) GetWarpConfig(context.Context) (*tables.TableWarpConfig, error) {
	return f.row, nil
}

func (f *fakeWarpStore) UpsertWarpConfig(_ context.Context, config *tables.TableWarpConfig) error {
	copied := *config
	f.row = &copied
	return nil
}
