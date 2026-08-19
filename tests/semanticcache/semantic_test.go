package semanticcache

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestParaphraseFixtures pre-flights every pair in paraphrasePairs against
// the deployed embedding model. Fails early with the actual cosine values
// if a pair has drifted, so downstream semantic cases never debug a
// borderline-flaky pair. Costs ~10 embedding calls (cents).
//
// Set SC_SKIP_FIXTURE_VERIFY=1 to skip this when running semantic cases
// against an environment with no openai/text-embedding-3-small access.
func TestParaphraseFixtures(t *testing.T) {
	if os.Getenv("SC_SKIP_FIXTURE_VERIFY") == "1" {
		t.Skip("SC_SKIP_FIXTURE_VERIFY=1")
	}
	for _, pair := range paraphrasePairs {
		p := pair
		t.Run(p.Name, func(t *testing.T) {
			t.Parallel()
			lc := newLogCtx("fixtures", p.Name)

			ec := embedVector(t, lc, 1, p.Canonical)
			ep := embedVector(t, lc, 2, p.Paraphrase)
			eu := embedVector(t, lc, 3, p.Unrelated)

			simHit := cosine(ec, ep)
			simMiss := cosine(ec, eu)

			logf(t, lc.at(4), "INFO", "cosine_check", map[string]any{
				"hit_cosine":  fmt.Sprintf("%.4f", simHit),
				"miss_cosine": fmt.Sprintf("%.4f", simMiss),
			})

			if simHit < 0.85 {
				t.Errorf("HIT cosine %.4f < 0.85 — paraphrase too distant\n  canonical=%q\n  paraphrase=%q",
					simHit, p.Canonical, p.Paraphrase)
			}
			if simMiss > 0.6 {
				t.Errorf("MISS cosine %.4f > 0.6 — unrelated too close\n  canonical=%q\n  unrelated=%q",
					simMiss, p.Canonical, p.Unrelated)
			}
		})
	}
}

// embedVector hits /v1/embeddings and parses the float64 vector. Plugin
// state irrelevant — direct API call.
func embedVector(t *testing.T, lc logCtx, step int, text string) []float64 {
	t.Helper()
	req := embeddingRequest{Model: "openai/" + cfg.OpenAIEmbed, Input: text}
	status, body, _, err := doJSON(t, "POST", "/v1/embeddings", req, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("embedVector: status=%d err=%v body=%s", status, err, truncate(string(body), 300))
	}
	var resp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("embedVector decode: %v", err)
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		t.Fatalf("embedVector: empty data in response %s", truncate(string(body), 300))
	}
	logf(t, lc.at(step), "INFO", "embedding_computed", map[string]any{
		"dim":      len(resp.Data[0].Embedding),
		"text_len": len(text),
	})
	return resp.Data[0].Embedding
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// -----------------------------------------------------------------------------
// Phase 2 — semantic mode
// -----------------------------------------------------------------------------

const (
	ttlSemantic        = "30s"
	defaultKeySemantic = "phase2-default"
	thresholdSemantic  = 0.85
)

// semanticNamespace is a dedicated Weaviate class for the semantic-mode suite.
// Phase 1 created cfg.Namespace with dimension=1 (direct-only); reusing that
// namespace for dim=1536 writes would error out with "vector dimensions do
// not match the index dimensions" — a Weaviate constraint, not a plugin bug.
// Real users switching modes face the same constraint and create a new
// namespace, so the suite mirrors that.
func semanticNamespace() string { return cfg.Namespace + "Semantic" }

// semanticBaseline is the canonical Phase 2 plugin config — used by setup and
// by every t.Cleanup that restores baseline after a mutating case.
func semanticBaseline() map[string]any {
	// Lock the embedding model: dimension=1536 is hard-coded and only
	// text-embedding-3-small produces 1536-dim vectors. Any other model would
	// cause confusing dimension-mismatch failures downstream rather than a
	// clear prerequisite error here.
	if cfg.OpenAIEmbed != "text-embedding-3-small" {
		panic(fmt.Sprintf("semantic suite expects cfg.OpenAIEmbed=text-embedding-3-small, got %q", cfg.OpenAIEmbed))
	}
	c := semanticConfig("openai", cfg.OpenAIEmbed, 1536, ttlSemantic, thresholdSemantic, defaultKeySemantic)
	c["vector_store_namespace"] = semanticNamespace()
	return c
}

func restoreSemanticBaseline(t *testing.T, lc logCtx, step int) {
	t.Helper()
	pluginUpdate(t, lc, step, true, semanticBaseline())
}

