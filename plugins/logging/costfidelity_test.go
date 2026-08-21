package logging

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin cost recomputation to the cost computed live at request time.
//
// The bug they guard against: calculateCostForLog rebuilds a BifrostResponse from a
// log row, and every pricing input the row fails to carry silently changes the
// price. The worst case is the cache breakdown — PromptTokens is normalized to be
// inclusive of the cache buckets (see the Anthropic chat mapping) and computeTextCost
// subtracts them back out, so losing the breakdown reprices every cached token at the
// full input rate. On a 70%-cache-read request that is a ~2.5x overbill; at 85% it is
// ~4.3x. A user hit ~3x in production.
//
// Every assertion here compares against live pricing on an equivalent response rather
// than merely checking cost > 0. A `> 0` assertion is exactly what let the drift ship.

const costEpsilon = 1e-12

func assertCostsEqual(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > costEpsilon {
		t.Fatalf("%s: recomputed cost = %.12f, live cost = %.12f (ratio %.4fx)", label, got, want, got/want)
	}
}

func newCostFidelityPlugin(t *testing.T) *LoggerPlugin {
	t.Helper()
	return &LoggerPlugin{
		store:          newTestStore(t),
		pricingManager: newTestPricingManager(t),
		logger:         testLogger{},
	}
}

// liveCost prices a response the way the PostHook does at request time.
func liveCost(t *testing.T, plugin *LoggerPlugin, resp *schemas.BifrostResponse, provider string) float64 {
	t.Helper()
	scopes := modelcatalog.PricingLookupScopes{Provider: provider}
	return plugin.pricingManager.CalculateCost(resp, &scopes)
}

// TestCalculateCostForLogMatchesLiveForCachedRequest is the regression test for the
// reported ~3x inflation. A 100k-token Anthropic prompt that is 70% cache read and
// 10% cache write must reprice to the same number it was billed at live.
func TestCalculateCostForLogMatchesLiveForCachedRequest(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const (
		promptTokens      = 100_000
		cachedReadTokens  = 70_000
		cachedWriteTokens = 10_000
		completionTokens  = 500
	)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  cachedReadTokens,
			CachedWriteTokens: cachedWriteTokens,
		},
	}

	want := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{
					Provider: schemas.Anthropic,
					Model:    "claude-sonnet-4-20250514",
				},
			},
		},
	}, string(schemas.Anthropic))
	if want <= 0 {
		t.Fatalf("live cost must be positive for the comparison to mean anything, got %v", want)
	}

	entry := &logstore.Log{
		ID:               "req-cache-parity",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		CachedReadTokens: cachedReadTokens,
		TokenUsageParsed: usage,
	}

	got, err := plugin.calculateCostForLog(entry)
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	assertCostsEqual(t, "cached chat request", got, want)

	// Guard the magnitude of the original defect explicitly: pricing the same token
	// count with no cache breakdown must be dramatically more expensive, so this test
	// would have failed loudly against the old code rather than passing on a
	// coincidence.
	noBreakdown := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{
					Provider: schemas.Anthropic,
					Model:    "claude-sonnet-4-20250514",
				},
			},
		},
	}, string(schemas.Anthropic))
	if noBreakdown <= want*1.5 {
		t.Fatalf("expected a cache-blind price to be far higher than the correct one; correct=%.12f blind=%.12f", want, noBreakdown)
	}
}

// TestCalculateCostForLogRefusesDegradedUsage covers the hybrid-store case that
// produced the production overbill: token_usage is offloaded to object storage, so
// DeserializeFields rebuilds a stub from the denormalized columns. Pricing that stub
// is what inflated the cost, so recomputation must refuse it and report an error the
// job can count as unpriceable.
func TestCalculateCostForLogRefusesDegradedUsage(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	// Mimic a row read back from a store that offloaded its payload: the token_usage
	// column is blank but the denormalized counters survive.
	entry := &logstore.Log{
		ID:               "req-degraded",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		HasObject:        true,
		TokenUsage:       "",
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		CachedReadTokens: 70_000,
	}
	if err := entry.DeserializeFields(); err != nil {
		t.Fatalf("DeserializeFields() error = %v", err)
	}
	if !entry.IsUsageDegraded() {
		t.Fatal("expected the column rebuild to mark usage as degraded")
	}

	if _, err := plugin.calculateCostForLog(entry); !errors.Is(err, errPricingInputsUnavailable) {
		t.Fatalf("expected errPricingInputsUnavailable, got %v", err)
	}
}

// TestCalculateCostForLogPricesDegradedDirectCacheHitAsZero pins the ordering of the two
// gates in calculateCostForLog.
//
// A direct cache hit was served without an LLM call, so calculateCostWithCache returns
// zero without reading usage at all — the token breakdown is irrelevant to the price.
// Refusing such a row for degraded usage would leave its cost nil forever, and because
// MissingCostOnly matches on cost <= 0 every later pass would fetch and reject it again.
// The zero must be recorded instead, which is terminal.
func TestCalculateCostForLogPricesDegradedDirectCacheHitAsZero(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	// Same offloaded-row shape as TestCalculateCostForLogRefusesDegradedUsage, plus the
	// cache_debug column. cache_debug is denormalized in the DB, so it survives on rows
	// whose token_usage payload does not.
	entry := &logstore.Log{
		ID:               "req-degraded-direct-hit",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		HasObject:        true,
		TokenUsage:       "",
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		CachedReadTokens: 70_000,
		CacheDebug:       `{"cache_hit":true,"hit_type":"direct"}`,
	}
	require.NoError(t, entry.DeserializeFields())
	require.True(t, entry.IsUsageDegraded(),
		"the fixture must actually be degraded or this test proves nothing")

	cost, err := plugin.calculateCostForLog(entry)
	require.NoError(t, err, "a provably-free row must not be reported unpriceable")
	assert.Zero(t, cost)
}

