package logstore

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func providerRequestIDTestLog(id, requestID string, timestamp time.Time) *Log {
	return &Log{
		ID:                      id,
		Timestamp:               timestamp,
		Object:                  "chat.completion",
		Provider:                "openai",
		Model:                   "gpt-test",
		Status:                  "success",
		ProviderRequestID:       requestID,
		ProviderRequestIDHeader: "x-request-id",
		ProviderRequestIDTrailParsed: []schemas.ProviderRequestIDRecord{{
			Attempt: 0, Provider: schemas.OpenAI, RequestID: requestID, HeaderName: "x-request-id",
		}},
	}
}

func TestProviderRequestIDTrailSerialization(t *testing.T) {
	entry := providerRequestIDTestLog("serialize", "req-serialize", time.Now())
	require.NoError(t, entry.SerializeFields())
	require.JSONEq(t, `[{"attempt":0,"provider":"openai","request_id":"req-serialize","header_name":"x-request-id"}]`, entry.ProviderRequestIDTrail)

	loaded := &Log{ProviderRequestIDTrail: entry.ProviderRequestIDTrail}
	require.NoError(t, loaded.DeserializeFields())
	require.Equal(t, entry.ProviderRequestIDTrailParsed, loaded.ProviderRequestIDTrailParsed)

	loaded.ProviderRequestIDTrail = `{invalid`
	loaded.ProviderRequestIDTrailParsed = entry.ProviderRequestIDTrailParsed
	require.NoError(t, loaded.DeserializeFields())
	require.Nil(t, loaded.ProviderRequestIDTrailParsed)
}

func TestProviderRequestIDOldLogEmptyFieldsRemainCompatible(t *testing.T) {
	entry := &Log{}
	require.NoError(t, entry.DeserializeFields())
	require.Empty(t, entry.ProviderRequestID)
	require.Empty(t, entry.ProviderRequestIDHeader)
	require.Empty(t, entry.ProviderRequestIDTrail)
	require.Nil(t, entry.ProviderRequestIDTrailParsed)
}

func TestProviderRequestIDExactFilterAndRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:provider_request_id?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelError)}
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.Create(ctx, providerRequestIDTestLog("match", "req-exact", now)))
	require.NoError(t, store.Create(ctx, providerRequestIDTestLog("other", "req-exact-suffix", now.Add(-time.Second))))

	result, err := store.SearchLogs(ctx, SearchFilters{ProviderRequestID: "req-exact"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "match", result.Logs[0].ID)
	require.Equal(t, "req-exact", result.Logs[0].ProviderRequestID)
	require.Equal(t, "x-request-id", result.Logs[0].ProviderRequestIDHeader)

	detail, err := store.FindByID(ctx, "match")
	require.NoError(t, err)
	require.Len(t, detail.ProviderRequestIDTrailParsed, 1)
	require.Equal(t, "req-exact", detail.ProviderRequestIDTrailParsed[0].RequestID)
}

func TestMigrationAddProviderRequestIDColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE logs (
		id varchar(255) primary key,
		timestamp datetime,
		object_type varchar(255),
		provider varchar(255),
		model varchar(255),
		status varchar(50),
		created_at datetime
	)`).Error)
	migrationLogger := bifrost.NewDefaultLogger(schemas.LogLevelError)

	for i := 0; i < 2; i++ {
		require.NoError(t, migrationAddProviderRequestIDColumns(context.Background(), db, migrationLogger))
		for _, column := range []string{"provider_request_id", "provider_request_id_header", "provider_request_id_trail"} {
			require.True(t, db.Migrator().HasColumn(&Log{}, column), "missing column %s", column)
		}
		require.True(t, db.Migrator().HasIndex(&Log{}, "idx_logs_provider_request_id"))
	}
}
