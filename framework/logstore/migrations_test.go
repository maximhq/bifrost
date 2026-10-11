package logstore

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestMigrationAddMCPRedactionMappingColumn verifies the MCP mapping column is additive, idempotent, and preserves existing rows.
func TestMigrationAddMCPRedactionMappingColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE mcp_tool_logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO mcp_tool_logs (id) VALUES (?)", "mcp-existing").Error)

	ctx := context.Background()
	require.NoError(t, migrationAddMCPRedactionMappingColumn(ctx, db, testLogger{}))
	require.True(t, db.Migrator().HasColumn(&MCPToolLog{}, "RedactionMapping"))
	require.NoError(t, migrationAddMCPRedactionMappingColumn(ctx, db, testLogger{}))

	var count int64
	require.NoError(t, db.Table("mcp_tool_logs").Where("id = ?", "mcp-existing").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestMigrationAddMCPPluginLogsColumn verifies the MCP plugin-log column is additive, idempotent, and preserves existing rows.
func TestMigrationAddMCPPluginLogsColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE mcp_tool_logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO mcp_tool_logs (id) VALUES (?)", "mcp-existing").Error)

	ctx := context.Background()
	require.NoError(t, migrationAddMCPPluginLogsColumn(ctx, db, testLogger{}))
	require.True(t, db.Migrator().HasColumn(&MCPToolLog{}, "PluginLogs"))
	require.NoError(t, migrationAddMCPPluginLogsColumn(ctx, db, testLogger{}))

	var count int64
	require.NoError(t, db.Table("mcp_tool_logs").Where("id = ?", "mcp-existing").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestMigrationAddSessionIDColumn verifies that the session lookup
// column is additive, indexed, and preserves pre-existing request logs.
func TestMigrationAddSessionIDColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO logs (id) VALUES (?)", "existing-log").Error)

	ctx := context.Background()
	require.NoError(t, migrationAddSessionIDColumn(ctx, db, testLogger{}))
	require.True(t, db.Migrator().HasColumn(&Log{}, "SessionID"))
	require.True(t, db.Migrator().HasIndex(&Log{}, "idx_logs_session_id"))
	require.NoError(t, migrationAddSessionIDColumn(ctx, db, testLogger{}))

	var count int64
	require.NoError(t, db.Table("logs").Where("id = ?", "existing-log").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// pgTestSchema is this package's dedicated Postgres schema. Test packages
// (configstore, configstore/tables, logstore) run in parallel against the same
// database, so each one works in its own schema to avoid clobbering the
// others' tables and rows.
const pgTestSchema = "logstore_test"

// postgresDSN matches the postgres service in tests/docker-compose.yml and
// framework/docker-compose.yml.
const postgresDSN = "host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable search_path=" + pgTestSchema

// trySetupPostgresDB attempts to connect to Postgres and returns the connection.
// Returns nil if Postgres is unavailable.
func trySetupPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(postgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil
	}

	// Verify the connection is actually live before proceeding.
	sqlDB, err := db.DB()
	if err != nil {
		return nil
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		return nil
	}

	// All objects live in this package's dedicated schema (via search_path in
	// the DSN), isolated from other test packages sharing the same database.
	if err := db.Exec("CREATE SCHEMA IF NOT EXISTS " + pgTestSchema).Error; err != nil {
		return nil
	}

	return db
}

// setupLogsTableForGINIndexTest creates the logs table in a pre-migration state
// (with metadata column but without the GIN index) for testing the GIN index migration.
func setupLogsTableForGINIndexTest(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Drop existing tables and migration tracking in the correct order.
	// Preserve the shared migrations table — only clear its rows.
	dropAllManagedMatViews(db)
	db.Exec("DROP INDEX IF EXISTS idx_logs_metadata_gin")
	db.Exec("DROP TABLE IF EXISTS logs")
	db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)")
	db.Exec("DELETE FROM migrations")

	// Create a minimal logs table with only the columns needed for the test
	err := db.Exec(`
		CREATE TABLE logs (
			id VARCHAR(255) PRIMARY KEY,
			timestamp TIMESTAMP NOT NULL,
			object_type VARCHAR(255) NOT NULL,
			provider VARCHAR(255) NOT NULL,
			model VARCHAR(255) NOT NULL,
			status VARCHAR(50) NOT NULL,
			metadata TEXT,
			created_at TIMESTAMP NOT NULL
		)
	`).Error
	require.NoError(t, err, "Failed to create logs table")

	// The migrator will create the migrations table automatically when it runs

	// Clean up tables after the test
	t.Cleanup(func() {
		dropAllManagedMatViews(db)
		db.Exec("DROP INDEX IF EXISTS idx_logs_metadata_gin")
		db.Exec("DROP TABLE IF EXISTS logs")
		db.Exec("DELETE FROM migrations")
	})
}

func dropAllManagedMatViews(db *gorm.DB) {
	for _, view := range allMatViewNames() {
		db.Exec("DROP MATERIALIZED VIEW IF EXISTS " + view + " CASCADE")
	}
	for _, view := range legacyMatViewNames {
		db.Exec("DROP MATERIALIZED VIEW IF EXISTS " + view + " CASCADE")
	}
}

// insertTestLog inserts a test log entry with the given metadata value.
func insertTestLog(t *testing.T, db *gorm.DB, id string, metadata *string) {
	t.Helper()
	now := time.Now()

	var metadataVal interface{}
	if metadata != nil {
		metadataVal = *metadata
	}

	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, now, "chat_completion", "openai", "gpt-4", "success", metadataVal, now).Error
	require.NoError(t, err, "Failed to insert test log %s", id)
}

