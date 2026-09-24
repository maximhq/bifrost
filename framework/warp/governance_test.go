package warp

import (
	"context"
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
