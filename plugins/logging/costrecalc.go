package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
)

// CostRecalcJobKind is the sidekiq job kind used for background cost recalculation.
const CostRecalcJobKind = "logs_recalculate_cost"

// costRecalcBatchSize caps the page of the synchronous RecalculateCostsWithProgress
// path, which reads SearchLogsForBilling with DB-resident modality payloads, so
// its page must be as small as the hydration chunk. The background job below
// pages with costRecalcPageSize instead.
var costRecalcBatchSize = logstore.BillingHydrationChunkSize

// costRecalcPageSize is the background job's DB page size and the number of rows
// processed between checkpoints. The page is read with OmitBillingPayloads, so
// it carries scalar pricing inputs only and 500 rows stay small; rows priced from
// a payload are re-read one at a time (see priceRecalcPage) and hydration still
// runs BillingHydrationChunkSize rows at a time. A page of 3 with a full COUNT of
// the window per page made a 90-day recalculation cost hundreds of millions of
// queries. It is a var only so tests can exercise multi-page paths; production
// never reassigns it.
var costRecalcPageSize = 500

// CostRecalcJobMeta is the durable state of a cost-recalculation job. It is stored
// verbatim as the sidekiq job's metadata JSON, so the worker can resume from the
// cursor after a restart or crash. The runner treats it as opaque.
type CostRecalcJobMeta struct {
	// Filters is the base search filter with the time window frozen at enqueue
	// (StartTime = window start, EndTime = window end). Its MissingCostOnly field
	// is ignored in favor of the explicit MissingCostOnly below so the scope is
	// unambiguous across resumes.
	Filters logstore.SearchFilters `json:"filters"`
	// MissingCostOnly selects the scope: true recalculates only rows without a
	// cost, false recalculates every row in the window.
	MissingCostOnly bool `json:"missing_cost_only"`
	// CursorTime and CursorID are the (timestamp, id) of the last processed row.
	// The next page starts strictly after them in (timestamp ASC, id ASC) order,
	// so a resume neither repeats nor skips a row, including rows that share a
	// timestamp. Nil CursorTime means start from the window start.
	CursorTime *time.Time `json:"cursor_time,omitempty"`
	CursorID   string     `json:"cursor_id,omitempty"`
	// CursorOffset is the pre-keyset cursor: how many rows at exactly CursorTime
	// had been processed and still matched the scope. It is only read to resume a
	// checkpoint written before CursorID existed (CursorTime set, CursorID empty);
	// the first page after such a resume switches the job to the keyset cursor.
	CursorOffset int `json:"cursor_offset,omitempty"`
	// Total is the number of in-scope rows counted at enqueue, for a determinate
	// progress bar. Treat it as approximate and never as the length of the walk.
	// It drifts when rows change between the enqueue-time count and the walk, and
	// on a full recalculation (MissingCostOnly false) it can come from a stale
	// materialized view; see CountRecalcTargets.
	Total     int64 `json:"total"`
	Processed int   `json:"processed"`
	Updated   int   `json:"updated"`
	Skipped   int   `json:"skipped"`
	// Unpriceable is the subset of Skipped left alone because their pricing inputs
	// could not be recovered — for example, an object-storage fetch that failed.
	// Broken out because it means "we refused to write a number we knew
	// would be wrong", which is actionable, whereas the rest of Skipped covers
	// ordinary cases like a row with no usage to price. It is a subset rather than a
	// sibling so Updated + Skipped still accounts for every Processed row.
	Unpriceable int `json:"unpriceable,omitempty"`
	// Message carries a human-readable completion note for the UI.
	Message string `json:"message,omitempty"`
}

// CostRecalcProgress extracts done/total/message from a cost-recalculation job's metadata
// for generic job views. Malformed metadata yields zero values rather than an error.
func CostRecalcProgress(metadata string) (done, total int64, message string) {
	var meta struct {
		Total     int64  `json:"total"`
		Processed int64  `json:"processed"`
		Message   string `json:"message"`
	}
	if sonic.Unmarshal([]byte(metadata), &meta) != nil {
		return 0, 0, ""
	}
	return meta.Processed, meta.Total, meta.Message
}

// stoppedEarlyMessage summarizes a run that ended before walking the whole window,
// so a cancelled (or shutdown-interrupted) job still reports what it committed.
// Costs already written are kept — they are correct, just incomplete in coverage.
func stoppedEarlyMessage(meta *CostRecalcJobMeta) string {
	msg := fmt.Sprintf("Stopped early after checking %d log(s): %d cost value(s) recalculated, %d skipped.", meta.Processed, meta.Updated, meta.Skipped)
	if meta.Total > 0 && int64(meta.Processed) < meta.Total {
		msg += fmt.Sprintf(" %d log(s) in the selected window were not checked.", meta.Total-int64(meta.Processed))
	}
	return msg
}

