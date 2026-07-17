package logging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// fakeRecalcStore is a minimal in-memory LogStore that emulates the pieces of
// SearchLogs/BulkUpdateCost the cost-recalc job depends on: an inclusive
// timestamp lower bound, offset/limit paging over a (timestamp, id)-ordered set,
// and MissingCostOnly excluding rows that already carry a positive cost. Embedding
// the interface satisfies the ~60 unused methods; only the two the job calls are
// overridden. Any other call panics, surfacing an unexpected dependency.
type fakeRecalcStore struct {
	logstore.LogStore
	logs           []logstore.Log // pre-sorted by (timestamp, id)
	cost           map[string]float64
	hasCost        map[string]bool
	updateCount    map[string]int // successful BulkUpdateCost touches per id
	searchCalls    int
	bulkCalls      int
	failBulkOnCall int // 1-based bulk call number to fail; 0 = never
}

func newFakeRecalcStore(logs []logstore.Log) *fakeRecalcStore {
	return &fakeRecalcStore{
		logs:        logs,
		cost:        make(map[string]float64),
		hasCost:     make(map[string]bool),
		updateCount: make(map[string]int),
	}
}

func (s *fakeRecalcStore) SearchLogs(_ context.Context, f logstore.SearchFilters, p logstore.PaginationOptions) (*logstore.SearchResult, error) {
	s.searchCalls++
	if s.searchCalls > 1000 {
		return nil, fmt.Errorf("SearchLogs called too many times; likely an infinite loop")
	}
	var matched []logstore.Log
	for _, l := range s.logs {
		if f.StartTime != nil && l.Timestamp.Before(*f.StartTime) {
			continue
		}
		if f.EndTime != nil && l.Timestamp.After(*f.EndTime) {
			continue
		}
		// Mirrors the raw-table predicate: cost <= 0 (or unset) still matches.
		if f.MissingCostOnly && s.hasCost[l.ID] && s.cost[l.ID] > 0 {
			continue
		}
		matched = append(matched, l)
	}
	start := min(p.Offset, len(matched))
	end := len(matched)
	if p.Limit > 0 && start+p.Limit < end {
		end = start + p.Limit
	}
	page := append([]logstore.Log(nil), matched[start:end]...)
	return &logstore.SearchResult{Logs: page}, nil
}

func (s *fakeRecalcStore) BulkUpdateCost(_ context.Context, updates map[string]float64) error {
	s.bulkCalls++
	if s.failBulkOnCall != 0 && s.bulkCalls == s.failBulkOnCall {
		return fmt.Errorf("simulated bulk update failure")
	}
	for id, c := range updates {
		s.cost[id] = c
		s.hasCost[id] = true
		s.updateCount[id]++
	}
	return nil
}

// positiveLog resolves to a strictly positive cost via the test pricing datasheet
// (gpt-4o has non-zero input/output rates), so recalc updates it.
func positiveLog(id string, ts time.Time) logstore.Log {
	return logstore.Log{
		ID:        id,
		Timestamp: ts,
		Provider:  "openai",
		Model:     "gpt-4o",
		Object:    "chat.completion",
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}
}

// skipLog has no usage and no cache hit, so calculateCostForLog errors and recalc
// counts it as skipped (it keeps matching MissingCostOnly since it stays uncosted).
func skipLog(id string, ts time.Time) logstore.Log {
	return logstore.Log{
		ID:        id,
		Timestamp: ts,
		Provider:  "openai",
		Model:     "gpt-4o",
		Object:    "chat.completion",
	}
}

// bedrockMantleStreamLog mirrors a real bedrock_mantle streaming chat row. The
// datasheet files openai.gpt-5.5 under responses mode only, so pricing it
// requires both the bedrock_mantle→bedrock provider fold and the chat→responses
// mode fallback; without them recalc skips the row as zero-cost.
func bedrockMantleStreamLog(id string, ts time.Time) logstore.Log {
	return logstore.Log{
		ID:        id,
		Timestamp: ts,
		Provider:  "bedrock_mantle",
		Model:     "openai.gpt-5.5",
		Object:    "chat_completion_stream",
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     955,
			CompletionTokens: 3138,
			TotalTokens:      4093,
		},
	}
}

func newRecalcPlugin(t *testing.T, store *fakeRecalcStore) *LoggerPlugin {
	t.Helper()
	return &LoggerPlugin{
		store:          store,
		pricingManager: newTestPricingManager(t),
		logger:         testLogger{},
	}
}

