package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SearchLogs and SearchLogsForBilling read the same rows at deliberately different
// fidelities, and conflating them caused a production overbill.
//
// SearchLogs returns a usage stub rebuilt from denormalized columns — fine for
// rendering token counts in the list, useless for money. PromptTokens is inclusive of
// the cache buckets, so pricing the stub charges every cache-read token at the full
// input rate; on a 70%-cache-read request that is roughly 2.5x, and a user saw ~3x.
//
// SearchLogsForBilling must therefore hydrate the real payload, and must report
// anything it cannot hydrate instead of quietly handing back the stub.

func billingTestUsage() *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{
		PromptTokens:     100_000,
		CompletionTokens: 500,
		TotalTokens:      100_500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  70_000,
			CachedWriteTokens: 10_000,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens1h: 4_000,
			},
		},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: 120,
		},
	}
}

func seedOffloadedBillingLog(t *testing.T, hybrid *HybridLogStore, inner LogStore, id string, contentHidden bool) *Log {
	t.Helper()
	entry := &Log{
		ID:               id,
		Timestamp:        time.Now().UTC(),
		Provider:         "anthropic",
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		Object:           "chat.completion",
		ContentHidden:    contentHidden,
		TokenUsageParsed: billingTestUsage(),
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(context.Background(), entry))
	// Wait for has_object, not just the object: SearchLogsForBilling branches on
	// that flag to decide whether a row needs hydrating, and processUpload commits
	// it only after the Put.
	waitForOffload(t, inner, id)
	// Simulate a legacy row written before pricing metadata became unconditionally
	// DB-resident. The retained object still has the original payload.
	require.NoError(t, inner.Update(context.Background(), id, map[string]interface{}{
		"token_usage": "",
		"cache_debug": "",
	}))
	return entry
}

func TestHybrid_SearchLogsForBillingHydratesCacheBreakdown(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	seedOffloadedBillingLog(t, hybrid, inner, "bill-hydrate-1", false)
	require.Equal(t, 1, objStore.Len())

	// Precondition: the DB row really did lose its payload, so the hydration below is
	// doing the work rather than the column just happening to still be there.
	dbLog, err := inner.FindByID(ctx, "bill-hydrate-1")
	require.NoError(t, err)
	require.Empty(t, dbLog.TokenUsage, "token_usage should be offloaded to object storage")

	// The list path yields the lossy stub and flags itself as such.
	listResult, err := hybrid.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, listResult.Logs, 1)
	stub := listResult.Logs[0]
	require.NotNil(t, stub.TokenUsageParsed)
	assert.True(t, stub.IsUsageDegraded(), "the list rebuild must mark itself unfit for billing")
	assert.Nil(t, stub.TokenUsageParsed.CompletionTokensDetails,
		"the stub cannot carry completion details; only billing hydration recovers them")

	// The billing path recovers every pricing input.
	billingResult, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, billingResult.Logs, 1)
	hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&billingResult.Logs[0]})
	require.NoError(t, err)
	require.Empty(t, hydration.Unpriceable)
	require.Equal(t, []string{"bill-hydrate-1"}, hydration.Hydrated,
		"the store must report what it fetched so the caller can back it up")

	hydrated := billingResult.Logs[0]
	assert.False(t, hydrated.IsUsageDegraded(), "a hydrated row must be priceable")
	require.NotNil(t, hydrated.TokenUsageParsed)
	require.NotNil(t, hydrated.TokenUsageParsed.PromptTokensDetails)
	assert.Equal(t, 70_000, hydrated.TokenUsageParsed.PromptTokensDetails.CachedReadTokens)
	assert.Equal(t, 10_000, hydrated.TokenUsageParsed.PromptTokensDetails.CachedWriteTokens)
	require.NotNil(t, hydrated.TokenUsageParsed.PromptTokensDetails.CachedWriteTokenDetails,
		"the 5m/1h split drives the above-1hr cache-write rate")
	assert.Equal(t, 4_000, hydrated.TokenUsageParsed.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h)
	require.NotNil(t, hydrated.TokenUsageParsed.CompletionTokensDetails)
	assert.Equal(t, 120, hydrated.TokenUsageParsed.CompletionTokensDetails.ReasoningTokens)
}

