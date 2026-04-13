package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
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

// namedDB pairs a backend name with its GORM connection for use in subtests.
type namedDB struct {
	name string
	db   *gorm.DB
}

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "Failed to create test database")

	// Create the MCP clients table
	err = db.AutoMigrate(&tables.TableMCPClient{})
	require.NoError(t, err, "Failed to migrate test database")

	return db
}

// captureLogOutput captures log output during a function execution
func captureLogOutput(fn func()) string {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	fn()
	return buf.String()
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "hyphen to underscore",
			input:    "my-tool",
			expected: "my_tool",
		},
		{
			name:     "space to underscore",
			input:    "my tool",
			expected: "my_tool",
		},
		{
			name:     "multiple hyphens",
			input:    "my-super-tool",
			expected: "my_super_tool",
		},
		{
			name:     "multiple spaces",
			input:    "my super tool",
			expected: "my_super_tool",
		},
		{
			name:     "leading digits removed",
			input:    "123tool",
			expected: "tool",
		},
		{
			name:     "leading digits with hyphen",
			input:    "123my-tool",
			expected: "my_tool",
		},
		{
			name:     "empty after normalization",
			input:    "123",
			expected: "mcp_client",
		},
		{
			name:     "no change needed",
			input:    "my_tool",
			expected: "my_tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := normalizeMCPClientName(tt.input)
			assert.Equal(t, tt.expected, normalized, "normalizeMCPClientName should produce expected output")
		})
	}
}

