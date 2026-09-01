// Package governance provides simplified usage tracking for the new hierarchical system
package governance

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// UsageUpdate contains data for VK-level usage tracking
type UsageUpdate struct {
	Success    bool    `json:"success"`
	TokensUsed int64   `json:"tokens_used"`
	Cost       float64 `json:"cost"` // Cost in dollars
	RequestID  string  `json:"request_id"`

	// Budgets and RateLimits are the limits this attempt answers to, settled when its provider and
	// model were known and carried here rather than worked out again at charging time. They travel
	// on the update because charging is asynchronous: by the time it runs the request may be on a
	// later attempt, whose limits are not the ones this usage was incurred under.
	Budgets    []schemas.Limit `json:"-"`
	RateLimits []schemas.Limit `json:"-"`

	// Streaming optimization fields
	IsStreaming  bool `json:"is_streaming"`   // Whether this is a streaming response
	IsFinalChunk bool `json:"is_final_chunk"` // Whether this is the final chunk
	HasUsageData bool `json:"has_usage_data"` // Whether this chunk contains usage data

	// AttemptNumber distinguishes physical provider calls within one logical
	// request (the retry loop reuses RequestID across attempts). Billing is
	// deduped on RequestID+AttemptNumber so each token-consuming attempt bills
	// at most once while distinct attempts each bill.
	AttemptNumber int `json:"attempt_number,omitempty"`
	// BilledReason is auditing metadata only ("success" | "partial_usage_on_error"):
	// it makes it possible to assert we never bill both a success and a failure
	// for the same physical call. Not used for dedup.
	BilledReason string `json:"billed_reason,omitempty"`
}

// UsageTracker manages VK-level usage tracking and budget management
type UsageTracker struct {
	store       GovernanceStore
	resolver    *BudgetResolver
	configStore configstore.ConfigStore
	logger      schemas.Logger

	// Background workers
	trackerCtx    context.Context
	trackerCancel context.CancelFunc
	resetTicker   *time.Ticker
	done          chan struct{}
	wg            sync.WaitGroup

	// billed is the idempotency set: it records the
	// RequestID+AttemptNumber keys already billed, so a physical provider call
	// is billed at most once even when both the core ctx.Done() client-return
	// path and the provider goroutine's terminal post-hook fire for it. Bounded
	// by a TTL sweep on the existing resetWorker tick (no extra goroutine).
	billedMu sync.Mutex
	billed   map[string]time.Time
	// batchBilled tracks each durable batch aggregate's individual governance
	// target. Reporting may be retried after a database marker failure, so the
	// target-level key prevents budgets or rate limits that already succeeded
	// from being incremented again while allowing failed targets to retry.
	//
	// This is process-local and therefore best-effort: it makes batch reporting
	// idempotent within one process, not across a restart or another node. A
	// batch whose report succeeded but whose durable marker write failed stays
	// retryable, and a retry elsewhere has an empty map and will bill it again.
	// That gap is accepted — see framework/batchaccounting's package doc for why
	// and what closing it would cost. Note the synchronous path's `billed` map
	// above has the same property with no durable marker at all.
	batchBilled map[string]time.Time
}

const (
	workerInterval = 10 * time.Second
	// billedEntryTTL bounds the idempotency set. It must comfortably exceed the
	// lifetime of a single logical request (max retries × backoff + stream idle
	// timeout); 5 minutes is well beyond any real request.
	billedEntryTTL = 5 * time.Minute
	// Batch settlement retries can outlive a normal request by hours. Keep the
	// stable aggregate-log keys long enough for the durable reported marker to
	// be written and observed by every local retry path.
	batchBilledEntryTTL = 7 * 24 * time.Hour
)

