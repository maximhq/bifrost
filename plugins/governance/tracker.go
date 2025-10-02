// Package governance provides simplified usage tracking for the new hierarchical system
package governance

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
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
	TeamID     *string               `json:"team_id,omitempty"`     // For audit trail
	CustomerID *string               `json:"customer_id,omitempty"` // For audit trail

	// Streaming optimization fields
	IsStreaming  bool `json:"is_streaming"`   // Whether this is a streaming response
	IsFinalChunk bool `json:"is_final_chunk"` // Whether this is the final chunk
	HasUsageData bool `json:"has_usage_data"` // Whether this chunk contains usage data
}

// UsageTracker manages VK-level usage tracking and budget management
type UsageTracker struct {
	store       *GovernanceStore
	resolver    *BudgetResolver
	configStore configstore.ConfigStore
	logger      schemas.Logger

	// Background workers
	trackerCtx    context.Context
	trackerCancel context.CancelFunc
	resetTicker   *time.Ticker
	done          chan struct{}
	wg            sync.WaitGroup
}

// NewUsageTracker creates a new usage tracker for the hierarchical budget system
func NewUsageTracker(ctx context.Context, store *GovernanceStore, resolver *BudgetResolver, configStore configstore.ConfigStore, logger schemas.Logger) *UsageTracker {
	tracker := &UsageTracker{
		store:       store,
		resolver:    resolver,
		configStore: configStore,
		logger:      logger,
		done:        make(chan struct{}),
	}

	// Start background workers for business logic
	tracker.trackerCtx, tracker.trackerCancel = context.WithCancel(context.Background())
	tracker.startWorkers(tracker.trackerCtx)

	tracker.logger.Info("usage tracker initialized for hierarchical budget system")
	return tracker
}

// UpdateUsage queues a usage update for async processing (main business entry point)
func (t *UsageTracker) UpdateUsage(ctx context.Context, update *UsageUpdate) {
	// Get virtual key
	vk, exists := t.store.GetVirtualKey(update.VirtualKey)
	if !exists {
		t.logger.Debug(fmt.Sprintf("Virtual key not found: %s", update.VirtualKey))
		return
	}

	// Only process successful requests for usage tracking
	if !update.Success {
		t.logger.Debug(fmt.Sprintf("Request was not successful, skipping usage update for VK: %s", vk.ID))
		return
	}

	// Streaming optimization: only process certain updates based on streaming status
	shouldUpdateTokens := !update.IsStreaming || (update.IsStreaming && update.HasUsageData)
	shouldUpdateRequests := !update.IsStreaming || (update.IsStreaming && update.IsFinalChunk)
	shouldUpdateBudget := !update.IsStreaming || (update.IsStreaming && update.HasUsageData)

	// Update VK rate limit usage if applicable
	if vk.RateLimit != nil {
		if err := t.store.UpdateRateLimitUsage(ctx, update.VirtualKey, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			t.logger.Error("failed to update rate limit usage for VK %s: %v", vk.ID, err)
		}
	}

	// Update budget usage in hierarchy (VK → Team → Customer) only if we have usage data
	if shouldUpdateBudget && update.Cost > 0 {
		t.updateBudgetHierarchy(ctx, vk, update)
	}
}

// updateBudgetHierarchy updates budget usage atomically in the VK → Team → Customer hierarchy
func (t *UsageTracker) updateBudgetHierarchy(ctx context.Context, vk *configstore.TableVirtualKey, update *UsageUpdate) {
	// Use atomic budget update to prevent race conditions and ensure consistency
	if err := t.store.UpdateBudget(ctx, vk, update.Cost); err != nil {
		t.logger.Error("failed to update budget hierarchy atomically for VK %s: %v", vk.ID, err)
	}
}

// startWorkers starts all background workers for business logic
func (t *UsageTracker) startWorkers(ctx context.Context) {
	// Counter reset manager (business logic)
	t.resetTicker = time.NewTicker(1 * time.Minute)
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
	if err := t.store.ResetExpiredRateLimits(ctx); err != nil {
		t.logger.Error("failed to reset expired rate limits: %v", err)
	}

	// ==== PART 2: Reset Budgets ====
	if err := t.store.ResetExpiredBudgets(ctx); err != nil {
		t.logger.Error("failed to reset expired budgets: %v", err)
	}
}

// Public methods for monitoring and admin operations

// PerformStartupResets checks and resets any expired rate limits and budgets on startup
func (t *UsageTracker) PerformStartupResets(ctx context.Context) error {
	if t.configStore == nil {
		t.logger.Warn("config store is not available, skipping initialization of usage tracker")
		return nil
	}

	t.logger.Info("performing startup reset check for expired rate limits and budgets")
	now := time.Now()

	var resetRateLimits []*configstore.TableRateLimit
	var errs []string
	var vksWithRateLimits int
	var vksWithoutRateLimits int

	// ==== RESET EXPIRED RATE LIMITS ====
	// Check ALL virtual keys (both active and inactive) for expired rate limits
	allVKs, err := t.configStore.GetVirtualKeys(ctx)
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to load virtual keys for reset: %s", err.Error()))
	} else {
		t.logger.Debug(fmt.Sprintf("startup reset: checking %d virtual keys (active + inactive) for expired rate limits", len(allVKs)))
	}

	for i := range allVKs {
		vk := &allVKs[i] // Get pointer to VK for modifications
		if vk.RateLimit == nil {
			vksWithoutRateLimits++
			continue
		}

		vksWithRateLimits++

		rateLimit := vk.RateLimit
		rateLimitUpdated := false

		// Check token limits
		if rateLimit.TokenResetDuration != nil {
			if duration, err := configstore.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
				timeSinceReset := now.Sub(rateLimit.TokenLastReset)
				if timeSinceReset >= duration {
					rateLimit.TokenCurrentUsage = 0
					rateLimit.TokenLastReset = now
					rateLimitUpdated = true
				}
			} else {
				errs = append(errs, fmt.Sprintf("invalid token reset duration for VK %s: %s", vk.ID, *rateLimit.TokenResetDuration))
			}
		}

		// Check request limits
		if rateLimit.RequestResetDuration != nil {
			if duration, err := configstore.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
				timeSinceReset := now.Sub(rateLimit.RequestLastReset)
				if timeSinceReset >= duration {
					rateLimit.RequestCurrentUsage = 0
					rateLimit.RequestLastReset = now
					rateLimitUpdated = true
				}
			} else {
				errs = append(errs, fmt.Sprintf("invalid request reset duration for VK %s: %s", vk.ID, *rateLimit.RequestResetDuration))
			}
		}

		if rateLimitUpdated {
			resetRateLimits = append(resetRateLimits, rateLimit)
		}
	}

	// DB reset is also handled by this function
	if err := t.store.ResetExpiredBudgets(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("failed to reset expired budgets: %s", err.Error()))
	}

	// ==== PERSIST RESETS TO DATABASE ====
	if t.configStore != nil {
		if len(resetRateLimits) > 0 {
			if err := t.configStore.UpdateRateLimits(ctx, resetRateLimits); err != nil {
				errs = append(errs, fmt.Sprintf("failed to persist rate limit resets: %s", err.Error()))
			}
		}
	}
	if len(errs) > 0 {
		t.logger.Error("startup reset encountered %d errors: %v", len(errs), errs)
		return fmt.Errorf("startup reset completed with %d errors", len(errs))
	}

	return nil
}

// Cleanup stops all background workers and flushes pending operations
func (t *UsageTracker) Cleanup() error {
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
