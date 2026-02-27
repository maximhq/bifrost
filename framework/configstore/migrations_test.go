package configstore

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/pricingoverrides"
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

func TestMigrationReconcilePricingOverridesTable_PreservesExistingRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	err = db.AutoMigrate(&tables.TablePricingOverride{})
	require.NoError(t, err)

	inputCost := 1.25
	override := tables.TablePricingOverride{
		ID:           "override-1",
		Name:         "Config Override",
		ScopeKind:    pricingoverrides.ScopeKindGlobal,
		MatchType:    pricingoverrides.MatchTypeExact,
		Pattern:      "gpt-4.1",
		RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest},
		Patch: pricingoverrides.Patch{
			InputCostPerToken: &inputCost,
		},
		ConfigHash: "config-hash-1",
		CreatedAt:  time.Now().UTC().Round(time.Second),
		UpdatedAt:  time.Now().UTC().Round(time.Second),
	}
	require.NoError(t, db.Create(&override).Error)

	require.NoError(t, db.Migrator().DropIndex(&tables.TablePricingOverride{}, "idx_pricing_override_match"))
	require.False(t, db.Migrator().HasIndex(&tables.TablePricingOverride{}, "idx_pricing_override_match"))

	require.NoError(t, migrationReconcilePricingOverridesTable(context.Background(), db))

	var stored []tables.TablePricingOverride
	require.NoError(t, db.Order("id").Find(&stored).Error)
	require.Len(t, stored, 1)
	assert.Equal(t, override.ID, stored[0].ID)
	assert.Equal(t, override.Name, stored[0].Name)
	require.NotNil(t, stored[0].Patch.InputCostPerToken)
	assert.Equal(t, inputCost, *stored[0].Patch.InputCostPerToken)
	assert.Equal(t, override.ConfigHash, stored[0].ConfigHash)
	assert.True(t, db.Migrator().HasIndex(&tables.TablePricingOverride{}, "idx_pricing_override_scope"))
	assert.True(t, db.Migrator().HasIndex(&tables.TablePricingOverride{}, "idx_pricing_override_match"))
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

	// Create the gomigrate table for the migrator
	err = db.Exec(`
		CREATE TABLE IF NOT EXISTS gomigrate (
			id VARCHAR(255) PRIMARY KEY
		)
	`).Error
	require.NoError(t, err, "Failed to create gomigrate table")

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

	// Drop the table if it exists to start fresh (for this specific test)
	db.Exec("DROP TABLE IF EXISTS gomigrate")
	db.Exec("DROP TABLE IF EXISTS config_providers")

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

	// Create the gomigrate table for the migrator
	err = db.Exec(`
		CREATE TABLE IF NOT EXISTS gomigrate (
			id VARCHAR(255) PRIMARY KEY
		)
	`).Error
	if err != nil {
		return nil
	}

	// Clean up tables after the test
	t.Cleanup(func() {
		db.Exec("DROP TABLE IF EXISTS gomigrate")
		db.Exec("DROP TABLE IF EXISTS config_providers")
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
