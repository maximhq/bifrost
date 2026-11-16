package livestore

import (
	"context"
	"path/filepath"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// governanceDB returns a SQLite config store holding a team with a budget and a rate limit, two
// keys under it, and a model config scoped to the first key. The first key holds a budget, a rate
// limit and a provider config with a budget of its own; the model config holds a budget and a
// rate limit.
func governanceDB(t *testing.T) configstore.ConfigStore {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	db := store.DB()
	create := func(rows ...any) {
		t.Helper()
		for _, row := range rows {
			require.NoError(t, db.Omit(clause.Associations).Create(row).Error)
		}
	}
	limit := func(requests int64) *int64 { return &requests }
	window := "1m"
	ref := func(s string) *string { return &s }
	create(
		&tables.TableRateLimit{ID: "rl-team", RequestMaxLimit: limit(1), RequestResetDuration: &window},
		&tables.TableRateLimit{ID: "rl-vk", RequestMaxLimit: limit(2), RequestResetDuration: &window},
		&tables.TableRateLimit{ID: "rl-mc", RequestMaxLimit: limit(3), RequestResetDuration: &window},
		&tables.TableTeam{ID: "team-1", Name: "Platform", RateLimitID: ref("rl-team")},
		&tables.TableVirtualKey{ID: "vk-1", Name: "one", Value: *schemas.NewSecretVar("sk-1"), TeamID: ref("team-1"), RateLimitID: ref("rl-vk")},
		&tables.TableVirtualKey{ID: "vk-2", Name: "two", Value: *schemas.NewSecretVar("sk-2"), TeamID: ref("team-1")},
		&tables.TableModelConfig{ID: "mc-1", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeVirtualKey, ScopeID: ref("vk-1"), RateLimitID: ref("rl-mc")},
	)
	pc := &tables.TableVirtualKeyProviderConfig{VirtualKeyID: "vk-1", Provider: "openai", AllowedModels: schemas.WhiteList{"*"}}
	create(pc)
	create(
		&tables.TableBudget{ID: "b-team", MaxLimit: 10, ResetDuration: "1d", TeamID: ref("team-1")},
		&tables.TableBudget{ID: "b-vk", MaxLimit: 20, ResetDuration: "1d", VirtualKeyID: ref("vk-1")},
		&tables.TableBudget{ID: "b-pc", MaxLimit: 30, ResetDuration: "1d", ProviderConfigID: &pc.ID},
		&tables.TableBudget{ID: "b-mc", MaxLimit: 40, ResetDuration: "1d", ModelConfigID: ref("mc-1")},
	)
	return store
}

// loadedGovernance returns a store loaded from db.
func loadedGovernance(t *testing.T, db *gorm.DB) *Store {
	t.Helper()
	s, err := New()
	require.NoError(t, err)
	t.Cleanup(s.Close)
	require.NoError(t, s.LoadAll(context.Background(), db))
	return s
}

// TestRefreshOfKeyRereadsItsLimits checks that refreshing only a key re-reads the limits it holds,
// its provider configs' included: a changed budget takes its new value, and a budget added to the
// key, which no ref named, is found.
func TestRefreshOfKeyRereadsItsLimits(t *testing.T) {
	ctx := context.Background()
	db := governanceDB(t).DB()
	s := loadedGovernance(t, db)
	cols := s.Collections

	b, ok := cols.Budgets.Get("b-vk")
	require.True(t, ok)
	require.Equal(t, 20.0, b.MaxLimit)
	b, ok = cols.Budgets.Get("b-pc")
	require.True(t, ok)
	require.Equal(t, "vk-1", b.KeyID)

	require.NoError(t, db.Model(&tables.TableBudget{}).Where("id = ?", "b-vk").UpdateColumn("max_limit", 500).Error)
	require.NoError(t, db.Model(&tables.TableBudget{}).Where("id = ?", "b-pc").UpdateColumn("max_limit", 600).Error)
	require.NoError(t, db.Model(&tables.TableRateLimit{}).Where("id = ?", "rl-vk").UpdateColumn("request_max_limit", 9).Error)
	vkID := "vk-1"
	require.NoError(t, db.Omit(clause.Associations).Create(&tables.TableBudget{ID: "b-vk-new", MaxLimit: 70, ResetDuration: "1w", VirtualKeyID: &vkID}).Error)

	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindVirtualKey, ID: "vk-1"}}))

	b, _ = cols.Budgets.Get("b-vk")
	require.Equal(t, 500.0, b.MaxLimit, "the key's budget is re-read with it")
	b, _ = cols.Budgets.Get("b-pc")
	require.Equal(t, 600.0, b.MaxLimit, "so is its provider config's")
	rl, ok := cols.RateLimits.Get("rl-vk")
	require.True(t, ok)
	require.Equal(t, int64(9), *rl.RequestMaxLimit, "and its rate limit")
	b, ok = cols.Budgets.Get("b-vk-new")
	require.True(t, ok, "a budget added to the key is found by the key's refresh")
	require.Equal(t, "vk-1", b.KeyID)
	k, _ := cols.VirtualKeys.Get("vk-1")
	require.ElementsMatch(t, []string{"b-vk", "b-vk-new"}, k.BudgetIDs)
	b, _ = cols.Budgets.Get("b-team")
	require.Equal(t, 10.0, b.MaxLimit, "the team was not refreshed")

	require.NoError(t, db.Delete(&tables.TableBudget{}, "id = ?", "b-vk-new").Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindVirtualKey, ID: "vk-1"}}))
	_, ok = cols.Budgets.Get("b-vk-new")
	require.False(t, ok, "a budget whose row is gone is dropped by the key's refresh")
	k, _ = cols.VirtualKeys.Get("vk-1")
	require.Equal(t, []string{"b-vk"}, k.BudgetIDs)
}

