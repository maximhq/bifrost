package warp

import (
	"context"
	"fmt"
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
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour))
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
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, time.Now())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finalJSON, err := service.RunBackfillJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.Message, "Stopped")
}

type activeBackfillStore struct{ active *tables.TableSidekiqJob }

func (s activeBackfillStore) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
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
	advanceBackfillCursor(&meta, logstore.Log{Timestamp: timestamp})
	advanceBackfillCursor(&meta, logstore.Log{Timestamp: timestamp})
	require.Equal(t, 2, meta.CursorOffset)
	advanceBackfillCursor(&meta, logstore.Log{Timestamp: timestamp})
	require.Equal(t, 3, meta.CursorOffset)

	// A new timestamp restarts the count at that timestamp.
	later := timestamp.Add(time.Second)
	advanceBackfillCursor(&meta, logstore.Log{Timestamp: later})
	require.Equal(t, 1, meta.CursorOffset)
	require.True(t, meta.CursorTime.Equal(later))
}

// The cursor has to describe the same point the counters do. Advancing only at
// the end of a batch meant a cancellation mid-batch reported entries as scanned
// that the cursor had not passed, so the resume did them again - and the job
// could report more scanned than the total it set out to do.
func TestWarpBackfillCursorMatchesCountersOnCancel(t *testing.T) {
	timestamp := time.Now().UTC()
	meta := BackfillJobMeta{}

	// Two entries processed out of a larger batch, then cancelled.
	for range 2 {
		advanceBackfillCursor(&meta, logstore.Log{Timestamp: timestamp})
		meta.Scanned++
	}

	require.Equal(t, 2, meta.Scanned)
	require.Equal(t, meta.Scanned, meta.CursorOffset,
		"the cursor must have passed exactly the entries the counters claim")
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

	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorIs(t, err, context.Canceled)

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Zero(t, final.Scanned, "a log whose indexing was cancelled was not scanned")
	require.Nil(t, final.CursorTime, "the cursor must not have moved past it, so the resume retries it")
}

func failingBackfillEmbeddingExecutor(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found that support model: openai/text-embedding-3-small"}}
}

func backfillLogsForAbort(start time.Time, count int) []logstore.Log {
	logs := make([]logstore.Log, 0, count)
	for index := range count {
		logs = append(logs, logstore.Log{
			ID: fmt.Sprintf("log-%d", index), Timestamp: start.Add(time.Duration(index) * time.Second),
			Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "payment failed",
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
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour))
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
	require.Equal(t, backfillMaxConsecutiveFailures, final.Scanned)
	require.Equal(t, backfillMaxConsecutiveFailures, final.Failed)
	require.Zero(t, final.Indexed)
	require.Contains(t, final.LastError, "no keys found")
	require.Contains(t, final.Message, "Stopped")
}

func TestWarpBackfillSuccessResetsConsecutiveFailures(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &backfillLogReader{logs: backfillLogsForAbort(start, backfillMaxConsecutiveFailures+50)}
	calls := 0
	flaky := func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		calls++
		if calls%backfillMaxConsecutiveFailures == 0 {
			return backfillEmbeddingExecutor(ctx, request)
		}
		return failingBackfillEmbeddingExecutor(ctx, request)
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(flaky),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, backfillMaxConsecutiveFailures+50, final.Scanned)
	require.Equal(t, 1, final.Indexed)
}