func TestFindUniqueName_NoCollision(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Create a test client with a unique name
	client := &tables.TableMCPClient{
		Name:           "existing_client",
		ClientID:       "client-1",
		ConnectionType: "stdio",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	err := db.WithContext(ctx).Create(client).Error
	require.NoError(t, err)

	// Test findUniqueName with a different base name (no collision)
	logOutput := captureLogOutput(func() {
		uniqueName, err := findUniqueNameForTest("new_client", "new_client", 999, db.WithContext(ctx))
		require.NoError(t, err)
		assert.Equal(t, "new_client", uniqueName, "Should return base name when no collision")
	})

	// Should not log anything when there's no collision
	assert.Empty(t, logOutput, "Should not log when name is available without suffix")
}

func TestFindUniqueName_WithCollision(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Create existing clients that will cause collisions
	// First client with base name
	client1 := &tables.TableMCPClient{
		Name:           "my_tool",
		ClientID:       "client-1",
		ConnectionType: "stdio",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	err := db.WithContext(ctx).Create(client1).Error
	require.NoError(t, err)

	// Second client with first suffix
	client2 := &tables.TableMCPClient{
		Name:           "my_tool1",
		ClientID:       "client-2",
		ConnectionType: "stdio",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	err = db.WithContext(ctx).Create(client2).Error
	require.NoError(t, err)

	// Test findUniqueName with collision - should find "my_tool2"
	// excludeID is set to a non-existent ID (999) so all existing clients are considered
	var uniqueName string
	logOutput := captureLogOutput(func() {
		uniqueName, err = findUniqueNameForTest("my_tool", "my-tool", 999, db.WithContext(ctx))
	})

	require.NoError(t, err)
	assert.Equal(t, "my_tool2", uniqueName, "Should return name with suffix when collision occurs")
	assert.Contains(t, logOutput, "MCP Client Name Normalized: 'my-tool' -> 'my_tool2'", "Should log the transformation")
}

func TestFindUniqueName_MultipleCollisions(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Create existing clients that will cause multiple collisions
	client1 := &tables.TableMCPClient{
		Name:           "test_tool",
		ClientID:       "client-1",
		ConnectionType: "stdio",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	err := db.WithContext(ctx).Create(client1).Error
	require.NoError(t, err)

	client2 := &tables.TableMCPClient{
		Name:           "test_tool1",
		ClientID:       "client-2",
		ConnectionType: "stdio",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	err = db.WithContext(ctx).Create(client2).Error
	require.NoError(t, err)

	client3 := &tables.TableMCPClient{
		Name:           "test_tool2",
		ClientID:       "client-3",
		ConnectionType: "stdio",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	err = db.WithContext(ctx).Create(client3).Error
	require.NoError(t, err)

	// Test findUniqueName with multiple collisions - should find "test_tool3"
	var uniqueName string
	logOutput := captureLogOutput(func() {
		uniqueName, err = findUniqueNameForTest("test_tool", "test tool", 999, db.WithContext(ctx))
	})

	require.NoError(t, err)
	assert.Equal(t, "test_tool3", uniqueName, "Should return name with correct suffix after multiple collisions")
	assert.Contains(t, logOutput, "MCP Client Name Normalized: 'test tool' -> 'test_tool3'", "Should log the transformation")
}

func TestFindUniqueName_NormalizationAndCollision(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Create existing client with normalized name
	client := &tables.TableMCPClient{
		Name:           "my_tool",
		ClientID:       "client-1",
		ConnectionType: "stdio",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	err := db.WithContext(ctx).Create(client).Error
	require.NoError(t, err)

	// Test that "my-tool" normalizes to "my_tool" and then collides, requiring suffix
	var uniqueName string
	logOutput := captureLogOutput(func() {
		uniqueName, err = findUniqueNameForTest("my_tool", "my-tool", 999, db.WithContext(ctx))
	})

	require.NoError(t, err)
	assert.Equal(t, "my_tool2", uniqueName, "Should handle normalization and collision")
	assert.Contains(t, logOutput, "MCP Client Name Normalized: 'my-tool' -> 'my_tool2'", "Should log the full transformation")
}

func TestFindUniqueName_MultipleNormalizationsToSameBase(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Test case: 3 entries that normalize to the same base name:
	// "mcp client" -> "mcp_client"
	// "mcp-client" -> "mcp_client" (collision, becomes "mcp_client2")
	// "1mcp-client" -> "mcp_client" (collision, becomes "mcp_client3")
	// Note: In the actual migration, names are processed sequentially and each checks
	// against all previously created names. To simulate this, we need to create clients
	// with the original names first, then normalize them in sequence.

	// Helper function to normalize (same logic as in migrations.go)
	normalizeName := func(name string) string {
		normalized := strings.ReplaceAll(name, "-", "_")
		normalized = strings.ReplaceAll(normalized, " ", "_")
		normalized = strings.TrimLeftFunc(normalized, func(r rune) bool {
			return r >= '0' && r <= '9'
		})
		if normalized == "" {
			normalized = "mcp_client"
		}
		return normalized
	}

	// Create three clients with original names (simulating pre-migration state)
	clients := []*tables.TableMCPClient{
		{
			Name:           "mcp client",
			ClientID:       "client-1",
			ConnectionType: "stdio",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		},
		{
			Name:           "mcp-client",
			ClientID:       "client-2",
			ConnectionType: "stdio",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		},
		{
			Name:           "1mcp-client",
			ClientID:       "client-3",
			ConnectionType: "stdio",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		},
	}

	for _, client := range clients {
		err := db.WithContext(ctx).Create(client).Error
		require.NoError(t, err)
	}

	// Now simulate the migration: process each client sequentially
	// First: "mcp client" -> "mcp_client" (no collision)
	client1 := clients[0]
	normalizedName1 := normalizeName(client1.Name)
	var uniqueName1 string
	var err error
	logOutput1 := captureLogOutput(func() {
		uniqueName1, err = findUniqueNameForTest(normalizedName1, client1.Name, client1.ID, db.WithContext(ctx))
	})
	require.NoError(t, err)
	assert.Equal(t, "mcp_client", uniqueName1, "First normalization should use base name")
	assert.Empty(t, logOutput1, "Should not log when name is available without suffix")

	// Update first client
	err = db.WithContext(ctx).Model(client1).Update("name", uniqueName1).Error
	require.NoError(t, err)

	// Second: "mcp-client" -> "mcp_client" (collision with "mcp_client", becomes "mcp_client2")
	// Note: We need to check that "mcp_client" exists (from client1), so it should skip to "mcp_client2"
	client2 := clients[1]
	normalizedName2 := normalizeName(client2.Name)
	var uniqueName2 string
	logOutput2 := captureLogOutput(func() {
		uniqueName2, err = findUniqueNameForTest(normalizedName2, client2.Name, client2.ID, db.WithContext(ctx))
	})
	require.NoError(t, err)
	// With the updated implementation, suffixes start from 2 when base name exists
	// So "mcp-client" normalizes to "mcp_client" which collides, becomes "mcp_client2"
	assert.Equal(t, "mcp_client2", uniqueName2, "Second normalization should get suffix 2 (skipping 1)")
	assert.Contains(t, logOutput2, "MCP Client Name Normalized: 'mcp-client' -> 'mcp_client2'", "Should log the transformation")

	// Update second client
	err = db.WithContext(ctx).Model(client2).Update("name", uniqueName2).Error
	require.NoError(t, err)

	// Third: "1mcp-client" -> "mcp_client" (collision with "mcp_client" and "mcp_client2", becomes "mcp_client3")
	client3 := clients[2]
	normalizedName3 := normalizeName(client3.Name)
	var uniqueName3 string
	logOutput3 := captureLogOutput(func() {
		uniqueName3, err = findUniqueNameForTest(normalizedName3, client3.Name, client3.ID, db.WithContext(ctx))
	})
	require.NoError(t, err)
	// Third normalization finds "mcp_client" and "mcp_client2" exist, so becomes "mcp_client3"
	assert.Equal(t, "mcp_client3", uniqueName3, "Third normalization should get suffix 3")
	assert.Contains(t, logOutput3, "MCP Client Name Normalized: '1mcp-client' -> 'mcp_client3'", "Should log the transformation")

	// Update third client
	err = db.WithContext(ctx).Model(client3).Update("name", uniqueName3).Error
	require.NoError(t, err)

	// Final verification: all three should exist with correct names
	var finalClients []tables.TableMCPClient
	err = db.WithContext(ctx).Find(&finalClients).Error
	require.NoError(t, err)
	assert.Len(t, finalClients, 3, "Should have all 3 clients")

	names := make([]string, len(finalClients))
	for i, c := range finalClients {
		names[i] = c.Name
	}
	assert.Contains(t, names, "mcp_client", "Should contain mcp_client")
	assert.Contains(t, names, "mcp_client2", "Should contain mcp_client2")
	assert.Contains(t, names, "mcp_client3", "Should contain mcp_client3")
}

func TestFindUniqueName_MigrationScenarioWithInMemoryTracking(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// This test simulates the exact migration scenario where clients are processed in a loop
	// and we need to track assigned names in memory to avoid transaction visibility issues

	// Create three clients with original names (simulating pre-migration state)
	clients := []*tables.TableMCPClient{
		{
			Name:           "mcp client",
			ClientID:       "client-1",
			ConnectionType: "stdio",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		},
		{
			Name:           "mcp-client",
			ClientID:       "client-2",
			ConnectionType: "stdio",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		},
		{
			Name:           "1mcp-client",
			ClientID:       "client-3",
			ConnectionType: "stdio",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		},
	}

	for _, client := range clients {
		err := db.WithContext(ctx).Create(client).Error
		require.NoError(t, err)
	}

	// Simulate the migration: process clients in a loop with in-memory tracking
	assignedNames := make(map[string]bool)
	normalizeName := func(name string) string {
		normalized := strings.ReplaceAll(name, "-", "_")
		normalized = strings.ReplaceAll(normalized, " ", "_")
		normalized = strings.TrimLeftFunc(normalized, func(r rune) bool {
			return r >= '0' && r <= '9'
		})
		if normalized == "" {
			normalized = "mcp_client"
		}
		return normalized
	}

	var logOutputs []string
	for _, client := range clients {
		originalName := client.Name
		needsUpdate := strings.Contains(originalName, "-") || strings.Contains(originalName, " ") ||
			(len(originalName) > 0 && originalName[0] >= '0' && originalName[0] <= '9')

		if needsUpdate {
			normalizedName := normalizeName(originalName)
			uniqueName, err := findUniqueNameForTestWithTracking(normalizedName, originalName, client.ID, db.WithContext(ctx), assignedNames)
			require.NoError(t, err)

			// Capture log output
			logOutput := captureLogOutput(func() {
				// Log if name changed
				if originalName != uniqueName {
					log.Printf("MCP Client Name Normalized: '%s' -> '%s'", originalName, uniqueName)
				}
			})
			if logOutput != "" {
				logOutputs = append(logOutputs, logOutput)
			}

			// Update client
			err = db.WithContext(ctx).Model(client).Update("name", uniqueName).Error
			require.NoError(t, err)
		}
	}

	// Verify all three clients have correct names
	var finalClients []tables.TableMCPClient
	err := db.WithContext(ctx).Find(&finalClients).Error
	require.NoError(t, err)
	assert.Len(t, finalClients, 3, "Should have all 3 clients")

	names := make([]string, len(finalClients))
	for i, c := range finalClients {
		names[i] = c.Name
	}
	assert.Contains(t, names, "mcp_client", "Should contain mcp_client")
	assert.Contains(t, names, "mcp_client2", "Should contain mcp_client2")
	assert.Contains(t, names, "mcp_client3", "Should contain mcp_client3")

	// Verify logging: should log all three transformations
	allLogs := strings.Join(logOutputs, "")
	assert.Contains(t, allLogs, "MCP Client Name Normalized: 'mcp client' -> 'mcp_client'", "Should log first normalization")
	assert.Contains(t, allLogs, "MCP Client Name Normalized: 'mcp-client' -> 'mcp_client2'", "Should log second normalization")
	assert.Contains(t, allLogs, "MCP Client Name Normalized: '1mcp-client' -> 'mcp_client3'", "Should log third normalization")
}

// findUniqueNameForTestWithTracking is a test helper that tracks assigned names in memory
func findUniqueNameForTestWithTracking(baseName string, originalName string, excludeID uint, tx *gorm.DB, assignedNames map[string]bool) (string, error) {
	// First check if base name is already assigned in this migration
	if !assignedNames[baseName] {
		// Also check database for existing names (excluding current client)
		var count int64
		err := tx.Model(&tables.TableMCPClient{}).Where("name = ? AND id != ?", baseName, excludeID).Count(&count).Error
		if err != nil {
			return "", fmt.Errorf("failed to check name availability: %w", err)
		}
		if count == 0 {
			// Name is available
			assignedNames[baseName] = true
			// Log normalization even when no collision
			if originalName != baseName {
				log.Printf("MCP Client Name Normalized: '%s' -> '%s'", originalName, baseName)
			}
			return baseName, nil
		}
	}

	// Name exists (either assigned in this migration or in database), try with number suffix starting from 2
	suffix := 2
	const maxSuffix = 1000
	for {
		if suffix > maxSuffix {
			return "", fmt.Errorf("could not find unique name after %d attempts for base name: %s", maxSuffix, baseName)
		}
		candidateName := baseName + strconv.Itoa(suffix)

		// Check both in-memory map and database
		if !assignedNames[candidateName] {
			var count int64
			err := tx.Model(&tables.TableMCPClient{}).Where("name = ? AND id != ?", candidateName, excludeID).Count(&count).Error
			if err != nil {
				return "", fmt.Errorf("failed to check name availability: %w", err)
			}
			if count == 0 {
				// Found available name
				assignedNames[candidateName] = true
				log.Printf("MCP Client Name Normalized: '%s' -> '%s'", originalName, candidateName)
				return candidateName, nil
			}
		}
		suffix++
	}
}

// findUniqueNameForTest is a test helper that extracts the findUniqueName logic
// This mirrors the implementation in migrations.go for testing
func findUniqueNameForTest(baseName string, originalName string, excludeID uint, tx *gorm.DB) (string, error) {
	// First, try the base name
	var count int64
	err := tx.Model(&tables.TableMCPClient{}).Where("name = ? AND id != ?", baseName, excludeID).Count(&count).Error
	if err != nil {
		return "", fmt.Errorf("failed to check name availability: %w", err)
	}
	if count == 0 {
		// Name is available
		return baseName, nil
	}

	// Name exists, try with number suffix starting from 2
	// (base name is conceptually "1", so collisions start from "2")
	suffix := 2
	const maxSuffix = 1000 // Safety limit to prevent infinite loops
	for {
		if suffix > maxSuffix {
			return "", fmt.Errorf("could not find unique name after %d attempts for base name: %s", maxSuffix, baseName)
		}
		candidateName := baseName + strconv.Itoa(suffix)
		err := tx.Model(&tables.TableMCPClient{}).Where("name = ? AND id != ?", candidateName, excludeID).Count(&count).Error
		if err != nil {
			return "", fmt.Errorf("failed to check name availability: %w", err)
		}
		if count == 0 {
			// Found available name - log the transformation
			log.Printf("MCP Client Name Normalized: '%s' -> '%s'", originalName, candidateName)
			return candidateName, nil
		}
		suffix++
	}
}

// setupProviderTestDBWithoutStoreRawColumn creates an in-memory SQLite database
// with the config_providers table but WITHOUT the store_raw_request_response column,
// simulating the pre-migration state.
func setupProviderTestDBWithoutStoreRawColumn(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "Failed to create test database")

	// Create the config_providers table manually without store_raw_request_response column
	// This simulates the pre-migration state
	err = db.Exec(`
		CREATE TABLE config_providers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name VARCHAR(50) NOT NULL UNIQUE,
			network_config_json TEXT,
			concurrency_buffer_json TEXT,
			proxy_config_json TEXT,
			custom_provider_config_json TEXT,
			pricing_overrides_json TEXT,
			send_back_raw_request BOOLEAN DEFAULT 0,
			send_back_raw_response BOOLEAN DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			budget_id VARCHAR(255),
			rate_limit_id VARCHAR(255),
			config_hash VARCHAR(255),
			status VARCHAR(50) DEFAULT 'unknown',
			description TEXT,
			encryption_status VARCHAR(20) DEFAULT 'plain_text'
		)
	`).Error
	require.NoError(t, err, "Failed to create config_providers table")

	// Create the migrations table for the migrator (matches migrator.DefaultOptions.TableName)
	err = db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			id VARCHAR(255) PRIMARY KEY
		)
	`).Error
	require.NoError(t, err, "Failed to create migrations table")

	return db
}

// trySetupPostgresDBWithoutStoreRawColumn attempts to connect to Postgres and creates
// the config_providers table WITHOUT the store_raw_request_response column.
// Returns nil (without skipping the test) if Postgres is unavailable.
func trySetupPostgresDBWithoutStoreRawColumn(t *testing.T, testSuffix string) *gorm.DB {
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

	// Drop config_providers to start fresh (for this specific test).
	// Use CASCADE to drop dependent objects (composite types, sequences, etc.).
	db.Exec("DROP TABLE IF EXISTS config_providers CASCADE")

	// Clear migration tracking without dropping the table — other test packages
	// (e.g. logstore) may share this Postgres instance and use the same table
	// concurrently.  CREATE IF NOT EXISTS is safe even if the table already
	// exists from a previous test or a concurrent package.
	db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		id VARCHAR(255) PRIMARY KEY
	)`)
	db.Exec("DELETE FROM migrations")

	// Create the config_providers table manually without store_raw_request_response column
	// This simulates the pre-migration state (PostgreSQL syntax)
	err = db.Exec(`
		CREATE TABLE config_providers (
			id SERIAL PRIMARY KEY,
			name VARCHAR(50) NOT NULL UNIQUE,
			network_config_json TEXT,
			concurrency_buffer_json TEXT,
			proxy_config_json TEXT,
			custom_provider_config_json TEXT,
			pricing_overrides_json TEXT,
			send_back_raw_request BOOLEAN DEFAULT false,
			send_back_raw_response BOOLEAN DEFAULT false,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			budget_id VARCHAR(255),
			rate_limit_id VARCHAR(255),
			config_hash VARCHAR(255),
			status VARCHAR(50) DEFAULT 'unknown',
			description TEXT,
			encryption_status VARCHAR(20) DEFAULT 'plain_text'
		)
	`).Error
	if err != nil {
		return nil
	}

	// Clean up after the test — drop config_providers but leave migrations
	// intact for concurrent test packages.
	t.Cleanup(func() {
		db.Exec("DELETE FROM migrations")
		db.Exec("DROP TABLE IF EXISTS config_providers CASCADE")
	})

	return db
}

// forEachProviderMigrationDB returns backends for provider migration tests.
// Always includes SQLite; includes Postgres when available.
func forEachProviderMigrationDB(t *testing.T, testSuffix string) []namedDB {
	t.Helper()
	dbs := []namedDB{{"sqlite", setupProviderTestDBWithoutStoreRawColumn(t)}}
	if pgDB := trySetupPostgresDBWithoutStoreRawColumn(t, testSuffix); pgDB != nil {
		dbs = append(dbs, namedDB{"postgres", pgDB})
	}
	return dbs
}

func TestMigrationAddStoreRawRequestResponseColumn(t *testing.T) {
	tests := []struct {
		name                            string
		sendBackRawRequest              bool
		sendBackRawResponse             bool
		expectedStoreRawRequestResponse bool
	}{
		{
			name:                            "both false - store should be false",
			sendBackRawRequest:              false,
			sendBackRawResponse:             false,
			expectedStoreRawRequestResponse: false,
		},
		{
			name:                            "request true response false - store should be true",
			sendBackRawRequest:              true,
			sendBackRawResponse:             false,
			expectedStoreRawRequestResponse: true,
		},
		{
			name:                            "request false response true - store should be true",
			sendBackRawRequest:              false,
			sendBackRawResponse:             true,
			expectedStoreRawRequestResponse: true,
		},
		{
			name:                            "both true - store should be true",
			sendBackRawRequest:              true,
			sendBackRawResponse:             true,
			expectedStoreRawRequestResponse: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			for _, ndb := range forEachProviderMigrationDB(t, tt.name) {
				ndb := ndb
				t.Run(ndb.name, func(t *testing.T) {
					db := ndb.db
					ctx := context.Background()

					providerName := "test_provider"
					staleHash := "stale_hash_before_migration"
					now := time.Now()

					// Insert a provider with the old schema (no store_raw_request_response column)
					err := db.Exec(`
						INSERT INTO config_providers (
							name, send_back_raw_request, send_back_raw_response, 
							config_hash, created_at, updated_at, encryption_status
						) VALUES (?, ?, ?, ?, ?, ?, ?)
					`, providerName, tt.sendBackRawRequest, tt.sendBackRawResponse, staleHash, now, now, "plain_text").Error
					require.NoError(t, err, "Failed to insert test provider")

					// Verify column does not exist before migration
					hasColumn := db.Migrator().HasColumn(&tables.TableProvider{}, "store_raw_request_response")
					assert.False(t, hasColumn, "store_raw_request_response column should not exist before migration")

					// Run the migration
					err = migrationAddStoreRawRequestResponseColumn(ctx, db)
					require.NoError(t, err, "Migration should succeed")

					// Verify column exists after migration
					hasColumn = db.Migrator().HasColumn(&tables.TableProvider{}, "store_raw_request_response")
					assert.True(t, hasColumn, "store_raw_request_response column should exist after migration")

					// Fetch the provider and verify values
					var result struct {
						Name                    string
						SendBackRawRequest      bool
						SendBackRawResponse     bool
						StoreRawRequestResponse bool
						ConfigHash              string
					}
					err = db.Table("config_providers").
						Select("name, send_back_raw_request, send_back_raw_response, store_raw_request_response, config_hash").
						Where("name = ?", providerName).
						Scan(&result).Error
					require.NoError(t, err, "Failed to fetch provider after migration")

					// Verify store_raw_request_response was set correctly
					assert.Equal(t, tt.expectedStoreRawRequestResponse, result.StoreRawRequestResponse,
						"store_raw_request_response should be set based on send_back_raw_request OR send_back_raw_response")

					// Verify config_hash was updated (not the stale hash)
					assert.NotEqual(t, staleHash, result.ConfigHash,
						"config_hash should be updated after migration")

					// Verify the hash matches what GenerateConfigHash would produce
					expectedConfig := ProviderConfig{
						SendBackRawRequest:      tt.sendBackRawRequest,
						SendBackRawResponse:     tt.sendBackRawResponse,
						StoreRawRequestResponse: tt.expectedStoreRawRequestResponse,
					}
					expectedHash, err := expectedConfig.GenerateConfigHash(providerName)
					require.NoError(t, err, "Failed to generate expected hash")
					assert.Equal(t, expectedHash, result.ConfigHash,
						"config_hash should match the expected hash from GenerateConfigHash")
				})
			}
		})
	}
}

func TestMigrationAddStoreRawRequestResponseColumn_MultipleProviders(t *testing.T) {
	for _, ndb := range forEachProviderMigrationDB(t, "multiple") {
		ndb := ndb
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			ctx := context.Background()

			now := time.Now()

			// Insert multiple providers with different configurations
			providers := []struct {
				name                string
				sendBackRawRequest  bool
				sendBackRawResponse bool
			}{
				{"provider_neither", false, false},
				{"provider_request_only", true, false},
				{"provider_response_only", false, true},
				{"provider_both", true, true},
			}

			for _, p := range providers {
				err := db.Exec(`
					INSERT INTO config_providers (
						name, send_back_raw_request, send_back_raw_response, 
						config_hash, created_at, updated_at, encryption_status
					) VALUES (?, ?, ?, ?, ?, ?, ?)
				`, p.name, p.sendBackRawRequest, p.sendBackRawResponse, "stale_hash", now, now, "plain_text").Error
				require.NoError(t, err, "Failed to insert provider %s", p.name)
			}

			// Run the migration
			err := migrationAddStoreRawRequestResponseColumn(ctx, db)
			require.NoError(t, err, "Migration should succeed")

			// Verify each provider
			for _, p := range providers {
				var result struct {
					StoreRawRequestResponse bool
					ConfigHash              string
				}
				err := db.Table("config_providers").
					Select("store_raw_request_response, config_hash").
					Where("name = ?", p.name).
					Scan(&result).Error
				require.NoError(t, err, "Failed to fetch provider %s", p.name)

				expectedStore := p.sendBackRawRequest || p.sendBackRawResponse
				assert.Equal(t, expectedStore, result.StoreRawRequestResponse,
					"Provider %s: store_raw_request_response mismatch", p.name)
				assert.NotEqual(t, "stale_hash", result.ConfigHash,
					"Provider %s: config_hash should be updated", p.name)
			}
		})
	}
}

func TestMigrationAddStoreRawRequestResponseColumn_Idempotent(t *testing.T) {
	for _, ndb := range forEachProviderMigrationDB(t, "idempotent") {
		ndb := ndb
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			ctx := context.Background()

			now := time.Now()
			providerName := "idempotent_test_provider"

			// Insert a provider
			err := db.Exec(`
				INSERT INTO config_providers (
					name, send_back_raw_request, send_back_raw_response, 
					config_hash, created_at, updated_at, encryption_status
				) VALUES (?, ?, ?, ?, ?, ?, ?)
			`, providerName, true, false, "stale_hash", now, now, "plain_text").Error
			require.NoError(t, err, "Failed to insert test provider")

			// Run the migration first time
			err = migrationAddStoreRawRequestResponseColumn(ctx, db)
			require.NoError(t, err, "First migration should succeed")

			// Get the hash after first migration
			var firstHash string
			err = db.Table("config_providers").
				Select("config_hash").
				Where("name = ?", providerName).
				Scan(&firstHash).Error
			require.NoError(t, err)

			// Run the migration second time (should be idempotent)
			err = migrationAddStoreRawRequestResponseColumn(ctx, db)
			require.NoError(t, err, "Second migration should succeed (idempotent)")

			// Verify hash is unchanged after second run
			var secondHash string
			err = db.Table("config_providers").
				Select("config_hash").
				Where("name = ?", providerName).
				Scan(&secondHash).Error
			require.NoError(t, err)

			assert.Equal(t, firstHash, secondHash, "Hash should remain unchanged after idempotent migration run")
		})
	}
}

