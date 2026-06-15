package logging

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// seedProcessingRow inserts a partial "processing" row (input only) that mimics
// what cleanupStalePendingLogs persists before evicting an idle pending entry.
func seedProcessingRow(t *testing.T, store logstore.LogStore, id string, origTimestamp time.Time) {
	t.Helper()
	content := "hello from input"
	pending := &PendingLogData{
		RequestID: id,
		Timestamp: origTimestamp,
		InitialData: &InitialLogData{
			Object:   "chat.completion",
			Provider: "openai",
			Model:    "gpt-4o",
			InputHistory: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &content},
			}},
		},
	}
	partial := buildInitialLogEntry(pending)
	partial.CreatedAt = time.Now().UTC()
	if err := store.CreateIfNotExists(context.Background(), partial); err != nil {
		t.Fatalf("seed CreateIfNotExists() error = %v", err)
	}
}

// TestCleanupStalePendingPersistsPartialRow verifies the persist-before-evict
// half of the durability fallback: a stale LLM pending entry is written to the
// DB as a "processing" row (input preserved, CreatedAt reset to now, original
// Timestamp kept) before it is dropped from memory.
func TestCleanupStalePendingPersistsPartialRow(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	origTimestamp := time.Now().Add(-2 * time.Hour)
	stale := time.Now().Add(-pendingLogTTL - time.Minute)
	content := "input that must survive eviction"
	pending := &PendingLogData{
		RequestID: "req-evicted",
		Timestamp: origTimestamp,
		Status:    "processing",
		CreatedAt: stale,
		InitialData: &InitialLogData{
			Object:   "chat.completion",
			Provider: "openai",
			Model:    "gpt-4o",
			InputHistory: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &content},
			}},
		},
	}
	pending.LastActivity.Store(stale.UnixNano())
	plugin.pendingLogsEntries.Store("req-evicted", pending)

	plugin.cleanupStalePendingLogs()

	if _, ok := plugin.pendingLogsEntries.Load("req-evicted"); ok {
		t.Fatal("expected stale pending entry to be removed from memory")
	}

	// Flush the async write queue.
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-evicted")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "processing" {
		t.Fatalf("expected status processing, got %q", row.Status)
	}
	if row.Provider != "openai" || row.Model != "gpt-4o" {
		t.Fatalf("expected input fields preserved, got provider=%q model=%q", row.Provider, row.Model)
	}
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected input history preserved, got %d messages", len(row.InputHistoryParsed))
	}
	// Timestamp keeps the original request time; CreatedAt is reset to ~now so the
	// processing-row Flush window restarts.
	if !row.Timestamp.Equal(origTimestamp.UTC()) {
		t.Fatalf("expected original timestamp %v, got %v", origTimestamp.UTC(), row.Timestamp)
	}
	if row.CreatedAt.Before(time.Now().Add(-5 * time.Minute)) {
		t.Fatalf("expected CreatedAt reset to ~now, got %v", row.CreatedAt)
	}
}

// TestPostLLMHookReconcilesEvictedPartialRowSuccess verifies the finish half: a
// successful response whose in-memory pending was already evicted reconciles
// output onto the persisted partial row via UPDATE, preserving the input.
func TestPostLLMHookReconcilesEvictedPartialRowSuccess(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-reconcile-success"
	origTimestamp := time.Now().Add(-90 * time.Minute)
	seedProcessingRow(t, store, id, origTimestamp)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, id)

	finish := "stop"
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{Index: 0, FinishReason: &finish}},
			Usage:   usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o",
				ResolvedModelUsed:      "gpt-4o",
			},
		},
	}

	if _, _, err := plugin.PostLLMHook(ctx, result, nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" {
		t.Fatalf("expected status success after reconcile, got %q", row.Status)
	}
	if row.TotalTokens != 15 {
		t.Fatalf("expected total tokens 15 from reconciled output, got %d", row.TotalTokens)
	}
	// Input from the partial row must be preserved (UPDATE writes non-zero output
	// fields only, leaving input columns intact).
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected input history preserved through reconcile, got %d messages", len(row.InputHistoryParsed))
	}
}