// TestDeserializeFieldsClearsDegradedFlagAfterHydration checks the hybrid hydration
// ordering: AfterFind builds the stub while token_usage is still blank, then hydration
// fills the column and re-deserializes. If the flag survived that, billing would skip
// rows it can now price correctly.
func TestDeserializeFieldsClearsDegradedFlagAfterHydration(t *testing.T) {
	entry := &logstore.Log{
		ID:               "req-hydrated",
		Timestamp:        time.Now().UTC(),
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		CachedReadTokens: 70_000,
	}
	if err := entry.DeserializeFields(); err != nil {
		t.Fatalf("first DeserializeFields() error = %v", err)
	}
	if !entry.IsUsageDegraded() {
		t.Fatal("expected degraded usage before hydration")
	}

	// Hydration writes the serialized column, then re-deserializes.
	entry.TokenUsage = `{"prompt_tokens":100000,"completion_tokens":500,"total_tokens":100500,` +
		`"prompt_tokens_details":{"cached_read_tokens":70000,"cached_write_tokens":10000}}`
	if err := entry.DeserializeFields(); err != nil {
		t.Fatalf("second DeserializeFields() error = %v", err)
	}
	if entry.IsUsageDegraded() {
		t.Fatal("expected the degraded flag to clear once a real payload parsed")
	}
	if entry.TokenUsageParsed.PromptTokensDetails.CachedWriteTokens != 10_000 {
		t.Fatalf("expected hydrated cache-write detail, got %+v", entry.TokenUsageParsed.PromptTokensDetails)
	}
}

// TestCalculateCostForLogPreservesServedTier pins the store-independent tier leak: the
// Log table had no service_tier column and BifrostLLMUsage tags Speed/InferenceGeo
// `json:"-"`, so recomputation priced every row at standard rates. On OpenAI that
// silently moved priority traffic onto the cheaper standard rate.
func TestCalculateCostForLogPreservesServedTier(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const promptTokens, completionTokens = 1_000, 200
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
	priority := schemas.BifrostServiceTierPriority

	want := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage:       usage,
			ServiceTier: &priority,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
			},
		},
	}, string(schemas.OpenAI))

	standard := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
			},
		},
	}, string(schemas.OpenAI))
	if math.Abs(want-standard) <= costEpsilon {
		t.Skip("test datasheet has no distinct priority rate for gpt-4o; tier parity is unobservable")
	}

	tier := string(schemas.BifrostServiceTierPriority)
	entry := &logstore.Log{
		ID:               "req-tier-parity",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.OpenAI),
		Model:            "gpt-4o",
		Status:           "success",
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		ServiceTier:      &tier,
		TokenUsageParsed: usage,
	}

	got, err := plugin.calculateCostForLog(entry)
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	assertCostsEqual(t, "priority-tier chat request", got, want)
}

// TestApplyServedTierToEntryRecordsResponseAndUsageSources checks both capture paths.
// Non-streaming responses carry the tier on the response envelope; the streaming
// accumulator has no envelope, so Speed/InferenceGeo must be read off the usage struct
// (which is why the provider populates them there).
func TestApplyServedTierToEntryRecordsResponseAndUsageSources(t *testing.T) {
	flex := schemas.BifrostServiceTierFlex

	t.Run("from response envelope", func(t *testing.T) {
		entry := &logstore.Log{}
		applyServedTierToEntry(entry, &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{ServiceTier: &flex},
		}, nil)
		if entry.ServiceTier == nil || *entry.ServiceTier != "flex" {
			t.Fatalf("expected service_tier flex, got %v", entry.ServiceTier)
		}
	})

	t.Run("from usage on the streaming path", func(t *testing.T) {
		entry := &logstore.Log{}
		applyServedTierToEntry(entry, nil, &schemas.BifrostLLMUsage{
			Speed:        new("fast"),
			InferenceGeo: new("us"),
		})
		if entry.Speed == nil || *entry.Speed != "fast" {
			t.Fatalf("expected speed fast, got %v", entry.Speed)
		}
		if entry.InferenceGeo == nil || *entry.InferenceGeo != "us" {
			t.Fatalf("expected inference_geo us, got %v", entry.InferenceGeo)
		}
	})

	t.Run("a later usage-only chunk does not blank an established tier", func(t *testing.T) {
		entry := &logstore.Log{}
		applyServedTierToEntry(entry, &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{ServiceTier: &flex},
		}, nil)
		applyServedTierToEntry(entry, nil, &schemas.BifrostLLMUsage{})
		if entry.ServiceTier == nil || *entry.ServiceTier != "flex" {
			t.Fatalf("expected service_tier to survive a tier-less update, got %v", entry.ServiceTier)
		}
	})
}

