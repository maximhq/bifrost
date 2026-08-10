package tables

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
