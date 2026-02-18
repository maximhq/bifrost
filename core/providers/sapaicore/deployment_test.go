package sapaicore

import (
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestDeploymentCacheKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		baseURL       string
		resourceGroup string
		expected      string
	}{
		{
			name:          "basic key generation",
			baseURL:       "https://api.ai.sap.com",
			resourceGroup: "default",
			expected:      "https://api.ai.sap.com:default",
		},
		{
			name:          "with v2 suffix",
			baseURL:       "https://api.ai.sap.com/v2",
			resourceGroup: "my-group",
			expected:      "https://api.ai.sap.com/v2:my-group",
		},
		{
			name:          "empty baseURL",
			baseURL:       "",
			resourceGroup: "default",
			expected:      ":default",
		},
		{
			name:          "empty resourceGroup",
			baseURL:       "https://api.ai.sap.com",
			resourceGroup: "",
			expected:      "https://api.ai.sap.com:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deploymentCacheKey(tt.baseURL, tt.resourceGroup)
			if result != tt.expected {
				t.Errorf("deploymentCacheKey(%q, %q) = %q, want %q", tt.baseURL, tt.resourceGroup, result, tt.expected)
			}
		})
	}
}

func TestNewDeploymentCache(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)
	cache := NewDeploymentCache(client, tokenCache)

	if cache == nil {
		t.Fatal("NewDeploymentCache returned nil")
	}
	if cache.deployments == nil {
		t.Error("deployments map is nil")
	}
	if cache.client != client {
		t.Error("client not set correctly")
	}
	if cache.tokenCache != tokenCache {
		t.Error("tokenCache not set correctly")
	}
	if cache.ttl != DefaultDeploymentCacheTTL {
		t.Errorf("expected default TTL of %v, got %v", DefaultDeploymentCacheTTL, cache.ttl)
	}
}

func TestNewDeploymentCacheWithTTL(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)

	tests := []struct {
		name        string
		ttl         time.Duration
		expectedTTL time.Duration
	}{
		{
			name:        "custom TTL",
			ttl:         30 * time.Minute,
			expectedTTL: 30 * time.Minute,
		},
		{
			name:        "zero TTL uses default",
			ttl:         0,
			expectedTTL: DefaultDeploymentCacheTTL,
		},
		{
			name:        "negative TTL uses default",
			ttl:         -1 * time.Hour,
			expectedTTL: DefaultDeploymentCacheTTL,
		},
		{
			name:        "very short TTL clamped to minimum",
			ttl:         1 * time.Second,
			expectedTTL: MinDeploymentCacheTTL,
		},
		{
			name:        "TTL at minimum boundary",
			ttl:         MinDeploymentCacheTTL,
			expectedTTL: MinDeploymentCacheTTL,
		},
		{
			name:        "TTL just above minimum",
			ttl:         MinDeploymentCacheTTL + 1*time.Second,
			expectedTTL: MinDeploymentCacheTTL + 1*time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewDeploymentCacheWithTTL(client, tokenCache, tt.ttl)
			if cache.ttl != tt.expectedTTL {
				t.Errorf("expected TTL %v, got %v", tt.expectedTTL, cache.ttl)
			}
		})
	}
}

func TestDeploymentCache_ClearCache(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)
	cache := NewDeploymentCache(client, tokenCache)

	// Manually add a deployment to the cache
	key := deploymentCacheKey("https://api.ai.sap.com", "default")
	cache.deployments[key] = &cachedDeployments{
		modelToDeployment: map[string]CachedDeployment{
			"gpt-4": {DeploymentID: "d123", ModelName: "gpt-4", Backend: BackendOpenAI},
		},
		fetchedAt: time.Now(),
	}

	// Verify deployment exists
	if _, ok := cache.deployments[key]; !ok {
		t.Fatal("Deployment should exist before clearing")
	}

	// Clear the cache
	cache.ClearCache("https://api.ai.sap.com", "default")

	// Verify deployment is removed
	if _, ok := cache.deployments[key]; ok {
		t.Error("Deployment should be removed after ClearCache")
	}
}

func TestDeploymentCache_ClearCache_NonExistent(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)
	cache := NewDeploymentCache(client, tokenCache)

	// Should not panic when clearing non-existent cache entry
	cache.ClearCache("https://nonexistent.api.com", "nonexistent")
}

func TestDeploymentCache_ClearCache_All(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)
	cache := NewDeploymentCache(client, tokenCache)

	// Manually add multiple deployments to the cache
	key1 := deploymentCacheKey("https://api1.ai.sap.com", "group1")
	key2 := deploymentCacheKey("https://api2.ai.sap.com", "group2")

	cache.deployments[key1] = &cachedDeployments{
		modelToDeployment: map[string]CachedDeployment{
			"gpt-4": {DeploymentID: "d123", ModelName: "gpt-4", Backend: BackendOpenAI},
		},
		fetchedAt: time.Now(),
	}
	cache.deployments[key2] = &cachedDeployments{
		modelToDeployment: map[string]CachedDeployment{
			"claude-3": {DeploymentID: "d456", ModelName: "claude-3", Backend: BackendBedrock},
		},
		fetchedAt: time.Now(),
	}

	// Verify deployments exist
	if len(cache.deployments) != 2 {
		t.Fatalf("Expected 2 deployments, got %d", len(cache.deployments))
	}

	// Clear all cache entries by passing empty strings
	cache.ClearCache("", "")

	// Verify all deployments are removed
	if len(cache.deployments) != 0 {
		t.Errorf("Expected 0 deployments after ClearCache(\"\", \"\"), got %d", len(cache.deployments))
	}
}