// TestStreamingServiceTierSurvivesAccumulatorHandoff covers the streaming leak: the
// accumulator resolves the served tier across chunks, but it then crosses the tracer
// boundary as a schemas.StreamAccumulatorResult. That struct carried no ServiceTier
// field, so every streamed row logged an empty service_tier and repriced at standard
// rates — the same class of mis-billing #5669 fixed for non-streamed rows.
func TestStreamingServiceTierSurvivesAccumulatorHandoff(t *testing.T) {
	priority := schemas.BifrostServiceTierPriority

	processed := convertToProcessedStreamResponse(&schemas.StreamAccumulatorResult{
		RequestID:      "req-stream-tier",
		RequestedModel: "gpt-4o",
		ResolvedModel:  "gpt-4o",
		Provider:       schemas.OpenAI,
		Status:         "success",
		ServiceTier:    &priority,
		TokenUsage:     &schemas.BifrostLLMUsage{TotalTokens: 1},
	}, schemas.ChatCompletionStreamRequest)
	if processed == nil || processed.Data == nil {
		t.Fatal("expected a processed stream response with data")
	}
	if processed.Data.ServiceTier == nil || *processed.Data.ServiceTier != schemas.BifrostServiceTierPriority {
		t.Fatalf("expected priority tier to survive the conversion, got %v", processed.Data.ServiceTier)
	}

	entry := &logstore.Log{}
	(&LoggerPlugin{}).applyStreamingOutputToEntry(entry, processed, false, false)
	if entry.ServiceTier == nil || *entry.ServiceTier != "priority" {
		t.Fatalf("expected service_tier priority on the log entry, got %v", entry.ServiceTier)
	}
}

// TestCalculateCostForLogPricesOneHourCacheWrites covers the Responses-path leak:
// CachedWriteTokenDetails was dropped in the conversion, so 1h cache writes billed at
// the cheaper 5m rate.
func TestCalculateCostForLogPricesOneHourCacheWrites(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const promptTokens, cachedWriteTokens, completionTokens = 20_000, 20_000, 100
	details := &schemas.ChatPromptTokensDetails{
		CachedWriteTokens: cachedWriteTokens,
		CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
			CachedWriteTokens1h: cachedWriteTokens,
		},
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		TotalTokens:         promptTokens + completionTokens,
		PromptTokensDetails: details,
	}

	want := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.Anthropic, Model: "claude-sonnet-4-20250514"},
			},
		},
	}, string(schemas.Anthropic))

	fiveMinuteOnly := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:        promptTokens,
				CompletionTokens:    completionTokens,
				TotalTokens:         promptTokens + completionTokens,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedWriteTokens: cachedWriteTokens},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.Anthropic, Model: "claude-sonnet-4-20250514"},
			},
		},
	}, string(schemas.Anthropic))
	if math.Abs(want-fiveMinuteOnly) <= costEpsilon {
		t.Fatal("test datasheet must price 1h cache writes differently from 5m for this test to mean anything")
	}

	entry := &logstore.Log{
		ID:               "req-1h-cache",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ResponsesRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		TokenUsageParsed: usage,
	}

	got, err := plugin.calculateCostForLog(entry)
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	assertCostsEqual(t, "1h cache-write responses request", got, want)
}

// TestCalculateCostForLogUsesServerSideFallbackModel covers the routing leak.
// resolvePricing ranks ServerSideFallbackModel ahead of every other candidate because
// the tokens belong to the model that actually ran, but recomputation never put it on
// RoutingInfo — so a fallback-served turn was priced at the requested model's rates.
func TestCalculateCostForLogUsesServerSideFallbackModel(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const promptTokens, completionTokens = 1_000, 200
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}

	// The row was requested as gpt-4o but Anthropic-style server-side fallback served
	// a different model; pricing must follow the serving model.
	served := "gpt-4o-mini"
	want := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{
					Provider:                schemas.OpenAI,
					Model:                   "gpt-4o",
					ServerSideFallbackModel: &served,
				},
			},
		},
	}, string(schemas.OpenAI))

	requestedModelCost := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
			},
		},
	}, string(schemas.OpenAI))
	if math.Abs(want-requestedModelCost) <= costEpsilon {
		t.Fatal("gpt-4o and gpt-4o-mini must price differently for this test to mean anything")
	}

	entry := &logstore.Log{
		ID:                      "req-ssf",
		Timestamp:               time.Now().UTC(),
		Object:                  string(schemas.ChatCompletionRequest),
		Provider:                string(schemas.OpenAI),
		Model:                   "gpt-4o",
		Status:                  "success",
		ServerSideFallbackModel: &served,
		PromptTokens:            promptTokens,
		CompletionTokens:        completionTokens,
		TotalTokens:             promptTokens + completionTokens,
		TokenUsageParsed:        usage,
	}

	got, err := plugin.calculateCostForLog(entry)
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	assertCostsEqual(t, "server-side-fallback-served request", got, want)
}

