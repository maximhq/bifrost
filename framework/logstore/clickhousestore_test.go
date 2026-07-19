package logstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormschema "gorm.io/gorm/schema"
)

// ClickHouse test connection matches the clickhouse service in
// framework/docker-compose.yml (native protocol on host port 9001; host 9000
// is taken by Weaviate).
const (
	clickhouseTestHost     = "localhost"
	clickhouseTestPort     = "9001"
	clickhouseTestDatabase = "bifrost"
	clickhouseTestUser     = "bifrost"
	clickhouseTestPassword = "bifrost_password"
)

func clickhouseTestConfig() *ClickHouseConfig {
	return &ClickHouseConfig{
		Host:     schemas.NewSecretVar(clickhouseTestHost),
		Port:     schemas.NewSecretVar(clickhouseTestPort),
		Database: schemas.NewSecretVar(clickhouseTestDatabase),
		Username: schemas.NewSecretVar(clickhouseTestUser),
		Password: schemas.NewSecretVar(clickhouseTestPassword),
	}
}

// trySetupClickHouseStore connects to the docker-compose ClickHouse, runs
// migrations, and truncates the log tables for a clean slate. Skips the test
// when ClickHouse is unavailable.
func trySetupClickHouseStore(t *testing.T) *ClickHouseLogStore {
	t.Helper()
	ctx := context.Background()
	store, err := newClickHouseLogStore(ctx, clickhouseTestConfig(), 0, testLogger{})
	if err != nil {
		t.Skipf("ClickHouse not available, skipping test: %v", err)
	}
	ch := store.(*ClickHouseLogStore)
	for _, table := range []string{"logs", "mcp_tool_logs", "async_jobs", "webhook_deliveries"} {
		require.NoError(t, ch.db.Exec("TRUNCATE TABLE "+table).Error)
	}
	t.Cleanup(func() { _ = ch.Close(context.Background()) })
	return ch
}

func chTestLog(id string, ts time.Time) *Log {
	return &Log{
		ID:        id,
		Timestamp: ts,
		Object:    "chat.completion",
		Provider:  "openai",
		Model:     "gpt-4o",
		Status:    "processing",
		CreatedAt: ts,
	}
}

type clickHouseExtensionTestRow struct {
	ID        string
	Value     string
	CreatedAt time.Time
}

func (clickHouseExtensionTestRow) TableName() string { return "extension_test_events" }

func TestClickHouseEnsureExtensionTable(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	require.NoError(t, store.db.Exec("DROP TABLE IF EXISTS extension_test_events").Error)
	t.Cleanup(func() {
		_ = store.db.Exec("DROP TABLE IF EXISTS extension_test_events").Error
	})

	table := "extension_test_events"
	partitionBy := "toYYYYMM(created_at)"
	orderBy := "(created_at, id)"
	ttl := "toDateTime(created_at) + INTERVAL 30 DAY"
	skipIndexes := []string{"INDEX idx_extension_value lower(value) TYPE bloom_filter(0.01) GRANULARITY 1"}
	require.NoError(t, store.EnsureClickHouseTable(ctx, &clickHouseExtensionTestRow{}, table, partitionBy, orderBy, ttl, skipIndexes))
	require.NoError(t, (&HybridLogStore{inner: store}).EnsureClickHouseTable(ctx, &clickHouseExtensionTestRow{}, table, partitionBy, orderBy, ttl, skipIndexes))

	row := clickHouseExtensionTestRow{ID: "event-1", Value: "matched", CreatedAt: time.Now().UTC()}
	require.NoError(t, store.db.WithContext(ctx).Create(&row).Error)

	var count int64
	require.NoError(t, store.db.WithContext(ctx).Model(&clickHouseExtensionTestRow{}).Where("value = ?", "matched").Count(&count).Error)
	assert.Equal(t, int64(1), count)

	var createQuery string
	require.NoError(t, store.db.WithContext(ctx).
		Raw("SELECT create_table_query FROM system.tables WHERE database = currentDatabase() AND name = ?", table).
		Scan(&createQuery).Error)
	assert.Contains(t, createQuery, "ReplacingMergeTree")
	assert.Contains(t, createQuery, "TTL")
	assert.Contains(t, createQuery, "idx_extension_value")
}

