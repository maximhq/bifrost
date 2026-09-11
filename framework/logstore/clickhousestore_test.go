package logstore

import (
	"context"
	"fmt"
	"os"
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

// ClickHouse test connection defaults match the clickhouse service in
// framework/docker-compose.yml (native protocol on host port 9001; host 9000
// is taken by Weaviate). Each value can be overridden through the
// BIFROST_TEST_CLICKHOUSE_* environment variables so the suite can target
// another local instance (for example one whose ports collide with 9001).
// The suite TRUNCATEs the log tables and rewrites their TTL, so the default
// "bifrost" database is accepted only for the stock docker-compose target:
// setting any override, even just the host or port, requires
// BIFROST_TEST_CLICKHOUSE_DB to name a database containing "test"
// (requireDedicatedClickHouseTestDB fails the test otherwise). For example:
//
//	BIFROST_TEST_CLICKHOUSE_PORT=9011 BIFROST_TEST_CLICKHOUSE_DB=bifrost_test
const (
	clickhouseTestHost     = "localhost"
	clickhouseTestPort     = "9001"
	clickhouseTestDatabase = "bifrost"
	clickhouseTestUser     = "bifrost"
	clickhouseTestPassword = "bifrost_password"
)

// chTestEnv returns the environment override for key, or def when unset.
func chTestEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// chTestOverridden reports whether any BIFROST_TEST_CLICKHOUSE_* override is
// set, meaning the suite is pointed away from the stock docker-compose target.
func chTestOverridden() bool {
	for _, k := range []string{"HOST", "PORT", "DB", "USER", "PASSWORD"} {
		if os.Getenv("BIFROST_TEST_CLICKHOUSE_"+k) != "" {
			return true
		}
	}
	return false
}

// chTestTargetIsDedicated reports whether the suite may run its destructive
// setup (TRUNCATE of every log table, TTL rewrites) against cfg. The stock
// docker-compose target is dedicated by definition; an overridden target is
// accepted only when its database name marks it as a test database.
func chTestTargetIsDedicated(cfg *ClickHouseConfig, overridden bool) bool {
	if !overridden {
		return true
	}
	return strings.Contains(strings.ToLower(cfg.Database.GetValue()), "test")
}

// requireDedicatedClickHouseTestDB fails the test before any connection is
// opened when the configured target is not safe to truncate.
func requireDedicatedClickHouseTestDB(t *testing.T, cfg *ClickHouseConfig) {
	t.Helper()
	if !chTestTargetIsDedicated(cfg, chTestOverridden()) {
		t.Fatalf("refusing to run destructive ClickHouse tests against database %q: BIFROST_TEST_CLICKHOUSE_* overrides must point at a database whose name contains \"test\" (the suite truncates logs, mcp_tool_logs, async_jobs and webhook_deliveries and rewrites their TTL)", cfg.Database.GetValue())
	}
}

func clickhouseTestConfig() *ClickHouseConfig {
	return &ClickHouseConfig{
		Host:     schemas.NewSecretVar(chTestEnv("BIFROST_TEST_CLICKHOUSE_HOST", clickhouseTestHost)),
		Port:     schemas.NewSecretVar(chTestEnv("BIFROST_TEST_CLICKHOUSE_PORT", clickhouseTestPort)),
		Database: schemas.NewSecretVar(chTestEnv("BIFROST_TEST_CLICKHOUSE_DB", clickhouseTestDatabase)),
		Username: schemas.NewSecretVar(chTestEnv("BIFROST_TEST_CLICKHOUSE_USER", clickhouseTestUser)),
		Password: schemas.NewSecretVar(chTestEnv("BIFROST_TEST_CLICKHOUSE_PASSWORD", clickhouseTestPassword)),
	}
}

// trySetupClickHouseStore connects to the docker-compose ClickHouse, runs
// migrations, and truncates the log tables for a clean slate. Skips the test
// when ClickHouse is unavailable locally; in CI (the CI env var is set by
// GitHub Actions and tests/docker-compose.yml provides the service) an
// unreachable ClickHouse is a failure, so the suite can never silently skip.
func trySetupClickHouseStore(t *testing.T) *ClickHouseLogStore {
	t.Helper()
	ctx := context.Background()
	cfg := clickhouseTestConfig()
	requireDedicatedClickHouseTestDB(t, cfg)
	store, err := newClickHouseLogStore(ctx, cfg, 0, testLogger{})
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("ClickHouse not available in CI (is the clickhouse service in tests/docker-compose.yml up?): %v", err)
		}
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
		// A literal config (not clickhouseTestConfig) so the assertion stays
		// deterministic when BIFROST_TEST_CLICKHOUSE_* overrides are set.
		dsn, err := buildClickHouseDSN(&ClickHouseConfig{
			Host:     schemas.NewSecretVar("localhost"),
			Port:     schemas.NewSecretVar("9001"),
			Database: schemas.NewSecretVar("bifrost"),
			Username: schemas.NewSecretVar("bifrost"),
			Password: schemas.NewSecretVar("bifrost_password"),
		})
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

func TestChTestTargetIsDedicated(t *testing.T) {
	cfg := func(db string) *ClickHouseConfig { return &ClickHouseConfig{Database: schemas.NewSecretVar(db)} }
	assert.True(t, chTestTargetIsDedicated(cfg("bifrost"), false), "stock compose target is always allowed")
	assert.True(t, chTestTargetIsDedicated(cfg("bifrost_test"), true))
	assert.True(t, chTestTargetIsDedicated(cfg("TestLogs"), true), "case-insensitive")
	assert.False(t, chTestTargetIsDedicated(cfg("bifrost"), true), "an override onto a non-test database is refused")
	assert.False(t, chTestTargetIsDedicated(cfg(""), true))

	t.Setenv("BIFROST_TEST_CLICKHOUSE_PORT", "9011")
	assert.True(t, chTestOverridden())
}

func TestChServerVersionSupported(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"26.6.1.1193", true},
		{"24.8.14.39", true},
		{"24.4.1.1", true},
		{"24.3.9.5", false},
		{"23.8.2.7", false},
		{" 25.1 ", true},
	} {
		ok, err := chServerVersionSupported(tc.version)
		require.NoError(t, err, tc.version)
		assert.Equal(t, tc.ok, ok, tc.version)
	}
	for _, bad := range []string{"", "26", "x.y.z", "24.four"} {
		_, err := chServerVersionSupported(bad)
		assert.Error(t, err, bad)
	}
}