// TestRecalculateCostsCountsUnpriceableSeparately checks the job-level accounting: a
// row whose pricing inputs cannot be recovered must be left untouched and reported as
// unpriceable, not merged into the ordinary skip count. Writing a wrong number is the
// failure mode this whole change exists to prevent, so the count has to be visible.
func TestRecalculateCostsCountsUnpriceableSeparately(t *testing.T) {
	plugin := newCostFidelityPlugin(t)
	store := plugin.store
	ctx := context.Background()

	now := time.Now().UTC()

	// Priceable row.
	if err := store.Create(ctx, testRecalculateCostLog("req-priceable", now, 100, 50)); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Row that looks offloaded: denormalized counters but no token_usage payload.
	if err := store.Create(ctx, &logstore.Log{
		ID:               "req-unpriceable",
		Timestamp:        now.Add(time.Second),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		HasObject:        true,
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		CachedReadTokens: 70_000,
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	result, err := plugin.RecalculateCosts(ctx, logstore.SearchFilters{}, 10)
	if err != nil {
		t.Fatalf("RecalculateCosts() error = %v", err)
	}
	if result.Updated != 1 {
		t.Fatalf("expected exactly the priceable row to update, got %+v", result)
	}
	if result.Unpriceable != 1 {
		t.Fatalf("expected 1 unpriceable row, got %+v", result)
	}

	unpriceable, err := store.FindByID(ctx, "req-unpriceable")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if unpriceable.Cost != nil {
		t.Fatalf("expected an unpriceable row to keep a nil cost rather than a wrong one, got %v", *unpriceable.Cost)
	}
}

// legacyOffloadedStore emulates rows written while token_usage was offloaded to object
// storage: the DB row carries only the denormalized counters, and the real payload comes
// back through HydrateBillingChunk. This is the shape that produced the ~3x overbill.
//
// plugins/logging cannot build a real HybridLogStore — newHybridLogStore and the
// in-memory object store are unexported, and the only real backends are s3/gcs — so the
// store contract is emulated here and the genuine hydration is covered by
// TestHybrid_SearchLogsForBillingHydratesCacheBreakdown in the logstore package.
type legacyOffloadedStore struct {
	logstore.LogStore
	rows     []logstore.Log
	payloads map[string]string // id -> the real token_usage JSON, as if held in S3
	costs    map[string]float64
	fetches  int
	// missing marks IDs whose object cannot be fetched, so the row stays degraded.
	missing    map[string]struct{}
	backfilled map[string]logstore.BillingPayloadBackfill
}

func (s *legacyOffloadedStore) SearchLogsForBilling(_ context.Context, _ logstore.SearchFilters, _ logstore.PaginationOptions) (*logstore.SearchResult, error) {
	page := make([]logstore.Log, len(s.rows))
	copy(page, s.rows)
	for i := range page {
		// AfterFind runs on a real store; here it is explicit. With token_usage blank
		// this builds the lossy stub and marks the row degraded.
		if err := page[i].DeserializeFields(); err != nil {
			return nil, err
		}
	}
	return &logstore.SearchResult{
		Logs:  page,
		Stats: logstore.SearchStats{TotalRequests: int64(len(page))},
	}, nil
}

// SearchLogs backs the count-only "how many still have no cost" query the recalc runs
// after its loop. It deliberately uses the cheap list projection there, so this needs
// to serve Stats.TotalRequests and nothing more.
func (s *legacyOffloadedStore) SearchLogs(_ context.Context, f logstore.SearchFilters, _ logstore.PaginationOptions) (*logstore.SearchResult, error) {
	var remaining int64
	for _, r := range s.rows {
		if f.MissingCostOnly && s.costs[r.ID] > 0 {
			continue
		}
		remaining++
	}
	return &logstore.SearchResult{Stats: logstore.SearchStats{TotalRequests: remaining}}, nil
}

func (s *legacyOffloadedStore) HydrateBillingChunk(_ context.Context, logs []*logstore.Log) (logstore.BillingHydrationResult, error) {
	var result logstore.BillingHydrationResult
	for _, l := range logs {
		// Mirrors the real gate: a row that already carries its usage needs no fetch.
		if l == nil || !l.HasObject || l.TokenUsage != "" {
			continue
		}
		if _, gone := s.missing[l.ID]; gone {
			result.Unpriceable = append(result.Unpriceable, l.ID)
			continue
		}
		s.fetches++
		l.TokenUsage = s.payloads[l.ID]
		if err := l.DeserializeFields(); err != nil {
			return result, err
		}
		result.Hydrated = append(result.Hydrated, l.ID)
	}
	return result, nil
}

func (s *legacyOffloadedStore) BulkUpdateCost(_ context.Context, updates map[string]logstore.CostUpdate) error {
	for id, c := range updates {
		s.costs[id] = c.Total
	}
	return nil
}

// BulkBackfillBillingPayloads records the write-back so a test can assert that a
// recovered payload lands in the row, making the next recompute fetch-free.
func (s *legacyOffloadedStore) BulkBackfillBillingPayloads(_ context.Context, updates map[string]logstore.BillingPayloadBackfill) error {
	if s.backfilled == nil {
		s.backfilled = map[string]logstore.BillingPayloadBackfill{}
	}
	for id, b := range updates {
		s.backfilled[id] = b
		// Mirror what the DB would now hold, so a second pass sees the backfilled row.
		for i := range s.rows {
			if s.rows[i].ID == id {
				s.rows[i].TokenUsage = b.TokenUsage
				s.rows[i].CacheDebug = b.CacheDebug
			}
		}
	}
	return nil
}

func TestPriceLogsInChunksBackfillsContentHiddenPricingData(t *testing.T) {
	plugin := newCostFidelityPlugin(t)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     100,
		CompletionTokens: 10,
		TotalTokens:      110,
	}
	serialized := &logstore.Log{TokenUsageParsed: usage}
	require.NoError(t, serialized.SerializeFields())

	batch := []logstore.Log{{
		ID:               "hidden-retained",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.OpenAI),
		Model:            "gpt-4o",
		Status:           "success",
		HasObject:        true,
		ContentHidden:    true,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
	}}
	require.NoError(t, batch[0].DeserializeFields())

	store := &legacyOffloadedStore{
		payloads: map[string]string{"hidden-retained": serialized.TokenUsage},
		costs:    map[string]float64{},
		missing:  map[string]struct{}{},
	}
	plugin.store = store

	outcomes, err := plugin.priceLogsInChunks(context.Background(), batch)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.NoError(t, outcomes[0].err)
	require.Positive(t, outcomes[0].cost)
	require.Contains(t, store.backfilled, "hidden-retained")
	require.Equal(t, serialized.TokenUsage, store.backfilled["hidden-retained"].TokenUsage,
		"pricing metadata belongs in log_store even when content is hidden")
	require.Empty(t, batch[0].TokenUsage, "hydrated payload must be released after pricing")
}