// chCountRows counts logical rows visible for an id; with the connection-level
// final=1 setting, ReplacingMergeTree duplicates must collapse to one.
func chCountRows(t *testing.T, db *gorm.DB, table, id string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(fmt.Sprintf("SELECT count() FROM `%s` WHERE id = ?", table), id).Scan(&count).Error)
	return count
}

// --- Pure unit tests (no server required) ---

func TestBuildClickHouseDSN(t *testing.T) {
	t.Run("NativeDefaults", func(t *testing.T) {
		dsn, err := buildClickHouseDSN(&ClickHouseConfig{Host: schemas.NewSecretVar("ch.local")})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(dsn, "clickhouse://ch.local:9000/default?"), dsn)
		assert.Contains(t, dsn, "final=1")
		assert.Contains(t, dsn, "mutations_sync=1")
		assert.Contains(t, dsn, "prefer_column_name_to_alias=1")
		assert.Contains(t, dsn, "dial_timeout=10s")
		assert.NotContains(t, dsn, "secure=")
	})

	t.Run("NativeSecureUsesTLSPort", func(t *testing.T) {
		dsn, err := buildClickHouseDSN(&ClickHouseConfig{Host: schemas.NewSecretVar("ch.local"), Secure: true})
		require.NoError(t, err)
		assert.Contains(t, dsn, "ch.local:9440")
		assert.Contains(t, dsn, "secure=true")
	})

	t.Run("HTTPProtocol", func(t *testing.T) {
		dsn, err := buildClickHouseDSN(&ClickHouseConfig{Host: schemas.NewSecretVar("ch.local"), Protocol: "http"})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(dsn, "http://ch.local:8123/default?"), dsn)
	})

	t.Run("HTTPSecureUsesHTTPSScheme", func(t *testing.T) {
		dsn, err := buildClickHouseDSN(&ClickHouseConfig{Host: schemas.NewSecretVar("ch.local"), Protocol: "http", Secure: true})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(dsn, "https://ch.local:8443/default?"), dsn)
		assert.Contains(t, dsn, "secure=true")
	})

	t.Run("CredentialsPortAndDatabase", func(t *testing.T) {
		dsn, err := buildClickHouseDSN(clickhouseTestConfig())
		require.NoError(t, err)
		assert.Contains(t, dsn, "bifrost:bifrost_password@localhost:9001/bifrost")
	})

	t.Run("DialTimeoutMilliseconds", func(t *testing.T) {
		dsn, err := buildClickHouseDSN(&ClickHouseConfig{Host: schemas.NewSecretVar("ch.local"), DialTimeout: 2500})
		require.NoError(t, err)
		assert.Contains(t, dsn, "dial_timeout=2.5s")
	})

	t.Run("MissingHost", func(t *testing.T) {
		_, err := buildClickHouseDSN(&ClickHouseConfig{})
		require.Error(t, err)
	})

	t.Run("UnsupportedProtocol", func(t *testing.T) {
		_, err := buildClickHouseDSN(&ClickHouseConfig{Host: schemas.NewSecretVar("ch.local"), Protocol: "grpc"})
		require.Error(t, err)
	})
}

func TestChEscapeIdentifier(t *testing.T) {
	assert.Equal(t, "prod_cluster", chEscapeIdentifier("prod_cluster"))
	assert.Equal(t, "a``b", chEscapeIdentifier("a`b"))
	assert.Equal(t, "````", chEscapeIdentifier("``"))
}

// chUnitSchemaDB returns a gorm.DB usable for schema parsing without a live
// connection (chParseSchema only needs the naming strategy).
func chUnitSchemaDB() *gorm.DB {
	return &gorm.DB{Config: &gorm.Config{NamingStrategy: gormschema.NamingStrategy{}}}
}