func TestChTTLDays(t *testing.T) {
	t.Run("EngineFull", func(t *testing.T) {
		// Fixtures copied verbatim from system.tables.engine_full on ClickHouse 26.6.
		days, ok := chTTLDaysFromEngineFull("ReplacingMergeTree(ver) ORDER BY id TTL toDateTime(created_at) + toIntervalDay(7) SETTINGS index_granularity = 8192")
		assert.True(t, ok)
		assert.Equal(t, 7, days)

		days, ok = chTTLDaysFromEngineFull("ReplacingMergeTree(ver) PARTITION BY toYYYYMM(timestamp) ORDER BY (timestamp, id) SETTINGS index_granularity = 8192")
		assert.False(t, ok, "no TTL")
		assert.Equal(t, 0, days)

		_, ok = chTTLDaysFromEngineFull("ReplacingMergeTree(ver) ORDER BY id TTL timestamp + toIntervalDay(3) SETTINGS index_granularity = 8192")
		assert.False(t, ok, "a TTL Bifrost did not write is not managed")
	})
	t.Run("Clause", func(t *testing.T) {
		days, ok := chTTLDaysFromClause(chLogsTTL(3))
		assert.True(t, ok)
		assert.Equal(t, 3, days)

		_, ok = chTTLDaysFromClause(chLogsTTL(0))
		assert.False(t, ok, "retention < 1 is unmanaged")

		_, ok = chTTLDaysFromClause("toDateTime(created_at) + INTERVAL 3 DAY DELETE WHERE status = 'x'")
		assert.False(t, ok, "only the exact clause shape is managed")
	})
}

// countingRetentionManager is a LogRetentionManager stub that returns a
// scripted count per call and records how often it was asked.
type countingRetentionManager struct {
	counts []int64
	calls  int
}

func (m *countingRetentionManager) DeleteLogsBatch(_ context.Context, _ time.Time, _ int) (int64, error) {
	m.calls++
	if m.calls > len(m.counts) {
		return 0, nil
	}
	return m.counts[m.calls-1], nil
}