// NewUsageTracker creates a new usage tracker for the hierarchical budget system
func NewUsageTracker(ctx context.Context, store GovernanceStore, resolver *BudgetResolver, configStore configstore.ConfigStore, logger schemas.Logger) *UsageTracker {
	tracker := &UsageTracker{
		store:       store,
		resolver:    resolver,
		configStore: configStore,
		logger:      logger,
		done:        make(chan struct{}),
		billed:      make(map[string]time.Time),
		batchBilled: make(map[string]time.Time),
	}

	// Start background workers for business logic
	tracker.trackerCtx, tracker.trackerCancel = context.WithCancel(context.Background())
	tracker.startWorkers(tracker.trackerCtx)

	return tracker
}

// UpdateUsage queues a usage update for async processing (main business entry point)
func (t *UsageTracker) UpdateUsage(ctx context.Context, update *UsageUpdate) {
	// Bill for tokens the provider actually processed, even when the
	// request ultimately failed or was cancelled. A failed request is only
	// skipped when it consumed nothing (e.g. 401/403/429 before the model ran).
	hasUsage := update.TokensUsed > 0 || update.Cost > 0
	if !update.Success && !hasUsage {
		t.logger.Debug("Request was not successful and consumed no tokens, skipping usage update")
		return
	}

	// Idempotency: each physical provider call (RequestID + attempt) settles its
	// billing at most once. Only TERMINAL settlements are deduped — a streaming
	// request legitimately calls UpdateUsage multiple times per attempt (token
	// deltas on intermediate chunks, request count + cost on the final chunk),
	// and those must all be applied. The dedup specifically guards against the
	// success-terminal vs cancellation-terminal race for one physical call.
	// Empty RequestID (e.g. SDK-direct callers) is never deduped, preserving
	// prior behavior.
	isTerminal := !update.IsStreaming || update.IsFinalChunk
	if isTerminal && !t.tryClaimBilling(update) {
		t.logger.Debug("Usage already billed for request %s attempt %d, skipping", update.RequestID, update.AttemptNumber)
		return
	}

	// Streaming optimization: only process certain updates based on streaming status.
	// Request COUNT only increments for successful requests — a failed-but-billed
	// request adds cost+tokens but must not inflate success/rate-limit request
	// counts.
	shouldUpdateTokens := !update.IsStreaming || (update.IsStreaming && update.HasUsageData)
	shouldUpdateRequests := update.Success && (!update.IsStreaming || (update.IsStreaming && update.IsFinalChunk))
	shouldUpdateBudget := !update.IsStreaming || (update.IsStreaming && update.HasUsageData)

	// Everything this request answers to was resolved when its provider and model were settled, and
	// checked as one list. Charging reads that same list, so a limit cannot be enforced on a request
	// and then not billed for it, or billed and never enforced, which is what several independent
	// walks over the holder's shape used to risk, one per level that could be paying.
	//
	// The list already names the deployment's provider limits, the model configs that apply in every
	// scope, and whatever funds the holder. Nothing here asks which of those a limit is.
	if len(update.RateLimits) > 0 && (shouldUpdateTokens || shouldUpdateRequests) {
		if err := t.store.ChargeRateLimits(ctx, update.RateLimits, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			t.logger.Error("failed to count request %s against its rate limits: %v", update.RequestID, err)
		}
	}

	if len(update.Budgets) > 0 && shouldUpdateBudget && update.Cost > 0 {
		if err := t.store.ChargeBudgets(ctx, update.Budgets, update.Cost); err != nil {
			t.logger.Error("failed to bill request %s to its budgets: %v", update.RequestID, err)
		}
	}
}

// startWorkers starts all background workers for business logic
func (t *UsageTracker) startWorkers(ctx context.Context) {
	// Counter reset manager (business logic)
	t.resetTicker = time.NewTicker(workerInterval)
	t.wg.Add(1)
	go t.resetWorker(ctx)
}

// resetWorker manages periodic resets of rate limit and usage counters
func (t *UsageTracker) resetWorker(ctx context.Context) {
	defer t.wg.Done()

	for {
		select {
		case <-t.resetTicker.C:
			t.resetExpiredCounters(ctx)

		case <-t.done:
			return
		}
	}
}