// getMetadataValue retrieves the metadata value for a given log ID.
func getMetadataValue(t *testing.T, db *gorm.DB, id string) *string {
	t.Helper()
	var result struct {
		Metadata *string
	}
	err := db.Table("logs").Select("metadata").Where("id = ?", id).Scan(&result).Error
	require.NoError(t, err, "Failed to get metadata for log %s", id)
	return result.Metadata
}

// indexExists checks if the GIN index exists on the logs table.
func indexExists(t *testing.T, db *gorm.DB, indexName string) bool {
	t.Helper()
	var count int64
	err := db.Raw(`
		SELECT COUNT(*) FROM pg_indexes 
		WHERE tablename = 'logs' AND indexname = ?
	`, indexName).Scan(&count).Error
	require.NoError(t, err, "Failed to check index existence")
	return count > 0
}

func TestMigrationAddMetadataGINIndex_ValidJSON(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	setupLogsTableForGINIndexTest(t, db)
	ctx := context.Background()

	// Insert logs with valid JSON object metadata (arrays are not supported)
	validJSON1 := `{"key": "value"}`
	validJSON2 := `{"nested": {"foo": "bar"}, "array": [1, 2, 3]}`
	validJSON3 := `{"empty": {}}`
	validJSON4 := `{"number": 42, "bool": true, "null": null}`

	insertTestLog(t, db, "log-valid-1", &validJSON1)
	insertTestLog(t, db, "log-valid-2", &validJSON2)
	insertTestLog(t, db, "log-valid-3", &validJSON3)
	insertTestLog(t, db, "log-valid-4", &validJSON4)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Failed to get SQL DB: %v", err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get SQL connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Run the migration (cleanup only) then ensure the index is built.
	err = migrationAddMetadataGINIndex(ctx, db, testLogger{})
	require.NoError(t, err, "Migration should succeed")
	err = ensureMetadataGINIndex(ctx, conn)
	require.NoError(t, err, "GIN index creation should succeed")

	// Verify all valid JSON object values are preserved
	meta1 := getMetadataValue(t, db, "log-valid-1")
	assert.NotNil(t, meta1, "Valid JSON object should be preserved")
	assert.Equal(t, validJSON1, *meta1)

	meta2 := getMetadataValue(t, db, "log-valid-2")
	assert.NotNil(t, meta2, "Valid JSON object should be preserved")
	assert.Equal(t, validJSON2, *meta2)

	meta3 := getMetadataValue(t, db, "log-valid-3")
	assert.NotNil(t, meta3, "Valid JSON object with nested empty object should be preserved")
	assert.Equal(t, validJSON3, *meta3)

	meta4 := getMetadataValue(t, db, "log-valid-4")
	assert.NotNil(t, meta4, "Valid JSON object with various types should be preserved")
	assert.Equal(t, validJSON4, *meta4)

	// Verify the GIN index was created
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should be created")
}

func TestMigrationAddMetadataGINIndex_InvalidJSON(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	setupLogsTableForGINIndexTest(t, db)
	ctx := context.Background()

	// Insert logs with invalid JSON metadata (not valid JSON objects)
	invalid1 := `{"key": invalid}`     // Unquoted value
	invalid2 := `{key: "value"}`       // Unquoted key
	invalid3 := `{"key": "value",}`    // Trailing comma
	invalid4 := `just a string`        // Plain text
	invalid5 := ``                     // Empty string
	invalid6 := `{"unclosed": "brace"` // Unclosed brace
	invalid7 := `{"key": undefined}`   // JavaScript undefined
	invalid8 := `{'single': 'quotes'}` // Single quotes
	invalid9 := `[NULL]`               // Literal string [NULL] (not valid JSON)
	invalid10 := `NULL`                // Literal string NULL (not valid JSON)
	invalid11 := `null`                // Valid JSON but not a JSON object
	invalid12 := `[1, 2, 3]`           // Valid JSON array but not a JSON object

	insertTestLog(t, db, "log-invalid-1", &invalid1)
	insertTestLog(t, db, "log-invalid-2", &invalid2)
	insertTestLog(t, db, "log-invalid-3", &invalid3)
	insertTestLog(t, db, "log-invalid-4", &invalid4)
	insertTestLog(t, db, "log-invalid-5", &invalid5)
	insertTestLog(t, db, "log-invalid-6", &invalid6)
	insertTestLog(t, db, "log-invalid-7", &invalid7)
	insertTestLog(t, db, "log-invalid-8", &invalid8)
	insertTestLog(t, db, "log-invalid-9", &invalid9)
	insertTestLog(t, db, "log-invalid-10", &invalid10)
	insertTestLog(t, db, "log-invalid-11", &invalid11)
	insertTestLog(t, db, "log-invalid-12", &invalid12)
	insertTestLog(t, db, "log-actual-null", nil) // Actual SQL NULL
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Failed to get SQL DB: %v", err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get SQL connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// Run the migration (cleanup only) then ensure the index is built.
	err = migrationAddMetadataGINIndex(ctx, db, testLogger{})
	require.NoError(t, err, "Migration should succeed even with invalid JSON")
	err = ensureMetadataGINIndex(ctx, conn)
	require.NoError(t, err, "GIN index creation should succeed after invalid JSON cleanup")

	// Verify all non-object values were set to NULL (only JSON objects are supported)
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("log-invalid-%d", i)
		meta := getMetadataValue(t, db, id)
		assert.Nil(t, meta, "Non-object JSON for %s should be set to NULL", id)
	}

	// Verify actual SQL NULL remains NULL
	metaActualNull := getMetadataValue(t, db, "log-actual-null")
	assert.Nil(t, metaActualNull, "Actual NULL should remain NULL")

	// Verify the GIN index was created
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should be created")
}

