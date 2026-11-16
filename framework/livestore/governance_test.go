package livestore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
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
	require.Empty(t, cols.VirtualKeys.List(ParentIndex(KindTeam), "team-1"), "no key is indexed under the team")
	_, ok = cols.Budgets.Get("b-team")
	require.False(t, ok, "the team's budget goes with it")
	_, ok = cols.RateLimits.Get("rl-team")
	require.False(t, ok, "the team's rate limit goes with it")
	_, ok = cols.Budgets.Get("b-vk")
	require.True(t, ok, "the keys' own budgets stay")
	_, ok = cols.RateLimits.Get("rl-vk")
	require.True(t, ok)
}

// TestLimitOwnersFlagUnheldLimits checks that a store built with LimitOwners reads the owners a
// build adds with the registry's own: a budget or rate limit no registry holder holds is calendar
// aligned when one of the build's owners holds it with its flag on, on the load and on a refresh; a
// limit a registry holder holds keeps that holder's flag whatever the build's owners say; and a read
// of the build's owners that fails fails the refresh, leaving the record as it was.
func TestLimitOwnersFlagUnheldLimits(t *testing.T) {
	ctx := context.Background()
	store := governanceDB(t)
	db := store.DB()
	window := "1m"
	requests := int64(5)
	require.NoError(t, db.Exec("CREATE TABLE build_limit_owners (id VARCHAR(64) PRIMARY KEY, budget_id VARCHAR(64), rate_limit_id VARCHAR(64), calendar_aligned BOOLEAN NOT NULL)").Error)
	owner := func(id, budgetID, rateLimitID string, aligned bool) {
		t.Helper()
		require.NoError(t, db.Exec("INSERT INTO build_limit_owners (id, budget_id, rate_limit_id, calendar_aligned) VALUES (?, ?, ?, ?)", id, budgetID, rateLimitID, aligned).Error)
	}
	rollingTeam := "team-build-rolling"
	require.NoError(t, db.Create(&tables.TableTeam{ID: rollingTeam, Name: "rolling"}).Error)
	require.NoError(t, db.Create(&tables.TableBudget{ID: "b-build-owned", MaxLimit: 1, ResetDuration: "1d"}).Error)
	require.NoError(t, db.Create(&tables.TableBudget{ID: "b-build-plain", MaxLimit: 1, ResetDuration: "1d"}).Error)
	require.NoError(t, db.Create(&tables.TableBudget{ID: "b-build-team", MaxLimit: 1, ResetDuration: "1d", TeamID: &rollingTeam}).Error)
	require.NoError(t, db.Create(&tables.TableRateLimit{ID: "rl-build-owned", RequestMaxLimit: &requests, RequestResetDuration: &window}).Error)
	owner("o-1", "b-build-owned", "", false)
	owner("o-2", "b-build-owned", "rl-build-owned", true)
	owner("o-3", "b-build-team", "", true)

	budgetOwners := []registry.LimitOwner{{From: "build_limit_owners o", Column: "o.budget_id", Calendar: "o.calendar_aligned"}}
	rateLimitOwners := []registry.LimitOwner{{From: "build_limit_owners o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned"}}
	s, err := New(LimitOwners(budgetOwners, rateLimitOwners))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	require.NoError(t, s.LoadAll(ctx, db))
	cols := s.Collections

	budgetAligned := func(id string) bool {
		b, ok := cols.Budgets.Get(id)
		require.True(t, ok, id)
		return b.IsCalendarAligned
	}
	require.True(t, budgetAligned("b-build-owned"), "an unheld budget an aligned owner holds is calendar aligned")
	require.False(t, budgetAligned("b-build-plain"))
	require.False(t, budgetAligned("b-build-team"), "a team's budget takes its team's flag, not a build owner's")
	r, ok := cols.RateLimits.Get("rl-build-owned")
	require.True(t, ok)
	require.True(t, r.IsCalendarAligned)

	require.NoError(t, db.Exec("UPDATE build_limit_owners SET calendar_aligned = ? WHERE id = ?", false, "o-2").Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindBudget, ID: "b-build-owned"}}))
	require.False(t, budgetAligned("b-build-owned"), "a refresh reads the owners again")

	require.NoError(t, db.Exec("UPDATE build_limit_owners SET calendar_aligned = ? WHERE id = ?", true, "o-2").Error)
	require.NoError(t, db.Exec("DROP TABLE build_limit_owners").Error)
	require.Error(t, s.Refresh(ctx, []Ref{{Kind: KindBudget, ID: "b-build-owned"}}))
	require.False(t, budgetAligned("b-build-owned"), "a failed read leaves the record as it was")
}