// TestRemoveOfKeyDropsWhatItHolds checks that a Remove ref of a key drops its budgets (its provider
// configs' included), its rate limit and the model configs scoped to it with their limits, while
// the team's and the other key's records stay.
func TestRemoveOfKeyDropsWhatItHolds(t *testing.T) {
	ctx := context.Background()
	s := loadedGovernance(t, governanceDB(t).DB())
	cols := s.Collections
	for _, id := range []string{"b-vk", "b-pc", "b-mc"} {
		_, ok := cols.Budgets.Get(id)
		require.True(t, ok, "fixture check: %s is loaded", id)
	}
	_, ok := cols.ModelConfigs.Get("mc-1")
	require.True(t, ok, "fixture check: the key-scoped model config is loaded")

	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindVirtualKey, ID: "vk-1", Remove: true}}))

	_, ok = cols.VirtualKeys.Get("vk-1")
	require.False(t, ok)
	for _, id := range []string{"b-vk", "b-pc", "b-mc"} {
		_, ok := cols.Budgets.Get(id)
		require.False(t, ok, "%s goes with the key", id)
	}
	for _, id := range []string{"rl-vk", "rl-mc"} {
		_, ok := cols.RateLimits.Get(id)
		require.False(t, ok, "%s goes with the key", id)
	}
	_, ok = cols.ModelConfigs.Get("mc-1")
	require.False(t, ok, "the model config scoped to the key goes with it")

	_, ok = cols.VirtualKeys.Get("vk-2")
	require.True(t, ok)
	_, ok = cols.Budgets.Get("b-team")
	require.True(t, ok)
	_, ok = cols.RateLimits.Get("rl-team")
	require.True(t, ok)
}

// TestDeleteOfTeamRereadsItsKeys checks that deleting a team and refreshing a Remove ref of it
// re-reads its keys with their team cleared, keeps what the keys hold, and drops the team's own
// budget and rate limit.
func TestDeleteOfTeamRereadsItsKeys(t *testing.T) {
	ctx := context.Background()
	store := governanceDB(t)
	s := loadedGovernance(t, store.DB())
	cols := s.Collections
	k, _ := cols.VirtualKeys.Get("vk-1")
	require.Equal(t, "team-1", k.TeamID, "fixture check")

	require.NoError(t, store.DeleteTeam(ctx, "team-1"))
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindTeam, ID: "team-1", Remove: true}}))

	_, ok := cols.Teams.Get("team-1")
	require.False(t, ok)
	for _, id := range []string{"vk-1", "vk-2"} {
		k, ok := cols.VirtualKeys.Get(id)
		require.True(t, ok, "%s stays", id)
		require.Empty(t, k.TeamID, "%s is re-read with its team cleared", id)
	}
	require.Empty(t, cols.VirtualKeys.List("parent:"+string(KindTeam), "team-1"), "no key is indexed under the team")
	_, ok = cols.Budgets.Get("b-team")
	require.False(t, ok, "the team's budget goes with it")
	_, ok = cols.RateLimits.Get("rl-team")
	require.False(t, ok, "the team's rate limit goes with it")
	_, ok = cols.Budgets.Get("b-vk")
	require.True(t, ok, "the keys' own budgets stay")
	_, ok = cols.RateLimits.Get("rl-vk")
	require.True(t, ok)
}
