package utils

import (
	"fmt"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func capsWithMax(v int) *schemas.ModelCapabilities {
	return &schemas.ModelCapabilities{MaxOutputTokens: new(v)}
}

func TestCapabilitiesCacheStoreLookup(t *testing.T) {
	cache := newModelCapabilitiesCache(10)

	cache.store("claude-sonnet-4-20250514", capsWithMax(8192), 0)
	val, ok := cache.lookup("claude-sonnet-4-20250514", 0)
	if !ok || val == nil || *val.MaxOutputTokens != 8192 {
		t.Errorf("expected 8192, got %+v (ok=%v)", val, ok)
	}
}

func TestCapabilitiesCacheMiss(t *testing.T) {
	cache := newModelCapabilitiesCache(10)

	val, ok := cache.lookup("nonexistent-model", 0)
	if ok || val != nil {
		t.Errorf("expected miss, got %+v (ok=%v)", val, ok)
	}
}

// A tombstone is present-but-nil: found reports true so the caller knows the
// lookup was already resolved and must not re-query.
func TestCapabilitiesCacheTombstoneIsFound(t *testing.T) {
	cache := newModelCapabilitiesCache(10)

	cache.store("absent-model", nil, 0)
	val, ok := cache.lookup("absent-model", 0)
	if !ok {
		t.Error("tombstone should report found so the miss handler is not re-invoked")
	}
	if val != nil {
		t.Errorf("tombstone should carry a nil record, got %+v", val)
	}
}

// An entry written under an older generation reads as a miss, which is how a
// datasheet reload invalidates the whole cache without walking it.
func TestCapabilitiesCacheGenerationInvalidates(t *testing.T) {
	cache := newModelCapabilitiesCache(10)

	cache.store("model-a", capsWithMax(8192), 1)
	if _, ok := cache.lookup("model-a", 2); ok {
		t.Error("entry from an older generation should read as a miss")
	}
	// The stale entry is dropped rather than left to occupy a slot.
	if cache.order.Len() != 0 {
		t.Errorf("stale entry should be evicted on read, got %d entries", cache.order.Len())
	}
}

func TestCapabilitiesCacheUpdate(t *testing.T) {
	cache := newModelCapabilitiesCache(10)

	cache.store("claude-sonnet-4", capsWithMax(8192), 0)
	cache.store("claude-sonnet-4", capsWithMax(16384), 0)

	val, ok := cache.lookup("claude-sonnet-4", 0)
	if !ok || val == nil || *val.MaxOutputTokens != 16384 {
		t.Errorf("expected 16384 after update, got %+v (ok=%v)", val, ok)
	}
}

func TestCapabilitiesCacheEviction(t *testing.T) {
	cache := newModelCapabilitiesCache(3)

	cache.store("model-a", capsWithMax(1000), 0)
	cache.store("model-b", capsWithMax(2000), 0)
	cache.store("model-c", capsWithMax(3000), 0)
	// This should evict model-a (oldest insertion)
	cache.store("model-d", capsWithMax(4000), 0)

	if _, ok := cache.lookup("model-a", 0); ok {
		t.Error("model-a should have been evicted")
	}
	if val, ok := cache.lookup("model-b", 0); !ok || *val.MaxOutputTokens != 2000 {
		t.Errorf("model-b should still exist, got %+v (ok=%v)", val, ok)
	}
	if val, ok := cache.lookup("model-d", 0); !ok || *val.MaxOutputTokens != 4000 {
		t.Errorf("model-d should exist, got %+v (ok=%v)", val, ok)
	}
}

// Capacity 0 means unbounded, which is what the no-config-store mode needs:
// with no miss handler nothing can refill an evicted entry.
func TestCapabilitiesCacheUnboundedCapacity(t *testing.T) {
	cache := newModelCapabilitiesCache(0)

	for i := range 5000 {
		cache.store(fmt.Sprintf("model-%d", i), capsWithMax(i), 0)
	}
	if cache.order.Len() != 5000 {
		t.Errorf("capacity 0 should never evict, got %d entries", cache.order.Len())
	}
}

func TestCapabilitiesCacheBulkStore(t *testing.T) {
	cache := newModelCapabilitiesCache(100)

	entries := map[string]*schemas.ModelCapabilities{
		"claude-sonnet-4":  capsWithMax(8192),
		"claude-opus-4":    capsWithMax(4096),
		"gpt-4o":           capsWithMax(16384),
		"gemini-2.0-flash": capsWithMax(8192),
	}
	cache.bulkStore(entries, 0)

	for model, expected := range entries {
		val, ok := cache.lookup(model, 0)
		if !ok || *val.MaxOutputTokens != *expected.MaxOutputTokens {
			t.Errorf("bulkStore: model %s expected %d, got %+v (ok=%v)", model, *expected.MaxOutputTokens, val, ok)
		}
	}
}

func TestCapabilitiesCacheBulkStoreOverflow(t *testing.T) {
	cache := newModelCapabilitiesCache(3)

	cache.bulkStore(map[string]*schemas.ModelCapabilities{
		"model-1": capsWithMax(1000),
		"model-2": capsWithMax(2000),
		"model-3": capsWithMax(3000),
		"model-4": capsWithMax(4000),
		"model-5": capsWithMax(5000),
	}, 0)

	if cache.order.Len() != 3 {
		t.Errorf("expected 3 entries after overflow bulkStore, got %d", cache.order.Len())
	}
}

func TestCapabilitiesCacheBulkStoreUpdate(t *testing.T) {
	cache := newModelCapabilitiesCache(10)

	cache.store("claude-sonnet-4", capsWithMax(4096), 0)
	cache.bulkStore(map[string]*schemas.ModelCapabilities{
		"claude-sonnet-4": capsWithMax(8192),
	}, 0)

	val, ok := cache.lookup("claude-sonnet-4", 0)
	if !ok || *val.MaxOutputTokens != 8192 {
		t.Errorf("bulkStore should update existing entry, got %+v (ok=%v)", val, ok)
	}
}

func TestCapabilitiesCacheConcurrency(t *testing.T) {
	cache := newModelCapabilitiesCache(100)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("model-%d", i)
			cache.store(model, capsWithMax(i*1000), 0)
			cache.lookup(model, 0)
		}(i)
	}
	wg.Wait()

	if cache.order.Len() > 100 {
		t.Errorf("cache exceeded capacity: %d", cache.order.Len())
	}
}

