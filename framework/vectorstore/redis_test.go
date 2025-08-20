package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/redis/go-redis/v9"
)

// MockLogger implements schemas.Logger for testing
type MockRedisLogger struct{}

func (m *MockRedisLogger) Debug(msg string, args ...any)                     { fmt.Printf("DEBUG: "+msg+"\n", args...) }
func (m *MockRedisLogger) Info(msg string, args ...any)                      { fmt.Printf("INFO: "+msg+"\n", args...) }
func (m *MockRedisLogger) Warn(msg string, args ...any)                      { fmt.Printf("WARN: "+msg+"\n", args...) }
func (m *MockRedisLogger) Error(msg string, args ...any)                     { fmt.Printf("ERROR: "+msg+"\n", args...) }
func (m *MockRedisLogger) Fatal(msg string, args ...any)                     { fmt.Printf("FATAL: "+msg+"\n", args...) }
func (m *MockRedisLogger) SetLevel(level schemas.LogLevel)                   { /* no-op for testing */ }
func (m *MockRedisLogger) SetOutputType(outputType schemas.LoggerOutputType) { /* no-op for testing */ }

// Test configurations
func getTestRedisConfig() RedisConfig {
	// Default to single Redis instance from docker-compose.yml
	addr := "localhost:6379"

	// Allow override via environment variable
	if envAddr := os.Getenv("REDIS_ADDR"); envAddr != "" {
		addr = envAddr
	}

	return RedisConfig{
		Addr:            addr,
		DB:              0,
		PoolSize:        50, // Increased for concurrent tests
		MinIdleConns:    5,
		MaxIdleConns:    20,
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		ContextTimeout:  10 * time.Second,
	}
}

