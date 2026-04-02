package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// postgresDSN matches the postgres service in tests/docker-compose.yml and
// framework/docker-compose.yml.
const postgresDSN = "host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable"

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
	if err := sqlDB.Ping(); err != nil {
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
	err = migrationAddMetadataGINIndex(ctx, db)
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
	err = migrationAddMetadataGINIndex(ctx, db)
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
	err := migrationAddMetadataGINIndex(ctx, db)
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
	err := migrationAddMetadataGINIndex(ctx, db)
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
	err = migrationAddMetadataGINIndex(ctx, db)
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
	err := migrationAddMetadataGINIndex(ctx, db)
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
	err := migrationAddMetadataGINIndex(ctx, db)
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

// ========== Embedding Input Column Migration Tests ==========

// setupLogsTableForEmbeddingInputTest creates a minimal logs table without the
// embedding_input column, simulating the pre-migration state.
func setupLogsTableForEmbeddingInputTest(t *testing.T, db *gorm.DB) {
	t.Helper()

	dialect := db.Dialector.Name()

	db.Exec("DROP TABLE IF EXISTS logs")
	db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)")
	db.Exec("DELETE FROM migrations WHERE id = 'logs_add_embedding_input_column'")

	var createSQL string
	switch dialect {
	case "postgres":
		createSQL = `CREATE TABLE logs (
			id VARCHAR(255) PRIMARY KEY,
			object_type VARCHAR(255) NOT NULL,
			input_history TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`
	default:
		createSQL = `CREATE TABLE logs (
			id TEXT PRIMARY KEY,
			object_type TEXT NOT NULL,
			input_history TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`
	}

	err := db.Exec(createSQL).Error
	require.NoError(t, err, "Failed to create logs table")

	t.Cleanup(func() {
		db.Exec("DROP TABLE IF EXISTS logs")
		db.Exec("DELETE FROM migrations WHERE id = 'logs_add_embedding_input_column'")
	})
}

// setupSQLiteInMemoryDB opens an in-memory SQLite database for testing.
func setupSQLiteInMemoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "Failed to open in-memory SQLite DB")
	return db
}

// insertTestEmbeddingLog inserts a log row with given object_type and input_history.
func insertTestEmbeddingLog(t *testing.T, db *gorm.DB, id, objectType string, inputHistory *string) {
	t.Helper()
	err := db.Exec(
		"INSERT INTO logs (id, object_type, input_history) VALUES (?, ?, ?)",
		id, objectType, inputHistory,
	).Error
	require.NoError(t, err, "Failed to insert test log %s", id)
}

// getEmbeddingInputValue reads the embedding_input column for a given log id.
func getEmbeddingInputValue(t *testing.T, db *gorm.DB, id string) *string {
	t.Helper()
	var result struct {
		EmbeddingInput *string `gorm:"column:embedding_input"`
	}
	err := db.Table("logs").Select("embedding_input").Where("id = ?", id).Scan(&result).Error
	require.NoError(t, err, "Failed to get embedding_input for log %s", id)
	return result.EmbeddingInput
}

// embeddingInputHistory builds the JSON stored in input_history for embedding logs
// in the old format (before this migration): a single ChatMessage with text content blocks.
// This mirrors what extractInputHistory produced for embedding requests on the main branch.
func embeddingInputHistory(texts []string) string {
	blocks := make([]map[string]string, len(texts))
	for i, text := range texts {
		blocks[i] = map[string]string{"type": "text", "text": text}
	}
	data, _ := json.Marshal([]interface{}{
		map[string]interface{}{"role": "user", "content": blocks},
	})
	return string(data)
}

// parseEmbeddingInput parses the JSON in embedding_input into [][]map[string]interface{},
// matching the shape of []schemas.EmbeddingContent (each inner slice is one EmbeddingContent).
func parseEmbeddingInput(t *testing.T, raw string) [][]map[string]interface{} {
	t.Helper()
	var result [][]map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &result), "Failed to parse embedding_input JSON: %s", raw)
	return result
}

