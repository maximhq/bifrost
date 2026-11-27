package configstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/migrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newRunStepsTestDB opens a file-backed SQLite database so every pooled connection sees the same migrations table.
func newRunStepsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "steps.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db
}

// countingStep returns a step that records one gormigrate row per ID and bumps calls[id] each time it is invoked.
func countingStep(calls map[string]int, ids ...string) migrationStep {
	return migrationStep{
		IDs: ids,
		run: func(ctx context.Context, db *gorm.DB, _ schemas.Logger) error {
			for _, id := range ids {
				calls[id]++
				m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
					ID:      id,
					Migrate: func(*gorm.DB) error { return nil },
				}})
				if err := m.Migrate(); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// TestRunMigrationSteps_FreshDBRunsEveryStep verifies an empty migrations table makes every step run.
func TestRunMigrationSteps_FreshDBRunsEveryStep(t *testing.T) {
	db := newRunStepsTestDB(t)
	calls := map[string]int{}
	steps := []migrationStep{countingStep(calls, "a"), countingStep(calls, "b"), countingStep(calls, "c")}

	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), steps))

	assert.Equal(t, map[string]int{"a": 1, "b": 1, "c": 1}, calls)
}

// TestRunMigrationSteps_SkipsAppliedStepsAndRunsPending verifies applied steps are not invoked when a new step is added.
func TestRunMigrationSteps_SkipsAppliedStepsAndRunsPending(t *testing.T) {
	db := newRunStepsTestDB(t)
	calls := map[string]int{}
	steps := []migrationStep{countingStep(calls, "a"), countingStep(calls, "b"), countingStep(calls, "c")}
	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), steps))

	steps = append(steps, countingStep(calls, "d"))
	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), steps))

	assert.Equal(t, map[string]int{"a": 1, "b": 1, "c": 1, "d": 1}, calls)
}

// TestRunMigrationSteps_AllAppliedRunsNothing verifies a fully applied list invokes no step.
func TestRunMigrationSteps_AllAppliedRunsNothing(t *testing.T) {
	db := newRunStepsTestDB(t)
	calls := map[string]int{}
	steps := []migrationStep{countingStep(calls, "a"), countingStep(calls, "b")}
	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), steps))
	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), steps))

	assert.Equal(t, map[string]int{"a": 1, "b": 1}, calls)
}

// TestRunMigrationSteps_GroupedStepRunsWhenOneIDPending verifies a multi-ID step runs if any of its IDs is missing.
func TestRunMigrationSteps_GroupedStepRunsWhenOneIDPending(t *testing.T) {
	db := newRunStepsTestDB(t)
	calls := map[string]int{}
	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), []migrationStep{countingStep(calls, "g1")}))
	calls["g1"] = 0

	grouped := countingStep(calls, "g1", "g2")
	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), []migrationStep{grouped}))

	assert.Equal(t, 1, calls["g2"])
	pending, err := pendingMigrationStepIDs(context.Background(), db, []migrationStep{grouped})
	require.NoError(t, err)
	assert.Empty(t, pending)
}

// TestRunMigrationSteps_PreflightErrorRunsEveryStep verifies a failing pending read falls back to running all steps.
func TestRunMigrationSteps_PreflightErrorRunsEveryStep(t *testing.T) {
	db := newRunStepsTestDB(t)
	// A migrations table without the id column passes the column checks in PendingIDs, then fails the read.
	require.NoError(t, db.Exec("CREATE TABLE migrations (sequence INTEGER, applied_at DATETIME, status TEXT)").Error)
	calls := map[string]int{}
	steps := []migrationStep{countingStep(calls, "a"), countingStep(calls, "b")}

	_, err := pendingMigrationStepIDs(context.Background(), db, steps)
	require.Error(t, err)

	// The steps themselves cannot use this broken table, so they only record that they were called.
	stub := func(id string) migrationStep {
		return migrationStep{IDs: []string{id}, run: func(context.Context, *gorm.DB, schemas.Logger) error {
			calls[id]++
			return nil
		}}
	}
	calls = map[string]int{}
	require.NoError(t, runMigrationSteps(context.Background(), db, newMockLogger(), []migrationStep{stub("a"), stub("b")}))

	assert.Equal(t, map[string]int{"a": 1, "b": 1}, calls)
}