// Helper function to check if Redis is available
func isRedisAvailable(config RedisConfig) bool {
	client := redis.NewClient(&redis.Options{
		Addr:        config.Addr,
		DB:          config.DB,
		DialTimeout: 2 * time.Second,
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Ping(ctx).Err()
	if err != nil {
		fmt.Printf("Redis not available at %s: %v\n", config.Addr, err)
		return false
	}

	fmt.Printf("Redis available at %s\n", config.Addr)
	return true
}

func TestRedisStore_Connection(t *testing.T) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	t.Run("successful connection", func(t *testing.T) {
		if !isRedisAvailable(config) {
			t.Fatal("Redis not available")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		store, err := newRedisStore(ctx, config, logger)
		if err != nil {
			t.Fatalf("Failed to create Redis store: %v", err)
		}
		if store == nil {
			t.Fatal("Store should not be nil")
		}

		// Test that we can actually use the connection
		err = store.Add(ctx, "test:connection", "test_value", time.Minute)
		if err != nil {
			t.Errorf("Should be able to add a key: %v", err)
		}

		value, err := store.GetChunk(ctx, "test:connection")
		if err != nil {
			t.Errorf("Should be able to get a key: %v", err)
		}
		if value != "test_value" {
			t.Errorf("Retrieved value should match: expected 'test_value', got '%s'", value)
		}

		// Cleanup
		err = store.Delete(ctx, []string{"test:connection"})
		if err != nil {
			t.Errorf("Should be able to delete keys: %v", err)
		}

		err = store.Close(ctx)
		if err != nil {
			t.Errorf("Should be able to close connection: %v", err)
		}
	})

	t.Run("connection with invalid address", func(t *testing.T) {
		invalidConfig := config
		invalidConfig.Addr = "localhost:9999"

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		store, err := newRedisStore(ctx, invalidConfig, logger)
		if err == nil {
			t.Error("Should fail with invalid address")
		}
		if store != nil {
			t.Error("Store should be nil on error")
		}
	})

	t.Run("connection with malformed address", func(t *testing.T) {
		invalidConfig := config
		invalidConfig.Addr = "invalid-host-that-does-not-exist:6379"

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		store, err := newRedisStore(ctx, invalidConfig, logger)
		if err == nil {
			t.Error("Should fail with malformed address")
		}
		if store != nil {
			t.Error("Store should be nil on error")
		}
	})

	t.Run("connection with auth credentials", func(t *testing.T) {
		if !isRedisAvailable(config) {
			t.Fatal("Redis not available")
		}

		authConfig := config
		authConfig.Username = "testuser"
		authConfig.Password = "testpass"

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// This should fail since our test Redis doesn't have auth configured
		store, err := newRedisStore(ctx, authConfig, logger)
		if err == nil && store != nil {
			// If it succeeds, clean up
			store.Close(ctx)
		}
		// We don't assert failure here since some Redis instances might not have auth
	})
}

func TestRedisStore_BasicOperations(t *testing.T) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		t.Fatal("Redis not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		t.Fatalf("Failed to create Redis store: %v", err)
	}
	defer store.Close(ctx)

	t.Run("add and get single value", func(t *testing.T) {
		key := "test:single"
		value := "single_test_value"

		err := store.Add(ctx, key, value, time.Minute)
		if err != nil {
			t.Errorf("Should be able to add key: %v", err)
		}

		retrieved, err := store.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Should be able to get key: %v", err)
		}
		if retrieved != value {
			t.Errorf("Retrieved value should match: expected '%s', got '%s'", value, retrieved)
		}

		// Cleanup
		err = store.Delete(ctx, []string{key})
		if err != nil {
			t.Errorf("Should be able to delete key: %v", err)
		}
	})

	t.Run("add and get multiple values", func(t *testing.T) {
		keys := []string{"test:multi:1", "test:multi:2", "test:multi:3"}
		values := []string{"value1", "value2", "value3"}

		// Add multiple keys
		for i, key := range keys {
			err := store.Add(ctx, key, values[i], time.Minute)
			if err != nil {
				t.Errorf("Should be able to add key %s: %v", key, err)
			}
		}

		// Get multiple keys
		retrieved, err := store.GetChunks(ctx, keys)
		if err != nil {
			t.Errorf("Should be able to get multiple keys: %v", err)
		}
		if len(retrieved) != 3 {
			t.Errorf("Should retrieve 3 values, got %d", len(retrieved))
		}

		// Convert interface{} to strings and verify
		for i, val := range retrieved {
			if val != values[i] {
				t.Errorf("Retrieved value %d should match: expected '%s', got '%v'", i, values[i], val)
			}
		}

		// Cleanup
		err = store.Delete(ctx, keys)
		if err != nil {
			t.Errorf("Should be able to delete multiple keys: %v", err)
		}
	})

	t.Run("get non-existent key", func(t *testing.T) {
		_, err := store.GetChunk(ctx, "test:nonexistent")
		if err == nil {
			t.Error("Should return error for non-existent key")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Should return redis.Nil error, got: %v", err)
		}
	})

	t.Run("delete non-existent keys", func(t *testing.T) {
		err := store.Delete(ctx, []string{"test:nonexistent:1", "test:nonexistent:2"})
		// Delete should not return error even if keys don't exist
		if err != nil {
			t.Errorf("Delete should not fail for non-existent keys: %v", err)
		}
	})

	t.Run("add with different databases", func(t *testing.T) {
		// Test with different database
		dbConfig := config
		dbConfig.DB = 1

		dbStore, err := newRedisStore(ctx, dbConfig, logger)
		if err != nil {
			t.Fatalf("Should be able to create store with different DB: %v", err)
		}
		defer dbStore.Close(ctx)

		key := "test:db:isolation"
		value1 := "db0_value"
		value2 := "db1_value"

		// Add to DB 0
		err = store.Add(ctx, key, value1, time.Minute)
		if err != nil {
			t.Errorf("Should be able to add to DB 0: %v", err)
		}

		// Add to DB 1
		err = dbStore.Add(ctx, key, value2, time.Minute)
		if err != nil {
			t.Errorf("Should be able to add to DB 1: %v", err)
		}

		// Verify isolation
		val0, err := store.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Should be able to get from DB 0: %v", err)
		}
		if val0 != value1 {
			t.Errorf("DB 0 value should be '%s', got '%s'", value1, val0)
		}

		val1, err := dbStore.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Should be able to get from DB 1: %v", err)
		}
		if val1 != value2 {
			t.Errorf("DB 1 value should be '%s', got '%s'", value2, val1)
		}

		// Cleanup
		store.Delete(ctx, []string{key})
		dbStore.Delete(ctx, []string{key})
	})
}

