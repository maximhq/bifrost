package tables

import (
	"math"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestTableBudgetOverrideLifecycle verifies replacement, effective-limit, and clear behavior.
func TestTableBudgetOverrideLifecycle(t *testing.T) {
	budget := &TableBudget{MaxLimit: 100}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 4))
	assert.True(t, budget.HasActiveOverride())
	assert.Equal(t, 125.0, budget.EffectiveMaxLimit())
	assert.Equal(t, 4, budget.OverrideCyclesRemaining)

	require.NoError(t, budget.SetOverride(50, BudgetOverrideModeForever, 0))
	assert.True(t, budget.HasActiveOverride())
	assert.Equal(t, 150.0, budget.EffectiveMaxLimit())
	assert.Equal(t, BudgetOverrideModeForever, budget.OverrideMode)

	budget.ClearOverride()
	assert.False(t, budget.HasActiveOverride())
	assert.Equal(t, 100.0, budget.EffectiveMaxLimit())
	assert.Zero(t, budget.OverrideAmount)
	assert.Empty(t, budget.OverrideMode)
	assert.Zero(t, budget.OverrideCyclesRemaining)
}

// TestTableBudgetSetOverrideRestoresPreviousState verifies invalid replacements are non-mutating.
func TestTableBudgetSetOverrideRestoresPreviousState(t *testing.T) {
	budget := &TableBudget{MaxLimit: 100}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 2))

	require.Error(t, budget.SetOverride(50, BudgetOverrideModeCycles, 0))
	assert.Equal(t, 25.0, budget.OverrideAmount)
	assert.Equal(t, BudgetOverrideModeCycles, budget.OverrideMode)
	assert.Equal(t, 2, budget.OverrideCyclesRemaining)
}

// TestTableBudgetRefreshOverrideCyclesRemaining verifies finite overrides expire
// as windows close while permanent overrides survive resets. The remaining count
// is derived from the grant and LastReset, so advancing LastReset is what spends
// a cycle.
func TestTableBudgetRefreshOverrideCyclesRemaining(t *testing.T) {
	grantedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	finite := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, finite.SetOverride(25, BudgetOverrideModeCycles, 2))
	assert.Equal(t, 2, finite.OverrideCyclesTotal)
	require.NotNil(t, finite.OverrideAnchorReset)
	assert.True(t, finite.OverrideAnchorReset.Equal(grantedAt))

	finite.LastReset = grantedAt.Add(time.Hour)
	finite.RefreshOverrideCyclesRemaining()
	assert.Equal(t, 1, finite.OverrideCyclesRemaining)
	assert.Equal(t, 125.0, finite.EffectiveMaxLimit())

	finite.LastReset = grantedAt.Add(2 * time.Hour)
	finite.RefreshOverrideCyclesRemaining()
	assert.False(t, finite.HasActiveOverride())
	assert.Equal(t, 100.0, finite.EffectiveMaxLimit())

	permanent := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, permanent.SetOverride(50, BudgetOverrideModeForever, 0))
	permanent.LastReset = grantedAt.Add(5 * time.Hour)
	permanent.RefreshOverrideCyclesRemaining()
	assert.True(t, permanent.HasActiveOverride())
	assert.Equal(t, 150.0, permanent.EffectiveMaxLimit())
}

// TestTableBudgetRefreshOverrideCyclesRemainingIsIdempotent verifies that
// refreshing repeatedly at a fixed LastReset does not spend extra cycles. This is
// the property that makes it safe for every node, the ticker, the request path and
// every config reload to all call it freely.
func TestTableBudgetRefreshOverrideCyclesRemainingIsIdempotent(t *testing.T) {
	grantedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	budget := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 3))

	budget.LastReset = grantedAt.Add(time.Hour)
	for i := 0; i < 10; i++ {
		budget.RefreshOverrideCyclesRemaining()
	}
	assert.Equal(t, 2, budget.OverrideCyclesRemaining, "repeated refreshes at one boundary must spend exactly one cycle")
}

// TestTableBudgetRefreshOverrideCyclesRemainingSpendsWholeGrantAfterLongGap
// verifies a multi-window gap collapses to the correct count in one call rather
// than needing one call per elapsed window.
func TestTableBudgetRefreshOverrideCyclesRemainingSpendsWholeGrantAfterLongGap(t *testing.T) {
	grantedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	budget := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 3))

	budget.LastReset = grantedAt.Add(240 * time.Hour)
	budget.RefreshOverrideCyclesRemaining()
	assert.False(t, budget.HasActiveOverride(), "a 3 cycle grant must be fully spent after 240 elapsed windows")
}

