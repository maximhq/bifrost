package governance

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestGovernanceDataFromRowsServesFullRowsWithLiveUsage checks the snapshot admin reads serve: rows
// carry every column the config store holds (a key's description and creation time, a team's
// customer), keys are listed under their value and, during a rotation's grace period, their previous
// value, teams count their keys, and budgets carry the usage charged since the rows were read.
func TestGovernanceDataFromRowsServesFullRowsWithLiveUsage(t *testing.T) {
	ctx := context.Background()
	customer := buildCustomer("cust-1", "Acme", nil)
	team := buildTeam("team-1", "Platform", nil)
	team.CustomerID = &customer.ID
	budget := buildBudget("vk-budget", 100, "1d")
	vk := buildVirtualKeyWithBudget("vk-1", "sk-bf-current", "rotating key", budget)
	vk.Description = "kept in the row only"
	vk.TeamID = &team.ID
	grace := time.Now().Add(time.Hour)
	vk.PreviousValue = *schemas.NewSecretVar("sk-bf-previous")
	vk.PreviousValueExpiresAt = &grace

	h := newLiveHarness(t, &configstore.GovernanceConfig{
		Customers:   []configstoreTables.TableCustomer{*customer},
		Teams:       []configstoreTables.TableTeam{*team},
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	require.NoError(t, store.BumpBudgetUsage(ctx, budget.ID, 12.5))

	usage := store.GetGovernanceUsageData(ctx)
	data, err := GovernanceDataFromRows(ctx, h.configStore, usage.Budgets, usage.RateLimits)
	require.NoError(t, err)

	current, ok := data.VirtualKeys["sk-bf-current"]
	require.True(t, ok, "a key is listed under its value")
	previous, ok := data.VirtualKeys["sk-bf-previous"]
	require.True(t, ok, "a rotating key is also listed under its previous value during the grace period")
	assert.Equal(t, current.ID, previous.ID)
	assert.Equal(t, "kept in the row only", current.Description, "the row's columns are served")
	assert.False(t, current.CreatedAt.IsZero(), "the row's creation time is served")
	require.Len(t, current.Budgets, 1)
	assert.Equal(t, 12.5, current.Budgets[0].CurrentUsage, "budgets carry live usage")
	require.NotNil(t, current.Team)
	assert.Equal(t, "Platform", current.Team.Name)
	require.Contains(t, data.Teams, "team-1")
	assert.Equal(t, int64(2), data.Teams["team-1"].VirtualKeyCount, "a team counts each listing of its keys, as the in-memory snapshot did")
	require.NotNil(t, data.Teams["team-1"].Customer)
	assert.Equal(t, "Acme", data.Teams["team-1"].Customer.Name)
	assert.Equal(t, 12.5, data.Budgets[budget.ID].CurrentUsage)
}

// TestModelConfigIndexShadowsAsBefore checks how model configs whose names fold to one key are
// indexed: the one with the higher ID answers after a load, as configs always loaded in ID order;
// deleting it leaves the key empty rather than reviving the one it shadowed; and a config whose name
// changes leaves its old key.
func TestModelConfigIndexShadowsAsBefore(t *testing.T) {
	ctx := context.Background()
	lower := buildModelConfig("mc-a", "gpt-4o", nil, nil, nil)
	higher := buildModelConfig("mc-b", "openai/gpt-4o", nil, nil, nil)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*lower, *higher},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)

	ids := func(model string) []string {
		var out []string
		for _, mc := range store.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", model, nil, nil) {
			out = append(out, mc.ID)
		}
		return out
	}
	require.Equal(t, []string{"mc-b"}, ids("gpt-4o"), "the higher ID answers for the shared key")

	h.deleteModelConfig("mc-b")
	require.Empty(t, ids("gpt-4o"), "deleting the answering config empties the key; the shadowed one does not answer")

	h.putModelConfig(buildModelConfig("mc-a", "gpt-4.1", nil, nil, nil))
	require.Equal(t, []string{"mc-a"}, ids("gpt-4.1"))
	require.Empty(t, ids("gpt-4o"))

	// Renamed again from a key it answers for, so the old key was occupied.
	h.putModelConfig(buildModelConfig("mc-a", "gpt-4.5", nil, nil, nil))
	require.Equal(t, []string{"mc-a"}, ids("gpt-4.5"))
	require.Empty(t, ids("gpt-4.1"), "a renamed config leaves its old key")
}