func TestMigrationAddMetadataGINIndex_MixedData(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	setupLogsTableForGINIndexTest(t, db)
	ctx := context.Background()

	// Insert a mix of valid JSON, invalid JSON, and NULL metadata
	validJSON := `{"environment": "production", "version": "1.0.0"}`
	invalidJSON := `{"broken": invalid_value}`

	insertTestLog(t, db, "log-mixed-valid", &validJSON)
	insertTestLog(t, db, "log-mixed-invalid", &invalidJSON)
	insertTestLog(t, db, "log-mixed-null", nil)

	// Run the migration (cleanup only) then ensure the index is built.
	err := migrationAddMetadataGINIndex(ctx, db, testLogger{})
	require.NoError(t, err, "Migration should succeed")

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Failed to get SQL DB: %v", err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get SQL connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	err = ensureMetadataGINIndex(ctx, conn)
	require.NoError(t, err, "GIN index creation should succeed")

	// Verify valid JSON is preserved
	metaValid := getMetadataValue(t, db, "log-mixed-valid")
	assert.NotNil(t, metaValid, "Valid JSON should be preserved")
	assert.Equal(t, validJSON, *metaValid)

	// Verify invalid JSON is cleaned to NULL
	metaInvalid := getMetadataValue(t, db, "log-mixed-invalid")
	assert.Nil(t, metaInvalid, "Invalid JSON should be set to NULL")

	// Verify NULL remains NULL
	metaNull := getMetadataValue(t, db, "log-mixed-null")
	assert.Nil(t, metaNull, "NULL metadata should remain NULL")

	// Verify the GIN index was created
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should be created")
}

func TestMigrationAddMetadataGINIndex_Idempotent(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	setupLogsTableForGINIndexTest(t, db)
	ctx := context.Background()

	// Insert a log with valid JSON
	validJSON := `{"test": "idempotent"}`
	insertTestLog(t, db, "log-idempotent", &validJSON)

	// Run the migration (cleanup only) then ensure the index is built.
	err := migrationAddMetadataGINIndex(ctx, db, testLogger{})
	require.NoError(t, err, "First migration should succeed")

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Failed to get SQL DB: %v", err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get SQL connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	err = ensureMetadataGINIndex(ctx, conn)
	require.NoError(t, err, "GIN index creation should succeed")

	// Verify index exists
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should exist after first migration")

	// Verify metadata is preserved
	meta1 := getMetadataValue(t, db, "log-idempotent")
	assert.NotNil(t, meta1)
	assert.Equal(t, validJSON, *meta1)

	// Run the migration second time (should be idempotent due to gomigrate tracking)
	err = migrationAddMetadataGINIndex(ctx, db, testLogger{})
	require.NoError(t, err, "Second migration should succeed (idempotent)")
	err = ensureMetadataGINIndex(ctx, conn)
	require.NoError(t, err, "ensureMetadataGINIndex should be a no-op when index already exists")

	// Verify index still exists
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should exist after second migration")

	// Verify metadata is still preserved
	meta2 := getMetadataValue(t, db, "log-idempotent")
	assert.NotNil(t, meta2)
	assert.Equal(t, validJSON, *meta2)
}

func TestMigrationAddMetadataGINIndex_EmptyTable(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	setupLogsTableForGINIndexTest(t, db)
	ctx := context.Background()

	// Run the migration (cleanup only) then ensure the index is built.
	err := migrationAddMetadataGINIndex(ctx, db, testLogger{})
	require.NoError(t, err, "Migration should succeed on empty table")

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Failed to get SQL DB: %v", err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get SQL connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	err = ensureMetadataGINIndex(ctx, conn)
	require.NoError(t, err, "GIN index creation should succeed on empty table")

	// Verify the GIN index was created
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should be created even on empty table")
}

