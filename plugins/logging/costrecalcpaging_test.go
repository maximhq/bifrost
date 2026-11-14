package logging

import (
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// imageLog is a row priced from its image_generation_output payload, so the
// job must re-read it with the full billing projection before pricing.
func imageLog(id string, ts time.Time) logstore.Log {
	return logstore.Log{
		ID:        id,
		Timestamp: ts,
		Provider:  "openai",
		Model:     "gpt-image-1",
		Object:    "image_generation",
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens: 10,
			TotalTokens:  10,
		},
	}
}

// TestRunCostRecalcJob_PagesByKeysetWithoutCounting pins H23: the job pages the
// window about 500 rows at a time by a (timestamp, id) keyset, never asks the
// store for a total count, never materializes payload columns on the page,
// still hydrates at most BillingHydrationChunkSize rows at once, and prices
// every row exactly once, including rows that share a timestamp across a page
// boundary.
func TestRunCostRecalcJob_PagesByKeysetWithoutCounting(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const rows = 1203
	logs := make([]logstore.Log, 0, rows)
	for i := range rows {
		// Groups of four rows share a timestamp, so ties straddle page boundaries.
		logs = append(logs, positiveLog(fmt.Sprintf("row-%05d", i), base.Add(time.Duration(i/4)*time.Millisecond)))
	}
	store := newFakeRecalcStore(logs)
	p := newRecalcPlugin(t, store)

	final, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), Total: rows})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Processed != rows || final.Updated != rows {
		t.Fatalf("Processed=%d Updated=%d, want %d each", final.Processed, final.Updated, rows)
	}
	for id, n := range store.updateCount {
		if n != 1 {
			t.Fatalf("row %s updated %d times, want exactly 1", id, n)
		}
	}
	if len(store.updateCount) != rows {
		t.Fatalf("distinct rows updated = %d, want %d", len(store.updateCount), rows)
	}

	if len(store.paginations) > 4 {
		t.Fatalf("window pages = %d, want at most 4 for %d rows at ~500 per page", len(store.paginations), rows)
	}
	for i, pg := range store.paginations {
		if !pg.SkipCount {
			t.Fatalf("page %d asked for a total count; the walk never reads it", i)
		}
		if !pg.OmitBillingPayloads {
			t.Fatalf("page %d materializes payload columns; a 500-row page must carry scalars only", i)
		}
		if pg.Limit < 500 {
			t.Fatalf("page %d limit = %d, want about 500", i, pg.Limit)
		}
		if i > 0 && (pg.AfterTimestamp == nil || pg.AfterID == "" || pg.Offset != 0) {
			t.Fatalf("page %d must continue from a keyset cursor, got after=%v/%q offset=%d", i, pg.AfterTimestamp, pg.AfterID, pg.Offset)
		}
	}
	for _, size := range store.hydrateChunkSizes {
		if size > logstore.BillingHydrationChunkSize {
			t.Fatalf("hydration chunk of %d exceeds BillingHydrationChunkSize=%d", size, logstore.BillingHydrationChunkSize)
		}
	}
	if final.CursorID != logs[rows-1].ID {
		t.Fatalf("CursorID = %q, want the last row %q", final.CursorID, logs[rows-1].ID)
	}
}

// TestRunCostRecalcJob_RereadsPayloadRowsIndividually pins that a row priced
// from a modality payload is re-read on its own with the full projection, so
// only one payload-bearing row is ever resident per read.
func TestRunCostRecalcJob_RereadsPayloadRowsIndividually(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	logs := []logstore.Log{
		positiveLog("a", base),
		imageLog("b", base.Add(time.Second)),
		positiveLog("c", base.Add(2*time.Second)),
		imageLog("d", base.Add(3*time.Second)),
	}
	store := newFakeRecalcStore(logs)
	p := newRecalcPlugin(t, store)

	final, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), Total: 4})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Processed != 4 {
		t.Fatalf("Processed = %d, want 4", final.Processed)
	}
	if store.byIDCalls != 2 {
		t.Fatalf("payload re-reads = %d, want 2 (one per image row)", store.byIDCalls)
	}
}

// TestRunCostRecalcJob_ResumesLegacyOffsetCheckpoint pins that a checkpoint
// written before the keyset cursor (CursorTime + CursorOffset, no CursorID)
// resumes without skipping or repeating a row.
func TestRunCostRecalcJob_ResumesLegacyOffsetCheckpoint(t *testing.T) {
	restore := costRecalcPageSize
	costRecalcPageSize = 3
	defer func() { costRecalcPageSize = restore }()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var logs []logstore.Log
	for i := range 8 {
		logs = append(logs, positiveLog(fmt.Sprintf("r%02d", i), base))
	}
	store := newFakeRecalcStore(logs)
	p := newRecalcPlugin(t, store)
	// Legacy state: two rows at base already processed.
	cursor := base
	final, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), Total: 8, CursorTime: &cursor, CursorOffset: 2, Processed: 2, Updated: 2})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Processed != 8 {
		t.Fatalf("Processed = %d, want 8", final.Processed)
	}
	for i := range 8 {
		id := fmt.Sprintf("r%02d", i)
		want := 1
		if i < 2 {
			want = 0
		}
		if store.updateCount[id] != want {
			t.Fatalf("row %s updated %d times, want %d", id, store.updateCount[id], want)
		}
	}
}
