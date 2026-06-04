package semanticcache

import (
	"net/http"
	"net/url"
	"testing"
)

// clearByCacheID hits DELETE /api/cache/clear/{cacheId}. Returns the HTTP
// status code so callers in §3.3-style cases can assert specific contracts.
func clearByCacheID(t *testing.T, lc logCtx, step int, cacheID string) int {
	t.Helper()
	status, body, _, err := doJSON(t, "DELETE", "/api/cache/clear/"+url.PathEscape(cacheID), nil, nil)
	if err != nil {
		t.Fatalf("clearByCacheID http error: %v", err)
	}
	logf(t, lc.at(step), "INFO", "clear_by_id", map[string]any{
		"cache_id": cacheID,
		"status":   status,
	})
	if status != http.StatusOK && status != http.StatusNotFound {
		t.Logf("clearByCacheID body: %s", truncate(string(body), 200))
	}
	return status
}

// clearByCacheKey hits DELETE /api/cache/clear-by-key/{cacheKey}.
func clearByCacheKey(t *testing.T, lc logCtx, step int, key string) int {
	t.Helper()
	status, body, _, err := doJSON(t, "DELETE", "/api/cache/clear-by-key/"+url.PathEscape(key), nil, nil)
	if err != nil {
		t.Fatalf("clearByCacheKey http error: %v", err)
	}
	logf(t, lc.at(step), "INFO", "clear_by_key", map[string]any{
		"cache_key": key,
		"status":    status,
	})
	if status != http.StatusOK && status != http.StatusNotFound {
		t.Logf("clearByCacheKey body: %s", truncate(string(body), 200))
	}
	return status
}