// TestRecalculateCostsOnLegacyOffloadedRowMatchesLiveCost is the end-to-end regression
// test for the reported bug: a cache-heavy row whose token_usage lives only in object
// storage must reprice to exactly what it was billed live, not to the ~3x that pricing
// the denormalized stub produces.
func TestRecalculateCostsOnLegacyOffloadedRowMatchesLiveCost(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const (
		promptTokens      = 100_000
		cachedReadTokens  = 70_000
		cachedWriteTokens = 10_000
		completionTokens  = 500
	)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  cachedReadTokens,
			CachedWriteTokens: cachedWriteTokens,
		},
	}

	want := liveCost(t, plugin, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.Anthropic, Model: "claude-sonnet-4-20250514"},
			},
		},
	}, string(schemas.Anthropic))
	require.Greater(t, want, 0.0, "live cost must be positive for the comparison to mean anything")

	// Serialize once to get the payload S3 would hold and the counters the DB keeps.
	serialized := &logstore.Log{TokenUsageParsed: usage}
	require.NoError(t, serialized.SerializeFields())

	row := logstore.Log{
		ID:               "legacy-offloaded",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		HasObject:        true,
		TokenUsage:       "", // blanked at write time; the payload is in object storage
		PromptTokens:     serialized.PromptTokens,
		CompletionTokens: serialized.CompletionTokens,
		TotalTokens:      serialized.TotalTokens,
		CachedReadTokens: serialized.CachedReadTokens,
	}

	store := &legacyOffloadedStore{
		rows:     []logstore.Log{row},
		payloads: map[string]string{"legacy-offloaded": serialized.TokenUsage},
		costs:    map[string]float64{},
		missing:  map[string]struct{}{},
	}
	plugin.store = store

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 10)
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated, "the row must be repriced, not skipped: %+v", result)
	require.Zero(t, result.Unpriceable)
	require.Equal(t, 1, store.fetches, "the offloaded payload must be fetched exactly once")

	assertCostsEqual(t, "legacy offloaded cache-heavy row", store.costs["legacy-offloaded"], want)

	priceUsage := func(u *schemas.BifrostLLMUsage) float64 {
		return liveCost(t, plugin, &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				Usage: u,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: schemas.ChatCompletionRequest,
					RoutingInfo: schemas.RoutingInfo{Provider: schemas.Anthropic, Model: "claude-sonnet-4-20250514"},
				},
			},
		}, string(schemas.Anthropic))
	}

	// Prove hydration is load-bearing: the stub the row arrives with prices to a
	// different number, so skipping the fetch would mis-bill.
	//
	// Note the direction. Now that cached_read_tokens is in the projection the stub
	// keeps the large cheap bucket and loses only the cache-write total, so it
	// *under*-bills by the write-rate delta. That is a much smaller error than the
	// reported one — which is the point of the assertion below it.
	stub := row
	require.NoError(t, stub.DeserializeFields())
	require.True(t, stub.IsUsageDegraded())
	stubCost := priceUsage(stub.TokenUsageParsed)
	require.Greater(t, math.Abs(stubCost-want), costEpsilon,
		"pricing the un-hydrated stub must differ from the true cost, or hydration would be pointless")

	// And reproduce the reported ~3x: that required NO cache detail at all, which is
	// what the old projection produced by never selecting cached_read_tokens. Cache
	// reads are the biggest bucket at a tenth of the input rate, so losing them is
	// what turned a mis-bill into a multiple.
	cacheBlind := priceUsage(&schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	})
	require.Greater(t, cacheBlind, want*1.5,
		"a fully cache-blind price should be a multiple of the true cost; that gap is the reported bug")
}

// TestRecalculateCostsLeavesUnfetchableLegacyRowAlone covers the other half: when the
// object cannot be read, the row must keep its existing cost rather than be repriced
// from the stub.
func TestRecalculateCostsLeavesUnfetchableLegacyRowAlone(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	row := logstore.Log{
		ID:               "legacy-lost",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		HasObject:        true,
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		CachedReadTokens: 70_000,
	}
	store := &legacyOffloadedStore{
		rows:     []logstore.Log{row},
		payloads: map[string]string{},
		costs:    map[string]float64{},
		missing:  map[string]struct{}{"legacy-lost": {}},
	}
	plugin.store = store

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 10)
	require.NoError(t, err)
	assert.Zero(t, result.Updated, "an unfetchable row must not be repriced from the stub")
	assert.Equal(t, 1, result.Unpriceable)
	assert.Empty(t, store.costs, "no cost may be written for a row we cannot price")
}

