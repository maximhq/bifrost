package lib

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadLogStoreForMaintenancePreservesRestartRequired(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logger := &testLogger{}
	SetLogger(logger)

	configStoreConfig := &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{
			Path: filepath.Join(dir, "config.db"),
		},
	}
	configStore, err := configstore.NewConfigStore(ctx, configStoreConfig, logger)
	require.NoError(t, err)
	require.NoError(t, configStore.SetRestartRequiredConfig(ctx, &configstoreTables.RestartRequiredConfig{
		Required: true,
		Reason:   "pending provider restart",
	}))
	require.NoError(t, configStore.UpdateLogsStoreConfig(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{
			Path: filepath.Join(dir, "logs.db"),
		},
	}))
	require.NoError(t, configStore.Close(ctx))

	logsConfig, logsStore, err := LoadLogStoreForMaintenance(ctx, dir, logger)
	require.NoError(t, err)
	require.NotNil(t, logsConfig)
	require.NotNil(t, logsStore)
	require.NoError(t, logsStore.Close(ctx))

	configStore, err = configstore.NewConfigStore(ctx, configStoreConfig, logger)
	require.NoError(t, err)
	defer configStore.Close(ctx)
	restart, err := configStore.GetRestartRequiredConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, restart)
	assert.True(t, restart.Required)
	assert.Equal(t, "pending provider restart", restart.Reason)
}