// overrideAnchor returns a fixed grant anchor for validation table cases.
func overrideAnchor() *time.Time {
	anchor := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return &anchor
}

// TestTableBudgetValidateOverride verifies that persisted override fields cannot form an ambiguous state.
func TestTableBudgetValidateOverride(t *testing.T) {
	tests := []struct {
		name      string
		budget    TableBudget
		wantError bool
	}{
		{name: "no override"},
		{
			name: "forever override",
			budget: TableBudget{
				OverrideAmount: 25,
				OverrideMode:   BudgetOverrideModeForever,
			},
		},
		{
			name: "finite override",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 3,
				OverrideCyclesTotal:     3,
				OverrideAnchorReset:     overrideAnchor(),
			},
		},
		{
			name: "finite override partway through its grant",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 1,
				OverrideCyclesTotal:     3,
				OverrideAnchorReset:     overrideAnchor(),
			},
		},
		{
			name: "cycles mode without an anchor",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 3,
				OverrideCyclesTotal:     3,
			},
			wantError: true,
		},
		{
			name: "cycles mode with total below remaining",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 4,
				OverrideCyclesTotal:     3,
				OverrideAnchorReset:     overrideAnchor(),
			},
			wantError: true,
		},
		{
			name: "forever mode with a grant anchor",
			budget: TableBudget{
				OverrideAmount:      25,
				OverrideMode:        BudgetOverrideModeForever,
				OverrideAnchorReset: overrideAnchor(),
			},
			wantError: true,
		},
		{
			name:      "no mode with a grant anchor",
			budget:    TableBudget{OverrideAnchorReset: overrideAnchor()},
			wantError: true,
		},
		{name: "amount without mode", budget: TableBudget{OverrideAmount: 25}, wantError: true},
		{name: "cycles without mode", budget: TableBudget{OverrideCyclesRemaining: 1}, wantError: true},
		{
			name:      "cycles mode without amount",
			budget:    TableBudget{OverrideMode: BudgetOverrideModeCycles, OverrideCyclesRemaining: 1},
			wantError: true,
		},
		{
			name:      "cycles mode without remaining cycles",
			budget:    TableBudget{OverrideAmount: 25, OverrideMode: BudgetOverrideModeCycles},
			wantError: true,
		},
		{
			name: "forever mode with remaining cycles",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeForever,
				OverrideCyclesRemaining: 1,
			},
			wantError: true,
		},
		{
			name:      "unknown mode",
			budget:    TableBudget{OverrideAmount: 25, OverrideMode: BudgetOverrideMode("unknown")},
			wantError: true,
		},
		{
			name:      "negative amount",
			budget:    TableBudget{OverrideAmount: -1, OverrideMode: BudgetOverrideModeForever},
			wantError: true,
		},
		{
			name:      "not a number amount",
			budget:    TableBudget{OverrideAmount: math.NaN(), OverrideMode: BudgetOverrideModeForever},
			wantError: true,
		},
		{
			name:      "infinite amount",
			budget:    TableBudget{OverrideAmount: math.Inf(1), OverrideMode: BudgetOverrideModeForever},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.budget.validateOverride()
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// setupBudgetTestDB creates an in-memory SQLite database with the budget table
// and its virtual key owner migrated. Kept separate from setupTestDB so the
// nested-preload test can rely on exactly these two tables being present.
func setupBudgetTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TableVirtualKey{}, &TableBudget{}))
	return db
}

// TestParseDurationQuarter verifies the "Q" suffix parses as an approximate
// 90-day window, mirroring how "M" approximates a month as 30 days. The
// approximation only drives rolling windows; a calendar-aligned quarter uses
// the exact boundary instead.
func TestParseDurationQuarter(t *testing.T) {
	oneQuarter, err := ParseDuration("1Q")
	require.NoError(t, err)
	assert.Equal(t, 90*24*time.Hour, oneQuarter)

	twoQuarters, err := ParseDuration("2Q")
	require.NoError(t, err)
	assert.Equal(t, 180*24*time.Hour, twoQuarters)

	// A quarter must sort above a month so inheritUsageFromClosestShorterBudget
	// treats a monthly budget as the closest shorter window.
	oneMonth, err := ParseDuration("1M")
	require.NoError(t, err)
	assert.Greater(t, oneQuarter, oneMonth)

	_, err = ParseDuration("Q")
	assert.Error(t, err, "a bare suffix with no multiplier must be rejected")
}