// TestHybrid_SearchLogsForBillingHydratesContentHiddenForBilling keeps serving reads
// metadata-only while allowing the internal billing path to recover retained pricing
// inputs from object storage.
func TestHybrid_SearchLogsForBillingHydratesContentHiddenForBilling(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	seedOffloadedBillingLog(t, hybrid, inner, "bill-hidden-1", true)
	require.Equal(t, 1, objStore.Len())

	result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
	require.NoError(t, err)
	assert.Empty(t, hydration.Unpriceable)
	assert.Equal(t, []string{"bill-hidden-1"}, hydration.Hydrated)
	assert.False(t, result.Logs[0].IsUsageDegraded())
	require.NotNil(t, result.Logs[0].TokenUsageParsed)
	require.NotNil(t, result.Logs[0].TokenUsageParsed.PromptTokensDetails)
	assert.Equal(t, 70_000, result.Logs[0].TokenUsageParsed.PromptTokensDetails.CachedReadTokens)
}

// TestHybrid_SearchLogsForBillingReportsFetchFailureAsUnpriceable is the important
// difference from hydrateLog, which degrades gracefully on a failed fetch. Graceful
// degradation is right for rendering a message preview and wrong for money: it would
// leave the stub in place and bill it. A lost object must surface as unpriceable.
func TestHybrid_SearchLogsForBillingReportsFetchFailureAsUnpriceable(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	seedOffloadedBillingLog(t, hybrid, inner, "bill-lost-1", false)
	require.Equal(t, 1, objStore.Len())

	// Simulate an object that is gone (lifecycle rule, bucket migration, corruption).
	for _, key := range objStore.Keys() {
		require.NoError(t, objStore.Delete(ctx, key))
	}

	result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
	require.NoError(t, err, "one unreadable object must not fail the whole chunk")
	assert.Equal(t, []string{"bill-lost-1"}, hydration.Unpriceable)
	assert.Empty(t, hydration.Hydrated, "a failed fetch recovered nothing, so there is nothing to back up")
	assert.True(t, result.Logs[0].IsUsageDegraded(),
		"the row must remain flagged so calculateCostForLog refuses it even if the ID list is ignored")
}

// TestHybrid_SearchLogsForBillingLeavesNonOffloadedRowsAlone checks we do not pay for
// object fetches we do not need.
func TestHybrid_SearchLogsForBillingLeavesNonOffloadedRowsAlone(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	// Write straight through the inner store so nothing is offloaded and has_object
	// stays false.
	entry := &Log{
		ID:               "bill-inline-1",
		Timestamp:        time.Now().UTC(),
		Provider:         "anthropic",
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		Object:           "chat.completion",
		TokenUsageParsed: billingTestUsage(),
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, inner.CreateIfNotExists(ctx, entry))

	result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
	require.NoError(t, err)
	require.Empty(t, hydration.Unpriceable)
	assert.False(t, result.Logs[0].IsUsageDegraded())
	require.NotNil(t, result.Logs[0].TokenUsageParsed.PromptTokensDetails)
	assert.Equal(t, 70_000, result.Logs[0].TokenUsageParsed.PromptTokensDetails.CachedReadTokens)
}