// runJob marshals meta, runs the job collecting every checkpoint snapshot, and
// unmarshals the returned meta. It returns the final meta, the checkpoints, and
// the job error (if any).
func runJob(t *testing.T, p *LoggerPlugin, meta CostRecalcJobMeta) (CostRecalcJobMeta, []string, error) {
	t.Helper()
	in, err := sonic.Marshal(&meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	var checkpoints []string
	out, jobErr := p.RunCostRecalcJob(context.Background(), string(in), func(s string) error {
		checkpoints = append(checkpoints, s)
		return nil
	})
	var final CostRecalcJobMeta
	if uerr := sonic.Unmarshal([]byte(out), &final); uerr != nil {
		t.Fatalf("unmarshal returned meta: %v", uerr)
	}
	return final, checkpoints, jobErr
}

func window(base time.Time) logstore.SearchFilters {
	start := base.Add(-time.Hour)
	end := base.Add(time.Hour)
	return logstore.SearchFilters{StartTime: &start, EndTime: &end}
}

// TestCalculateCostForLog_BedrockMantleChatStreamUsesResponsesPricing pins the
// recalc entry point on a stored streaming chat row whose only datasheet entry is
// filed under responses mode: the cost must come back from the responses rates
// rather than zero.
func TestCalculateCostForLog_BedrockMantleChatStreamUsesResponsesPricing(t *testing.T) {
	p := newRecalcPlugin(t, newFakeRecalcStore(nil))
	entry := bedrockMantleStreamLog("mantle-1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	cost, err := p.calculateCostForLog(&entry)
	if err != nil {
		t.Fatalf("calculateCostForLog() error = %v", err)
	}
	// openai.gpt-5.5 testdata rates (responses mode): input 5.5e-6, output 3.3e-5.
	want := 955*5.5e-6 + 3138*3.3e-5
	if diff := cost - want; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("cost = %v, want %v (responses-mode rates via the chat→responses fallback)", cost, want)
	}
}

// TestRunCostRecalcJob_BackfillsBedrockMantleStreamRow drives the full
// missing-cost recalc job over an uncosted bedrock_mantle streaming row and
// proves it is now backfilled instead of counted as skipped.
func TestRunCostRecalcJob_BackfillsBedrockMantleStreamRow(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	store := newFakeRecalcStore([]logstore.Log{bedrockMantleStreamLog("mantle-1", base)})
	p := newRecalcPlugin(t, store)

	final, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), MissingCostOnly: true, Total: 1})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Updated != 1 {
		t.Errorf("Updated = %d, want 1", final.Updated)
	}
	if final.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 (the row must no longer resolve to zero cost)", final.Skipped)
	}
	want := 955*5.5e-6 + 3138*3.3e-5
	if diff := store.cost["mantle-1"] - want; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("persisted cost = %v, want %v", store.cost["mantle-1"], want)
	}
}

// TestRunCostRecalcJob_FullRecalcTiePagination proves the offset cursor walks
// through more same-timestamp rows than a single batch holds without skipping or
// re-touching any — the case the old one-nanosecond nudge silently dropped.
func TestRunCostRecalcJob_FullRecalcTiePagination(t *testing.T) {
	restore := costRecalcBatchSize
	costRecalcBatchSize = 3
	defer func() { costRecalcBatchSize = restore }()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := base // all rows share one instant
	var logs []logstore.Log
	for i := range 7 {
		logs = append(logs, positiveLog(fmt.Sprintf("r%02d", i), ts))
	}
	store := newFakeRecalcStore(logs)
	p := newRecalcPlugin(t, store)

	final, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), MissingCostOnly: false, Total: 7})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Updated != 7 {
		t.Errorf("Updated = %d, want 7 (every row must be costed exactly once)", final.Updated)
	}
	if final.Processed != 7 {
		t.Errorf("Processed = %d, want 7", final.Processed)
	}
	if final.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", final.Skipped)
	}
	for id, n := range store.updateCount {
		if n != 1 {
			t.Errorf("row %s updated %d times, want exactly 1", id, n)
		}
	}
	if len(store.updateCount) != 7 {
		t.Errorf("distinct rows updated = %d, want 7 (none silently skipped)", len(store.updateCount))
	}
}

