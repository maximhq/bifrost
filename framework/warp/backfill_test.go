package warp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/require"
)

type backfillLogReader struct {
	LogReaderStub
	logs []logstore.Log
	// missing marks IDs Search still lists but GetLog no longer finds - the
	// window retention deletes out from underneath a running backfill.
	missing map[string]bool
	// getErr makes every GetLog fail like an unreachable log store.
	getErr error
}

func (r *backfillLogReader) Search(_ context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	selected := make([]logstore.Log, 0, len(r.logs))
	for _, entry := range r.logs {
		if filters.StartTime != nil && entry.Timestamp.Before(*filters.StartTime) {
			continue
		}
		if filters.EndTime != nil && entry.Timestamp.After(*filters.EndTime) {
			continue
		}
		selected = append(selected, entry)
	}
	start := min(pagination.Offset, len(selected))
	end := min(start+pagination.Limit, len(selected))
	return &logstore.SearchResult{
		Logs: selected[start:end], Pagination: logstore.PaginationOptions{TotalCount: int64(len(selected))},
		Stats: logstore.SearchStats{TotalRequests: int64(len(selected))},
	}, nil
}

func (r *backfillLogReader) GetLog(_ context.Context, id string) (*logstore.Log, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.missing[id] {
		return nil, nil
	}
	for index := range r.logs {
		if r.logs[index].ID == id {
			copy := r.logs[index]
			return &copy, nil
		}
	}
	return nil, nil
}

// GetLogsByIDs preserves input order and omits ids it can't find, mirroring
// the real implementation's contract (see framework/warp/logreader.go).
func (r *backfillLogReader) GetLogsByIDs(_ context.Context, ids []string) ([]logstore.Log, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	found := make([]logstore.Log, 0, len(ids))
	for _, id := range ids {
		if r.missing[id] {
			continue
		}
		for index := range r.logs {
			if r.logs[index].ID == id {
				found = append(found, r.logs[index])
				break
			}
		}
	}
	return found, nil
}

// erroringOnceLogsByIDsReader fails GetLogsByIDs the first time it's called,
// then behaves normally - simulating a transient page-level fetch failure
// (e.g. a storage blip) rather than anything wrong with the logs themselves.
type erroringOnceLogsByIDsReader struct {
	backfillLogReader
	failed bool
}

func (r *erroringOnceLogsByIDsReader) GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error) {
	if !r.failed {
		r.failed = true
		return nil, errors.New("storage unavailable")
	}
	return r.backfillLogReader.GetLogsByIDs(ctx, ids)
}

func backfillEmbeddingExecutor(_ *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	dimension := *request.Params.Dimensions
	return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, dimension)}}}}, nil
}

func TestWarpBackfillIndexesWindowAndCheckpointsCounts(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &backfillLogReader{logs: []logstore.Log{
		{ID: "visible", Timestamp: start.Add(time.Hour), Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "payment failed"},
		{ID: "hidden", Timestamp: start.Add(2 * time.Hour), Object: string(schemas.ResponsesRequest), Status: "success", ContentHidden: true, ContentSummary: "secret"},
	}}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(backfillEmbeddingExecutor),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)
	var initial BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(metaJSON), &initial))
	require.Equal(t, int64(2), initial.Total)

	var checkpoints []string
	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(value string) error {
		checkpoints = append(checkpoints, value)
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, checkpoints)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 2, final.Scanned)
	require.Equal(t, 1, final.Indexed)
	require.Equal(t, 1, final.Skipped)
	require.Zero(t, final.Failed)
	require.NotNil(t, final.CursorTime)
}

func TestWarpBackfillCancellationReturnsLastProgress(t *testing.T) {
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&backfillLogReader{}),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(backfillEmbeddingExecutor),
	)
	defer service.Shutdown()
	start := time.Now().Add(-time.Hour)
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, time.Now(), false)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finalJSON, err := service.RunBackfillJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.Message, "Stopped")
}

