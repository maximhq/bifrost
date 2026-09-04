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
	logs              []logstore.Log // pre-sorted by (timestamp, id)
	cost              map[string]float64
	split             map[string]logstore.CostUpdate // full per-category update per id
	hasCost           map[string]bool
	updateCount       map[string]int // successful BulkUpdateCost touches per id
	searchCalls       int
	bulkCalls         int
	failBulkOnCall    int   // 1-based bulk call number to fail; 0 = never
	hydrateChunkSizes []int // sizes handed to HydrateBillingChunk, in order
	backfilled        []int // sizes handed to BulkBackfillBillingPayloads, in order
	// updates captures single-row Update() writes, which is how a job kind's cost
	// and its debug blob are persisted together.
	updates map[string]map[string]any
}

func newFakeRecalcStore(logs []logstore.Log) *fakeRecalcStore {
	return &fakeRecalcStore{
		logs:        logs,
		cost:        make(map[string]float64),
		split:       make(map[string]logstore.CostUpdate),
		hasCost:     make(map[string]bool),
		updateCount: make(map[string]int),
		updates:     make(map[string]map[string]any),
	}
}

func (s *fakeRecalcStore) Update(_ context.Context, id string, entry any) error {
	fields, ok := entry.(map[string]any)
	if !ok {
		return fmt.Errorf("fakeRecalcStore.Update: want map[string]any, got %T", entry)
	}
	s.updates[id] = fields
	if c, ok := fields["cost"].(float64); ok {
		s.cost[id] = c
		s.hasCost[id] = true
	}
	return nil
}

// SearchLogsForBilling is what the recalc job actually calls. The fake has nothing
// offloaded to object storage, so it returns the same rows as SearchLogs — matching
// RDBLogStore, where every pricing input is already present in the row.
func (s *fakeRecalcStore) SearchLogsForBilling(ctx context.Context, f logstore.SearchFilters, p logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return s.SearchLogs(ctx, f, p)
}

// HydrateBillingChunk records the chunk sizes it is handed so tests can assert the
// caller never asks for more payloads at once than BillingHydrationChunkSize. Nothing
// is offloaded here, so there is nothing to fetch.
func (s *fakeRecalcStore) HydrateBillingChunk(_ context.Context, logs []*logstore.Log) (logstore.BillingHydrationResult, error) {
	s.hydrateChunkSizes = append(s.hydrateChunkSizes, len(logs))
	return logstore.BillingHydrationResult{}, nil
}