// TestModelConfigSettingsChangeKeepsItsKeyAnswering checks that refreshing a model config whose key
// does not change never leaves that key empty, so a request evaluated during the refresh still meets
// the config's limits.
func TestModelConfigSettingsChangeKeepsItsKeyAnswering(t *testing.T) {
	ctx := context.Background()
	config := func(calendarAligned bool) *configstoreTables.TableModelConfig {
		mc := buildModelConfig("mc-1", "gpt-4o", nil, buildBudget("mc-1-budget", 100, "1d"), nil)
		mc.CalendarAligned = calendarAligned
		return mc
	}
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*config(false)},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	answering := func() bool {
		return len(store.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", "gpt-4o", nil, nil)) == 1
	}
	require.True(t, answering(), "fixture check")

	var missed atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if !answering() {
				missed.Add(1)
			}
		}
	}()
	// A failed write ends the test early; the cleanup still stops and joins the reader.
	var stopOnce sync.Once
	stopReader := func() {
		stopOnce.Do(func() { close(stop) })
		<-done
	}
	t.Cleanup(stopReader)
	for i := range 200 {
		h.putModelConfig(config(i%2 == 0))
	}
	stopReader()
	require.Zero(t, missed.Load(), "a lookup during a settings-only refresh found no config for its key")
}

// TestDeletingShadowedModelConfigDropsItsLimits checks that deleting a model config another one
// shadows under the same key drops its budget and rate limit from memory, though no lookup reaches
// it, and leaves the answering config's limits in place and its key still answering with it.
func TestDeletingShadowedModelConfigDropsItsLimits(t *testing.T) {
	ctx := context.Background()
	shadowed := buildModelConfig("mc-a", "gpt-4o", nil, buildBudget("mc-a-budget", 10, "1d"), buildRateLimit("mc-a-rate-limit", 100, 100))
	answering := buildModelConfig("mc-b", "openai/gpt-4o", nil, buildBudget("mc-b-budget", 10, "1d"), nil)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*shadowed, *answering},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	ids := func() []string {
		var out []string
		for _, mc := range store.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", "gpt-4o", nil, nil) {
			out = append(out, mc.ID)
		}
		return out
	}
	require.NotNil(t, store.LoadBudget(ctx, "mc-a-budget"), "fixture check")
	require.NotNil(t, store.LoadRateLimit(ctx, "mc-a-rate-limit"), "fixture check")
	require.Equal(t, []string{"mc-b"}, ids(), "fixture check: the answering config holds the shared key")

	h.deleteModelConfig("mc-a")
	require.Nil(t, store.LoadBudget(ctx, "mc-a-budget"), "the shadowed config's budget goes with it")
	require.Nil(t, store.LoadRateLimit(ctx, "mc-a-rate-limit"), "and its rate limit")
	require.NotNil(t, store.LoadBudget(ctx, "mc-b-budget"), "the answering config keeps its budget")
	require.Equal(t, []string{"mc-b"}, ids(), "deleting the shadowed config leaves the shared key answering with the other")
}

// recordingLocker is a sync.Locker that records whether it is held and how often it was taken.
type recordingLocker struct {
	mu    sync.Mutex
	held  bool
	taken int
}

// Lock takes the lock and records it.
func (l *recordingLocker) Lock() {
	l.mu.Lock()
	l.held = true
	l.taken++
}

// Unlock records the release and releases the lock.
func (l *recordingLocker) Unlock() {
	l.held = false
	l.mu.Unlock()
}