// TestLogsCleanerStopsAfterOversizedBatch pins the loop contract ClickHouse
// relies on: a store that deletes the whole expired range in one statement
// returns a count above batchSize, and the cleaner must stop there instead of
// issuing the delete again.
func TestLogsCleanerStopsAfterOversizedBatch(t *testing.T) {
	t.Run("OversizedCountEndsTheLoop", func(t *testing.T) {
		m := &countingRetentionManager{counts: []int64{250}}
		NewLogsCleaner(m, CleanerConfig{RetentionDays: 3}, testLogger{}).cleanupOldLogs(context.Background())
		assert.Equal(t, 1, m.calls, "a count above batchSize means the store already deleted everything")
	})
	t.Run("FullBatchesKeepGoing", func(t *testing.T) {
		m := &countingRetentionManager{counts: []int64{100, 100, 40}}
		NewLogsCleaner(m, CleanerConfig{RetentionDays: 3}, testLogger{}).cleanupOldLogs(context.Background())
		assert.Equal(t, 3, m.calls, "SQL stores return exactly batchSize while rows remain")
	})
	t.Run("ExactBatchThenEmpty", func(t *testing.T) {
		m := &countingRetentionManager{counts: []int64{100, 0}}
		NewLogsCleaner(m, CleanerConfig{RetentionDays: 3}, testLogger{}).cleanupOldLogs(context.Background())
		assert.Equal(t, 2, m.calls, "a full batch is followed by one more probe that finds nothing")
	})
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

	updates := map[string]CostUpdate{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("ch-cost-%d", i)
		require.NoError(t, store.CreateIfNotExists(ctx, chTestLog(id, ts.Add(time.Duration(i)*time.Millisecond))))
		total := float64(i) * 0.1
		updates[id] = CostUpdate{Total: total, Input: total * 0.6, Output: total * 0.3, Additional: total * 0.1}
	}
	// Unknown ids must be ignored, not error.
	updates["ch-cost-missing"] = CostUpdate{Total: 9.9, Input: 9.9}

	require.NoError(t, store.BulkUpdateCost(ctx, updates))
	require.NoError(t, store.BulkUpdateCost(ctx, nil)) // no-op

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("ch-cost-%d", i)
		total := float64(i) * 0.1
		found, err := store.FindByID(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, found.Cost, "cost missing for %s", id)
		assert.InDelta(t, total, *found.Cost, 1e-9)
		// The per-category split is reprice too, reconciling to the total.
		assert.InDelta(t, total*0.6, found.InputCost, 1e-9)
		assert.InDelta(t, total*0.3, found.OutputCost, 1e-9)
		assert.InDelta(t, total*0.1, found.AdditionalCost, 1e-9)
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

	// ClickHouse LIKE is case-sensitive; the filterdata search must not be.
	upper, err := store.GetDistinctModels(ctx, 10, "GPT")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gpt-4o"}, upper)
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

// chMutationIDs snapshots the mutation ids ClickHouse has recorded for table.
// system.mutations keeps finished entries (and trims them in the background),
// so tests diff a before/after snapshot instead of asserting absolute counts.
func chMutationIDs(t *testing.T, db *gorm.DB, table string) map[string]struct{} {
	t.Helper()
	var ids []string
	require.NoError(t, db.Raw("SELECT mutation_id FROM system.mutations WHERE database = currentDatabase() AND table = ?", table).Scan(&ids).Error)
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// chNewMutationCommands returns the command text of every mutation recorded
// for table since the before snapshot, in creation order.
func chNewMutationCommands(t *testing.T, db *gorm.DB, table string, before map[string]struct{}) []string {
	t.Helper()
	type row struct {
		MutationID string
		Command    string
	}
	var rows []row
	require.NoError(t, db.Raw("SELECT mutation_id, command FROM system.mutations WHERE database = currentDatabase() AND table = ? ORDER BY create_time, mutation_id", table).Scan(&rows).Error)
	var cmds []string
	for _, r := range rows {
		if _, seen := before[r.MutationID]; seen {
			continue
		}
		cmds = append(cmds, r.Command)
	}
	return cmds
}

// chLightweightDeletePrefix is how ClickHouse records a lightweight DELETE in
// system.mutations (the command column wraps each command in parentheses).
// A heavyweight ALTER TABLE ... DELETE is recorded as "(DELETE WHERE ...)"
// and rewrites every column of every affected part.
const chLightweightDeletePrefix = "UPDATE _row_exists = 0"

func assertLightweightMutations(t *testing.T, table string, cmds []string) {
	t.Helper()
	for _, cmd := range cmds {
		assert.True(t, strings.HasPrefix(strings.TrimLeft(cmd, "("), chLightweightDeletePrefix), "%s: expected a lightweight delete mutation, got %q", table, cmd)
		assert.NotContains(t, cmd, "DELETE WHERE", "%s: heavyweight ALTER TABLE ... DELETE rewrites whole parts (#7098)", table)
	}
}

// TestClickHouseDeleteLogsBatchIsSingleLightweightMutation covers #7098: one
// retention sweep must cost one lightweight mutation, not one heavyweight
// part rewrite per 100 rows. It drives the same loop LogsCleaner runs.
func TestClickHouseDeleteLogsBatchIsSingleLightweightMutation(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Millisecond)
	entries := make([]*Log, 0, 250)
	for i := range 250 {
		entries = append(entries, chTestLog(fmt.Sprintf("ch-sweep-%03d", i), old.Add(time.Duration(i)*time.Millisecond)))
	}
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))
	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-sweep-fresh", time.Now().UTC().Truncate(time.Millisecond))))

	before := chMutationIDs(t, store.db, "logs")
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	var total int64
	for {
		deleted, err := store.DeleteLogsBatch(ctx, cutoff, batchSize)
		require.NoError(t, err)
		total += deleted
		if deleted != int64(batchSize) {
			break
		}
	}
	assert.Equal(t, int64(250), total, "cleaner logging relies on an accurate deleted count")

	_, err := store.FindByID(ctx, "ch-sweep-fresh")
	assert.NoError(t, err, "rows newer than the cutoff must survive")
	_, err = store.FindByID(ctx, "ch-sweep-000")
	assert.ErrorIs(t, err, ErrNotFound)

	cmds := chNewMutationCommands(t, store.db, "logs", before)
	require.Len(t, cmds, 1, "one sweep must issue exactly one mutation, got %v", cmds)
	assertLightweightMutations(t, "logs", cmds)
}