// TestPostLLMHookReconcilesEvictedPartialRowError verifies the error variant of
// the reconcile path: a terminal error with no in-memory pending updates the
// persisted partial row to status=error with error details.
func TestPostLLMHookReconcilesEvictedPartialRowError(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-reconcile-error"
	seedProcessingRow(t, store, id, time.Now().Add(-90*time.Minute))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, id)

	statusCode := 500
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "provider failed late"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4o",
			ResolvedModelUsed:      "gpt-4o",
		},
	}

	if _, _, err := plugin.PostLLMHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "error" {
		t.Fatalf("expected status error after reconcile, got %q", row.Status)
	}
	if row.ErrorDetails == "" && row.ErrorDetailsParsed == nil {
		t.Fatal("expected error details persisted on reconciled row")
	}
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected input history preserved through reconcile, got %d messages", len(row.InputHistoryParsed))
	}
}

// fallbackSpyStore counts the DB-fallback probe (IsLogEntryPresent) so we can
// assert the terminal-call guard prevents per-chunk DB hits.
type fallbackSpyStore struct {
	logstore.LogStore
	presentCalls atomic.Int64
}

func (s *fallbackSpyStore) IsLogEntryPresent(ctx context.Context, id string) (bool, error) {
	s.presentCalls.Add(1)
	// Report absent so the fallback falls through to existing behavior; this test
	// only cares about whether the probe is invoked.
	return false, nil
}

// TestPostLLMHookNoDBProbeOnNonFinalChunk verifies that a non-final streaming
// chunk with no in-memory pending does NOT probe the DB (avoiding the per-chunk
// hammering that caused the original warning storm), while a terminal call does.
func TestPostLLMHookNoDBProbeOnNonFinalChunk(t *testing.T) {
	spy := &fallbackSpyStore{LogStore: newTestStore(t)}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, spy, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	streamResult := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionStreamRequest,
				Provider:    schemas.OpenAI,
			},
		},
	}

	// Non-final chunk: no StreamEndIndicator set => IsFinalChunk == false.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-stream-nopending")
	if _, _, err := plugin.PostLLMHook(ctx, streamResult, nil); err != nil {
		t.Fatalf("PostLLMHook() non-final error = %v", err)
	}
	if got := spy.presentCalls.Load(); got != 0 {
		t.Fatalf("expected no DB probe on non-final chunk, got %d calls", got)
	}

	// Final chunk: StreamEndIndicator set => terminal call => DB probe runs.
	finalCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	finalCtx.SetValue(schemas.BifrostContextKeyRequestID, "req-stream-nopending")
	finalCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	if _, _, err := plugin.PostLLMHook(finalCtx, streamResult, nil); err != nil {
		t.Fatalf("PostLLMHook() final error = %v", err)
	}
	if got := spy.presentCalls.Load(); got != 1 {
		t.Fatalf("expected exactly one DB probe on the final chunk, got %d calls", got)
	}
}

// slowStore wraps a LogStore and injects latency into the batched write paths so
// inserts and updates back up in the writeQueue/batch — the worst case for the
// evict→persist→reconcile ordering guarantee.
type slowStore struct {
	logstore.LogStore
	delay time.Duration
}

func (s *slowStore) BatchCreateIfNotExists(ctx context.Context, entries []*logstore.Log) error {
	time.Sleep(s.delay)
	return s.LogStore.BatchCreateIfNotExists(ctx, entries)
}

func (s *slowStore) BatchUpdate(ctx context.Context, entries []*logstore.Log) error {
	time.Sleep(s.delay)
	return s.LogStore.BatchUpdate(ctx, entries)
}