// BulkBackfillBillingPayloads should never be reached here: nothing is offloaded, so no
// row is ever hydrated and there is nothing recovered to write back.
func (s *fakeRecalcStore) BulkBackfillBillingPayloads(_ context.Context, updates map[string]logstore.BillingPayloadBackfill) error {
	s.backfilled = append(s.backfilled, len(updates))
	return nil
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

func (s *fakeRecalcStore) BulkUpdateCost(_ context.Context, updates map[string]logstore.CostUpdate) error {
	s.bulkCalls++
	if s.failBulkOnCall != 0 && s.bulkCalls == s.failBulkOnCall {
		return fmt.Errorf("simulated bulk update failure")
	}
	for id, c := range updates {
		s.cost[id] = c.Total
		s.split[id] = c
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

// TestRunCostRecalcJob_BackfillsCostSplit verifies recompute refreshes the
// denormalized input/output/additional columns, not just the total, so the split
// reconciles to the cost column after a reprice.
func TestRunCostRecalcJob_BackfillsCostSplit(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	store := newFakeRecalcStore([]logstore.Log{positiveLog("split-1", base)})
	p := newRecalcPlugin(t, store)

	if _, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base), MissingCostOnly: false, Total: 1}); err != nil {
		t.Fatalf("RunCostRecalcJob error = %v", err)
	}

	u, ok := store.split["split-1"]
	if !ok {
		t.Fatal("expected a cost update for split-1")
	}
	// gpt-4o testdata rates: input 2.5e-6/token, output 1e-5/token (100 prompt, 50 completion).
	wantIn, wantOut := 100*2.5e-6, 50*1e-5
	if d := u.Input - wantIn; d < -1e-12 || d > 1e-12 {
		t.Fatalf("input cost %v != %v", u.Input, wantIn)
	}
	if d := u.Output - wantOut; d < -1e-12 || d > 1e-12 {
		t.Fatalf("output cost %v != %v", u.Output, wantOut)
	}
	if d := u.Total - (u.Input + u.Output + u.Additional); d < -1e-12 || d > 1e-12 {
		t.Fatalf("split does not reconcile to total: total=%v in=%v out=%v add=%v", u.Total, u.Input, u.Output, u.Additional)
	}
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

// TestRunCostRecalcJob_HydratesInBoundedChunks pins the memory bound.
//
// A hydrated row holds its whole offloaded payload — potentially full message
// histories and raw request/response bodies — so a recompute must never ask for a
// whole batch of them at once. The job walks the batch in BillingHydrationChunkSize
// slices and releases each before advancing, which keeps peak payload memory flat no
// matter how large the batch or the recompute window is.
func TestRunCostRecalcJob_HydratesInBoundedChunks(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	const rows = 10
	logs := make([]logstore.Log, 0, rows)
	for i := range rows {
		logs = append(logs, positiveLog(fmt.Sprintf("chunk-%02d", i), base.Add(time.Duration(i)*time.Second)))
	}
	store := newFakeRecalcStore(logs)
	p := newRecalcPlugin(t, store)

	final, _, err := runJob(t, p, CostRecalcJobMeta{Filters: window(base)})
	if err != nil {
		t.Fatalf("RunCostRecalcJob() error = %v", err)
	}
	if final.Updated != rows {
		t.Fatalf("expected all %d rows priced, got Updated=%d", rows, final.Updated)
	}

	if len(store.hydrateChunkSizes) == 0 {
		t.Fatal("expected the job to hydrate through HydrateBillingChunk, but it never called it")
	}
	total := 0
	for _, size := range store.hydrateChunkSizes {
		if size > logstore.BillingHydrationChunkSize {
			t.Fatalf("hydration chunk of %d exceeds BillingHydrationChunkSize=%d; peak payload memory would scale with batch size, got chunks %v",
				size, logstore.BillingHydrationChunkSize, store.hydrateChunkSizes)
		}
		total += size
	}
	if total != rows {
		t.Fatalf("chunks covered %d rows, want every one of %d: %v", total, rows, store.hydrateChunkSizes)
	}
}

// A job kind reprices an aggregate row to a single scalar total and leaves
// breakdown nil; the generic path is the other way round. persistRecalcOutcomes
// reading the nil breakdown for a kind-owned outcome wrote CostUpdate{} — zeros —
// over a real provider-reported cost, and still counted the row as priced. This
// exercises the seam end to end rather than what RepriceFromLog returns, which is
// where that bug hid.
func TestPersistRecalcOutcomes_KindOwnedCostSurvivesPersistence(t *testing.T) {
	const id = "runware-settlement"
	store := newFakeRecalcStore([]logstore.Log{{ID: id}})
	plugin := newRecalcPlugin(t, store)

	// What VideoSettler.RepriceFromLog returns for a provider-reported cost: a
	// positive total, and no debug blob to write alongside it.
	outcomes := []billingOutcome{{cost: 0.42, kindOwned: true}}

	tally, err := plugin.persistRecalcOutcomes(context.Background(), []logstore.Log{{ID: id}}, outcomes)
	if err != nil {
		t.Fatalf("persistRecalcOutcomes: %v", err)
	}
	if got := store.cost[id]; got != 0.42 {
		t.Errorf("provider cost did not survive the reprice: got %v, want 0.42", got)
	}
	if got := store.split[id].Total; got != 0.42 {
		t.Errorf("split total = %v, want 0.42", got)
	}
	if !tally.priced[0] {
		t.Error("a repriced row must count as priced")
	}
}

// The generic path carries a per-category split. Resolving both paths through one
// helper must not flatten it into a bare total.
func TestPersistRecalcOutcomes_GenericBreakdownKeepsItsSplit(t *testing.T) {
	const id = "chat-row"
	store := newFakeRecalcStore([]logstore.Log{{ID: id}})
	plugin := newRecalcPlugin(t, store)

	outcomes := []billingOutcome{{
		cost:      0.30,
		breakdown: &schemas.BifrostCost{InputCost: 0.10, OutputCost: 0.20, TotalCost: 0.30},
	}}

	tally, err := plugin.persistRecalcOutcomes(context.Background(), []logstore.Log{{ID: id}}, outcomes)
	if err != nil {
		t.Fatalf("persistRecalcOutcomes: %v", err)
	}
	got := store.split[id]
	if got.Total != 0.30 || got.Input != 0.10 || got.Output != 0.20 {
		t.Errorf("the split collapsed: got total=%v input=%v output=%v, want 0.30/0.10/0.20",
			got.Total, got.Input, got.Output)
	}
	if !tally.priced[0] {
		t.Error("a repriced row must count as priced")
	}
}

// priced[i] means the row now carries a POSITIVE cost and will drop out of a
// MissingCostOnly scan; the caller advances its offset only past rows that stay.
// Marking a settled zero as priced stalls that cursor — the row keeps matching
// cost <= 0, reappears at the head of the next page, and the page repeats forever.
func TestPersistRecalcOutcomes_SettledZeroDoesNotStallTheCursor(t *testing.T) {
	const id = "failed-video"
	store := newFakeRecalcStore([]logstore.Log{{ID: id}})
	plugin := newRecalcPlugin(t, store)

	outcomes := []billingOutcome{{cost: 0, kindOwned: true, settledZero: true}}

	tally, err := plugin.persistRecalcOutcomes(context.Background(), []logstore.Log{{ID: id}}, outcomes)
	if err != nil {
		t.Fatalf("persistRecalcOutcomes: %v", err)
	}
	if tally.priced[0] {
		t.Error("a settled zero still matches cost <= 0, so it must not be reported as priced")
	}
	if !store.hasCost[id] {
		t.Error("the zero must still be written, not skipped")
	}
	if got := store.cost[id]; got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// A job kind can reprice an aggregate to zero and still have a refreshed debug
// blob to persist — a batch whose models priced but whose usage was zero. The
// zero branch used to return early and drop that blob, leaving stale breakdowns.
func TestPersistRecalcOutcomes_ZeroCostStillWritesDebugBlob(t *testing.T) {
	const id = "zero-usage-batch"
	store := newFakeRecalcStore([]logstore.Log{{ID: id}})
	plugin := newRecalcPlugin(t, store)

	outcomes := []billingOutcome{{
		cost:        0,
		kindOwned:   true,
		settledZero: true,
		debugUpdate: `{"batch_id":"b1","accounting":{"model_breakdowns":{}}}`,
		debugColumn: "batch_debug",
	}}

	tally, err := plugin.persistRecalcOutcomes(context.Background(), []logstore.Log{{ID: id}}, outcomes)
	if err != nil {
		t.Fatalf("persistRecalcOutcomes: %v", err)
	}
	if tally.updated != 1 {
		t.Errorf("updated = %d, want 1 — the debug blob must be written even at zero cost", tally.updated)
	}
	if got := store.updates[id]["batch_debug"]; got == nil {
		t.Error("batch_debug was not persisted; the refreshed breakdowns are lost")
	}
	if tally.priced[0] {
		t.Error("zero cost must not be reported as priced")
	}
}

// persistRecalcOutcomes is a shipped path that the job-kind refactor restructured,
// so every outcome shape it can receive is pinned here against the behaviour it
// had before. The one deliberate difference is noted on its case.
func TestPersistRecalcOutcomes_ShapeMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outcome     billingOutcome
		wantSkipped int
		wantUpdated int
		wantBulk    bool // written via BulkUpdateCost
		wantDebug   bool // batch_debug/video_debug persisted
		wantPriced  bool
	}{
		{
			name:        "pricing error is skipped and counted unpriceable",
			outcome:     billingOutcome{err: errPricingInputsUnavailable},
			wantSkipped: 1,
		},
		{
			name:        "cache hit resolved to zero writes an explicit zero, stays unpriced",
			outcome:     billingOutcome{cost: 0, knownZeroCost: true},
			wantUpdated: 1,
			wantBulk:    true,
			wantPriced:  false,
		},
		{
			name:        "unresolved zero is skipped",
			outcome:     billingOutcome{cost: 0},
			wantSkipped: 1,
		},
		{
			name:        "generic positive cost goes through the bulk path",
			outcome:     billingOutcome{cost: 0.3, breakdown: &schemas.BifrostCost{TotalCost: 0.3}},
			wantUpdated: 1,
			wantBulk:    true,
			wantPriced:  true,
		},
		{
			name: "kind aggregate writes cost and debug together",
			outcome: billingOutcome{
				cost: 0.42, kindOwned: true,
				debugUpdate: `{"x":1}`, debugColumn: "batch_debug",
			},
			wantUpdated: 1,
			wantDebug:   true,
			wantPriced:  true,
		},
		{
			name: "echo row refreshes its display only and never bills",
			outcome: billingOutcome{
				cost: 0.42, kindOwned: true, displayOnly: true,
				debugUpdate: `{"x":1}`, debugColumn: "batch_debug",
			},
			wantUpdated: 1,
			wantDebug:   true,
			wantPriced:  false,
		},
		{
			// The one deliberate change from the previous behaviour: a zero-cost row
			// carrying a refreshed blob used to be skipped, dropping the blob and
			// leaving stale model breakdowns behind.
			name: "zero cost with a refreshed blob still persists the blob",
			outcome: billingOutcome{
				cost: 0, kindOwned: true, settledZero: true,
				debugUpdate: `{"x":1}`, debugColumn: "batch_debug",
			},
			wantUpdated: 1,
			wantDebug:   true,
			wantPriced:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "row"
			store := newFakeRecalcStore([]logstore.Log{{ID: id}})
			plugin := newRecalcPlugin(t, store)

			tally, err := plugin.persistRecalcOutcomes(
				context.Background(), []logstore.Log{{ID: id}}, []billingOutcome{tc.outcome})
			if err != nil {
				t.Fatalf("persistRecalcOutcomes: %v", err)
			}
			if tally.skipped != tc.wantSkipped {
				t.Errorf("skipped = %d, want %d", tally.skipped, tc.wantSkipped)
			}
			if tally.updated != tc.wantUpdated {
				t.Errorf("updated = %d, want %d", tally.updated, tc.wantUpdated)
			}
			if got := store.updateCount[id] > 0; got != tc.wantBulk {
				t.Errorf("bulk-written = %v, want %v", got, tc.wantBulk)
			}
			if got := store.updates[id] != nil; got != tc.wantDebug {
				t.Errorf("debug persisted = %v, want %v", got, tc.wantDebug)
			}
			if tally.priced[0] != tc.wantPriced {
				t.Errorf("priced = %v, want %v", tally.priced[0], tc.wantPriced)
			}
		})
	}
}
