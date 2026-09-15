package warp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

type backfillLogReader struct {
	LogReaderStub
	logs []logstore.Log
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
	found := make([]logstore.Log, 0, len(ids))
	for _, id := range ids {
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
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: timestamp}, {Timestamp: timestamp}})
	require.Equal(t, 2, meta.CursorOffset)
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: timestamp}})
	require.Equal(t, 3, meta.CursorOffset)
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
