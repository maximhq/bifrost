package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Object fetches are the expensive part of a recompute: the object store exposes only
// whole-key Get, so recovering ~300 bytes of counters downloads the entire payload,
// message histories and raw bodies included. These tests pin exactly when that happens.
//
// The rule being enforced: fetch if and only if the row is actually missing a pricing
// input that this request type bills on.

// countingObjectStore records every Get so a test can assert the exact number of
// fetches rather than merely observing the end state.
type countingObjectStore struct {
	*objectstore.InMemoryObjectStore
	gets []string
}

func (s *countingObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.gets = append(s.gets, key)
	return s.InMemoryObjectStore.Get(ctx, key)
}

func newCountingHybrid(t *testing.T, excludeFields []string) (*HybridLogStore, LogStore, *countingObjectStore) {
	t.Helper()
	inner, err := newSqliteLogStore(
		context.Background(),
		&SQLiteConfig{Path: filepath.Join(t.TempDir(), "gate.db")},
		hybridTestLogger{},
	)
	require.NoError(t, err)
	objStore := &countingObjectStore{InMemoryObjectStore: objectstore.NewInMemoryObjectStore()}
	hybrid := newHybridLogStore(inner, objStore, "test", hybridTestLogger{}, excludeFields)
	return hybrid, inner, objStore
}