func TestDeploymentCache_GetDeploymentID_StaticDeployments(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)
	cache := NewDeploymentCache(client, tokenCache)

	staticDeployments := map[string]string{
		"gpt-4":               "deployment-gpt4",
		"anthropic--claude-3": "deployment-claude",
	}

	tests := []struct {
		name               string
		modelName          string
		expectedDeployment string
		expectedBackend    BackendType
	}{
		{
			name:               "OpenAI model from static",
			modelName:          "gpt-4",
			expectedDeployment: "deployment-gpt4",
			expectedBackend:    BackendOpenAI,
		},
		{
			name:               "Bedrock model from static",
			modelName:          "anthropic--claude-3",
			expectedDeployment: "deployment-claude",
			expectedBackend:    BackendBedrock,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deploymentID, backend, err := cache.GetDeploymentID(
				tt.modelName,
				staticDeployments,
				"", "", "", "", "", // credentials not needed for static lookup
			)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if deploymentID != tt.expectedDeployment {
				t.Errorf("got deployment %q, want %q", deploymentID, tt.expectedDeployment)
			}
			if backend != tt.expectedBackend {
				t.Errorf("got backend %q, want %q", backend, tt.expectedBackend)
			}
		})
	}
}

func TestDeploymentCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)
	cache := NewDeploymentCache(client, tokenCache)

	// Pre-populate cache
	key := deploymentCacheKey("https://api.ai.sap.com", "default")
	cache.deployments[key] = &cachedDeployments{
		modelToDeployment: map[string]CachedDeployment{
			"gpt-4": {DeploymentID: "d123", ModelName: "gpt-4", Backend: BackendOpenAI},
		},
		fetchedAt: time.Now(),
	}

	var wg sync.WaitGroup
	const numGoroutines = 100

	// Concurrent reads should not cause race conditions
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.mu.RLock()
			_ = cache.deployments[key]
			cache.mu.RUnlock()
		}()
	}

	// Concurrent clears should not cause race conditions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.ClearCache("https://api.ai.sap.com", "default")
		}()
	}

	wg.Wait()
}

func TestDetermineBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		model    string
		expected BackendType
	}{
		{
			name:     "Anthropic model",
			model:    "anthropic--claude-3-sonnet",
			expected: BackendBedrock,
		},
		{
			name:     "Amazon model",
			model:    "amazon--titan-embed",
			expected: BackendBedrock,
		},
		{
			name:     "Gemini model",
			model:    "gemini-1.5-pro",
			expected: BackendVertex,
		},
		{
			name:     "OpenAI model",
			model:    "gpt-4",
			expected: BackendOpenAI,
		},
		{
			name:     "GPT-4 Turbo",
			model:    "gpt-4-turbo",
			expected: BackendOpenAI,
		},
		{
			name:     "Unknown model defaults to OpenAI",
			model:    "some-unknown-model",
			expected: BackendOpenAI,
		},
		{
			name:     "Empty model defaults to OpenAI",
			model:    "",
			expected: BackendOpenAI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetermineBackend(tt.model)
			if result != tt.expected {
				t.Errorf("DetermineBackend(%q) = %q, want %q", tt.model, result, tt.expected)
			}
		})
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already has /v2",
			input:    "https://api.ai.sap.com/v2",
			expected: "https://api.ai.sap.com/v2",
		},
		{
			name:     "no suffix",
			input:    "https://api.ai.sap.com",
			expected: "https://api.ai.sap.com/v2",
		},
		{
			name:     "trailing slash",
			input:    "https://api.ai.sap.com/",
			expected: "https://api.ai.sap.com/v2",
		},
		{
			name:     "empty URL",
			input:    "",
			expected: "/v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeBaseURL(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeBaseURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestDeploymentCache_TTLExpiration(t *testing.T) {
	t.Parallel()

	client := &fasthttp.Client{}
	tokenCache := NewTokenCache(client)
	cache := NewDeploymentCache(client, tokenCache)

	key := deploymentCacheKey("https://api.ai.sap.com", "default")

	// Test fresh cache entry (should be valid)
	cache.deployments[key] = &cachedDeployments{
		modelToDeployment: map[string]CachedDeployment{
			"gpt-4": {DeploymentID: "d123", ModelName: "gpt-4", Backend: BackendOpenAI},
		},
		fetchedAt: time.Now(),
	}

	// Check that fresh entry is within TTL
	if time.Since(cache.deployments[key].fetchedAt) >= cache.ttl {
		t.Error("fresh cache entry should be within TTL")
	}

	// Test expired cache entry
	cache.deployments[key] = &cachedDeployments{
		modelToDeployment: map[string]CachedDeployment{
			"gpt-4": {DeploymentID: "d123", ModelName: "gpt-4", Backend: BackendOpenAI},
		},
		fetchedAt: time.Now().Add(-2 * time.Hour), // Expired
	}

	// Check that expired entry is beyond TTL
	if time.Since(cache.deployments[key].fetchedAt) < cache.ttl {
		t.Error("expired cache entry should be beyond TTL")
	}
}

func TestCachedDeployment_Fields(t *testing.T) {
	t.Parallel()

	deployment := CachedDeployment{
		DeploymentID: "d12345",
		ModelName:    "gpt-4-turbo",
		Backend:      BackendOpenAI,
	}

	if deployment.DeploymentID != "d12345" {
		t.Errorf("expected DeploymentID 'd12345', got %q", deployment.DeploymentID)
	}
	if deployment.ModelName != "gpt-4-turbo" {
		t.Errorf("expected ModelName 'gpt-4-turbo', got %q", deployment.ModelName)
	}
	if deployment.Backend != BackendOpenAI {
		t.Errorf("expected Backend BackendOpenAI, got %q", deployment.Backend)
	}
}