// TestRecalculateCostsRecordsZeroForUnfetchableDirectCacheHit covers the second place a
// row can be rejected before pricing: the hydration blocklist in priceLogsInChunks,
// which short-circuits before calculateCostForLog ever runs.
//
// A direct cache hit stays provably free even when its object is gone, because hit_type
// lives in the cache_debug DB column rather than the offloaded payload. Without the
// known-zero check in that branch the row is counted unpriceable and left with a nil
// cost, so every MissingCostOnly pass reattempts the same dead fetch forever.
func TestRecalculateCostsRecordsZeroForUnfetchableDirectCacheHit(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	row := logstore.Log{
		ID:               "legacy-lost-hit",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.Anthropic),
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		HasObject:        true,
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		CachedReadTokens: 70_000,
		CacheDebug:       `{"cache_hit":true,"hit_type":"direct"}`,
	}
	store := &legacyOffloadedStore{
		rows:     []logstore.Log{row},
		payloads: map[string]string{},
		costs:    map[string]float64{},
		missing:  map[string]struct{}{"legacy-lost-hit": {}},
	}
	plugin.store = store

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Updated, "a provably-free row must be closed out, not left unpriced")
	assert.Zero(t, result.Unpriceable, "cost is knowable here, so the row is not unpriceable")
	cost, ok := store.costs["legacy-lost-hit"]
	require.True(t, ok, "the zero must actually be written so MissingCostOnly stops revisiting")
	assert.Zero(t, cost)
}

// TestRecalculateCostsBackfillsRecoveredUsageSoSecondRunSkipsObjectStore is the payoff
// of the write-back: recompute is self-healing.
//
// The first pass over a window pays one object fetch per offloaded row and writes the
// small pricing inputs into the row. Every pass after it reads them from the database,
// so the object store is not touched at all — and the cost comes out identical.
func TestRecalculateCostsBackfillsRecoveredUsageSoSecondRunSkipsObjectStore(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  70_000,
			CachedWriteTokens: 10_000,
		},
	}
	serialized := &logstore.Log{TokenUsageParsed: usage}
	require.NoError(t, serialized.SerializeFields())

	store := &legacyOffloadedStore{
		rows: []logstore.Log{{
			ID:               "backfill-1",
			Timestamp:        time.Now().UTC(),
			Object:           string(schemas.ChatCompletionRequest),
			Provider:         string(schemas.Anthropic),
			Model:            "claude-sonnet-4-20250514",
			Status:           "success",
			HasObject:        true,
			PromptTokens:     serialized.PromptTokens,
			CompletionTokens: serialized.CompletionTokens,
			TotalTokens:      serialized.TotalTokens,
			CachedReadTokens: serialized.CachedReadTokens,
		}},
		payloads: map[string]string{"backfill-1": serialized.TokenUsage},
		costs:    map[string]float64{},
		missing:  map[string]struct{}{},
	}
	plugin.store = store

	first, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 10)
	require.NoError(t, err)
	require.Equal(t, 1, first.Updated)
	require.Equal(t, 1, store.fetches, "the first pass must fetch the offloaded payload")

	// The recovered usage was written back to the row.
	require.Contains(t, store.backfilled, "backfill-1",
		"a fetched payload must be persisted, or every future run refetches it")
	assert.Equal(t, serialized.TokenUsage, store.backfilled["backfill-1"].TokenUsage)

	firstCost := store.costs["backfill-1"]
	require.Greater(t, firstCost, 0.0)

	// Second pass over the same window: same answer, no object store involvement.
	store.costs = map[string]float64{}
	second, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 10)
	require.NoError(t, err)
	require.Equal(t, 1, second.Updated)
	assert.Equal(t, 1, store.fetches,
		"the second pass must be served entirely from the database; fetches should still be 1")
	assertCostsEqual(t, "recompute after backfill", store.costs["backfill-1"], firstCost)
}

// These tests cover calculateBatchAggregateCost and its wiring into
// RecalculateCosts: a batch aggregate row (object=batch_results) spanning more
// than one model has Model="mixed" and one blended token_usage, so the generic
// calculateCostForLog path can never price it — "mixed" has no catalog entry.
// calculateBatchAggregateCost reprices per model_breakdown entry instead, the
// same way settlement originally priced each result item.

