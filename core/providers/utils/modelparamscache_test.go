package utils

import (
	"fmt"
	"sync"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestModelParamsCacheGetSet(t *testing.T) {
	cache := newModelParamsCache(10)

	cache.Set("claude-sonnet-4-20250514", ModelParams{MaxOutputTokens: intPtr(8192)})
	val, ok := cache.Get("claude-sonnet-4-20250514")
	if !ok || val.MaxOutputTokens == nil || *val.MaxOutputTokens != 8192 {
		t.Errorf("expected 8192, got %+v (ok=%v)", val, ok)
	}
}

func TestModelParamsCacheMiss(t *testing.T) {
	cache := newModelParamsCache(10)

	val, ok := cache.Get("nonexistent-model")
	if ok || val.MaxOutputTokens != nil {
		t.Errorf("expected miss, got %+v (ok=%v)", val, ok)
	}
}

func TestModelParamsCacheUpdate(t *testing.T) {
	cache := newModelParamsCache(10)

	cache.Set("claude-sonnet-4", ModelParams{MaxOutputTokens: intPtr(8192)})
	cache.Set("claude-sonnet-4", ModelParams{MaxOutputTokens: intPtr(16384)})

	val, ok := cache.Get("claude-sonnet-4")
	if !ok || val.MaxOutputTokens == nil || *val.MaxOutputTokens != 16384 {
		t.Errorf("expected 16384 after update, got %+v (ok=%v)", val, ok)
	}
}

func TestModelParamsCacheEviction(t *testing.T) {
	cache := newModelParamsCache(3)

	cache.Set("model-a", ModelParams{MaxOutputTokens: intPtr(1000)})
	cache.Set("model-b", ModelParams{MaxOutputTokens: intPtr(2000)})
	cache.Set("model-c", ModelParams{MaxOutputTokens: intPtr(3000)})
	// This should evict model-a (oldest insertion)
	cache.Set("model-d", ModelParams{MaxOutputTokens: intPtr(4000)})

	if _, ok := cache.Get("model-a"); ok {
		t.Error("model-a should have been evicted")
	}
	if val, ok := cache.Get("model-b"); !ok || *val.MaxOutputTokens != 2000 {
		t.Errorf("model-b should still exist, got %+v (ok=%v)", val, ok)
	}
	if val, ok := cache.Get("model-d"); !ok || *val.MaxOutputTokens != 4000 {
		t.Errorf("model-d should exist, got %+v (ok=%v)", val, ok)
	}
}

func TestModelParamsCacheBulkSet(t *testing.T) {
	cache := newModelParamsCache(100)

	entries := map[string]ModelParams{
		"claude-sonnet-4":  {MaxOutputTokens: intPtr(8192)},
		"claude-opus-4":    {MaxOutputTokens: intPtr(4096)},
		"gpt-4o":           {MaxOutputTokens: intPtr(16384)},
		"gemini-2.0-flash": {MaxOutputTokens: intPtr(8192)},
	}
	cache.BulkSet(entries)

	for model, expected := range entries {
		val, ok := cache.Get(model)
		if !ok || *val.MaxOutputTokens != *expected.MaxOutputTokens {
			t.Errorf("BulkSet: model %s expected %d, got %+v (ok=%v)", model, *expected.MaxOutputTokens, val, ok)
		}
	}
}

func TestModelParamsCacheBulkSetOverflow(t *testing.T) {
	cache := newModelParamsCache(3)

	entries := map[string]ModelParams{
		"model-1": {MaxOutputTokens: intPtr(1000)},
		"model-2": {MaxOutputTokens: intPtr(2000)},
		"model-3": {MaxOutputTokens: intPtr(3000)},
		"model-4": {MaxOutputTokens: intPtr(4000)},
		"model-5": {MaxOutputTokens: intPtr(5000)},
	}
	cache.BulkSet(entries)

	if cache.order.Len() != 3 {
		t.Errorf("expected 3 entries after overflow BulkSet, got %d", cache.order.Len())
	}
}

func TestModelParamsCacheBulkSetUpdate(t *testing.T) {
	cache := newModelParamsCache(10)

	cache.Set("claude-sonnet-4", ModelParams{MaxOutputTokens: intPtr(4096)})
	cache.BulkSet(map[string]ModelParams{
		"claude-sonnet-4": {MaxOutputTokens: intPtr(8192)},
	})

	val, ok := cache.Get("claude-sonnet-4")
	if !ok || *val.MaxOutputTokens != 8192 {
		t.Errorf("BulkSet should update existing entry, got %+v (ok=%v)", val, ok)
	}
}

func TestModelParamsCacheConcurrency(t *testing.T) {
	cache := newModelParamsCache(100)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("model-%d", i)
			cache.Set(model, ModelParams{MaxOutputTokens: intPtr(i * 1000)})
			cache.Get(model)
		}(i)
	}
	wg.Wait()

	if cache.order.Len() > 100 {
		t.Errorf("cache exceeded capacity: %d", cache.order.Len())
	}
}