func TestMigrationAddMetadataGINIndex_EdgeCases(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	setupLogsTableForGINIndexTest(t, db)
	ctx := context.Background()

	// Test edge cases that might be tricky (only JSON objects are supported)
	emptyObject := `{}`
	emptyArray := `[]`                       // Not a JSON object, should be nullified
	whitespaceJSON := `  {"key": "value"}  ` // Valid JSON with surrounding whitespace
	unicodeJSON := `{"emoji": "🎉", "chinese": "中文"}`
	largeNumber := `{"bignum": 99999999999999999999}`
	scientificNotation := `{"sci": 1.23e10}`

	insertTestLog(t, db, "log-edge-empty-obj", &emptyObject)
	insertTestLog(t, db, "log-edge-empty-arr", &emptyArray)
	insertTestLog(t, db, "log-edge-whitespace", &whitespaceJSON)
	insertTestLog(t, db, "log-edge-unicode", &unicodeJSON)
	insertTestLog(t, db, "log-edge-large-num", &largeNumber)
	insertTestLog(t, db, "log-edge-scientific", &scientificNotation)

	// Run the migration (cleanup only) then ensure the index is built.
	err := migrationAddMetadataGINIndex(ctx, db, testLogger{})
	require.NoError(t, err, "Migration should succeed")

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Failed to get SQL DB: %v", err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get SQL connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	err = ensureMetadataGINIndex(ctx, conn)
	require.NoError(t, err, "GIN index creation should succeed")

	// Verify all edge cases are handled correctly
	// Empty object should be preserved, but empty array is not a JSON object
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-empty-obj"), "Empty object should be preserved")
	assert.Nil(t, getMetadataValue(t, db, "log-edge-empty-arr"), "Empty array should be nullified (not a JSON object)")

	// Whitespace JSON should be preserved (Postgres handles it)
	meta := getMetadataValue(t, db, "log-edge-whitespace")
	assert.NotNil(t, meta, "Whitespace JSON object should be preserved")

	// Unicode should be preserved
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-unicode"), "Unicode JSON object should be preserved")

	// Large numbers and scientific notation should be preserved
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-large-num"), "Large number JSON object should be preserved")
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-scientific"), "Scientific notation JSON object should be preserved")

	// Verify the GIN index was created
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should be created")
}

// TestCorrelationColumnsMigration verifies that all correlation columns are additive,
// indexed, idempotent, and preserve rows written before the migration.
func TestCorrelationColumnsMigration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE mcp_tool_logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO logs (id) VALUES (?)", "log-existing").Error)
	require.NoError(t, db.Exec("INSERT INTO mcp_tool_logs (id) VALUES (?)", "mcp-existing").Error)

	ctx := context.Background()
	require.NoError(t, migrationCreateAgentLogsTable(ctx, db, testLogger{}))
	for _, column := range []struct {
		model any
		field string
		index string
	}{
		{&Log{}, "AgentCorrelationID", "idx_logs_agent_correlation_id"},
		{&MCPToolLog{}, "SessionID", "idx_mcp_logs_session_id"},
		{&MCPToolLog{}, "AgentCorrelationID", "idx_mcp_logs_agent_correlation_id"},
	} {
		require.True(t, db.Migrator().HasColumn(column.model, column.field), "missing %s", column.field)
		require.True(t, db.Migrator().HasIndex(column.model, column.index), "missing %s", column.index)
	}
	require.NoError(t, migrationCreateAgentLogsTable(ctx, db, testLogger{}))

	var logCount, mcpCount int64
	require.NoError(t, db.Table("logs").Where("id = ?", "log-existing").Count(&logCount).Error)
	require.NoError(t, db.Table("mcp_tool_logs").Where("id = ?", "mcp-existing").Count(&mcpCount).Error)
	assert.Equal(t, int64(1), logCount)
	assert.Equal(t, int64(1), mcpCount)
}

func TestAgentLogsMigrationRollbackGuardsRecordedHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, db.Exec("CREATE TABLE logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE mcp_tool_logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, migrationCreateAgentLogsTable(ctx, db, testLogger{}))
	require.NoError(t, rollbackAgentLogsMigration(db, testLogger{}))
	assert.False(t, db.Migrator().HasTable(&AgentLog{}), "empty Agent history is safe to drop")

	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	require.NoError(t, db.Create(&AgentLog{
		ID:         "agent-log-1",
		Timestamp:  time.Now(),
		RecordKind: "request",
		Status:     "completed",
		AgentName:  "test-agent",
		RequestID:  "request-1",
	}).Error)

	err = rollbackAgentLogsMigration(db, testLogger{})
	require.Error(t, err, "rollback must refuse while recorded Agent history exists")
	assert.Contains(t, err.Error(), "agent_logs_init is non-rollbackable")
	assert.True(t, db.Migrator().HasTable(&AgentLog{}), "a refused rollback must leave the table intact")

	var surviving int64
	require.NoError(t, db.Model(&AgentLog{}).Count(&surviving).Error)
	assert.EqualValues(t, 1, surviving, "the recorded Agent history must survive")
}

