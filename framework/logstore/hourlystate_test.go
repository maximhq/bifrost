package logstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// hourlyTestDB creates an isolated schema without dropping another test's relations.
func hourlyTestDB(t *testing.T) (*gorm.DB, *sql.DB) {
	t.Helper()
	admin := trySetupPostgresDB(t)
	if admin == nil {
		t.Skip("PostgreSQL unavailable")
	}
	schema := "hourly_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+hourlyIdent(schema)).Error)
	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+schema, 1)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		require.NoError(t, admin.Exec("DROP SCHEMA "+hourlyIdent(schema)+" CASCADE").Error)
	})
	require.NoError(t, db.AutoMigrate(&Log{}))
	return db, sqlDB
}

// TestHourlyStateTransactionalTracking verifies invalidation, rollback, and freezing.
func TestHourlyStateTransactionalTracking(t *testing.T) {
	db, sqlDB := hourlyTestDB(t)
	ctx := context.Background()
	tx, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, ensureHourlyState(ctx, tx))
	require.NoError(t, tx.Commit())
	stamp := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)
	insert := `INSERT INTO logs (id,timestamp,status,provider,model,object_type,created_at) VALUES (?,?,'success','openai','test','chat_completion',now())`
	require.NoError(t, db.Exec(insert, "a", stamp).Error)
	var generation int64
	require.NoError(t, db.Raw(`SELECT generation FROM bifrost_hourly_hours`).Scan(&generation).Error)
	require.EqualValues(t, 1, generation)
	// A payload-only update is not an aggregate change.
	require.NoError(t, db.Exec(`UPDATE logs SET input_history='[]' WHERE id='a'`).Error)
	require.NoError(t, db.Raw(`SELECT generation FROM bifrost_hourly_hours`).Scan(&generation).Error)
	require.EqualValues(t, 1, generation)
	// Rollbacks do not leave phantom invalidations.
	tx, err = sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE logs SET cost=2 WHERE id='a'`)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, db.Raw(`SELECT generation FROM bifrost_hourly_hours`).Scan(&generation).Error)
	require.EqualValues(t, 1, generation)
	require.NoError(t, db.Exec(`UPDATE logs SET timestamp=?,cost=2 WHERE id='a'`, stamp.Add(-3*time.Hour)).Error)
	var hours int64
	require.NoError(t, db.Table("bifrost_hourly_hours").Count(&hours).Error)
	require.EqualValues(t, 2, hours)
	require.NoError(t, db.Exec(`UPDATE bifrost_hourly_hours SET frozen=true`).Error)
	require.NoError(t, db.Exec(`DELETE FROM logs WHERE id='a'`).Error)
	var ignored int64
	require.NoError(t, db.Raw(`SELECT SUM(ignored_changes) FROM bifrost_hourly_hours`).Scan(&ignored).Error)
	require.EqualValues(t, 1, ignored)
}

// TestHourlyStatePinnedTimezone checks that connection timezones cannot move buckets.
func TestHourlyStatePinnedTimezone(t *testing.T) {
	_, sqlDB := hourlyTestDB(t)
	ctx := context.Background()
	tx, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.Exec(`SET LOCAL TIME ZONE 'Asia/Kolkata'`)
	require.NoError(t, err)
	require.NoError(t, ensureHourlyState(ctx, tx))
	require.NoError(t, tx.Commit())
	var bucket time.Time
	require.NoError(t, sqlDB.QueryRow(`SELECT bifrost_hourly_bucket('2026-09-19 10:45:00+00'::timestamptz)`).Scan(&bucket))
	require.Equal(t, "2026-09-19T10:30:00Z", bucket.UTC().Format(time.RFC3339))
}
