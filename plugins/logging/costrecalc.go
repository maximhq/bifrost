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

// costRecalcBatchSize is the number of rows processed between checkpoints. Each
// batch is a single SearchLogs page followed by one BulkUpdateCost and one
// metadata checkpoint, so this also bounds how much work a crash can lose. It is
// a var (not a const) only so tests can shrink it to exercise multi-batch paths;
// production never reassigns it.
var costRecalcBatchSize = 1000

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
	// CursorTime is the inclusive lower time bound for the next batch. Nil means
	// start from the window start (Filters.StartTime). It advances to the last
	// processed row's timestamp after each batch so a resume continues from there.
	CursorTime *time.Time `json:"cursor_time,omitempty"`
	// CursorOffset is how many rows at exactly CursorTime have already been processed
	// and still match the scope (so they reappear at the head of the next page). The
	// next batch queries timestamp >= CursorTime with this offset, which walks through
	// any number of rows sharing one timestamp without re-touching or skipping a single
	// one — the (timestamp ASC, id ASC) ordering keeps the offset stable.
	CursorOffset int `json:"cursor_offset,omitempty"`
	// Total is the number of in-scope rows counted at enqueue, for a determinate
	// progress bar. Processed can drift slightly if the underlying rows change between
	// the enqueue-time count and the walk; treat progress as approximate.
	Total     int64 `json:"total"`
	Processed int   `json:"processed"`
	Updated   int   `json:"updated"`
	Skipped   int   `json:"skipped"`
	// Message carries a human-readable completion note for the UI.
	Message string `json:"message,omitempty"`
}

// CountRecalcTargets returns how many logs fall in scope for a cost recalculation
// with the given filters. missingCostOnly narrows to rows that currently have no
// cost. It counts via the same raw-table SearchLogs path the worker walks (which
// honors MissingCostOnly), so the number matches the job's Total exactly — unlike
// the stats endpoint, which can fall back to hourly matviews that cannot filter on
// per-row missing cost. The caller must have already resolved any period into
// filters.StartTime/EndTime.
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
		Limit:  costRecalcBatchSize,
		SortBy: "timestamp",
		Order:  "asc",
	}

	for {
		if err := ctx.Err(); err != nil {
			// Cancelled (shutdown). Persist the cursor so a resume continues here.
			_ = checkpoint(snapshot())
			return snapshot(), err
		}

		lower := windowStart
		if meta.CursorTime != nil {
			lower = meta.CursorTime
		}
		filters.StartTime = lower
		filters.EndTime = windowEnd
		pagination.Offset = meta.CursorOffset

		searchResult, err := p.store.SearchLogs(ctx, filters, pagination)
		if err != nil {
			return snapshot(), fmt.Errorf("failed to search logs for cost recalculation: %w", err)
		}
		batch := searchResult.Logs
		if len(batch) == 0 {
			break
		}

		costUpdates := make(map[string]float64, len(batch))
		gotPositiveCost := make([]bool, len(batch))
		batchSkipped := 0
		for i := range batch {
			logEntry := batch[i]
			cost, calcErr := p.calculateCostForLog(&logEntry)
			if calcErr != nil {
				batchSkipped++
				p.logger.Debug("skipping cost recalculation for log %s: %v", logEntry.ID, calcErr)
				continue
			}
			if cost <= 0 {
				if isKnownZeroCostLog(&logEntry) {
					costUpdates[logEntry.ID] = cost
				} else {
					batchSkipped++
					p.logger.Debug("skipping cost recalculation for log %s: resolved cost is zero", logEntry.ID)
				}
				continue
			}
			costUpdates[logEntry.ID] = cost
			gotPositiveCost[i] = true
		}

		if len(costUpdates) > 0 {
			if err := p.store.BulkUpdateCost(ctx, costUpdates); err != nil {
				return snapshot(), fmt.Errorf("failed to bulk update costs: %w", err)
			}
			meta.Updated += len(costUpdates)
		}
		// Merge the skip count only once the batch is durably committed, so a retry
		// after a BulkUpdateCost failure cannot double-count the same skipped rows.
		meta.Skipped += batchSkipped
		meta.Processed += len(batch)

		// Advance the cursor. The lower bound is inclusive and rows that keep matching
		// the scope reappear at the head of the next page, so carry an offset counting
		// already-processed rows at exactly the cursor's timestamp. In full-recalc mode
		// every row keeps matching; in missing-cost mode only rows still without a
		// positive cost do (a skip, or a zero-cost resolution, still matches cost <= 0).
		// Counting those pages through any number of same-timestamp rows without
		// re-touching or skipping one.
		lastTs := batch[len(batch)-1].Timestamp
		stayedAtLastTs := 0
		for i := len(batch) - 1; i >= 0 && batch[i].Timestamp.Equal(lastTs); i-- {
			if !meta.MissingCostOnly || !gotPositiveCost[i] {
				stayedAtLastTs++
			}
		}

		if meta.CursorTime != nil && lastTs.Equal(*meta.CursorTime) {
			// The whole batch sat at the cursor's timestamp; keep the cursor and grow
			// the offset so the next page continues past the rows just handled.
			meta.CursorOffset += stayedAtLastTs
		} else {
			cursor := lastTs
			meta.CursorTime = &cursor
			meta.CursorOffset = stayedAtLastTs
		}

		if err := checkpoint(snapshot()); err != nil {
			return snapshot(), fmt.Errorf("failed to checkpoint cost recalc progress: %w", err)
		}

		if len(batch) < costRecalcBatchSize {
			break
		}
	}

	meta.Message = fmt.Sprintf("Recalculated %d cost value(s); %d skipped.", meta.Updated, meta.Skipped)
	return snapshot(), nil
}