// TestClickHouseFlushIsLightweightAndSkipsWhenEmpty covers the minute sweep
// from plugins/logging: Flush and FlushMCPToolLogs must use lightweight
// deletes and must not issue any mutation when nothing is left to flush.
func TestClickHouseFlushIsLightweightAndSkipsWhenEmpty(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-40 * time.Minute).Truncate(time.Millisecond)
	stuck := chTestLog("ch-flush-stuck", old)
	done := chTestLog("ch-flush-done", old)
	done.Status = "success"
	require.NoError(t, store.CreateIfNotExists(ctx, stuck))
	require.NoError(t, store.CreateIfNotExists(ctx, done))
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, []*MCPToolLog{chTestMCPToolLog("ch-flush-mcp", old)}))

	logsBefore := chMutationIDs(t, store.db, "logs")
	mcpBefore := chMutationIDs(t, store.db, "mcp_tool_logs")
	since := time.Now().UTC().Add(-30 * time.Minute)
	require.NoError(t, store.Flush(ctx, since))
	require.NoError(t, store.FlushMCPToolLogs(ctx, since))

	_, err := store.FindByID(ctx, "ch-flush-stuck")
	assert.ErrorIs(t, err, ErrNotFound, "stale processing row must be flushed")
	_, err = store.FindByID(ctx, "ch-flush-done")
	assert.NoError(t, err, "completed rows must survive the flush")
	_, err = store.FindMCPToolLog(ctx, "ch-flush-mcp")
	assert.ErrorIs(t, err, ErrNotFound, "stale processing MCP row must be flushed")

	logsCmds := chNewMutationCommands(t, store.db, "logs", logsBefore)
	require.Len(t, logsCmds, 1, "logs: one flush must issue exactly one mutation, got %v", logsCmds)
	assertLightweightMutations(t, "logs", logsCmds)
	mcpCmds := chNewMutationCommands(t, store.db, "mcp_tool_logs", mcpBefore)
	require.Len(t, mcpCmds, 1, "mcp_tool_logs: one flush must issue exactly one mutation, got %v", mcpCmds)
	assertLightweightMutations(t, "mcp_tool_logs", mcpCmds)

	// Nothing left to flush: the once-a-minute sweep must not touch the
	// tables at all (the issue counted ~1,440 mutations per table per day).
	logsBefore = chMutationIDs(t, store.db, "logs")
	mcpBefore = chMutationIDs(t, store.db, "mcp_tool_logs")
	require.NoError(t, store.Flush(ctx, since))
	require.NoError(t, store.FlushMCPToolLogs(ctx, since))
	assert.Empty(t, chNewMutationCommands(t, store.db, "logs", logsBefore), "empty flush must not issue a mutation")
	assert.Empty(t, chNewMutationCommands(t, store.db, "mcp_tool_logs", mcpBefore), "empty flush must not issue a mutation")
}