func TestAgentLogsMigrationRollbackGuardsCorrelationData(t *testing.T) {
	tests := []struct {
		name   string
		table  string
		column string
	}{
		{name: "LLM Agent correlation", table: "logs", column: "agent_correlation_id"},
		{name: "MCP session", table: "mcp_tool_logs", column: "session_id"},
		{name: "MCP Agent correlation", table: "mcp_tool_logs", column: "agent_correlation_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			require.NoError(t, db.Exec("CREATE TABLE logs (id TEXT PRIMARY KEY)").Error)
			require.NoError(t, db.Exec("CREATE TABLE mcp_tool_logs (id TEXT PRIMARY KEY)").Error)
			require.NoError(t, migrationCreateAgentLogsTable(context.Background(), db, testLogger{}))
			require.NoError(t, db.Table(tt.table).Create(map[string]interface{}{"id": "correlated-log", tt.column: "correlation-1"}).Error)

			err = rollbackAgentLogsMigration(db, testLogger{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "agent_logs_init is non-rollbackable")
			assert.Contains(t, err.Error(), tt.table+"."+tt.column)
			assert.True(t, db.Migrator().HasTable(&AgentLog{}), "a refused rollback must leave the Agent log table intact")
			assert.True(t, db.Migrator().HasColumn(tt.table, tt.column), "a refused rollback must leave the populated correlation column intact")
		})
	}
}

// TestPerformanceIndexesCoverProjectIDs pins the project indexes to the background builders: the
// project column migrations add only the columns (addColumnIfNotExists never creates a field's
// index), so an upgraded deployment scans logs by project_id unindexed unless a builder carries
// them. logs gets the (project_id, timestamp) composite from ensureOwnerTimestampIndexes;
// mcp_tool_logs keeps its single-column entry in ensurePerformanceIndexes. A fresh database gets
// them from the model's index tags at table creation.
func TestPerformanceIndexesCoverProjectIDs(t *testing.T) {
	tables := map[string]string{}
	for _, idx := range performanceIndexes {
		tables[idx.name] = idx.table
	}
	for _, idx := range rankingNameTimestampIndexes {
		tables[idx.name] = idx.table
	}
	assert.Equal(t, "logs", tables["idx_logs_project_ts"])
	assert.Equal(t, "mcp_tool_logs", tables["idx_mcp_logs_project_id"])
}

// TestPerformanceIndexesHaveNoDuplicateColumns keeps the background builder from building the
// same index twice under two names: a second index on the same columns costs a full build on
// every upgraded deployment and a write on every insert, and the planner still picks one. A
// partial index counts as a duplicate of a full one on the same columns, since an equality
// filter on the column already implies its IS NOT NULL predicate.
func TestPerformanceIndexesHaveNoDuplicateColumns(t *testing.T) {
	onColumns := regexp.MustCompile(`(?i)\bON\s+(\w+)\s*(USING\s+\w+\s*)?\(([^)]*)\)`)
	seen := map[string]string{}
	for _, idx := range performanceIndexes {
		match := onColumns.FindStringSubmatch(idx.sql)
		require.NotNil(t, match, "cannot read the columns of %s from %q", idx.name, idx.sql)
		key := strings.ToLower(strings.TrimSpace(match[1]+" "+strings.TrimSpace(match[2])) + "(" + strings.Join(strings.Fields(match[3]), " ") + ")")
		if other, ok := seen[key]; ok {
			t.Errorf("%s and %s both index %s", other, idx.name, key)
			continue
		}
		seen[key] = idx.name
	}
}

func TestPerformanceIndexesCoverCorrelationIDs(t *testing.T) {
	tables := map[string]string{}
	for _, idx := range performanceIndexes {
		tables[idx.name] = idx.table
	}
	assert.Equal(t, "logs", tables["idx_logs_agent_correlation_id"])
	assert.Equal(t, "mcp_tool_logs", tables["idx_mcp_logs_session_id"])
	assert.Equal(t, "mcp_tool_logs", tables["idx_mcp_logs_agent_correlation_id"])
}

