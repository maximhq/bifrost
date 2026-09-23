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
	// backfillMaxConsecutiveFailures stops a run that is failing for a reason no
	// individual entry can fix.
	//
	// Every failure here costs an embedding request. With no bound, a provider
	// outage or a revoked key made the job walk the entire window issuing one
	// doomed call per log - the most expensive possible way to discover the
	// provider is down. A run of this many in a row is a broken dependency, not
	// unlucky rows, so the job checkpoints what it did and stops; the cursor is
	// where it stopped, so a retry resumes rather than restarts.
	backfillMaxConsecutiveFailures = 20
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

	// Counted across batches, not within one: a provider outage does not stop at
	// a batch boundary, and resetting per batch would let the job keep paying for
	// failed embeddings indefinitely in runs of ninety-nine.
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
			if getErr != nil || entry == nil {
				// This one does advance: the log is gone or unreadable, so there is
				// nothing a resume could retry and leaving the cursor behind would
				// make the job read it again forever.
				advanceBackfillCursor(&meta, listed)
				meta.Scanned++
				meta.Failed++
				// A vanished log is a fact about the window - retention can delete
				// a row between Search listing it and GetLog reading it - not a
				// dependency failure. Counting it in the streak let a long deleted
				// stretch hand the cutoff to the first real failure that followed.
				// A read that failed with anything but not-found still counts.
				if getErr != nil && !errors.Is(getErr, logstore.ErrNotFound) {
					consecutiveFailures++
				}
				if getErr != nil {
					meta.LastError = getErr.Error()
				} else {
					meta.LastError = "log disappeared during backfill"
				}
				// The breaker applies here too. This branch continues past the
				// post-index cutoff below, so an unreachable log store grew the
				// streak without bound, walked the whole window, and recorded
				// the job complete with nothing indexed and nothing to retry it.
				if consecutiveFailures >= backfillMaxConsecutiveFailures {
					meta.Message = fmt.Sprintf("Stopped after %d consecutive failures, having scanned %d log(s).", consecutiveFailures, meta.Scanned)
					if checkpointErr := progress(snapshot()); checkpointErr != nil {
						return snapshot(), fmt.Errorf("Warp backfill stopped after %d consecutive read failures (%s), and its checkpoint could not be written: %w", consecutiveFailures, meta.LastError, checkpointErr)
					}
					return snapshot(), fmt.Errorf("Warp backfill stopped after %d consecutive read failures: %s", consecutiveFailures, meta.LastError)
				}
				continue
			}
			// The config verified for this batch, not a fresh read: a save landing
			// between the check above and this write would otherwise index into a
			// different embedding space than the job froze.
			outcome, indexErr := s.indexer.IndexWithConfig(ctx, config, entry)
			// Same again after the expensive half. A cancelled embedding returns an
			// error that is indistinguishable from a genuine failure, and counting
			// it as Failed both misreports the run and leaves the entry behind the
			// cursor.
			if cancelErr := ctx.Err(); cancelErr != nil {
				meta.Message = fmt.Sprintf("Stopped after scanning %d log(s).", meta.Scanned)
				_ = progress(snapshot())
				return snapshot(), cancelErr
			}
			// Only now, once the cancellable work is behind us. Advancing before
			// the index call left the cursor past a log whose vector write may
			// never have happened, so a resume skipped it: counted as scanned,
			// never indexed, and nothing recording the gap.
			advanceBackfillCursor(&meta, listed)
			meta.Scanned++
			switch {
			case indexErr != nil:
				meta.Failed++
				meta.LastError = indexErr.Error()
				consecutiveFailures++
			case outcome == IndexOutcomeSkipped:
				// A skip is a healthy outcome - the log had nothing to index - so it
				// clears the streak just as an index does.
				meta.Skipped++
				consecutiveFailures = 0
			default:
				meta.Indexed++
				consecutiveFailures = 0
			}
			if consecutiveFailures >= backfillMaxConsecutiveFailures {
				meta.Message = fmt.Sprintf("Stopped after %d consecutive failures, having scanned %d log(s).", consecutiveFailures, meta.Scanned)
				_ = progress(snapshot())
				return snapshot(), fmt.Errorf("warp backfill stopped after %d consecutive indexing failures; last error: %s", consecutiveFailures, meta.LastError)
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