// A page-level fetch failure (GetLogsByIDs erroring before any log in the
// page was even attempted) must not be recorded as per-log failures: nothing
// in that page is scanned, the cursor does not move past it, and a retry
// picks the whole page back up rather than treating it as resolved.
func TestWarpBackfillFetchErrorDoesNotAdvanceOrCount(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &erroringOnceLogsByIDsReader{backfillLogReader: backfillLogReader{logs: backfillLogsForAbort(start, 5)}}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(backfillEmbeddingExecutor),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)

	failedJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "storage unavailable")
	var failed BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(failedJSON), &failed))
	require.Zero(t, failed.Scanned)
	require.Zero(t, failed.Failed)
	require.Nil(t, failed.CursorTime)

	// The reader now succeeds (the transient failure has cleared): retrying
	// from the unchanged checkpoint must scan every log exactly once.
	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: failedJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, len(reader.logs), final.Scanned)
	require.Equal(t, len(reader.logs), final.Indexed)
}

// Cancellation that lands mid-page, while some logs in it are still being
// indexed, must not be recorded as counted or as scanned - only the check at
// the top of the outer loop (on the next page) may do that, and only once the
// cursor has genuinely stopped moving.
func TestWarpBackfillCancellationDuringIndexingNotCounted(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := backfillLogsForAbort(start, 5)
	reader := &backfillLogReader{logs: logs}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelID := logs[2].ID
	cancelDuringIndex := func(bctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		if request.Input != nil && request.Input.Text != nil && strings.Contains(*request.Input.Text, cancelID) {
			cancel()
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "caller cancelled"}}
		}
		return backfillEmbeddingExecutor(bctx, request)
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(cancelDuringIndex),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Zero(t, final.Scanned)
	require.Nil(t, final.CursorTime)
}

type activeBackfillStore struct{ active *tables.TableSidekiqJob }

func (s activeBackfillStore) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return s.active, nil
}

func (s activeBackfillStore) GetLatestSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return s.active, nil
}

func TestWarpEmbeddingSpaceChangeBlockedDuringBackfill(t *testing.T) {
	store := &recordingStore{row: validWarpConfigRow()}
	service := NewService(nil, WithConfigStore(store), WithVectorStore(newFakeWarpVectorStore()), WithBackfillJobStore(activeBackfillStore{active: &tables.TableSidekiqJob{ID: "job"}}))
	input := validWarpConfigInput()
	input.EmbeddingModel = "new-model"
	input.LogVectorStoreNamespace = "BifrostWarpLogsV2"
	_, err := service.SaveConfig(context.Background(), input)
	require.ErrorIs(t, err, ErrBackfillInProgress)
}

func TestAdvanceBackfillCursorCountsTimestampTies(t *testing.T) {
	timestamp := time.Now().UTC()
	meta := BackfillJobMeta{}
	// The cursor advances per page now, so ties are counted across pages: a
	// resume has to skip every row already consumed at that exact timestamp,
	// however many pages they arrived over.
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: timestamp}})
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: timestamp}})
	require.Equal(t, 2, meta.CursorOffset)
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: timestamp}})
	require.Equal(t, 3, meta.CursorOffset)

	// Ties within one page are counted too, not just across pages.
	page := BackfillJobMeta{}
	advanceBackfillCursor(&page, []logstore.Log{{Timestamp: timestamp}, {Timestamp: timestamp}, {Timestamp: timestamp}})
	require.Equal(t, 3, page.CursorOffset)

	// A new timestamp restarts the count at that timestamp.
	later := timestamp.Add(time.Second)
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: later}})
	require.Equal(t, 1, meta.CursorOffset)
	require.True(t, meta.CursorTime.Equal(later))

	// An empty page leaves the cursor exactly where it was.
	advanceBackfillCursor(&meta, nil)
	require.Equal(t, 1, meta.CursorOffset)
	require.True(t, meta.CursorTime.Equal(later))
}

