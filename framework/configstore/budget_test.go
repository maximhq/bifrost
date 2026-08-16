package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// legacyBudgetDigest restates the pre-quarter hashing contract - ID, then the
// marshalled MaxLimit, then ResetDuration - independently of the implementation.
// A budget with no quarter definition must keep producing exactly this digest,
// so an unguarded new field in GenerateBudgetHash fails here rather than
// silently forcing every deployment to resync its budgets on upgrade.
func legacyBudgetDigest(t *testing.T, b tables.TableBudget) string {
	t.Helper()
	h := sha256.New()
	h.Write([]byte(b.ID))
	data, err := sonic.Marshal(b.MaxLimit)
	require.NoError(t, err)
	h.Write(data)
	h.Write([]byte(b.ResetDuration))
	return hex.EncodeToString(h.Sum(nil))
}

// TestGenerateBudgetHashUnchangedForNonQuarterlyBudgets pins the back-compat
// contract: adding the quarter definition must not shift the digest of any
// budget that has none, or every upgraded gateway would see a spurious
// "config.json changed" on first boot.
func TestGenerateBudgetHashUnchangedForNonQuarterlyBudgets(t *testing.T) {
	for _, duration := range []string{"5m", "1h", "1d", "1w", "1M"} {
		budget := tables.TableBudget{ID: "budget-1", MaxLimit: 1000, ResetDuration: duration}
		got, err := GenerateBudgetHash(budget)
		require.NoError(t, err)
		assert.Equal(t, legacyBudgetDigest(t, budget), got, "duration %s", duration)
	}
}

