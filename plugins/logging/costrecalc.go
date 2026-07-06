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
// metadata checkpoint, so this also bounds how much work a crash can lose.
const costRecalcBatchSize = 1000

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
	// Total is the number of in-scope rows counted at enqueue, for a determinate
	// progress bar. Processed can slightly exceed it because the inclusive cursor
	// may re-touch a boundary timestamp; treat progress as approximate.
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
// metadata JSON. Per-row cost updates are idempotent, so resuming from the cursor
// (which may re-touch the boundary timestamp) is safe.
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
	snapshot := func() string {
		data, err := sonic.Marshal(&meta)
		if err != nil {
			return metaJSON
		}
		return string(data)
	}

	windowStart := meta.Filters.StartTime
	windowEnd := meta.Filters.EndTime

	filters := meta.Filters
	filters.MissingCostOnly = meta.MissingCostOnly

	pagination := logstore.PaginationOptions{
		Limit:  costRecalcBatchSize,
		Offset: 0,
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

		searchResult, err := p.store.SearchLogs(ctx, filters, pagination)
		if err != nil {
			return snapshot(), fmt.Errorf("failed to search logs for cost recalculation: %w", err)
		}
		batch := searchResult.Logs
		if len(batch) == 0 {
			break
		}

		costUpdates := make(map[string]float64, len(batch))
		for i := range batch {
			logEntry := batch[i]
			cost, calcErr := p.calculateCostForLog(&logEntry)
			if calcErr != nil {
				meta.Skipped++
				p.logger.Debug("skipping cost recalculation for log %s: %v", logEntry.ID, calcErr)
				continue
			}
			if cost <= 0 {
				if isKnownZeroCostLog(&logEntry) {
					costUpdates[logEntry.ID] = cost
				} else {
					meta.Skipped++
					p.logger.Debug("skipping cost recalculation for log %s: resolved cost is zero", logEntry.ID)
				}
				continue
			}
			costUpdates[logEntry.ID] = cost
		}

		if len(costUpdates) > 0 {
			if err := p.store.BulkUpdateCost(ctx, costUpdates); err != nil {
				return snapshot(), fmt.Errorf("failed to bulk update costs: %w", err)
			}
			meta.Updated += len(costUpdates)
		}
		meta.Processed += len(batch)

		lastTs := batch[len(batch)-1].Timestamp
		// Anti-stall: an inclusive lower bound re-fetches rows sharing the cursor's
		// timestamp. If a full batch is entirely at the cursor's instant, there may
		// be more rows at that same instant than a batch holds and a pure inclusive
		// cursor would loop forever. Nudge one nanosecond past to guarantee forward
		// progress. In MissingCostOnly mode updated rows drop out, so this only bites
		// the pathological all-same-timestamp full-recalc case.
		if meta.CursorTime != nil && lastTs.Equal(*meta.CursorTime) && len(batch) == costRecalcBatchSize {
			lastTs = lastTs.Add(time.Nanosecond)
		}
		cursor := lastTs
		meta.CursorTime = &cursor

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