// TestHybrid_HydrateBillingChunkSkipsRowsThatNeedNothing pins the skip guard.
//
// A deployment can keep token_usage and cache_debug DB-resident via
// object_storage_exclude_fields. Those rows still have has_object set — their message
// history is offloaded — but they already carry every pricing input, so fetching the
// object would be pure waste. Deleting the object proves no fetch is attempted.
func TestHybrid_HydrateBillingChunkSkipsRowsThatNeedNothing(t *testing.T) {
	hybrid, inner, objStore := newTestHybridWithExclude(t, []string{"token_usage", "cache_debug"})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &Log{
		ID:               "skip-1",
		Timestamp:        time.Now().UTC(),
		Provider:         "anthropic",
		Model:            "claude-sonnet-4-20250514",
		Status:           "success",
		Object:           "chat.completion",
		TokenUsageParsed: billingTestUsage(),
		CacheDebugParsed: &schemas.BifrostCacheDebug{CacheHit: false},
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("a long prompt")}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "skip-1")

	dbRow, err := inner.FindByID(ctx, "skip-1")
	require.NoError(t, err)
	require.True(t, dbRow.HasObject, "the message history should still have been offloaded")
	require.NotEmpty(t, dbRow.TokenUsage, "the exclusion list should have kept token_usage in the DB")

	// Remove the object entirely. A fetch would now fail and mark the row unpriceable,
	// so an empty result proves the guard skipped it.
	for _, key := range objStore.Keys() {
		require.NoError(t, objStore.Delete(ctx, key))
	}

	result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)

	hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
	require.NoError(t, err)
	assert.Empty(t, hydration.Hydrated,
		"a row that already carries its pricing inputs must not be fetched from object storage")
	assert.Empty(t, hydration.Unpriceable,
		"and skipping the fetch must not be mistaken for a failed one")
	assert.False(t, result.Logs[0].IsUsageDegraded())
	require.NotNil(t, result.Logs[0].TokenUsageParsed.PromptTokensDetails)
	assert.Equal(t, 10_000, result.Logs[0].TokenUsageParsed.PromptTokensDetails.CachedWriteTokens)
}

// TestHybrid_HydrateBillingChunkKeepsModalityRowWithoutTokenUsage pins that "carries no
// token_usage" is not the same as "unpriceable".
//
// A TTS row prices off speech_output.usage.input_chars and never reads token_usage at all
// — computeSpeechCost takes the character count, and computeOCRCost takes no usage
// argument whatsoever. Once the modality payload is back the row has every input pricing
// reads, so reporting it Unpriceable would claim the inputs are unrecoverable when
// hydration in fact fully succeeded, and would leave a real TTS charge unbilled forever.
func TestHybrid_HydrateBillingChunkKeepsModalityRowWithoutTokenUsage(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &Log{
		ID:        "speech-nousage-1",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-4o-mini-tts",
		Status:    "success",
		Object:    "speech",
		// Deliberately no TokenUsageParsed: a TTS response carries no token usage, which
		// is exactly the shape the unconditional token_usage guard used to reject.
		SpeechOutputParsed: &schemas.BifrostSpeechResponse{
			Usage: &schemas.SpeechUsage{InputChars: 512},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "speech-nousage-1")
	require.Equal(t, 1, objStore.Len())

	// Precondition: the payload really did leave the row, so the hydration below is doing
	// the work rather than the column just happening to still be there.
	dbLog, err := inner.FindByID(ctx, "speech-nousage-1")
	require.NoError(t, err)
	require.Empty(t, dbLog.SpeechOutput, "speech_output should be offloaded to object storage")

	result, err := hybrid.SearchLogsForBilling(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)

	hydration, err := hybrid.HydrateBillingChunk(ctx, []*Log{&result.Logs[0]})
	require.NoError(t, err)
	assert.Empty(t, hydration.Unpriceable,
		"the row carries every input pricing reads, so it must not be reported unrecoverable")
	assert.Equal(t, []string{"speech-nousage-1"}, hydration.Hydrated)
	require.NotNil(t, result.Logs[0].SpeechOutputParsed,
		"per-character TTS billing needs the recovered speech_output")
	assert.Equal(t, 512, result.Logs[0].SpeechOutputParsed.Usage.InputChars)
}