// TestKeysShareTheirGrantSet checks that keys granted the same things hold one grant set, that a
// key whose grants change moves to a set of its own, and that a set no key holds any more is
// dropped once the grace period has passed.
func TestKeysShareTheirGrantSet(t *testing.T) {
	ctx := context.Background()
	db := governanceDB(t).DB()
	clock := setClock(t, time.Unix(1_000_000, 0))
	s := loadedGovernance(t, db)
	cols := s.Collections
	grantsOf := func(id string) registry.GrantSetID {
		t.Helper()
		k, ok := cols.VirtualKeys.Get(id)
		require.True(t, ok, id)
		_, held := cols.GrantSet(k.Grants)
		require.True(t, held, "%s's grant set is held", id)
		return k.Grants
	}
	require.NotEqual(t, grantsOf("vk-1"), grantsOf("vk-2"), "vk-1 holds a provider config vk-2 does not")

	require.NoError(t, db.Omit(clause.Associations).Create(&tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "vk-2", Provider: "openai", AllowedModels: schemas.WhiteList{"*"},
	}).Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindVirtualKey, ID: "vk-2"}}))
	shared := grantsOf("vk-1")
	require.Equal(t, shared, grantsOf("vk-2"), "keys granted the same things share a set")
	one, _ := cols.GrantSet(grantsOf("vk-1"))
	two, _ := cols.GrantSet(grantsOf("vk-2"))
	require.Same(t, one, two, "the set is stored once")
	k1, _ := cols.VirtualKeys.Get("vk-1")
	k2, _ := cols.VirtualKeys.Get("vk-2")
	require.NotEqual(t, k1.ProviderLimits[0].ConfigID, k2.ProviderLimits[0].ConfigID, "what funds each config stays with its key")

	require.NoError(t, db.Model(&tables.TableVirtualKeyProviderConfig{}).Where("virtual_key_id = ?", "vk-2").
		UpdateColumn("allowed_models", `["gpt-4o"]`).Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindVirtualKey, ID: "vk-2"}}))
	require.NotEqual(t, shared, grantsOf("vk-2"), "a key whose grants change moves to its own set")
	require.Equal(t, shared, grantsOf("vk-1"))

	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindVirtualKey, ID: "vk-1", Remove: true}}))
	*clock = clock.Add(internedGrace + time.Second)
	s.sweep()
	_, held := cols.GrantSet(shared)
	require.False(t, held, "a set no key holds is dropped after the grace period")
	grantsOf("vk-2")
}

// TestFileRecordsHoldEachKeysGrantSet checks that the records a gateway without a config store
// loads come with their keys' grant sets, so a key built from config.json grants what it declares.
func TestFileRecordsHoldEachKeysGrantSet(t *testing.T) {
	s, err := New()
	require.NoError(t, err)
	t.Cleanup(s.Close)
	fc := registry.FromConfig(registry.FileInput{Governance: &configstore.GovernanceConfig{
		VirtualKeys: []tables.TableVirtualKey{{
			ID: "vk-1", Name: "file", Value: *schemas.NewSecretVar("sk-file"),
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}}},
		}},
	}})
	require.NoError(t, s.LoadRecords(context.Background(), s.Collections.FileRecords(fc)))
	k, ok := s.Collections.VirtualKeys.Get("vk-1")
	require.True(t, ok)
	set, ok := s.Collections.GrantSet(k.Grants)
	require.True(t, ok, "the key's grant set is held")
	require.Len(t, set.Providers, 1)
	require.Equal(t, "openai", set.Providers[0].Provider)
}

// TestCustomersAreFoundByName checks the customer name index: a customer is found by its name,
// and a rename moves it to the new name.
func TestCustomersAreFoundByName(t *testing.T) {
	ctx := context.Background()
	db := governanceDB(t).DB()
	require.NoError(t, db.Omit(clause.Associations).Create(&tables.TableCustomer{ID: "cust-1", Name: "Acme"}).Error)
	s := loadedGovernance(t, db)
	cols := s.Collections
	c, ok := cols.Customers.GetBy(registry.CustomerByName, "Acme")
	require.True(t, ok)
	require.Equal(t, "cust-1", c.ID)

	require.NoError(t, db.Model(&tables.TableCustomer{}).Where("id = ?", "cust-1").UpdateColumn("name", "Acme Corp").Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindCustomer, ID: "cust-1"}}))
	_, ok = cols.Customers.GetBy(registry.CustomerByName, "Acme")
	require.False(t, ok, "the old name finds nothing")
	c, ok = cols.Customers.GetBy(registry.CustomerByName, "Acme Corp")
	require.True(t, ok)
	require.Equal(t, "cust-1", c.ID)
}