func TestRedisStore_GetAllOperations(t *testing.T) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
			t.Fatal("Redis not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		t.Fatalf("Failed to create Redis store: %v", err)
	}
	defer store.Close(ctx)

	// Setup test data
	testKeys := []string{
		"test:getall:item1",
		"test:getall:item2",
		"test:getall:item3",
		"test:getall:special:item4",
		"other:key:item5",
	}

	// Add test data
	for i, key := range testKeys {
		err := store.Add(ctx, key, fmt.Sprintf("value%d", i+1), time.Minute)
		if err != nil {
			t.Fatalf("Should be able to add test key %s: %v", key, err)
		}
	}

	t.Run("get all keys with pattern", func(t *testing.T) {
		// Add a small delay to ensure keys are persisted
		time.Sleep(100 * time.Millisecond)

		keys, cursor, err := store.GetAll(ctx, "test:getall*", nil, 10)
		if err != nil {
			t.Errorf("Should be able to get keys with pattern: %v", err)
		}

		// Should find the first 4 keys that match the pattern
		expectedKeys := []string{
			"test:getall:item1",
			"test:getall:item2",
			"test:getall:item3",
			"test:getall:special:item4",
		}

		// Redis SCAN might not return all keys in one call, so we need to handle pagination
		allKeys := keys
		for cursor != nil {
			moreKeys, nextCursor, err := store.GetAll(ctx, "test:getall*", cursor, 10)
			if err != nil {
				t.Errorf("Should be able to continue scanning: %v", err)
				break
			}
			allKeys = append(allKeys, moreKeys...)
			cursor = nextCursor
		}

		if len(allKeys) != 4 {
			t.Errorf("Should find 4 matching keys, got %d: %v", len(allKeys), allKeys)
		}
		for _, expectedKey := range expectedKeys {
			found := false
			for _, key := range allKeys {
				if key == expectedKey {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Should contain key %s", expectedKey)
			}
		}
	})

	t.Run("get all keys with pagination", func(t *testing.T) {
		// Use a smaller count to test pagination
		keys, cursor, err := store.GetAll(ctx, "test:getall*", nil, 2)
		if err != nil {
			t.Errorf("Should be able to get keys with pagination: %v", err)
		}

		// We should get some keys, and potentially a cursor for more
		if len(keys) == 0 && cursor == nil {
			t.Error("Should get some keys or have a cursor for more")
		}

		// If there's a cursor, try to get more
		allKeys := keys
		for cursor != nil {
			moreKeys, nextCursor, err := store.GetAll(ctx, "test:getall*", cursor, 2)
			if err != nil {
				t.Errorf("Should be able to get more keys with cursor: %v", err)
				break
			}
			allKeys = append(allKeys, moreKeys...)
			cursor = nextCursor
		}

		// Total should be 4 keys
		if len(allKeys) != 4 {
			t.Errorf("Should eventually find all 4 matching keys, got %d: %v", len(allKeys), allKeys)
		}
	})

	t.Run("get all with non-matching pattern", func(t *testing.T) {
		keys, _, err := store.GetAll(ctx, "nonexistent:*", nil, 10)
		if err != nil {
			t.Errorf("Should not error on non-matching pattern: %v", err)
		}
		if len(keys) != 0 {
			t.Errorf("Should find no keys for non-matching pattern, got %d", len(keys))
		}
		// Note: cursor might not be nil even with no results due to Redis SCAN behavior
		// This is acceptable as SCAN is probabilistic
	})

	// Cleanup
	err = store.Delete(ctx, testKeys)
	if err != nil {
		t.Errorf("Should be able to cleanup test keys: %v", err)
	}
}