func TestChApplyUpdateMapSkipsDedupKeys(t *testing.T) {
	ctx := context.Background()
	st, err := chParseSchema(chUnitSchemaDB(), &Log{})
	require.NoError(t, err)

	ts := time.Now().UTC().Truncate(time.Millisecond)
	row := *chTestLog("log-1", ts)
	dest := reflect.ValueOf(&row).Elem()

	err = chApplyUpdateMap(ctx, st, dest, map[string]interface{}{
		"status":    "success",
		"id":        "hijacked",
		"timestamp": ts.Add(time.Hour),
		"cost":      0.42,
	})
	require.NoError(t, err)

	assert.Equal(t, "success", row.Status)
	require.NotNil(t, row.Cost)
	assert.Equal(t, 0.42, *row.Cost)
	// Dedup key columns must survive untouched.
	assert.Equal(t, "log-1", row.ID)
	assert.Equal(t, ts, row.Timestamp)
}

func TestChApplyStructUpdateSkipsDedupKeys(t *testing.T) {
	ctx := context.Background()
	st, err := chParseSchema(chUnitSchemaDB(), &Log{})
	require.NoError(t, err)

	ts := time.Now().UTC().Truncate(time.Millisecond)
	row := *chTestLog("log-1", ts)
	dest := reflect.ValueOf(&row).Elem()

	update := Log{ID: "hijacked", Timestamp: ts.Add(time.Hour), Status: "error", Model: "gpt-4o-mini"}
	require.NoError(t, chApplyStructUpdate(ctx, st, dest, reflect.ValueOf(&update).Elem()))

	assert.Equal(t, "error", row.Status)
	assert.Equal(t, "gpt-4o-mini", row.Model)
	assert.Equal(t, "log-1", row.ID)
	assert.Equal(t, ts, row.Timestamp)
	// Zero-valued fields in the update struct must not clobber existing values.
	assert.Equal(t, "openai", row.Provider)
}

// --- Integration tests (require docker-compose clickhouse) ---

func TestClickHouseCreateAndFind(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-create-1", ts)))

	found, err := store.FindByID(ctx, "ch-create-1")
	require.NoError(t, err)
	assert.Equal(t, "openai", found.Provider)
	assert.Equal(t, "gpt-4o", found.Model)
	assert.Equal(t, "processing", found.Status)

	present, err := store.IsLogEntryPresent(ctx, "ch-create-1")
	require.NoError(t, err)
	assert.True(t, present)

	_, err = store.FindByID(ctx, "does-not-exist")
	assert.ErrorIs(t, err, ErrNotFound)

	hasLogs, err := store.HasLogs(ctx)
	require.NoError(t, err)
	assert.True(t, hasLogs)

	require.NoError(t, store.Ping(ctx))
}

func TestClickHouseIdempotentCreate(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	entry := chTestLog("ch-idem-1", ts)
	require.NoError(t, store.CreateIfNotExists(ctx, entry))
	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-idem-1", ts)))

	// final=1 must collapse the duplicate inserts into a single logical row.
	assert.Equal(t, int64(1), chCountRows(t, store.db, "logs", "ch-idem-1"))
}

func TestClickHouseCreateIfNotExistsKeepsExistingRow(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-keep-1", ts)))
	require.NoError(t, store.Update(ctx, "ch-keep-1", map[string]interface{}{
		"status":     "success",
		"has_object": true,
	}))

	// A retried insert of the initial "processing" entry must be a no-op:
	// ReplacingMergeTree alone would keep the newest ver and resurrect the
	// stale row, dropping status and has_object.
	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-keep-1", ts)))
	found, err := store.FindByID(ctx, "ch-keep-1")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	assert.True(t, found.HasObject)

	// Batch variant: existing id skipped, new id inserted.
	require.NoError(t, store.BatchCreateIfNotExists(ctx, []*Log{
		chTestLog("ch-keep-1", ts),
		chTestLog("ch-keep-2", ts.Add(time.Millisecond)),
	}))
	found, err = store.FindByID(ctx, "ch-keep-1")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	assert.True(t, found.HasObject)
	_, err = store.FindByID(ctx, "ch-keep-2")
	require.NoError(t, err)

	// MCP variant.
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, []*MCPToolLog{chTestMCPToolLog("ch-keep-mcp-1", ts)}))
	require.NoError(t, store.UpdateMCPToolLog(ctx, "ch-keep-mcp-1", map[string]interface{}{"status": "success", "has_object": true}))
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, []*MCPToolLog{chTestMCPToolLog("ch-keep-mcp-1", ts)}))
	foundMCP, err := store.FindMCPToolLog(ctx, "ch-keep-mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "success", foundMCP.Status)
	assert.True(t, foundMCP.HasObject)
}

