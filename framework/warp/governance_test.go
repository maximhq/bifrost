package warp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// fakeGovernanceReader is describe_virtual_key's one dependency. lookup is
// keyed on id, mirroring GetVirtualKey's own "not found" contract: a missing
// key returns configstore.ErrNotFound, not a nil, nil pair.
type fakeGovernanceReader struct {
	byID       map[string]*tables.TableVirtualKey
	err        error
	sawContext context.Context
	sawID      string
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

func TestWarpDescribeVirtualKeyReportsUnavailableWithoutAGovernanceReader(t *testing.T) {
	_, err := runTool(t, "describe_virtual_key", &ToolDeps{}, map[string]any{"virtual_key_id": "vk-1"})
	require.ErrorContains(t, err, "not available")
}

func TestWarpDescribeVirtualKeyRequiresAnID(t *testing.T) {
	deps := &ToolDeps{governance: &fakeGovernanceReader{}}
	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "  "})
	require.ErrorContains(t, err, "virtual_key_id")
}

func TestWarpDescribeVirtualKeyReportsUnknownID(t *testing.T) {
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{}}
	deps := &ToolDeps{governance: fake}
	_, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-missing"})
	require.ErrorContains(t, err, "vk-missing")
	require.ErrorContains(t, err, "describe_filter_space")
}

// The caller's context is what carries queryscope's row-level filter into the
// store - GetVirtualKey narrows to rows the caller may see the same way every
// LogReader method does. Losing it here would return any key to anyone who
// asked, the same failure mode LogReader's own tools guard against.
func TestWarpDescribeVirtualKeyPassesCallerContextToStore(t *testing.T) {
	type scopeKey struct{}
	fake := &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{
		"vk-1": {ID: "vk-1", Name: "prod"},
	}}
	deps := &ToolDeps{governance: fake}
	tool, ok := toolByName(buildTools(), "describe_virtual_key")
	require.True(t, ok)

	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	_, err := tool.execute(ctx, deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)
	require.Equal(t, "caller-scope", fake.sawContext.Value(scopeKey{}))
	require.Equal(t, "vk-1", fake.sawID)
}