// resetExpiredCounters manages periodic resets of usage counters AND budgets using flexible durations.
//
// This pass is the only thing that advances the persisted last_reset boundary
// for a budget under steady traffic, because the request-time reset path in
// BumpBudgetUsage rolls counters over in memory and leaves persistence to the
// dump below. It also runs on a ticker, and a Go ticker drops ticks when the
// receiver is slow, so a pass that overruns workerInterval silently stretches
// the real reset cadence for the whole node. That is otherwise invisible:
// enforcement keeps working off the in-memory counters while the persisted
// boundary falls further behind, so the only symptom is a stale last_reset.
// The overrun warning below exists to make that state say so out loud.
func (t *UsageTracker) resetExpiredCounters(ctx context.Context) {
	start := time.Now()

	// ==== PART 1: Reset Rate Limits ====
	resetRateLimits := t.store.ResetExpiredRateLimitsInMemory(ctx, true)
	if err := t.store.ResetExpiredRateLimits(ctx, resetRateLimits); err != nil {
		t.logger.Error("failed to reset expired rate limits: %v", err)
	}

	// ==== PART 2: Reset Budgets ====
	resetBudgets := t.store.ResetExpiredBudgetsInMemory(ctx, true)
	if err := t.store.ResetExpiredBudgets(ctx, resetBudgets); err != nil {
		t.logger.Error("failed to reset expired budgets: %v", err)
	}

	// ==== PART 3: Dump all rate limits and budgets to database ====
	if err := t.store.DumpRateLimits(ctx, nil, nil); err != nil {
		t.logger.Error("failed to dump rate limits to database: %v", err)
	}
	if err := t.store.DumpBudgets(ctx, nil); err != nil {
		t.logger.Error("failed to dump budgets to database: %v", err)
	}

	// ==== PART 4: Sweep expired billing-idempotency keys ====
	t.sweepBilled()

	if total := time.Since(start); total > workerInterval {
		// The next tick is already overdue, so resets are no longer landing at
		// workerInterval granularity.
		t.logger.Warn("reset cycle took %s, longer than the %s worker interval (%d rate limits, %d budgets reset): reset cadence has slipped and persisted last_reset will lag the window boundary",
			total, workerInterval, len(resetRateLimits), len(resetBudgets))
	}
}

// tryClaimBilling records that the physical provider call identified by
// (RequestID, AttemptNumber) is being billed and returns true if this is the
// first claim. Subsequent calls for the same key return false so the same
// physical call is never billed twice An empty RequestID is treated as
// non-dedupable (always returns true) to preserve behavior for SDK-direct
// callers that carry no request id.
func (t *UsageTracker) tryClaimBilling(update *UsageUpdate) bool {
	if update.RequestID == "" {
		return true
	}
	key := fmt.Sprintf("%s:%d", update.RequestID, update.AttemptNumber)
	t.billedMu.Lock()
	defer t.billedMu.Unlock()
	if _, seen := t.billed[key]; seen {
		return false
	}
	t.billed[key] = time.Now()
	return true
}

// sweepBilled drops idempotency keys older than billedEntryTTL, bounding the
// map to roughly the requests seen within the TTL window.
func (t *UsageTracker) sweepBilled() {
	now := time.Now()
	cutoff := now.Add(-billedEntryTTL)
	batchCutoff := now.Add(-batchBilledEntryTTL)
	t.billedMu.Lock()
	defer t.billedMu.Unlock()
	for k, at := range t.billed {
		if at.Before(cutoff) {
			delete(t.billed, k)
		}
	}
	for k, at := range t.batchBilled {
		if at.Before(batchCutoff) {
			delete(t.batchBilled, k)
		}
	}
}

func (t *UsageTracker) tryClaimBatchBilling(key string) bool {
	if key == "" {
		return true
	}
	t.billedMu.Lock()
	defer t.billedMu.Unlock()
	if _, seen := t.batchBilled[key]; seen {
		return false
	}
	t.batchBilled[key] = time.Now()
	return true
}

func (t *UsageTracker) releaseBatchBilling(key string) {
	if key == "" {
		return
	}
	t.billedMu.Lock()
	delete(t.batchBilled, key)
	t.billedMu.Unlock()
}

