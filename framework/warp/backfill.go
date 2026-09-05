package warp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// embeddingConfigSignature identifies the embedding space a backfill was frozen
// against.
//
// Length-prefixed rather than separator-joined. With a bare "|" the separator
// inside a value imitates the boundary of the next field: a custom provider
// named "openai|a" with model "b" spells exactly what provider "openai" with
// model "a|b" spells, and nothing rejects that character - so a job could carry
// on writing into a space it was never frozen against. A length prefix cannot
// be forged by the content it describes.
func embeddingConfigSignature(config *schemas.WarpConfig) string {
	var builder strings.Builder
	for _, field := range []string{
		string(config.EmbeddingProvider),
		config.EmbeddingModel,
		strconv.Itoa(config.EmbeddingDimension),
		config.EffectiveLogVectorStoreNamespace(),
	} {
		fmt.Fprintf(&builder, "%d:%s|", len(field), field)
	}
	return builder.String()
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
			// Advanced per entry rather than per batch, so the counters and the
			// cursor always describe the same point - and after the work rather
			// than before it, so a cancelled entry is retried rather than skipped.
			entry, getErr := s.logs.GetLog(ctx, listed.ID)
			// Cancellation is not a failure of this entry, and must not be counted
			// as one. Advancing the cursor past a log whose work never ran means the
			// resume skips it: it is recorded as scanned, never indexed, and nothing
			// says so.
			if cancelErr := ctx.Err(); cancelErr != nil {
				meta.Message = fmt.Sprintf("Stopped after scanning %d log(s).", meta.Scanned)
				_ = progress(snapshot())
				return snapshot(), cancelErr
			}
			failed, skipped := true, false
			// A vanished log is a fact about the window - retention can delete a
			// row between Search listing it and GetLog reading it - not a
			// dependency failure. Counting it in the streak let a long deleted
			// stretch hand the breaker's cutoff to the first real failure that
			// followed. It is still recorded as failed; it just never feeds the
			// breaker. A read that failed with anything but not-found does.
			countsTowardStreak := true
			switch {
			case getErr != nil:
				meta.LastError = getErr.Error()
				if errors.Is(getErr, logstore.ErrNotFound) {
					countsTowardStreak = false
				}
			case entry == nil:
				meta.LastError = "log disappeared during backfill"
				countsTowardStreak = false
			default:
				// The config verified for this batch, not a fresh read: a save
				// landing between the check above and this write would otherwise
				// index into a different embedding space than the job froze.
				outcome, indexErr := s.indexer.IndexWithConfig(ctx, config, entry)
				// Recorded, not counted yet. The counters move below, together with
				// the cursor, because a cancellation landing between here and there
				// produced a snapshot whose Indexed or Skipped already included an
				// entry the cursor had not passed - so the resume processed the same
				// log again and counted it twice, and the totals could exceed Total.
				switch {
				case indexErr != nil:
					meta.LastError = indexErr.Error()
				case outcome == IndexOutcomeSkipped:
					failed, skipped = false, true
				default:
					failed = false
				}
			}
			// Cancellation first, before anything is counted or the cursor moves.
			// A cancelled index returns an error indistinguishable from a genuine
			// one, so counting it both misreports the run and feeds the
			// consecutive-failure breaker - a cancel reported as a provider
			// outage. Leaving the cursor behind is what makes the entry retried on
			// resume rather than silently skipped.
			if cancelErr := ctx.Err(); cancelErr != nil {
				meta.Message = fmt.Sprintf("Stopped after scanning %d log(s).", meta.Scanned)
				_ = progress(snapshot())
				return snapshot(), cancelErr
			}
			// Past the cancellable work now, so this entry is genuinely done -
			// indexed, skipped, or failed for a reason a retry would hit again.
			advanceBackfillCursor(&meta, listed)
			meta.Scanned++
			if !failed {
				// Counted here, in the same step as the cursor, so a snapshot never
				// claims an outcome for an entry the cursor has not passed.
				if skipped {
					meta.Skipped++
				} else {
					meta.Indexed++
				}
				consecutiveFailures = 0
				continue
			}
			meta.Failed++
			if countsTowardStreak {
				consecutiveFailures++
			}
			if consecutiveFailures >= backfillMaxConsecutiveFailures {
				// Every recent row failed the same way, which points at the embedding
				// provider or key rather than the data. Stop here so a 100k-log window
				// does not spend hours failing. Progress is checkpointed so the UI
				// shows exactly where it gave up.
				meta.Message = fmt.Sprintf("Stopped after %d consecutive failures (%d scanned).", consecutiveFailures, meta.Scanned)
				// The checkpoint is the only record of where this gave up, and the
				// UI reads it to show that. Discarding a failed write left the job
				// reporting an older position than it reached, so a resume redid
				// work and the operator saw a run that appeared to stop earlier
				// than it did - reported as the same error either way.
				if checkpointErr := progress(snapshot()); checkpointErr != nil {
					return snapshot(), fmt.Errorf("Warp backfill stopped after %d consecutive failures (%s), and its checkpoint could not be written: %w", consecutiveFailures, meta.LastError, checkpointErr)
				}
				return snapshot(), fmt.Errorf("Warp backfill stopped after %d consecutive failures: %s", consecutiveFailures, meta.LastError)
			}
		}

		if err := progress(snapshot()); err != nil {
			return snapshot(), fmt.Errorf("checkpoint Warp backfill: %w", err)
		}
		if len(result.Logs) < backfillBatchSize {
			// Checked before the loop exits, so a job cancelled during its last
			// short batch does not report success.
			if cancelErr := ctx.Err(); cancelErr != nil {
				meta.Message = fmt.Sprintf("Stopped after scanning %d log(s).", meta.Scanned)
				return snapshot(), cancelErr
			}
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

// advanceBackfillCursor moves the cursor past one entry.
//
// The cursor is a timestamp plus how many rows at that exact timestamp have
// been consumed, which is what lets a resume skip them without a keyset column.
// It depends on equal-timestamp rows coming back in a stable order, which is
// why the log search orders by (timestamp, id).
func advanceBackfillCursor(meta *BackfillJobMeta, entry logstore.Log) {
	at := entry.Timestamp
	if meta.CursorTime != nil && meta.CursorTime.Equal(at) {
		meta.CursorOffset++
	} else {
		meta.CursorOffset = 1
	}
	value := at
	meta.CursorTime = &value
}

func marshalBackfillMeta(meta BackfillJobMeta) (string, error) {
	encoded, err := sonic.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal Warp backfill metadata: %w", err)
	}
	return string(encoded), nil
}