func TestGetMaxOutputTokensOrDefault(t *testing.T) {
	key := CapabilityCacheKey("test-or-default", schemas.Anthropic)
	SetModelCapability(key, capsWithMax(16384))
	t.Cleanup(func() { DeleteModelCapability(key) })

	val := GetMaxOutputTokensOrDefault(schemas.Anthropic, "test-or-default", 4096)
	if val != 16384 {
		t.Errorf("expected cached value 16384, got %d", val)
	}

	val = GetMaxOutputTokensOrDefault(schemas.OpenAI, "missing-model-default", 4096)
	if val != 4096 {
		t.Errorf("expected default 4096 for missing non-claude model, got %d", val)
	}
}

func TestCapabilitiesCacheFetchDedupes(t *testing.T) {
	cache := newModelCapabilitiesCache(10)
	calls := 0
	handler := func(rowKey string) *schemas.ModelCapabilities {
		calls++
		if rowKey == "db-model" {
			return capsWithMax(32000)
		}
		return nil
	}

	got := cache.fetch("db-model", handler)
	if got == nil || *got.MaxOutputTokens != 32000 {
		t.Errorf("expected 32000 from miss handler, got %+v", got)
	}
	if calls != 1 {
		t.Errorf("expected 1 handler call, got %d", calls)
	}

	if got := cache.fetch("unknown-model", handler); got != nil {
		t.Errorf("expected nil for unknown model, got %+v", got)
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