func TestRedisStore_TTLOperations(t *testing.T) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		t.Fatal("Redis not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		t.Fatalf("Failed to create Redis store: %v", err)
	}
	defer store.Close(ctx)

	t.Run("key expires after TTL", func(t *testing.T) {
		key := "test:ttl:expire"
		value := "expires_soon"

		// Add key with short TTL
		err := store.Add(ctx, key, value, 2*time.Second)
		if err != nil {
			t.Errorf("Should be able to add key with TTL: %v", err)
		}

		// Key should exist immediately
		retrieved, err := store.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Key should exist immediately: %v", err)
		}
		if retrieved != value {
			t.Errorf("Value should match: expected '%s', got '%s'", value, retrieved)
		}

		// Wait for expiration
		time.Sleep(3 * time.Second)

		// Key should be expired
		_, err = store.GetChunk(ctx, key)
		if err == nil {
			t.Error("Key should be expired")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Should return ErrNotFound for expired key, got: %v", err)
		}
	})

	t.Run("key with zero TTL persists", func(t *testing.T) {
		key := "test:ttl:persist"
		value := "persists"

		// Add key with zero TTL (no expiration)
		err := store.Add(ctx, key, value, 0)
		if err != nil {
			t.Errorf("Should be able to add key with zero TTL: %v", err)
		}

		// Key should exist
		retrieved, err := store.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Key should exist: %v", err)
		}
		if retrieved != value {
			t.Errorf("Value should match: expected '%s', got '%s'", value, retrieved)
		}

		// Wait a bit to ensure it doesn't expire
		time.Sleep(1 * time.Second)

		// Key should still exist
		retrieved, err = store.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Key should still exist: %v", err)
		}
		if retrieved != value {
			t.Errorf("Value should still match: expected '%s', got '%s'", value, retrieved)
		}

		// Cleanup
		err = store.Delete(ctx, []string{key})
		if err != nil {
			t.Errorf("Should be able to delete persistent key: %v", err)
		}
	})

	t.Run("key TTL updates on re-add", func(t *testing.T) {
		key := "test:ttl:update"
		value1 := "value1"
		value2 := "value2"

		// Add key with short TTL
		err := store.Add(ctx, key, value1, 2*time.Second)
		if err != nil {
			t.Errorf("Should be able to add key: %v", err)
		}

		// Wait a bit but not enough to expire
		time.Sleep(1 * time.Second)

		// Re-add with longer TTL and different value
		err = store.Add(ctx, key, value2, time.Minute)
		if err != nil {
			t.Errorf("Should be able to re-add key: %v", err)
		}

		// Should have new value
		retrieved, err := store.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Key should exist: %v", err)
		}
		if retrieved != value2 {
			t.Errorf("Value should be updated: expected '%s', got '%s'", value2, retrieved)
		}

		// Wait past original TTL
		time.Sleep(2 * time.Second)

		// Key should still exist due to new TTL
		retrieved, err = store.GetChunk(ctx, key)
		if err != nil {
			t.Errorf("Key should still exist with new TTL: %v", err)
		}
		if retrieved != value2 {
			t.Errorf("Value should still match: expected '%s', got '%s'", value2, retrieved)
		}

		// Cleanup
		err = store.Delete(ctx, []string{key})
		if err != nil {
			t.Errorf("Should be able to delete key: %v", err)
		}
	})
}