// TestClickHouseFlushSkipsSupersededProcessingRow: a log created as processing
// and then updated to success leaves the old version physically in place until
// merge. The minute sweep must not treat that superseded version as stale, or
// it would issue a mutation every run until the part merged.
func TestClickHouseFlushSkipsSupersededProcessingRow(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-40 * time.Minute).Truncate(time.Millisecond)
	require.NoError(t, store.CreateIfNotExists(ctx, chTestLog("ch-flush-superseded", old)))
	require.NoError(t, store.Update(ctx, "ch-flush-superseded", map[string]interface{}{"status": "success"}))
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, []*MCPToolLog{chTestMCPToolLog("ch-flush-superseded-mcp", old)}))
	require.NoError(t, store.UpdateMCPToolLog(ctx, "ch-flush-superseded-mcp", map[string]interface{}{"status": "success"}))

	logsBefore := chMutationIDs(t, store.db, "logs")
	mcpBefore := chMutationIDs(t, store.db, "mcp_tool_logs")
	since := time.Now().UTC().Add(-30 * time.Minute)
	require.NoError(t, store.Flush(ctx, since))
	require.NoError(t, store.FlushMCPToolLogs(ctx, since))
	assert.Empty(t, chNewMutationCommands(t, store.db, "logs", logsBefore), "superseded processing version must not trigger a flush mutation")
	assert.Empty(t, chNewMutationCommands(t, store.db, "mcp_tool_logs", mcpBefore), "superseded processing version must not trigger a flush mutation")

	found, err := store.FindByID(ctx, "ch-flush-superseded")
	require.NoError(t, err)
	assert.Equal(t, "success", found.Status)
	mcp, err := store.FindMCPToolLog(ctx, "ch-flush-superseded-mcp")
	require.NoError(t, err)
	assert.Equal(t, "success", mcp.Status)
}

// TestClickHouseFinalAfterLightweightDelete proves the _row_exists mask left
// by a lightweight delete cannot resurrect an older ReplacingMergeTree version
// of the row under the connection-level final=1 setting, and that the
// user-triggered deletes are lightweight too.
func TestClickHouseFinalAfterLightweightDelete(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	for _, id := range []string{"ch-final-1", "ch-final-2", "ch-final-3"} {
		require.NoError(t, store.CreateIfNotExists(ctx, chTestLog(id, ts)))
		// A second ReplacingMergeTree version of the same id via read-modify-write.
		require.NoError(t, store.Update(ctx, id, map[string]interface{}{"status": "success"}))
		// Two physical versions must exist (FINAL would hide the older one),
		// otherwise the resurrection case below is not actually exercised.
		require.EqualValues(t, 2, chCountIDsNoFinal(t, store, "logs", []string{id}), "expected both versions of %s on disk before the delete", id)
		require.Equal(t, int64(1), chCountRows(t, store.db, "logs", id), "FINAL collapses them to one logical row")
	}
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, []*MCPToolLog{chTestMCPToolLog("ch-final-mcp", ts)}))

	logsBefore := chMutationIDs(t, store.db, "logs")
	mcpBefore := chMutationIDs(t, store.db, "mcp_tool_logs")
	require.NoError(t, store.DeleteLog(ctx, "ch-final-1"))
	require.NoError(t, store.DeleteLogs(ctx, []string{"ch-final-2", "ch-final-3"}))
	require.NoError(t, store.DeleteMCPToolLogs(ctx, []string{"ch-final-mcp"}))

	for _, id := range []string{"ch-final-1", "ch-final-2", "ch-final-3"} {
		_, err := store.FindByID(ctx, id)
		assert.ErrorIs(t, err, ErrNotFound, "log %s should be deleted", id)
		assert.Equal(t, int64(0), chCountRows(t, store.db, "logs", id), "no version of %s may survive under FINAL", id)
		assert.EqualValues(t, 0, chCountIDsNoFinal(t, store, "logs", []string{id}), "both physical versions of %s must be masked, not just the newest", id)
	}
	_, err := store.FindMCPToolLog(ctx, "ch-final-mcp")
	assert.ErrorIs(t, err, ErrNotFound)

	logsCmds := chNewMutationCommands(t, store.db, "logs", logsBefore)
	require.Len(t, logsCmds, 2, "DeleteLog + DeleteLogs must issue one mutation each, got %v", logsCmds)
	assertLightweightMutations(t, "logs", logsCmds)
	mcpCmds := chNewMutationCommands(t, store.db, "mcp_tool_logs", mcpBefore)
	require.Len(t, mcpCmds, 1, "DeleteMCPToolLogs must issue one mutation, got %v", mcpCmds)
	assertLightweightMutations(t, "mcp_tool_logs", mcpCmds)
}

