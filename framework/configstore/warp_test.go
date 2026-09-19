package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestWarpConfigStoreLifecycle(t *testing.T) {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableWarpConfig{}))
	ctx := context.Background()

	// A deployment that never configured Warp is the common case, and it must
	// read back as "nothing here" rather than an error the caller has to
	// special-case at every call site.
	config, err := store.GetWarpConfig(ctx)
	require.NoError(t, err)
	require.Nil(t, config)

	require.NoError(t, store.UpsertWarpConfig(ctx, &tables.TableWarpConfig{
		Enabled:       true,
		Provider:      "openai",
		Model:         "gpt-4o",
		APIKeyID:      "key-abc",
		MaxIterations: 6,
	}))

	config, err = store.GetWarpConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, config)
	require.Equal(t, tables.WarpConfigRowID, config.ID)
	require.True(t, config.Enabled)
	require.Equal(t, "gpt-4o", config.Model)
	require.Equal(t, 6, config.MaxIterations)
	require.Equal(t, "key-abc", config.APIKeyID)

	// The table is a singleton by contract. A second write must overwrite rather
	// than insert: an autoincremented second row would be invisible to
	// GetWarpConfig, so the operator's change would appear to save and then have
	// no effect.
	require.NoError(t, store.UpsertWarpConfig(ctx, &tables.TableWarpConfig{
		Enabled:  false,
		Provider: "anthropic",
		Model:    "claude-sonnet-5",
	}))

	var count int64
	require.NoError(t, store.DB().Model(&tables.TableWarpConfig{}).Count(&count).Error)
	require.Equal(t, int64(1), count)

	config, err = store.GetWarpConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, config)
	require.False(t, config.Enabled)
	require.Equal(t, "anthropic", config.Provider)
}

// A caller that leaves ID unset must still land on the singleton row rather
// than creating a second one.
func TestWarpConfigUpsertPinsSingletonID(t *testing.T) {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableWarpConfig{}))
	ctx := context.Background()

	config := &tables.TableWarpConfig{Provider: "openai", Model: "gpt-4o"}
	require.NoError(t, store.UpsertWarpConfig(ctx, config))
	require.Equal(t, tables.WarpConfigRowID, config.ID)

	stored, err := store.GetWarpConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "gpt-4o", stored.Model)
}

// The 500 this reproduces: a database that created warp_config before the table
// was reshaped keeps the old columns, because applied migration IDs are never
// re-run. The follow-up migration has to add api_key_id and retire the columns
// that held a credential, or every save fails on a column that does not exist.
func TestWarpConfigMigrationReshapesLegacyTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	ctx := context.Background()

	// Stand the table up as the first migration left it.
	require.NoError(t, db.Exec(`CREATE TABLE warp_config (
		id integer PRIMARY KEY,
		enabled numeric DEFAULT false,
		provider text,
		model text,
		base_url text,
		api_key text,
		max_iterations integer DEFAULT 0,
		request_timeout_seconds integer DEFAULT 0,
		system_prompt_suffix text,
		encryption_status text DEFAULT 'plain_text',
		created_at datetime NOT NULL,
		updated_at datetime NOT NULL
	)`).Error)
	require.True(t, db.Migrator().HasColumn(&tables.TableWarpConfig{}, "api_key"))
	require.False(t, db.Migrator().HasColumn(&tables.TableWarpConfig{}, "api_key_id"))

	require.NoError(t, migrationAddWarpAPIKeyIDColumn(ctx, db, testMigrationLogger))

	require.True(t, db.Migrator().HasColumn(&tables.TableWarpConfig{}, "api_key_id"))

	// The retired column held a credential, so what matters is that no value
	// survives - not that the column is gone. GORM's SQLite driver returns nil
	// from DropColumn without dropping anything, so asserting on the column's
	// absence would pass on Postgres and quietly leave a secret at rest here.
	if db.Migrator().HasColumn(&tables.TableWarpConfig{}, "api_key") {
		var remaining int64
		require.NoError(t, db.Table("warp_config").Where("api_key IS NOT NULL").Count(&remaining).Error)
		require.Zero(t, remaining, "a retired credential column must not keep its value")
	}

	// The write that used to 500.
	store := &RDBConfigStore{}
	store.db.Store(db)
	require.NoError(t, store.UpsertWarpConfig(ctx, &tables.TableWarpConfig{
		Enabled: true, Provider: "openai", Model: "gpt-4o", APIKeyID: "key-abc",
	}))
	config, err := store.GetWarpConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "key-abc", config.APIKeyID)
}