func TestClickHouseBatchCreate(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	entries := []*Log{
		chTestLog("ch-batch-1", ts),
		chTestLog("ch-batch-2", ts.Add(time.Millisecond)),
		chTestLog("ch-batch-3", ts.Add(2*time.Millisecond)),
	}
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))
	require.NoError(t, store.BatchCreateIfNotExists(ctx, nil)) // no-op

	for _, id := range []string{"ch-batch-1", "ch-batch-2", "ch-batch-3"} {
		_, err := store.FindByID(ctx, id)
		require.NoError(t, err)
	}
}

func TestClickHouseUpdateWithMap(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-upd-map", ts)))
	require.NoError(t, store.Update(ctx, "ch-upd-map", map[string]interface{}{
		"status": "success",
		"cost":   1.25,
	}))

	found, err := store.FindByID(ctx, "ch-upd-map")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	require.NotNil(t, found.Cost)
	assert.Equal(t, 1.25, *found.Cost)
	// Untouched columns must survive the re-insert.
	assert.Equal(t, "openai", found.Provider)
	assert.Equal(t, "gpt-4o", found.Model)
	assert.Equal(t, int64(1), chCountRows(t, store.db, "logs", "ch-upd-map"))

	assert.ErrorIs(t, store.Update(ctx, "missing-id", map[string]interface{}{"status": "success"}), ErrNotFound)
}

func TestClickHouseUpdateWithStruct(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-upd-struct", ts)))

	latency := 123.5
	require.NoError(t, store.Update(ctx, "ch-upd-struct", &Log{Status: "success", Latency: &latency}))

	found, err := store.FindByID(ctx, "ch-upd-struct")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	require.NotNil(t, found.Latency)
	assert.Equal(t, 123.5, *found.Latency)
	assert.Equal(t, "gpt-4o", found.Model)
}

func TestClickHouseUpdateCannotRewriteDedupKey(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-upd-key", ts)))

	// An update that tries to move the dedup key must not fork a second
	// logical row (the table ORDER BY is (timestamp, id)).
	require.NoError(t, store.Update(ctx, "ch-upd-key", map[string]interface{}{
		"timestamp": ts.Add(time.Hour),
		"id":        "ch-upd-key-forged",
		"status":    "success",
	}))

	assert.Equal(t, int64(1), chCountRows(t, store.db, "logs", "ch-upd-key"))
	assert.Equal(t, int64(0), chCountRows(t, store.db, "logs", "ch-upd-key-forged"))

	found, err := store.FindByID(ctx, "ch-upd-key")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	assert.Equal(t, ts.UnixMilli(), found.Timestamp.UnixMilli())
}

func TestClickHouseConcurrentUpdatesPreserveBothPatches(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()

	// The object-offload path (has_object) racing the completion path
	// (status/cost) is the exact lost-update scenario the per-id RMW locks
	// exist for; without them one patch silently vanishes.
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("ch-race-%d", i)
		ts := time.Now().UTC().Truncate(time.Millisecond)
		require.NoError(t, store.CreateIfNotExists(ctx, chTestLog(id, ts)))

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = store.Update(ctx, id, map[string]interface{}{"status": "success", "cost": 0.5})
		}()
		go func() {
			defer wg.Done()
			errs[1] = store.Update(ctx, id, map[string]interface{}{"has_object": true})
		}()
		wg.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])

		found, err := store.FindByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "success", found.Status, "status patch lost for %s", id)
		assert.True(t, found.HasObject, "has_object patch lost for %s", id)
	}
}