func batchModelBreakdown(promptTokens, completionTokens int) schemas.BatchModelBreakdown {
	return schemas.BatchModelBreakdown{
		RequestCount: 1,
		Usage: schemas.BifrostLLMUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
}

// TestCalculateBatchAggregateCost_MultiModel prices two models in one row —
// gpt-4o (1.25e-06/5e-06 batch rate) and gpt-4o-mini (7.5e-08/3e-07) in
// testdata/pricing.json. A batch is inherently single-provider (one provider's
// batch API), so both models share the row's Provider; the defaultBatchPricingRatio
// fallback itself is already covered directly at the pricing-engine level
// (TestCalculateCostForUsage_BatchResultsDefaultsToHalfStandardRate) and at the
// settlement level (TestBatchPricing_ClaudeHaiku45_DefaultsToHalfStandardRate) —
// this test's job is the multi-model loop and per-model write-back, not re-proving
// that math. Both models' costs must land in the summed total and in their own
// ModelBreakdown entry.
func TestCalculateBatchAggregateCost_MultiModel(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	entry := &logstore.Log{
		ID:       "batch-multi",
		Object:   string(schemas.BatchResultsRequest),
		Provider: string(schemas.OpenAI),
		Model:    "mixed",
		BatchDebugParsed: &schemas.BifrostBatchDebug{
			BatchID: "batch_multi",
			Accounting: &schemas.BatchAccountingDebug{
				ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
					"gpt-4o":      batchModelBreakdown(1000, 200),
					"gpt-4o-mini": batchModelBreakdown(500, 100),
				},
			},
		},
	}

	cost, batchDebugJSON, err := plugin.calculateBatchAggregateCost(entry)
	require.NoError(t, err)
	assert.NotEmpty(t, batchDebugJSON)

	wantGPT4o := 1000*1.25e-06 + 200*5e-06
	wantGPT4oMini := 500*7.5e-08 + 100*3e-07
	assertCostsEqual(t, "batch multi-model total", cost, wantGPT4o+wantGPT4oMini)

	breakdowns := entry.BatchDebugParsed.Accounting.ModelBreakdowns
	require.NotNil(t, breakdowns["gpt-4o"].Cost)
	assertCostsEqual(t, "gpt-4o breakdown cost", *breakdowns["gpt-4o"].Cost, wantGPT4o)
	require.NotNil(t, breakdowns["gpt-4o-mini"].Cost)
	assertCostsEqual(t, "gpt-4o-mini breakdown cost", *breakdowns["gpt-4o-mini"].Cost, wantGPT4oMini)

	// The returned JSON is what gets persisted to batch_debug — it must reflect
	// the same mutated breakdowns, not a stale pre-pricing snapshot.
	var roundTripped schemas.BifrostBatchDebug
	require.NoError(t, sonic.UnmarshalString(batchDebugJSON, &roundTripped))
	require.NotNil(t, roundTripped.Accounting.ModelBreakdowns["gpt-4o"].Cost)
	assertCostsEqual(t, "round-tripped gpt-4o cost", *roundTripped.Accounting.ModelBreakdowns["gpt-4o"].Cost, wantGPT4o)
}

// TestCalculateBatchAggregateCost_PartiallyUnpriced covers a batch where one
// model prices and another has no catalog entry at all (not even a standard
// rate to default from): the total must reflect only the priced model, and the
// unpriced model's breakdown Cost must stay nil rather than being reported as a
// real (and wrong) zero.
func TestCalculateBatchAggregateCost_PartiallyUnpriced(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	entry := &logstore.Log{
		ID:       "batch-partial",
		Object:   string(schemas.BatchResultsRequest),
		Provider: string(schemas.OpenAI),
		Model:    "mixed",
		BatchDebugParsed: &schemas.BifrostBatchDebug{
			BatchID: "batch_partial",
			Accounting: &schemas.BatchAccountingDebug{
				ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
					"gpt-4o":                batchModelBreakdown(1000, 200),
					"totally-unknown-model": batchModelBreakdown(300, 50),
				},
			},
		},
	}

	cost, _, err := plugin.calculateBatchAggregateCost(entry)
	require.NoError(t, err)
	assertCostsEqual(t, "partially-unpriced batch total", cost, 1000*1.25e-06+200*5e-06)

	breakdowns := entry.BatchDebugParsed.Accounting.ModelBreakdowns
	require.NotNil(t, breakdowns["gpt-4o"].Cost)
	assert.Nil(t, breakdowns["totally-unknown-model"].Cost,
		"a model with no pricing at all must stay nil, not be reported as free")
}

// TestCalculateBatchAggregateCost_EmbeddingEndpointUsesEmbeddingRates covers the
// modality the row itself cannot express: Object is "batch_results" for every
// batch and normalizes to "chat", so an embeddings batch used to look for chat
// rates on an embedding-only model and could never reprice at all. The endpoint
// settlement recorded in batch_debug is what makes the lookup land.
func TestCalculateBatchAggregateCost_EmbeddingEndpointUsesEmbeddingRates(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	newEntry := func(endpoint string) *logstore.Log {
		return &logstore.Log{
			ID:       "batch-embeddings",
			Object:   string(schemas.BatchResultsRequest),
			Provider: string(schemas.OpenAI),
			Model:    "text-embedding-3-small",
			BatchDebugParsed: &schemas.BifrostBatchDebug{
				BatchID:  "batch_embeddings",
				Endpoint: endpoint,
				Accounting: &schemas.BatchAccountingDebug{
					ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
						"text-embedding-3-small": batchModelBreakdown(1000, 0),
					},
				},
			},
		}
	}

	cost, _, err := plugin.calculateBatchAggregateCost(newEntry(string(schemas.BatchEndpointEmbeddings)))
	require.NoError(t, err)
	// testdata input_cost_per_token_batches for text-embedding-3-small: 1e-08.
	assertCostsEqual(t, "embeddings batch total", cost, 1000*1e-08)

	// Rows written before the endpoint was persisted keep the old behavior rather
	// than being guessed at — for an embedding-only model that still means unpriceable.
	_, _, err = plugin.calculateBatchAggregateCost(newEntry(""))
	require.ErrorIs(t, err, errPricingInputsUnavailable)
}

