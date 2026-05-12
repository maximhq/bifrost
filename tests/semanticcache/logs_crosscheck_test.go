package semanticcache

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"
)

// floatEpsilon is the tolerance for cache_debug float field comparison between
// the in-flight response stamp and the persisted log row. The two paths use
// different JSON encoders (encoding/json for the HTTP response, sonic for
// the log store at logstore/payload.go:509), and sonic's default precision
// produces small (~1e-5) differences for similarity/threshold values. Not
// semantic drift — just round-trip noise. 1e-4 is comfortably above the
// observed delta while still tight enough to catch any real divergence.
const floatEpsilon = 1e-4

// logEntry is the minimum slice of a Bifrost log row we need for cross-checking
// the persisted cache_debug against the in-flight response. The full Log row
// has dozens of fields — we only care about ID, Timestamp, and CacheDebug.
type logEntry struct {
	ID         string      `json:"id"`
	Timestamp  string      `json:"timestamp"`
	CacheDebug *cacheDebug `json:"cache_debug,omitempty"`
}

// findLogByCacheDebug polls /api/logs descending-by-timestamp looking for an
// entry whose cache_debug matches the response stamp's (cache_id, cache_hit)
// pair. Returns the matching log row or fatal-fails after the timeout.
//
// Why match BOTH fields: for a semantic hit, A's miss-and-store log row and
// B's hit-replay log row carry the SAME cache_id (B's stamped cache_id points
// to A's storage entry). Without the cache_hit discriminator the helper would
// return whichever row was persisted first (usually A's miss).
//
// Polling exists because Bifrost's logging pipeline is asynchronous — the HTTP
// response returns before the row is persisted.
func findLogByCacheDebug(t *testing.T, lc logCtx, step int, want *cacheDebug) *logEntry {
	t.Helper()
	if want == nil || want.CacheID == nil {
		t.Fatalf("findLogByCacheDebug: response cache_debug or cache_id is nil")
	}
	wantID := *want.CacheID
	deadline := time.Now().Add(5 * time.Second)
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		status, body, _, err := doJSON(t, "GET",
			"/api/logs?limit=50&sort_by=timestamp&order=desc", nil, nil)
		if err != nil {
			t.Fatalf("findLogByCacheDebug GET err: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("findLogByCacheDebug status=%d body=%s", status, truncate(string(body), 300))
		}
		var resp struct {
			Logs []logEntry `json:"logs"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("findLogByCacheDebug decode: %v\nbody=%s", err, truncate(string(body), 300))
		}
		for i := range resp.Logs {
			l := &resp.Logs[i]
			if l.CacheDebug == nil || l.CacheDebug.CacheID == nil {
				continue
			}
			if *l.CacheDebug.CacheID != wantID {
				continue
			}
			if l.CacheDebug.CacheHit != want.CacheHit {
				continue
			}
			logf(t, lc.at(step), "INFO", "log_found", map[string]any{
				"cache_id": wantID, "log_id": l.ID, "cache_hit": l.CacheDebug.CacheHit, "attempts": attempts,
			})
			return l
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("log entry with cache_id=%s cache_hit=%v not found after %d attempts", wantID, want.CacheHit, attempts)
	return nil
}

// assertLogMatchesResponseCacheDebug verifies every field of the persisted
// log's cache_debug matches the in-flight response's cache_debug. Catches
// drift between PostLLMHook stamping and the durable log write — same data
// path the UI Logs view reads, so this guards a real production contract.
func assertLogMatchesResponseCacheDebug(t *testing.T, lc logCtx, step int, respCD, logCD *cacheDebug) {
	t.Helper()
	if respCD == nil {
		t.Fatalf("response cache_debug is nil; nothing to cross-check")
	}
	if logCD == nil {
		t.Fatalf("log row has no cache_debug; expected matching stamp")
	}
	mismatches := []string{}
	if logCD.CacheHit != respCD.CacheHit {
		mismatches = append(mismatches, fmt.Sprintf("cache_hit: resp=%v log=%v", respCD.CacheHit, logCD.CacheHit))
	}
	if deref(logCD.CacheID) != deref(respCD.CacheID) {
		mismatches = append(mismatches, fmt.Sprintf("cache_id: resp=%q log=%q", deref(respCD.CacheID), deref(logCD.CacheID)))
	}
	if deref(logCD.HitType) != deref(respCD.HitType) {
		mismatches = append(mismatches, fmt.Sprintf("hit_type: resp=%q log=%q", deref(respCD.HitType), deref(logCD.HitType)))
	}
	if deref(logCD.RequestedProvider) != deref(respCD.RequestedProvider) {
		mismatches = append(mismatches, fmt.Sprintf("requested_provider: resp=%q log=%q", deref(respCD.RequestedProvider), deref(logCD.RequestedProvider)))
	}
	if deref(logCD.RequestedModel) != deref(respCD.RequestedModel) {
		mismatches = append(mismatches, fmt.Sprintf("requested_model: resp=%q log=%q", deref(respCD.RequestedModel), deref(logCD.RequestedModel)))
	}
	if deref(logCD.ProviderUsed) != deref(respCD.ProviderUsed) {
		mismatches = append(mismatches, fmt.Sprintf("provider_used: resp=%q log=%q", deref(respCD.ProviderUsed), deref(logCD.ProviderUsed)))
	}
	if deref(logCD.ModelUsed) != deref(respCD.ModelUsed) {
		mismatches = append(mismatches, fmt.Sprintf("model_used: resp=%q log=%q", deref(respCD.ModelUsed), deref(logCD.ModelUsed)))
	}
	// Numeric float fields aren't expected to differ but float64 round-trip
	// through sonic JSON is exact for these magnitudes; equality check is fine.
	if !floatPtrEq(logCD.Threshold, respCD.Threshold) {
		mismatches = append(mismatches, fmt.Sprintf("threshold: resp=%v log=%v", respPtrStr(respCD.Threshold), respPtrStr(logCD.Threshold)))
	}
	if !floatPtrEq(logCD.Similarity, respCD.Similarity) {
		mismatches = append(mismatches, fmt.Sprintf("similarity: resp=%v log=%v", respPtrStr(respCD.Similarity), respPtrStr(logCD.Similarity)))
	}
	if !intPtrEq(logCD.InputTokens, respCD.InputTokens) {
		mismatches = append(mismatches, fmt.Sprintf("input_tokens: resp=%v log=%v", intPtrStr(respCD.InputTokens), intPtrStr(logCD.InputTokens)))
	}
	// cache_hit_latency is not cross-checked: the log row may be persisted
	// after the response was sent, and the field can be the same OR slightly
	// different depending on where in PostLLMHook the stamp lands.

	if len(mismatches) > 0 {
		t.Fatalf("cache_debug response/log mismatch:\n  - %s", joinLines(mismatches))
	}
	logf(t, lc.at(step), "PASS", "log_matches_response_cache_debug", map[string]any{
		"cache_id": deref(respCD.CacheID),
		"hit_type": deref(respCD.HitType),
		"fields_compared": []string{
			"cache_hit", "cache_id", "hit_type",
			"requested_provider", "requested_model",
			"provider_used", "model_used", "input_tokens",
			"threshold", "similarity",
		},
	})
}

func floatPtrEq(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) < floatEpsilon
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func respPtrStr(p *float64) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%.6f", *p)
}

func intPtrStr(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *p)
}

func joinLines(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += "\n  - "
		}
		out += v
	}
	return out
}
