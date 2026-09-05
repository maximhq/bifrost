package warp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/sidekiq"
)

const (
	BackfillJobKind   = "warp_log_embedding_backfill"
	backfillBatchSize = 100
	// backfillMaxConsecutiveFailures is how many logs in a row may fail to index
	// before the job gives up. A dead embedding provider fails every row the same
	// way, so continuing past this point only burns time and quota.
	backfillMaxConsecutiveFailures = 100
)

var ErrBackfillInProgress = errors.New("warp: a log embedding backfill is running")

// BackfillJobStore is the durable lookup surface used to enforce one job and
// protect an active embedding space from configuration changes.
type BackfillJobStore interface {
	GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
}

// BackfillJobMeta is both the immutable request and the resumable checkpoint.
type BackfillJobMeta struct {
	StartTime       time.Time  `json:"start_time"`
	EndTime         time.Time  `json:"end_time"`
	ConfigSignature string     `json:"config_signature"`
	Namespace       string     `json:"namespace"`
	CursorTime      *time.Time `json:"cursor_time,omitempty"`
	CursorOffset    int        `json:"cursor_offset,omitempty"`
	Total           int64      `json:"total"`
	Scanned         int        `json:"scanned"`
	Indexed         int        `json:"indexed"`
	Skipped         int        `json:"skipped"`
	Failed          int        `json:"failed"`
	LastError       string     `json:"last_error,omitempty"`
	Message         string     `json:"message,omitempty"`
}

func embeddingConfigSignature(config *schemas.WarpConfig) string {
	return fmt.Sprintf("%s|%s|%d|%s", config.EmbeddingProvider, config.EmbeddingModel, config.EmbeddingDimension, config.EffectiveLogVectorStoreNamespace())
}

// RegisterBackfill binds Warp's handler to the shared Sidekiq runner.
func (s *Service) RegisterBackfill(runner *sidekiq.Runner) {
	if runner == nil || s.indexer == nil || s.logs == nil {
		return
	}
	runner.Register(BackfillJobKind, s.RunBackfillJob)
}

// BuildBackfillJobMeta freezes the selected window and embedding space, and
// counts candidates so callers can show determinate progress.
func (s *Service) BuildBackfillJobMeta(ctx context.Context, start, end time.Time) (string, error) {
	if !start.Before(end) {
		return "", fmt.Errorf("%w: start_time must be before end_time", ErrInvalidConfig)
	}
	if s.logs == nil || s.indexer == nil {
		return "", ErrUnavailable
	}
	config, err := s.Config(ctx)
	if err != nil {
		return "", err
	}
	filters := backfillFilters(start, end)
	result, err := s.logs.Search(ctx, &filters, &logstore.PaginationOptions{Limit: 1, SortBy: "timestamp", Order: "asc"})
	if err != nil {
		return "", fmt.Errorf("count Warp log embedding candidates: %w", err)
	}
	total := result.Stats.TotalRequests
	if result.Pagination.TotalCount > total {
		total = result.Pagination.TotalCount
	}
	meta := BackfillJobMeta{StartTime: start.UTC(), EndTime: end.UTC(), ConfigSignature: embeddingConfigSignature(config), Namespace: config.EffectiveLogVectorStoreNamespace(), Total: total}
	return marshalBackfillMeta(meta)
}

