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
	"gorm.io/gorm"
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

	// Streaming optimization fields
	IsStreaming  bool `json:"is_streaming"`   // Whether this is a streaming response
	IsFinalChunk bool `json:"is_final_chunk"` // Whether this is the final chunk
	HasUsageData bool `json:"has_usage_data"` // Whether this chunk contains usage data
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
}

const (
	workerInterval = 10 * time.Second
)

// NewUsageTracker creates a new usage tracker for the hierarchical budget system
func NewUsageTracker(ctx context.Context, store GovernanceStore, resolver *BudgetResolver, configStore configstore.ConfigStore, logger schemas.Logger) *UsageTracker {
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

	return tracker
}

// UpdateUsage queues a usage update for async processing (main business entry point)
func (t *UsageTracker) UpdateUsage(ctx context.Context, update *UsageUpdate) {
	// Only process successful requests for usage tracking
	if !update.Success {
		t.logger.Debug("Request was not successful, skipping usage update")
		return
	}

	// Streaming optimization: only process certain updates based on streaming status
	shouldUpdateTokens := !update.IsStreaming || (update.IsStreaming && update.HasUsageData)
	shouldUpdateRequests := !update.IsStreaming || (update.IsStreaming && update.IsFinalChunk)
	shouldUpdateBudget := !update.IsStreaming || (update.IsStreaming && update.HasUsageData)

	// 1. Update rate limit usage for both provider-level and model-level
	// This applies even when virtual keys are disabled or not present
	if err := t.store.UpdateProviderAndModelRateLimitUsageInMemory(ctx, update.Model, update.Provider, update.TokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
		t.logger.Error("failed to update rate limit usage for model %s, provider %s: %v", update.Model, update.Provider, err)
	}

	// 2. Update budget usage for both provider-level and model-level
	// This applies even when virtual keys are disabled or not present
	if shouldUpdateBudget && update.Cost > 0 {
		if err := t.store.UpdateProviderAndModelBudgetUsageInMemory(ctx, update.Model, update.Provider, update.Cost); err != nil {
			t.logger.Error("failed to update budget usage for model %s, provider %s: %v", update.Model, update.Provider, err)
		}
	}

	// 3. Now handle virtual key-level updates (if virtual key exists)
	if update.VirtualKey == "" {
		// No virtual key, provider-level and model-level updates already done above
		return
	}

	// Get virtual key
	vk, exists := t.store.GetVirtualKey(update.VirtualKey)
	if !exists {
		t.logger.Debug(fmt.Sprintf("Virtual key not found: %s", update.VirtualKey))
		return
	}

	// Update rate limit usage (both provider-level and VK-level) if applicable
	if vk.RateLimit != nil || len(vk.ProviderConfigs) > 0 {
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
	resetRateLimits := t.store.ResetExpiredRateLimitsInMemory(ctx)
	if err := t.store.ResetExpiredRateLimits(ctx, resetRateLimits); err != nil {
		t.logger.Error("failed to reset expired rate limits: %v", err)
	}

	// ==== PART 2: Reset Budgets ====
	resetBudgets := t.store.ResetExpiredBudgetsInMemory(ctx)
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
}

// Public methods for monitoring and admin operations

// PerformStartupResets checks and resets any expired rate limits and budgets on startup
func (t *UsageTracker) PerformStartupResets(ctx context.Context) error {
	if t.configStore == nil {
		t.logger.Warn("config store is not available, skipping initialization of usage tracker")
		return nil
	}

	t.logger.Debug("performing startup reset check for expired rate limits and budgets")
	now := time.Now()

	var resetRateLimits []*configstoreTables.TableRateLimit
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
			if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
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
			if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
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
	resetBudgets := t.store.ResetExpiredBudgetsInMemory(ctx)
	if err := t.store.ResetExpiredBudgets(ctx, resetBudgets); err != nil {
		errs = append(errs, fmt.Sprintf("failed to reset expired budgets: %s", err.Error()))
	}

	// ==== PERSIST RESETS TO DATABASE ====
	// Use selective updates to avoid overwriting config fields (max_limit, reset_duration)
	if t.configStore != nil && len(resetRateLimits) > 0 {
		if err := t.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			for _, rateLimit := range resetRateLimits {
				// Build update map with only the fields that were reset
				updates := make(map[string]interface{})
				updates["token_current_usage"] = rateLimit.TokenCurrentUsage
				updates["token_last_reset"] = rateLimit.TokenLastReset
				updates["request_current_usage"] = rateLimit.RequestCurrentUsage
				updates["request_last_reset"] = rateLimit.RequestLastReset

				// Direct UPDATE only resets usage and last_reset fields
				// This prevents overwriting max_limit or reset_duration that may have been changed during startup
				result := tx.WithContext(ctx).
					Session(&gorm.Session{SkipHooks: true}).
					Model(&configstoreTables.TableRateLimit{}).
					Where("id = ?", rateLimit.ID).
					Updates(updates)

				if result.Error != nil {
					return fmt.Errorf("failed to reset rate limit %s: %w", rateLimit.ID, result.Error)
				}
			}
			return nil
		}); err != nil {
			errs = append(errs, fmt.Sprintf("failed to persist rate limit resets: %s", err.Error()))
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