// TestRunCostRecalcJob_MissingCostOnlyTiePagination interleaves rows that resolve
// to a positive cost (which leave the missing-cost set once updated) with rows
// that stay uncosted, all at one timestamp spanning multiple batches. The carried
// offset must skip exactly the already-seen rows that remain visible.
func TestRunCostRecalcJob_MissingCostOnlyTiePagination(t *testing.T) {
	restore := costRecalcBatchSize
	costRecalcBatchSize = 3
	defer func() { costRecalcBatchSize = restore }()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := base
	// pos, skip, pos, skip, pos, skip, pos
	logs := []logstore.Log{
		positiveLog("r00", ts),
		skipLog("r01", ts),
		positiveLog("r02", ts),
		skipLog("r03", ts),
		positiveLog("r04", ts),
		skipLog("r05", ts),
		positiveLog("r06", ts),
	}
	store := newFakeRecalcStore(logs)
	p := newRecalcPlugin(t, store)

	final, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), MissingCostOnly: true, Total: 7})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Updated != 4 {
		t.Errorf("Updated = %d, want 4", final.Updated)
	}
	if final.Skipped != 3 {
		t.Errorf("Skipped = %d, want 3", final.Skipped)
	}
	if final.Processed != 7 {
		t.Errorf("Processed = %d, want 7 (each row visited exactly once)", final.Processed)
	}
	for _, id := range []string{"r00", "r02", "r04", "r06"} {
		if store.updateCount[id] != 1 {
			t.Errorf("positive row %s updated %d times, want 1", id, store.updateCount[id])
		}
	}
	for _, id := range []string{"r01", "r03", "r05"} {
		if store.hasCost[id] {
			t.Errorf("skipped row %s must never be costed", id)
		}
	}
}

// TestRunCostRecalcJob_SkipCountNotInflatedOnRetry covers the checkpoint/error
// contract: a BulkUpdateCost failure returns the last committed snapshot without
// folding in the failed batch's skip count, so retrying from that snapshot cannot
// double-count skips or re-touch already-costed rows.
func TestRunCostRecalcJob_SkipCountNotInflatedOnRetry(t *testing.T) {
	restore := costRecalcBatchSize
	costRecalcBatchSize = 3
	defer func() { costRecalcBatchSize = restore }()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// Distinct timestamps, one skip per batch.
	logs := []logstore.Log{
		skipLog("r1", base.Add(1*time.Second)),
		positiveLog("r2", base.Add(2*time.Second)),
		positiveLog("r3", base.Add(3*time.Second)),
		skipLog("r4", base.Add(4*time.Second)),
		positiveLog("r5", base.Add(5*time.Second)),
		positiveLog("r6", base.Add(6*time.Second)),
	}
	store := newFakeRecalcStore(logs)
	store.failBulkOnCall = 2 // second batch's bulk update fails
	p := newRecalcPlugin(t, store)

	afterFail, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), MissingCostOnly: false, Total: 6})
	if err == nil {
		t.Fatal("expected error from failed BulkUpdateCost, got nil")
	}
	// Only batch 1 committed: r1 skipped, r2+r3 updated.
	if afterFail.Skipped != 1 {
		t.Errorf("Skipped after failure = %d, want 1 (failed batch must not fold in its skip)", afterFail.Skipped)
	}
	if afterFail.Updated != 2 {
		t.Errorf("Updated after failure = %d, want 2", afterFail.Updated)
	}
	if afterFail.Processed != 3 {
		t.Errorf("Processed after failure = %d, want 3", afterFail.Processed)
	}

	// Retry from the returned snapshot with the store now healthy.
	store.failBulkOnCall = 0
	final, _, err := runJob(t, p, afterFail)
	if err != nil {
		t.Fatalf("retry RunCostRecalcJob() error = %v", err)
	}
	if final.Skipped != 2 {
		t.Errorf("final Skipped = %d, want 2 (r1 and r4 once each, not inflated)", final.Skipped)
	}
	if final.Updated != 4 {
		t.Errorf("final Updated = %d, want 4", final.Updated)
	}
	if final.Processed != 6 {
		t.Errorf("final Processed = %d, want 6", final.Processed)
	}
	for _, id := range []string{"r2", "r3", "r5", "r6"} {
		if store.updateCount[id] != 1 {
			t.Errorf("row %s updated %d times across the failure+retry, want exactly 1", id, store.updateCount[id])
		}
	}
}

// TestRunCostRecalcJob_EmptyWindow confirms a job over an empty result set
// completes cleanly with a summary and no work.
func TestRunCostRecalcJob_EmptyWindow(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	store := newFakeRecalcStore(nil)
	p := newRecalcPlugin(t, store)

	final, checkpoints, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), MissingCostOnly: true})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Processed != 0 || final.Updated != 0 || final.Skipped != 0 {
		t.Errorf("expected zero counters, got Processed=%d Updated=%d Skipped=%d", final.Processed, final.Updated, final.Skipped)
	}
	if final.Message == "" {
		t.Error("expected a completion Message")
	}
	if len(checkpoints) != 0 {
		t.Errorf("expected no checkpoints for an empty window, got %d", len(checkpoints))
	}
}