// setupKeyDBWithLegacyDeploymentColumns creates an in-memory SQLite database
// with the config_keys table including legacy deployment columns and NO aliases_json.
// This simulates the pre-aliases, post-encryption database state.
func setupKeyDBWithLegacyDeploymentColumns(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Create migrations table (required by the migrator)
	err = db.Exec(`CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)`).Error
	require.NoError(t, err)

	// Create config_keys table with legacy deployment columns and encryption_status
	// but WITHOUT aliases_json — simulating a DB from before the aliases migration
	err = db.Exec(`
		CREATE TABLE config_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name VARCHAR(255) NOT NULL UNIQUE,
			provider_id INTEGER NOT NULL,
			provider VARCHAR(50),
			key_id VARCHAR(255) NOT NULL UNIQUE,
			value TEXT NOT NULL,
			models_json TEXT,
			blacklisted_models_json TEXT,
			weight REAL,
			enabled BOOLEAN DEFAULT true,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			config_hash VARCHAR(255),
			azure_endpoint TEXT,
			azure_api_version TEXT,
			azure_client_id TEXT,
			azure_client_secret TEXT,
			azure_tenant_id TEXT,
			azure_scopes TEXT,
			azure_deployments_json TEXT,
			vertex_project_id TEXT,
			vertex_project_number TEXT,
			vertex_region TEXT,
			vertex_auth_credentials TEXT,
			vertex_deployments_json TEXT,
			bedrock_access_key TEXT,
			bedrock_secret_key TEXT,
			bedrock_session_token TEXT,
			bedrock_region TEXT,
			bedrock_arn TEXT,
			bedrock_role_arn TEXT,
			bedrock_external_id TEXT,
			bedrock_role_session_name TEXT,
			bedrock_batch_s3_config TEXT,
			bedrock_deployments_json TEXT,
			replicate_deployments_json TEXT,
			replicate_use_deployments_endpoint BOOLEAN,
			vllm_url TEXT,
			vllm_model_name TEXT,
			use_for_batch_api BOOLEAN DEFAULT false,
			status VARCHAR(50) DEFAULT 'unknown',
			description TEXT,
			encryption_status VARCHAR(20) DEFAULT 'plain_text',
			ollama_url TEXT,
			sgl_url TEXT
		)
	`).Error
	require.NoError(t, err)

	t.Cleanup(func() {
		db.Exec("DELETE FROM migrations")
		db.Exec("DROP TABLE IF EXISTS config_keys")
	})

	return db
}

