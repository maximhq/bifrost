package warp

import (
	"context"
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
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: timestamp}, {Timestamp: timestamp}})
	require.Equal(t, 2, meta.CursorOffset)
	advanceBackfillCursor(&meta, []logstore.Log{{Timestamp: timestamp}})
	require.Equal(t, 3, meta.CursorOffset)
}