// TestBillingRowNeedsHydration is the decision table for the gate. Each case is a row
// shape a real deployment produces.
func TestBillingRowNeedsHydration(t *testing.T) {
	const usageJSON = `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`
	const cacheJSON = `{"cache_hit":false}`

	excludedSet := func(fields ...string) map[string]struct{} {
		set := make(map[string]struct{}, len(fields))
		for _, f := range fields {
			set[f] = struct{}{}
		}
		return set
	}

	cases := []struct {
		name     string
		log      Log
		excluded map[string]struct{}
		want     bool
		why      string
	}{
		{
			name: "everything resident on a chat row",
			log:  Log{Object: "chat.completion", TokenUsage: usageJSON, CacheDebug: cacheJSON},
			want: false,
			why:  "nothing is missing, so a fetch would recover nothing",
		},
		{
			name: "token_usage offloaded",
			log:  Log{Object: "chat.completion", TokenUsage: "", CacheDebug: cacheJSON},
			want: true,
			why:  "the cache breakdown is the field whose absence caused the overbill",
		},
		{
			name: "cache_debug empty beside a recovered token_usage",
			log:  Log{Object: "chat.completion", TokenUsage: usageJSON, CacheDebug: ""},
			want: false,
			why: "offloading blanks token_usage, so its presence means a prior pass already " +
				"fetched and backfilled this row; the empty cache_debug beside it is the real answer",
		},
		{
			name:     "cache_debug offloadable while token_usage is config-resident",
			log:      Log{Object: "chat.completion", TokenUsage: usageJSON, CacheDebug: ""},
			excluded: excludedSet("token_usage"),
			want:     true,
			why: "here token_usage is present only because of config, so it vouches for nothing; " +
				"an offloaded semantic-cache hit priced without cache_debug bills $0 as full price",
		},
		{
			name:     "cache_debug empty but configured DB-resident",
			log:      Log{Object: "chat.completion", TokenUsage: usageJSON, CacheDebug: ""},
			excluded: excludedSet("cache_debug"),
			want:     false,
			why:      "the column never leaves the DB, so empty is the real answer and the object holds nothing to recover",
		},
		{
			name: "already hydrated",
			log:  Log{Object: "chat.completion", TokenUsage: usageJSON, CacheDebug: "", billingPayloadsHydrated: true},
			want: false,
			why:  "the object was already read; re-asking returns the same thing and would refetch forever",
		},
		{
			name: "speech row missing its own payload",
			log:  Log{Object: "speech", TokenUsage: usageJSON, CacheDebug: cacheJSON},
			want: true,
			why:  "TTS bills per input character, which only speech_output carries",
		},
		{
			name: "speech row with its payload resident",
			log:  Log{Object: "speech", TokenUsage: usageJSON, CacheDebug: cacheJSON, SpeechOutput: `{"usage":{"input_chars":5}}`},
			want: false,
			why:  "the per-character input is already present",
		},
		{
			name: "chat row missing speech_output",
			log:  Log{Object: "chat.completion", TokenUsage: usageJSON, CacheDebug: cacheJSON},
			want: false,
			why:  "a chat row does not bill on speech_output, so its absence is not a gap",
		},
		{
			name: "ocr row missing its own payload",
			log:  Log{Object: "ocr", TokenUsage: usageJSON, CacheDebug: cacheJSON},
			want: true,
			why:  "OCR bills per page processed, which only ocr_output carries",
		},
		{
			name: "image row missing its own payload",
			log:  Log{Object: "image_generation", TokenUsage: usageJSON, CacheDebug: cacheJSON},
			want: true,
			why:  "per-image pricing needs the image count from image_generation_output",
		},
		{
			name: "video row with its payload resident",
			log:  Log{Object: "video_generation", TokenUsage: usageJSON, CacheDebug: cacheJSON, VideoGenerationOutput: `{"seconds":"8"}`},
			want: false,
			why:  "the billed duration is already present",
		},
		// Content-hidden rows are written by a different code path (ClearPayload, which
		// ignores the exclusion list) so the exclusion set says nothing about them.
		{
			name: "content-hidden chat row that kept its pricing metadata",
			log:  Log{Object: "chat.completion", ContentHidden: true, TokenUsage: usageJSON, CacheDebug: cacheJSON},
			want: false,
			why:  "hiding content does not strip pricing inputs, so there is nothing in the object to recover",
		},
		{
			name: "legacy content-hidden chat row",
			log:  Log{Object: "chat.completion", ContentHidden: true, TokenUsage: "", CacheDebug: ""},
			want: true,
			why:  "written before pricing metadata was retained, so the object is the only source",
		},
		{
			name:     "content-hidden chat row with an empty cache_debug while token_usage is config-resident",
			log:      Log{Object: "chat.completion", ContentHidden: true, TokenUsage: usageJSON, CacheDebug: ""},
			excluded: excludedSet("token_usage"),
			want:     false,
			why: "the exclusion set does not govern hidden rows: token_usage is present because " +
				"prepareDBEntry restored it alongside cache_debug, so the empty cache_debug is the real " +
				"answer and treating config as decisive would refetch this row on every pass forever",
		},
		{
			name:     "content-hidden speech row whose payload column is configured resident",
			log:      Log{Object: "speech", ContentHidden: true, TokenUsage: usageJSON, CacheDebug: cacheJSON},
			excluded: excludedSet("speech_output"),
			want:     true,
			why: "hidden rows are cleared unfiltered, so a configured-resident modality column is " +
				"blank anyway; trusting the config here would price the row off a missing payload",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := billingRowNeedsHydration(&tc.log, tc.excluded)
			assert.Equal(t, tc.want, got, tc.why)
		})
	}
}

