package lib

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// seedRuntimeOwnedRows creates the governance rows an access profile mints at
// runtime: a virtual key, a budget, a rate limit and a model config, none of
// which config.json can declare.
func seedRuntimeOwnedRows(t *testing.T, ctx context.Context, store configstore.ConfigStore, suffix string) {
	t.Helper()
	tokenMax := int64(500)
	tokenDur := "1h"
	require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{
		ID:       "vk-" + suffix,
		Name:     "vk-" + suffix,
		Value:    *schemas.NewSecretVar("bf-vk-" + suffix),
		IsActive: schemas.Ptr(true),
	}))
	require.NoError(t, store.CreateBudget(ctx, &tables.TableBudget{
		ID: "budget-" + suffix, MaxLimit: 25.0, ResetDuration: "1M",
	}))
	require.NoError(t, store.CreateRateLimit(ctx, &tables.TableRateLimit{
		ID: "rl-" + suffix, TokenMaxLimit: &tokenMax, TokenResetDuration: &tokenDur,
	}))
	require.NoError(t, store.CreateModelConfig(ctx, &tables.TableModelConfig{
		ID: "mc-" + suffix, ModelName: "gpt-" + suffix, Scope: "global",
	}))
}

// governanceRowIDs collapses a governance config into per-collection ID sets.
func governanceRowIDs(t *testing.T, ctx context.Context, store configstore.ConfigStore) map[string]map[string]bool {
	t.Helper()
	gov, err := store.GetGovernanceConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, gov)
	ids := map[string]map[string]bool{
		"virtual_keys":  {},
		"budgets":       {},
		"rate_limits":   {},
		"model_configs": {},
	}
	for _, vk := range gov.VirtualKeys {
		ids["virtual_keys"][vk.ID] = true
	}
	for _, b := range gov.Budgets {
		ids["budgets"][b.ID] = true
	}
	for _, rl := range gov.RateLimits {
		ids["rate_limits"][rl.ID] = true
	}
	for _, mc := range gov.ModelConfigs {
		ids["model_configs"][mc.ID] = true
	}
	return ids
}

// configDataWithAllGovernanceSections declares every prunable governance section
// so each prune loop actually runs.
func configDataWithAllGovernanceSections(tempDir string) *ConfigData {
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		VirtualKeys:  []tables.TableVirtualKey{},
		Budgets:      []tables.TableBudget{},
		RateLimits:   []tables.TableRateLimit{},
		ModelConfigs: []tables.TableModelConfig{},
	}
	return configData
}

// TestSQLite_SourceOfTruthConfigJSON_RuntimeOwnedRowsSurvivePrune covers the
// access-profile data loss: config.json is authoritative, so rows it does not
// declare are deleted, but an access profile's per-user virtual key, budgets,
// rate limits and model configs are minted at runtime and can never appear in the
// file. The registered resolver marks them as runtime-owned; unregistered drift
// alongside them must still be pruned.
func TestSQLite_SourceOfTruthConfigJSON_RuntimeOwnedRowsSurvivePrune(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	configData := configDataWithAllGovernanceSections(tempDir)
	createConfigFile(t, tempDir, configData)

	config1, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	seedRuntimeOwnedRows(t, ctx, config1.ConfigStore, "managed")
	seedRuntimeOwnedRows(t, ctx, config1.ConfigStore, "drift")
	config1.Close(ctx)

	t.Cleanup(func() { RegisterRuntimeOwnedGovernanceResolver(nil) })
	RegisterRuntimeOwnedGovernanceResolver(func(context.Context, configstore.ConfigStore) (*RuntimeOwnedGovernanceRows, error) {
		return &RuntimeOwnedGovernanceRows{
			VirtualKeyIDs:  map[string]bool{"vk-managed": true},
			BudgetIDs:      map[string]bool{"budget-managed": true},
			RateLimitIDs:   map[string]bool{"rl-managed": true},
			ModelConfigIDs: map[string]bool{"mc-managed": true},
		}, nil
	})

	configData.SourceOfTruth = SourceOfTruthConfigJSON
	createConfigFile(t, tempDir, configData)

	config2, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config2.Close(ctx)

	ids := governanceRowIDs(t, ctx, config2.ConfigStore)
	require.True(t, ids["virtual_keys"]["vk-managed"], "access-profile virtual key must survive the prune")
	require.True(t, ids["budgets"]["budget-managed"], "access-profile budget must survive the prune")
	require.True(t, ids["rate_limits"]["rl-managed"], "access-profile rate limit must survive the prune")
	require.True(t, ids["model_configs"]["mc-managed"], "access-profile model config must survive the prune")

	require.False(t, ids["virtual_keys"]["vk-drift"], "unprotected virtual key must still be pruned")
	require.False(t, ids["budgets"]["budget-drift"], "unprotected budget must still be pruned")
	require.False(t, ids["rate_limits"]["rl-drift"], "unprotected rate limit must still be pruned")
	require.False(t, ids["model_configs"]["mc-drift"], "unprotected model config must still be pruned")

	// The in-memory governance config must agree with what survived, otherwise the
	// file-only governance path would serve a virtual key set the DB contradicts.
	memVKs := map[string]bool{}
	for _, vk := range config2.GovernanceConfig.VirtualKeys {
		memVKs[vk.ID] = true
	}
	require.True(t, memVKs["vk-managed"], "surviving virtual key must stay in the in-memory governance config")
	require.False(t, memVKs["vk-drift"], "pruned virtual key must not stay in the in-memory governance config")
}

// TestSQLite_SourceOfTruthConfigJSON_NoResolverPrunesEverything pins the OSS
// default: with no resolver registered nothing is protected and the prune is
// exactly as before.
func TestSQLite_SourceOfTruthConfigJSON_NoResolverPrunesEverything(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	configData := configDataWithAllGovernanceSections(tempDir)
	createConfigFile(t, tempDir, configData)

	config1, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	seedRuntimeOwnedRows(t, ctx, config1.ConfigStore, "managed")
	config1.Close(ctx)

	configData.SourceOfTruth = SourceOfTruthConfigJSON
	createConfigFile(t, tempDir, configData)

	config2, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config2.Close(ctx)

	ids := governanceRowIDs(t, ctx, config2.ConfigStore)
	require.False(t, ids["virtual_keys"]["vk-managed"])
	require.False(t, ids["budgets"]["budget-managed"])
	require.False(t, ids["rate_limits"]["rl-managed"])
	require.False(t, ids["model_configs"]["mc-managed"])
}

// TestSQLite_SourceOfTruthConfigJSON_ResolverErrorDoesNotBlockLoad verifies a
// failing resolver degrades to protecting nothing rather than aborting startup.
func TestSQLite_SourceOfTruthConfigJSON_ResolverErrorDoesNotBlockLoad(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	configData := configDataWithAllGovernanceSections(tempDir)
	createConfigFile(t, tempDir, configData)

	config1, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	seedRuntimeOwnedRows(t, ctx, config1.ConfigStore, "managed")
	config1.Close(ctx)

	t.Cleanup(func() { RegisterRuntimeOwnedGovernanceResolver(nil) })
	RegisterRuntimeOwnedGovernanceResolver(func(context.Context, configstore.ConfigStore) (*RuntimeOwnedGovernanceRows, error) {
		return nil, errors.New("access profile tables unavailable")
	})

	configData.SourceOfTruth = SourceOfTruthConfigJSON
	createConfigFile(t, tempDir, configData)

	config2, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config2.Close(ctx)

	ids := governanceRowIDs(t, ctx, config2.ConfigStore)
	require.False(t, ids["virtual_keys"]["vk-managed"])
}