// Public methods for monitoring and admin operations

// PerformStartupResets checks and resets any expired rate limits and budgets on startup
func (t *UsageTracker) PerformStartupResets(ctx context.Context) error {
	if t.configStore == nil {
		t.logger.Warn("config store is not available, skipping initialization of usage tracker")
		return nil
	}

	t.logger.Debug("performing startup reset check for expired rate limits and budgets")
	var errs []string
	for _, err := range t.validateStartupResetDurations(ctx) {
		errs = append(errs, err.Error())
	}

	// ==== RESET EXPIRED RATE LIMITS ====
	// Reuse the shared in-memory reset path so startup, ticker, and request-time
	// resets all apply the same LastDB baseline and reset-hook side effects.
	rateLimitResetStart := time.Now()
	resetRateLimits := t.store.ResetExpiredRateLimitsInMemory(ctx, true)
	t.logger.Info("[startup-timing] PerformStartupResets in-memory reset of %d rate limits took %v", len(resetRateLimits), time.Since(rateLimitResetStart))
	if err := t.store.ResetExpiredRateLimits(ctx, resetRateLimits); err != nil {
		errs = append(errs, fmt.Sprintf("failed to reset expired rate limits: %s", err.Error()))
	}

	// DB reset is also handled by this function
	budgetResetStart := time.Now()
	resetBudgets := t.store.ResetExpiredBudgetsInMemory(ctx, true)
	t.logger.Info("[startup-timing] PerformStartupResets in-memory reset of %d budgets took %v", len(resetBudgets), time.Since(budgetResetStart))
	if err := t.store.ResetExpiredBudgets(ctx, resetBudgets); err != nil {
		errs = append(errs, fmt.Sprintf("failed to reset expired budgets: %s", err.Error()))
	}
	if len(errs) > 0 {
		t.logger.Error("startup reset encountered %d errors: %v", len(errs), errs)
		return fmt.Errorf("startup reset completed with %d errors", len(errs))
	}

	return nil
}

func (t *UsageTracker) validateStartupResetDurations(ctx context.Context) []error {
	data := t.store.GetGovernanceData(ctx)
	if data == nil {
		return nil
	}

	var errs []error
	for _, budget := range data.Budgets {
		if budget == nil || budget.ResetDuration == "" || budget.IsCalendarAligned {
			continue
		}
		if _, err := configstoreTables.ParseDuration(budget.ResetDuration); err != nil {
			errs = append(errs, fmt.Errorf("invalid budget reset duration for budget %s: %w", budget.ID, err))
		}
	}

	for _, rateLimit := range data.RateLimits {
		if rateLimit == nil || rateLimit.IsCalendarAligned {
			continue
		}
		if rateLimit.TokenResetDuration != nil {
			if _, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err != nil {
				errs = append(errs, fmt.Errorf("invalid token reset duration for rate limit %s: %w", rateLimit.ID, err))
			}
		}
		if rateLimit.RequestResetDuration != nil {
			if _, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err != nil {
				errs = append(errs, fmt.Errorf("invalid request reset duration for rate limit %s: %w", rateLimit.ID, err))
			}
		}
	}

	return errs
}

// Cleanup stops all background workers and flushes pending operations
func (t *UsageTracker) Cleanup() error {
	// Final flush of in-memory deltas to DB before shutdown. Without this,
	// any deltas accumulated since the last `workerInterval` tick are lost.
	if err := t.store.DumpBudgets(context.Background(), nil); err != nil {
		t.logger.Error("final budget dump on shutdown failed: %v", err)
	}
	if err := t.store.DumpRateLimits(context.Background(), nil, nil); err != nil {
		t.logger.Error("final rate-limit dump on shutdown failed: %v", err)
	}

	// Stop background workers
	if t.trackerCancel != nil {
		t.trackerCancel()
	}
	close(t.done)
	if t.resetTicker != nil {
		t.resetTicker.Stop()
	}
	// Wait for workers to finish
	t.wg.Wait()

	t.logger.Debug("usage tracker cleanup completed")
	return nil
}