// TestHydrateBillingChunkFetchesOnlyWhenRequired proves the gate end to end by counting
// real Get calls, across the three reasons a fetch is unnecessary.
func TestHydrateBillingChunkFetchesOnlyWhenRequired(t *testing.T) {
	t.Run("no fetch when the row was never offloaded", func(t *testing.T) {
		hybrid, inner, objStore := newCountingHybrid(t, nil)
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		entry := &Log{
			ID: "inline-1", Timestamp: time.Now().UTC(), Provider: "anthropic",
			Model: "claude-sonnet-4-20250514", Status: "success", Object: "chat.completion",
			TokenUsageParsed: billingTestUsage(),
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, inner.CreateIfNotExists(ctx, entry)) // straight through: has_object stays false

		result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Logs, 1)

		hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
		require.NoError(t, err)
		require.Empty(t, hydration.Unpriceable)
		assert.Empty(t, objStore.gets, "a row that was never offloaded must not be fetched")
	})

	t.Run("no fetch when the exclusion list kept the billing fields in the DB", func(t *testing.T) {
		hybrid, inner, objStore := newCountingHybrid(t, []string{"token_usage", "cache_debug"})
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		entry := &Log{
			ID: "resident-1", Timestamp: time.Now().UTC(), Provider: "anthropic",
			Model: "claude-sonnet-4-20250514", Status: "success", Object: "chat.completion",
			TokenUsageParsed: billingTestUsage(),
			CacheDebugParsed: &schemas.BifrostCacheDebug{CacheHit: false},
			InputHistoryParsed: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("a long prompt")}},
			},
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
		waitForOffload(t, inner, "resident-1")

		result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Logs, 1)
		require.True(t, result.Logs[0].HasObject, "the message history should still have been offloaded")

		hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
		require.NoError(t, err)
		require.Empty(t, hydration.Unpriceable)
		assert.Empty(t, objStore.gets,
			"the row already carries every pricing input; fetching the object would recover nothing")
	})

	// prepareDBEntry keeps token_usage and cache_debug on hidden rows, so a hidden row
	// written by the current code is not a reason to download its payload.
	t.Run("no fetch for a content-hidden row that kept its pricing metadata", func(t *testing.T) {
		hybrid, inner, objStore := newCountingHybrid(t, nil)
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		entry := &Log{
			ID: "hidden-1", Timestamp: time.Now().UTC(), Provider: "anthropic",
			Model: "claude-sonnet-4-20250514", Status: "success", Object: "chat.completion",
			ContentHidden: true, TokenUsageParsed: billingTestUsage(),
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
		waitForOffload(t, inner, "hidden-1")

		result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Logs, 1)
		require.True(t, result.Logs[0].HasObject, "the hidden payload should still have been offloaded")

		hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
		require.NoError(t, err)
		assert.Empty(t, hydration.Unpriceable)
		assert.Empty(t, objStore.gets,
			"hiding content does not remove pricing inputs from the row; fetching the whole payload would recover nothing")
	})

	// Rows written before prepareDBEntry retained pricing metadata: nothing on the row
	// can price them, so the retained object is the only source.
	t.Run("one billing-only fetch for a legacy content-hidden row", func(t *testing.T) {
		hybrid, inner, objStore := newCountingHybrid(t, nil)
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		entry := &Log{
			ID: "hidden-legacy-1", Timestamp: time.Now().UTC(), Provider: "anthropic",
			Model: "claude-sonnet-4-20250514", Status: "success", Object: "chat.completion",
			ContentHidden: true, TokenUsageParsed: billingTestUsage(),
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
		waitForOffload(t, inner, "hidden-legacy-1")
		// Reproduce the old write path, which blanked pricing metadata along with the
		// rest of the payload.
		require.NoError(t, inner.Update(ctx, "hidden-legacy-1", map[string]interface{}{
			"token_usage": "",
		}))

		result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Logs, 1)

		hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
		require.NoError(t, err)
		assert.Empty(t, hydration.Unpriceable)
		assert.Equal(t, []string{"hidden-legacy-1"}, hydration.Hydrated)
		assert.Len(t, objStore.gets, 1,
			"billing may recover retained pricing inputs without changing serving-path visibility")
	})

	t.Run("exactly one fetch when token_usage was offloaded", func(t *testing.T) {
		hybrid, inner, objStore := newCountingHybrid(t, nil)
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		entry := &Log{
			ID: "offloaded-1", Timestamp: time.Now().UTC(), Provider: "anthropic",
			Model: "claude-sonnet-4-20250514", Status: "success", Object: "chat.completion",
			TokenUsageParsed: billingTestUsage(),
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
		waitForOffload(t, inner, "offloaded-1")
		require.NoError(t, inner.Update(ctx, "offloaded-1", map[string]interface{}{
			"token_usage": "",
			"cache_debug": "",
		}))

		result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Logs, 1)
		require.Empty(t, result.Logs[0].TokenUsage, "precondition: token_usage was offloaded")

		hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
		require.NoError(t, err)
		require.Empty(t, hydration.Unpriceable)
		assert.Equal(t, []string{"offloaded-1"}, hydration.Hydrated, "and it must report what it fetched, so the caller can back it up")
		assert.Len(t, objStore.gets, 1, "one fetch, and only one, for a genuinely degraded row")
		assert.False(t, result.Logs[0].IsUsageDegraded(), "and it must have actually recovered the payload")
	})

	t.Run("a second hydration of the same row does not refetch", func(t *testing.T) {
		hybrid, inner, objStore := newCountingHybrid(t, nil)
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		entry := &Log{
			ID: "idem-1", Timestamp: time.Now().UTC(), Provider: "anthropic",
			Model: "claude-sonnet-4-20250514", Status: "success", Object: "chat.completion",
			TokenUsageParsed: billingTestUsage(),
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
		waitForOffload(t, inner, "idem-1")
		require.NoError(t, inner.Update(ctx, "idem-1", map[string]interface{}{
			"token_usage": "",
			"cache_debug": "",
		}))

		result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		row := &result.Logs[0]

		_, err = hybrid.HydrateBillingChunk(ctx, []*Log{row})
		require.NoError(t, err)
		_, err = hybrid.HydrateBillingChunk(ctx, []*Log{row})
		require.NoError(t, err)
		assert.Len(t, objStore.gets, 1,
			"once hydrated the row carries its inputs, so the gate must skip it on a repeat call")
	})
}