func TestClickHouseBulkUpdateCost(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	updates := map[string]float64{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("ch-cost-%d", i)
		require.NoError(t, store.CreateIfNotExists(ctx, chTestLog(id, ts.Add(time.Duration(i)*time.Millisecond))))
		updates[id] = float64(i) * 0.1
	}
	// Unknown ids must be ignored, not error.
	updates["ch-cost-missing"] = 9.9

	require.NoError(t, store.BulkUpdateCost(ctx, updates))
	require.NoError(t, store.BulkUpdateCost(ctx, nil)) // no-op

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("ch-cost-%d", i)
		found, err := store.FindByID(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, found.Cost, "cost missing for %s", id)
		assert.InDelta(t, float64(i)*0.1, *found.Cost, 1e-9)
		assert.Equal(t, int64(1), chCountRows(t, store.db, "logs", id))
	}
}

func TestClickHouseSearchAndStats(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	for i := 0; i < 3; i++ {
		entry := chTestLog(fmt.Sprintf("ch-search-%d", i), ts.Add(time.Duration(i)*time.Second))
		entry.Status = "success"
		if i == 2 {
			entry.Provider = "anthropic"
			entry.Model = "claude-sonnet-4-5"
		}
		require.NoError(t, store.CreateIfNotExists(ctx, entry))
	}

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Logs, 3)

	filtered, err := store.SearchLogs(ctx, SearchFilters{Providers: []string{"anthropic"}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, filtered.Logs, 1)

	stats, err := store.GetStats(ctx, SearchFilters{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.TotalRequests)

	models, err := store.GetDistinctModels(ctx, 10, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gpt-4o", "claude-sonnet-4-5"}, models)
}

func TestClickHouseDeleteLogs(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	for _, id := range []string{"ch-del-1", "ch-del-2", "ch-del-3"} {
		require.NoError(t, store.CreateIfNotExists(ctx, chTestLog(id, ts)))
	}

	require.NoError(t, store.DeleteLog(ctx, "ch-del-1"))
	require.NoError(t, store.DeleteLogs(ctx, []string{"ch-del-2", "ch-del-3"}))

	for _, id := range []string{"ch-del-1", "ch-del-2", "ch-del-3"} {
		_, err := store.FindByID(ctx, id)
		assert.ErrorIs(t, err, ErrNotFound, "log %s should be deleted", id)
	}
}

func TestClickHouseDeleteLogsBatch(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Millisecond)
	fresh := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-old", old)))
	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-fresh", fresh)))

	deleted, err := store.DeleteLogsBatch(ctx, time.Now().UTC().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted, "cleaner pacing relies on an accurate deleted count")

	_, err = store.FindByID(ctx, "ch-old")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.FindByID(ctx, "ch-fresh")
	assert.NoError(t, err)
}

func chTestMCPToolLog(id string, ts time.Time) *MCPToolLog {
	return &MCPToolLog{
		ID:        id,
		Timestamp: ts,
		ToolName:  "search_web",
		Status:    "processing",
		CreatedAt: ts,
	}
}

func TestClickHouseMCPToolLogs(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	entries := []*MCPToolLog{
		chTestMCPToolLog("ch-mcp-1", ts),
		chTestMCPToolLog("ch-mcp-2", ts.Add(time.Millisecond)),
	}
	entries[0].RedactionMapping = `plain:{"input":{"EMAIL-1":"private@example.com"}}`
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, entries))
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, nil)) // no-op

	found, err := store.FindMCPToolLog(ctx, "ch-mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "search_web", found.ToolName)
	assert.Equal(t, entries[0].RedactionMapping, found.RedactionMapping)

	// Map update.
	latency := 42.0
	require.NoError(t, store.UpdateMCPToolLog(ctx, "ch-mcp-1", map[string]interface{}{
		"status":            "success",
		"latency":           latency,
		"redaction_mapping": `plain:{"output":{"EMAIL-2":"result@example.com"}}`,
	}))
	found, err = store.FindMCPToolLog(ctx, "ch-mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	require.NotNil(t, found.Latency)
	assert.Equal(t, 42.0, *found.Latency)
	assert.Contains(t, found.RedactionMapping, "result@example.com")
	assert.Equal(t, int64(1), chCountRows(t, store.db, "mcp_tool_logs", "ch-mcp-1"))

	// Struct update preserves untouched fields and the dedup key.
	require.NoError(t, store.UpdateMCPToolLog(ctx, "ch-mcp-2", &MCPToolLog{Status: "error", Timestamp: ts.Add(time.Hour)}))
	found, err = store.FindMCPToolLog(ctx, "ch-mcp-2")
	require.NoError(t, err)
	assert.Equal(t, "error", found.Status)
	assert.Equal(t, "search_web", found.ToolName)
	assert.Equal(t, ts.Add(time.Millisecond).UnixMilli(), found.Timestamp.UnixMilli())
	assert.Equal(t, int64(1), chCountRows(t, store.db, "mcp_tool_logs", "ch-mcp-2"))

	assert.ErrorIs(t, store.UpdateMCPToolLog(ctx, "missing-id", map[string]interface{}{"status": "success"}), ErrNotFound)

	hasLogs, err := store.HasMCPToolLogs(ctx)
	require.NoError(t, err)
	assert.True(t, hasLogs)

	result, err := store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Logs, 2)
}