// runEmbeddingInputMigrationCases exercises all correctness scenarios against the given DB.
// runMigration is called in place of a bare migrationAddEmbeddingInputColumn so that the
// Postgres variant can also invoke ensureEmbeddingInputBackfill in the same step.
func runEmbeddingInputMigrationCases(t *testing.T, db *gorm.DB, runMigration func(ctx context.Context) error) {
	t.Helper()

	t.Run("SingleText", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		history := embeddingInputHistory([]string{"hello world"})
		insertTestEmbeddingLog(t, db, "emb-1", "embedding", &history)

		require.NoError(t, runMigration(ctx))

		val := getEmbeddingInputValue(t, db, "emb-1")
		require.NotNil(t, val, "embedding_input should be set after migration")

		parsed := parseEmbeddingInput(t, *val)
		require.Len(t, parsed, 1, "one EmbeddingContent per text")
		require.Len(t, parsed[0], 1, "one part in EmbeddingContent")
		assert.Equal(t, "text", parsed[0][0]["type"])
		assert.Equal(t, "hello world", parsed[0][0]["text"])
	})

	t.Run("MultipleTexts", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		texts := []string{"first", "second", "third"}
		history := embeddingInputHistory(texts)
		insertTestEmbeddingLog(t, db, "emb-multi", "embedding", &history)

		require.NoError(t, runMigration(ctx))

		val := getEmbeddingInputValue(t, db, "emb-multi")
		require.NotNil(t, val)

		parsed := parseEmbeddingInput(t, *val)
		require.Len(t, parsed, 3, "one EmbeddingContent per input text")
		for i, text := range texts {
			require.Len(t, parsed[i], 1)
			assert.Equal(t, "text", parsed[i][0]["type"])
			assert.Equal(t, text, parsed[i][0]["text"])
		}
	})

	t.Run("NullInputHistorySkipped", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		insertTestEmbeddingLog(t, db, "emb-null", "embedding", nil)

		require.NoError(t, runMigration(ctx))

		assert.Nil(t, getEmbeddingInputValue(t, db, "emb-null"),
			"embedding_input should stay NULL when input_history is NULL")
	})

	t.Run("EmptyInputHistoryVariantsSkipped", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		for id, h := range map[string]string{
			"emb-empty":    "",
			"emb-emptyarr": "[]",
			"emb-nullstr":  "null",
		} {
			hh := h
			insertTestEmbeddingLog(t, db, id, "embedding", &hh)
		}

		require.NoError(t, runMigration(ctx))

		for _, id := range []string{"emb-empty", "emb-emptyarr", "emb-nullstr"} {
			assert.Nil(t, getEmbeddingInputValue(t, db, id),
				"embedding_input should remain NULL for id=%s", id)
		}
	})

	t.Run("NonEmbeddingRowsUntouched", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		chatHistory := embeddingInputHistory([]string{"some text"})
		insertTestEmbeddingLog(t, db, "chat-1", "chat.completion", &chatHistory)
		insertTestEmbeddingLog(t, db, "text-1", "text.completion", &chatHistory)

		embHistory := embeddingInputHistory([]string{"embed me"})
		insertTestEmbeddingLog(t, db, "emb-1", "embedding", &embHistory)

		require.NoError(t, runMigration(ctx))

		assert.Nil(t, getEmbeddingInputValue(t, db, "chat-1"), "chat.completion should not be touched")
		assert.Nil(t, getEmbeddingInputValue(t, db, "text-1"), "text.completion should not be touched")
		assert.NotNil(t, getEmbeddingInputValue(t, db, "emb-1"), "embedding row should be backfilled")
	})

	t.Run("ExistingEmbeddingInputPreserved", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		history := embeddingInputHistory([]string{"original"})
		insertTestEmbeddingLog(t, db, "emb-exists", "embedding", &history)

		// First run populates embedding_input
		require.NoError(t, runMigration(ctx))
		val1 := getEmbeddingInputValue(t, db, "emb-exists")
		require.NotNil(t, val1)

		// Change input_history and force re-run by clearing migration tracking
		newHistory := embeddingInputHistory([]string{"changed"})
		require.NoError(t, db.Exec("UPDATE logs SET input_history = ? WHERE id = 'emb-exists'", newHistory).Error)
		db.Exec("DELETE FROM migrations WHERE id = 'logs_add_embedding_input_column'")

		require.NoError(t, runMigration(ctx))

		// embedding_input must not change since it is no longer NULL
		val2 := getEmbeddingInputValue(t, db, "emb-exists")
		require.NotNil(t, val2)
		assert.Equal(t, *val1, *val2, "non-NULL embedding_input must not be overwritten")
	})

	t.Run("Idempotent", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		history := embeddingInputHistory([]string{"idempotent"})
		insertTestEmbeddingLog(t, db, "emb-idem", "embedding", &history)

		require.NoError(t, migrationAddEmbeddingInputColumn(ctx, db), "first run")
		val1 := getEmbeddingInputValue(t, db, "emb-idem")

		require.NoError(t, migrationAddEmbeddingInputColumn(ctx, db), "second run should be a no-op")
		val2 := getEmbeddingInputValue(t, db, "emb-idem")

		require.NotNil(t, val1)
		require.NotNil(t, val2)
		assert.Equal(t, *val1, *val2, "embedding_input unchanged after idempotent re-run")
	})

	t.Run("EmptyTextBlocksFiltered", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		// Input history has empty-string text blocks mixed in with valid ones.
		blocks := []map[string]string{
			{"type": "text", "text": "valid"},
			{"type": "text", "text": ""},          // empty — must be filtered out
			{"type": "text", "text": "also valid"},
		}
		data, _ := json.Marshal([]interface{}{
			map[string]interface{}{"role": "user", "content": blocks},
		})
		history := string(data)
		insertTestEmbeddingLog(t, db, "emb-filter", "embedding", &history)

		require.NoError(t, runMigration(ctx))

		val := getEmbeddingInputValue(t, db, "emb-filter")
		require.NotNil(t, val)

		parsed := parseEmbeddingInput(t, *val)
		require.Len(t, parsed, 2, "empty text block must be filtered out")
		assert.Equal(t, "valid", parsed[0][0]["text"])
		assert.Equal(t, "also valid", parsed[1][0]["text"])
	})

	t.Run("EmptyTable", func(t *testing.T) {
		setupLogsTableForEmbeddingInputTest(t, db)
		ctx := context.Background()

		require.NoError(t, migrationAddEmbeddingInputColumn(ctx, db), "migration on empty table should succeed")
	})
}

func TestMigrationAddEmbeddingInputColumn_Postgres(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	runEmbeddingInputMigrationCases(t, db, func(ctx context.Context) error {
		if err := migrationAddEmbeddingInputColumn(ctx, db); err != nil {
			return err
		}
		return ensureEmbeddingInputBackfill(ctx, conn)
	})
}

func TestMigrationAddEmbeddingInputColumn_SQLite(t *testing.T) {
	db := setupSQLiteInMemoryDB(t)
	runEmbeddingInputMigrationCases(t, db, func(ctx context.Context) error {
		return migrationAddEmbeddingInputColumn(ctx, db)
	})
}