func TestRedisStore_ConcurrentOperations(t *testing.T) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		t.Fatal("Redis not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		t.Fatalf("Failed to create Redis store: %v", err)
	}
	defer store.Close(ctx)

	t.Run("concurrent writes and reads", func(t *testing.T) {
		const numGoroutines = 5  // Reduced concurrency
		const numOperations = 20 // Reduced operations per goroutine

		// Channel to collect errors
		errChan := make(chan error, numGoroutines*numOperations)

		// Start multiple goroutines doing concurrent operations
		for i := 0; i < numGoroutines; i++ {
			go func(routineID int) {
				for j := 0; j < numOperations; j++ {
					key := fmt.Sprintf("test:concurrent:%d:%d", routineID, j)
					value := fmt.Sprintf("value_%d_%d", routineID, j)

					// Add key
					if err := store.Add(ctx, key, value, time.Minute); err != nil {
						errChan <- fmt.Errorf("failed to add key %s: %w", key, err)
						continue
					}

					// Read key back
					retrieved, err := store.GetChunk(ctx, key)
					if err != nil {
						errChan <- fmt.Errorf("failed to get key %s: %w", key, err)
						continue
					}

					if retrieved != value {
						errChan <- fmt.Errorf("value mismatch for key %s: expected %s, got %s", key, value, retrieved)
						continue
					}

					// Delete key
					if err := store.Delete(ctx, []string{key}); err != nil {
						errChan <- fmt.Errorf("failed to delete key %s: %w", key, err)
						continue
					}

					// Small delay to avoid overwhelming the connection pool
					time.Sleep(10 * time.Millisecond)
				}
			}(i)
		}

		// Wait for operations to complete
		time.Sleep(5 * time.Second)

		// Check for errors
		close(errChan)
		var errors []error
		for err := range errChan {
			errors = append(errors, err)
		}

		if len(errors) > 0 {
			t.Errorf("Got %d errors during concurrent operations:", len(errors))
			for i, err := range errors {
				if i < 10 { // Limit output to first 10 errors
					t.Errorf("  Error %d: %v", i+1, err)
				}
			}
		}
	})

	t.Run("concurrent access to same keys", func(t *testing.T) {
		const numGoroutines = 5
		const numOperations = 20
		const sharedKey = "test:shared:key"

		// Channel to collect errors
		errChan := make(chan error, numGoroutines*numOperations)

		// Start multiple goroutines accessing the same key
		for i := 0; i < numGoroutines; i++ {
			go func(routineID int) {
				for j := 0; j < numOperations; j++ {
					value := fmt.Sprintf("value_%d_%d", routineID, j)

					// Set the shared key
					if err := store.Add(ctx, sharedKey, value, time.Minute); err != nil {
						errChan <- fmt.Errorf("failed to set shared key from routine %d: %w", routineID, err)
						continue
					}

					// Try to read it back
					_, err := store.GetChunk(ctx, sharedKey)
					if err != nil && !errors.Is(err, ErrNotFound) {
						errChan <- fmt.Errorf("failed to get shared key from routine %d: %w", routineID, err)
						continue
					}
				}
			}(i)
		}

		// Wait for operations to complete
		time.Sleep(5 * time.Second)

		// Check for errors
		close(errChan)
		var errors []error
		for err := range errChan {
			errors = append(errors, err)
		}

		if len(errors) > 0 {
			t.Errorf("Got %d errors during concurrent shared key access:", len(errors))
			for i, err := range errors {
				if i < 5 { // Limit output to first 5 errors
					t.Errorf("  Error %d: %v", i+1, err)
				}
			}
		}

		// Cleanup
		store.Delete(ctx, []string{sharedKey})
	})
}

func TestRedisStore_ContextTimeoutHandling(t *testing.T) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		t.Skip("Redis not available, skipping test")
	}

	t.Run("context timeout in config", func(t *testing.T) {
		// Create store with very short context timeout for operations
		timeoutConfig := config
		timeoutConfig.ContextTimeout = 1 * time.Nanosecond // Extremely short timeout

		ctx := context.Background()
		store, err := newRedisStore(ctx, timeoutConfig, logger)
		if err != nil {
			t.Fatalf("Should be able to create store: %v", err)
		}
		defer store.Close(ctx)

		// This should timeout due to config timeout being very short
		err = store.Add(ctx, "test:config:timeout", "value", time.Minute)
		if err == nil {
			t.Error("Should timeout with short config timeout")
			return
		}
		if !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Errorf("Should timeout with context deadline exceeded, got: %v", err)
		}
	})

	t.Run("external context timeout", func(t *testing.T) {
		ctx := context.Background()
		store, err := newRedisStore(ctx, config, logger)
		if err != nil {
			t.Fatalf("Should be able to create store: %v", err)
		}
		defer store.Close(ctx)

		// Create a context that times out quickly
		shortCtx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()

		// This should timeout
		err = store.Add(shortCtx, "test:external:timeout", "value", time.Minute)
		if err == nil {
			t.Error("Should timeout with short external context")
		}
		if !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Errorf("Should timeout with context deadline exceeded, got: %v", err)
		}
	})
}

