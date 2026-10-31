package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// A /results call that did not settle the batch gets a log row carrying a read-only
// copy of the settled price and no cost of its own. That NULL cost is final — no
// recalculation will ever fill it — so the row must stay out of "show missing cost
// only", which it used to flood.
func TestMissingCostOnlyExcludesBatchEchoRows(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC)
	settled := 1.25

	newBatchRow := func(id string, ts time.Time, cost *float64, echo bool) *Log {
		entry := &Log{
			ID:        id,
			Timestamp: ts,
			Object:    string(schemas.BatchResultsRequest),
			Provider:  "anthropic",
			Model:     "claude-sonnet-4",
			Status:    "success",
			Cost:      cost,
			BatchDebugParsed: &schemas.BifrostBatchDebug{
				BatchID: "batch_1",
				Accounting: &schemas.BatchAccountingDebug{
					Echo: echo,
					ModelBreakdowns: map[string]schemas.BatchModelBreakdown{
						"claude-sonnet-4": {Model: "claude-sonnet-4", RequestCount: 1},
					},
				},
			},
		}
		require.NoError(t, entry.SerializeFields())
		return entry
	}

	unpricedChat := &Log{
		ID:        "chat-unpriced",
		Timestamp: base,
		Object:    "chat.completion",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4",
		Status:    "success",
	}
	// An aggregate row that failed to price is exactly what the filter is for.
	unpricedAggregate := newBatchRow("batch-aggregate-unpriced", base.Add(time.Minute), nil, false)
	pricedAggregate := newBatchRow("batch-aggregate-priced", base.Add(2*time.Minute), &settled, false)
	echoRow := newBatchRow("batch-echo", base.Add(3*time.Minute), nil, true)

	for _, entry := range []*Log{unpricedChat, unpricedAggregate, pricedAggregate, echoRow} {
		require.NoError(t, store.Create(ctx, entry))
	}

	result, err := store.SearchLogs(ctx, SearchFilters{MissingCostOnly: true}, PaginationOptions{
		Limit: 50, SortBy: "timestamp", Order: "asc",
	})
	require.NoError(t, err)

	got := make([]string, 0, len(result.Logs))
	for _, l := range result.Logs {
		got = append(got, l.ID)
	}
	require.ElementsMatch(t, []string{"chat-unpriced", "batch-aggregate-unpriced"}, got,
		"only rows a recalculation can actually resolve belong in the missing-cost scope")
}