// TestMigrationAddMCPGovernanceSnapshots verifies the attribution columns are
// additive, idempotent, and leave rows written before them intact — those rows
// keep their bare ids, which is the accepted cost of not rewriting history.
func TestMigrationAddMCPGovernanceSnapshots(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE mcp_tool_logs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO mcp_tool_logs (id) VALUES (?)", "mcp-existing").Error)

	ctx := context.Background()
	require.NoError(t, migrationAddMCPGovernanceSnapshots(ctx, db, testLogger{}))
	for _, field := range []string{
		"UserName", "TeamName", "CustomerName", "BusinessUnitName",
		"TeamIDs", "TeamNames", "CustomerIDs", "CustomerNames",
		"BusinessUnitIDs", "BusinessUnitNames", "BudgetIDs", "RateLimitIDs",
	} {
		require.True(t, db.Migrator().HasColumn(&MCPToolLog{}, field), "missing column for %s", field)
	}
	require.NoError(t, migrationAddMCPGovernanceSnapshots(ctx, db, testLogger{}))

	var count int64
	require.NoError(t, db.Table("mcp_tool_logs").Where("id = ?", "mcp-existing").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestMCPGovernanceSnapshotsMigrationIsRegistered keeps the migration reachable:
// an unregistered step leaves the columns missing on every real deployment while
// every unit test that calls it directly still passes.
func TestMCPGovernanceSnapshotsMigrationIsRegistered(t *testing.T) {
	for _, step := range logstoreMigrationSteps {
		for _, id := range step.IDs {
			if id == "mcp_tool_logs_add_governance_snapshots" {
				return
			}
		}
	}
	t.Fatal("mcp_tool_logs_add_governance_snapshots is not registered in logstoreMigrationSteps")
}

// TestMigrationAddWarpConversationTables_NonRollbackable pins that rolling the
// history tables back is refused while they hold anything. warp_conversations
// and warp_messages are persistent user content - saved chats someone can
// reopen - so dropping them is not a schema reversal, it is deleting the data.
// An empty pair is still safe to drop, which keeps a failed upgrade reversible.
func TestMigrationAddWarpConversationTables_NonRollbackable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, migrationAddWarpConversationTables(ctx, db, testLogger{}))
	require.True(t, db.Migrator().HasTable(&WarpConversation{}))
	require.True(t, db.Migrator().HasTable(&WarpMessage{}))

	// Empty: the rollback is a genuine reversal and must be allowed.
	require.NoError(t, rollbackWarpConversationTables(db))
	require.False(t, db.Migrator().HasTable(&WarpConversation{}),
		"an empty history is safe to drop")

	// Re-create via AutoMigrate, not the migration: migration ids are write-once,
	// so a second run of the same id is recorded as already applied and does
	// nothing. Then seed a saved conversation, so the drop would destroy content.
	require.NoError(t, db.AutoMigrate(&WarpConversation{}, &WarpMessage{}))
	require.NoError(t, db.Create(&WarpConversation{
		ID: "c-1", OwnerID: "u-1", Title: "how much did we spend?",
	}).Error)

	err = rollbackWarpConversationTables(db)
	require.Error(t, err, "rollback must refuse while saved conversations exist")
	assert.Contains(t, err.Error(), "non-rollbackable")
	assert.True(t, db.Migrator().HasTable(&WarpConversation{}),
		"a refused rollback must leave the table intact")

	var surviving int64
	require.NoError(t, db.Model(&WarpConversation{}).Count(&surviving).Error)
	assert.EqualValues(t, 1, surviving, "the saved conversation must survive")
}

// TestMigrationAddWarpMessageOutcomeColumns_RollbackGuardsRecordedUsage pins
// that rolling the outcome columns back is refused once they hold anything.
//
// These are not pure schema. finish_reason is what marks an answer partial, and
// total_tokens/cost are the recorded spend for a saved chat, so dropping a
// populated set destroys a record rather than reversing a migration. Empty is
// still reversible, which keeps a failed upgrade recoverable.
func TestMigrationAddWarpMessageOutcomeColumns_RollbackGuardsRecordedUsage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, db.AutoMigrate(&WarpConversation{}, &WarpMessage{}))
	require.NoError(t, migrationAddWarpMessageOutcomeColumns(ctx, db, testLogger{}))
	for _, column := range warpMessageOutcomeColumns {
		require.True(t, db.Migrator().HasColumn(&WarpMessage{}, column.column))
	}

	// A message with no recorded outcome: the rollback is a genuine reversal.
	require.NoError(t, db.Create(&WarpMessage{ID: "m-plain", ConversationID: "c-1", Role: "user", Content: "q"}).Error)
	require.NoError(t, rollbackWarpMessageOutcomeColumns(db))
	require.False(t, db.Migrator().HasColumn(&WarpMessage{}, "cost"),
		"columns holding nothing are safe to drop")

	// Re-add them, then record a turn's spend. Now the drop would destroy it.
	require.NoError(t, db.AutoMigrate(&WarpMessage{}))
	require.NoError(t, db.Create(&WarpMessage{
		ID: "m-answer", ConversationID: "c-1", Role: "assistant", Content: "a",
		FinishReason: "partial", TotalTokens: 1200, Cost: 0.042,
	}).Error)

	err = rollbackWarpMessageOutcomeColumns(db)
	require.Error(t, err, "rollback must refuse while a recorded outcome exists")
	assert.Contains(t, err.Error(), "non-rollbackable")
	assert.True(t, db.Migrator().HasColumn(&WarpMessage{}, "cost"),
		"a refused rollback must leave the columns intact")

	var got WarpMessage
	require.NoError(t, db.Where("id = ?", "m-answer").First(&got).Error)
	assert.Equal(t, 1200, got.TotalTokens, "the recorded usage must survive")
	assert.InDelta(t, 0.042, got.Cost, 1e-9)
}

// The cross-owner sweep needs an index it can actually use.
//
// DeleteWarpConversationsOlderThan filters on updated_at alone, and the
// composite index leads with owner_id, so it cannot serve a bare range lookup -
// the sweep scans the whole table every hour on a deployment where most rows
// are not stale. AutoMigrate only creates the composite one, and the table
// migration is write-once, so existing installs never get this without a
// migration of its own.
func TestMigrationAddWarpConversationsUpdatedAtIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, migrationAddWarpConversationTables(ctx, db, testLogger{}))
	require.NoError(t, migrationAddWarpConversationsUpdatedAtIndex(ctx, db, testLogger{}))
	require.True(t, db.Migrator().HasIndex(&WarpConversation{}, "idx_warp_conversations_updated_at"),
		"the retention sweep filters on updated_at alone and needs it leading")

	// Idempotent: the helper runs on every boot and must not fail on an index
	// that is already there.
	require.NoError(t, migrationAddWarpConversationsUpdatedAtIndex(ctx, db, testLogger{}))
	require.True(t, db.Migrator().HasIndex(&WarpConversation{}, "idx_warp_conversations_updated_at"))
}

