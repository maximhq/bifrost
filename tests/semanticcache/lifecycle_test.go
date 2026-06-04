package semanticcache

import (
	"net/http"
	"testing"
)

const (
	ttlLifecycle        = "30s"
	defaultKeyLifecycle = "phase3-default"
)

// TestLifecycle exercises plugin disable / enable / delete lifecycle.
//
// Unlike TestDirect / TestSemantic, every subtest runs SERIALLY by design —
// each case mutates plugin lifecycle state (enabled flag, existence) which
// is fundamentally global and not parallelizable. No `t.Parallel()` calls
// in this file.
//
// Test flow (linear timeline):
//
//	Setup → seed entry under direct-only plugin
//	3.1   → PUT {enabled:false}
//	3.2   → request after disable, no cache_debug stamped
//	3.3   → DELETE /api/cache/clear/{id}     → expect 400
//	3.4   → DELETE /api/cache/clear-by-key/{k} → expect 400
//	3.5   → PUT {enabled:true}
//	3.6   → seed entry STILL hits (disable preserves namespace data)
//	3.7   → DELETE /api/plugins/semantic_cache
//	3.8   → request after delete, no cache_debug
//	3.9   → POST /api/plugins to recreate (same namespace)
//	3.10  → seed entry STILL hits (delete+recreate preserves namespace data —
//	        contract from commit a7c611e2e removing CleanUpOnShutdown)
func TestLifecycle(t *testing.T) {
	lc := newLogCtx("lifecycle", "setup")
	logf(t, lc.at(0), "SETUP", "phase_start", map[string]any{
		"mode": "direct-only",
		"ttl":  ttlLifecycle,
	})

	// Clean state — Phase 2 may have left a plugin in semantic mode; tear it
	// down so we can create from scratch in direct-only.
	if _, exists := pluginGet(t, lc, 1); exists {
		pluginDelete(t, lc, 2)
	}

	// Create plugin in direct-only mode.
	created := pluginCreate(t, lc, 3, true, directOnlyConfig(ttlLifecycle, defaultKeyLifecycle))
	if !created.Enabled || created.Status.Status != "active" {
		t.Fatalf("setup: expected enabled+active, got enabled=%v status=%q",
			created.Enabled, created.Status.Status)
	}

	// Populate the seed entry. We'll reference seedCacheID and seedReq across
	// disable / re-enable / delete / recreate to assert namespace persistence.
	seedKey := "phase3-seed"
	seedReq := simpleChat(cfg.OpenAIModel, "Name the largest planet in our solar system.")
	respA := postChat(t, lc, 4, seedReq, cacheHeaders{Key: seedKey})
	seedCacheID := assertMiss(t, lc, 5, respA)
	waitForCacheWrite(t, lc, 6)
	// Confirm the seed entry is queryable before we start disrupting state.
	_ = assertHit(t, lc, 8, postChat(t, lc, 7, seedReq, cacheHeaders{Key: seedKey}), "direct")
	logf(t, lc.at(9), "SETUP", "seed_entry_ready", map[string]any{"cache_id": seedCacheID})

	allKeys := []string{seedKey, "phase3-k2", "phase3-k8"}
	teardownLc := newLogCtx("lifecycle", "teardown")
	t.Cleanup(func() {
		// Best-effort: clear keys if the plugin is loaded at teardown time.
		// If a case left it disabled/deleted, the 400 is informational.
		for _, k := range allKeys {
			_ = clearByCacheKey(t, teardownLc.at(99), 99, k)
		}
	})

	// 3.1 disable_via_update — PUT {enabled:false, config:<current>}.
	// Per UI wire parity (PLAN §3.5), we re-send the current config along
	// with enabled=false — never PUT bare {enabled:false} which would wipe
	// the saved config blob.
	t.Run("3.1_disable_via_update", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.1_disable_via_update")
		updated := pluginUpdate(t, lc, 1, false, directOnlyConfig(ttlLifecycle, defaultKeyLifecycle))
		if updated.Enabled {
			t.Fatalf("expected enabled=false in update response, got true")
		}
		// Confirm via GET that the disabled state is reflected.
		p, exists := pluginGet(t, lc, 2)
		if !exists {
			t.Fatalf("plugin row should persist after disable (only memory unloaded)")
		}
		if p.Enabled {
			t.Fatalf("GET expected enabled=false, got true")
		}
		if p.Status.Status != "disabled" {
			t.Fatalf("expected status=disabled, got %q", p.Status.Status)
		}
	})

	// 3.2 request_after_disable_no_cache_debug — plugin removed from
	// in-memory pipeline; PreLLMHook never runs; no cache_debug stamped.
	t.Run("3.2_request_after_disable_no_cache_debug", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.2_request_after_disable_no_cache_debug")
		resp := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, "What's 2+2?"), cacheHeaders{Key: "phase3-k2"})
		assertNoCacheDebug(t, lc, 2, resp)
	})

	// 3.3 clear_endpoints_when_plugin_disabled — the cache-clear handler must
	// return HTTP 400 with "plugin is not loaded" when the resolver returns
	// nil. Pre-fix this returned 405; bug surfaced + fixed earlier this run.
	t.Run("3.3_clear_endpoints_when_plugin_disabled", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.3_clear_endpoints_when_plugin_disabled")
		status := clearByCacheID(t, lc, 1, "00000000-0000-0000-0000-000000000000")
		if status != http.StatusBadRequest {
			t.Fatalf("expected 400 (plugin not loaded), got %d", status)
		}
	})

	// 3.4 clear_by_key_endpoints_when_disabled — same contract for clear-by-key.
	t.Run("3.4_clear_by_key_endpoints_when_disabled", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.4_clear_by_key_endpoints_when_disabled")
		status := clearByCacheKey(t, lc, 1, "phase3-disabled-test")
		if status != http.StatusBadRequest {
			t.Fatalf("expected 400 (plugin not loaded), got %d", status)
		}
	})

	// 3.5 re_enable_via_update — flip back to enabled; status flips to active.
	t.Run("3.5_re_enable_via_update", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.5_re_enable_via_update")
		updated := pluginUpdate(t, lc, 1, true, directOnlyConfig(ttlLifecycle, defaultKeyLifecycle))
		if !updated.Enabled {
			t.Fatalf("expected enabled=true after re-enable, got false")
		}
		if updated.Status.Status != "active" {
			t.Fatalf("expected status=active after re-enable, got %q", updated.Status.Status)
		}
	})

	// 3.6 replay_previous_entries_after_reenable — entries written before
	// disable must still be queryable. Namespace data is independent of
	// plugin in-memory lifecycle.
	t.Run("3.6_replay_previous_entries_after_reenable", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.6_replay_previous_entries_after_reenable")
		resp := postChat(t, lc, 1, seedReq, cacheHeaders{Key: seedKey})
		gotID := assertHit(t, lc, 2, resp, "direct")
		assertSameCacheID(t, lc, 3, gotID, seedCacheID)
	})

	// 3.7 delete_plugin — DELETE removes both DB row and in-memory plugin.
	t.Run("3.7_delete_plugin", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.7_delete_plugin")
		pluginDelete(t, lc, 1)
		if _, exists := pluginGet(t, lc, 2); exists {
			t.Fatalf("plugin should be 404 after delete")
		}
	})

	// 3.8 request_after_delete — no plugin instance, no cache_debug.
	t.Run("3.8_request_after_delete", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.8_request_after_delete")
		resp := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, "What's 3+3?"), cacheHeaders{Key: "phase3-k8"})
		assertNoCacheDebug(t, lc, 2, resp)
	})

	// 3.9 re_create_clean — POST with the SAME config (and therefore the
	// same namespace). Recreate must succeed and surface status=active.
	t.Run("3.9_re_create_clean", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.9_re_create_clean")
		created := pluginCreate(t, lc, 1, true, directOnlyConfig(ttlLifecycle, defaultKeyLifecycle))
		if !created.Enabled || created.Status.Status != "active" {
			t.Fatalf("recreate: expected enabled+active, got enabled=%v status=%q",
				created.Enabled, created.Status.Status)
		}
	})

	// 3.10 namespace_persists_across_delete_recreate — the contract that
	// commit a7c611e2e (removing CleanUpOnShutdown) enabled: entries written
	// under a namespace must survive plugin delete + recreate. Without this,
	// any production restart of Bifrost would wipe the cache.
	t.Run("3.10_namespace_persists_across_delete_recreate", func(t *testing.T) {
		lc := newLogCtx("lifecycle", "3.10_namespace_persists_across_delete_recreate")
		resp := postChat(t, lc, 1, seedReq, cacheHeaders{Key: seedKey})
		gotID := assertHit(t, lc, 2, resp, "direct")
		assertSameCacheID(t, lc, 3, gotID, seedCacheID)
	})

	logf(t, teardownLc.at(99), "TEARDOWN", "phase_end", nil)
}
