package configstore

import (
	"context"
	"encoding/json"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestProviderRequestIDConfigRedactionAndHash(t *testing.T) {
	config := &ProviderConfig{
		ProviderRequestID: &schemas.ProviderRequestIDConfig{Enabled: true, HeaderName: "x-request-id"},
	}
	redacted := config.Redacted()
	require.NotNil(t, redacted.ProviderRequestID)
	require.Equal(t, config.ProviderRequestID, redacted.ProviderRequestID)

	first, err := config.GenerateConfigHash("openai")
	require.NoError(t, err)
	config.ProviderRequestID.HeaderName = "x-trace-id"
	second, err := config.GenerateConfigHash("openai")
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}

func TestProviderRequestIDConfigRDBRoundTrip(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	config := ProviderConfig{
		ProviderRequestID: &schemas.ProviderRequestIDConfig{Enabled: true, HeaderName: "x-request-id"},
	}

	require.NoError(t, store.AddProvider(ctx, schemas.OpenAI, config))
	loaded, err := store.GetProviderConfig(ctx, schemas.OpenAI)
	require.NoError(t, err)
	require.Equal(t, config.ProviderRequestID, loaded.ProviderRequestID)
}

func TestProviderRequestIDConfigFileJSONToRDBRoundTrip(t *testing.T) {
	var config ProviderConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"provider_request_id": {
			"enabled": true,
			"header_name": " X-Trace-ID "
		}
	}`), &config))

	providerConfig := &schemas.ProviderConfig{ProviderRequestID: config.ProviderRequestID}
	require.NoError(t, schemas.NormalizeProviderRequestIDConfig(schemas.OpenAI, providerConfig))
	config.ProviderRequestID = providerConfig.ProviderRequestID

	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.AddProvider(ctx, schemas.OpenAI, config))
	loaded, err := store.GetProviderConfig(ctx, schemas.OpenAI)
	require.NoError(t, err)
	require.Equal(t, &schemas.ProviderRequestIDConfig{Enabled: true, HeaderName: "x-trace-id"}, loaded.ProviderRequestID)
}

func TestMigrationAddProviderRequestIDConfigColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE config_providers (id integer primary key, name text)`).Error)
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)

	for i := 0; i < 2; i++ {
		require.NoError(t, migrationAddProviderRequestIDConfigColumn(context.Background(), db, logger))
		require.True(t, db.Migrator().HasColumn(&tables.TableProvider{}, "provider_request_id_config_json"))
	}

	var count int64
	require.NoError(t, db.Table("migrations").Where("id = ?", "add_provider_request_id_config_column").Count(&count).Error)
	require.Equal(t, int64(1), count)
}
