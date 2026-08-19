package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyRealtimeTurnContextValuesCarriesGovernanceAttribution(t *testing.T) {
	preCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	preCtx.SetValue(schemas.BifrostContextKeyGovernanceBudgetIDs, []string{"budget-1", "budget-2"})
	preCtx.SetValue(schemas.BifrostContextKeyGovernanceRateLimitIDs, []string{"rate-limit-1"})
	preCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, false)

	postCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	applyRealtimeTurnContextValues(postCtx, preCtx.GetUserValues())

	budgetIDs, ok := postCtx.Value(schemas.BifrostContextKeyGovernanceBudgetIDs).([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"budget-1", "budget-2"}, budgetIDs)
	rateLimitIDs, ok := postCtx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"rate-limit-1"}, rateLimitIDs)
	assert.Nil(t, postCtx.Value(schemas.BifrostContextKeyStreamEndIndicator), "the finalizer owns the post-context stream marker")
}

func TestApplyRealtimeTurnContextValuesCarriesEmptyGovernanceAttribution(t *testing.T) {
	// Typed empty slices must survive the pre/post context handoff so a turn with
	// no applicable governance cannot fall through to values on its base context.
	baseCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	baseCtx.SetValue(schemas.BifrostContextKeyGovernanceBudgetIDs, []string{"stale-budget"})
	preCtx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)
	preCtx.SetValue(schemas.BifrostContextKeyGovernanceBudgetIDs, []string{})
	preCtx.SetValue(schemas.BifrostContextKeyGovernanceRateLimitIDs, []string{})

	postCtx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)
	applyRealtimeTurnContextValues(postCtx, preCtx.GetUserValues())

	budgetIDs, ok := postCtx.Value(schemas.BifrostContextKeyGovernanceBudgetIDs).([]string)
	require.True(t, ok)
	assert.Empty(t, budgetIDs)
	rateLimitIDs, ok := postCtx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
	require.True(t, ok)
	assert.Empty(t, rateLimitIDs)
}