// TestClickHouseHybridHasObjectSurvivesDuplicateCreate exercises the full
// HybridLogStore-over-ClickHouse flow that hybrid mode depends on: create a
// payload-bearing entry, let the async upload worker flip has_object, apply
// the completion update, then retry the initial create. The completed status,
// the has_object flag, and payload hydration must all survive the retry.
func TestClickHouseHybridHasObjectSurvivesDuplicateCreate(t *testing.T) {
	ch := trySetupClickHouseStore(t)
	objStore := objectstore.NewInMemoryObjectStore()
	hybrid := newHybridLogStore(ch, objStore, "test", hybridTestLogger{}, nil)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	input := "hello from clickhouse hybrid"
	mkEntry := func() *Log {
		entry := chTestLog("ch-hybrid-1", ts)
		entry.InputHistoryParsed = []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}},
		}
		return entry
	}

	require.NoError(t, hybrid.CreateIfNotExists(ctx, mkEntry()))

	// The upload worker sets has_object asynchronously after the S3 put.
	waitForUploads(t, func() bool {
		log, err := ch.FindByID(ctx, "ch-hybrid-1")
		return err == nil && log.HasObject
	})

	// Completion update from the logging plugin's write path.
	require.NoError(t, hybrid.Update(ctx, "ch-hybrid-1", map[string]interface{}{"status": "success"}))

	// Duplicate create retry must not resurrect the stale processing row.
	require.NoError(t, hybrid.CreateIfNotExists(ctx, mkEntry()))

	found, err := hybrid.FindByID(ctx, "ch-hybrid-1")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	assert.True(t, found.HasObject)
	assert.NotEmpty(t, found.InputHistory, "payload should hydrate from the object store")
	assert.Contains(t, found.ContentSummary, input)

	require.NoError(t, hybrid.Close(ctx))
}