// The result must never carry the key's own secret value, its rotation
// history, or any provider credential beneath it - only the budget/limit/
// provider shape describeVirtualKey hand-picks. This is the regression test
// for that: a row deliberately carrying secret-shaped data in every field
// describeVirtualKey does not touch, asserting none of it survives.
func TestWarpDescribeVirtualKeyNeverLeaksSecretFields(t *testing.T) {
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
	deps := &ToolDeps{governance: fake}

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
	require.InDelta(t, 100.0, budgets[0]["max_limit"], 0.001)
	require.InDelta(t, 42.0, budgets[0]["current_usage"], 0.001)

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
func TestWarpDescribeVirtualKeyBudgetReportsEffectiveLimitUnderOverride(t *testing.T) {
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
	deps := &ToolDeps{governance: fake}

	result, err := runTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-1"})
	require.NoError(t, err)

	budgets := result.(map[string]any)["budgets"].([]map[string]any)
	require.InDelta(t, 150.0, budgets[0]["max_limit"], 0.001, "override amount must be folded into the reported cap")
	require.Equal(t, true, budgets[0]["override_active"])
}

func int64Ptr(v int64) *int64 { return &v }

// A key with nothing configured used to come back with no "budgets" or
// "rate_limit" field at all, and the model read the missing field as a failed
// lookup ("Warp couldn't retrieve a configured budget"). Both are always
// present now, empty or null, and the result says in words that nothing caps
// the key itself and what may still apply.
func TestWarpDescribeVirtualKeySaysWhenNothingCapsTheKey(t *testing.T) {
	bu := "bu-1"
	vk := &tables.TableVirtualKey{ID: "vk-bare", Name: "bare", BusinessUnitID: &bu}
	deps := &ToolDeps{governance: &fakeGovernanceReader{byID: map[string]*tables.TableVirtualKey{"vk-bare": vk}}}

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
func TestWarpDescribeVirtualKeyReportsWhatTheDecoratorOverlays(t *testing.T) {
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
		deps := &ToolDeps{governance: reader, vkDecorator: decorator}
		if userLimits {
			deps.userGovernance = &fakeUserGovernanceReader{}
		}
		out := directMap(t, mustRunTool(t, "describe_virtual_key", deps, map[string]any{"virtual_key_id": "vk-managed"}))
		require.Equal(t, true, out["access_profile_managed"])
		budgets := out["budgets"].([]map[string]any)
		require.Len(t, budgets, 1)
		require.InDelta(t, 450.0, budgets[0]["max_limit"], 0.001, "override folded in")
		require.InDelta(t, 380.28, budgets[0]["current_usage"], 0.001)
		require.InDelta(t, 69.72, budgets[0]["remaining"], 0.001)
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
	_, err := runTool(t, "describe_virtual_key", &ToolDeps{governance: reader, vkDecorator: failing}, map[string]any{"virtual_key_id": "vk-managed"})
	require.ErrorContains(t, err, "could not resolve what governs virtual key")
}

// remaining is what the question is actually about, and a model doing the
// subtraction itself got it wrong often enough. It never goes below zero: an
// overspent budget has nothing left, not a negative amount.
func TestWarpBudgetSummaryReportsRemaining(t *testing.T) {
	require.InDelta(t, 30.0, budgetSummary(tables.TableBudget{MaxLimit: 100, CurrentUsage: 70})["remaining"], 0.001)
	require.InDelta(t, 0.0, budgetSummary(tables.TableBudget{MaxLimit: 100, CurrentUsage: 130})["remaining"], 0.001)
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
func TestWarpDescribeUserLimits(t *testing.T) {
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
	deps := &ToolDeps{userGovernance: reader}

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
	require.InDelta(t, 69.72, budgets[0]["remaining"], 0.001)
	rateLimit := profile["rate_limit"].(map[string]any)
	require.Equal(t, int64(1000), rateLimit["request_max_limit"])
	require.Equal(t, "1h", rateLimit["request_reset_duration"])
	providers := profile["providers"].([]map[string]any)
	require.Len(t, providers, 1)
	require.Equal(t, "databricks", providers[0]["provider"])
	require.Equal(t, []string{"dbrx"}, providers[0]["allowed_models"])
	require.InDelta(t, 20.0, providers[0]["budgets"].([]map[string]any)[0]["remaining"], 0.001)
	models := providers[0]["model_budgets"].([]map[string]any)
	require.Equal(t, "dbrx", models[0]["model"])
	require.Contains(t, out["guidance"], "remaining is max_limit minus current_usage")

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

// Without a reader the tool is not in the set at all - offering it would tell
// the model a capability exists and cost a step to discover otherwise, the
// same reasoning semantic_search_logs follows.
func TestWarpDescribeUserLimitsOnlyOfferedWithAReader(t *testing.T) {
	_, without := toolByName(buildToolsFor(nil, false), UserLimitsToolName)
	require.False(t, without)
	_, with := toolByName(buildToolsFor(nil, true), UserLimitsToolName)
	require.True(t, with)

	agent := newTestAgent(&scriptedModel{}, &fakeLogReader{}, 3)
	_, before := toolByName(agent.tools, UserLimitsToolName)
	require.False(t, before)
	agent.SetGovernanceExtras(nil, &fakeUserGovernanceReader{})
	_, after := toolByName(agent.tools, UserLimitsToolName)
	require.True(t, after, "handing the agent a reader adds the tool")

	// The prompt names the tool exactly when the declarations carry it.
	require.NotContains(t, systemInstructions(&schemas.WarpConfig{}, true), UserLimitsToolName)
	withLimits := systemInstructionsFor(&schemas.WarpConfig{}, toolAvailability{userLimits: true})
	require.Contains(t, withLimits, UserLimitsToolName)
	require.Contains(t, withLimits, "managed by an access profile")
	require.Contains(t, withLimits, "not a log window")
}

// directMap is the tool's own return value, untouched by serialization, so
// the typed shapes describeVirtualKey builds can be asserted as built.
func directMap(t *testing.T, result any) map[string]any {
	t.Helper()
	out, ok := result.(map[string]any)
	require.True(t, ok, "result is %T", result)
	return out
}

func mustRunTool(t *testing.T, name string, deps *ToolDeps, args map[string]any) any {
	t.Helper()
	out, err := runTool(t, name, deps, args)
	require.NoError(t, err)
	return out
}

func strPtr(v string) *string { return &v }
