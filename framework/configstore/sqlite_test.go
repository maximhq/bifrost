package configstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A write transaction that reads before it writes must wait for a concurrent writer rather than
// fail. SQLite upgrades a deferred transaction's read lock to the write lock without consulting the
// busy timeout when another connection holds it, so the upgrade fails at once with "database is
// locked". The startup pricing sync holds the write lock for seconds on a fresh store, which is how
// UpdateClientMetadata failed right after LoadConfig. Write transactions therefore begin IMMEDIATE.
func TestSQLiteWriteTransactionWaitsForAConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteConfigStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	rdb := store.(*RDBConfigStore)
	t.Cleanup(func() { _ = rdb.Close(ctx) })
	require.NoError(t, store.UpdateClientConfig(ctx, &ClientConfig{LogRetentionDays: 30}))

	holding := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		released <- rdb.DB().Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&tables.TableClientConfig{}).Where("1 = 1").Update("log_retention_days", 7).Error; err != nil {
				return err
			}
			close(holding)
			time.Sleep(300 * time.Millisecond)
			return nil
		})
	}()
	<-holding

	start := time.Now()
	require.NoError(t, store.UpdateClientMetadata(ctx, map[string]any{"onboarding_dismissed": true}), "the metadata write waits for the other writer instead of failing")
	require.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "the metadata write waited for the lock")
	require.NoError(t, <-released)

	metadata, err := store.GetClientMetadata(ctx)
	require.NoError(t, err)
	require.Equal(t, true, metadata["onboarding_dismissed"])
	cfg, err := store.GetClientConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 7, cfg.LogRetentionDays, "the other writer's change is kept")
}