// TestBulkEvictReconcileIntegritySQLite is the end-to-end stress test: a large
// batch of requests is evicted (bulk partial-row inserts) and then finished (bulk
// reconcile updates) against a deliberately slow store so the two write lanes back
// up and interleave. It asserts that EVERY request ends up with a single row whose
// input (from the partial) AND output (from the reconcile) are intact, the final
// status is correct, and nothing was dropped — proving the ordering guarantee holds
// under load on sqlite.
func TestBulkEvictReconcileIntegritySQLite(t *testing.T) {
	base := newTestStore(t)
	slow := &slowStore{LogStore: base, delay: 3 * time.Millisecond}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, slow, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const N = 800
	origTimestamp := time.Now().Add(-2 * time.Hour)
	stale := time.Now().Add(-pendingLogTTL - time.Minute)

	// Phase 1: seed N pending entries that are all past the idle TTL, then evict
	// them in one sweep — this bulk-enqueues N partial "processing" inserts.
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("bulk-%d", i)
		content := "input-" + id
		pending := &PendingLogData{
			RequestID: id,
			Timestamp: origTimestamp,
			Status:    "processing",
			CreatedAt: stale,
			InitialData: &InitialLogData{
				Object:   "chat.completion",
				Provider: "openai",
				Model:    "gpt-4o",
				InputHistory: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &content},
				}},
			},
		}
		pending.LastActivity.Store(stale.UnixNano())
		plugin.pendingLogsEntries.Store(id, pending)
	}
	plugin.cleanupStalePendingLogs()

	// Phase 2: finish all N requests with no in-memory pending (the entries were
	// just evicted) — this bulk-enqueues N reconcile updates while the inserts are
	// still draining through the slow store.
	finish := "stop"
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("bulk-%d", i)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyRequestID, id)
		result := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				Choices: []schemas.BifrostResponseChoice{{Index: 0, FinishReason: &finish}},
				Usage:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:            schemas.ChatCompletionRequest,
					Provider:               schemas.OpenAI,
					OriginalModelRequested: "gpt-4o",
					ResolvedModelUsed:      "gpt-4o",
				},
			},
		}
		if _, _, err := plugin.PostLLMHook(ctx, result, nil); err != nil {
			t.Fatalf("PostLLMHook(%s) error = %v", id, err)
		}
	}

	// Phase 3: drain the queue fully.
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	// Phase 4: every request must have exactly one row with input + output intact.
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("bulk-%d", i)
		row, err := base.FindByID(context.Background(), id)
		if err != nil {
			t.Fatalf("FindByID(%s) error = %v (log lost)", id, err)
		}
		if row.Status != "success" {
			t.Fatalf("%s: expected reconciled status success, got %q", id, row.Status)
		}
		if row.TotalTokens != 15 {
			t.Fatalf("%s: expected output tokens 15, got %d", id, row.TotalTokens)
		}
		if len(row.InputHistoryParsed) != 1 {
			t.Fatalf("%s: expected input preserved through reconcile, got %d messages", id, len(row.InputHistoryParsed))
		}
		if !row.Timestamp.Equal(origTimestamp.UTC()) {
			t.Fatalf("%s: expected original timestamp preserved, got %v", id, row.Timestamp)
		}
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", dropped)
	}
}

// TestBatchUpdateSkipsMissingRows verifies store-level semantics the fallback
// relies on: BatchUpdate writes non-zero fields onto present rows and silently
// skips absent IDs (no ErrNotFound for the whole batch).
func TestBatchUpdateSkipsMissingRows(t *testing.T) {
	store := newTestStore(t)
	seedProcessingRow(t, store, "present-row", time.Now())

	newStatus := "success"
	updates := []*logstore.Log{
		{ID: "present-row", Status: newStatus, TotalTokens: 42},
		{ID: "absent-row", Status: newStatus, TotalTokens: 7},
	}
	if err := store.BatchUpdate(context.Background(), updates); err != nil {
		t.Fatalf("BatchUpdate() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "present-row")
	if err != nil {
		t.Fatalf("FindByID(present-row) error = %v", err)
	}
	if row.Status != "success" || row.TotalTokens != 42 {
		t.Fatalf("expected present row updated, got status=%q tokens=%d", row.Status, row.TotalTokens)
	}
	if _, err := store.FindByID(context.Background(), "absent-row"); err == nil {
		t.Fatal("expected absent row to remain absent (BatchUpdate must not insert)")
	}
}