// TestBackfillMakesLaterHydrationFetchFree is the convergence property, proven against
// a real hybrid store: after the recovered pricing inputs are written back, a fresh read
// of the same row needs no object fetch at all.
//
// This is what makes recompute self-healing — the first pass pays the fetches, every
// pass after it is served from the database.
func TestBackfillMakesLaterHydrationFetchFree(t *testing.T) {
	hybrid, inner, objStore := newCountingHybrid(t, nil)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &Log{
		ID: "converge-1", Timestamp: time.Now().UTC(), Provider: "anthropic",
		Model: "claude-sonnet-4-20250514", Status: "success", Object: "chat.completion",
		TokenUsageParsed: billingTestUsage(),
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("a long prompt")}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "converge-1")
	require.NoError(t, inner.Update(ctx, "converge-1", map[string]interface{}{
		"token_usage": "",
		"cache_debug": "",
	}))

	// First pass: the row arrives degraded and one fetch recovers it.
	first, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, first.Logs, 1)
	require.Empty(t, first.Logs[0].TokenUsage, "precondition: token_usage was offloaded")

	hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&first.Logs[0]})
	require.NoError(t, err)
	require.Equal(t, []string{"converge-1"}, hydration.Hydrated)
	require.Len(t, objStore.gets, 1)

	// Persist what was recovered, the way the recalc job does.
	require.NoError(t, hybrid.BulkBackfillBillingPayloads(ctx, map[string]BillingPayloadBackfill{
		"converge-1": {TokenUsage: first.Logs[0].TokenUsage, CacheDebug: first.Logs[0].CacheDebug},
	}))

	// Second pass: a completely fresh read of the same row.
	second, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, second.Logs, 1)
	require.NotEmpty(t, second.Logs[0].TokenUsage, "the backfill should have landed in the row")
	assert.False(t, second.Logs[0].IsUsageDegraded(), "and it should parse as a real payload, not a stub")

	hydration, err = hybrid.HydrateBillingChunk(ctx, []*Log{&second.Logs[0]})
	require.NoError(t, err)
	assert.Empty(t, hydration.Hydrated)
	assert.Empty(t, hydration.Unpriceable)
	assert.Len(t, objStore.gets, 1,
		"the second pass must be served from the database; the fetch count must not grow")

	// The full breakdown survived the round trip, which is what pricing depends on.
	details := second.Logs[0].TokenUsageParsed.PromptTokensDetails
	require.NotNil(t, details)
	assert.Equal(t, 70_000, details.CachedReadTokens)
	assert.Equal(t, 10_000, details.CachedWriteTokens)
	require.NotNil(t, details.CachedWriteTokenDetails)
	assert.Equal(t, 4_000, details.CachedWriteTokenDetails.CachedWriteTokens1h)

	// Offloading is not undone: the row still points at its object, and the DB keeps only
	// the last-user-message preview prepareDBEntry leaves behind, not the full history.
	dbRow, err := inner.FindByID(ctx, "converge-1")
	require.NoError(t, err)
	assert.True(t, dbRow.HasObject, "the row must still reference its offloaded payload")
	assert.Equal(t, 1, objStore.Len(), "the object must still hold the full payload")
}