// TestClickHouseNodeUsageCursorDoesNotRewind guards the budget-usage gossip
// cursor against the GORM ClickHouse driver's seconds-truncation of time.Time
// args: a truncated cursor bound would rewind to the start of its second and
// double-count every row already aggregated on the previous scan.
func TestClickHouseNodeUsageCursorDoesNotRewind(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	// Sub-second offsets are the point of this test: rows land mid-second.
	base := time.Now().UTC().Truncate(time.Second).Add(288 * time.Millisecond)

	nodeID := "node-1"
	budgetIDs := `["b1"]`
	mk := func(id string, ts time.Time, cost float64) *Log {
		l := chTestLog(id, ts)
		l.Status = "success"
		l.ClusterNodeID = &nodeID
		l.BudgetIDs = &budgetIDs
		l.Cost = &cost
		return l
	}
	require.NoError(t, store.CreateIfNotExists(ctx, mk("ch-usage-1", base, 1.0)))
	require.NoError(t, store.CreateIfNotExists(ctx, mk("ch-usage-2", base.Add(200*time.Millisecond), 2.0)))

	first, err := store.GetNodeUsageAfter(ctx, nodeID, NodeUsageCursor{Timestamp: base.Add(-time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, 2, first.RowCount)
	assert.InDelta(t, 3.0, first.BudgetCosts["b1"], 1e-9)

	// Re-scan from the advanced cursor: nothing new, so nothing may be
	// re-aggregated - a rewound (seconds-truncated) cursor would return both
	// rows again and double-count the budget spend.
	second, err := store.GetNodeUsageAfter(ctx, nodeID, first.NextCursor)
	require.NoError(t, err)
	assert.Equal(t, 0, second.RowCount, "cursor must not rewind into already-counted rows")
	assert.Empty(t, second.BudgetCosts)
}

func TestClickHouseAsyncJobs(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	job := &AsyncJob{
		ID:          "ch-job-1",
		Status:      schemas.AsyncJobStatusProcessing,
		RequestType: schemas.ChatCompletionRequest,
		CreatedAt:   now,
	}
	require.NoError(t, store.CreateAsyncJob(ctx, job))

	found, err := store.FindAsyncJobByID(ctx, "ch-job-1")
	require.NoError(t, err)
	assert.Equal(t, schemas.AsyncJobStatusProcessing, found.Status)

	completedAt := now.Add(time.Second)
	require.NoError(t, store.UpdateAsyncJob(ctx, "ch-job-1", map[string]interface{}{
		"status":       string(schemas.AsyncJobStatusCompleted),
		"response":     `{"ok":true}`,
		"completed_at": completedAt,
	}))
	found, err = store.FindAsyncJobByID(ctx, "ch-job-1")
	require.NoError(t, err)
	assert.Equal(t, schemas.AsyncJobStatusCompleted, found.Status)
	assert.Equal(t, `{"ok":true}`, found.Response)
	assert.Equal(t, int64(1), chCountRows(t, store.db, "async_jobs", "ch-job-1"))

	// Expired job cleanup.
	expiredAt := now.Add(-time.Hour)
	expired := &AsyncJob{
		ID:          "ch-job-expired",
		Status:      schemas.AsyncJobStatusCompleted,
		RequestType: schemas.ChatCompletionRequest,
		ExpiresAt:   &expiredAt,
		CreatedAt:   now.Add(-2 * time.Hour),
	}
	require.NoError(t, store.CreateAsyncJob(ctx, expired))
	_, err = store.DeleteExpiredAsyncJobs(ctx)
	require.NoError(t, err)
	_, err = store.FindAsyncJobByID(ctx, "ch-job-expired")
	assert.Error(t, err, "expired job should be deleted")

	// Stale processing job cleanup.
	stale := &AsyncJob{
		ID:          "ch-job-stale",
		Status:      schemas.AsyncJobStatusProcessing,
		RequestType: schemas.ChatCompletionRequest,
		CreatedAt:   now.Add(-48 * time.Hour),
	}
	require.NoError(t, store.CreateAsyncJob(ctx, stale))
	_, err = store.DeleteStaleAsyncJobs(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	_, err = store.FindAsyncJobByID(ctx, "ch-job-stale")
	assert.Error(t, err, "stale processing job should be deleted")

	// The completed job must survive both cleanups.
	_, err = store.FindAsyncJobByID(ctx, "ch-job-1")
	assert.NoError(t, err)
}

func TestClickHouseHistograms(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	for i := 0; i < 4; i++ {
		entry := chTestLog(fmt.Sprintf("ch-hist-%d", i), ts.Add(time.Duration(i)*time.Second))
		entry.Status = "success"
		cost := 0.25
		entry.Cost = &cost
		entry.TotalTokens = 100
		entry.PromptTokens = 60
		entry.CompletionTokens = 40
		require.NoError(t, store.CreateIfNotExists(ctx, entry))
	}

	hist, err := store.GetHistogram(ctx, SearchFilters{}, 60)
	require.NoError(t, err)
	require.NotNil(t, hist)

	costHist, err := store.GetCostHistogram(ctx, SearchFilters{}, 60)
	require.NoError(t, err)
	require.NotNil(t, costHist)

	tokenHist, err := store.GetTokenHistogram(ctx, SearchFilters{}, 60)
	require.NoError(t, err)
	require.NotNil(t, tokenHist)

	modelRankings, err := store.GetModelRankings(ctx, SearchFilters{})
	require.NoError(t, err)
	require.NotNil(t, modelRankings)
}