// CountRecalcTargets returns how many logs fall in scope for a cost recalculation
// with the given filters. missingCostOnly narrows to rows that currently have no
// cost. The caller must have already resolved any period into
// filters.StartTime/EndTime.
//
// How exact the count is depends on the scope. With missingCostOnly set, no
// materialized view can express a per-row missing-cost filter, so SearchLogs
// counts off the raw table and the number matches the job's Total exactly.
// Without it, SearchLogs is free to take its count from mv_logs_hourly on a
// matview-eligible window, and that view lags the raw table by its refresh
// interval; rows written or deleted since the last refresh are counted wrong.
// The result is a progress denominator only, never a bound on the walk: the
// worker pages the raw table until it runs out of rows, so a stale Total shows
// up as a progress bar that overshoots or finishes early, not as rows skipped
// or repriced twice.
func (p *LoggerPlugin) CountRecalcTargets(ctx context.Context, filters logstore.SearchFilters, missingCostOnly bool) (int64, error) {
	countFilters := filters
	countFilters.MissingCostOnly = missingCostOnly
	countResult, err := p.store.SearchLogs(ctx, countFilters, logstore.PaginationOptions{
		Limit:  1, // only Stats.TotalRequests is needed
		Offset: 0,
		SortBy: "timestamp",
		Order:  "asc",
	})
	if err != nil {
		return 0, fmt.Errorf("failed to count logs for cost recalculation: %w", err)
	}
	return countResult.Stats.TotalRequests, nil
}

// BuildCostRecalcJobMeta counts the in-scope rows and returns the initial job
// metadata JSON to enqueue. The caller is expected to have already resolved any
// period into filters.StartTime/EndTime (the frozen window).
func (p *LoggerPlugin) BuildCostRecalcJobMeta(ctx context.Context, filters logstore.SearchFilters, missingCostOnly bool) (string, error) {
	if p.pricingManager == nil {
		return "", fmt.Errorf("pricing manager is not configured")
	}
	// Count with the same scope the worker will apply so Total matches the work.
	total, err := p.CountRecalcTargets(ctx, filters, missingCostOnly)
	if err != nil {
		return "", err
	}
	meta := CostRecalcJobMeta{
		Filters:         filters,
		MissingCostOnly: missingCostOnly,
		Total:           total,
	}
	data, err := sonic.Marshal(&meta)
	if err != nil {
		return "", fmt.Errorf("failed to marshal cost recalc job metadata: %w", err)
	}
	return string(data), nil
}