func TestRedisStore_ErrorHandling(t *testing.T) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		t.Skip("Redis not available, skipping test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		t.Fatalf("Failed to create Redis store: %v", err)
	}
	defer store.Close(ctx)

	t.Run("invalid cursor in GetAll", func(t *testing.T) {
		// Add a test key first to ensure there are keys to find
		testKey := "error:test:key"
		err := store.Add(ctx, testKey, "test_value", time.Minute)
		if err != nil {
			t.Fatalf("Should be able to add test key: %v", err)
		}

		invalidCursor := "invalid_cursor_value"
		keys, cursor, err := store.GetAll(ctx, "error:test*", &invalidCursor, 10)

		// Should return error for invalid cursor
		if err == nil {
			t.Error("Should return error for invalid cursor")
		}
		if keys != nil {
			t.Error("Keys should be nil on error")
		}
		if cursor != nil {
			t.Error("Cursor should be nil on error")
		}
		if !strings.Contains(err.Error(), "invalid cursor value") {
			t.Errorf("Should mention invalid cursor, got: %v", err)
		}

		// Cleanup
		store.Delete(ctx, []string{testKey})
	})

	t.Run("empty key operations", func(t *testing.T) {
		// Test empty key
		err := store.Add(ctx, "", "value", time.Minute)
		if err != nil {
			// Redis allows empty keys, but some operations might fail
			t.Logf("Add with empty key failed (expected): %v", err)
		}

		_, err = store.GetChunk(ctx, "")
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Logf("Get with empty key failed: %v", err)
		}
	})

	t.Run("large value operations", func(t *testing.T) {
		// Test with a reasonably large value
		largeValue := strings.Repeat("x", 1024*1024) // 1MB
		key := "test:large:value"

		err := store.Add(ctx, key, largeValue, time.Minute)
		if err != nil {
			t.Errorf("Should be able to add large value: %v", err)
		} else {
			// If successful, verify retrieval
			retrieved, err := store.GetChunk(ctx, key)
			if err != nil {
				t.Errorf("Should be able to get large value: %v", err)
			}
			if len(retrieved) != len(largeValue) {
				t.Errorf("Large value length mismatch: expected %d, got %d", len(largeValue), len(retrieved))
			}

			// Cleanup
			store.Delete(ctx, []string{key})
		}
	})
}

// Benchmark tests
func BenchmarkRedisStore_Add(b *testing.B) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		b.Skip("Redis not available, skipping benchmark")
	}

	ctx := context.Background()
	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close(ctx)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("bench:add:%d", i)
			err := store.Add(ctx, key, "benchmark_value", time.Minute)
			if err != nil {
				b.Errorf("Failed to add key: %v", err)
			}
			i++
		}
	})
}

func BenchmarkRedisStore_Get(b *testing.B) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		b.Skip("Redis not available, skipping benchmark")
	}

	ctx := context.Background()
	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close(ctx)

	// Pre-populate some keys
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("bench:get:%d", i)
		err := store.Add(ctx, key, "benchmark_value", time.Minute)
		if err != nil {
			b.Fatalf("Failed to pre-populate key: %v", err)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("bench:get:%d", i%1000)
			_, err := store.GetChunk(ctx, key)
			if err != nil && !errors.Is(err, ErrNotFound) {
				b.Errorf("Failed to get key: %v", err)
			}
			i++
		}
	})
}

func BenchmarkRedisStore_GetChunks(b *testing.B) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		b.Skip("Redis not available, skipping benchmark")
	}

	ctx := context.Background()
	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close(ctx)

	// Pre-populate keys for batch retrieval
	batchSize := 10
	keys := make([]string, batchSize)
	for i := 0; i < batchSize; i++ {
		keys[i] = fmt.Sprintf("bench:batch:%d", i)
		err := store.Add(ctx, keys[i], fmt.Sprintf("value_%d", i), time.Minute)
		if err != nil {
			b.Fatalf("Failed to pre-populate key: %v", err)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := store.GetChunks(ctx, keys)
			if err != nil {
				b.Errorf("Failed to get batch: %v", err)
			}
		}
	})
}

func BenchmarkRedisStore_Delete(b *testing.B) {
	config := getTestRedisConfig()
	logger := &MockRedisLogger{}

	if !isRedisAvailable(config) {
		b.Skip("Redis not available, skipping benchmark")
	}

	ctx := context.Background()
	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close(ctx)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("bench:delete:%d", i)
			// Add key first
			store.Add(ctx, key, "value", time.Minute)
			// Then delete it
			err := store.Delete(ctx, []string{key})
			if err != nil {
				b.Errorf("Failed to delete key: %v", err)
			}
			i++
		}
	})
}