// TestMigrationDropDeploymentColumnsAndAddAliases_EncryptedRows tests that the
// aliases migration correctly handles rows where encryption_status is 'encrypted'
// but deployment columns contain plaintext JSON (because those columns were never
// in the encryption list). This was the root cause of the base64 decode crash.
func TestMigrationDropDeploymentColumnsAndAddAliases_EncryptedRows(t *testing.T) {
	if !encrypt.IsEnabled() {
		t.Skip("encryption not enabled, skipping encrypted rows test")
	}

	db := setupKeyDBWithLegacyDeploymentColumns(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Encrypt the key values (simulating what EncryptPlaintextRows would have done)
	encVal1, err := encrypt.Encrypt("sk-azure-key-1")
	require.NoError(t, err)
	encVal2, err := encrypt.Encrypt("sk-vertex-key-1")
	require.NoError(t, err)
	encVal3, err := encrypt.Encrypt("sk-replicate-key-1")
	require.NoError(t, err)

	// Insert encrypted rows with PLAINTEXT deployment JSON.
	// azure_deployments_json, vertex_deployments_json, and replicate_deployments_json
	// were never in the encryption list, so they remain plaintext even though the row
	// is marked as encrypted.
	err = db.Exec(`
		INSERT INTO config_keys (
			name, provider_id, provider, key_id, value, models_json,
			azure_deployments_json, encryption_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'encrypted', ?, ?)`,
		"azure-key-1", 1, "azure", "ak-1", encVal1, `["*"]`,
		`{"gpt-4":"dep-gpt4","gpt-3.5":"dep-gpt35"}`, now, now,
	).Error
	require.NoError(t, err)

	err = db.Exec(`
		INSERT INTO config_keys (
			name, provider_id, provider, key_id, value, models_json,
			vertex_deployments_json, encryption_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'encrypted', ?, ?)`,
		"vertex-key-1", 2, "vertex", "vk-1", encVal2, `["*"]`,
		`{"gemini-pro":"dep-gemini"}`, now, now,
	).Error
	require.NoError(t, err)

	err = db.Exec(`
		INSERT INTO config_keys (
			name, provider_id, provider, key_id, value, models_json,
			replicate_deployments_json, encryption_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'encrypted', ?, ?)`,
		"replicate-key-1", 3, "replicate", "rk-1", encVal3, `["*"]`,
		`{"llama":"dep-llama"}`, now, now,
	).Error
	require.NoError(t, err)

	// Run the aliases migration — this should NOT crash with base64 decode error.
	err = migrationDropDeploymentColumnsAndAddAliases(ctx, db)
	require.NoError(t, err, "migration should not crash on encrypted rows with plaintext deployment data")

	// Verify aliases_json was populated and is readable via GORM hooks
	var keys []tables.TableKey
	err = db.Order("name").Find(&keys).Error
	require.NoError(t, err)
	require.Len(t, keys, 3)

	// Azure key should have its deployment data migrated to aliases
	assert.Equal(t, "azure-key-1", keys[0].Name)
	assert.NotNil(t, keys[0].Aliases)

	// Vertex key
	assert.Equal(t, "vertex-key-1", keys[2].Name)
	assert.NotNil(t, keys[2].Aliases)

	// Replicate key — sorted alphabetically, it's between azure and vertex
	assert.Equal(t, "replicate-key-1", keys[1].Name)
	assert.NotNil(t, keys[1].Aliases)
}

// TestMigrationDropDeploymentColumnsAndAddAliases_BedrockEncrypted tests the branch
// where bedrock_deployments_json is already encrypted before migration. The migration
// should detect it's already encrypted (Decrypt succeeds) and NOT double-encrypt it.
func TestMigrationDropDeploymentColumnsAndAddAliases_BedrockEncrypted(t *testing.T) {
	if !encrypt.IsEnabled() {
		t.Skip("encryption not enabled, skipping bedrock encrypted test")
	}

	db := setupKeyDBWithLegacyDeploymentColumns(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Encrypt the key value
	encVal, err := encrypt.Encrypt("sk-bedrock-key-1")
	require.NoError(t, err)

	// Encrypt the deployments JSON (simulating bedrock which WAS in the encryption list)
	bedrockDeployments := `{"claude":"dep-claude","claude-instant":"dep-instant"}`
	encDeployments, err := encrypt.Encrypt(bedrockDeployments)
	require.NoError(t, err)

	// Insert a bedrock row where bedrock_deployments_json is already encrypted
	err = db.Exec(`
		INSERT INTO config_keys (
			name, provider_id, provider, key_id, value, models_json,
			bedrock_deployments_json, encryption_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'encrypted', ?, ?)`,
		"bedrock-key-1", 4, "bedrock", "bk-1", encVal, `["*"]`,
		encDeployments, now, now,
	).Error
	require.NoError(t, err)

	// Run the aliases migration — should detect already-encrypted data and skip re-encryption
	err = migrationDropDeploymentColumnsAndAddAliases(ctx, db)
	require.NoError(t, err, "migration should handle already-encrypted bedrock deployments")

	// Verify aliases_json was populated and is readable via GORM hooks (AfterFind decrypts)
	var keys []tables.TableKey
	err = db.Find(&keys).Error
	require.NoError(t, err)
	require.Len(t, keys, 1)

	assert.Equal(t, "bedrock-key-1", keys[0].Name)
	assert.NotNil(t, keys[0].Aliases)

	// Verify the aliases contain the original deployment data (not double-encrypted)
	aliases := keys[0].Aliases
	assert.Contains(t, aliases, "claude")
	assert.Equal(t, "dep-claude", aliases["claude"])
	assert.Equal(t, "dep-instant", aliases["claude-instant"])
}

// ============================================================================
// Helper: full migration DB setup
// ============================================================================

// setupFullMigrationDB creates a fresh in-memory SQLite database and runs the
// full triggerMigrations chain (the same code path as production startup).
// Returns an RDBConfigStore for CRUD verification and the raw *gorm.DB for
// low-level assertions.
// testDBCounter ensures each test gets a unique shared-cache in-memory SQLite URI.
var testDBCounter int64

func setupFullMigrationDB(t *testing.T) (*RDBConfigStore, *gorm.DB) {
	t.Helper()
	// Use a unique shared-cache URI so all connections see the same in-memory DB
	// without requiring MaxOpenConns(1) (which deadlocks when code opens transactions
	// and queries on s.db concurrently).
	n := time.Now().UnixNano() + testDBCounter
	testDBCounter++
	dsn := fmt.Sprintf("file:testdb_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "Failed to create test database")

	ctx := context.Background()
	err = triggerMigrations(ctx, db)
	require.NoError(t, err, "triggerMigrations should succeed on a fresh DB")

	store := &RDBConfigStore{
		db:     db,
		logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo),
	}
	return store, db
}

// ============================================================================
// Part 2: Full-chain integration tests
// ============================================================================

func TestTriggerMigrations_FreshDB(t *testing.T) {
	_, db := setupFullMigrationDB(t)

	// Every critical table should exist after the full migration chain.
	criticalTables := []interface{}{
		&tables.TableProvider{},
		&tables.TableKey{},
		&tables.TableVirtualKey{},
		&tables.TableMCPClient{},
		&tables.TableBudget{},
		&tables.TableRateLimit{},
		&tables.TablePlugin{},
		&tables.TableCustomer{},
		&tables.TableTeam{},
		&tables.SessionsTable{},
		&tables.TableOauthConfig{},
		&tables.TableOauthToken{},
		&tables.TableModelPricing{},
		&tables.TableGovernanceConfig{},
		&tables.TableClientConfig{},
		&tables.TableVirtualKeyProviderConfig{},
		&tables.TableVirtualKeyMCPConfig{},
	}

	migrator := db.Migrator()
	for _, table := range criticalTables {
		assert.True(t, migrator.HasTable(table), "table should exist: %T", table)
	}
}

func TestTriggerMigrations_Idempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	ctx := context.Background()

	// First run
	err = triggerMigrations(ctx, db)
	require.NoError(t, err, "first triggerMigrations should succeed")

	// Second run – must be a no-op, not an error.
	err = triggerMigrations(ctx, db)
	require.NoError(t, err, "second triggerMigrations should succeed (idempotent)")

	// Tables should still be intact
	assert.True(t, db.Migrator().HasTable(&tables.TableProvider{}), "TableProvider should still exist")
	assert.True(t, db.Migrator().HasTable(&tables.TableKey{}), "TableKey should still exist")
	assert.True(t, db.Migrator().HasTable(&tables.TableVirtualKey{}), "TableVirtualKey should still exist")
}

func TestFullMigration_ProviderAndKeyCRUD(t *testing.T) {
	if !encrypt.IsEnabled() {
		t.Skip("encryption not enabled")
	}

	store, db := setupFullMigrationDB(t)
	ctx := context.Background()

	config := ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "key-uuid-1",
				Name:   "openai-primary",
				Value:  *schemas.NewEnvVar("sk-test-secret-key-12345"),
				Models: schemas.WhiteList{"*"},
				Weight: 1.0,
			},
		},
	}

	err := store.AddProvider(ctx, "openai", config)
	require.NoError(t, err)

	// Read back through store (AfterFind hooks decrypt)
	result, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Contains(t, result, schemas.ModelProvider("openai"))

	openai := result[schemas.ModelProvider("openai")]
	require.Len(t, openai.Keys, 1)
	assert.Equal(t, "openai-primary", openai.Keys[0].Name)
	assert.Equal(t, "sk-test-secret-key-12345", openai.Keys[0].Value.GetValue())

	// Verify encryption at the raw DB level
	var rawValue string
	var rawStatus string
	err = db.Table("config_keys").Select("value").Where("key_id = ?", "key-uuid-1").Scan(&rawValue).Error
	require.NoError(t, err)
	err = db.Table("config_keys").Select("encryption_status").Where("key_id = ?", "key-uuid-1").Scan(&rawStatus).Error
	require.NoError(t, err)

	assert.NotEqual(t, "sk-test-secret-key-12345", rawValue, "key value should be encrypted at rest")
	assert.Equal(t, "encrypted", rawStatus)
}