// The cursor has to describe the same point the counters do. A page is indexed
// concurrently, so the unit of agreement is the page: counters and cursor both
// move once the whole page is accounted for, and a page that was cut short
// moves neither - which is what makes its logs retried rather than skipped.
func TestWarpBackfillCursorMatchesCountersPerPage(t *testing.T) {
	timestamp := time.Now().UTC()
	meta := BackfillJobMeta{}

	// One full page of two logs at the same timestamp, counted and passed.
	page := []logstore.Log{{Timestamp: timestamp}, {Timestamp: timestamp}}
	meta.Scanned += len(page)
	advanceBackfillCursor(&meta, page)

	require.Equal(t, 2, meta.Scanned)
	require.Equal(t, meta.Scanned, meta.CursorOffset,
		"the cursor must have passed exactly the entries the counters claim")

	// A page that was cut short leaves both untouched, so the resume retries it.
	before := *meta.CursorTime
	offsetBefore, scannedBefore := meta.CursorOffset, meta.Scanned
	advanceBackfillCursor(&meta, nil)
	require.Equal(t, scannedBefore, meta.Scanned)
	require.Equal(t, offsetBefore, meta.CursorOffset)
	require.True(t, meta.CursorTime.Equal(before))
}

// The signature must not be able to collide across genuinely different spaces.
//
// It is compared before every batch to decide whether the embedding
// configuration moved under a running job. Joining the fields with a bare "|"
// and no escaping lets the separator inside one field imitate the boundary of
// the next: a custom provider named "openai|a" with model "b" spells exactly
// what provider "openai" with model "a|b" spells. Neither ValidateConfigInput
// nor the provider list rejects that character, so the job would carry on
// writing into a space it was never frozen against.
func TestWarpEmbeddingSignatureIsUnambiguous(t *testing.T) {
	shifted := &schemas.WarpConfig{
		EmbeddingProvider: "openai|a", EmbeddingModel: "b", EmbeddingDimension: 1536,
		LogVectorStoreNamespace: "ns",
	}
	joined := &schemas.WarpConfig{
		EmbeddingProvider: "openai", EmbeddingModel: "a|b", EmbeddingDimension: 1536,
		LogVectorStoreNamespace: "ns",
	}
	require.NotEqual(t, embeddingConfigSignature(shifted), embeddingConfigSignature(joined),
		"a separator inside a value must not be able to imitate the separator itself")

	// The same configuration must still sign identically, or every batch aborts.
	require.Equal(t, embeddingConfigSignature(joined), embeddingConfigSignature(&schemas.WarpConfig{
		EmbeddingProvider: "openai", EmbeddingModel: "a|b", EmbeddingDimension: 1536,
		LogVectorStoreNamespace: "ns",
	}))

	// And a genuine difference must still be visible.
	require.NotEqual(t, embeddingConfigSignature(joined), embeddingConfigSignature(&schemas.WarpConfig{
		EmbeddingProvider: "openai", EmbeddingModel: "a|b", EmbeddingDimension: 3072,
		LogVectorStoreNamespace: "ns",
	}))
}

// A namespace-only change must be blocked while a backfill is running.
//
// The guard asks embeddingSpaceChanged, which compares provider, model and
// dimension only - so renaming the namespace slipped past the active-job
// lookup, was persisted, and then made the running job abort on its next
// signature check, because that signature does include the namespace. The job
// does not continue safely; it fails.
func TestWarpEmbeddingSpaceChangeIncludesNamespace(t *testing.T) {
	stored := &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: "BifrostWarpLogs",
	}
	renamed := &ConfigInput{
		Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: "BifrostWarpLogsV2",
	}
	require.True(t, embeddingSpaceChanged(stored, renamed),
		"the namespace is part of the space the backfill froze, so renaming it is a change")

	unchanged := *renamed
	unchanged.LogVectorStoreNamespace = "BifrostWarpLogs"
	require.False(t, embeddingSpaceChanged(stored, &unchanged))

	// Whitespace is not a change: the effective namespace is trimmed.
	spaced := *renamed
	spaced.LogVectorStoreNamespace = "  BifrostWarpLogs  "
	require.False(t, embeddingSpaceChanged(stored, &spaced))
}