// TestCalculateBatchAggregateCost_PartialRepriceMarksIncomplete pins the agreement
// between settlement and recalculation: recovering some of a batch's cost is worth
// persisting, but the total must not present itself as whole while a model is still
// rateless. The flag is the durable record that it under-states.
func TestCalculateBatchAggregateCost_PartialRepriceMarksIncomplete(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	entry := &logstore.Log{
		ID:       "batch-still-partial",
		Object:   string(schemas.BatchResultsRequest),
		Provider: string(schemas.OpenAI),
		Model:    "mixed",
		BatchDebugParsed: &schemas.BifrostBatchDebug{
			BatchID: "batch_still_partial",
			Accounting: &schemas.BatchAccountingDebug{
				ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
					"gpt-4o":                batchModelBreakdown(1000, 200),
					"totally-unknown-model": batchModelBreakdown(300, 50),
				},
			},
		},
	}

	_, batchDebugJSON, err := plugin.calculateBatchAggregateCost(entry)
	require.NoError(t, err)
	assert.True(t, entry.BatchDebugParsed.Accounting.Incomplete)

	var roundTripped schemas.BifrostBatchDebug
	require.NoError(t, sonic.UnmarshalString(batchDebugJSON, &roundTripped))
	assert.True(t, roundTripped.Accounting.Incomplete,
		"the flag must survive into the persisted batch_debug, not just the in-memory copy")
}

// TestCalculateBatchAggregateCost_IncompleteIsNeverCleared: the flag can stand for
// usage that is not in ModelBreakdowns at all — rows whose model the provider never
// named, rows lost to parse errors — and repricing the breakdowns that did land says
// nothing about those. A pass that priced everything it can see must not close it.
func TestCalculateBatchAggregateCost_IncompleteIsNeverCleared(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	entry := &logstore.Log{
		ID:       "batch-parse-errors",
		Object:   string(schemas.BatchResultsRequest),
		Provider: string(schemas.OpenAI),
		Model:    "gpt-4o",
		BatchDebugParsed: &schemas.BifrostBatchDebug{
			BatchID: "batch_parse_errors",
			Accounting: &schemas.BatchAccountingDebug{
				ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
					"gpt-4o": batchModelBreakdown(1000, 200),
				},
				ParseErrorCount: 3,
				Incomplete:      true,
			},
		},
	}

	_, _, err := plugin.calculateBatchAggregateCost(entry)
	require.NoError(t, err)
	assert.True(t, entry.BatchDebugParsed.Accounting.Incomplete,
		"unparseable rows are gone for good; a successful reprice does not recover them")
	assert.Equal(t, 3, entry.BatchDebugParsed.Accounting.ParseErrorCount)
}

// TestCalculateBatchAggregateCost_AllUnpriced covers a batch where nothing can
// price: it must report errPricingInputsUnavailable, the same sentinel every
// other genuinely-unpriceable row reports, so the recalc job's Unpriceable
// counter (not just Skipped) reflects it.
func TestCalculateBatchAggregateCost_AllUnpriced(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	entry := &logstore.Log{
		ID:       "batch-none",
		Object:   string(schemas.BatchResultsRequest),
		Provider: string(schemas.OpenAI),
		Model:    "totally-unknown-model",
		BatchDebugParsed: &schemas.BifrostBatchDebug{
			BatchID: "batch_none",
			Accounting: &schemas.BatchAccountingDebug{
				ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
					"totally-unknown-model": batchModelBreakdown(300, 50),
				},
			},
		},
	}

	_, _, err := plugin.calculateBatchAggregateCost(entry)
	require.ErrorIs(t, err, errPricingInputsUnavailable)
}

// TestRecalculateCostsReprisesMixedModelBatchRow is the end-to-end regression
// test: a real "mixed"-model batch row, stuck with cost=nil because Model="mixed"
// has no catalog entry, must come out of RecalculateCosts with both the row's
// cost and its batch_debug model_breakdowns correctly populated — not just one
// of the two, which would leave them permanently disagreeing.
func TestRecalculateCostsReprisesMixedModelBatchRow(t *testing.T) {
	plugin := newCostFidelityPlugin(t)
	store := plugin.store

	entry := &logstore.Log{
		ID:        "batch-e2e-mixed",
		Timestamp: time.Now().UTC(),
		Object:    string(schemas.BatchResultsRequest),
		Provider:  string(schemas.OpenAI),
		Model:     "mixed",
		Status:    "success",
		Cost:      nil,
		BatchDebugParsed: &schemas.BifrostBatchDebug{
			BatchID: "batch_e2e_mixed",
			Accounting: &schemas.BatchAccountingDebug{
				ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
					"gpt-4o":      batchModelBreakdown(1000, 200),
					"gpt-4o-mini": batchModelBreakdown(500, 100),
				},
			},
		},
	}
	require.NoError(t, store.Create(context.Background(), entry))

	result, err := plugin.RecalculateCosts(context.Background(), logstore.SearchFilters{}, 10)
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)

	logged, err := store.FindByID(context.Background(), "batch-e2e-mixed")
	require.NoError(t, err)
	require.NotNil(t, logged.Cost)

	wantGPT4o := 1000*1.25e-06 + 200*5e-06
	wantGPT4oMini := 500*7.5e-08 + 100*3e-07
	assertCostsEqual(t, "e2e mixed-model row cost", *logged.Cost, wantGPT4o+wantGPT4oMini)

	require.NotNil(t, logged.BatchDebugParsed)
	require.NotNil(t, logged.BatchDebugParsed.Accounting)
	breakdowns := logged.BatchDebugParsed.Accounting.ModelBreakdowns
	require.NotNil(t, breakdowns["gpt-4o"].Cost)
	assertCostsEqual(t, "e2e gpt-4o breakdown cost", *breakdowns["gpt-4o"].Cost, wantGPT4o)
	require.NotNil(t, breakdowns["gpt-4o-mini"].Cost)
	assertCostsEqual(t, "e2e gpt-4o-mini breakdown cost", *breakdowns["gpt-4o-mini"].Cost, wantGPT4oMini)
}
