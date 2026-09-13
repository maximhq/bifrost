package logging

import (
	"context"
	"errors"
	"fmt"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/stretchr/testify/require"
)

func lifecycleStore(t *testing.T) logstore.LogStore {
	t.Helper()
	s, err := logstore.NewLogStore(context.Background(), &logstore.Config{Enabled: true, Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "cas.db")}, ContentAddressed: &logstore.ContentAddressedConfig{Enabled: true, MinFieldBytes: 32, MinChunkBytes: 32}}, testLogger{})
	require.NoError(t, err)
	require.IsType(t, &logstore.CasLogStore{}, s)
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	return s
}

func TestLifecycleCAS(t *testing.T) {
	for _, mode := range []string{"normal", "stream", "cancel", "fail", "retry"} {
		t.Run(mode, func(t *testing.T) {
			s := lifecycleStore(t)
			pricingPath := filepath.Join(t.TempDir(), "pricing.json")
			require.NoError(t, os.WriteFile(pricingPath, []byte(`{"test-model":{"provider":"openai","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`), 0600))
			ds := datasheet.New(nil, testLogger{}, datasheet.Config{URL: "file://" + pricingPath})
			require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))
			catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)
			p, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{MaxBatchSize: 1, BatchInterval: "10ms"}}, testLogger{}, s, nil, catalog, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, p.Cleanup()) })
			events := make(chan *logstore.Log, 4)
			p.SetLogCallback(func(_ context.Context, row *logstore.Log) {
				if row.Status != "processing" {
					events <- row
				}
			})
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyRequestID, mode)
			rt := schemas.ChatCompletionRequest
			if mode == "stream" || mode == "cancel" {
				rt = schemas.ChatCompletionStreamRequest
			}
			tracer := tracing.NewTracer(tracing.NewTraceStore(time.Minute, testLogger{}), catalog, testLogger{})
			ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
			ctx.SetValue(schemas.BifrostContextKeyTraceID, mode)
			req := &schemas.BifrostRequest{RequestType: rt, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "test-model", Params: &schemas.ChatParameters{}, Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(strings.Repeat("synthetic ", 100))}}}}}
			_, _, err = p.PreLLMHook(ctx, req)
			require.NoError(t, err)
			ef := schemas.BifrostResponseExtraFields{RequestType: rt, RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "test-model"}}
			usage := &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}
			response := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Model: "test-model", Usage: usage, ExtraFields: ef}}
			if mode == "stream" || mode == "cancel" {
				response.ChatResponse.Usage = nil
				response.ChatResponse.Choices = []schemas.BifrostResponseChoice{{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{Role: schemas.Ptr("assistant"), Content: schemas.Ptr(strings.Repeat("synthetic-output ", 100))}}}}
				_, _, err = p.PostLLMHook(ctx, response, nil)
				require.NoError(t, err)
				require.Empty(t, p.writeQueue)
				response.ChatResponse.Choices = nil
				response.ChatResponse.Usage = usage
				response.ChatResponse.ExtraFields.ChunkIndex = 1
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}
			var be *schemas.BifrostError
			wantStatus := "success"
			if mode == "fail" || mode == "cancel" {
				be = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "synthetic failure"}, ExtraFields: schemas.BifrostErrorExtraFields{RequestType: rt, Provider: schemas.OpenAI, OriginalModelRequested: "test-model", BilledUsage: usage}}
				response = nil
				wantStatus = "error"
				if mode == "cancel" {
					be.StatusCode = schemas.Ptr(499)
					wantStatus = "cancelled"
				}
			}
			if mode == "retry" {
				ctx.SetValue(schemas.BifrostContextKeyNumberOfRetries, 2)
			}
			_, _, err = p.PostLLMHook(ctx, response, be)
			require.NoError(t, err)
			require.NoError(t, p.Inject(context.Background(), &schemas.Trace{TraceID: mode, InternalID: mode}))
			select {
			case event := <-events:
				saved, e := s.FindByID(context.Background(), event.ID)
				require.NoError(t, e)
				require.Equal(t, wantStatus, saved.Status)
				require.True(t, saved.HasObject)
				require.Len(t, saved.InputHistoryParsed, 1)
				if mode == "stream" {
					require.NotNil(t, saved.OutputMessageParsed)
					require.NotNil(t, saved.OutputMessageParsed.Content)
					require.Equal(t, strings.Repeat("synthetic-output ", 100), *saved.OutputMessageParsed.Content.ContentStr)
				}
				require.Equal(t, 120, saved.TotalTokens)
				require.NotNil(t, saved.Cost)
				require.InDelta(t, 0.00014, *saved.Cost, 1e-12)
				if mode == "retry" {
					require.Equal(t, 2, saved.NumberOfRetries)
				}
				result, e := s.SearchLogsForBilling(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 10})
				require.NoError(t, e)
				require.Len(t, result.Logs, 1)
				hydration, e := s.(*logstore.CasLogStore).HydrateBillingChunk(context.Background(), []*logstore.Log{&result.Logs[0]})
				require.NoError(t, e)
				require.Empty(t, hydration.Unpriceable)
				require.Equal(t, 120, result.Logs[0].TotalTokens)
				repriced, e := p.calculateCostForLog(&result.Logs[0])
				require.NoError(t, e)
				require.InDelta(t, *saved.Cost, repriced, 1e-12)
			case <-time.After(5 * time.Second):
				t.Fatal("terminal callback timed out")
			}
			_, pending := p.pendingLogsEntries.Load(mode)
			require.False(t, pending)
		})
	}
}

