package governance

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// pgEnvOr reads a Postgres test setting, falling back to the local dev default.
func pgEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// openDumpGuardPostgres opens local Postgres in a throwaway schema holding just
// the budget and rate limit tables, or skips when no server is reachable.
func openDumpGuardPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &postgresconn.Config{
		Host:     schemas.NewSecretVar(pgEnvOr("BIFROST_TEST_PG_HOST", "localhost")),
		Port:     schemas.NewSecretVar(pgEnvOr("BIFROST_TEST_PG_PORT", "5432")),
		User:     schemas.NewSecretVar(pgEnvOr("BIFROST_TEST_PG_USER", "bifrost")),
		Password: schemas.NewSecretVar(pgEnvOr("BIFROST_TEST_PG_PASSWORD", "bifrost_password")),
		DBName:   schemas.NewSecretVar(pgEnvOr("BIFROST_TEST_PG_DB", "bifrost")),
		SSLMode:  schemas.NewSecretVar("disable"),
	}
	schema := fmt.Sprintf("dumpguard_%d", time.Now().UnixNano())
	admin, err := postgresconn.Open(postgresconn.BuildDSN(cfg)+" connect_timeout=2", cfg, gormlogger.Discard)
	if err == nil {
		err = admin.Exec("SELECT 1").Error
	}
	if err != nil {
		t.Skipf("local Postgres unavailable: %v", err)
	}
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		postgresconn.Close(admin, nil)
	})
	db, err := postgresconn.Open(postgresconn.BuildDSN(cfg)+" search_path="+schema, cfg, gormlogger.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { postgresconn.Close(db, nil) })
	require.NoError(t, db.AutoMigrate(&configstoreTables.TableRateLimit{}, &configstoreTables.TableBudget{}))
	return db
}

// rowVersion returns a row's xmin, which changes whenever an UPDATE writes a
// new version of the row, even with identical values.
func rowVersion(t *testing.T, db *gorm.DB, table, id string) string {
	t.Helper()
	var xmin string
	require.NoError(t, db.Raw("SELECT xmin::text FROM "+table+" WHERE id = ?", id).Scan(&xmin).Error)
	require.NotEmpty(t, xmin)
	return xmin
}

// TestDumpBatchSQLSkipsUnchangedRowsOnPostgres pins the IS DISTINCT FROM guard:
// re-sending identical values (a full sweep, or a peer that already wrote them)
// must not create a new row version, while a changed value still lands.
func TestDumpBatchSQLSkipsUnchangedRowsOnPostgres(t *testing.T) {
	ctx := context.Background()
	db := openDumpGuardPostgres(t)
	gs := &LocalGovernanceStore{logger: NewMockLogger()}
	now := time.Now().UTC().Truncate(time.Microsecond)

	rl := buildRateLimitWithUsage("pg-rl", 1_000_000, 5, 1_000, 1)
	rl.TokenLastReset, rl.RequestLastReset = now, now
	require.NoError(t, db.Create(rl).Error)
	b := buildBudgetWithUsage("pg-b", 1000, 10, "24h")
	b.LastReset, b.CreatedAt, b.UpdatedAt = now, now, now
	require.NoError(t, db.Create(b).Error)

	rlRow := rateLimitDumpRow{ID: rl.ID, TokenCurrentUsage: 9, TokenLastReset: now, RequestCurrentUsage: 2, RequestLastReset: now}
	bRow := budgetDumpRow{ID: b.ID, CurrentUsage: 12.5, LastReset: now}

	require.NoError(t, gs.dumpRateLimitBatch(ctx, db, []rateLimitDumpRow{rlRow}))
	require.NoError(t, gs.writeBudgetBatch(ctx, db, []budgetDumpRow{bRow}, "<="))
	rlVersion := rowVersion(t, db, "governance_rate_limits", rl.ID)
	bVersion := rowVersion(t, db, "governance_budgets", b.ID)

	require.NoError(t, gs.dumpRateLimitBatch(ctx, db, []rateLimitDumpRow{rlRow}))
	require.NoError(t, gs.writeBudgetBatch(ctx, db, []budgetDumpRow{bRow}, "<="))
	require.Equal(t, rlVersion, rowVersion(t, db, "governance_rate_limits", rl.ID), "identical rate limit values must not write a new row version")
	require.Equal(t, bVersion, rowVersion(t, db, "governance_budgets", b.ID), "identical budget values must not write a new row version")

	rlRow.TokenCurrentUsage = 10
	bRow.CurrentUsage = 13
	require.NoError(t, gs.dumpRateLimitBatch(ctx, db, []rateLimitDumpRow{rlRow}))
	require.NoError(t, gs.writeBudgetBatch(ctx, db, []budgetDumpRow{bRow}, "<="))
	require.NotEqual(t, rlVersion, rowVersion(t, db, "governance_rate_limits", rl.ID))
	require.NotEqual(t, bVersion, rowVersion(t, db, "governance_budgets", b.ID))

	var gotRL configstoreTables.TableRateLimit
	require.NoError(t, db.First(&gotRL, "id = ?", rl.ID).Error)
	require.Equal(t, int64(10), gotRL.TokenCurrentUsage)
	require.Equal(t, int64(2), gotRL.RequestCurrentUsage)
	var gotB configstoreTables.TableBudget
	require.NoError(t, db.First(&gotB, "id = ?", b.ID).Error)
	require.Equal(t, 13.0, gotB.CurrentUsage)
	require.True(t, gotB.LastReset.Equal(now))

	// A newer persisted boundary still blocks a stale usage write (unchanged guard).
	stale := budgetDumpRow{ID: b.ID, CurrentUsage: 99, LastReset: now.Add(-time.Hour)}
	require.NoError(t, gs.writeBudgetBatch(ctx, db, []budgetDumpRow{stale}, "<="))
	require.NoError(t, db.First(&gotB, "id = ?", b.ID).Error)
	require.Equal(t, 13.0, gotB.CurrentUsage)
}