// TestGenerateBudgetHashAgreesAcrossConfigAndDatabaseSources is the test that
// matters most here.
//
// GenerateBudgetHash exists to decide whether a budget in config.json differs
// from the one in the database. The two sources populate the reset config
// differently: a budget parsed from config.json has ResetConfig set and
// ResetConfigJSON still empty, because BeforeSave has not run on it, while a
// budget read back from the database has both. Hashing the raw JSON blob would
// therefore make the two sides disagree for every quarterly budget - not a
// missed change, but a change detected on every single comparison, resyncing
// forever. The hash has to read the effective quarter month instead.
func TestGenerateBudgetHashAgreesAcrossConfigAndDatabaseSources(t *testing.T) {
	fromConfigFile := tables.TableBudget{
		ID:            "budget-quarterly",
		MaxLimit:      5000,
		ResetDuration: "1Q",
		ResetConfig:   &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	fromDatabase := tables.TableBudget{
		ID:              "budget-quarterly",
		MaxLimit:        5000,
		ResetDuration:   "1Q",
		ResetConfig:     &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
		ResetConfigJSON: `{"quarter_start_month":4}`,
	}

	configHash, err := GenerateBudgetHash(fromConfigFile)
	require.NoError(t, err)
	dbHash, err := GenerateBudgetHash(fromDatabase)
	require.NoError(t, err)

	assert.Equal(t, configHash, dbHash,
		"a config.json budget and its persisted counterpart must hash identically, or change detection resyncs on every comparison")
}

// TestGenerateBudgetHashTracksQuarterStartMonth verifies the whole point of the
// change: editing the quarter definition in config.json is detected.
func TestGenerateBudgetHashTracksQuarterStartMonth(t *testing.T) {
	april := tables.TableBudget{
		ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q",
		ResetConfig: &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	october := tables.TableBudget{
		ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q",
		ResetConfig: &tables.BudgetResetConfig{QuarterStartMonth: int(time.October)},
	}
	unset := tables.TableBudget{ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q"}
	january := tables.TableBudget{
		ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q",
		ResetConfig: &tables.BudgetResetConfig{QuarterStartMonth: int(time.January)},
	}
	// A reset_config object that omits quarter_start_month: distinct from unset,
	// because the config is present and only the month defaults to zero.
	zeroValue := tables.TableBudget{
		ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q",
		ResetConfig: &tables.BudgetResetConfig{},
	}

	aprilHash, err := GenerateBudgetHash(april)
	require.NoError(t, err)
	octoberHash, err := GenerateBudgetHash(october)
	require.NoError(t, err)
	unsetHash, err := GenerateBudgetHash(unset)
	require.NoError(t, err)
	januaryHash, err := GenerateBudgetHash(january)
	require.NoError(t, err)
	zeroValueHash, err := GenerateBudgetHash(zeroValue)
	require.NoError(t, err)

	assert.NotEqual(t, aprilHash, octoberHash, "a quarter-start edit must be detected as a change")
	assert.Equal(t, unsetHash, januaryHash, "an unset quarter start and an explicit January are the same window")
	assert.Equal(t, januaryHash, zeroValueHash, "a zero quarter start normalizes to January, so it must not read as a config change")
}

// setupBudgetDBWithoutResetConfigColumn builds the pre-migration state: a
// governance_budgets table as it existed before reset_config_json.
//
// A fresh database is not a useful test of this migration, because CreateTable
// derives the schema from the current struct and produces the column whether or
// not the migration exists. Only an already-provisioned table can tell the two
// apart, which is the case that matters: every deployment upgrading into this
// release has one.
func setupBudgetDBWithoutResetConfigColumn(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "Failed to create test database")

	require.NoError(t, db.Exec(`
		CREATE TABLE governance_budgets (
			id VARCHAR(255) PRIMARY KEY,
			max_limit REAL NOT NULL,
			reset_duration VARCHAR(50) NOT NULL,
			last_reset DATETIME,
			current_usage REAL DEFAULT 0,
			override_amount REAL NOT NULL DEFAULT 0,
			override_mode VARCHAR(20) NOT NULL DEFAULT '',
			override_cycles_remaining INTEGER NOT NULL DEFAULT 0,
			override_cycles_total INTEGER NOT NULL DEFAULT 0,
			override_anchor_reset DATETIME,
			team_id VARCHAR(255),
			virtual_key_id VARCHAR(255),
			provider_config_id INTEGER,
			model_config_id VARCHAR(255),
			customer_id VARCHAR(255),
			config_hash VARCHAR(255),
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`).Error, "Failed to create pre-migration governance_budgets table")

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			id VARCHAR(255) PRIMARY KEY
		)
	`).Error, "Failed to create migrations table")

	return db
}

// TestMigrationAddsBudgetResetConfigColumnToExistingTable verifies the upgrade
// path: an already-provisioned budgets table gains the column, and running the
// migration again is a no-op.
func TestMigrationAddsBudgetResetConfigColumnToExistingTable(t *testing.T) {
	db := setupBudgetDBWithoutResetConfigColumn(t)
	ctx := context.Background()

	require.False(t, db.Migrator().HasColumn(&tables.TableBudget{}, "reset_config_json"),
		"precondition: the column must be absent before the migration runs")

	require.NoError(t, migrationAddBudgetResetConfigColumn(ctx, db, testMigrationLogger))
	assert.True(t, db.Migrator().HasColumn(&tables.TableBudget{}, "reset_config_json"))

	// Re-running must not error: migrations are replayed on every boot.
	require.NoError(t, migrationAddBudgetResetConfigColumn(ctx, db, testMigrationLogger))
	assert.True(t, db.Migrator().HasColumn(&tables.TableBudget{}, "reset_config_json"))
}

// TestExistingBudgetsKeepTheirCadenceAfterMigration is the upgrade-safety test.
//
// The migration is additive with no backfill, so every budget that predates it
// has a NULL reset config. That must read back as a nil ResetConfig and a
// January quarter start, leaving the budget's window arithmetic byte-identical
// to what it was before the upgrade. A backfill writing an explicit month here
// would be indistinguishable in the data but would start reporting these rows
// as quarterly-configured to the API and the UI.
func TestExistingBudgetsKeepTheirCadenceAfterMigration(t *testing.T) {
	db := setupBudgetDBWithoutResetConfigColumn(t)
	ctx := context.Background()

	// A budget written before the column existed.
	require.NoError(t, db.WithContext(ctx).Exec(
		`INSERT INTO governance_budgets (id, max_limit, reset_duration, last_reset, current_usage, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"legacy-budget", 250.0, "1M", time.Now().UTC(), 42.0, time.Now().UTC(), time.Now().UTC(),
	).Error)

	require.NoError(t, migrationAddBudgetResetConfigColumn(ctx, db, testMigrationLogger))

	var loaded tables.TableBudget
	require.NoError(t, db.WithContext(ctx).First(&loaded, "id = ?", "legacy-budget").Error)
	assert.Nil(t, loaded.ResetConfig)
	assert.Empty(t, loaded.ResetConfigJSON)
	assert.Equal(t, time.January, loaded.QuarterStartMonth())
	assert.Equal(t, "1M", loaded.ResetDuration)
	assert.Equal(t, 42.0, loaded.CurrentUsage)
}

