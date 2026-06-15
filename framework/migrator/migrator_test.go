package migrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openPendingIDsTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func createCurrentMigrationTable(t *testing.T, db *gorm.DB, tableName, idColumn string) {
	t.Helper()

	require.NoError(t, db.Table(tableName).AutoMigrate((&Gormigrate{
		tx: db,
		options: &Options{
			TableName:           tableName,
			IDColumnName:        idColumn,
			IDColumnSize:        255,
			SequenceColumnName:  "sequence",
			AppliedAtColumnName: "applied_at",
			StatusColumnName:    "status",
		},
	}).model()))
}

func insertMigrationRow(t *testing.T, db *gorm.DB, tableName, idColumn, id string, sequence int64) {
	t.Helper()

	require.NoError(t, db.Table(tableName).Create(map[string]any{
		idColumn:     id,
		"sequence":   sequence,
		"applied_at": time.Now(),
		"status":     "success",
	}).Error)
}

func TestPendingIDsMissingTableReturnsAllExpected(t *testing.T) {
	db := openPendingIDsTestDB(t)

	pending, err := PendingIDs(context.Background(), db, nil, []string{"one", "two"})

	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, pending)
}

func TestPendingIDsReturnsMissingIDsInExpectedOrder(t *testing.T) {
	db := openPendingIDsTestDB(t)
	createCurrentMigrationTable(t, db, DefaultOptions.TableName, DefaultOptions.IDColumnName)
	insertMigrationRow(t, db, DefaultOptions.TableName, DefaultOptions.IDColumnName, "two", 1)

	pending, err := PendingIDs(context.Background(), db, nil, []string{"one", "two", "three"})

	require.NoError(t, err)
	require.Equal(t, []string{"one", "three"}, pending)
}

func TestPendingIDsCurrentTableWithAllIDsReturnsNone(t *testing.T) {
	db := openPendingIDsTestDB(t)
	createCurrentMigrationTable(t, db, DefaultOptions.TableName, DefaultOptions.IDColumnName)
	insertMigrationRow(t, db, DefaultOptions.TableName, DefaultOptions.IDColumnName, "one", 1)
	insertMigrationRow(t, db, DefaultOptions.TableName, DefaultOptions.IDColumnName, "two", 2)

	pending, err := PendingIDs(context.Background(), db, nil, []string{"one", "two"})

	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestPendingIDsLegacyTableWithoutMetadataReturnsAllExpected(t *testing.T) {
	db := openPendingIDsTestDB(t)
	require.NoError(t, db.Exec("CREATE TABLE migrations (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO migrations (id) VALUES (?)", "one").Error)

	pending, err := PendingIDs(context.Background(), db, nil, []string{"one"})

	require.NoError(t, err)
	require.Equal(t, []string{"one"}, pending)
}

func TestPendingIDsUsesCustomOptions(t *testing.T) {
	db := openPendingIDsTestDB(t)
	opts := *DefaultOptions
	opts.TableName = "custom_migrations"
	opts.IDColumnName = "migration_id"
	createCurrentMigrationTable(t, db, opts.TableName, opts.IDColumnName)
	insertMigrationRow(t, db, opts.TableName, opts.IDColumnName, "one", 1)

	pending, err := PendingIDs(context.Background(), db, &opts, []string{"one", "two"})

	require.NoError(t, err)
	require.Equal(t, []string{"two"}, pending)
}