// RunCostRecalcJob is the sidekiq handler body for CostRecalcJobKind. It walks the
// frozen window in timestamp order, one batch at a time, recomputing costs and
// checkpointing the cursor after each batch. It matches the shape sidekiq expects:
// given the current metadata JSON and a checkpoint callback, it returns the final
// metadata JSON. Per-row cost updates are idempotent, and the cursor carries an
// offset so a batch never re-touches or skips rows that share a timestamp, so
// resuming from a checkpoint is safe.
func (p *LoggerPlugin) RunCostRecalcJob(ctx context.Context, metaJSON string, checkpoint func(string) error) (string, error) {
	if p.pricingManager == nil {
		return metaJSON, fmt.Errorf("pricing manager is not configured")
	}
	var meta CostRecalcJobMeta
	if err := sonic.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return metaJSON, fmt.Errorf("failed to parse cost recalc job metadata: %w", err)
	}

	// snapshot marshals the current progress; on the rare marshal failure it falls
	// back to the last good JSON so the checkpoint/return still carries a cursor.
	lastGoodSnapshot := metaJSON
	snapshot := func() string {
		data, err := sonic.Marshal(&meta)
		if err != nil {
			return lastGoodSnapshot
		}
		lastGoodSnapshot = string(data)
		return lastGoodSnapshot
	}

	windowStart := meta.Filters.StartTime
	windowEnd := meta.Filters.EndTime

	filters := meta.Filters
	filters.MissingCostOnly = meta.MissingCostOnly

	pagination := logstore.PaginationOptions{
		Limit:  costRecalcPageSize,
		SortBy: "timestamp",
		Order:  "asc",
		// The walk never reads a total, and counting the window on every page
		// was the dominant cost of a recalculation.
		SkipCount: true,
		// Scalars only: a 500-row page of image or audio payloads would be
		// gigabytes. Payload rows are re-read individually in priceRecalcPage.
		OmitBillingPayloads: true,
	}

	for {
		if err := ctx.Err(); err != nil {
			// Stopped early — either a user cancellation or a node shutdown. Record what
			// was done and persist the cursor; on shutdown a resume continues from here,
			// on a cancellation the checkpoint is rejected (the row is no longer running)
			// and the runner stores this same snapshot against the cancelled job instead.
			meta.Message = stoppedEarlyMessage(&meta)
			_ = checkpoint(snapshot())
			return snapshot(), err
		}

		lower := windowStart
		if meta.CursorTime != nil {
			lower = meta.CursorTime
		}
		filters.StartTime = lower
		filters.EndTime = windowEnd
		if meta.CursorTime != nil && meta.CursorID != "" {
			pagination.AfterTimestamp = meta.CursorTime
			pagination.AfterID = meta.CursorID
			pagination.Offset = 0
		} else {
			// First page, or a checkpoint written before the keyset cursor existed:
			// fall back to the inclusive lower bound plus the carried offset once.
			pagination.AfterTimestamp = nil
			pagination.AfterID = ""
			pagination.Offset = meta.CursorOffset
		}

		// Billing reads go through SearchLogsForBilling, not SearchLogs: the list
		// projection omits the modality output payloads and, on object-storage-backed
		// stores, returns rows whose token_usage was blanked at write time. Pricing
		// those degraded rows charges cached tokens at the full input rate.
		searchResult, err := p.store.SearchLogsForBilling(ctx, filters, pagination)
		if err != nil {
			return snapshot(), fmt.Errorf("failed to search logs for cost recalculation: %w", err)
		}
		batch := searchResult.Logs
		if len(batch) == 0 {
			break
		}

		// Re-reads payload rows one at a time, then hydrates and prices the page
		// BillingHydrationChunkSize rows at a time.
		outcomes, err := p.priceRecalcPage(ctx, batch)
		if err != nil {
			return snapshot(), err
		}

		// Shared with RecalculateCostsWithProgress so the two paths cannot drift on
		// how a page is persisted — notably the batch-aggregate rows, which carry a
		// scalar total instead of a breakdown.
		tally, err := p.persistRecalcOutcomes(ctx, batch, outcomes)
		if err != nil {
			return snapshot(), err
		}
		// Merge the counts only once the batch is durably committed, so a retry
		// after a failed write cannot double-count the same rows.
		meta.Updated += tally.updated
		meta.Skipped += tally.skipped
		meta.Unpriceable += tally.unpriceable
		meta.Processed += len(batch)

		// Advance the keyset cursor past the last row of the page. Rows before it
		// are never revisited, whether or not they still match the scope, which
		// visits exactly the rows the old timestamp-plus-offset cursor visited.
		last := batch[len(batch)-1]
		cursor := last.Timestamp
		meta.CursorTime = &cursor
		meta.CursorID = last.ID
		meta.CursorOffset = 0

		if err := checkpoint(snapshot()); err != nil {
			// A cancellation flips the job row out of running, so the checkpoint is
			// rejected before the loop gets back to its ctx.Err() guard. Report the
			// cancellation, not the checkpoint write, as the reason the job stopped.
			if cerr := ctx.Err(); cerr != nil {
				meta.Message = stoppedEarlyMessage(&meta)
				return snapshot(), cerr
			}
			return snapshot(), fmt.Errorf("failed to checkpoint cost recalc progress: %w", err)
		}

		if len(batch) < costRecalcPageSize {
			break
		}
	}

	meta.Message = fmt.Sprintf("Recalculated %d cost value(s); %d skipped.", meta.Updated, meta.Skipped)
	if meta.Unpriceable > 0 {
		meta.Message += fmt.Sprintf(" %d of those were left unchanged because their pricing inputs were unavailable.", meta.Unpriceable)
	}
	return snapshot(), nil
}

// priceRecalcPage prices one scalar-only page (read with OmitBillingPayloads),
// BillingHydrationChunkSize rows at a time. Before each chunk is priced, every
// row whose object type bills on a modality payload is re-read on its own with
// the full billing projection, so at most one chunk of payloads is resident at
// once, the same bound the old 3-row pages gave. A row deleted between the page
// read and its re-read is skipped rather than priced without its payload.
// It returns one outcome per page row, in page order.
func (p *LoggerPlugin) priceRecalcPage(ctx context.Context, page []logstore.Log) ([]billingOutcome, error) {
	outcomes := make([]billingOutcome, len(page))
	for start := 0; start < len(page); start += logstore.BillingHydrationChunkSize {
		end := min(start+logstore.BillingHydrationChunkSize, len(page))

		chunk := make([]logstore.Log, 0, end-start)
		positions := make([]int, 0, end-start)
		for i := start; i < end; i++ {
			row := page[i]
			if logstore.BillingPayloadRequired(row.Object) {
				full, err := p.store.SearchLogsForBilling(ctx, logstore.SearchFilters{RequestID: row.ID}, logstore.PaginationOptions{Limit: 1, SkipCount: true})
				if err != nil {
					return nil, fmt.Errorf("failed to read pricing payload for log %s: %w", row.ID, err)
				}
				if len(full.Logs) == 0 {
					outcomes[i].err = fmt.Errorf("log %s was deleted before its pricing payload could be read", row.ID)
					continue
				}
				row = full.Logs[0]
			}
			chunk = append(chunk, row)
			positions = append(positions, i)
		}
		if len(chunk) == 0 {
			continue
		}

		chunkOutcomes, err := p.priceLogsInChunks(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for j, i := range positions {
			outcomes[i] = chunkOutcomes[j]
		}
	}
	return outcomes, nil
}
