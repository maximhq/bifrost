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
	VirtualKey string                `json:"virtual_key"`
	Provider   schemas.ModelProvider `json:"provider"`
	Model      string                `json:"model"`
	Success    bool                  `json:"success"`
	TokensUsed int64                 `json:"tokens_used"`
	Cost       float64               `json:"cost"` // Cost in dollars
	RequestID  string                `json:"request_id"`
	UserID     string                `json:"user_id,omitempty"` // User ID for enterprise user-level governance

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

	// 1. Update rate limit usage for both provider-level and model-level
	// This applies even when virtual keys are disabled or not present
	// Guard: only update when Model is set (MCP paths may not have it); provider is optional —
	// the underlying function handles empty provider by skipping provider-level and still
	// updating any matching global model-only configs.
	if update.Model != "" {
		if err := t.store.UpdateProviderAndModelRateLimitUsageInMemory(ctx, update.Model, update.Provider, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			t.logger.Error("failed to update rate limit usage for model %s, provider %s: %v", update.Model, update.Provider, err)
		}
	}

	// 2. Update budget usage for both provider-level and model-level
	// This applies even when virtual keys are disabled or not present
	// Guard: only update when Model is set (MCP paths may not have it); provider is optional —
	// the underlying function handles empty provider by skipping provider-level and still
	// updating any matching global model-only configs.
	if update.Model != "" && shouldUpdateBudget && update.Cost > 0 {
		if err := t.store.UpdateProviderAndModelBudgetUsageInMemory(ctx, update.Model, update.Provider, update.Cost); err != nil {
			t.logger.Error("failed to update budget usage for model %s, provider %s: %v", update.Model, update.Provider, err)
		}
	}

	// 3. Update user-level governance (enterprise-only, before VK-level)
	if update.UserID != "" {
		// Update user rate limit usage
		if err := t.store.UpdateUserRateLimitUsageInMemory(ctx, update.UserID, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			t.logger.Error("failed to update user rate limit usage for user %s: %v", update.UserID, err)
		}
		// Update user budget usage
		if shouldUpdateBudget && update.Cost > 0 {
			if err := t.store.UpdateUserBudgetUsageInMemory(ctx, update.UserID, update.Cost); err != nil {
				t.logger.Error("failed to update user budget usage for user %s: %v", update.UserID, err)
			}
		}
		// Update per-user-scoped model config rate limits and budgets. Mirrors the
		// VK-scoped model block below. Gated on model being present — MCP tool
		// execution paths (no model) are excluded naturally by this guard.
		if update.Model != "" {
			if err := t.store.UpdateScopedModelRateLimitUsageInMemory(ctx, configstoreTables.ModelConfigScopeUser, update.UserID, update.Model, update.Provider, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
				t.logger.Error("failed to update scoped model rate limit usage for user %s: %v", update.UserID, err)
			}
			if shouldUpdateBudget && update.Cost > 0 {
				if err := t.store.UpdateScopedModelBudgetUsageInMemory(ctx, configstoreTables.ModelConfigScopeUser, update.UserID, update.Model, update.Provider, update.Cost); err != nil {
					t.logger.Error("failed to update scoped model budget usage for user %s: %v", update.UserID, err)
				}
			}
		}
	}

	// 4. Now handle virtual key-level updates (if virtual key exists)
	if update.VirtualKey == "" {
		// No virtual key, provider-level and model-level updates already done above
		return
	}

	// Get virtual key
	vk, exists := t.store.GetVirtualKey(ctx, update.VirtualKey)
	if !exists {
		t.logger.Debug(fmt.Sprintf("Virtual key not found: %s", update.VirtualKey))
		return
	}

	// Update per-VK-scoped model config usage (counterpart to the global model updates above).
	// Without this, per-VK model limits never increment and so never trip.
	if update.Model != "" {
		if err := t.store.UpdateScopedModelRateLimitUsageInMemory(ctx, configstoreTables.ModelConfigScopeVirtualKey, vk.ID, update.Model, update.Provider, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			t.logger.Error("failed to update scoped model rate limit usage for VK %s: %v", vk.ID, err)
		}
		if shouldUpdateBudget && update.Cost > 0 {
			if err := t.store.UpdateScopedModelBudgetUsageInMemory(ctx, configstoreTables.ModelConfigScopeVirtualKey, vk.ID, update.Model, update.Provider, update.Cost); err != nil {
				t.logger.Error("failed to update scoped model budget usage for VK %s: %v", vk.ID, err)
			}
		}
	}

	// Update rate limit usage (VK-level, provider-config-level, team-level, customer-level) if applicable
	// Include TeamID and CustomerID checks since rate limits can be configured at those levels
	if vk.RateLimit != nil || len(vk.ProviderConfigs) > 0 || vk.TeamID != nil || vk.CustomerID != nil {
		if err := t.store.UpdateVirtualKeyRateLimitUsageInMemory(ctx, vk, update.Provider, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			t.logger.Error("failed to update rate limit usage for VK %s: %v", vk.ID, err)
		}
	}

	// Update budget usage in hierarchy (VK → Team → Customer) only if we have usage data
	if shouldUpdateBudget && update.Cost > 0 {
		t.logger.Debug("updating budget usage for VK %s", vk.ID)
		// Use atomic budget update to prevent race conditions and ensure consistency
		if err := t.store.UpdateVirtualKeyBudgetUsageInMemory(ctx, vk, update.Provider, update.Cost); err != nil {
			t.logger.Error("failed to update budget hierarchy atomically for VK %s: %v", vk.ID, err)
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

// resetExpiredCounters manages periodic resets of usage counters AND budgets using flexible durations
func (t *UsageTracker) resetExpiredCounters(ctx context.Context) {
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
