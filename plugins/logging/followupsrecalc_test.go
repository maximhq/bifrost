package logging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// countingRecalcStore wraps fakeRecalcStore and fills Stats.TotalRequests on
// every read that asks for a count, the way RDBLogStore does. It records each
// counted read so tests can pin how many full COUNT queries a walk issues.
type countingRecalcStore struct {
	*fakeRecalcStore
	countedReads []logstore.PaginationOptions
	pageReads    []logstore.PaginationOptions
}

// SearchLogs serves the page from the inner fake and, unless SkipCount is set,
// counts every row matching the filters (the real count ignores the cursor).
func (s *countingRecalcStore) SearchLogs(ctx context.Context, f logstore.SearchFilters, p logstore.PaginationOptions) (*logstore.SearchResult, error) {
	res, err := s.fakeRecalcStore.SearchLogs(ctx, f, p)
	if err != nil || f.RequestID != "" {
		return res, err
	}
	s.pageReads = append(s.pageReads, p)
	if !p.SkipCount {
		s.countedReads = append(s.countedReads, p)
		all, err := s.fakeRecalcStore.SearchLogs(ctx, f, logstore.PaginationOptions{})
		if err != nil {
			return nil, err
		}
		res.Stats.TotalRequests = int64(len(all.Logs))
	}
	return res, nil
}

// SearchLogsForBilling routes through the counting SearchLogs so billing page
// reads are recorded too.
func (s *countingRecalcStore) SearchLogsForBilling(ctx context.Context, f logstore.SearchFilters, p logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return s.SearchLogs(ctx, f, p)
}

// TestRecalculateCostsWithProgress_CountsOnceAndPagesByKeyset pins that the
// synchronous recalc path runs one exact COUNT for the progress total (plus the
// final remaining count), reads every page with SkipCount and a (timestamp, id)
// keyset after the first, and still visits every row exactly once in both
// scopes, including rows that stay uncosted and ties across page boundaries.
func TestRecalculateCostsWithProgress_CountsOnceAndPagesByKeyset(t *testing.T) {
	for _, missingOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("missingCostOnly=%v", missingOnly), func(t *testing.T) {
			base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			const rows = 23
			logs := make([]logstore.Log, 0, rows)
			for i := range rows {
				ts := base.Add(time.Duration(i/4) * time.Millisecond)
				id := fmt.Sprintf("row-%03d", i)
				if i%5 == 0 {
					// Stays uncosted, so it keeps matching MissingCostOnly.
					logs = append(logs, skipLog(id, ts))
				} else {
					logs = append(logs, positiveLog(id, ts))
				}
			}
			inner := newFakeRecalcStore(logs)
			store := &countingRecalcStore{fakeRecalcStore: inner}
			p := &LoggerPlugin{store: store, pricingManager: newTestPricingManager(t), logger: testLogger{}}

			var progressTotals []int64
			res, err := p.RecalculateCostsWithProgress(context.Background(), logstore.SearchFilters{MissingCostOnly: missingOnly}, 3, func(pr RecalculateCostProgress) {
				progressTotals = append(progressTotals, pr.TotalMatched)
			})
			if err != nil {
				t.Fatalf("RecalculateCostsWithProgress() error = %v", err)
			}
			if res.TotalMatched != rows {
				t.Fatalf("TotalMatched = %d, want %d", res.TotalMatched, rows)
			}
			for _, total := range progressTotals {
				if total != rows {
					t.Fatalf("progress TotalMatched = %d, want %d on every tick", total, rows)
				}
			}
			wantUpdated := rows - (rows+4)/5
			if res.Updated != wantUpdated {
				t.Fatalf("Updated = %d, want %d", res.Updated, wantUpdated)
			}
			for id, n := range inner.updateCount {
				if n != 1 {
					t.Fatalf("row %s updated %d times, want exactly 1", id, n)
				}
			}
			if len(inner.updateCount) != wantUpdated {
				t.Fatalf("distinct rows updated = %d, want %d", len(inner.updateCount), wantUpdated)
			}

			// One exact count for the total, one for Remaining at the end.
			if len(store.countedReads) != 2 {
				t.Fatalf("counted reads = %d, want 2 (total + remaining)", len(store.countedReads))
			}
			var pages []logstore.PaginationOptions
			for _, pg := range store.pageReads {
				if pg.SkipCount {
					pages = append(pages, pg)
				}
			}
			if len(pages) != len(store.pageReads)-2 {
				t.Fatalf("page reads that counted = %d, want 0", len(store.pageReads)-2-len(pages))
			}
			if len(pages) == 0 {
				t.Fatalf("no page reads recorded")
			}
			for i, pg := range pages {
				if pg.Offset != 0 {
					t.Fatalf("page %d uses offset %d, want keyset paging", i, pg.Offset)
				}
				if i > 0 && (pg.AfterTimestamp == nil || pg.AfterID == "") {
					t.Fatalf("page %d has no keyset cursor", i)
				}
			}
		})
	}
}