// sqlRecorder captures every statement gorm executes, so a test can assert on
// statement order - here, that rollback takes its locks in writer order.
type sqlRecorder struct {
	logger.Interface
	statements []string
}

func (r *sqlRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	r.statements = append(r.statements, sql)
}

// The rollback must lock warp_conversations before warp_messages.
//
// AppendWarpMessages locks the conversation row first and then inserts
// messages, so a rollback that grabs ACCESS EXCLUSIVE on warp_messages first
// acquires the same two tables in the opposite order - a textbook PostgreSQL
// deadlock, with one side aborted by the server. The drop order stays
// child-before-parent; only the lock order follows the writers.
func TestWarpRollbackLocksTablesInWriterOrder(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}
	ctx := context.Background()

	// The table migration is ID-gated in the shared migrations table, and this
	// test calls the rollback function directly (no RollbackLast), so the ID row
	// must be cleared or a repeat run skips creation and finds no tables.
	db.Exec("DROP TABLE IF EXISTS warp_messages")
	db.Exec("DROP TABLE IF EXISTS warp_conversations")
	db.Exec("DELETE FROM migrations WHERE id = 'logs_add_warp_conversation_tables'")
	require.NoError(t, migrationAddWarpConversationTables(ctx, db, testLogger{}))
	t.Cleanup(func() {
		db.Exec("DROP TABLE IF EXISTS warp_messages")
		db.Exec("DROP TABLE IF EXISTS warp_conversations")
		db.Exec("DELETE FROM migrations WHERE id = 'logs_add_warp_conversation_tables'")
	})

	recorder := &sqlRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	session := db.Session(&gorm.Session{Logger: recorder})
	require.NoError(t, session.Transaction(rollbackWarpConversationTables))

	conversationsLock, messagesLock := -1, -1
	for i, sql := range recorder.statements {
		switch sql {
		case "LOCK TABLE warp_conversations IN ACCESS EXCLUSIVE MODE":
			conversationsLock = i
		case "LOCK TABLE warp_messages IN ACCESS EXCLUSIVE MODE":
			messagesLock = i
		}
	}
	require.NotEqual(t, -1, conversationsLock, "rollback must lock warp_conversations")
	require.NotEqual(t, -1, messagesLock, "rollback must lock warp_messages")
	require.Less(t, conversationsLock, messagesLock,
		"locks must follow writer order (conversation first), or a concurrent append can deadlock the rollback")
}

// ========== Embedding Input Column Migration Tests ==========

// runEmbeddingInputColumnCases pins that the migration only adds the column and leaves existing rows NULL.
func runEmbeddingInputColumnCases(t *testing.T, db *gorm.DB) {
	t.Helper()
	db.Exec("DROP TABLE IF EXISTS logs")
	db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)")
	db.Exec("DELETE FROM migrations WHERE id = 'logs_add_embedding_input_column'")
	require.NoError(t, db.Exec(`CREATE TABLE logs (id VARCHAR(255) PRIMARY KEY, object_type VARCHAR(255) NOT NULL, input_history TEXT)`).Error)
	t.Cleanup(func() {
		db.Exec("DROP TABLE IF EXISTS logs")
		db.Exec("DELETE FROM migrations WHERE id = 'logs_add_embedding_input_column'")
	})

	history := `[{"role":"user","content":[{"type":"text","text":"hello"}]}]`
	require.NoError(t, db.Exec("INSERT INTO logs (id, object_type, input_history) VALUES (?, ?, ?)", "emb-1", "embedding", history).Error)

	ctx := context.Background()
	require.NoError(t, migrationAddEmbeddingInputColumn(ctx, db, testLogger{}))
	require.True(t, db.Migrator().HasColumn(&Log{}, "embedding_input"))

	var result struct {
		EmbeddingInput *string `gorm:"column:embedding_input"`
	}
	require.NoError(t, db.Table("logs").Select("embedding_input").Where("id = ?", "emb-1").Scan(&result).Error)
	assert.Nil(t, result.EmbeddingInput, "historical rows are not backfilled; the UI falls back to input_history")

	require.NoError(t, migrationAddEmbeddingInputColumn(ctx, db, testLogger{}), "re-run should be a no-op")
}

func TestMigrationAddEmbeddingInputColumn_Postgres(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}
	runEmbeddingInputColumnCases(t, db)
}

func TestMigrationAddEmbeddingInputColumn_SQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	runEmbeddingInputColumnCases(t, db)
}

// TestAdvisoryKeyHalves pins how a bigint advisory key maps onto the
// (classid, objid) pair pg_locks reports, which the migration probe queries by.
func TestAdvisoryKeyHalves(t *testing.T) {
	classid, objid := advisoryKeyHalves(migrationAdvisoryLockKey)
	assert.Equal(t, int64(0), classid)
	assert.Equal(t, int64(migrationAdvisoryLockKey), objid)

	classid, objid = advisoryKeyHalves(int64(7)<<32 | 42)
	assert.Equal(t, int64(7), classid)
	assert.Equal(t, int64(42), objid)
}

