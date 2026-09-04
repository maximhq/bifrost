package warp

import (
	"context"
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

// A provider outage fails every entry, and every failure costs an embedding
// request. Without a bound the job walked the whole window issuing one doomed
// call per log - the most expensive possible way to find out the provider is
// down - and finished reporting "done" with everything failed.
func TestWarpBackfillStopsAfterConsecutiveFailures(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := make([]logstore.Log, 0, backfillMaxConsecutiveFailures*3)
	for i := range cap(logs) {
		logs = append(logs, logstore.Log{
			ID: fmt.Sprintf("log-%03d", i), Timestamp: start.Add(time.Duration(i) * time.Minute),
			Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "payment failed",
		})
	}
	reader := &backfillLogReader{logs: logs}
	calls := 0
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()),
		WithEmbeddingExecutor(func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			calls++
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider is down"}}
		}),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.Error(t, err, "a run that failed every entry must not report success")
	require.ErrorContains(t, err, "consecutive indexing failures")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, backfillMaxConsecutiveFailures, final.Failed)
	require.Equal(t, backfillMaxConsecutiveFailures, final.Scanned,
		"it must stop at the threshold, not walk the rest of the window")
	require.LessOrEqual(t, calls, backfillMaxConsecutiveFailures,
		"every attempt past the threshold is a paid-for request that could not succeed")
	// The cursor is where it stopped, so a retry resumes rather than restarts.
	require.NotNil(t, final.CursorTime)
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
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err, "vanished logs are not consecutive indexing failures, so one real failure must not abort the run")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, vanished+1, final.Failed, "vanished logs and the one real failure are still recorded as failed")
	require.Equal(t, total, final.Scanned, "the run must walk the whole window")
	require.Equal(t, 5, final.Indexed, "the readable tail must still be indexed")
}

// An unreachable log store must trip the failure breaker on the read path too.
//
// The read-failure branch continues before the cutoff check, which only runs
// after IndexWithConfig - so a database that failed every GetLog grew the
// streak without bound, walked the whole window, and returned nil. The job was
// then recorded complete with Indexed == 0 and the cursor at the end of the
// window, so nothing ever retried it.
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
	metaJSON, err := service.BuildBackfillJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)

	finalJSON, err := service.RunBackfillJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.Error(t, err, "a run whose every read failed must not report success")
	require.ErrorContains(t, err, "consecutive")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, backfillMaxConsecutiveFailures, final.Scanned,
		"it must stop at the threshold, not walk the rest of the window")
}
