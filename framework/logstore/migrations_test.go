package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
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

	// Drop existing tables and migration tracking in the correct order
	// Note: The migrator uses "migrations" table by default, not "gomigrate"
	db.Exec("DROP INDEX IF EXISTS idx_logs_metadata_gin")
	db.Exec("DROP TABLE IF EXISTS logs")
	db.Exec("DROP TABLE IF EXISTS migrations")

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
		db.Exec("DROP INDEX IF EXISTS idx_logs_metadata_gin")
		db.Exec("DROP TABLE IF EXISTS logs")
		db.Exec("DROP TABLE IF EXISTS migrations")
	})
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

	// Insert logs with valid JSON metadata
	validJSON1 := `{"key": "value"}`
	validJSON2 := `{"nested": {"foo": "bar"}, "array": [1, 2, 3]}`
	validJSON3 := `[1, 2, 3]`
	validJSON4 := `{"number": 42, "bool": true, "null": null}`

	insertTestLog(t, db, "log-valid-1", &validJSON1)
	insertTestLog(t, db, "log-valid-2", &validJSON2)
	insertTestLog(t, db, "log-valid-3", &validJSON3)
	insertTestLog(t, db, "log-valid-4", &validJSON4)

	// Run the migration
	err := migrationAddMetadataGINIndex(ctx, db)
	require.NoError(t, err, "Migration should succeed")

	// Verify all valid JSON values are preserved
	meta1 := getMetadataValue(t, db, "log-valid-1")
	assert.NotNil(t, meta1, "Valid JSON should be preserved")
	assert.Equal(t, validJSON1, *meta1)

	meta2 := getMetadataValue(t, db, "log-valid-2")
	assert.NotNil(t, meta2, "Valid JSON should be preserved")
	assert.Equal(t, validJSON2, *meta2)

	meta3 := getMetadataValue(t, db, "log-valid-3")
	assert.NotNil(t, meta3, "Valid JSON array should be preserved")
	assert.Equal(t, validJSON3, *meta3)

	meta4 := getMetadataValue(t, db, "log-valid-4")
	assert.NotNil(t, meta4, "Valid JSON with various types should be preserved")
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

	// Insert logs with invalid JSON metadata that would pass the old regex but fail ::jsonb cast
	invalid1 := `{"key": invalid}`            // Unquoted value
	invalid2 := `{key: "value"}`              // Unquoted key
	invalid3 := `{"key": "value",}`           // Trailing comma
	invalid4 := `just a string`               // Plain text
	invalid5 := ``                            // Empty string
	invalid6 := `{"unclosed": "brace"`        // Unclosed brace
	invalid7 := `{"key": undefined}`          // JavaScript undefined
	invalid8 := `{'single': 'quotes'}`        // Single quotes
	invalid9 := `[NULL]`                      // Literal string [NULL] (not valid JSON)
	invalid10 := `NULL`                       // Literal string NULL (not valid JSON)
	invalid11 := `null`                       // Valid JSON null (lowercase)

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
	insertTestLog(t, db, "log-actual-null", nil) // Actual SQL NULL

	// Run the migration
	err := migrationAddMetadataGINIndex(ctx, db)
	require.NoError(t, err, "Migration should succeed even with invalid JSON")

	// Verify invalid JSON values were set to NULL
	for i := 1; i <= 10; i++ {
		var id string
		if i < 10 {
			id = "log-invalid-" + string(rune('0'+i))
		} else {
			id = "log-invalid-10"
		}
		meta := getMetadataValue(t, db, id)
		assert.Nil(t, meta, "Invalid JSON for %s should be set to NULL", id)
	}

	// Verify valid JSON "null" is preserved (it's valid JSON)
	metaNull := getMetadataValue(t, db, "log-invalid-11")
	assert.NotNil(t, metaNull, "Valid JSON null should be preserved")
	assert.Equal(t, "null", *metaNull)

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

	// Run the migration
	err := migrationAddMetadataGINIndex(ctx, db)
	require.NoError(t, err, "Migration should succeed")

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

	// Run the migration first time
	err := migrationAddMetadataGINIndex(ctx, db)
	require.NoError(t, err, "First migration should succeed")

	// Verify index exists
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should exist after first migration")

	// Verify metadata is preserved
	meta1 := getMetadataValue(t, db, "log-idempotent")
	assert.NotNil(t, meta1)
	assert.Equal(t, validJSON, *meta1)

	// Run the migration second time (should be idempotent due to gomigrate tracking)
	err = migrationAddMetadataGINIndex(ctx, db)
	require.NoError(t, err, "Second migration should succeed (idempotent)")

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

	// Run the migration on an empty table
	err := migrationAddMetadataGINIndex(ctx, db)
	require.NoError(t, err, "Migration should succeed on empty table")

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

	// Test edge cases that might be tricky
	emptyObject := `{}`
	emptyArray := `[]`
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

	// Run the migration
	err := migrationAddMetadataGINIndex(ctx, db)
	require.NoError(t, err, "Migration should succeed")

	// Verify all edge cases are handled correctly
	// Empty object and array should be preserved
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-empty-obj"), "Empty object should be preserved")
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-empty-arr"), "Empty array should be preserved")

	// Whitespace JSON should be preserved (Postgres handles it)
	meta := getMetadataValue(t, db, "log-edge-whitespace")
	assert.NotNil(t, meta, "Whitespace JSON should be preserved")

	// Unicode should be preserved
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-unicode"), "Unicode JSON should be preserved")

	// Large numbers and scientific notation should be preserved
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-large-num"), "Large number JSON should be preserved")
	assert.NotNil(t, getMetadataValue(t, db, "log-edge-scientific"), "Scientific notation JSON should be preserved")

	// Verify the GIN index was created
	assert.True(t, indexExists(t, db, "idx_logs_metadata_gin"), "GIN index should be created")
}