// TestMigrationLockProbeNonPostgresNeverReportsMigration pins the fail-open
// defaults: no store, no database, or a non-Postgres dialect all read as
// "not migrating" without issuing any query.
func TestMigrationLockProbeNonPostgresNeverReportsMigration(t *testing.T) {
	var nilProbe *migrationLockProbe
	assert.False(t, nilProbe.inProgress(context.Background()))
	assert.False(t, newMigrationLockProbe(nil, testLogger{}).inProgress(context.Background()))

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "probe.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store := &RDBLogStore{db: db, logger: testLogger{}}
	assert.False(t, store.MigrationInProgress(context.Background()))
	assert.False(t, MigrationInProgressFor(context.Background(), store))
	assert.False(t, MigrationInProgressFor(context.Background(), struct{}{}), "a value without the gate reads as not migrating")
}

// TestMigrationLockProbeCachesWithinTTL pins that one answer is reused for
// maintenanceCheckTTL so many pollers cost the database one query per TTL.
func TestMigrationLockProbeCachesWithinTTL(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	probe := &migrationLockProbe{now: func() time.Time { return now }, checkedAt: now, held: true}
	assert.True(t, probe.inProgress(context.Background()), "a fresh cached answer is served without a database")

	now = now.Add(maintenanceCheckTTL)
	assert.False(t, probe.inProgress(context.Background()), "an expired cache falls through to the (absent) database and fails open")
}

// TestMigrationInProgressTracksAdvisoryLockPostgres pins the live probe: it
// sees the migration lock held by another session, ignores other keys, clears
// when the holder goes away, and fails open when the database is unreachable.
func TestMigrationInProgressTracksAdvisoryLockPostgres(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}
	store := &RDBLogStore{db: db, logger: testLogger{}}
	fresh := func() bool {
		probe := store.migrationProbe()
		probe.mu.Lock()
		probe.checkedAt = time.Time{}
		probe.mu.Unlock()
		return store.MigrationInProgress(context.Background())
	}

	require.False(t, fresh(), "no lock held yet")

	holderDB, holder := acquireTestAdvisoryLockOnIsolatedPool(t, migrationAdvisoryLockKey)
	require.True(t, fresh(), "migration lock held by another session must be visible")
	assert.True(t, store.MigrationInProgress(context.Background()), "cached answer within the TTL")
	closeTestAdvisoryLockSession(t, holderDB, holder)
	require.Eventually(t, probeReports(store, false), 5*time.Second, 50*time.Millisecond, "lock must clear once the holder's session ends")

	indexDB, indexHolder := acquireTestAdvisoryLockOnIsolatedPool(t, indexAdvisoryLockKey)
	require.False(t, fresh(), "a different advisory key (background index build) must not read as a migration")
	closeTestAdvisoryLockSession(t, indexDB, indexHolder)

	closedDB := trySetupPostgresDB(t)
	require.NotNil(t, closedDB)
	closedSQL, err := closedDB.DB()
	require.NoError(t, err)
	require.NoError(t, closedSQL.Close())
	assert.False(t, newMigrationLockProbe(closedDB, testLogger{}).inProgress(context.Background()), "an unreachable database fails open")
}

// probeReports returns a poll function that resets the probe cache and reports
// whether MigrationInProgress equals want.
func probeReports(store *RDBLogStore, want bool) func() bool {
	return func() bool {
		probe := store.migrationProbe()
		probe.mu.Lock()
		probe.checkedAt = time.Time{}
		probe.mu.Unlock()
		return store.MigrationInProgress(context.Background()) == want
	}
}

// TestMigrationLockProbeCachesFromQueryCompletion pins that the TTL starts when
// the answer arrives, not when the query was issued: a slow probe must not leave
// an already-expired answer for the callers queued behind it, or each of them
// would run its own slow query in turn.
func TestMigrationLockProbeCachesFromQueryCompletion(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	queries := 0
	probe := &migrationLockProbe{
		now: func() time.Time { return now },
		query: func(context.Context) (bool, error) {
			queries++
			now = now.Add(maintenanceCheckTTL + 500*time.Millisecond) // a slow round trip
			return true, nil
		},
	}
	assert.True(t, probe.inProgress(context.Background()))
	assert.True(t, probe.inProgress(context.Background()), "the answer that just arrived is served from cache")
	assert.Equal(t, 1, queries, "a slow probe must not make the next caller query again")
}

// TestMigrationLockProbeWaitersHonourContext pins that a caller whose context
// ends while another caller's query is in flight returns at once (fail open)
// instead of waiting for that probe, so shutdown never waits on the database.
func TestMigrationLockProbeWaitersHonourContext(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer releaseOnce()
	probe := &migrationLockProbe{
		now: time.Now,
		query: func(context.Context) (bool, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return true, nil
		},
	}
	first := make(chan bool, 1)
	go func() { first <- probe.inProgress(context.Background()) }()
	<-entered

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	second := make(chan bool, 1)
	go func() { second <- probe.inProgress(cancelled) }()
	select {
	case got := <-second:
		assert.False(t, got, "a caller whose context ended must fail open")
	case <-time.After(time.Second):
		t.Fatal("a caller with an ended context waited for another caller's probe")
	}
	releaseOnce()
	assert.True(t, <-first, "the in-flight probe still completes normally")
}