func TestLifecycleDegradedPayloadStatus(t *testing.T) {
	s := lifecycleStore(t)
	p := &LoggerPlugin{ctx: context.Background(), store: s, logger: testLogger{}}
	row := &logstore.Log{ID: "degraded", Timestamp: time.Now(), Status: "success", Model: "test-model", ParamsParsed: map[string]any{"bad": make(chan int), "good": "retained"}, TotalTokens: 120}
	p.processBatch([]*writeQueueEntry{{log: row}})
	saved, err := s.FindByID(context.Background(), row.ID)
	require.NoError(t, err)
	require.Equal(t, "degraded", saved.MetadataParsed["logging_payload_status"])
	require.Equal(t, "serialization_failure", saved.MetadataParsed["logging_payload_reason"])
	require.Equal(t, "success", saved.Status)
	require.Equal(t, 120, saved.TotalTokens)
}

func TestLifecycleContinuousFailureAndQueueFull(t *testing.T) {
	s := lifecycleStore(t)
	fault := &persistenceFaultStore{LogStore: s, failures: map[string]int{}, attempts: map[string]int{}}
	p := &LoggerPlugin{ctx: context.Background(), store: fault, logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 4)}
	callbacks := make(chan struct{}, 100)
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("failed-%d", i)
		fault.failures[id] = -1
		p.enqueueLogEntry(&logstore.Log{ID: id, Timestamp: time.Now(), Status: "success"}, func(*logstore.Log) { callbacks <- struct{}{} })
	}
	require.Equal(t, int64(60), p.droppedRequests.Load())
	for len(p.writeQueue) > 0 {
		p.processBatch([]*writeQueueEntry{<-p.writeQueue})
	}
	require.Equal(t, int64(64), p.droppedRequests.Load())
	require.Empty(t, callbacks)
	for _, n := range fault.attempts {
		require.Equal(t, 3, n)
	}
	result, err := s.SearchLogsForBilling(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 100})
	require.NoError(t, err)
	require.Empty(t, result.Logs)
	p.processBatch([]*writeQueueEntry{{log: &logstore.Log{ID: "recovered", Timestamp: time.Now(), Status: "success"}, callback: func(*logstore.Log) { callbacks <- struct{}{} }}})
	select {
	case <-callbacks:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery callback timed out")
	}
	_, err = s.FindByID(context.Background(), "recovered")
	require.NoError(t, err)
}

