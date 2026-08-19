package semanticcache

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Test image URLs copied from core/internal/llmtests/utils.go so the e2e
// suite uses the same fixtures the rest of the test-suite has validated
// providers against.
const (
	testImageURL1 = "https://pestworldcdn-dcf2a8gbggazaghf.z01.azurefd.net/media/561791/carpenter-ant4.jpg"
	testImageURL2 = "https://images.pexels.com/photos/30662605/pexels-photo-30662605/free-photo-of-eiffel-tower-view-from-the-seine-river-in-paris.jpeg"
)

// TestDirect runs the direct-only caching cases from the plan (1.1–1.55).
//
// Parallelism rules — IMPORTANT for anyone adding new cases:
//
//   - Cases that ONLY exercise per-request behavior (different cache keys,
//     headers, params, attachments) call `t.Parallel()` at the top of the
//     subtest body. Cache keys are unique per case so they never collide.
//
//   - Cases that mutate the plugin's CONFIG via `pluginUpdate` (e.g. flipping
//     cache_by_model, exclude_system_prompt, default_cache_key) must NOT
//     call `t.Parallel()`. They run synchronously inside the parent loop,
//     one at a time. Each such case restores the baseline config via
//     `t.Cleanup` before returning.
//
// Go's test framework guarantees the order: every `t.Parallel()` subtest
// PAUSES until the parent function reaches its end, then all paused
// subtests unblock and run concurrently. So all 4 mutating cases (1.4,
// 1.6, 1.8, 1.10) execute serially first; the remaining parallel cases
// then fire off together against the restored baseline plugin.
//
// Adding a new mutating case → omit `t.Parallel()` + add a `// Serial:`
// comment so the next person sees the intent.
func TestDirect(t *testing.T) {
	lc := newLogCtx("direct", "setup")
	logf(t, lc.at(0), "SETUP", "phase_start", map[string]any{
		"mode": "direct-only",
		"ttl":  ttlDirect,
	})

	// Setup: create the plugin in direct-only mode with a default cache key
	// scoped to phase1, so case 1.3 can test the default-key path. Cases that
	// mutate config PUT during the case and restore baseline via t.Cleanup.
	created := pluginCreate(t, lc, 1, true, directOnlyConfig(ttlDirect, defaultKeyDirect))
	if created.Status.Status != "active" && created.Status.Status != "ready" && created.Status.Status != "Ready" && created.Status.Status != "Initialized" {
		t.Logf("note: plugin status=%q (continuing — status field naming may vary)", created.Status.Status)
	}

	// Cleanup at end of phase — clear every key used. Plugin stays loaded so
	// later phases can PUT-update it.
	allKeys := []string{
		defaultKeyDirect,
		"phase1-k1-a", "phase1-k1-b", "phase1-k2", "phase1-ttl",
		"phase1-k5", "phase1-k6", "phase1-k7", "phase1-k8",
		"phase1-k9", "phase1-k10", "phase1-k11", "phase1-k12",
		"phase1-k14", "phase1-k15", "phase1-k16", "phase1-k17",
		"phase1-k18", "phase1-k19", "phase1-k41",
		"phase1-k45", "phase1-k46", "phase1-k54",
		"phase1-k32", "phase1-k33", "phase1-k34", "phase1-k35", "phase1-k36",
		"phase1-k48", "phase1-k49", "phase1-k50", "phase1-k51", "phase1-k52",
		"phase1-k26", "phase1-k27", "phase1-k28", "phase1-k29",
		"phase1-k30", "phase1-k31", "phase1-k42", "phase1-k43",
		"phase1-k20", "phase1-k21", "phase1-k22", "phase1-k23",
		"phase1-k53", "phase1-k53-seed",
		"phase1-k24", "phase1-k25", "phase1-k47",
		"phase1-k38", "phase1-k39", "phase1-k37",
		"phase1-k55",
	}
	t.Cleanup(func() {
		for _, k := range allKeys {
			_ = clearByCacheKey(t, lc.at(99), 99, k)
		}
	})

	// 1.1 exact_match_chat
	t.Run("1.1_exact_match_chat", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.1_exact_match_chat")
		req := simpleChat(cfg.OpenAIModel, "What is the capital of France?")

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: "phase1-k1-a"})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: "phase1-k1-a"})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.2 key_isolation
	t.Run("1.2_key_isolation", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.2_key_isolation")
		req := simpleChat(cfg.OpenAIModel, "Recommend a science fiction book to read.")

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: "phase1-k1-b"})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, req, cacheHeaders{Key: "phase1-k2"})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.3 default_cache_key — no header, default key on plugin applies.
	t.Run("1.3_default_cache_key", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.3_default_cache_key")
		req := simpleChat(cfg.OpenAIModel, "Give me one fun fact about octopuses.")

		respA := postChat(t, lc, 1, req, cacheHeaders{})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.4 no_key_no_default — when DefaultCacheKey="" and no x-bf-cache-key,
	// the plugin's PreLLMHook bails before any cache work (`resolveCacheKey` returns false).
	// PostLLMHook also bails because state was never created. So no cache_debug is stamped.
	t.Run("1.4_no_key_no_default", func(t *testing.T) {
		// Serial: this case mutates plugin config (default_cache_key="").
		lc := newLogCtx("direct", "1.4_no_key_no_default")

		// Flip default_cache_key off.
		pluginUpdate(t, lc, 1, true, directOnlyConfig(ttlDirect, ""))
		t.Cleanup(func() { restoreDirectBaseline(t, lc, 99) })

		req := simpleChat(cfg.OpenAIModel, "Tell me a one-line joke about teapots.")
		respA := postChat(t, lc, 2, req, cacheHeaders{})
		assertNoCacheDebug(t, lc, 3, respA)

		respB := postChat(t, lc, 4, req, cacheHeaders{})
		assertNoCacheDebug(t, lc, 5, respB)
	})

	// 1.5 cache_by_model_default_true — model in cache key by default, so two
	// requests with same body but different models → distinct cache_ids.
	t.Run("1.5_cache_by_model_default_true", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.5_cache_by_model_default_true")
		key := "phase1-k5"
		body := "What is the speed of light in vacuum?"

		respA := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, simpleChat(cfg.OpenAIModelAlt, body), cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.6 cache_by_model_false — flip the flag, same body across two models
	// should produce the same cache_id; B hits the entry stored by A.
	t.Run("1.6_cache_by_model_false", func(t *testing.T) {
		// Serial: this case mutates plugin config (cache_by_model=false).
		lc := newLogCtx("direct", "1.6_cache_by_model_false")

		cfgBlob := directOnlyConfig(ttlDirect, defaultKeyDirect)
		cfgBlob["cache_by_model"] = false
		pluginUpdate(t, lc, 1, true, cfgBlob)
		t.Cleanup(func() { restoreDirectBaseline(t, lc, 99) })

		key := "phase1-k6"
		body := "Recommend one short walk-friendly podcast."

		respA := postChat(t, lc, 2, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 3, respA)
		waitForCacheWrite(t, lc, 4)

		respB := postChat(t, lc, 5, simpleChat(cfg.OpenAIModelAlt, body), cacheHeaders{Key: key})
		idB := assertHit(t, lc, 6, respB, "direct")
		assertSameCacheID(t, lc, 7, idB, idA)
	})

	// 1.7 cache_by_provider_default_true — provider in cache key by default.
	t.Run("1.7_cache_by_provider_default_true", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.7_cache_by_provider_default_true")
		if os.Getenv("SC_CHAT_MODEL_ANTHROPIC") == "" {
			t.Skip("anthropic model not configured (SC_CHAT_MODEL_ANTHROPIC unset)")
		}
		key := "phase1-k7"
		body := "Give one tip for staying focused while reading."

		respA := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, simpleChat(cfg.AnthroModel, body), cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.8 cache_by_provider_false — with both cache_by_provider and
	// cache_by_model off, providers can share cache entries.
	t.Run("1.8_cache_by_provider_false", func(t *testing.T) {
		// Serial: this case mutates plugin config (cache_by_* = false).
		lc := newLogCtx("direct", "1.8_cache_by_provider_false")
		if os.Getenv("SC_CHAT_MODEL_ANTHROPIC") == "" {
			t.Skip("anthropic model not configured (SC_CHAT_MODEL_ANTHROPIC unset)")
		}

		cfgBlob := directOnlyConfig(ttlDirect, defaultKeyDirect)
		cfgBlob["cache_by_provider"] = false
		cfgBlob["cache_by_model"] = false
		pluginUpdate(t, lc, 1, true, cfgBlob)
		t.Cleanup(func() { restoreDirectBaseline(t, lc, 99) })

		key := "phase1-k8"
		body := "Say hi in three words."

		respA := postChat(t, lc, 2, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 3, respA)
		waitForCacheWrite(t, lc, 4)

		respB := postChat(t, lc, 5, simpleChat(cfg.AnthroModel, body), cacheHeaders{Key: key})
		idB := assertHit(t, lc, 6, respB, "direct")
		assertSameCacheID(t, lc, 7, idB, idA)
	})

	// 1.9 exclude_system_prompt_false — system message is part of the hash
	// by default; different systems → different cache_ids.
	t.Run("1.9_exclude_system_prompt_false", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.9_exclude_system_prompt_false")
		key := "phase1-k9"
		user := "What's 2+2?"

		respA := postChat(t, lc, 1, chatWithSystem(cfg.OpenAIModel, "You are a math tutor.", user), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, chatWithSystem(cfg.OpenAIModel, "You are a pirate.", user), cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.10 exclude_system_prompt_true — flag flips system message out of the
	// hash; identical user message hits regardless of system.
	t.Run("1.10_exclude_system_prompt_true", func(t *testing.T) {
		// Serial: this case mutates plugin config (exclude_system_prompt=true).
		lc := newLogCtx("direct", "1.10_exclude_system_prompt_true")

		cfgBlob := directOnlyConfig(ttlDirect, defaultKeyDirect)
		cfgBlob["exclude_system_prompt"] = true
		pluginUpdate(t, lc, 1, true, cfgBlob)
		t.Cleanup(func() { restoreDirectBaseline(t, lc, 99) })

		key := "phase1-k10"
		user := "What's the powerhouse of the cell?"

		respA := postChat(t, lc, 2, chatWithSystem(cfg.OpenAIModel, "You are a biology teacher.", user), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 3, respA)
		waitForCacheWrite(t, lc, 4)

		respB := postChat(t, lc, 5, chatWithSystem(cfg.OpenAIModel, "You are Sherlock Holmes.", user), cacheHeaders{Key: key})
		idB := assertHit(t, lc, 6, respB, "direct")
		assertSameCacheID(t, lc, 7, idB, idA)
	})

	// 1.11 conversation_threshold_skips — len(messages) > threshold (default 3)
	// → plugin bails before any cache work. No cache_debug on either response.
	t.Run("1.11_conversation_threshold_skips", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.11_conversation_threshold_skips")
		key := "phase1-k11"

		msgs := []chatMessage{
			{Role: "user", Content: textContent("Hi.")},
			{Role: "assistant", Content: textContent("Hello! How can I help?")},
			{Role: "user", Content: textContent("What's the weather like in Paris?")},
			{Role: "user", Content: textContent("Actually, give me one travel tip for Paris.")},
		}
		req := chatRequest{Model: cfg.OpenAIModel, Messages: msgs}

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key})
		assertNoCacheDebug(t, lc, 2, respA)

		respB := postChat(t, lc, 3, req, cacheHeaders{Key: key})
		assertNoCacheDebug(t, lc, 4, respB)
	})

	// 1.12 conversation_threshold_boundary — len(messages) == threshold (3)
	// is still cached (code uses `>`, not `>=`). Boundary case.
	t.Run("1.12_conversation_threshold_boundary", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.12_conversation_threshold_boundary")
		key := "phase1-k12"

		msgs := []chatMessage{
			{Role: "user", Content: textContent("Hi.")},
			{Role: "assistant", Content: textContent("Hello!")},
			{Role: "user", Content: textContent("Recommend one calming tea.")},
		}
		req := chatRequest{Model: cfg.OpenAIModel, Messages: msgs}

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.13 ttl_expiry_default
	t.Run("1.13_ttl_expiry_default", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.13_ttl_expiry_default")
		req := simpleChat(cfg.OpenAIModel, "Name a primary color.")
		key := "phase1-ttl"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		// Confirm a fresh read hits within TTL.
		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "direct")

		// Sleep past TTL + 2s safety margin.
		wait := ttlDirectDuration + 2*time.Second
		logf(t, lc.at(6), "INFO", "sleep_for_ttl", map[string]any{"seconds": wait.Seconds()})
		time.Sleep(wait)

		respC := postChat(t, lc, 7, req, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 8, respC)
	})

	// 1.14 ttl_per_request_override — x-bf-cache-ttl=3s overrides plugin default (10s).
	// B hit within 3s, C miss after sleeping past 3s + safety.
	t.Run("1.14_ttl_per_request_override", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.14_ttl_per_request_override")
		req := simpleChat(cfg.OpenAIModel, "Name a noble gas.")
		key := "phase1-k14"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, TTL: "3s"})
		_ = assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, TTL: "3s"})
		_ = assertHit(t, lc, 5, respB, "direct")

		// Sleep past per-request TTL but well under plugin default (10s).
		wait := 4 * time.Second
		logf(t, lc.at(6), "INFO", "sleep_past_per_request_ttl", map[string]any{"seconds": wait.Seconds()})
		time.Sleep(wait)

		respC := postChat(t, lc, 7, req, cacheHeaders{Key: key, TTL: "3s"})
		_ = assertMiss(t, lc, 8, respC)
	})

	// 1.15 ttl_invalid_header_ignored — bogus x-bf-cache-ttl is silently ignored
	// (lib/ctx.go:381). Plugin default TTL applies; B still hits within default.
	t.Run("1.15_ttl_invalid_header_ignored", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.15_ttl_invalid_header_ignored")
		req := simpleChat(cfg.OpenAIModel, "What is a haiku?")
		key := "phase1-k15"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, TTL: "garbage"})
		_ = assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, TTL: "also-garbage"})
		_ = assertHit(t, lc, 5, respB, "direct")
	})

	// 1.16 no_store_header — both A and B send x-bf-cache-no-store=true; nothing
	// is ever written, so both miss. cache_debug IS stamped (plugin runs, but
	// PostLLMHook's shouldSkipCaching short-circuits the write).
	t.Run("1.16_no_store_header", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.16_no_store_header")
		req := simpleChat(cfg.OpenAIModel, "Define entropy in one sentence.")
		key := "phase1-k16"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, NoStore: "true"})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, NoStore: "true"})
		idB := assertMiss(t, lc, 5, respB)
		// Same body + key → same deterministic cache_id even though no entry exists.
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.17 no_store_with_hit — A writes normally; B sends no-store=true but the
	// header only blocks WRITES, not reads. B still hits the entry A stored.
	t.Run("1.17_no_store_with_hit", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.17_no_store_with_hit")
		req := simpleChat(cfg.OpenAIModel, "What's the boiling point of water in Celsius?")
		key := "phase1-k17"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, NoStore: "true"})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.18 cache_type_direct_header — explicit x-bf-cache-type=direct in direct-only
	// mode is a no-op narrow (direct is already the only path). B still hits.
	t.Run("1.18_cache_type_direct_header", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.18_cache_type_direct_header")
		req := simpleChat(cfg.OpenAIModel, "Name the Roman god of war.")
		key := "phase1-k18"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, Type: "direct"})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, Type: "direct"})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.19 cache_type_semantic_in_direct_only — STRICT assertion of PLAN §12 bug #2.
	// In direct-only mode with x-bf-cache-type=semantic, the plugin has no
	// embedding executor → no semantic search can run. Direct search is also
	// suppressed by the header. The canDoSemanticSearch early-exit guard in
	// PreLLMHook (plugins/semanticcache/main.go) returns before any cache
	// activity, so no cache_debug is stamped and no orphan entry is written.
	// If either appears, the guard has regressed.
	t.Run("1.19_cache_type_semantic_in_direct_only", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.19_cache_type_semantic_in_direct_only")
		req := simpleChat(cfg.OpenAIModel, "Tell me one famous quote about courage.")
		key := "phase1-k19"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, Type: "semantic"})
		assertNoCacheDebug(t, lc, 2, respA)

		respB := postChat(t, lc, 3, req, cacheHeaders{Key: key, Type: "semantic"})
		assertNoCacheDebug(t, lc, 4, respB)
	})

	// 1.41 threshold_header_ignored_direct_only — x-bf-cache-threshold has no
	// effect on direct lookups (it's only consulted in performSemanticSearch).
	// B with threshold=0.0 still finds A's deterministic entry.
	t.Run("1.41_threshold_header_ignored_direct_only", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.41_threshold_header_ignored_direct_only")
		req := simpleChat(cfg.OpenAIModel, "Name a famous bridge.")
		key := "phase1-k41"
		zero := 0.0

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, Threshold: &zero})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.45 no_store_explicit_false — header value MUST be the literal "true" to
	// disable writes (ctx.go:406). Sending "false" does NOT block writes.
	t.Run("1.45_no_store_explicit_false", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.45_no_store_explicit_false")
		req := simpleChat(cfg.OpenAIModel, "What's a synonym for happy?")
		key := "phase1-k45"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, NoStore: "false"})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, NoStore: "false"})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.46 no_store_uppercase_true — header match is case-sensitive. "TRUE" does
	// not toggle the no-store flag; writes proceed normally.
	t.Run("1.46_no_store_uppercase_true", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.46_no_store_uppercase_true")
		req := simpleChat(cfg.OpenAIModel, "Name a famous painter.")
		key := "phase1-k46"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, NoStore: "TRUE"})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, NoStore: "TRUE"})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.37 params_top_logprobs — top_logprobs is a non-trivial chat parameter
	// that lands in the params metadata (utils.go:795). Distinct values must
	// produce distinct cache_ids. Stands in for the "extra_params" case in
	// the plan since extra_params is hard to wire on the OpenAI-compat
	// endpoint — same isolation contract, less plumbing.
	t.Run("1.37_params_top_logprobs", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.37_params_top_logprobs")
		key := "phase1-k37"
		body := "Name one mountain range."
		yes := true

		reqA := simpleChat(cfg.OpenAIModel, body)
		reqA.LogProbs = &yes
		t1 := 2
		reqA.TopLogProbs = &t1

		reqB := simpleChat(cfg.OpenAIModel, body)
		reqB.LogProbs = &yes
		t2 := 5
		reqB.TopLogProbs = &t2

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.38 clear_by_cache_id — populate an entry, delete it by id, verify the
	// same body now misses.
	t.Run("1.38_clear_by_cache_id", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.38_clear_by_cache_id")
		key := "phase1-k38"
		req := simpleChat(cfg.OpenAIModel, "Name one type of tree.")

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		// Confirm the entry is queryable before we delete it.
		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "direct")

		// Delete by id.
		if got := clearByCacheID(t, lc, 6, idA); got != http.StatusOK {
			t.Fatalf("expected 200 from clear-by-id, got %d", got)
		}

		// Subsequent identical request must miss again.
		respC := postChat(t, lc, 7, req, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 8, respC)
	})

	// 1.39 clear_by_key — populate two distinct bodies under the same cache
	// key, then bulk-delete by key; both should miss afterwards.
	t.Run("1.39_clear_by_key", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.39_clear_by_key")
		key := "phase1-k39"
		reqA := simpleChat(cfg.OpenAIModel, "Recommend one mystery novel.")
		reqB := simpleChat(cfg.OpenAIModel, "Recommend one biography.")

		respA1 := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA1)
		respB1 := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 4, respB1)
		waitForCacheWrite(t, lc, 5)

		// Both should now hit before we clear.
		_ = assertHit(t, lc, 7, postChat(t, lc, 6, reqA, cacheHeaders{Key: key}), "direct")
		_ = assertHit(t, lc, 9, postChat(t, lc, 8, reqB, cacheHeaders{Key: key}), "direct")

		// Bulk-clear the whole key.
		if got := clearByCacheKey(t, lc, 10, key); got != http.StatusOK {
			t.Fatalf("expected 200 from clear-by-key, got %d", got)
		}

		// Both should miss again.
		_ = assertMiss(t, lc, 12, postChat(t, lc, 11, reqA, cacheHeaders{Key: key}))
		_ = assertMiss(t, lc, 14, postChat(t, lc, 13, reqB, cacheHeaders{Key: key}))
	})

	// 1.40 clear_unknown_id — DELETE with a random uuid. Whether Bifrost returns
	// 200 (idempotent delete) or 404 (strict not-found), the contract is:
	// no 5xx and no crash. Documents the actual behavior in the log so PLAN
	// can pin it down later.
	t.Run("1.40_clear_unknown_id", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.40_clear_unknown_id")
		unknownID := "00000000-0000-0000-0000-000000000000"
		status := clearByCacheID(t, lc, 1, unknownID)
		if status >= 500 {
			t.Fatalf("clear unknown id returned %d (server error); expected idempotent 200 or 404", status)
		}
		// Accept either contract; surface which one in the log for PLAN docs.
		logf(t, lc.at(2), "PASS", "clear_unknown_id_documented", map[string]any{
			"status":   status,
			"contract": "idempotent" + (map[bool]string{true: "_or_404"}[status == http.StatusNotFound]),
		})
	})

	// 1.24 streaming_chat — SSE chat, A→B identical. B replays cached chunks;
	// final chunk on B has cache_hit=true with hit_type=direct, and chunk count
	// matches A's chunk count.
	t.Run("1.24_streaming_chat", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.24_streaming_chat")
		key := "phase1-k24"
		req := simpleChat(cfg.OpenAIModel, "Recite three colors of the rainbow, one per line.")

		respA := postChatStream(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		if len(respA.dataChunks()) < 2 {
			t.Fatalf("expected ≥2 data chunks on miss stream, got %d", len(respA.dataChunks()))
		}
		waitForCacheWrite(t, lc, 3)

		respB := postChatStream(t, lc, 4, req, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
		if got, want := len(respB.dataChunks()), len(respA.dataChunks()); got != want {
			t.Fatalf("expected B chunk count %d to match A's %d", got, want)
		}
	})

	// 1.25 streaming_replay_order — chunk-by-chunk content should be identical
	// between A (live stream) and B (cached replay). Plugin stores chunks as a
	// JSON array and replays them in order (search.go:351).
	t.Run("1.25_streaming_replay_order", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.25_streaming_replay_order")
		key := "phase1-k25"
		req := simpleChat(cfg.OpenAIModel, "Count from one to five.")

		respA := postChatStream(t, lc, 1, req, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChatStream(t, lc, 4, req, cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "direct")

		a := respA.dataChunks()
		b := respB.dataChunks()
		if len(a) != len(b) {
			t.Fatalf("chunk count mismatch: A=%d B=%d", len(a), len(b))
		}
		for i := range a {
			ta, tb := a[i].chunkText(), b[i].chunkText()
			if ta != tb {
				t.Fatalf("chunk %d text mismatch:\nA=%q\nB=%q", i, ta, tb)
			}
		}
		logf(t, lc.at(6), "PASS", "chunks_identical_in_order", map[string]any{"count": len(a)})
	})

	// 1.47 streaming_non_final_chunks_have_no_cache_debug — only the final
	// data chunk carries the cache_debug stamp (stampCacheDebugForMiss /
	// stampCacheDebugForHit skip non-final chunks). All earlier chunks must
	// have cache_debug absent on both A (miss) and B (hit).
	t.Run("1.47_streaming_non_final_chunks_no_cache_debug", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.47_streaming_non_final_chunks_no_cache_debug")
		key := "phase1-k47"
		req := simpleChat(cfg.OpenAIModel, "List two breakfast foods.")

		check := func(stage string, resp *streamResponse) {
			data := resp.dataChunks()
			if len(data) == 0 {
				t.Fatalf("[%s] no data chunks received", stage)
			}
			for i := 0; i < len(data)-1; i++ {
				if cd := data[i].cacheDebug(); cd != nil {
					t.Fatalf("[%s] non-final chunk %d had cache_debug stamped: %+v", stage, i, cd)
				}
			}
			finalCD := data[len(data)-1].cacheDebug()
			if finalCD == nil {
				t.Fatalf("[%s] final chunk missing cache_debug stamp", stage)
			}
		}

		respA := postChatStream(t, lc, 1, req, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA)
		check("miss", respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChatStream(t, lc, 4, req, cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "direct")
		check("hit", respB)
		logf(t, lc.at(6), "PASS", "non_final_chunks_clean", map[string]any{
			"a_count": len(respA.dataChunks()),
			"b_count": len(respB.dataChunks()),
		})
	})

	// 1.20 text_completion — /v1/completions with same prompt → hit. Plugin's
	// metadata extractor handles TextCompletionRequest specifically.
	t.Run("1.20_text_completion", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.20_text_completion")
		key := "phase1-k20"
		maxTok := 30
		req := textCompletionRequest{
			Model:     "openai/gpt-3.5-turbo-instruct",
			Prompt:    "The capital of Japan is",
			MaxTokens: &maxTok,
		}

		respA := postTextCompletion(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postTextCompletion(t, lc, 4, req, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.21 responses_api — /v1/responses with identical input → hit.
	t.Run("1.21_responses_api", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.21_responses_api")
		key := "phase1-k21"
		req := responsesRequest{
			Model: cfg.OpenAIModel,
			Input: "Name one type of cloud.",
		}

		respA := postResponses(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postResponses(t, lc, 4, req, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.22 embedding_endpoint — /v1/embeddings with identical input → hit.
	// Plugin's EmbeddingRequest path is direct-cache-only (semantic search is
	// suppressed for embedding requests — see PreLLMHook semanticEligible check).
	t.Run("1.22_embedding_endpoint", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.22_embedding_endpoint")
		key := "phase1-k22"
		req := embeddingRequest{
			Model: "openai/" + cfg.OpenAIEmbed,
			Input: "The quick brown fox jumps over the lazy dog.",
		}

		respA := postEmbedding(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postEmbedding(t, lc, 4, req, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.23 image_generation — /v1/images/generations with identical prompt → hit.
	// Note: this case is expensive ($0.04/image on dall-e-3). Skip by setting
	// SC_SKIP_IMAGE_GEN=1.
	t.Run("1.23_image_generation", func(t *testing.T) {
		t.Parallel()
		if os.Getenv("SC_SKIP_IMAGE_GEN") == "1" {
			t.Skip("SC_SKIP_IMAGE_GEN=1")
		}
		lc := newLogCtx("direct", "1.23_image_generation")
		key := "phase1-k23"
		n := 1
		req := imageGenRequest{
			Model:  "openai/dall-e-3",
			Prompt: "A minimalist line drawing of a red teapot on a white background.",
			N:      &n,
			Size:   "1024x1024",
		}

		respA := postImageGen(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postImageGen(t, lc, 4, req, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.53 responses_previous_response_id — different previous_response_id
	// values must produce distinct cache_ids (it's in params_hash via utils.go:834).
	// We use placeholder IDs since we only check params_hash isolation, not
	// the actual conversation chain.
	t.Run("1.53_responses_previous_response_id", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.53_responses_previous_response_id")
		key := "phase1-k53"

		// Need a real previous_response_id for the provider to accept the call.
		// Create one by first making a /v1/responses call and capturing its id.
		seed := postResponses(t, lc, 1, responsesRequest{
			Model: cfg.OpenAIModel,
			Input: "Say 'one'.",
		}, cacheHeaders{Key: "phase1-k53-seed"})

		var seedBody struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(seed.bodyRaw, &seedBody); err != nil || seedBody.ID == "" {
			t.Skipf("could not extract response id to seed previous_response_id: %v", err)
		}

		// Make a second seed call so we have two distinct previous_response_ids.
		seed2 := postResponses(t, lc, 2, responsesRequest{
			Model: cfg.OpenAIModel,
			Input: "Say 'two'.",
		}, cacheHeaders{Key: "phase1-k53-seed"})
		var seed2Body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(seed2.bodyRaw, &seed2Body); err != nil || seed2Body.ID == "" {
			t.Skipf("could not extract second response id: %v", err)
		}

		input := "Continue."
		reqA := responsesRequest{Model: cfg.OpenAIModel, Input: input, PreviousResponseID: &seedBody.ID}
		reqB := responsesRequest{Model: cfg.OpenAIModel, Input: input, PreviousResponseID: &seed2Body.ID}

		respA := postResponses(t, lc, 3, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 4, respA)

		respB := postResponses(t, lc, 5, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 6, respB)
		assertDifferentCacheID(t, lc, 7, idA, idB)
	})

	// 1.26 normalization_case — getNormalizedInputForCaching lowercases + trims
	// (utils.go:122). "Hello" and "hello " hash identically.
	t.Run("1.26_normalization_case", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.26_normalization_case")
		key := "phase1-k26"

		reqA := simpleChat(cfg.OpenAIModel, "Hello, who wrote 1984?")
		reqB := simpleChat(cfg.OpenAIModel, "hello, who wrote 1984? ")

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, reqB, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.27 normalization_whitespace — leading/trailing whitespace trimmed; inner
	// whitespace preserved verbatim.
	t.Run("1.27_normalization_whitespace", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.27_normalization_whitespace")
		key := "phase1-k27"

		reqA := simpleChat(cfg.OpenAIModel, "  Name one type of pasta.  ")
		reqB := simpleChat(cfg.OpenAIModel, "Name one type of pasta.")

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, reqB, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.28 unicode_prompt — non-ASCII + emoji round-trips through hash + cache.
	t.Run("1.28_unicode_prompt", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.28_unicode_prompt")
		key := "phase1-k28"
		body := "🚀 Quel est le sens de la vie? 寿司は美味しい。"

		respA := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.29 large_prompt — ~10KB prompt; the second call's wall-clock should be
	// dominated by cache_hit_latency (~ms), not provider latency (~s).
	t.Run("1.29_large_prompt", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.29_large_prompt")
		key := "phase1-k29"
		// Repeat a sentence to ~10KB.
		body := strings.Repeat("In a region far away, beneath the silver moon, a curious traveler set out at dawn carrying a worn leather satchel and a heart full of questions. ", 70)
		body += " Summarize the above in one sentence."

		respA := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
		// cache_hit_latency is stamped at hit time — assert it's at least set.
		// (Sanity check; provider latency would be much higher.)
		if cd := respB.cacheDebug(); cd == nil || cd.CacheHitLatency == nil {
			t.Fatalf("expected cache_hit_latency stamped on large_prompt hit")
		}
	})

	// 1.30 image_in_message — identical image_url block in both A and B → hit.
	// Verifies extractAttachmentsForCaching contributes consistently to the hash.
	t.Run("1.30_image_in_message", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.30_image_in_message")
		key := "phase1-k30"

		reqA := chatWithImage(cfg.OpenAIModel, "What is shown in this image?", testImageURL1)
		reqB := chatWithImage(cfg.OpenAIModel, "What is shown in this image?", testImageURL1)

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, reqB, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.31 image_attachment_diff — same text, different image URL → distinct cache_ids.
	t.Run("1.31_image_attachment_diff", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.31_image_attachment_diff")
		key := "phase1-k31"
		prompt := "What is shown in this image?"

		reqA := chatWithImage(cfg.OpenAIModel, prompt, testImageURL1)
		reqB := chatWithImage(cfg.OpenAIModel, prompt, testImageURL2)

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.42 nil_content_msg — a 3-message conversation including an assistant
	// tool-call message with nil content (followed by a tool response).
	// extractChatMessageContent handles nil content as empty string (utils.go:312)
	// so the hash is stable across runs.
	t.Run("1.42_nil_content_msg", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.42_nil_content_msg")
		key := "phase1-k42"

		mkReq := func() chatRequest {
			return chatRequest{
				Model: cfg.OpenAIModel,
				Messages: []chatMessage{
					{Role: "user", Content: textContent("What's the weather in NYC?")},
					{
						Role: "assistant",
						// Content intentionally omitted (nil) — assistant
						// tool-call messages set content=null per OpenAI spec.
						ToolCalls: []chatToolCall{{
							ID:   "call_abc",
							Type: "function",
							Function: chatToolCallFunc{
								Name:      "get_weather",
								Arguments: `{"city":"NYC"}`,
							},
						}},
					},
					{Role: "tool", ToolCallID: "call_abc", Content: textContent("Sunny, 72°F")},
				},
			}
		}

		respA := postChat(t, lc, 1, mkReq(), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, mkReq(), cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.43 empty_messages — sending messages:[] should be rejected by the
	// provider (or Bifrost validation) without crashing Bifrost. Accept any
	// non-2xx response; the contract is "no crash, no orphan cache entry."
	t.Run("1.43_empty_messages", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.43_empty_messages")
		key := "phase1-k43"

		req := chatRequest{Model: cfg.OpenAIModel, Messages: []chatMessage{}}
		hdr := http.Header{}
		(cacheHeaders{Key: key}).apply(&http.Request{Header: hdr})

		status, body, _, err := doJSON(t, "POST", "/v1/chat/completions", req, hdr)
		if err != nil {
			t.Fatalf("empty_messages http error: %v", err)
		}
		logf(t, lc.at(1), "INFO", "response", map[string]any{
			"status":   status,
			"body_len": len(body),
		})
		if status >= 200 && status < 300 {
			t.Fatalf("expected non-success status for empty messages, got %d body=%s",
				status, truncate(string(body), 200))
		}
		// Subsequent identical request should also fail — and crucially
		// shouldn't return a stale cache hit.
		status2, body2, _, _ := doJSON(t, "POST", "/v1/chat/completions", req, hdr)
		if status2 >= 200 && status2 < 300 {
			t.Fatalf("expected non-success status on retry, got %d body=%s",
				status2, truncate(string(body2), 200))
		}
		logf(t, lc.at(2), "PASS", "no_crash_on_empty_messages", map[string]any{
			"status_a": status, "status_b": status2,
		})
	})

	// 1.44 plugin_get_status — GET /api/plugins/semantic_cache after the phase
	// is warm. status should be "active" and config should round-trip what we PUT.
	t.Run("1.44_plugin_get_status", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.44_plugin_get_status")
		p, exists := pluginGet(t, lc, 1)
		if !exists {
			t.Fatalf("plugin %q should exist mid-phase", pluginName)
		}
		if !p.Enabled {
			t.Fatalf("expected plugin enabled=true, got %v", p.Enabled)
		}
		validStatuses := map[string]bool{"active": true, "ready": true, "Ready": true, "Initialized": true}
		if got := p.Status.Status; !validStatuses[got] {
			t.Fatalf("expected plugin status to be one of active/ready/Ready/Initialized, got %q", got)
		}
		// Config blob round-trip checks — backend may coerce numeric types
		// when re-serializing from the DB.
		gotDim, _ := p.Config["dimension"].(float64)
		if int(gotDim) != 1 {
			t.Fatalf("expected dimension=1 (direct-only), got %v", p.Config["dimension"])
		}
		if got, _ := p.Config["default_cache_key"].(string); got != defaultKeyDirect {
			t.Fatalf("expected default_cache_key=%q, got %q", defaultKeyDirect, got)
		}
		logf(t, lc.at(2), "PASS", "plugin_status_validated", map[string]any{
			"status":            p.Status.Status,
			"enabled":           p.Enabled,
			"dimension":         p.Config["dimension"],
			"default_cache_key": p.Config["default_cache_key"],
		})
	})

	// 1.32 params_temperature_isolation — temperature is part of params hash,
	// so the same body with different temperatures produces distinct cache_ids.
	t.Run("1.32_params_temperature_isolation", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.32_params_temperature_isolation")
		key := "phase1-k32"
		body := "Pick one number between 1 and 10."

		reqA := simpleChat(cfg.OpenAIModel, body)
		t1 := 0.3
		reqA.Temperature = &t1

		reqB := simpleChat(cfg.OpenAIModel, body)
		t2 := 0.7
		reqB.Temperature = &t2

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.33 params_top_p_isolation — top_p in params hash.
	t.Run("1.33_params_top_p_isolation", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.33_params_top_p_isolation")
		key := "phase1-k33"
		body := "Name a Greek philosopher."

		reqA := simpleChat(cfg.OpenAIModel, body)
		tp1 := 0.5
		reqA.TopP = &tp1

		reqB := simpleChat(cfg.OpenAIModel, body)
		tp2 := 0.9
		reqB.TopP = &tp2

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.34 params_seed_same — same seed, same body → hit.
	t.Run("1.34_params_seed_same", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.34_params_seed_same")
		key := "phase1-k34"
		body := "Recommend one Latin saying."
		seed := 42

		reqA := simpleChat(cfg.OpenAIModel, body)
		reqA.Seed = &seed
		reqB := simpleChat(cfg.OpenAIModel, body)
		reqB.Seed = &seed

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, reqB, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.35 params_seed_diff — different seeds → miss.
	t.Run("1.35_params_seed_diff", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.35_params_seed_diff")
		key := "phase1-k35"
		body := "Recommend one quote about patience."

		reqA := simpleChat(cfg.OpenAIModel, body)
		s1 := 42
		reqA.Seed = &s1

		reqB := simpleChat(cfg.OpenAIModel, body)
		s2 := 99
		reqB.Seed = &s2

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.36 params_max_tokens_isolation — max_tokens in params hash.
	t.Run("1.36_params_max_tokens_isolation", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.36_params_max_tokens_isolation")
		key := "phase1-k36"
		body := "List two healthy snacks."

		reqA := simpleChat(cfg.OpenAIModel, body)
		m1 := 60
		reqA.MaxTokens = &m1

		reqB := simpleChat(cfg.OpenAIModel, body)
		m2 := 120
		reqB.MaxTokens = &m2

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.48 tools_order_independent — Tools is hashed as a sorted set (utils.go:801-813),
	// so reordering identical tool definitions must NOT change the cache_id.
	// This catches the MCP-randomized-map regression the docstring calls out.
	t.Run("1.48_tools_order_independent", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.48_tools_order_independent")
		key := "phase1-k48"
		body := "Look up the current weather in Tokyo."

		toolA := chatTool{Type: "function", Function: &toolFunction{
			Name: "get_weather", Description: "Get current weather",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}},
		}}
		toolB := chatTool{Type: "function", Function: &toolFunction{
			Name: "search_web", Description: "Search the web",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}},
		}}

		reqA := simpleChat(cfg.OpenAIModel, body)
		reqA.Tools = []chatTool{toolA, toolB}
		reqB := simpleChat(cfg.OpenAIModel, body)
		reqB.Tools = []chatTool{toolB, toolA} // swapped order

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, reqB, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 1.49 tools_function_name_change — different tool names → distinct params hash → miss.
	t.Run("1.49_tools_function_name_change", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.49_tools_function_name_change")
		key := "phase1-k49"
		body := "Search for top hiking trails near Seattle."

		mkTool := func(name string) chatTool {
			return chatTool{Type: "function", Function: &toolFunction{
				Name: name, Description: "do a search",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}},
			}}
		}

		reqA := simpleChat(cfg.OpenAIModel, body)
		reqA.Tools = []chatTool{mkTool("search")}

		reqB := simpleChat(cfg.OpenAIModel, body)
		reqB.Tools = []chatTool{mkTool("lookup")}

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.50 prompt_cache_key_in_metadata — params.PromptCacheKey is extracted
	// into the metadata map (utils.go:781) so different values → different cache_ids.
	t.Run("1.50_prompt_cache_key_in_metadata", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.50_prompt_cache_key_in_metadata")
		key := "phase1-k50"
		body := "Translate 'hello' to French."

		reqA := simpleChat(cfg.OpenAIModel, body)
		pckA := "tenant-X"
		reqA.PromptCacheKey = &pckA

		reqB := simpleChat(cfg.OpenAIModel, body)
		pckB := "tenant-Y"
		reqB.PromptCacheKey = &pckB

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.51 service_tier_in_metadata — service_tier is in params hash.
	t.Run("1.51_service_tier_in_metadata", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.51_service_tier_in_metadata")
		key := "phase1-k51"
		body := "Define empathy in one sentence."

		// "auto" and "default" are both accepted by gpt-4o-mini ("flex" is gated
		// on premium models). The point is to differ; the values matter only
		// for params_hash isolation.
		reqA := simpleChat(cfg.OpenAIModel, body)
		stA := "default"
		reqA.ServiceTier = &stA

		reqB := simpleChat(cfg.OpenAIModel, body)
		stB := "auto"
		reqB.ServiceTier = &stB

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.52 store_flag_in_metadata — params.Store toggle changes params hash.
	t.Run("1.52_store_flag_in_metadata", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.52_store_flag_in_metadata")
		key := "phase1-k52"
		body := "Name one chess opening."

		reqA := simpleChat(cfg.OpenAIModel, body)
		storeA := true
		reqA.Store = &storeA

		reqB := simpleChat(cfg.OpenAIModel, body)
		storeB := false
		reqB.Store = &storeB

		respA := postChat(t, lc, 1, reqA, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)

		respB := postChat(t, lc, 3, reqB, cacheHeaders{Key: key})
		idB := assertMiss(t, lc, 4, respB)
		assertDifferentCacheID(t, lc, 5, idA, idB)
	})

	// 1.54 ttl_zero_per_request — x-bf-cache-ttl=0s (or negative) falls back to
	// the plugin default TTL. Without this contract, "0s" would yield
	// expires_at=now and silently break caching for the affected request;
	// instead the plugin treats non-positive values as "use default", matching
	// Init's behavior for Config.TTL=0.
	t.Run("1.54_ttl_zero_per_request", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.54_ttl_zero_per_request")
		req := simpleChat(cfg.OpenAIModel, "Name a constellation.")
		key := "phase1-k54"

		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key, TTL: "0s"})
		idA := assertMiss(t, lc, 2, respA)

		waitForCacheWrite(t, lc, 3)

		// B with TTL=0s should hit — the override is rejected as non-positive
		// and the plugin's default (10s) keeps A's entry alive.
		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, TTL: "0s"})
		idB := assertHit(t, lc, 5, respB, "direct")
		assertSameCacheID(t, lc, 6, idB, idA)

		// Negative TTL should follow the same fallback path.
		respC := postChat(t, lc, 7, req, cacheHeaders{Key: key, TTL: "-30s"})
		_ = assertHit(t, lc, 8, respC, "direct")
	})

	// 1.55 cache_debug_in_logs_endpoint — cross-check that the persisted log
	// row's cache_debug column matches the in-flight response stamp. Guards
	// against drift between PostLLMHook stamping and durable storage (same
	// data path the UI Logs view reads).
	t.Run("1.55_cache_debug_in_logs_endpoint", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("direct", "1.55_cache_debug_in_logs_endpoint")
		key := "phase1-k55"
		req := simpleChat(cfg.OpenAIModel, "Name one famous lighthouse.")

		// Generate a hit so cache_debug carries the full set of hit-only fields.
		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, req, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key})
		respCD := assertHitAndReturnCacheDebug(t, lc, 5, respB, "direct")

		entry := findLogByCacheDebug(t, lc, 6, respCD)
		assertLogMatchesResponseCacheDebug(t, lc, 7, respCD, entry.CacheDebug)
	})

	logf(t, newLogCtx("direct", "teardown").at(99), "TEARDOWN", "phase_end", nil)
}

// assertHitAndReturnCacheDebug is the same as assertHit but also returns the
// full cacheDebug struct (the regular helper returns just the cache_id string).
// Used by the /api/logs cross-check cases that need to compare all fields.
func assertHitAndReturnCacheDebug(t *testing.T, lc logCtx, step int, resp cacheDebugged, wantType string) *cacheDebug {
	t.Helper()
	_ = assertHit(t, lc, step, resp, wantType)
	return resp.cacheDebug()
}

// restoreDirectBaseline PUTs the canonical direct-only config so cases that
// mutate via pluginUpdate leave a clean slate for the next subtest.
func restoreDirectBaseline(t *testing.T, lc logCtx, step int) {
	t.Helper()
	pluginUpdate(t, lc, step, true, directOnlyConfig(ttlDirect, defaultKeyDirect))
}

// Defaults the phase 1 cases share. Kept narrow so a future case can tighten
// ttl (e.g. case 1.14) without colliding.
const (
	ttlDirect        = "10s"
	defaultKeyDirect = "phase1-default"
)

var ttlDirectDuration = 10 * time.Second

func simpleChat(model, content string) chatRequest {
	return chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "user", Content: textContent(content)},
		},
	}
}

func chatWithSystem(model, system, user string) chatRequest {
	return chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: textContent(system)},
			{Role: "user", Content: textContent(user)},
		},
	}
}

// chatWithImage builds a user message with an image_url + text block. Used to
// exercise the attachments path of buildRequestMetadataForCaching.
func chatWithImage(model, text, imageURL string) chatRequest {
	return chatRequest{
		Model: model,
		Messages: []chatMessage{{
			Role: "user",
			Content: blocksContent([]map[string]any{
				{"type": "text", "text": text},
				{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
			}),
		}},
	}
}