// A log whose indexing was cancelled must stay behind the cursor.
//
// The cursor advanced before IndexWithConfig, so a cancel during embedding left
// it past a log whose vector may never have been written. The resume then skips
// that log entirely: counted as scanned, never indexed, and nothing anywhere
// says so. A log that vanished between the search and the read is different -
// there is nothing to retry there, so that path keeps its advance.
func TestWarpBackfillCursorStaysBehindACancelledIndex(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	first := start.Add(time.Hour)
	reader := &backfillLogReader{logs: []logstore.Log{
		{ID: "one", Timestamp: first, Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "payment failed"},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancels while the embedding is in flight, which is the window the cursor
	// must not have crossed.
	cancelling := func(bctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		cancel()
		return backfillEmbeddingExecutor(bctx, req)
	}

	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(cancelling),
	)
	defer service.Shutdown()

	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorIs(t, err, context.Canceled)

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Zero(t, final.Scanned, "a log whose indexing was cancelled was not scanned")
	require.Nil(t, final.CursorTime, "the cursor must not have moved past it, so the resume retries it")
}

// usageBackfillEmbeddingExecutor answers like backfillEmbeddingExecutor and
// reports 5 prompt tokens per call, the way a real provider bills an embed.
func usageBackfillEmbeddingExecutor(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	response, bifrostErr := backfillEmbeddingExecutor(ctx, request)
	response.Usage = &schemas.BifrostLLMUsage{PromptTokens: 5, TotalTokens: 5}
	return response, bifrostErr
}

// embeddingPricedCatalog prices text-embedding-3-small at $0.02 / 1M tokens -
// the model validWarpConfigRow embeds with - from a local datasheet, so the
// real pricing path runs without reaching the network.
func embeddingPricedCatalog(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pricing.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"text-embedding-3-small":{"input_cost_per_token":2e-08,"provider":"openai","mode":"embedding"}}`), 0o600))
	store := datasheet.New(nil, bifrost.NewNoOpLogger(), datasheet.Config{URL: "file://" + path})
	require.NoError(t, store.LoadFromURLIntoMemory(context.Background()))
	return modelcatalog.NewTestCatalogWithDatasheet(store)
}

func runBackfillToEnd(t *testing.T, service *Service, start time.Time) (BackfillJobMeta, error) {
	t.Helper()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)
	finalJSON, runErr := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	return final, runErr
}

// The backfill's embedding calls skip the plugin pipeline, so they never reach
// the logs - the job's own checkpoint is the only place their spend shows up.
func TestWarpBackfillTotalsEmbeddingSpend(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &backfillLogReader{logs: backfillLogsForAbort(start, 3)}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(usageBackfillEmbeddingExecutor),
		WithModelCatalog(embeddingPricedCatalog(t)),
	)
	defer service.Shutdown()

	final, err := runBackfillToEnd(t, service, start)
	require.NoError(t, err)
	require.Equal(t, 3, final.Indexed)
	require.Equal(t, int64(15), final.EmbeddingTokens)
	require.NotNil(t, final.EmbeddingCost)
	require.InDelta(t, 15*2e-08, *final.EmbeddingCost, 1e-15)
}

// Without a catalog the cost is unknown, not free: it stays absent so the UI
// does not render $0, while tokens are still counted.
func TestWarpBackfillLeavesCostUnsetWithoutCatalog(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &backfillLogReader{logs: backfillLogsForAbort(start, 2)}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(usageBackfillEmbeddingExecutor),
	)
	defer service.Shutdown()

	final, err := runBackfillToEnd(t, service, start)
	require.NoError(t, err)
	require.Equal(t, int64(10), final.EmbeddingTokens)
	require.Nil(t, final.EmbeddingCost)
}

// A page the failure breaker rolls back out of the progress counters was still
// billed call by call, so its spend must survive the rollback.
func TestWarpBackfillKeepsSpendOfRolledBackPage(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &backfillLogReader{logs: backfillLogsForAbort(start, backfillMaxConsecutiveFailures)}
	// Answers, and bills, but with a vector of the wrong size - so every log
	// fails after the provider has already charged for it.
	wrongDimension := func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data:  []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, 3)}}},
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 5, TotalTokens: 5},
		}, nil
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(wrongDimension),
		WithModelCatalog(embeddingPricedCatalog(t)),
	)
	defer service.Shutdown()

	final, err := runBackfillToEnd(t, service, start)
	require.Error(t, err)
	require.Contains(t, err.Error(), "consecutive")
	require.Zero(t, final.Scanned, "the page is rolled back out of progress")
	require.Equal(t, int64(5*backfillMaxConsecutiveFailures), final.EmbeddingTokens)
	require.NotNil(t, final.EmbeddingCost)
	require.InDelta(t, float64(5*backfillMaxConsecutiveFailures)*2e-08, *final.EmbeddingCost, 1e-15)
}

func failingBackfillEmbeddingExecutor(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found that support model: openai/text-embedding-3-small"}}
}

func backfillLogsForAbort(start time.Time, count int) []logstore.Log {
	logs := make([]logstore.Log, 0, count)
	for index := range count {
		id := fmt.Sprintf("log-%d", index)
		logs = append(logs, logstore.Log{
			// The id is folded into the embedded text (not just used as the log's
			// own ID) so a test executor can key behaviour off which specific log
			// is being embedded - indexing within a page runs concurrently, so
			// nothing about call order or count is deterministic.
			ID: id, Timestamp: start.Add(time.Duration(index) * time.Second),
			Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "payment failed " + id,
		})
	}
	return logs
}

func TestWarpBackfillStopsAfterConsecutiveFailures(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &backfillLogReader{logs: backfillLogsForAbort(start, backfillMaxConsecutiveFailures+50)}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(failingBackfillEmbeddingExecutor),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)

	var checkpoints []string
	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(value string) error {
		checkpoints = append(checkpoints, value)
		return nil
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "consecutive")
	require.Contains(t, err.Error(), "no keys found")
	require.NotEmpty(t, checkpoints)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	// The failure streak tripped the threshold exactly at the end of the first
	// page (backfillBatchSize == backfillMaxConsecutiveFailures), but that page
	// is still rolled back out of the checkpoint entirely: a resume must retry
	// every log in it once the provider is fixed, not treat it as scanned.
	require.Zero(t, final.Scanned)
	require.Zero(t, final.Failed)
	require.Zero(t, final.Indexed)
	require.Nil(t, final.CursorTime)
	require.Contains(t, final.LastError, "no keys found")
	require.Contains(t, final.Message, "Stopped")
}

func TestWarpBackfillSuccessResetsConsecutiveFailures(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := backfillLogsForAbort(start, backfillMaxConsecutiveFailures+50)
	reader := &backfillLogReader{logs: logs}
	// One log succeeds, keyed off its own id rather than call count or order -
	// indexing within a page runs concurrently (backfillIndexConcurrency), so
	// nothing about which call lands "first" or "the Nth" is deterministic.
	successID := logs[len(logs)/2].ID
	flaky := func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		if request.Input != nil && request.Input.Text != nil && strings.Contains(*request.Input.Text, successID) {
			return backfillEmbeddingExecutor(ctx, request)
		}
		return failingBackfillEmbeddingExecutor(ctx, request)
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(flaky),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)
	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, backfillMaxConsecutiveFailures+50, final.Scanned)
	require.Equal(t, 1, final.Indexed)
}

// Retention can delete a log between Search listing it and GetLog reading it.
// A vanished log is a fact about the window, not a dependency failure - and a
// long deleted stretch that counted toward the failure streak aborted a
// perfectly healthy run (or let one later real failure trip the cutoff early).
func TestWarpBackfillDoesNotCountVanishedLogsAsFailures(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// A vanished stretch one short of the threshold, then one genuine indexing
	// failure, then a healthy tail. Counting the vanished logs in the streak
	// makes that single real failure the twentieth - aborting a run whose
	// dependencies failed exactly once.
	vanished := backfillMaxConsecutiveFailures - 1
	total := vanished + 6
	logs := make([]logstore.Log, 0, total)
	missing := map[string]bool{}
	for i := range total {
		id := fmt.Sprintf("log-%03d", i)
		summary := "payment failed"
		if i == vanished {
			summary = "poison entry"
		}
		logs = append(logs, logstore.Log{
			ID: id, Timestamp: start.Add(time.Duration(i) * time.Minute),
			Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: summary,
		})
		if i < vanished {
			missing[id] = true
		}
	}
	reader := &backfillLogReader{logs: logs, missing: missing}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()),
		WithEmbeddingExecutor(func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			if strings.Contains(*request.Input.Text, "poison entry") {
				return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "embedding failed"}}
			}
			return backfillEmbeddingExecutor(ctx, request)
		}),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err, "vanished logs are not consecutive indexing failures, so one real failure must not abort the run")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, vanished+1, final.Failed, "vanished logs and the one real failure are still recorded as failed")
	require.Equal(t, total, final.Scanned, "the run must walk the whole window")
	require.Equal(t, 5, final.Indexed, "the readable tail must still be indexed")
}

// An unreachable log store must stop the run, not walk the window.
//
// A database that failed every read once grew the streak without bound, walked
// the whole window, and returned nil - recorded complete with Indexed == 0 and
// the cursor at the end of the window, so nothing ever retried it. A failed
// page fetch is now a page-level error: nothing in the page is counted and the
// cursor stays put for the resume.
func TestWarpBackfillStopsWhenEveryReadFails(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	total := backfillMaxConsecutiveFailures * 3
	logs := make([]logstore.Log, 0, total)
	for i := range total {
		logs = append(logs, logstore.Log{
			ID: fmt.Sprintf("log-%03d", i), Timestamp: start.Add(time.Duration(i) * time.Minute),
			Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "payment failed",
		})
	}
	reader := &backfillLogReader{logs: logs, getErr: fmt.Errorf("database is unreachable")}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()),
		WithEmbeddingExecutor(backfillEmbeddingExecutor),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.Error(t, err, "a run whose every read failed must not report success")
	require.ErrorContains(t, err, "database is unreachable")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Zero(t, final.Scanned, "it must stop at the first page, not walk the rest of the window")
	require.Nil(t, final.CursorTime, "the cursor must stay put so a resume retries the page")
}

// A vanished log between provider failures preserves the streak - it must not
// reset it. Resetting on a neutral event would defeat the breaker outright: a
// dead provider scanning a window where retention deletes every tenth row
// would go 19 failures, reset, 19 failures, reset - and walk the whole window
// paying for every doomed embedding call. Only a success or a healthy skip
// says the dependency works; a deleted row says nothing either way.
func TestWarpBackfillStreakSurvivesVanishedLogsBetweenFailures(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// One short of two full streaks: had the vanished row reset the count, 99
	// failures, a reset, then 99 more would never reach the threshold and the
	// run would walk the whole window against a dead provider.
	total := backfillMaxConsecutiveFailures*2 - 1
	logs := make([]logstore.Log, 0, total)
	missing := map[string]bool{}
	for i := range total {
		id := fmt.Sprintf("log-%03d", i)
		logs = append(logs, logstore.Log{
			ID: id, Timestamp: start.Add(time.Duration(i) * time.Minute),
			Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "payment failed",
		})
		// One vanished row one entry short of the threshold.
		if i == backfillMaxConsecutiveFailures-1 {
			missing[id] = true
		}
	}
	reader := &backfillLogReader{logs: logs, missing: missing}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()),
		WithEmbeddingExecutor(func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider is down"}}
		}),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour), false)
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.Error(t, err, "a dead provider must trip the breaker even with a vanished row in the streak")
	require.ErrorContains(t, err, "consecutive")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	// The trip lands on the first log of the second page, which is rolled back
	// out of the checkpoint, leaving only the completed first page.
	require.Equal(t, backfillBatchSize, final.Scanned,
		"the vanished row costs one extra scan, never a fresh streak")
}

type terminalBackfillStore struct{ job *tables.TableSidekiqJob }

func (s terminalBackfillStore) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return nil, nil
}

func (s terminalBackfillStore) GetLatestSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return s.job, nil
}

func TestWarpBackfillResumesFromFailedRunCheckpoint(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	logs := backfillLogsForAbort(start, backfillMaxConsecutiveFailures+50)
	reader := &backfillLogReader{logs: logs}
	// The very first log indexes fine so the first page (backfillBatchSize)
	// completes and checkpoints; every log after that fails, so the failure
	// streak trips the abort threshold on the very first log of the next page.
	// That leaves a checkpoint at the boundary between the two pages, with the
	// second page rolled back out of it entirely (see RunBackfillJob) rather
	// than counted with a cursor that never advanced into it. Keyed off the
	// log's own id, not call order: indexing within a page is concurrent
	// (backfillIndexConcurrency), so "the first call" isn't a meaningful
	// notion, but outcomes are still accounted for in the page's original
	// order regardless of completion order.
	successID := logs[0].ID
	firstThenFailing := func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		if request.Input != nil && request.Input.Text != nil && strings.Contains(*request.Input.Text, successID) {
			return backfillEmbeddingExecutor(ctx, request)
		}
		return failingBackfillEmbeddingExecutor(ctx, request)
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(firstThenFailing),
	)
	defer service.Shutdown()

	// First run: dies after backfillMaxConsecutiveFailures, leaving a checkpoint
	// partway through the window.
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, end, false)
	require.NoError(t, err)
	failedJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.Error(t, err)
	var failed BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(failedJSON), &failed))
	// Only the completed first page is checkpointed; the second page, where the
	// streak actually tripped the threshold, is rolled back out entirely so a
	// resume retries all of it rather than double-counting the one log from it
	// that was seen before the abort.
	require.Equal(t, backfillBatchSize, failed.Scanned)
	require.NotNil(t, failed.CursorTime)

	// A second BuildBackfillJobMeta call over the identical window, with a store
	// that reports that run as failed, must resume from the checkpoint rather
	// than rescan from offset 0.
	service.backfillJobs = terminalBackfillStore{job: &tables.TableSidekiqJob{Status: tables.SidekiqStatusFailed, Metadata: failedJSON}}
	resumedJSON, err := service.BuildBackfillJobMeta(context.Background(), start, end, false)
	require.NoError(t, err)
	var resumed BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(resumedJSON), &resumed))
	require.Equal(t, failed.Scanned, resumed.Scanned)
	require.Equal(t, failed.CursorOffset, resumed.CursorOffset)
	require.NotNil(t, resumed.CursorTime)
	require.Contains(t, resumed.Message, "Resuming")

	// Resuming once the provider is fixed must scan exactly the remaining logs,
	// not double-count the one log from the rolled-back page that the failed
	// run already saw. Total scanned across both runs must equal the window's
	// log count exactly, whether counted as one run or split across a
	// stop/resume.
	service.indexer.embed = backfillEmbeddingExecutor
	resumedFinalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: resumedJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var resumedFinal BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(resumedFinalJSON), &resumedFinal))
	require.Equal(t, len(logs), resumedFinal.Scanned)

	// restart=true must ignore the checkpoint and start clean.
	restartedJSON, err := service.BuildBackfillJobMeta(context.Background(), start, end, true)
	require.NoError(t, err)
	var restarted BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(restartedJSON), &restarted))
	require.Zero(t, restarted.Scanned)
	require.Nil(t, restarted.CursorTime)
}
