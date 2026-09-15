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
	"golang.org/x/sync/errgroup"
)

const (
	BackfillJobKind   = "warp_log_embedding_backfill"
	backfillBatchSize = 100
	// backfillIndexConcurrency bounds how many logs in a page are embedded and
	// upserted at once. Each one is a network round trip to the embedding
	// provider, so indexing them one at a time left a backfill almost entirely
	// idle, waiting on the wire; this overlaps them the way the live indexer's
	// own worker pool already does (see warpIndexWorkers).
	backfillIndexConcurrency = 8
	// backfillMaxConsecutiveFailures is how many logs in a row may fail to index
	// before the job gives up. A dead embedding provider fails every row the same
	// way, so continuing past this point only burns time and quota.
	backfillMaxConsecutiveFailures = 100
)

var ErrBackfillInProgress = errors.New("warp: a log embedding backfill is running")

// BackfillJobStore is the durable lookup surface used to enforce one job,
// protect an active embedding space from configuration changes, and find a
// prior run's checkpoint to resume from.
type BackfillJobStore interface {
	GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
	GetLatestSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
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
//
// If a previous backfill over the exact same window and embedding
// configuration stopped partway through - it failed, or an operator
// cancelled it - its checkpoint is reused instead of rescanning from offset
// 0. A crash at 5000/5021 otherwise meant every restart re-scanned and
// re-embedded all 5000 already-indexed logs. Pass restart to force a clean
// scan and discard that checkpoint anyway (e.g. after fixing bad data in the
// window rather than a flaky provider).
func (s *Service) BuildBackfillJobMeta(ctx context.Context, start, end time.Time, restart bool) (string, error) {
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
	signature := embeddingConfigSignature(config)
	start, end = start.UTC(), end.UTC()

	if !restart {
		resumed, ok, err := s.resumableBackfillMeta(ctx, start, end, signature)
		if err != nil {
			return "", err
		}
		if ok {
			return marshalBackfillMeta(resumed)
		}
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
	meta := BackfillJobMeta{StartTime: start, EndTime: end, ConfigSignature: signature, Namespace: config.EffectiveLogVectorStoreNamespace(), Total: total}
	return marshalBackfillMeta(meta)
}

// resumableBackfillMeta looks for the most recent backfill job that stopped
// partway through this exact frozen window under this exact embedding
// configuration, and returns its checkpoint. A job that ran to completion
// isn't a resume candidate - a fresh window/config combination starts clean -
// and neither is one whose progress never advanced past the first page,
// since a fresh scan there costs nothing extra anyway.
func (s *Service) resumableBackfillMeta(ctx context.Context, start, end time.Time, signature string) (BackfillJobMeta, bool, error) {
	if s.backfillJobs == nil {
		return BackfillJobMeta{}, false, nil
	}
	last, err := s.backfillJobs.GetLatestSidekiqJobByKind(ctx, BackfillJobKind)
	if err != nil {
		return BackfillJobMeta{}, false, fmt.Errorf("look up latest Warp backfill job: %w", err)
	}
	if last == nil {
		return BackfillJobMeta{}, false, nil
	}
	if last.Status != tables.SidekiqStatusFailed && last.Status != tables.SidekiqStatusCancelled {
		return BackfillJobMeta{}, false, nil
	}
	var meta BackfillJobMeta
	if sonic.Unmarshal([]byte(last.Metadata), &meta) != nil {
		return BackfillJobMeta{}, false, nil
	}
	if meta.CursorTime == nil || !meta.StartTime.Equal(start) || !meta.EndTime.Equal(end) || meta.ConfigSignature != signature {
		return BackfillJobMeta{}, false, nil
	}
	meta.LastError = ""
	meta.Message = fmt.Sprintf("Resuming from log %d of %d.", meta.Scanned, meta.Total)
	return meta, true, nil
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

		outcomes, pageErr := s.indexBackfillPage(ctx, result.Logs)
		if pageErr != nil {
			// The page was cut short - by external cancellation, or a fetch that
			// failed before any log in it was even attempted: nothing in it is
			// counted, and the cursor does not advance past a page that never
			// finished, matching a clean stop between pages.
			meta.Message = fmt.Sprintf("Stopped after scanning %d log(s).", meta.Scanned)
			_ = progress(snapshot())
			return snapshot(), pageErr
		}

		// Snapshotted so a mid-page abort below can roll the page back out of the
		// checkpoint entirely, rather than leaving counters that ran ahead of a
		// cursor still parked at the page's start.
		pageStartScanned, pageStartIndexed, pageStartSkipped, pageStartFailed := meta.Scanned, meta.Indexed, meta.Skipped, meta.Failed
		for index := range outcomes {
			meta.Scanned++
			item := outcomes[index]
			failed := true
			switch {
			case item.err != nil:
				meta.LastError = item.err.Error()
			case item.outcome == IndexOutcomeSkipped:
				failed = false
				meta.Skipped++
			default:
				failed = false
				meta.Indexed++
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
				// does not spend hours failing. The whole page is rolled back out of
				// the checkpoint - counters to their pre-page values, cursor already
				// unmoved - so a resume (once the provider is fixed) retries every log
				// in it instead of leaving a prefix double-counted against a cursor
				// that never advanced past this page.
				meta.Scanned, meta.Indexed, meta.Skipped, meta.Failed = pageStartScanned, pageStartIndexed, pageStartSkipped, pageStartFailed
				meta.Message = fmt.Sprintf("Stopped after %d consecutive failures.", consecutiveFailures)
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

// backfillPageOutcome is one log's indexing result within a page.
type backfillPageOutcome struct {
	outcome IndexOutcome
	err     error
}

// indexBackfillPage embeds and upserts every log in a page concurrently,
// bounded by backfillIndexConcurrency, instead of one at a time - each call
// is a network round trip to the embedding provider, so a page mostly waited
// on the wire rather than the CPU. Logs are also fetched in one batched call
// rather than one round trip per row.
//
// The returned outcomes cover only the logs that finished, in their original
// page order; gaps from a log the run raced ahead of are simply omitted. A
// non-nil error means the page was cut short - either the fetch that seeds it
// never ran, or ctx was cancelled before the rest of it could - and the
// caller must not count anything from this page or advance the cursor past
// it.
func (s *Service) indexBackfillPage(ctx context.Context, logs []logstore.Log) ([]backfillPageOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, len(logs))
	for index := range logs {
		ids[index] = logs[index].ID
	}
	entries, err := s.logs.GetLogsByIDs(ctx, ids)
	if err != nil {
		// A page-level error, not len(logs) per-log ones: nothing in the page was
		// even fetched, so nothing about it should be counted as scanned or
		// failed, and the caller must retry the whole page rather than treat it
		// as resolved.
		return nil, fmt.Errorf("fetch logs for Warp backfill: %w", err)
	}
	entryByID := make(map[string]*logstore.Log, len(entries))
	for index := range entries {
		entryByID[entries[index].ID] = &entries[index]
	}

	done := make([]bool, len(logs))
	results := make([]backfillPageOutcome, len(logs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(backfillIndexConcurrency)
	for index := range logs {
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			entry, ok := entryByID[logs[index].ID]
			if !ok {
				results[index] = backfillPageOutcome{err: errors.New("log disappeared during backfill")}
				done[index] = true
				return nil
			}
			outcome, indexErr := s.indexer.Index(groupCtx, entry)
			if indexErr != nil && ctx.Err() != nil {
				// ctx was cancelled while this log was mid-flight. A real indexing
				// failure at this log would look identical from here, but this is the
				// caller stopping, not the log's fault: leave it out of results
				// entirely (done stays false) so it is omitted from outcomes below
				// rather than counted as failed, and propagate the cancellation so
				// RunBackfillJob knows the page was cut short.
				return ctx.Err()
			}
			results[index] = backfillPageOutcome{outcome: outcome, err: indexErr}
			done[index] = true
			return nil
		})
	}
	waitErr := group.Wait()

	outcomes := make([]backfillPageOutcome, 0, len(logs))
	for index := range logs {
		if done[index] {
			outcomes = append(outcomes, results[index])
		}
	}
	return outcomes, waitErr
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