// TestUpdateBudgetPersistsResetConfig verifies the quarter definition is
// config-owned: unlike usage and override state, which UpdateBudget carries
// forward from the stored row, the caller's value wins, exactly as it does for
// max_limit and reset_duration.
func TestUpdateBudgetPersistsResetConfig(t *testing.T) {
	store, db := setupFullMigrationDB(t)
	ctx := context.Background()

	budget := &tables.TableBudget{
		ID:            "budget-under-edit",
		MaxLimit:      1000,
		ResetDuration: "1Q",
		LastReset:     time.Now().UTC(),
		ResetConfig:   &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	require.NoError(t, store.CreateBudget(ctx, budget))

	var afterCreate tables.TableBudget
	require.NoError(t, db.WithContext(ctx).First(&afterCreate, "id = ?", budget.ID).Error)
	require.NotNil(t, afterCreate.ResetConfig)
	assert.Equal(t, int(time.April), afterCreate.ResetConfig.QuarterStartMonth)

	// Editing the quarter start persists the new definition.
	afterCreate.ResetConfig = &tables.BudgetResetConfig{QuarterStartMonth: int(time.October)}
	require.NoError(t, store.UpdateBudget(ctx, &afterCreate))

	var afterUpdate tables.TableBudget
	require.NoError(t, db.WithContext(ctx).First(&afterUpdate, "id = ?", budget.ID).Error)
	require.NotNil(t, afterUpdate.ResetConfig)
	assert.Equal(t, int(time.October), afterUpdate.ResetConfig.QuarterStartMonth)
}

// TestUpdateBudgetClearsResetConfigWhenLeavingQuarterly verifies switching a
// budget off a quarterly window drops the now-meaningless quarter definition
// rather than leaving it stranded in the column, where a later switch back to
// quarterly would silently resurrect a start month the operator never re-chose.
func TestUpdateBudgetClearsResetConfigWhenLeavingQuarterly(t *testing.T) {
	store, db := setupFullMigrationDB(t)
	ctx := context.Background()

	budget := &tables.TableBudget{
		ID:            "budget-leaving-quarterly",
		MaxLimit:      1000,
		ResetDuration: "1Q",
		LastReset:     time.Now().UTC(),
		ResetConfig:   &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	require.NoError(t, store.CreateBudget(ctx, budget))

	var loaded tables.TableBudget
	require.NoError(t, db.WithContext(ctx).First(&loaded, "id = ?", budget.ID).Error)
	loaded.ResetDuration = "1M"
	loaded.ResetConfig = nil
	require.NoError(t, store.UpdateBudget(ctx, &loaded))

	var afterUpdate tables.TableBudget
	require.NoError(t, db.WithContext(ctx).First(&afterUpdate, "id = ?", budget.ID).Error)
	assert.Equal(t, "1M", afterUpdate.ResetDuration)
	assert.Nil(t, afterUpdate.ResetConfig)
	assert.Empty(t, afterUpdate.ResetConfigJSON)
}