// TestBudgetResetConfigRoundTripsThroughDatabase verifies the quarter definition
// survives a save/reload cycle via the JSON column.
func TestBudgetResetConfigRoundTripsThroughDatabase(t *testing.T) {
	db := setupBudgetTestDB(t)

	budget := &TableBudget{
		ID:            "budget-quarterly",
		MaxLimit:      1000,
		ResetDuration: "1Q",
		LastReset:     time.Now().UTC(),
		ResetConfig:   &BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	require.NoError(t, db.Create(budget).Error)

	var reloaded TableBudget
	require.NoError(t, db.First(&reloaded, "id = ?", "budget-quarterly").Error)
	require.NotNil(t, reloaded.ResetConfig)
	assert.Equal(t, int(time.April), reloaded.ResetConfig.QuarterStartMonth)
	assert.Equal(t, time.April, reloaded.QuarterStartMonth())
}

// TestBudgetResetConfigSurvivesNestedPreload is the cluster-correctness test.
//
// Enterprise peers never receive a budget's configuration over the wire: the
// gossip payload carries usage only, and a config change broadcasts just an
// entity ID, which each peer answers by re-reading the row (ReloadVirtualKey).
// The quarter definition therefore reaches other nodes solely through the
// AfterFind hook firing on a nested preload. If it does not fire, the writing
// node computes April quarters while every peer computes January ones, with
// nothing in the logs to show for it.
func TestBudgetResetConfigSurvivesNestedPreload(t *testing.T) {
	db := setupBudgetTestDB(t)

	vk := &TableVirtualKey{
		ID:    "vk-quarterly",
		Name:  "quarterly-vk",
		Value: schemas.SecretVar{Val: "bf-vk-quarterly"},
		Budgets: []TableBudget{{
			ID:            "budget-nested",
			MaxLimit:      500,
			ResetDuration: "1Q",
			LastReset:     time.Now().UTC(),
			ResetConfig:   &BudgetResetConfig{QuarterStartMonth: int(time.October)},
		}},
	}
	require.NoError(t, db.Create(vk).Error)

	var reloaded TableVirtualKey
	require.NoError(t, db.Preload("Budgets").First(&reloaded, "id = ?", "vk-quarterly").Error)
	require.Len(t, reloaded.Budgets, 1)
	require.NotNil(t, reloaded.Budgets[0].ResetConfig,
		"AfterFind must fire on preloaded budgets, otherwise cluster peers silently default to January quarters")
	assert.Equal(t, int(time.October), reloaded.Budgets[0].ResetConfig.QuarterStartMonth)
}

// TestBudgetResetConfigAbsentStaysNil verifies a budget without a quarter
// definition round-trips as nil rather than as a zeroed struct, so the JSON API
// omits the field and QuarterStartMonth falls back to January.
func TestBudgetResetConfigAbsentStaysNil(t *testing.T) {
	db := setupBudgetTestDB(t)

	budget := &TableBudget{
		ID:            "budget-monthly",
		MaxLimit:      100,
		ResetDuration: "1M",
		LastReset:     time.Now().UTC(),
	}
	require.NoError(t, db.Create(budget).Error)

	var reloaded TableBudget
	require.NoError(t, db.First(&reloaded, "id = ?", "budget-monthly").Error)
	assert.Nil(t, reloaded.ResetConfig)
	assert.Empty(t, reloaded.ResetConfigJSON)
	assert.Equal(t, time.January, reloaded.QuarterStartMonth())
}

// TestBudgetResetConfigRejectsInvalidQuarterStartMonth verifies BeforeSave
// guards the month range. A zero value means "unset" and is accepted.
func TestBudgetResetConfigRejectsInvalidQuarterStartMonth(t *testing.T) {
	db := setupBudgetTestDB(t)

	for _, month := range []int{-1, 13, 99} {
		budget := &TableBudget{
			ID:            "budget-bad-month",
			MaxLimit:      100,
			ResetDuration: "1Q",
			LastReset:     time.Now().UTC(),
			ResetConfig:   &BudgetResetConfig{QuarterStartMonth: month},
		}
		err := db.Create(budget).Error
		require.Error(t, err, "quarter_start_month %d must be rejected", month)
		assert.Contains(t, err.Error(), "quarter_start_month")
	}
}

// TestBudgetResetConfigRejectedOnNonQuarterlyDuration verifies a quarter
// definition cannot be attached to a window that has no quarters, which would
// otherwise persist a setting that silently does nothing.
func TestBudgetResetConfigRejectedOnNonQuarterlyDuration(t *testing.T) {
	db := setupBudgetTestDB(t)

	budget := &TableBudget{
		ID:            "budget-monthly-with-quarter-config",
		MaxLimit:      100,
		ResetDuration: "1M",
		LastReset:     time.Now().UTC(),
		ResetConfig:   &BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	err := db.Create(budget).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reset_config")
}

// TestIsQuarterlyDurationIsCaseSensitive pins the suffix grammar.
//
// The duration suffixes are case-significant - "M" is a month while "m" is a
// minute - so "1q" must not be mistaken for a quarter. Reset durations arrive
// as free-form strings from config.json and the API, where a lowercase typo is
// an easy mistake; treating it as quarterly would silently give the budget a
// 90-day window, whereas rejecting it surfaces the typo. ParseDuration already
// rejects "1q" outright, and this keeps the two in agreement.
func TestIsQuarterlyDurationIsCaseSensitive(t *testing.T) {
	assert.True(t, IsQuarterlyDuration("1Q"))
	assert.True(t, IsQuarterlyDuration("2Q"))

	assert.False(t, IsQuarterlyDuration("1q"))
	assert.False(t, IsQuarterlyDuration(""))
	assert.False(t, IsQuarterlyDuration("1M"))
	assert.False(t, IsQuarterlyDuration("Quarterly"))

	_, err := ParseDuration("1q")
	assert.Error(t, err, "a lowercase suffix must not silently parse as a quarter")
}

// TestBudgetRejectsZeroLengthQuarter verifies "0Q" is caught by the existing
// non-positive duration guard rather than persisting a window of length zero,
// which the reset path would see as perpetually due.
func TestBudgetRejectsZeroLengthQuarter(t *testing.T) {
	db := setupBudgetTestDB(t)

	budget := &TableBudget{
		ID:            "budget-zero-quarter",
		MaxLimit:      100,
		ResetDuration: "0Q",
		LastReset:     time.Now().UTC(),
	}
	err := db.Create(budget).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reset duration must be > 0")
}

// TestBudgetResetConfigNotSerializedWhenValidationFails pins the ordering inside
// BeforeSave: validation runs before serialization, so a rejected quarter
// definition never reaches the column. Were the order reversed, a failed save
// would leave the blob populated on the in-memory struct, and a later save that
// cleared ResetConfig would still carry the stale JSON through.
func TestBudgetResetConfigNotSerializedWhenValidationFails(t *testing.T) {
	db := setupBudgetTestDB(t)

	budget := &TableBudget{
		ID:            "budget-rejected",
		MaxLimit:      100,
		ResetDuration: "1M",
		LastReset:     time.Now().UTC(),
		ResetConfig:   &BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	require.Error(t, db.Create(budget).Error)
	assert.Empty(t, budget.ResetConfigJSON,
		"a rejected reset config must not be serialized onto the struct")
}

// TestBudgetAfterFindRejectsMalformedResetConfig verifies a corrupt blob surfaces
// as a read error rather than silently degrading to a January quarter. A budget
// whose quarter definition cannot be read is not the same as one that has none,
// and enforcing against the wrong window is worse than failing the read.
func TestBudgetAfterFindRejectsMalformedResetConfig(t *testing.T) {
	db := setupBudgetTestDB(t)

	budget := &TableBudget{
		ID:            "budget-corrupt",
		MaxLimit:      100,
		ResetDuration: "1Q",
		LastReset:     time.Now().UTC(),
		ResetConfig:   &BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	require.NoError(t, db.Create(budget).Error)

	// Corrupt the column behind the model's back.
	require.NoError(t, db.Exec(
		`UPDATE governance_budgets SET reset_config_json = ? WHERE id = ?`,
		"{not json", budget.ID,
	).Error)

	var reloaded TableBudget
	err := db.First(&reloaded, "id = ?", budget.ID).Error
	require.Error(t, err, "a malformed reset config must fail the read, not default to January")
}

// TestBudgetQuarterStartMonthDefaultsToJanuary verifies the accessor is safe on
// a nil receiver and on a budget whose config was never set, so every call site
// can read it without a nil check.
func TestBudgetQuarterStartMonthDefaultsToJanuary(t *testing.T) {
	var nilBudget *TableBudget
	assert.Equal(t, time.January, nilBudget.QuarterStartMonth())

	assert.Equal(t, time.January, (&TableBudget{}).QuarterStartMonth())
	assert.Equal(t, time.January, (&TableBudget{ResetConfig: &BudgetResetConfig{}}).QuarterStartMonth())
	assert.Equal(t, time.July, (&TableBudget{ResetConfig: &BudgetResetConfig{QuarterStartMonth: 7}}).QuarterStartMonth())
}