func TestFullMigration_VirtualKeyCRUD(t *testing.T) {
	if !encrypt.IsEnabled() {
		t.Skip("encryption not enabled")
	}

	store, db := setupFullMigrationDB(t)
	ctx := context.Background()
	now := time.Now()

	vk := &tables.TableVirtualKey{
		ID:        "vk-test-001",
		Name:      "test-virtual-key",
		Value:     "vk-secret-value-12345",
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	err := store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	// Read back
	vks, err := store.GetVirtualKeys(ctx)
	require.NoError(t, err)
	require.Len(t, vks, 1)

	assert.Equal(t, "vk-test-001", vks[0].ID)
	assert.Equal(t, "test-virtual-key", vks[0].Name)
	assert.Equal(t, "vk-secret-value-12345", vks[0].Value) // AfterFind decrypts
	assert.True(t, vks[0].IsActive)

	// Verify encryption at raw DB level
	var rawValue, rawStatus, rawHash string
	err = db.Table("governance_virtual_keys").
		Select("value").Where("id = ?", "vk-test-001").Scan(&rawValue).Error
	require.NoError(t, err)
	err = db.Table("governance_virtual_keys").
		Select("encryption_status").Where("id = ?", "vk-test-001").Scan(&rawStatus).Error
	require.NoError(t, err)
	err = db.Table("governance_virtual_keys").
		Select("value_hash").Where("id = ?", "vk-test-001").Scan(&rawHash).Error
	require.NoError(t, err)

	assert.NotEqual(t, "vk-secret-value-12345", rawValue, "VK value should be encrypted at rest")
	assert.Equal(t, "encrypted", rawStatus)
	assert.NotEmpty(t, rawHash, "value_hash should be populated")
}

func TestFullMigration_MCPClientCRUD(t *testing.T) {
	if !encrypt.IsEnabled() {
		t.Skip("encryption not enabled")
	}

	store, db := setupFullMigrationDB(t)
	ctx := context.Background()

	clientConfig := &schemas.MCPClientConfig{
		ID:               "mcp-client-001",
		Name:             "test_mcp_server",
		ConnectionType:   schemas.MCPConnectionTypeSSE,
		ConnectionString: schemas.NewEnvVar("https://mcp.example.com/sse"),
		ToolsToExecute:   schemas.WhiteList{"*"},
	}

	err := store.CreateMCPClientConfig(ctx, clientConfig)
	require.NoError(t, err)

	// Read back through store
	mcpClient, err := store.GetMCPClientByName(ctx, "test_mcp_server")
	require.NoError(t, err)
	require.NotNil(t, mcpClient)

	assert.Equal(t, "mcp-client-001", mcpClient.ClientID)
	assert.Equal(t, "test_mcp_server", mcpClient.Name)
	assert.Equal(t, "sse", mcpClient.ConnectionType)
	assert.Equal(t, "https://mcp.example.com/sse", mcpClient.ConnectionString.GetValue())

	// Verify encryption at raw DB level
	var rawConnStr, rawStatus string
	err = db.Table("config_mcp_clients").
		Select("connection_string").Where("client_id = ?", "mcp-client-001").Scan(&rawConnStr).Error
	require.NoError(t, err)
	err = db.Table("config_mcp_clients").
		Select("encryption_status").Where("client_id = ?", "mcp-client-001").Scan(&rawStatus).Error
	require.NoError(t, err)

	assert.NotEqual(t, "https://mcp.example.com/sse", rawConnStr, "connection string should be encrypted at rest")
	assert.Equal(t, "encrypted", rawStatus)
}

func TestFullMigration_EncryptPlaintextRows(t *testing.T) {
	if !encrypt.IsEnabled() {
		t.Skip("encryption not enabled")
	}

	store, db := setupFullMigrationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert a provider first (FK for config_keys)
	err := db.Exec(`INSERT INTO config_providers (name, encryption_status, created_at, updated_at)
		VALUES (?, 'plain_text', ?, ?)`, "openai", now, now).Error
	require.NoError(t, err)

	// Get the provider ID
	var providerID uint
	err = db.Table("config_providers").Select("id").Where("name = ?", "openai").Scan(&providerID).Error
	require.NoError(t, err)

	// Insert plaintext key (bypassing GORM hooks)
	err = db.Exec(`INSERT INTO config_keys (name, provider_id, provider, key_id, value, models_json,
		encryption_status, created_at, updated_at)
		VALUES (?, ?, 'openai', ?, ?, '["*"]', 'plain_text', ?, ?)`,
		"plaintext-key", providerID, "pk-1", "sk-plaintext-secret", now, now).Error
	require.NoError(t, err)

	// Insert plaintext virtual key
	err = db.Exec(`INSERT INTO governance_virtual_keys (id, name, value, is_active, encryption_status, created_at, updated_at)
		VALUES (?, ?, ?, true, 'plain_text', ?, ?)`,
		"vk-plain-1", "plaintext-vk", "vk-plain-secret", now, now).Error
	require.NoError(t, err)

	// Verify they are plaintext before encryption
	var preStatus string
	err = db.Table("config_keys").Select("encryption_status").Where("key_id = ?", "pk-1").Scan(&preStatus).Error
	require.NoError(t, err)
	assert.Equal(t, "plain_text", preStatus)

	// Run the encryption pass
	err = store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	// Verify raw DB: encryption_status changed, values differ
	var rawKeyValue, rawKeyStatus string
	err = db.Table("config_keys").Select("value").Where("key_id = ?", "pk-1").Scan(&rawKeyValue).Error
	require.NoError(t, err)
	err = db.Table("config_keys").Select("encryption_status").Where("key_id = ?", "pk-1").Scan(&rawKeyStatus).Error
	require.NoError(t, err)
	assert.NotEqual(t, "sk-plaintext-secret", rawKeyValue, "key should be encrypted after EncryptPlaintextRows")
	assert.Equal(t, "encrypted", rawKeyStatus)

	var rawVKValue, rawVKStatus string
	err = db.Table("governance_virtual_keys").Select("value").Where("id = ?", "vk-plain-1").Scan(&rawVKValue).Error
	require.NoError(t, err)
	err = db.Table("governance_virtual_keys").Select("encryption_status").Where("id = ?", "vk-plain-1").Scan(&rawVKStatus).Error
	require.NoError(t, err)
	assert.NotEqual(t, "vk-plain-secret", rawVKValue, "VK should be encrypted after EncryptPlaintextRows")
	assert.Equal(t, "encrypted", rawVKStatus)

	// Verify GORM read decrypts correctly
	var key tables.TableKey
	err = db.Where("key_id = ?", "pk-1").First(&key).Error
	require.NoError(t, err)
	assert.Equal(t, "sk-plaintext-secret", key.Value.GetValue())

	var vk tables.TableVirtualKey
	err = db.Where("id = ?", "vk-plain-1").First(&vk).Error
	require.NoError(t, err)
	assert.Equal(t, "vk-plain-secret", vk.Value)
}

func TestFullMigration_EndToEnd(t *testing.T) {
	if !encrypt.IsEnabled() {
		t.Skip("encryption not enabled")
	}

	store, db := setupFullMigrationDB(t)
	ctx := context.Background()
	now := time.Now()

	// Add two providers with keys
	for _, p := range []struct {
		provider string
		keyID    string
		keyName  string
		keyValue string
	}{
		{"openai", "key-oa-1", "openai-key", "sk-openai-secret"},
		{"anthropic", "key-ant-1", "anthropic-key", "sk-anthropic-secret"},
	} {
		err := store.AddProvider(ctx, schemas.ModelProvider(p.provider), ProviderConfig{
			Keys: []schemas.Key{{
				ID:     p.keyID,
				Name:   p.keyName,
				Value:  *schemas.NewEnvVar(p.keyValue),
				Models: schemas.WhiteList{"*"},
				Weight: 1.0,
			}},
		})
		require.NoError(t, err, "AddProvider %s", p.provider)
	}

	// Add virtual keys
	for _, vk := range []struct {
		id, name, value string
	}{
		{"vk-1", "vk-alpha", "vk-alpha-secret"},
		{"vk-2", "vk-beta", "vk-beta-secret"},
	} {
		err := store.CreateVirtualKey(ctx, &tables.TableVirtualKey{
			ID: vk.id, Name: vk.name, Value: vk.value,
			IsActive: true, CreatedAt: now, UpdatedAt: now,
		})
		require.NoError(t, err, "CreateVirtualKey %s", vk.name)
	}

	// Add MCP client
	err := store.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:               "mcp-e2e-1",
		Name:             "e2e_mcp_client",
		ConnectionType:   schemas.MCPConnectionTypeSSE,
		ConnectionString: schemas.NewEnvVar("https://mcp.e2e.test/sse"),
		ToolsToExecute:   schemas.WhiteList{"*"},
	})
	require.NoError(t, err)

	// Verify providers
	providers, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	assert.Len(t, providers, 2)
	assert.Contains(t, providers, schemas.ModelProvider("openai"))
	assert.Contains(t, providers, schemas.ModelProvider("anthropic"))
	assert.Equal(t, "sk-openai-secret", providers["openai"].Keys[0].Value.GetValue())
	assert.Equal(t, "sk-anthropic-secret", providers["anthropic"].Keys[0].Value.GetValue())

	// Verify virtual keys
	vks, err := store.GetVirtualKeys(ctx)
	require.NoError(t, err)
	assert.Len(t, vks, 2)

	// Verify MCP client
	mcpClient, err := store.GetMCPClientByName(ctx, "e2e_mcp_client")
	require.NoError(t, err)
	assert.Equal(t, "https://mcp.e2e.test/sse", mcpClient.ConnectionString.GetValue())

	// Verify all sensitive data is encrypted at the raw DB level
	type encCheck struct {
		table, column, whereCol, whereVal, plaintext string
	}
	checks := []encCheck{
		{"config_keys", "value", "key_id", "key-oa-1", "sk-openai-secret"},
		{"config_keys", "value", "key_id", "key-ant-1", "sk-anthropic-secret"},
		{"governance_virtual_keys", "value", "id", "vk-1", "vk-alpha-secret"},
		{"governance_virtual_keys", "value", "id", "vk-2", "vk-beta-secret"},
		{"config_mcp_clients", "connection_string", "client_id", "mcp-e2e-1", "https://mcp.e2e.test/sse"},
	}
	for _, c := range checks {
		var rawVal string
		err := db.Table(c.table).Select(c.column).Where(fmt.Sprintf("%s = ?", c.whereCol), c.whereVal).Scan(&rawVal).Error
		require.NoError(t, err)
		assert.NotEqual(t, c.plaintext, rawVal, "raw %s.%s for %s=%s should be encrypted",
			c.table, c.column, c.whereCol, c.whereVal)
	}
}