// TestSemantic runs the semantic-mode cases (2.1–2.44).
//
// Parallelism rules (same as Phase 1):
//
//   - Read-only cases call `t.Parallel()`.
//   - Cases that mutate plugin config via `pluginUpdate` (2.12, 2.13, 2.21,
//     2.31, 2.32) MUST NOT call `t.Parallel()`. They run synchronously inside
//     the parent loop, one at a time, restoring baseline via `t.Cleanup`.
//
// Plugin lifecycle: this test is self-contained — it upserts the plugin to
// semantic mode at setup regardless of whether Phase 1 ran. Existing entries
// in the namespace from prior runs are tolerated because each case uses a
// unique cache_key (phase2-kNN).
func TestSemantic(t *testing.T) {
	lc := newLogCtx("semantic", "setup")
	logf(t, lc.at(0), "SETUP", "phase_start", map[string]any{
		"mode":      "semantic",
		"ttl":       ttlSemantic,
		"threshold": thresholdSemantic,
		"dimension": 1536,
	})

	// Upsert plugin to semantic mode. PUT creates with enabled:false if
	// missing, then the same call's body sets enabled:true + config.
	if _, exists := pluginGet(t, lc, 1); exists {
		pluginUpdate(t, lc, 2, true, semanticBaseline())
	} else {
		pluginCreate(t, lc, 2, true, semanticBaseline())
	}

	allKeys := []string{
		defaultKeySemantic,
		"phase2-k1", "phase2-k2", "phase2-k3", "phase2-k4", "phase2-k5",
		"phase2-k6", "phase2-k7", "phase2-k8", "phase2-k9", "phase2-k10", "phase2-k10-alt",
		"phase2-k11", "phase2-k12", "phase2-k13", "phase2-k14", "phase2-k15",
		"phase2-k16", "phase2-k17", "phase2-k18", "phase2-k19", "phase2-k20",
		"phase2-k21", "phase2-k22", "phase2-k23", "phase2-k24", "phase2-k25",
		"phase2-k26", "phase2-k27", "phase2-k28", "phase2-k29",
		"phase2-k31a", "phase2-k32",
		"phase2-k33", "phase2-k34", "phase2-k35", "phase2-k36",
		"phase2-k37", "phase2-k38", "phase2-k39",
		"phase2-k40", "phase2-k41", "phase2-k42", "phase2-k43",
		"phase2-k39-seedA", "phase2-k39-seedB",
		"phase2-k44",
	}
	t.Cleanup(func() {
		// Surface unexpected cleanup failures so stale entries don't poison
		// subsequent runs. 404 is fine — not every key in allKeys gets
		// written by every run.
		for _, k := range allKeys {
			if got := clearByCacheKey(t, lc.at(99), 99, k); got != http.StatusOK && got != http.StatusNotFound {
				t.Errorf("cleanup clearByCacheKey(%q): unexpected status %d", k, got)
			}
		}
	})

	// 2.1 direct_path_still_works — exact-match in semantic mode hits direct first.
	t.Run("2.1_direct_path_still_works", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.1_direct_path_still_works")
		key := "phase2-k1"
		req := simpleChat(cfg.OpenAIModel, "Name one common edible mushroom variety.")
		respA := postChat(t, lc, 1, req, cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)
		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key})
		idB := assertHit(t, lc, 5, respB, "direct") // direct runs first in semantic mode
		assertSameCacheID(t, lc, 6, idB, idA)
	})

	// 2.2 semantic_hit_paraphrase — distinct text but high semantic similarity.
	t.Run("2.2_semantic_hit_paraphrase", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.2_semantic_hit_paraphrase")
		key := "phase2-k2"
		pair := pairByName(t, "capital_france")

		respA := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "semantic")
		cd := respB.cacheDebug()
		if cd.Similarity == nil || cd.Threshold == nil || cd.ProviderUsed == nil || cd.ModelUsed == nil || cd.InputTokens == nil {
			t.Fatalf("expected similarity/threshold/provider_used/model_used/input_tokens stamped on semantic hit, got %+v", cd)
		}
		if *cd.Similarity < *cd.Threshold {
			t.Fatalf("semantic hit but similarity %.4f < threshold %.4f", *cd.Similarity, *cd.Threshold)
		}
	})

	// 2.3 below_threshold_miss — unrelated body misses, but cache_debug still
	// stamped with provider_used/input_tokens (semantic search ran).
	t.Run("2.3_below_threshold_miss", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.3_below_threshold_miss")
		key := "phase2-k3"
		pair := pairByName(t, "boiling_water")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Unrelated), cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 5, respB)
		cd := respB.cacheDebug()
		if cd.ProviderUsed == nil || cd.InputTokens == nil {
			t.Fatalf("expected provider_used + input_tokens stamped on semantic-search miss, got %+v", cd)
		}
	})

	// 2.4 threshold_header_relax — low threshold accepts unrelated as hit.
	t.Run("2.4_threshold_header_relax", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.4_threshold_header_relax")
		key := "phase2-k4"
		pair := pairByName(t, "vinaigrette")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		low := 0.1
		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Unrelated), cacheHeaders{Key: key, Threshold: &low})
		_ = assertHit(t, lc, 5, respB, "semantic")
	})

	// 2.5 threshold_header_tighten — high threshold rejects a normally-hit paraphrase.
	t.Run("2.5_threshold_header_tighten", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.5_threshold_header_tighten")
		key := "phase2-k5"
		pair := pairByName(t, "opera_composer")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		high := 0.999
		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, Threshold: &high})
		_ = assertMiss(t, lc, 5, respB)
	})

	// 2.6 threshold_clamp_above — threshold > 1.0 clamps to 1.0 → miss.
	t.Run("2.6_threshold_clamp_above", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.6_threshold_clamp_above")
		key := "phase2-k6"
		pair := pairByName(t, "photosynthesis")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		over := 2.0 // clamps to 1.0
		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, Threshold: &over})
		_ = assertMiss(t, lc, 5, respB)
	})

	// 2.7 threshold_clamp_below — threshold < 0 clamps to 0 → hits anything.
	t.Run("2.7_threshold_clamp_below", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.7_threshold_clamp_below")
		key := "phase2-k7"
		pair := pairByName(t, "capital_france")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		under := -1.0 // clamps to 0.0
		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Unrelated), cacheHeaders{Key: key, Threshold: &under})
		_ = assertHit(t, lc, 5, respB, "semantic")
	})

	// 2.8 cache_type_direct_in_semantic — x-bf-cache-type=direct on a paraphrase
	// suppresses semantic search; B misses despite high similarity.
	t.Run("2.8_cache_type_direct_in_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.8_cache_type_direct_in_semantic")
		key := "phase2-k8"
		pair := pairByName(t, "boiling_water")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, Type: "direct"})
		_ = assertMiss(t, lc, 5, respB)
	})

	// 2.9 cache_type_semantic_only_exact — x-bf-cache-type=semantic on identical
	// body still produces a hit, but via the semantic path (direct suppressed).
	t.Run("2.9_cache_type_semantic_only_exact", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.9_cache_type_semantic_only_exact")
		key := "phase2-k9"
		req := simpleChat(cfg.OpenAIModel, "Recommend one short documentary film about science.")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, req, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, req, cacheHeaders{Key: key, Type: "semantic"})
		_ = assertHit(t, lc, 5, respB, "semantic")
	})

	// 2.10 cache_key_isolation_semantic — paraphrases under different keys → miss.
	t.Run("2.10_cache_key_isolation_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.10_cache_key_isolation_semantic")
		pair := pairByName(t, "vinaigrette")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: "phase2-k10"}))
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: "phase2-k10-alt"})
		_ = assertMiss(t, lc, 5, respB)
	})

	// 2.11 cache_by_model_isolation_semantic — different models, default flag → miss.
	t.Run("2.11_cache_by_model_isolation_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.11_cache_by_model_isolation_semantic")
		key := "phase2-k11"
		pair := pairByName(t, "photosynthesis")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModelAlt, pair.Paraphrase), cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 5, respB)
	})

	// 2.12 cache_by_model_false_semantic — flip flag, paraphrase cross-model → hit.
	t.Run("2.12_cache_by_model_false_semantic", func(t *testing.T) {
		// Serial: mutates plugin config (cache_by_model=false).
		lc := newLogCtx("semantic", "2.12_cache_by_model_false_semantic")

		cfg2 := semanticBaseline()
		cfg2["cache_by_model"] = false
		pluginUpdate(t, lc, 1, true, cfg2)
		t.Cleanup(func() { restoreSemanticBaseline(t, lc, 99) })

		key := "phase2-k12"
		pair := pairByName(t, "opera_composer")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModelAlt, pair.Paraphrase), cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "semantic")
	})

	// 2.13 cross_provider_semantic — both cache_by_* flags off; paraphrase across providers → hit.
	t.Run("2.13_cross_provider_semantic", func(t *testing.T) {
		// Serial: mutates plugin config (cache_by_provider/model=false).
		if cfg.AnthroModel == "" {
			t.Skip("anthropic model not configured")
		}
		lc := newLogCtx("semantic", "2.13_cross_provider_semantic")

		cfg2 := semanticBaseline()
		cfg2["cache_by_model"] = false
		cfg2["cache_by_provider"] = false
		pluginUpdate(t, lc, 1, true, cfg2)
		t.Cleanup(func() { restoreSemanticBaseline(t, lc, 99) })

		key := "phase2-k13"
		pair := pairByName(t, "capital_france")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		respB := postChat(t, lc, 4, simpleChat(cfg.AnthroModel, pair.Paraphrase), cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "semantic")
	})

	// 2.14 streaming_semantic_replay — paraphrase across two SSE streams → B replays.
	t.Run("2.14_streaming_semantic_replay", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.14_streaming_semantic_replay")
		key := "phase2-k14"
		pair := pairByName(t, "boiling_water")

		respA := postChatStream(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)

		respB := postChatStream(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "semantic")
		if len(respB.dataChunks()) != len(respA.dataChunks()) {
			t.Fatalf("expected B chunk count %d to match A's %d", len(respB.dataChunks()), len(respA.dataChunks()))
		}
	})

	// 2.15 semantic_then_direct_same_request — paraphrase stores; exact same body
	// hits via direct (faster, embedding-cost fields absent on B).
	t.Run("2.15_semantic_then_direct_same_request", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.15_semantic_then_direct_same_request")
		key := "phase2-k15"
		pair := pairByName(t, "vinaigrette")

		// A: canonical body — stores under direct ID.
		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		// B: same canonical body — direct runs first and hits.
		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "direct")
		cd := respB.cacheDebug()
		if cd.ProviderUsed != nil || cd.ModelUsed != nil || cd.InputTokens != nil {
			t.Fatalf("expected provider_used/model_used/input_tokens NIL on direct hit (no embedding generated), got %+v", cd)
		}
	})

	// 2.16 clear_cache_id_semantic — populate via semantic, delete by id, retry → miss.
	t.Run("2.16_clear_cache_id_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.16_clear_cache_id_semantic")
		key := "phase2-k16"
		pair := pairByName(t, "opera_composer")

		respA := postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key})
		idA := assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)
		// Confirm paraphrase hits.
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key}), "semantic")

		if got := clearByCacheID(t, lc, 6, idA); got != http.StatusOK {
			t.Fatalf("expected 200 from clear-by-id, got %d", got)
		}

		// Paraphrase now misses.
		_ = assertMiss(t, lc, 8, postChat(t, lc, 7, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key}))
	})

	// 2.17 clear_by_key_semantic — populate 2 paraphrases, clear-by-key, all miss.
	t.Run("2.17_clear_by_key_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.17_clear_by_key_semantic")
		key := "phase2-k17"
		pair1 := pairByName(t, "capital_france")
		pair2 := pairByName(t, "photosynthesis")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair1.Canonical), cacheHeaders{Key: key}))
		_ = assertMiss(t, lc, 4, postChat(t, lc, 3, simpleChat(cfg.OpenAIModel, pair2.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 5)

		if got := clearByCacheKey(t, lc, 6, key); got != http.StatusOK {
			t.Fatalf("expected 200, got %d", got)
		}

		_ = assertMiss(t, lc, 8, postChat(t, lc, 7, simpleChat(cfg.OpenAIModel, pair1.Paraphrase), cacheHeaders{Key: key}))
		_ = assertMiss(t, lc, 10, postChat(t, lc, 9, simpleChat(cfg.OpenAIModel, pair2.Paraphrase), cacheHeaders{Key: key}))
	})

	// 2.18 ttl_expiry_semantic — sleep past TTL, paraphrase misses.
	t.Run("2.18_ttl_expiry_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.18_ttl_expiry_semantic")
		key := "phase2-k18"
		pair := pairByName(t, "boiling_water")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key, TTL: "5s"}))
		waitForCacheWrite(t, lc, 3)

		// Confirm hit within TTL.
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, TTL: "5s"}), "semantic")

		wait := 6 * time.Second
		logf(t, lc.at(6), "INFO", "sleep_past_ttl", map[string]any{"seconds": wait.Seconds()})
		time.Sleep(wait)

		_ = assertMiss(t, lc, 8, postChat(t, lc, 7, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, TTL: "5s"}))
	})

	// 2.19 ttl_per_request_semantic — distinct shorter TTL applies.
	t.Run("2.19_ttl_per_request_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.19_ttl_per_request_semantic")
		key := "phase2-k19"
		pair := pairByName(t, "vinaigrette")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key, TTL: "4s"}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, TTL: "4s"}), "semantic")

		time.Sleep(5 * time.Second)
		_ = assertMiss(t, lc, 7, postChat(t, lc, 6, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, TTL: "4s"}))
	})

	// 2.20 no_store_semantic — A no-store; B paraphrase → miss (nothing stored).
	t.Run("2.20_no_store_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.20_no_store_semantic")
		key := "phase2-k20"
		pair := pairByName(t, "opera_composer")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key, NoStore: "true"}))
		waitForCacheWrite(t, lc, 3)

		_ = assertMiss(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key}))
	})

	// 2.21 exclude_system_prompt_semantic — flag flips system out of hash + embedding;
	// paraphrase + different systems → semantic hit.
	t.Run("2.21_exclude_system_prompt_semantic", func(t *testing.T) {
		// Serial: mutates plugin config.
		lc := newLogCtx("semantic", "2.21_exclude_system_prompt_semantic")
		cfg2 := semanticBaseline()
		cfg2["exclude_system_prompt"] = true
		pluginUpdate(t, lc, 1, true, cfg2)
		t.Cleanup(func() { restoreSemanticBaseline(t, lc, 99) })

		key := "phase2-k21"
		pair := pairByName(t, "capital_france")
		userA := pair.Canonical
		userB := pair.Paraphrase

		_ = assertMiss(t, lc, 3, postChat(t, lc, 2, chatWithSystem(cfg.OpenAIModel, "You are a geographer.", userA), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 4)

		_ = assertHit(t, lc, 6, postChat(t, lc, 5, chatWithSystem(cfg.OpenAIModel, "You are a poet.", userB), cacheHeaders{Key: key}), "semantic")
	})

	// 2.22 conversation_threshold_semantic — 4-message conversation skipped entirely.
	t.Run("2.22_conversation_threshold_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.22_conversation_threshold_semantic")
		key := "phase2-k22"

		msgs := []chatMessage{
			{Role: "user", Content: textContent("Hi.")},
			{Role: "assistant", Content: textContent("Hello! How can I help?")},
			{Role: "user", Content: textContent("Tell me about the boiling point of water.")},
			{Role: "user", Content: textContent("Actually, just give me the temperature in Celsius.")},
		}
		req := chatRequest{Model: cfg.OpenAIModel, Messages: msgs}

		assertNoCacheDebug(t, lc, 2, postChat(t, lc, 1, req, cacheHeaders{Key: key}))
		assertNoCacheDebug(t, lc, 4, postChat(t, lc, 3, req, cacheHeaders{Key: key}))
	})

	// 2.23 attachments_change_semantic — paraphrase + different image URL → miss
	// (attachments part of params_hash, filter excludes).
	t.Run("2.23_attachments_change_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.23_attachments_change_semantic")
		key := "phase2-k23"
		textA := "What's pictured in this image?"
		textB := "Describe the contents of this image."

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, chatWithImage(cfg.OpenAIModel, textA, testImageURL1), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertMiss(t, lc, 5, postChat(t, lc, 4, chatWithImage(cfg.OpenAIModel, textB, testImageURL2), cacheHeaders{Key: key}))
	})

	// 2.24 embedding_endpoint_semantic_skip — embedding requests bypass semantic
	// search entirely (PreLLMHook semanticEligible check). Exact match hits
	// direct; different input misses (no paraphrase match attempt).
	t.Run("2.24_embedding_endpoint_semantic_skip", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.24_embedding_endpoint_semantic_skip")
		key := "phase2-k24"
		req := embeddingRequest{Model: "openai/" + cfg.OpenAIEmbed, Input: "The cat sat on the mat."}

		_ = assertMiss(t, lc, 2, postEmbedding(t, lc, 1, req, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postEmbedding(t, lc, 4, req, cacheHeaders{Key: key}), "direct")

		// Different input — no semantic fallback, just direct miss.
		req2 := embeddingRequest{Model: "openai/" + cfg.OpenAIEmbed, Input: "The dog chased the ball."}
		_ = assertMiss(t, lc, 7, postEmbedding(t, lc, 6, req2, cacheHeaders{Key: key}))
	})

	// 2.25 image_gen_semantic_paraphrase — image prompts paraphrase across two calls.
	t.Run("2.25_image_gen_semantic_paraphrase", func(t *testing.T) {
		t.Parallel()
		if os.Getenv("SC_SKIP_IMAGE_GEN") == "1" {
			t.Skip("SC_SKIP_IMAGE_GEN=1")
		}
		lc := newLogCtx("semantic", "2.25_image_gen_semantic_paraphrase")
		key := "phase2-k25"
		pair := imagePairByName(t, "red_apple")
		n := 1
		reqA := imageGenRequest{Model: "openai/dall-e-3", Prompt: pair.Canonical, N: &n, Size: "1024x1024"}
		reqB := imageGenRequest{Model: "openai/dall-e-3", Prompt: pair.Paraphrase, N: &n, Size: "1024x1024"}

		_ = assertMiss(t, lc, 2, postImageGen(t, lc, 1, reqA, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postImageGen(t, lc, 4, reqB, cacheHeaders{Key: key}), "semantic")
	})

	// 2.26 responses_api_semantic — paraphrase on /v1/responses → semantic hit.
	t.Run("2.26_responses_api_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.26_responses_api_semantic")
		key := "phase2-k26"
		pair := pairByName(t, "photosynthesis")
		reqA := responsesRequest{Model: cfg.OpenAIModel, Input: pair.Canonical}
		reqB := responsesRequest{Model: cfg.OpenAIModel, Input: pair.Paraphrase}

		_ = assertMiss(t, lc, 2, postResponses(t, lc, 1, reqA, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postResponses(t, lc, 4, reqB, cacheHeaders{Key: key}), "semantic")
	})

	// 2.27 text_completion_semantic — paraphrase on /v1/completions → semantic hit.
	t.Run("2.27_text_completion_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.27_text_completion_semantic")
		key := "phase2-k27"
		maxTok := 40
		reqA := textCompletionRequest{Model: "openai/gpt-3.5-turbo-instruct", Prompt: "Briefly explain how photosynthesis works in green plants.", MaxTokens: &maxTok}
		reqB := textCompletionRequest{Model: "openai/gpt-3.5-turbo-instruct", Prompt: "In a few sentences, describe how photosynthesis works in green plants.", MaxTokens: &maxTok}

		_ = assertMiss(t, lc, 2, postTextCompletion(t, lc, 1, reqA, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postTextCompletion(t, lc, 4, reqB, cacheHeaders{Key: key}), "semantic")
	})

	// 2.28 gemini_semantic_hit — chat provider != embedding provider.
	t.Run("2.28_gemini_semantic_hit", func(t *testing.T) {
		t.Parallel()
		if cfg.GeminiModel == "" {
			t.Skip("gemini model not configured")
		}
		lc := newLogCtx("semantic", "2.28_gemini_semantic_hit")
		key := "phase2-k28"
		pair := pairByName(t, "capital_france")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.GeminiModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.GeminiModel, pair.Paraphrase), cacheHeaders{Key: key}), "semantic")
	})

	// 2.29 params_hash_isolates_semantic — paraphrases with different temperatures → miss.
	t.Run("2.29_params_hash_isolates_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.29_params_hash_isolates_semantic")
		key := "phase2-k29"
		pair := pairByName(t, "boiling_water")

		reqA := simpleChat(cfg.OpenAIModel, pair.Canonical)
		t1 := 0.2
		reqA.Temperature = &t1
		reqB := simpleChat(cfg.OpenAIModel, pair.Paraphrase)
		t2 := 0.9
		reqB.Temperature = &t2

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, reqA, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertMiss(t, lc, 5, postChat(t, lc, 4, reqB, cacheHeaders{Key: key}))
	})

	// 2.30 plugin_status_semantic — GET shows status active + semantic config.
	t.Run("2.30_plugin_status_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.30_plugin_status_semantic")
		p, exists := pluginGet(t, lc, 1)
		if !exists {
			t.Fatalf("plugin should exist mid-phase")
		}
		if !p.Enabled || p.Status.Status != "active" {
			t.Fatalf("expected enabled+active, got enabled=%v status=%q", p.Enabled, p.Status.Status)
		}
		gotProvider, _ := p.Config["provider"].(string)
		if gotProvider != "openai" {
			t.Fatalf("expected provider=openai, got %q", gotProvider)
		}
		gotDim, _ := p.Config["dimension"].(float64)
		if int(gotDim) != 1536 {
			t.Fatalf("expected dimension=1536, got %v", p.Config["dimension"])
		}
	})

	// 2.31 namespace_change_isolates — entries scoped to namespace; flipping
	// the namespace makes prior entries unreachable, flipping back restores.
	t.Run("2.31_namespace_change_isolates", func(t *testing.T) {
		// Serial: mutates plugin config (vector_store_namespace).
		lc := newLogCtx("semantic", "2.31_namespace_change_isolates")
		// Use a known body for direct-cache reproducibility.
		body := "What is the boiling point of pure water at standard pressure?"
		key := "phase2-k31a"
		altNS := cfg.Namespace + "Alt"
		// Step 7 will store an entry in altNS. The outer t.Cleanup at the
		// suite level iterates allKeys against whatever namespace the plugin
		// currently points at — once we restore baseline below, the altNS
		// entry becomes unreachable from there. Flip back to altNS, clear,
		// then restore baseline.
		t.Cleanup(func() {
			altCfg := semanticBaseline()
			altCfg["vector_store_namespace"] = altNS
			pluginUpdate(t, lc, 97, true, altCfg)
			_ = clearByCacheKey(t, lc.at(98), 98, key)
			restoreSemanticBaseline(t, lc, 99)
		})

		// Phase 2 baseline is namespace=cfg.Namespace. Populate an entry.
		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		// Confirm hit under baseline namespace.
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key}), "direct")

		// Flip to alternate namespace; same body should miss.
		cfg2 := semanticBaseline()
		cfg2["vector_store_namespace"] = altNS
		pluginUpdate(t, lc, 6, true, cfg2)
		_ = assertMiss(t, lc, 8, postChat(t, lc, 7, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key}))

		// Flip back to baseline; entry should resurface.
		pluginUpdate(t, lc, 9, true, semanticBaseline())
		_ = assertHit(t, lc, 11, postChat(t, lc, 10, simpleChat(cfg.OpenAIModel, body), cacheHeaders{Key: key}), "direct")
	})

	// 2.32 dimension_change_silent_miss — write at dim 1536, switch model to
	// dim 3072 same namespace; reads should miss (UI banner warns about this).
	// Documents actual behavior — error vs silent miss vs warn.
	t.Run("2.32_dimension_change_silent_miss", func(t *testing.T) {
		// Serial: mutates plugin config (embedding_model + dimension).
		lc := newLogCtx("semantic", "2.32_dimension_change_silent_miss")
		t.Cleanup(func() { restoreSemanticBaseline(t, lc, 99) })

		key := "phase2-k32"
		pair := pairByName(t, "opera_composer")

		// Write under dim=1536.
		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)

		// Switch to text-embedding-3-large (dim 3072) on the SAME namespace.
		cfg2 := semanticConfig("openai", "text-embedding-3-large", 3072, ttlSemantic, thresholdSemantic, defaultKeySemantic)
		cfg2["vector_store_namespace"] = semanticNamespace()
		pluginUpdate(t, lc, 4, true, cfg2)

		// Read paraphrase. Expected: miss (UI warns: "reads will silently miss").
		// If Bifrost errors instead, the test will fail at postChat with status!=200
		// — that surfaces a different actual behavior worth documenting.
		respB := postChat(t, lc, 5, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key})
		if cd := respB.cacheDebug(); cd != nil && cd.CacheHit {
			t.Fatalf("expected miss (UI banner: dim change makes reads silently miss); got hit cache_id=%s", deref(cd.CacheID))
		}
		logf(t, lc.at(6), "PASS", "dimension_change_silent_miss_documented", map[string]any{
			"behavior": "miss",
		})
	})

	// 2.33 streaming_tool_calls_replay — paraphrase preserves tool_calls in replay.
	t.Run("2.33_streaming_tool_calls_replay", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.33_streaming_tool_calls_replay")
		key := "phase2-k33"
		toolDef := chatTool{Type: "function", Function: &toolFunction{
			Name: "get_weather", Description: "Get the current weather in a city",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}},
		}}

		reqA := simpleChat(cfg.OpenAIModel, "What's the current weather in Tokyo right now?")
		reqA.Tools = []chatTool{toolDef}
		reqB := simpleChat(cfg.OpenAIModel, "Tell me the present weather in Tokyo right now.")
		reqB.Tools = []chatTool{toolDef}

		respA := postChatStream(t, lc, 1, reqA, cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA)
		waitForCacheWrite(t, lc, 3)
		respB := postChatStream(t, lc, 4, reqB, cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "semantic")
		if len(respB.dataChunks()) != len(respA.dataChunks()) {
			t.Fatalf("chunk count mismatch: A=%d B=%d", len(respA.dataChunks()), len(respB.dataChunks()))
		}
	})

	// 2.34 tools_order_independent_semantic — paraphrase with reordered tools → hit.
	t.Run("2.34_tools_order_independent_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.34_tools_order_independent_semantic")
		key := "phase2-k34"
		toolA := chatTool{Type: "function", Function: &toolFunction{Name: "get_weather", Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}}
		toolB := chatTool{Type: "function", Function: &toolFunction{Name: "search_web", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}

		reqA := simpleChat(cfg.OpenAIModel, "What is the capital city of France in modern times?")
		reqA.Tools = []chatTool{toolA, toolB}
		reqB := simpleChat(cfg.OpenAIModel, "Tell me the capital city of France in modern times.")
		reqB.Tools = []chatTool{toolB, toolA}

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, reqA, cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, reqB, cacheHeaders{Key: key}), "semantic")
	})

	// 2.35 tools_function_name_change_semantic — different tool names → miss.
	t.Run("2.35_tools_function_name_change_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.35_tools_function_name_change_semantic")
		key := "phase2-k35"
		mkTool := func(name string) chatTool {
			return chatTool{Type: "function", Function: &toolFunction{Name: name, Parameters: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}}
		}
		reqA := simpleChat(cfg.OpenAIModel, "Briefly explain how photosynthesis works in green plants.")
		reqA.Tools = []chatTool{mkTool("search")}
		reqB := simpleChat(cfg.OpenAIModel, "In a few sentences, describe how photosynthesis works in green plants.")
		reqB.Tools = []chatTool{mkTool("lookup")}

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, reqA, cacheHeaders{Key: key}))
		// Wait so reqA's write commits; otherwise reqB misses for trivial
		// reasons (empty cache) rather than tool-name isolation.
		waitForCacheWrite(t, lc, 3)
		_ = assertMiss(t, lc, 5, postChat(t, lc, 4, reqB, cacheHeaders{Key: key}))
	})

	// 2.36 prompt_cache_key_semantic — different prompt_cache_key → miss.
	t.Run("2.36_prompt_cache_key_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.36_prompt_cache_key_semantic")
		key := "phase2-k36"
		pair := pairByName(t, "vinaigrette")

		reqA := simpleChat(cfg.OpenAIModel, pair.Canonical)
		pckA := "tenant-X"
		reqA.PromptCacheKey = &pckA
		reqB := simpleChat(cfg.OpenAIModel, pair.Paraphrase)
		pckB := "tenant-Y"
		reqB.PromptCacheKey = &pckB

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, reqA, cacheHeaders{Key: key}))
		// Wait so reqA's write commits; otherwise reqB misses for trivial
		// reasons (empty cache) rather than prompt_cache_key isolation.
		waitForCacheWrite(t, lc, 3)
		_ = assertMiss(t, lc, 5, postChat(t, lc, 4, reqB, cacheHeaders{Key: key}))
	})

	// 2.37 service_tier_semantic — different service_tier → miss.
	t.Run("2.37_service_tier_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.37_service_tier_semantic")
		key := "phase2-k37"
		pair := pairByName(t, "capital_france")

		reqA := simpleChat(cfg.OpenAIModel, pair.Canonical)
		stA := "default"
		reqA.ServiceTier = &stA
		reqB := simpleChat(cfg.OpenAIModel, pair.Paraphrase)
		stB := "auto"
		reqB.ServiceTier = &stB

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, reqA, cacheHeaders{Key: key}))
		// Wait so reqA's write commits; otherwise reqB misses for trivial
		// reasons (empty cache) rather than service_tier isolation.
		waitForCacheWrite(t, lc, 3)
		_ = assertMiss(t, lc, 5, postChat(t, lc, 4, reqB, cacheHeaders{Key: key}))
	})

	// 2.38 store_flag_semantic — different store flag → miss.
	t.Run("2.38_store_flag_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.38_store_flag_semantic")
		key := "phase2-k38"
		pair := pairByName(t, "boiling_water")

		reqA := simpleChat(cfg.OpenAIModel, pair.Canonical)
		storeA := true
		reqA.Store = &storeA
		reqB := simpleChat(cfg.OpenAIModel, pair.Paraphrase)
		storeB := false
		reqB.Store = &storeB

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, reqA, cacheHeaders{Key: key}))
		// Wait so reqA's write commits; otherwise reqB misses for trivial
		// reasons (empty cache) rather than store-flag isolation.
		waitForCacheWrite(t, lc, 3)
		_ = assertMiss(t, lc, 5, postChat(t, lc, 4, reqB, cacheHeaders{Key: key}))
	})

	// 2.39 responses_previous_response_id_semantic — different previous_response_id → miss.
	t.Run("2.39_responses_previous_response_id_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.39_responses_previous_response_id_semantic")
		key := "phase2-k39"

		// Seed two response IDs. Distinct cache keys are essential — sharing one
		// key would cause the second seed to semantic-hit the first and return
		// the SAME response id, defeating the isolation test.
		seed1 := postResponses(t, lc, 1, responsesRequest{Model: cfg.OpenAIModel, Input: "Recite the first digit of pi."}, cacheHeaders{Key: "phase2-k39-seedA", NoStore: "true"})
		var s1 struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(seed1.bodyRaw, &s1); err != nil || s1.ID == "" {
			t.Skipf("could not seed response id: %v", err)
		}
		seed2 := postResponses(t, lc, 2, responsesRequest{Model: cfg.OpenAIModel, Input: "Name the largest moon of Jupiter."}, cacheHeaders{Key: "phase2-k39-seedB", NoStore: "true"})
		var s2 struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(seed2.bodyRaw, &s2); err != nil || s2.ID == "" {
			t.Skipf("could not seed second response id: %v", err)
		}
		if s1.ID == s2.ID {
			t.Skipf("seed response ids collided (%s); test prerequisite not met", s1.ID)
		}

		reqA := responsesRequest{Model: cfg.OpenAIModel, Input: "Continue from before.", PreviousResponseID: &s1.ID}
		reqB := responsesRequest{Model: cfg.OpenAIModel, Input: "Continue from prior.", PreviousResponseID: &s2.ID}

		_ = assertMiss(t, lc, 4, postResponses(t, lc, 3, reqA, cacheHeaders{Key: key}))
		// Wait so reqA's write commits; otherwise reqB misses for trivial
		// reasons (empty cache) rather than previous_response_id isolation.
		waitForCacheWrite(t, lc, 5)
		_ = assertMiss(t, lc, 7, postResponses(t, lc, 6, reqB, cacheHeaders{Key: key}))
	})

	// 2.40 no_store_explicit_false_semantic — header value "false" doesn't toggle.
	t.Run("2.40_no_store_explicit_false_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.40_no_store_explicit_false_semantic")
		key := "phase2-k40"
		pair := pairByName(t, "opera_composer")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key, NoStore: "false"}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, NoStore: "false"}), "semantic")
	})

	// 2.41 no_store_uppercase_true_semantic — case-sensitive match; "TRUE" does NOT block.
	t.Run("2.41_no_store_uppercase_true_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.41_no_store_uppercase_true_semantic")
		key := "phase2-k41"
		pair := pairByName(t, "photosynthesis")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key, NoStore: "TRUE"}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, NoStore: "TRUE"}), "semantic")
	})

	// 2.42 streaming_non_final_chunks_no_cache_debug_semantic — only final
	// chunk has cache_debug, both on miss (semantic search ran) and hit (semantic replay).
	t.Run("2.42_streaming_non_final_chunks_no_cache_debug_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.42_streaming_non_final_chunks_no_cache_debug_semantic")
		key := "phase2-k42"
		pair := pairByName(t, "vinaigrette")

		check := func(stage string, resp *streamResponse) {
			data := resp.dataChunks()
			if len(data) == 0 {
				t.Fatalf("[%s] no data chunks", stage)
			}
			for i := 0; i < len(data)-1; i++ {
				if cd := data[i].cacheDebug(); cd != nil {
					t.Fatalf("[%s] non-final chunk %d had cache_debug: %+v", stage, i, cd)
				}
			}
			if data[len(data)-1].cacheDebug() == nil {
				t.Fatalf("[%s] final chunk missing cache_debug", stage)
			}
		}

		respA := postChatStream(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key})
		_ = assertMiss(t, lc, 2, respA)
		check("miss-with-semantic-search", respA)
		waitForCacheWrite(t, lc, 3)
		respB := postChatStream(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key})
		_ = assertHit(t, lc, 5, respB, "semantic")
		check("hit-semantic", respB)
	})

	// 2.43 ttl_zero_per_request_semantic — TTL=0s falls back to default; B paraphrase hits.
	t.Run("2.43_ttl_zero_per_request_semantic", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.43_ttl_zero_per_request_semantic")
		key := "phase2-k43"
		pair := pairByName(t, "capital_france")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key, TTL: "0s"}))
		waitForCacheWrite(t, lc, 3)
		_ = assertHit(t, lc, 5, postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key, TTL: "0s"}), "semantic")
	})

	// 2.44 cache_debug_in_logs_endpoint — cross-check persisted log row's
	// cache_debug column against the in-flight semantic hit. In semantic mode
	// cache_debug carries the richest field set (provider_used, model_used,
	// input_tokens, threshold, similarity), making this a high-value drift
	// check.
	t.Run("2.44_cache_debug_in_logs_endpoint", func(t *testing.T) {
		t.Parallel()
		lc := newLogCtx("semantic", "2.44_cache_debug_in_logs_endpoint")
		key := "phase2-k44"
		pair := pairByName(t, "vinaigrette")

		_ = assertMiss(t, lc, 2, postChat(t, lc, 1, simpleChat(cfg.OpenAIModel, pair.Canonical), cacheHeaders{Key: key}))
		waitForCacheWrite(t, lc, 3)
		respB := postChat(t, lc, 4, simpleChat(cfg.OpenAIModel, pair.Paraphrase), cacheHeaders{Key: key})
		respCD := assertHitAndReturnCacheDebug(t, lc, 5, respB, "semantic")

		entry := findLogByCacheDebug(t, lc, 6, respCD)
		assertLogMatchesResponseCacheDebug(t, lc, 7, respCD, entry.CacheDebug)
	})

	logf(t, newLogCtx("semantic", "teardown").at(99), "TEARDOWN", "phase_end", nil)
}