// TestKeysAreFoundByCreatorAndBusinessUnit checks the key indexes by creator and by owning business
// unit, and that a key edited to another creator moves between them.
func TestKeysAreFoundByCreatorAndBusinessUnit(t *testing.T) {
	ctx := context.Background()
	db := governanceDB(t).DB()
	ref := func(s string) *string { return &s }
	for _, k := range []*tables.TableVirtualKey{
		{ID: "vk-a", Name: "a", Value: *schemas.NewSecretVar("sk-a"), CreatedByUserID: ref("user-1"), BusinessUnitID: ref("bu-1")},
		{ID: "vk-b", Name: "b", Value: *schemas.NewSecretVar("sk-b"), CreatedByUserID: ref("user-1")},
		{ID: "vk-c", Name: "c", Value: *schemas.NewSecretVar("sk-c")},
	} {
		require.NoError(t, db.Omit(clause.Associations).Create(k).Error)
	}
	s := loadedGovernance(t, db)
	ids := func(index, key string) []string {
		var out []string
		for _, k := range s.Collections.VirtualKeys.List(index, key) {
			out = append(out, k.ID)
		}
		return out
	}
	require.Equal(t, []string{"vk-a", "vk-b"}, ids(registry.VirtualKeyByCreator, "user-1"))
	require.Equal(t, []string{"vk-a"}, ids(registry.VirtualKeyByBusinessUnit, "bu-1"))
	require.Empty(t, ids(registry.VirtualKeyByCreator, ""), "a key with no creator is not listed")

	require.NoError(t, db.Model(&tables.TableVirtualKey{}).Where("id = ?", "vk-b").UpdateColumn("created_by_user_id", "user-2").Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindVirtualKey, ID: "vk-b"}}))
	require.Equal(t, []string{"vk-a"}, ids(registry.VirtualKeyByCreator, "user-1"))
	require.Equal(t, []string{"vk-b"}, ids(registry.VirtualKeyByCreator, "user-2"))
}

// TestRoutingRulesAreFoundByScopeWithTheirTargets checks that routing rules load with their targets
// and are listed by scope, global rules under one key whatever scope ID they carry, and that a
// refresh moves a rule whose scope changed.
func TestRoutingRulesAreFoundByScopeWithTheirTargets(t *testing.T) {
	ctx := context.Background()
	db := governanceDB(t).DB()
	team := "team-1"
	model := "gpt-4o"
	for _, r := range []*tables.TableRoutingRule{
		{ID: "r-global", Name: "global", Scope: "global", CelExpression: "true", Priority: 1},
		{ID: "r-team", Name: "team", Scope: "team", ScopeID: &team, CelExpression: "true", Priority: 2},
	} {
		require.NoError(t, db.Omit(clause.Associations).Create(r).Error)
	}
	require.NoError(t, db.Create(&tables.TableRoutingTarget{RuleID: "r-team", Model: &model, Weight: 1}).Error)
	s := loadedGovernance(t, db)
	ids := func(key string) []string {
		var out []string
		for _, r := range s.Collections.RoutingRules.List(registry.RoutingRuleByScope, key) {
			out = append(out, r.ID)
		}
		return out
	}
	require.Equal(t, []string{"r-global"}, ids(registry.RoutingRuleScopeKey("global", "")))
	require.Equal(t, []string{"r-global"}, ids(registry.RoutingRuleScopeKey("global", "ignored")))
	require.Equal(t, []string{"r-team"}, ids(registry.RoutingRuleScopeKey("team", team)))
	teamRule, ok := s.Collections.RoutingRules.Get("r-team")
	require.True(t, ok)
	require.Len(t, teamRule.Targets, 1)
	require.Equal(t, model, *teamRule.Targets[0].Model)

	require.NoError(t, db.Model(&tables.TableRoutingRule{}).Where("id = ?", "r-team").Updates(map[string]any{"scope": "global", "scope_id": nil}).Error)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: KindRoutingRule, ID: "r-team"}}))
	require.Equal(t, []string{"r-global", "r-team"}, ids(registry.RoutingRuleScopeKey("global", "")))
	require.Empty(t, ids(registry.RoutingRuleScopeKey("team", team)))
}