func chEngineFull(t *testing.T, db *gorm.DB, table string) string {
	t.Helper()
	var engineFull string
	require.NoError(t, db.Raw("SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = ?", table).Scan(&engineFull).Error)
	return engineFull
}

// TestClickHouseTTLReconciledOnExistingTables covers the second half of
// #7098: CREATE TABLE IF NOT EXISTS never updates the TTL of an existing
// table, so a changed logs_store.retention_days must be reconciled with
// MODIFY TTL / REMOVE TTL at startup.
func TestClickHouseTTLReconciledOnExistingTables(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	retained := []string{"logs", "mcp_tool_logs", "webhook_deliveries"}
	// removeTTLs strips any TTL from the shared tables. REMOVE TTL errors on
	// a table that has none (BAD_ARGUMENTS), so it is only issued when one is
	// present. It runs before the initial-state check, because retention 0
	// deliberately preserves whatever an interrupted earlier run left behind,
	// and again on cleanup so every other test still sees TTL-free tables.
	removeTTLs := func() {
		t.Helper()
		for _, table := range retained {
			if strings.Contains(chEngineFull(t, store.db, table), "TTL ") {
				require.NoError(t, store.db.Exec("ALTER TABLE `"+table+"` REMOVE TTL").Error, "%s: reset TTL", table)
			}
		}
	}
	removeTTLs()
	t.Cleanup(removeTTLs)
	// managedDays asserts the table carries exactly one Bifrost-managed TTL of
	// want days, using the production parser so an appended or leftover rule
	// cannot satisfy a looser substring check.
	managedDays := func(table string, want int) {
		t.Helper()
		engineFull := chEngineFull(t, store.db, table)
		days, ok := chTTLDaysFromEngineFull(engineFull)
		require.True(t, ok, "%s: expected a managed TTL, got %q", table, engineFull)
		assert.Equal(t, want, days, "%s: %q", table, engineFull)
		assert.Equal(t, 1, strings.Count(engineFull, "TTL "), "%s: exactly one TTL clause expected in %q", table, engineFull)
	}

	for _, table := range retained {
		require.NotContains(t, chEngineFull(t, store.db, table), "TTL", "%s: fixture tables are created without retention", table)
	}

	withTTL, err := newClickHouseLogStore(ctx, clickhouseTestConfig(), 3, testLogger{})
	require.NoError(t, err)
	require.NoError(t, withTTL.Close(ctx))
	for _, table := range retained {
		managedDays(table, 3)
	}
	managedDays("async_jobs", 7)

	// A different retention replaces the TTL in place.
	longer, err := newClickHouseLogStore(ctx, clickhouseTestConfig(), 5, testLogger{})
	require.NoError(t, err)
	require.NoError(t, longer.Close(ctx))
	for _, table := range retained {
		managedDays(table, 5)
	}
	// Snapshot the complete definitions so the retention-zero restart is
	// proven to leave them byte-for-byte unchanged, not merely still matching.
	before := map[string]string{}
	for _, table := range append(retained, "async_jobs") {
		before[table] = chEngineFull(t, store.db, table)
	}

	// Retention 0 (the default when the field is omitted) means "not managed
	// by Bifrost": an existing TTL, including one an operator applied by hand
	// as the #7098 workaround, must survive a restart.
	unmanaged, err := newClickHouseLogStore(ctx, clickhouseTestConfig(), 0, testLogger{})
	require.NoError(t, err)
	require.NoError(t, unmanaged.Close(ctx))
	for table, want := range before {
		assert.Equal(t, want, chEngineFull(t, store.db, table), "%s: retention_days=0 must leave the table definition untouched", table)
	}
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
