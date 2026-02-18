package sapaicore

import (
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCacheKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		clientID string
		authURL  string
		expected string
	}{
		{
			name:     "basic key generation",
			clientID: "client123",
			authURL:  "https://auth.example.com",
			expected: "client123:https://auth.example.com",
		},
		{
			name:     "empty clientID",
			clientID: "",
			authURL:  "https://auth.example.com",
			expected: ":https://auth.example.com",
		},
		{
			name:     "empty authURL",
			clientID: "client123",
			authURL:  "",
			expected: "client123:",
		},
		{
			name:     "special characters",
			clientID: "client:with:colons",
			authURL:  "https://auth.example.com/oauth/token",
			expected: "client:with:colons:https://auth.example.com/oauth/token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cacheKey(tt.clientID, tt.authURL)
			if result != tt.expected {
				t.Errorf("cacheKey(%q, %q) = %q, want %q", tt.clientID, tt.authURL, result, tt.expected)
			}
		})
	}
}

func TestNewTokenCache(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	cache := NewTokenCache(client)

	if cache == nil {
		t.Fatal("NewTokenCache returned nil")
	}
	if cache.tokens == nil {
		t.Error("tokens map is nil")
	}
	if cache.client != client {
		t.Error("client not set correctly")
	}
}

func TestTokenCache_ClearToken(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	cache := NewTokenCache(client)

	// Manually add a token to the cache
	key := cacheKey("testClient", "https://auth.example.com")
	cache.tokens[key] = &cachedToken{
		accessToken: "test-token",
		expiresAt:   time.Now().Add(1 * time.Hour),
	}

	// Verify token exists
	if _, ok := cache.tokens[key]; !ok {
		t.Fatal("Token should exist before clearing")
	}

	// Clear the token
	cache.ClearToken("testClient", "https://auth.example.com")

	// Verify token is removed
	if _, ok := cache.tokens[key]; ok {
		t.Error("Token should be removed after ClearToken")
	}
}

func TestTokenCache_ClearToken_NonExistent(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	cache := NewTokenCache(client)

	// Should not panic when clearing non-existent token
	cache.ClearToken("nonexistent", "https://auth.example.com")
}

func TestTokenCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	cache := NewTokenCache(client)

	// Pre-populate cache with a valid token
	key := cacheKey("testClient", "https://auth.example.com")
	cache.tokens[key] = &cachedToken{
		accessToken: "test-token",
		expiresAt:   time.Now().Add(1 * time.Hour),
	}

	var wg sync.WaitGroup
	const numGoroutines = 100

	// Concurrent reads should not cause race conditions
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.mu.RLock()
			_ = cache.tokens[key]
			cache.mu.RUnlock()
		}()
	}

	// Concurrent clears should not cause race conditions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cache.ClearToken("testClient", "https://auth.example.com")
		}(i)
	}

	wg.Wait()
}

func TestCachedToken_Expiration(t *testing.T) {
	t.Parallel()

	// Test that a token with 30 second buffer is considered expired
	now := time.Now()

	tests := []struct {
		name         string
		expiresAt    time.Time
		shouldExpire bool
		description  string
	}{
		{
			name:         "future expiration - valid",
			expiresAt:    now.Add(2 * time.Minute),
			shouldExpire: false,
			description:  "Token expiring in 2 minutes should be valid",
		},
		{
			name:         "within buffer - should refresh",
			expiresAt:    now.Add(25 * time.Second),
			shouldExpire: true,
			description:  "Token expiring in 25 seconds should be refreshed (within 30s buffer)",
		},
		{
			name:         "past expiration",
			expiresAt:    now.Add(-1 * time.Minute),
			shouldExpire: true,
			description:  "Token that expired 1 minute ago should be refreshed",
		},
		{
			name:         "exactly at buffer boundary",
			expiresAt:    now.Add(30 * time.Second),
			shouldExpire: true,
			description:  "Token expiring exactly at buffer boundary should be refreshed",
		},
		{
			name:         "just past buffer boundary",
			expiresAt:    now.Add(31 * time.Second),
			shouldExpire: false,
			description:  "Token expiring just past buffer boundary should be valid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &cachedToken{
				accessToken: "test-token",
				expiresAt:   tt.expiresAt,
			}

			// Check if token would be considered expired with 30 second buffer
			isExpired := !time.Now().Add(30 * time.Second).Before(token.expiresAt)

			if isExpired != tt.shouldExpire {
				t.Errorf("%s: got expired=%v, want expired=%v", tt.description, isExpired, tt.shouldExpire)
			}
		})
	}
}

func TestTokenCache_Size(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	cache := NewTokenCache(client)

	// Add multiple tokens
	for i := 0; i < 10; i++ {
		key := cacheKey("client"+string(rune('0'+i)), "https://auth.example.com")
		cache.tokens[key] = &cachedToken{
			accessToken: "token-" + string(rune('0'+i)),
			expiresAt:   time.Now().Add(1 * time.Hour),
		}
	}

	if len(cache.tokens) != 10 {
		t.Errorf("Expected 10 tokens in cache, got %d", len(cache.tokens))
	}

	// Clear one token
	cache.ClearToken("client0", "https://auth.example.com")

	if len(cache.tokens) != 9 {
		t.Errorf("Expected 9 tokens in cache after clearing one, got %d", len(cache.tokens))
	}
}

func TestTokenCache_Cleanup(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	cache := NewTokenCache(client)

	// Add a mix of expired and valid tokens
	cache.tokens[cacheKey("expired1", "https://auth.example.com")] = &cachedToken{
		accessToken: "expired-token-1",
		expiresAt:   time.Now().Add(-1 * time.Hour), // Expired
	}
	cache.tokens[cacheKey("expired2", "https://auth.example.com")] = &cachedToken{
		accessToken: "expired-token-2",
		expiresAt:   time.Now().Add(-30 * time.Minute), // Expired
	}
	cache.tokens[cacheKey("valid1", "https://auth.example.com")] = &cachedToken{
		accessToken: "valid-token-1",
		expiresAt:   time.Now().Add(1 * time.Hour), // Valid
	}
	cache.tokens[cacheKey("valid2", "https://auth.example.com")] = &cachedToken{
		accessToken: "valid-token-2",
		expiresAt:   time.Now().Add(30 * time.Minute), // Valid
	}

	// Run cleanup
	cache.Cleanup()

	// Should only have valid tokens left
	if len(cache.tokens) != 2 {
		t.Errorf("Expected 2 tokens after cleanup, got %d", len(cache.tokens))
	}

	// Verify correct tokens remain
	if _, ok := cache.tokens[cacheKey("valid1", "https://auth.example.com")]; !ok {
		t.Error("valid1 token should remain after cleanup")
	}
	if _, ok := cache.tokens[cacheKey("valid2", "https://auth.example.com")]; !ok {
		t.Error("valid2 token should remain after cleanup")
	}
	if _, ok := cache.tokens[cacheKey("expired1", "https://auth.example.com")]; ok {
		t.Error("expired1 token should be removed after cleanup")
	}
	if _, ok := cache.tokens[cacheKey("expired2", "https://auth.example.com")]; ok {
		t.Error("expired2 token should be removed after cleanup")
	}
}
