package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// TestShouldUseFilterDataCacheAllowsUnscopedEmptyQuery verifies unscoped
// requests can still share the no-query filterdata cache.
func TestShouldUseFilterDataCacheAllowsUnscopedEmptyQuery(t *testing.T) {
	if !shouldUseFilterDataCache(context.Background(), "") {
		t.Fatal("expected unscoped empty-query request to use filterdata cache")
	}
	if !shouldUseFilterDataCache(context.Background(), "   ") {
		t.Fatal("expected whitespace-only query to use filterdata cache")
	}
}

// TestShouldUseFilterDataCacheRejectsSearchQuery verifies search requests are
// request-specific and must not share the empty-query cache.
func TestShouldUseFilterDataCacheRejectsSearchQuery(t *testing.T) {
	if shouldUseFilterDataCache(context.Background(), "vk") {
		t.Fatal("expected non-empty query to bypass filterdata cache")
	}
}

// TestShouldUseFilterDataCacheRejectsScopedContext verifies DAC-scoped
// requests never consume or populate the shared all-data cache.
func TestShouldUseFilterDataCacheRejectsScopedContext(t *testing.T) {
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("1 = 0")
	})
	if shouldUseFilterDataCache(ctx, "") {
		t.Fatal("expected scoped request to bypass filterdata cache")
	}
}