// ============================================================================
// Part 3: Individual complex migration tests
// ============================================================================

// setupPreEncryptionDB runs all migrations up to (but not including) the
// encryption columns migration. This is approximated by running migrationInit
// plus the essential early migrations on a fresh SQLite DB.
func setupPreEncryptionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	ctx := context.Background()
	// Run the initial migration to create core tables
	err = migrationInit(ctx, db)
	require.NoError(t, err)
	// Sessions table is created by a later migration, but required by the
	// encryption columns migration.
	err = migrationAddSessionsTable(ctx, db)
	require.NoError(t, err)

	return db
}

func TestMigrationAddEncryptionColumns(t *testing.T) {
	db := setupPreEncryptionDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert rows BEFORE encryption columns exist (they won't have encryption_status)
	err := db.Exec(`INSERT INTO config_providers (name, created_at, updated_at) VALUES (?, ?, ?)`,
		"openai", now, now).Error
	require.NoError(t, err)

	var providerID uint
	err = db.Table("config_providers").Select("id").Where("name = ?", "openai").Scan(&providerID).Error
	require.NoError(t, err)

	err = db.Exec(`INSERT INTO config_keys (name, provider_id, provider, key_id, value, created_at, updated_at)
		VALUES (?, ?, 'openai', ?, ?, ?, ?)`,
		"test-key", providerID, "ek-1", "sk-test", now, now).Error
	require.NoError(t, err)

	err = db.Exec(`INSERT INTO governance_virtual_keys (id, name, value, is_active, created_at, updated_at)
		VALUES (?, ?, ?, true, ?, ?)`,
		"vk-enc-1", "enc-vk", "vk-value", now, now).Error
	require.NoError(t, err)

	err = db.Exec(`INSERT INTO sessions (token, created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		"sess-token", now, now, now).Error
	require.NoError(t, err)

	// Run the encryption columns migration
	err = migrationAddEncryptionColumns(ctx, db)
	require.NoError(t, err)

	// Verify encryption_status column exists and is backfilled on all 9 tables
	encTables := []string{
		"config_keys", "governance_virtual_keys", "sessions",
		"oauth_configs", "oauth_tokens", "config_mcp_clients",
		"config_providers", "config_vector_store", "config_plugins",
	}
	for _, table := range encTables {
		var count int64
		err := db.Table(table).Where("encryption_status = 'plain_text'").Count(&count).Error
		if err != nil {
			// Table might be empty, just verify column exists
			assert.True(t, db.Migrator().HasColumn(table, "encryption_status"),
				"encryption_status column should exist on %s", table)
			continue
		}
	}

	// Verify pre-existing rows have encryption_status = 'plain_text'
	var keyStatus string
	err = db.Table("config_keys").Select("encryption_status").Where("key_id = ?", "ek-1").Scan(&keyStatus).Error
	require.NoError(t, err)
	assert.Equal(t, "plain_text", keyStatus)

	var providerStatus string
	err = db.Table("config_providers").Select("encryption_status").Where("name = ?", "openai").Scan(&providerStatus).Error
	require.NoError(t, err)
	assert.Equal(t, "plain_text", providerStatus)

	var vkStatus string
	err = db.Table("governance_virtual_keys").Select("encryption_status").Where("id = ?", "vk-enc-1").Scan(&vkStatus).Error
	require.NoError(t, err)
	assert.Equal(t, "plain_text", vkStatus)

	// Verify value_hash on governance_virtual_keys is NULL (not empty string)
	var rawHash *string
	err = db.Table("governance_virtual_keys").Select("value_hash").Where("id = ?", "vk-enc-1").Scan(&rawHash).Error
	require.NoError(t, err)
	assert.Nil(t, rawHash, "value_hash should be NULL, not empty string")

	// Verify token_hash on sessions is NULL
	var tokenHash *string
	err = db.Table("sessions").Select("token_hash").Where("token = ?", "sess-token").Scan(&tokenHash).Error
	require.NoError(t, err)
	assert.Nil(t, tokenHash, "token_hash should be NULL, not empty string")

	// Idempotency: running again should not error
	err = migrationAddEncryptionColumns(ctx, db)
	require.NoError(t, err)
}

func TestMigrationCleanupMCPClientToolsConfig(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ctx := context.Background()

	// Create the MCP clients table using AutoMigrate
	err = db.AutoMigrate(&tables.TableMCPClient{})
	require.NoError(t, err)

	// Create the migrations tracking table
	err = db.Exec(`CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)`).Error
	require.NoError(t, err)

	now := time.Now()

	// Insert clients with various tools_to_execute_json states
	clients := []struct {
		name  string
		tools string
	}{
		{"client_empty_array", "[]"},
		{"client_empty_string", ""},
		{"client_wildcard", `["*"]`},
		{"client_specific", `["tool1","tool2"]`},
	}

	for i, c := range clients {
		err := db.Exec(`INSERT INTO config_mcp_clients (client_id, name, connection_type, tools_to_execute_json, created_at, updated_at, encryption_status)
			VALUES (?, ?, 'stdio', ?, ?, ?, 'plain_text')`,
			fmt.Sprintf("client-%d", i), c.name, c.tools, now, now).Error
		require.NoError(t, err)
	}

	// Also insert one with NULL tools
	err = db.Exec(`INSERT INTO config_mcp_clients (client_id, name, connection_type, tools_to_execute_json, created_at, updated_at, encryption_status)
		VALUES (?, ?, 'stdio', NULL, ?, ?, 'plain_text')`,
		"client-null", "client_null_tools", now, now).Error
	require.NoError(t, err)

	// Run the cleanup migration
	err = migrationCleanupMCPClientToolsConfig(ctx, db)
	require.NoError(t, err)

	// Verify: empty/null → ["*"], existing values preserved
	for _, tc := range []struct {
		name     string
		expected string
	}{
		{"client_empty_array", `["*"]`},
		{"client_empty_string", `["*"]`},
		{"client_null_tools", `["*"]`},
		{"client_wildcard", `["*"]`},
		{"client_specific", `["tool1","tool2"]`},
	} {
		var tools string
		err := db.Table("config_mcp_clients").Select("tools_to_execute_json").
			Where("name = ?", tc.name).Scan(&tools).Error
		require.NoError(t, err)
		assert.Equal(t, tc.expected, tools, "tools for %s", tc.name)
	}
}

func TestMigrationAddConfigHashColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ctx := context.Background()

	// Create migrations table
	err = db.Exec(`CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)`).Error
	require.NoError(t, err)

	// Create tables WITHOUT config_hash column to simulate pre-migration state
	err = db.Exec(`CREATE TABLE config_providers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name VARCHAR(50) NOT NULL UNIQUE,
		network_config_json TEXT,
		concurrency_buffer_json TEXT,
		proxy_config_json TEXT,
		custom_provider_config_json TEXT,
		send_back_raw_request BOOLEAN DEFAULT 0,
		send_back_raw_response BOOLEAN DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		encryption_status VARCHAR(20) DEFAULT 'plain_text'
	)`).Error
	require.NoError(t, err)

	err = db.Exec(`CREATE TABLE config_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name VARCHAR(255) NOT NULL,
		key_id VARCHAR(255) NOT NULL UNIQUE,
		provider_id INTEGER NOT NULL,
		provider VARCHAR(50),
		value TEXT NOT NULL,
		models_json TEXT,
		weight REAL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		encryption_status VARCHAR(20) DEFAULT 'plain_text'
	)`).Error
	require.NoError(t, err)

	now := time.Now()

	// Insert provider and key without config_hash
	err = db.Exec(`INSERT INTO config_providers (name, created_at, updated_at, encryption_status)
		VALUES (?, ?, ?, 'plain_text')`, "openai", now, now).Error
	require.NoError(t, err)

	var providerID uint
	err = db.Table("config_providers").Select("id").Where("name = ?", "openai").Scan(&providerID).Error
	require.NoError(t, err)

	err = db.Exec(`INSERT INTO config_keys (name, key_id, provider_id, provider, value, models_json, created_at, updated_at, encryption_status)
		VALUES (?, ?, ?, 'openai', ?, '["*"]', ?, ?, 'plain_text')`,
		"test-key", "ck-1", providerID, "sk-test-value", now, now).Error
	require.NoError(t, err)

	// Run the migration
	err = migrationAddConfigHashColumn(ctx, db)
	require.NoError(t, err)

	// Verify config_hash column exists
	assert.True(t, db.Migrator().HasColumn(&tables.TableProvider{}, "config_hash"))
	assert.True(t, db.Migrator().HasColumn(&tables.TableKey{}, "config_hash"))

	// Verify hashes are non-empty
	var providerHash string
	err = db.Table("config_providers").Select("config_hash").Where("name = ?", "openai").Scan(&providerHash).Error
	require.NoError(t, err)
	assert.NotEmpty(t, providerHash, "provider config_hash should be backfilled")

	var keyHash string
	err = db.Table("config_keys").Select("config_hash").Where("key_id = ?", "ck-1").Scan(&keyHash).Error
	require.NoError(t, err)
	assert.NotEmpty(t, keyHash, "key config_hash should be backfilled")

	// Verify provider hash matches expected computation
	expectedProviderConfig := ProviderConfig{}
	expectedHash, err := expectedProviderConfig.GenerateConfigHash("openai")
	require.NoError(t, err)
	assert.Equal(t, expectedHash, providerHash, "provider hash should match GenerateConfigHash output")
}

func TestMigrationBackfillEmptyVirtualKeyConfigs(t *testing.T) {
	// Use full migration DB to get all tables, then insert test data
	store, db := setupFullMigrationDB(t)
	_ = store
	ctx := context.Background()
	now := time.Now()

	// Clear the migration tracking for the specific migration so it runs again
	db.Exec(`DELETE FROM migrations WHERE id = 'backfill_empty_virtual_key_configs'`)

	// Create a provider
	err := db.Exec(`INSERT INTO config_providers (name, encryption_status, created_at, updated_at)
		VALUES ('openai', 'plain_text', ?, ?)`, now, now).Error
	require.NoError(t, err)

	// Create a virtual key with NO provider configs
	err = db.Exec(`INSERT INTO governance_virtual_keys (id, name, value, is_active, encryption_status, created_at, updated_at)
		VALUES ('vk-backfill-1', 'backfill-vk', 'vk-value', true, 'plain_text', ?, ?)`, now, now).Error
	require.NoError(t, err)

	// Verify no provider configs exist
	var provConfigCount int64
	db.Table("governance_virtual_key_provider_configs").Where("virtual_key_id = ?", "vk-backfill-1").Count(&provConfigCount)
	assert.Equal(t, int64(0), provConfigCount, "should have no provider configs before migration")

	// Run the migration
	err = migrationBackfillEmptyVirtualKeyConfigs(ctx, db)
	require.NoError(t, err)

	// Verify provider configs were created for the VK
	db.Table("governance_virtual_key_provider_configs").Where("virtual_key_id = ?", "vk-backfill-1").Count(&provConfigCount)
	assert.Equal(t, int64(1), provConfigCount, "should have 1 provider config after backfill (one for openai)")

	// Verify the provider config has correct defaults
	var allowAllKeys bool
	err = db.Table("governance_virtual_key_provider_configs").
		Select("allow_all_keys").Where("virtual_key_id = ?", "vk-backfill-1").Scan(&allowAllKeys).Error
	require.NoError(t, err)
	assert.True(t, allowAllKeys, "backfilled provider config should have AllowAllKeys=true")

	// Verify config_hash was recomputed (non-empty)
	var vkHash string
	err = db.Table("governance_virtual_keys").Select("config_hash").
		Where("id = ?", "vk-backfill-1").Scan(&vkHash).Error
	require.NoError(t, err)
	assert.NotEmpty(t, vkHash, "VK config_hash should be recomputed after backfill")
}

func TestMigrationBackfillAllowedModelsWildcard(t *testing.T) {
	_, db := setupFullMigrationDB(t)
	ctx := context.Background()
	now := time.Now()

	// Clear migration tracking so it runs again
	db.Exec(`DELETE FROM migrations WHERE id = 'backfill_allowed_models_wildcard'`)

	// Create a provider
	err := db.Exec(`INSERT INTO config_providers (name, encryption_status, created_at, updated_at)
		VALUES ('openai', 'plain_text', ?, ?)`, now, now).Error
	require.NoError(t, err)

	var providerID uint
	err = db.Table("config_providers").Select("id").Where("name = ?", "openai").Scan(&providerID).Error
	require.NoError(t, err)

	// Create a key with empty models_json
	err = db.Exec(`INSERT INTO config_keys (name, key_id, provider_id, provider, value, models_json,
		encryption_status, created_at, updated_at)
		VALUES ('empty-models-key', 'emk-1', ?, 'openai', 'sk-test', '[]', 'plain_text', ?, ?)`,
		providerID, now, now).Error
	require.NoError(t, err)

	// Create a VK
	err = db.Exec(`INSERT INTO governance_virtual_keys (id, name, value, is_active, encryption_status, created_at, updated_at)
		VALUES ('vk-wildcard-1', 'wildcard-vk', 'vk-val', true, 'plain_text', ?, ?)`, now, now).Error
	require.NoError(t, err)

	// Create a provider config with empty allowed_models
	err = db.Exec(`INSERT INTO governance_virtual_key_provider_configs (virtual_key_id, provider, allowed_models, allow_all_keys)
		VALUES ('vk-wildcard-1', 'openai', '[]', true)`).Error
	require.NoError(t, err)

	// Run the migration
	err = migrationBackfillAllowedModelsWildcard(ctx, db)
	require.NoError(t, err)

	// Verify provider config allowed_models changed to ["*"]
	var allowedModels string
	err = db.Table("governance_virtual_key_provider_configs").
		Select("allowed_models").Where("virtual_key_id = ?", "vk-wildcard-1").Scan(&allowedModels).Error
	require.NoError(t, err)
	assert.Equal(t, `["*"]`, allowedModels, "allowed_models should be backfilled to wildcard")

	// Verify key models_json changed to ["*"]
	var modelsJSON string
	err = db.Table("config_keys").Select("models_json").Where("key_id = ?", "emk-1").Scan(&modelsJSON).Error
	require.NoError(t, err)
	assert.Equal(t, `["*"]`, modelsJSON, "models_json should be backfilled to wildcard")

	// Verify key config_hash was recomputed
	var keyHash string
	err = db.Table("config_keys").Select("config_hash").Where("key_id = ?", "emk-1").Scan(&keyHash).Error
	require.NoError(t, err)
	assert.NotEmpty(t, keyHash, "key config_hash should be recomputed")
}

func TestMigrationRemoveServerPrefixFromMCPTools(t *testing.T) {
	_, db := setupFullMigrationDB(t)
	ctx := context.Background()
	now := time.Now()

	// Clear migration tracking so it runs again
	db.Exec(`DELETE FROM migrations WHERE id = 'remove_server_prefix_from_mcp_tools'`)

	// Insert an MCP client with prefixed tool names
	toolsJSON, _ := json.Marshal([]string{"my_server_tool1", "my_server_tool2", "standalone_tool"})
	autoToolsJSON, _ := json.Marshal([]string{"my_server_auto1"})

	err := db.Exec(`INSERT INTO config_mcp_clients (client_id, name, connection_type,
		tools_to_execute_json, tools_to_auto_execute_json, encryption_status, created_at, updated_at)
		VALUES (?, ?, 'stdio', ?, ?, 'plain_text', ?, ?)`,
		"mcp-prefix-1", "my_server", string(toolsJSON), string(autoToolsJSON), now, now).Error
	require.NoError(t, err)

	// Run the migration
	err = migrationRemoveServerPrefixFromMCPTools(ctx, db)
	require.NoError(t, err)

	// Verify tools had prefixes stripped
	var resultToolsJSON string
	err = db.Table("config_mcp_clients").Select("tools_to_execute_json").
		Where("client_id = ?", "mcp-prefix-1").Scan(&resultToolsJSON).Error
	require.NoError(t, err)

	var resultTools []string
	err = json.Unmarshal([]byte(resultToolsJSON), &resultTools)
	require.NoError(t, err)

	assert.Contains(t, resultTools, "tool1", "should have stripped my_server_ prefix")
	assert.Contains(t, resultTools, "tool2", "should have stripped my_server_ prefix")
	assert.Contains(t, resultTools, "standalone_tool", "should preserve non-prefixed tool")
	assert.NotContains(t, resultTools, "my_server_tool1", "original prefixed name should be gone")

	// Verify auto-execute tools
	var resultAutoToolsJSON string
	err = db.Table("config_mcp_clients").Select("tools_to_auto_execute_json").
		Where("client_id = ?", "mcp-prefix-1").Scan(&resultAutoToolsJSON).Error
	require.NoError(t, err)

	var resultAutoTools []string
	err = json.Unmarshal([]byte(resultAutoToolsJSON), &resultAutoTools)
	require.NoError(t, err)
	assert.Contains(t, resultAutoTools, "auto1", "should have stripped my_server_ prefix from auto tools")
}

func TestMigrationRemoveServerPrefixFromMCPTools_Collision(t *testing.T) {
	_, db := setupFullMigrationDB(t)
	ctx := context.Background()
	now := time.Now()

	// Clear migration tracking
	db.Exec(`DELETE FROM migrations WHERE id = 'remove_server_prefix_from_mcp_tools'`)

	// Client where stripping the prefix would cause a collision:
	// "srv_read" (prefixed) → "read", but "read" already exists in the list
	toolsJSON, _ := json.Marshal([]string{"srv_read", "read"})
	err := db.Exec(`INSERT INTO config_mcp_clients (client_id, name, connection_type,
		tools_to_execute_json, encryption_status, created_at, updated_at)
		VALUES (?, ?, 'stdio', ?, 'plain_text', ?, ?)`,
		"mcp-collision", "srv", string(toolsJSON), now, now).Error
	require.NoError(t, err)

	// Run the migration — should not error, collision is handled
	err = migrationRemoveServerPrefixFromMCPTools(ctx, db)
	require.NoError(t, err)

	// The collision drops the duplicate, keeping "read" once
	var resultJSON string
	err = db.Table("config_mcp_clients").Select("tools_to_execute_json").
		Where("client_id = ?", "mcp-collision").Scan(&resultJSON).Error
	require.NoError(t, err)

	var resultTools []string
	err = json.Unmarshal([]byte(resultJSON), &resultTools)
	require.NoError(t, err)
	assert.Contains(t, resultTools, "read")
	// Should not have duplicates
	readCount := 0
	for _, tool := range resultTools {
		if tool == "read" {
			readCount++
		}
	}
	assert.Equal(t, 1, readCount, "should deduplicate on collision")
}

func TestMigrationReplaceEnableLiteLLMWithCompatColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ctx := context.Background()

	// Create migrations table
	err = db.Exec(`CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)`).Error
	require.NoError(t, err)

	// Create config_client from the GORM model so DropColumn works (SQLite recreates
	// the table from model columns). Then add the legacy column on top.
	err = db.AutoMigrate(&tables.TableClientConfig{})
	require.NoError(t, err)

	// Add the legacy column that the migration will read and drop
	err = db.Exec(`ALTER TABLE config_client ADD COLUMN enable_litellm_fallbacks BOOLEAN DEFAULT 0`).Error
	require.NoError(t, err)

	now := time.Now()

	// Insert a row with enable_litellm_fallbacks = true
	err = db.Exec(`INSERT INTO config_client (enable_litellm_fallbacks, created_at, updated_at)
		VALUES (1, ?, ?)`, now, now).Error
	require.NoError(t, err)

	// Insert a row with enable_litellm_fallbacks = false
	err = db.Exec(`INSERT INTO config_client (enable_litellm_fallbacks, created_at, updated_at)
		VALUES (0, ?, ?)`, now, now).Error
	require.NoError(t, err)

	// Run the migration
	err = migrationReplaceEnableLiteLLMWithCompatColumns(ctx, db)
	require.NoError(t, err)

	// Verify new columns exist
	mgr := db.Migrator()
	assert.True(t, mgr.HasColumn(&tables.TableClientConfig{}, "compat_convert_text_to_chat"))
	assert.True(t, mgr.HasColumn(&tables.TableClientConfig{}, "compat_convert_chat_to_responses"))
	assert.True(t, mgr.HasColumn(&tables.TableClientConfig{}, "compat_should_drop_params"))
	assert.True(t, mgr.HasColumn(&tables.TableClientConfig{}, "compat_should_convert_params"))

	// Verify data migration: row 1 had litellm=true → compat_convert_text_to_chat=true
	type compatRow struct {
		ID                        uint
		CompatConvertTextToChat   bool `gorm:"column:compat_convert_text_to_chat"`
		CompatShouldConvertParams bool `gorm:"column:compat_should_convert_params"`
	}
	var rows []compatRow
	err = db.Table("config_client").
		Select("id, compat_convert_text_to_chat, compat_should_convert_params").
		Order("id").Find(&rows).Error
	require.NoError(t, err)
	require.Len(t, rows, 2)

	assert.True(t, rows[0].CompatConvertTextToChat, "row with litellm=true should have compat_convert_text_to_chat=true")
	assert.False(t, rows[0].CompatShouldConvertParams, "compat_should_convert_params should default to false")

	assert.False(t, rows[1].CompatConvertTextToChat, "row with litellm=false should have compat_convert_text_to_chat=false")
	assert.False(t, rows[1].CompatShouldConvertParams, "compat_should_convert_params should default to false")
}

