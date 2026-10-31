package logstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// pgIndexExists reports whether an index is present on a table in the test schema.
func pgIndexExists(t *testing.T, db *gorm.DB, table, indexName string) bool {
	t.Helper()
	var count int64
	err := db.Raw(`
		SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = ? AND tablename = ? AND indexname = ?
	`, pgTestSchema, table, indexName).Scan(&count).Error
	require.NoError(t, err, "failed to check index existence")
	return count > 0
}

// On Postgres the four delivery-filter indexes are built post-startup and
// CONCURRENTLY by ensurePerformanceIndexes, so migrationAddWebhookDeliveryFilterIndexes
// deliberately creates nothing there. Its rollback has to be exempt for the same
// reason and then some: a plain DropIndex inside the migration's transaction takes
// an ACCESS EXCLUSIVE lock on webhook_deliveries, blocking every read and write on
// an insert-heavy table — to remove indexes this migration never created.
func TestWebhookDeliveryFilterIndexRollbackSkipsPostgres(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}
	ctx := context.Background()

	require.NoError(t, db.Migrator().DropTable(&WebhookDelivery{}))
	require.NoError(t, db.AutoMigrate(&WebhookDelivery{}))
	t.Cleanup(func() { _ = db.Migrator().DropTable(&WebhookDelivery{}) })

	// Stand in for ensurePerformanceIndexes: the indexes exist, built outside
	// this migration.
	for _, index := range webhookDeliveryFilterIndexNames {
		if !db.Migrator().HasIndex(&WebhookDelivery{}, index) {
			require.NoError(t, db.Migrator().CreateIndex(&WebhookDelivery{}, index))
		}
		require.True(t, pgIndexExists(t, db, "webhook_deliveries", index), "precondition: %s should exist", index)
	}

	require.NoError(t, migrationAddWebhookDeliveryFilterIndexes(ctx, db, testLogger{}))
	require.NoError(t, webhookDeliveryFilterIndexMigration(ctx, db, testLogger{}).RollbackLast())

	for _, index := range webhookDeliveryFilterIndexNames {
		require.True(t, pgIndexExists(t, db, "webhook_deliveries", index),
			"rollback must leave %s alone on Postgres — it belongs to ensurePerformanceIndexes", index)
	}
}

// The exemption is Postgres-only: on the dialects the migration does create
// these indexes on, the rollback must still remove them.
func TestWebhookDeliveryFilterIndexRollbackDropsOnSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "rollback.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, db.AutoMigrate(&WebhookDelivery{}))
	require.NoError(t, migrationAddWebhookDeliveryFilterIndexes(ctx, db, testLogger{}))
	for _, index := range webhookDeliveryFilterIndexNames {
		require.True(t, db.Migrator().HasIndex(&WebhookDelivery{}, index), "precondition: %s should exist", index)
	}

	require.NoError(t, webhookDeliveryFilterIndexMigration(ctx, db, testLogger{}).RollbackLast())
	for _, index := range webhookDeliveryFilterIndexNames {
		require.False(t, db.Migrator().HasIndex(&WebhookDelivery{}, index), "rollback should drop %s on SQLite", index)
	}
}