// Pauses the first real writer transaction so queue saturation is deterministic.
// Later failures are immediate; recovery resumes the same SQLite CAS store.
type lifecycleOutageStore struct {
	logstore.LogStore
	entered  chan struct{}
	release  chan struct{}
	attempts atomic.Int64
	outage   atomic.Bool
}

func (s *lifecycleOutageStore) BatchCreateIfNotExists(ctx context.Context, rows []*logstore.Log) error {
	if s.attempts.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	if s.outage.Load() {
		return errors.New("synthetic outage")
	}
	return s.LogStore.BatchCreateIfNotExists(ctx, rows)
}
func TestLifecycleAsyncWriterOutage(t *testing.T) {
	real := lifecycleStore(t)
	store := &lifecycleOutageStore{LogStore: real, entered: make(chan struct{}), release: make(chan struct{})}
	store.outage.Store(true)
	p, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{MaxBatchSize: 1, BatchInterval: "10ms", WriteQueueCapacity: 4}}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Cleanup()) })
	events := make(chan string, 100)
	p.SetLogCallback(func(_ context.Context, row *logstore.Log) { events <- row.ID })
	p.EnqueueLogEntry(&logstore.Log{ID: "blocked", Timestamp: time.Now(), Status: "success"})
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start")
	}
	for i := 0; i < 64; i++ {
		p.EnqueueLogEntry(&logstore.Log{ID: fmt.Sprintf("outage-%d", i), Timestamp: time.Now(), Status: "success"})
	}
	require.Equal(t, 4, len(p.writeQueue))
	// The 60 entries that did not fit are now persisted synchronously in the
	// enqueuing goroutine instead of being dropped; with the store down each
	// consumes its three bounded attempts and still ends up counted as dropped
	// (a genuine store failure, not a queue-full loss).
	require.Equal(t, int64(60), p.droppedRequests.Load())
	require.Equal(t, int64(60), p.queueFullSyncPersists.Load())
	require.Equal(t, int64(181), store.attempts.Load())
	close(store.release)
	require.Eventually(t, func() bool { return p.droppedRequests.Load() == 65 }, 5*time.Second, time.Millisecond)
	// Continue failing over multiple independent batches, rather than only draining
	// the saturated queue once. Each log consumes exactly three bounded attempts.
	for round := 0; round < 10; round++ {
		p.EnqueueLogEntry(&logstore.Log{ID: fmt.Sprintf("continuous-%d", round), Timestamp: time.Now(), Status: "success"})
		require.Eventually(t, func() bool { return p.droppedRequests.Load() == int64(66+round) }, 5*time.Second, time.Millisecond)
	}
	// blocked (3) + 60 sync fallbacks (180) + 4 drained queue entries (12) +
	// 10 continuous rounds (30)
	require.Equal(t, int64(225), store.attempts.Load())
	require.Empty(t, events)
	store.outage.Store(false)
	p.EnqueueLogEntry(&logstore.Log{ID: "after-outage", Timestamp: time.Now(), Status: "success"})
	select {
	case id := <-events:
		require.Equal(t, "after-outage", id)
	case <-time.After(5 * time.Second):
		t.Fatal("writer failed to recover")
	}
	_, err = real.FindByID(context.Background(), "after-outage")
	require.NoError(t, err)
	result, err := real.SearchLogsForBilling(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 100})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
}