// TestLimitHooksWrapLimitChanges checks that the store applies the limit changes it follows from the
// live store under the hooks' lock, and reports the limits it removed while still holding it.
func TestLimitHooksWrapLimitChanges(t *testing.T) {
	ctx := context.Background()
	mc := buildModelConfig("mc-1", "gpt-4o", nil, buildBudget("mc-budget", 10, "1d"), buildRateLimit("mc-rate-limit", 100, 100))
	h := newLiveHarness(t, &configstore.GovernanceConfig{ModelConfigs: []configstoreTables.TableModelConfig{*mc}}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	lock := &recordingLocker{}
	var removedBudgets, removedRateLimits []string
	store.SetLimitHooks(LimitHooks{
		Lock: lock,
		Removed: func(_ context.Context, budgetIDs, rateLimitIDs []string) {
			require.True(t, lock.held, "removals are reported under the lock")
			removedBudgets = append(removedBudgets, budgetIDs...)
			removedRateLimits = append(removedRateLimits, rateLimitIDs...)
		},
	})

	h.putModelConfig(buildModelConfig("mc-1", "gpt-4o", nil, buildBudget("mc-budget", 20, "1d"), buildRateLimit("mc-rate-limit", 100, 100)))
	require.Equal(t, 20.0, store.LoadBudget(ctx, "mc-budget").MaxLimit)
	require.Positive(t, lock.taken, "installs take the lock")
	require.Empty(t, removedBudgets, "an install removes nothing")

	h.deleteModelConfig("mc-1")
	require.Equal(t, []string{"mc-budget"}, removedBudgets)
	require.Equal(t, []string{"mc-rate-limit"}, removedRateLimits)
	require.False(t, lock.held)
}

// TestVirtualKeyViewNamesGrantedKeysAsTheProviderHoldsThem checks the keys a key's view lists for
// a provider config: each key the grant names, with the row ID, name, key_id, models and provider
// the provider's record holds now, as the config's own read of them showed. A key renamed after
// the virtual key was read shows its new name, a key the provider no longer holds is left out, and
// a grant of every key lists none.
func TestVirtualKeyViewNamesGrantedKeysAsTheProviderHoldsThem(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {Keys: []schemas.Key{
		{ID: "k1", Name: "one", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"gpt-4o"}, Weight: 1},
		{ID: "k2", Name: "two", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1},
	}}})
	h.putVirtualKey(buildVirtualKeyWithProviders("vk-1", "sk-bf-1", "named", []configstoreTables.TableVirtualKeyProviderConfig{{
		Provider: string(schemas.OpenAI), AllowedModels: schemas.WhiteList{"*"}, Keys: []configstoreTables.TableKey{{KeyID: "k1"}},
	}}))
	h.putVirtualKey(buildVirtualKeyWithProviders("vk-2", "sk-bf-2", "every", openAIConfig()))
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	config := func(value string) configstoreTables.TableVirtualKeyProviderConfig {
		t.Helper()
		vk := gs.GetGovernanceData(context.Background()).VirtualKeys[value]
		require.NotNil(t, vk)
		require.Len(t, vk.ProviderConfigs, 1)
		return vk.ProviderConfigs[0]
	}

	var read configstoreTables.TableVirtualKeyProviderConfig
	require.NoError(t, h.db.Preload("Keys", func(k *gorm.DB) *gorm.DB { return k.Select("id, name, key_id, models_json, provider") }).
		Where("virtual_key_id = ?", "vk-1").First(&read).Error)
	require.Len(t, read.Keys, 1)
	row := read.Keys[0]
	want, err := json.Marshal(read.Keys)
	require.NoError(t, err)
	got, err := json.Marshal(config("sk-bf-1").Keys)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got), "the key as the config's read of it shows it")
	require.False(t, config("sk-bf-1").AllowAllKeys)

	every := config("sk-bf-2")
	require.True(t, every.AllowAllKeys)
	require.NotNil(t, every.Keys)
	require.Empty(t, every.Keys, "a grant of every key lists none")

	require.NoError(t, h.db.Model(&configstoreTables.TableKey{}).Where("key_id = ?", "k1").UpdateColumn("name", "renamed").Error)
	h.refresh(livestore.Ref{Kind: livestore.KindProvider, ID: string(schemas.OpenAI)})
	keys := config("sk-bf-1").Keys
	require.Len(t, keys, 1)
	require.Equal(t, "renamed", keys[0].Name, "the key as the provider holds it now")
	require.Equal(t, row.ID, keys[0].ID)

	require.NoError(t, h.db.Where("key_id = ?", "k1").Delete(&configstoreTables.TableKey{}).Error)
	h.refresh(livestore.Ref{Kind: livestore.KindProvider, ID: string(schemas.OpenAI)})
	require.Empty(t, config("sk-bf-1").Keys, "a key the provider no longer holds is left out")
}