func TestGetMaxOutputTokens(t *testing.T) {
	cache := getModelParamsCache()
	cache.Set("test-max-output", ModelParams{MaxOutputTokens: intPtr(16384)})

	val, ok := GetMaxOutputTokens("test-max-output")
	if !ok || val != 16384 {
		t.Errorf("expected 16384, got %d (ok=%v)", val, ok)
	}

	val, ok = GetMaxOutputTokens("missing-model-get")
	if ok || val != 0 {
		t.Errorf("expected miss, got %d (ok=%v)", val, ok)
	}
}

func TestGetMaxOutputTokensNilField(t *testing.T) {
	cache := getModelParamsCache()
	cache.Set("test-nil-field", ModelParams{})

	val, ok := GetMaxOutputTokens("test-nil-field")
	if ok || val != 0 {
		t.Errorf("expected miss for nil MaxOutputTokens, got %d (ok=%v)", val, ok)
	}
}

func TestGetMaxOutputTokensOrDefault(t *testing.T) {
	cache := getModelParamsCache()
	cache.Set("test-or-default", ModelParams{MaxOutputTokens: intPtr(16384)})

	val := GetMaxOutputTokensOrDefault("test-or-default", 4096)
	if val != 16384 {
		t.Errorf("expected cached value 16384, got %d", val)
	}

	val = GetMaxOutputTokensOrDefault("missing-model-default", 4096)
	if val != 4096 {
		t.Errorf("expected default 4096 for missing non-claude model, got %d", val)
	}
}

func TestCacheMissHandler(t *testing.T) {
	cache := newModelParamsCache(10)
	called := false
	cache.cacheMissHandler = func(model string) *ModelParams {
		called = true
		if model == "db-model" {
			return &ModelParams{MaxOutputTokens: intPtr(32000)}
		}
		return nil
	}

	// Miss handler returns a value → should be cached
	val, ok := cache.Get("db-model")
	if !ok || val.MaxOutputTokens == nil || *val.MaxOutputTokens != 32000 {
		t.Errorf("expected 32000 from miss handler, got %+v (ok=%v)", val, ok)
	}
	if !called {
		t.Error("miss handler was not called")
	}

	// Verify it was cached (handler should not be called again)
	called = false
	val, ok = cache.Get("db-model")
	if !ok || *val.MaxOutputTokens != 32000 {
		t.Errorf("expected cached 32000, got %+v (ok=%v)", val, ok)
	}
	if called {
		t.Error("miss handler should not be called for cached entry")
	}

	// Miss handler returns nil → should return false
	val, ok = cache.Get("unknown-model")
	if ok {
		t.Errorf("expected miss for unknown model, got %+v", val)
	}
}

func TestCacheMissHandlerNil(t *testing.T) {
	cache := newModelParamsCache(10)
	// No handler registered
	val, ok := cache.Get("any-model")
	if ok {
		t.Errorf("expected miss with nil handler, got %+v", val)
	}
}