// RunBackfillJob walks the frozen window in stable timestamp/id order. The
// inclusive cursor plus offset makes identical timestamps resumable.
func (s *Service) RunBackfillJob(ctx context.Context, job tables.TableSidekiqJob, progress sidekiq.ProgressFunc) (string, error) {
	var meta BackfillJobMeta
	if err := sonic.Unmarshal([]byte(job.Metadata), &meta); err != nil {
		return job.Metadata, fmt.Errorf("parse Warp backfill metadata: %w", err)
	}
	lastSnapshot := job.Metadata
	snapshot := func() string {
		encoded, err := marshalBackfillMeta(meta)
		if err == nil {
			lastSnapshot = encoded
		}
		return lastSnapshot
	}

	// Counted in memory only: a resumed job starts with a clean slate, which is
	// the point of resuming after the operator fixed the provider.
	consecutiveFailures := 0
	for {
		if err := ctx.Err(); err != nil {
			meta.Message = fmt.Sprintf("Stopped after scanning %d log(s).", meta.Scanned)
			_ = progress(snapshot())
			return snapshot(), err
		}
		config, err := s.Config(ctx)
		if err != nil {
			return snapshot(), err
		}
		if embeddingConfigSignature(config) != meta.ConfigSignature {
			return snapshot(), fmt.Errorf("Warp embedding configuration changed while backfill was running")
		}

		start := meta.StartTime
		if meta.CursorTime != nil {
			start = *meta.CursorTime
		}
		filters := backfillFilters(start, meta.EndTime)
		pagination := logstore.PaginationOptions{Limit: backfillBatchSize, Offset: meta.CursorOffset, SortBy: "timestamp", Order: "asc"}
		result, err := s.logs.Search(ctx, &filters, &pagination)
		if err != nil {
			return snapshot(), fmt.Errorf("search logs for Warp backfill: %w", err)
		}
		if result == nil || len(result.Logs) == 0 {
			break
		}

		for index := range result.Logs {
			if err := ctx.Err(); err != nil {
				meta.Message = fmt.Sprintf("Stopped after scanning %d log(s).", meta.Scanned)
				_ = progress(snapshot())
				return snapshot(), err
			}
			listed := result.Logs[index]
			entry, getErr := s.logs.GetLog(ctx, listed.ID)
			meta.Scanned++
			failed := true
			switch {
			case getErr != nil:
				meta.LastError = getErr.Error()
			case entry == nil:
				meta.LastError = "log disappeared during backfill"
			default:
				outcome, indexErr := s.indexer.Index(ctx, entry)
				switch {
				case indexErr != nil:
					meta.LastError = indexErr.Error()
				case outcome == IndexOutcomeSkipped:
					failed = false
					meta.Skipped++
				default:
					failed = false
					meta.Indexed++
				}
			}
			if !failed {
				consecutiveFailures = 0
				continue
			}
			meta.Failed++
			consecutiveFailures++
			if consecutiveFailures >= backfillMaxConsecutiveFailures {
				// Every recent row failed the same way, which points at the embedding
				// provider or key rather than the data. Stop here so a 100k-log window
				// does not spend hours failing. Progress is checkpointed so the UI
				// shows exactly where it gave up.
				meta.Message = fmt.Sprintf("Stopped after %d consecutive failures (%d scanned).", consecutiveFailures, meta.Scanned)
				_ = progress(snapshot())
				return snapshot(), fmt.Errorf("Warp backfill stopped after %d consecutive failures: %s", consecutiveFailures, meta.LastError)
			}
		}

		advanceBackfillCursor(&meta, result.Logs)
		if err := progress(snapshot()); err != nil {
			return snapshot(), fmt.Errorf("checkpoint Warp backfill: %w", err)
		}
		if len(result.Logs) < backfillBatchSize {
			break
		}
	}
	meta.Message = fmt.Sprintf("Scanned %d log(s): %d indexed, %d skipped, %d failed.", meta.Scanned, meta.Indexed, meta.Skipped, meta.Failed)
	return snapshot(), nil
}

func backfillFilters(start, end time.Time) logstore.SearchFilters {
	return logstore.SearchFilters{
		Objects: []string{string(schemas.ChatCompletionRequest), string(schemas.ChatCompletionStreamRequest), string(schemas.ResponsesRequest), string(schemas.ResponsesStreamRequest)},
		Status:  []string{"success", "error", "cancelled"}, StartTime: &start, EndTime: &end,
	}
}

func advanceBackfillCursor(meta *BackfillJobMeta, logs []logstore.Log) {
	if len(logs) == 0 {
		return
	}
	last := logs[len(logs)-1].Timestamp
	countAtLast := 0
	for index := len(logs) - 1; index >= 0 && logs[index].Timestamp.Equal(last); index-- {
		countAtLast++
	}
	if meta.CursorTime != nil && meta.CursorTime.Equal(last) {
		meta.CursorOffset += countAtLast
	} else {
		meta.CursorOffset = countAtLast
	}
	value := last
	meta.CursorTime = &value
}

func marshalBackfillMeta(meta BackfillJobMeta) (string, error) {
	encoded, err := sonic.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal Warp backfill metadata: %w", err)
	}
	return string(encoded), nil
}
