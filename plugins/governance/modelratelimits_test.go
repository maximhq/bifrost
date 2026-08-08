package governance

import (
	"context"
	"testing"
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestBumpRateLimitUsageOnlyUpdatesConfiguredMetrics(t *testing.T) {
	store := newStandaloneStore(t)
	maxLimit := int64(1000)
	duration := "1h"

	tokenRule := &configstoreTables.TableRateLimit{
		ID:                 "model-tokens",
		Metric:             configstoreTables.ModelRateLimitMetricTokens,
		TokenMaxLimit:      &maxLimit,
		TokenResetDuration: &duration,
		TokenLastReset:     time.Now(),
	}
	requestRule := &configstoreTables.TableRateLimit{
		ID:                   "model-requests",
		Metric:               configstoreTables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &maxLimit,
		RequestResetDuration: &duration,
		RequestLastReset:     time.Now(),
	}
	store.rateLimits.Store(tokenRule.ID, tokenRule)
	store.rateLimits.Store(requestRule.ID, requestRule)

	ctx := context.Background()
	require.NoError(t, store.BumpRateLimitUsage(ctx, tokenRule.ID, 37, true, true))
	require.NoError(t, store.BumpRateLimitUsage(ctx, requestRule.ID, 37, true, true))

	gotTokens := store.LoadRateLimit(ctx, tokenRule.ID)
	gotRequests := store.LoadRateLimit(ctx, requestRule.ID)
	require.NotNil(t, gotTokens)
	require.NotNil(t, gotRequests)
	require.Equal(t, int64(37), gotTokens.TokenCurrentUsage)
	require.Equal(t, int64(0), gotTokens.RequestCurrentUsage)
	require.Equal(t, int64(0), gotRequests.TokenCurrentUsage)
	require.Equal(t, int64(1), gotRequests.RequestCurrentUsage)
}

func TestCheckRateLimitEvaluatesAllModelWindowsIndependently(t *testing.T) {
	store := newStandaloneStore(t)
	now := time.Now()
	makeRequestRule := func(id string, limit int64) *configstoreTables.TableRateLimit {
		duration := "1m"
		return &configstoreTables.TableRateLimit{
			ID:                   id,
			Metric:               configstoreTables.ModelRateLimitMetricRequests,
			RequestMaxLimit:      &limit,
			RequestResetDuration: &duration,
			RequestLastReset:     now,
		}
	}
	makeTokenRule := func(id string, limit int64) *configstoreTables.TableRateLimit {
		duration := "1d"
		return &configstoreTables.TableRateLimit{
			ID:                 id,
			Metric:             configstoreTables.ModelRateLimitMetricTokens,
			TokenMaxLimit:      &limit,
			TokenResetDuration: &duration,
			TokenLastReset:     now,
		}
	}

	rpm := makeRequestRule("rpm", 15)
	rpd := makeRequestRule("rpd", 1500)
	tpm := makeTokenRule("tpm", 1000)
	tpd := makeTokenRule("tpd", 100000)
	entityWise := EntityWiseRateLimits{"model-config": {rpm, rpd, tpm, tpd}}

	decision, err := store.CheckRateLimit(context.Background(), entityWise, nil, nil)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision)

	rpm.RequestCurrentUsage = 15
	decision, err = store.CheckRateLimit(context.Background(), entityWise, nil, nil)
	require.Error(t, err)
	require.Equal(t, DecisionRequestLimited, decision)

	// Resetting the RPM snapshot does not affect the independent daily and
	// token windows; exhausting TPD is still enforced on its own rule.
	rpm.RequestCurrentUsage = 0
	tpd.TokenCurrentUsage = 100000
	decision, err = store.CheckRateLimit(context.Background(), entityWise, nil, nil)
	require.Error(t, err)
	require.Equal(t, DecisionTokenLimited, decision)
}

func TestUpdateModelConfigInMemoryPreservesOrResetsRuleUsageByWindow(t *testing.T) {
	store := newStandaloneStore(t)
	ctx := context.Background()
	modelID := "model-config"
	minute := "1m"
	day := "1d"
	maxLimit := int64(15)
	rule := configstoreTables.TableRateLimit{
		ID:                   "model-rpm",
		ModelConfigID:        &modelID,
		Metric:               configstoreTables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &maxLimit,
		RequestResetDuration: &minute,
		RequestCurrentUsage:  7,
		RequestLastReset:     time.Now().Add(-time.Minute),
	}
	model := &configstoreTables.TableModelConfig{ID: modelID, ModelName: "gpt-test", RateLimits: []configstoreTables.TableRateLimit{rule}}
	store.UpdateModelConfigInMemory(ctx, model)

	updatedMax := int64(30)
	updated := *model
	updated.RateLimits = []configstoreTables.TableRateLimit{{
		ID:                   rule.ID,
		ModelConfigID:        &modelID,
		Metric:               configstoreTables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &updatedMax,
		RequestResetDuration: &minute,
	}}
	store.UpdateModelConfigInMemory(ctx, &updated)
	preserved := store.LoadRateLimit(ctx, rule.ID)
	require.NotNil(t, preserved)
	require.Equal(t, int64(7), preserved.RequestCurrentUsage)

	changedWindow := *model
	changedWindow.RateLimits = []configstoreTables.TableRateLimit{{
		ID:                   rule.ID,
		ModelConfigID:        &modelID,
		Metric:               configstoreTables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &updatedMax,
		RequestResetDuration: &day,
	}}
	store.UpdateModelConfigInMemory(ctx, &changedWindow)
	reset := store.LoadRateLimit(ctx, rule.ID)
	require.NotNil(t, reset)
	require.Equal(t, int64(0), reset.RequestCurrentUsage)
}

func TestDeleteModelConfigsForScopeInMemoryReleasesSharedRuleAfterLastOwner(t *testing.T) {
	store := newStandaloneStore(t)
	ctx := context.Background()
	scopeID := "virtual-key"
	modelIDOne := "model-one"
	modelIDTwo := "model-two"
	maxLimit := int64(15)
	duration := "1m"

	makeModel := func(id string) *configstoreTables.TableModelConfig {
		return &configstoreTables.TableModelConfig{
			ID:      id,
			Scope:   configstoreTables.ModelConfigScopeVirtualKey,
			ScopeID: &scopeID,
			RateLimits: []configstoreTables.TableRateLimit{{
				ID:                   "shared-model-rule",
				ModelConfigID:        &id,
				Metric:               configstoreTables.ModelRateLimitMetricRequests,
				RequestMaxLimit:      &maxLimit,
				RequestResetDuration: &duration,
			}},
		}
	}
	store.modelConfigs.Store("model-one-key", makeModel(modelIDOne))
	store.modelConfigs.Store("model-two-key", makeModel(modelIDTwo))
	store.rateLimits.Store("shared-model-rule", &configstoreTables.TableRateLimit{ID: "shared-model-rule"})

	store.DeleteModelConfigsForScopeInMemory(ctx, configstoreTables.ModelConfigScopeVirtualKey, scopeID)

	_, exists := store.rateLimits.Load("shared-model-rule")
	require.False(t, exists, "shared rule should be released after the final model owner is removed")
}