func TestNormalizeClaudeModelName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		desc     string
	}{
		// Anthropic direct (bare model names)
		{"claude-sonnet-4-5", "claude-sonnet-4-5", "Anthropic: no version suffix"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4", "Anthropic: date suffix"},
		{"claude-opus-4-5", "claude-opus-4-5", "Anthropic: no version suffix"},
		{"claude-opus-4-6-20250514", "claude-opus-4-6", "Anthropic: date suffix"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", "Anthropic: no version suffix"},
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet", "Anthropic: legacy date suffix"},
		{"claude-3-7-sonnet-20250219", "claude-3-7-sonnet", "Anthropic: legacy date suffix"},

		// Bedrock (anthropic. prefix + -v1:0 suffix)
		{"anthropic.claude-3-sonnet-20240229-v1:0", "claude-3-sonnet", "Bedrock: prefix + v1:0"},
		{"anthropic.claude-opus-4-6-v1", "claude-opus-4-6", "Bedrock: prefix + v1 no colon"},
		{"anthropic.claude-3-7-sonnet-v1", "claude-3-7-sonnet", "Bedrock: prefix + v1 no colon"},
		{"anthropic.claude-sonnet-4-20250514-v1:0", "claude-sonnet-4", "Bedrock: prefix + date + v1:0"},
		{"anthropic.claude-3-5-sonnet-20241022-v1:0", "claude-3-5-sonnet", "Bedrock: prefix + legacy date + v1:0"},

		// Bedrock with region prefix
		{"us.anthropic.claude-sonnet-4-6", "claude-sonnet-4-6", "Bedrock regional: us prefix"},
		{"us.anthropic.claude-3-sonnet-20240229-v1:0", "claude-3-sonnet", "Bedrock regional: us + v1:0"},
		{"global.anthropic.claude-opus-4-6-20260301-v1:0", "claude-opus-4-6", "Bedrock regional: global + date + v1:0"},
		{"eu.anthropic.claude-sonnet-4-5-20250929-v1:0", "claude-sonnet-4-5", "Bedrock regional: eu + date + v1:0"},

		// Vertex (same as Anthropic direct — deployment is bare model name)
		{"claude-sonnet-4-5", "claude-sonnet-4-5", "Vertex: bare model"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4", "Vertex: date suffix"},

		// Azure (deployment names — typically bare model names)
		{"claude-opus-4-5", "claude-opus-4-5", "Azure: deployment name"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := normalizeClaudeModelName(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeClaudeModelName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetMaxOutputTokensOrDefaultStaticFallback(t *testing.T) {
	// Use a fresh cache with no entries to test static fallback only
	// We test via the normalizeClaudeModelName + map lookup directly
	// since the global cache may have entries from other tests
	tests := []struct {
		model    string
		expected int
		desc     string
	}{
		// Anthropic direct
		{"claude-sonnet-4-20250514", 64000, "Anthropic: claude-sonnet-4"},
		{"claude-opus-4-6-20250514", 128000, "Anthropic: claude-opus-4-6"},
		{"claude-3-5-sonnet-20241022", 8192, "Anthropic: claude-3-5-sonnet"},

		// Bedrock
		{"anthropic.claude-sonnet-4-20250514-v1:0", 64000, "Bedrock: claude-sonnet-4"},
		{"anthropic.claude-opus-4-6-v1", 128000, "Bedrock: claude-opus-4-6"},
		{"anthropic.claude-3-5-sonnet-20241022-v1:0", 8192, "Bedrock: claude-3-5-sonnet"},

		// Bedrock with region prefix
		{"us.anthropic.claude-opus-4-6-v1:0", 128000, "Bedrock regional: claude-opus-4-6"},
		{"global.anthropic.claude-sonnet-4-5-20250929-v1:0", 64000, "Bedrock regional: claude-sonnet-4-5"},
		{"eu.anthropic.claude-3-haiku-20240307-v1:0", 4096, "Bedrock regional: claude-3-haiku"},

		// Vertex
		{"claude-opus-4-5", 64000, "Vertex: claude-opus-4-5"},
		{"claude-haiku-4-5", 64000, "Vertex: claude-haiku-4-5"},

		// Azure
		{"claude-3-5-sonnet-20241022", 8192, "Azure: claude-3-5-sonnet"},
		{"claude-sonnet-4-6", 64000, "Azure: claude-sonnet-4-6"},

		// Non-Claude models should return the default
		{"gpt-4o", 4096, "Non-Claude: gpt-4o"},
		{"gemini-2.0-flash", 4096, "Non-Claude: gemini-2.0-flash"},
		{"command-r-plus", 4096, "Non-Claude: command-r-plus"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			// Test the static fallback logic directly
			got := staticAnthropicFallback(tt.model, 4096)
			if got != tt.expected {
				t.Errorf("staticAnthropicFallback(%q, 4096) = %d, want %d", tt.model, got, tt.expected)
			}
		})
	}
}

// staticAnthropicFallback is a test helper that mimics the fallback logic
// in GetMaxOutputTokensOrDefault without going through the global cache.
func staticAnthropicFallback(model string, defaultValue int) int {
	if !contains(model, "claude") {
		return defaultValue
	}
	base := normalizeClaudeModelName(model)
	if m, ok := knownAnthropicMaxOutputTokens[base]; ok {
		return m
	}
	return defaultValue
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || indexSubstring(s, substr) >= 0)
}

func indexSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