// TestWriterQueueFullPersistsWithoutDrop is the core no-loss guarantee: with a
// healthy store and a saturated queue, entries are persisted synchronously in
// the caller's goroutine, counted in queueFullSyncPersists, and never counted
// as dropped.
func TestWriterQueueFullPersistsWithoutDrop(t *testing.T) {
	s := lifecycleStore(t)
	p := &LoggerPlugin{ctx: context.Background(), store: s, logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 1)}
	p.writeQueue <- &writeQueueEntry{log: &logstore.Log{ID: "queued", Timestamp: time.Now(), Status: "success"}}
	callbacks := make(chan string, 4)
	p.enqueueLogEntry(&logstore.Log{ID: "synced", Timestamp: time.Now(), Status: "success"}, func(row *logstore.Log) { callbacks <- row.ID })
	p.enqueueMCPToolLogEntry(&logstore.MCPToolLog{ID: "mcp-synced", Timestamp: time.Now(), Status: "success", ToolName: "test-tool"}, func(row *logstore.MCPToolLog) { callbacks <- row.ID })
	require.Equal(t, int64(0), p.droppedRequests.Load())
	require.Equal(t, int64(2), p.queueFullSyncPersists.Load())
	saved, err := s.FindByID(context.Background(), "synced")
	require.NoError(t, err)
	require.Equal(t, "success", saved.Status)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-callbacks:
			seen[id] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("sync persist callbacks timed out; got %v", seen)
		}
	}
	require.Contains(t, seen, "synced")
	require.Contains(t, seen, "mcp-synced")
}

// TestWriterQueueFullWithPausedWriterLosesNothing exercises the full plugin:
// the batch writer is parked mid-transaction, the queue saturates, and further
// enqueues must still reach the store via the synchronous fallback. Nothing is
// dropped and every enqueued row is durable once the writer resumes.
func TestWriterQueueFullWithPausedWriterLosesNothing(t *testing.T) {
	real := lifecycleStore(t)
	store := &lifecycleOutageStore{LogStore: real, entered: make(chan struct{}), release: make(chan struct{})}
	p, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{MaxBatchSize: 1, BatchInterval: "10ms", WriteQueueCapacity: 4}}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Cleanup()) })
	p.EnqueueLogEntry(&logstore.Log{ID: "parked", Timestamp: time.Now(), Status: "success"})
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start")
	}
	for i := 0; i < 4; i++ {
		p.EnqueueLogEntry(&logstore.Log{ID: fmt.Sprintf("queued-%d", i), Timestamp: time.Now(), Status: "success"})
	}
	require.Equal(t, 4, len(p.writeQueue))
	for i := 0; i < 8; i++ {
		p.EnqueueLogEntry(&logstore.Log{ID: fmt.Sprintf("sync-%d", i), Timestamp: time.Now(), Status: "success"})
	}
	require.Equal(t, int64(8), p.queueFullSyncPersists.Load())
	require.Equal(t, int64(0), p.droppedRequests.Load())
	close(store.release)
	for i := 0; i < 13; i++ {
		id := ""
		if i == 0 {
			id = "parked"
		} else if i <= 4 {
			id = fmt.Sprintf("queued-%d", i-1)
		} else {
			id = fmt.Sprintf("sync-%d", i-5)
		}
		require.Eventually(t, func() bool {
			_, err := real.FindByID(context.Background(), id)
			return err == nil
		}, 5*time.Second, 5*time.Millisecond, "row %s never became durable", id)
	}
	require.Equal(t, int64(0), p.droppedRequests.Load())
}

func TestLifecycleTransientFailureDoesNotClaimPayloadDegradation(t *testing.T) {
	real := lifecycleStore(t)
	store := &persistenceFaultStore{LogStore: real, failures: map[string]int{"transient": 2}, attempts: map[string]int{}}
	p := &LoggerPlugin{ctx: context.Background(), store: store, logger: testLogger{}}
	row := &logstore.Log{ID: "transient", Timestamp: time.Now(), Status: "success", ParamsParsed: map[string]any{"good": "retained"}, MetadataParsed: map[string]any{"tenant": "synthetic"}}
	p.processBatch([]*writeQueueEntry{{log: row}})
	saved, err := real.FindByID(context.Background(), row.ID)
	require.NoError(t, err)
	require.Equal(t, "synthetic", saved.MetadataParsed["tenant"])
	require.NotContains(t, saved.MetadataParsed, "logging_payload_status")
	require.Equal(t, 3, store.attempts[row.ID])
}
