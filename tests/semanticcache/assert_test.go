package semanticcache

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// cacheWriteSettle is the gap we wait between a miss-and-store request and a
// subsequent expected-hit, since PostLLMHook writes to the vector store in a
// goroutine (main.go:553-569) — the HTTP response returns before the write
// commits. 500ms covers typical Weaviate write latency including first-write
// cold start. Override via SC_WRITE_SETTLE_MS for environments with slower stores.
var cacheWriteSettle = func() time.Duration {
	if v := os.Getenv("SC_WRITE_SETTLE_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 500 * time.Millisecond
}()

// waitForCacheWrite pauses long enough for the plugin's async PostLLMHook
// store write to commit before a follow-up read. Logged so timing is visible
// in run.log.
func waitForCacheWrite(t *testing.T, lc logCtx, step int) {
	t.Helper()
	logf(t, lc.at(step), "INFO", "wait_for_cache_write", map[string]any{
		"settle_ms": cacheWriteSettle.Milliseconds(),
	})
	time.Sleep(cacheWriteSettle)
}

// cacheDebugged is implemented by any HTTP response type that carries
// `extra_fields.cache_debug`. Lets the assertion helpers work across chat,
// text-completion, responses, embedding, image-gen, etc. without per-type
// duplication.
type cacheDebugged interface {
	cacheDebug() *cacheDebug
}

// assertMiss verifies the response is a cache miss with a non-empty cache_id stamped.
// cache_debug must be present (plugin ran), CacheHit must be false, cache_id must be set.
func assertMiss(t *testing.T, lc logCtx, step int, resp cacheDebugged) string {
	t.Helper()
	cd := resp.cacheDebug()
	if cd == nil {
		logf(t, lc.at(step), "FAIL", "assert_miss", map[string]any{"reason": "cache_debug absent"})
		t.Fatalf("expected miss with cache_debug stamped; cache_debug is nil")
	}
	if cd.CacheHit {
		logf(t, lc.at(step), "FAIL", "assert_miss", map[string]any{"cache_hit": true})
		t.Fatalf("expected miss, got cache_hit=true cache_id=%s", deref(cd.CacheID))
	}
	if cd.CacheID == nil || *cd.CacheID == "" {
		logf(t, lc.at(step), "FAIL", "assert_miss", map[string]any{"reason": "cache_id empty on miss"})
		t.Fatalf("expected cache_id stamped on miss; got nil/empty")
	}
	logf(t, lc.at(step), "PASS", "assert_miss", map[string]any{"cache_id": *cd.CacheID})
	return *cd.CacheID
}

// assertHit verifies the response is a cache hit with the expected hit_type.
// Returns the cache_id for further chaining (e.g. same_cache_id checks).
func assertHit(t *testing.T, lc logCtx, step int, resp cacheDebugged, wantType string) string {
	t.Helper()
	cd := resp.cacheDebug()
	if cd == nil {
		logf(t, lc.at(step), "FAIL", "assert_hit", map[string]any{"reason": "cache_debug absent"})
		t.Fatalf("expected hit with cache_debug stamped; cache_debug is nil")
	}
	if !cd.CacheHit {
		logf(t, lc.at(step), "FAIL", "assert_hit", map[string]any{"cache_hit": false})
		t.Fatalf("expected hit, got cache_hit=false cache_id=%s", deref(cd.CacheID))
	}
	if wantType != "" {
		if cd.HitType == nil || *cd.HitType != wantType {
			logf(t, lc.at(step), "FAIL", "assert_hit_type", map[string]any{
				"want": wantType, "got": deref(cd.HitType),
			})
			t.Fatalf("expected hit_type=%q, got %q", wantType, deref(cd.HitType))
		}
	}
	if cd.CacheID == nil || *cd.CacheID == "" {
		t.Fatalf("expected cache_id stamped on hit; got nil/empty")
	}
	if cd.CacheHitLatency == nil {
		t.Logf("warning: cache_hit_latency not stamped on hit")
	}
	logf(t, lc.at(step), "PASS", "assert_hit", map[string]any{
		"cache_id": *cd.CacheID,
		"hit_type": deref(cd.HitType),
		"latency":  derefInt64(cd.CacheHitLatency),
	})
	return *cd.CacheID
}

// assertNoCacheDebug verifies the plugin did NOT run (no cache_debug stamped).
// Used for plugin-disabled and skipped-caching cases.
func assertNoCacheDebug(t *testing.T, lc logCtx, step int, resp cacheDebugged) {
	t.Helper()
	cd := resp.cacheDebug()
	if cd != nil {
		logf(t, lc.at(step), "FAIL", "assert_no_cache_debug", map[string]any{
			"cache_hit": cd.CacheHit,
			"cache_id":  deref(cd.CacheID),
		})
		t.Fatalf("expected no cache_debug, got cache_hit=%v cache_id=%s", cd.CacheHit, deref(cd.CacheID))
	}
	logf(t, lc.at(step), "PASS", "assert_no_cache_debug", nil)
}

func assertSameCacheID(t *testing.T, lc logCtx, step int, got, want string) {
	t.Helper()
	if got != want {
		logf(t, lc.at(step), "FAIL", "assert_same_cache_id", map[string]any{"want": want, "got": got})
		t.Fatalf("expected same cache_id %q, got %q", want, got)
	}
	logf(t, lc.at(step), "PASS", "assert_same_cache_id", map[string]any{"cache_id": got})
}

func assertDifferentCacheID(t *testing.T, lc logCtx, step int, a, b string) {
	t.Helper()
	if a == b {
		logf(t, lc.at(step), "FAIL", "assert_diff_cache_id", map[string]any{"cache_id": a})
		t.Fatalf("expected different cache_ids, both = %q", a)
	}
	logf(t, lc.at(step), "PASS", "assert_diff_cache_id", map[string]any{"a": a, "b": b})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
