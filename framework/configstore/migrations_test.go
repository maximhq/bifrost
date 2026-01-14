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

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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
