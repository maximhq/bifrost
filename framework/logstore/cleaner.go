package logstore

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	cleanupInterval      = 24 * time.Hour
	minJitter            = 15 * time.Minute
	maxJitter            = 30 * time.Minute
	batchSize            = 100
	defaultRetentionDays = 365
)

// LogRetentionManager defines the interface for managing log retention and deletion
type LogRetentionManager interface {
	DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)
}

// MCPToolLogRetentionManager is implemented by log stores that can prune MCP
// tool logs by age. It is intentionally separate from LogRetentionManager so
// that a store which does not implement it keeps working (it simply skips MCP
// pruning) instead of failing the LogRetentionManager type assertion that
// gates the cleaner being started at all.
//
// Note for decorators: a type that embeds LogStore satisfies
// LogRetentionManager for free (DeleteLogsBatch is part of LogStore) but does
// NOT satisfy this interface, so it keeps pruning logs while silently skipping
// mcp_tool_logs. Forward DeleteMCPToolLogsBatch explicitly, as HybridLogStore
// does.
type MCPToolLogRetentionManager interface {
	DeleteMCPToolLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)
}

// CleanerConfig holds configuration for the log cleaner
type CleanerConfig struct {
	RetentionDays int
}

// LogsCleaner manages the cleanup of old logs and MCP tool logs
type LogsCleaner struct {
	manager     LogRetentionManager
	config      CleanerConfig
	logger      schemas.Logger
	stopCleanup chan struct{}
	mu          sync.Mutex
}

// NewLogsCleaner creates a new LogsCleaner instance
func NewLogsCleaner(manager LogRetentionManager, config CleanerConfig, logger schemas.Logger) *LogsCleaner {
	return &LogsCleaner{
		manager: manager,
		config:  config,
		logger:  logger,
	}
}

// StartCleanupRoutine starts a goroutine that periodically cleans up old logs
// and MCP tool logs
func (c *LogsCleaner) StartCleanupRoutine() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return early if already running
	if c.stopCleanup != nil {
		c.logger.Debug("log cleanup routine already running")
		return
	}

	c.stopCleanup = make(chan struct{})
	stopCh := c.stopCleanup

	go func() {
		// At the beginning, we will cleanup the logs
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		c.cleanupOldLogs(ctx)
		cancel()
		// Calculate initial delay with jitter
		timer := time.NewTimer(calculateNextRunDuration())
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				// Run cleanup
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				c.cleanupOldLogs(ctx)
				cancel()

				// Reset timer with new jitter for next run
				timer.Reset(calculateNextRunDuration())

			case <-stopCh:
				c.logger.Info("log cleanup routine stopped")
				return
			}
		}
	}()
	c.logger.Info("log cleanup routine started")
}

// StopCleanupRoutine gracefully stops the cleanup goroutine
func (c *LogsCleaner) StopCleanupRoutine() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return early if already stopped
	if c.stopCleanup == nil {
		c.logger.Debug("log cleanup routine already stopped")
		return
	}

	close(c.stopCleanup)
	c.stopCleanup = nil
}

// cleanupOldLogs deletes logs older than the retention period in batches, and
// prunes MCP tool logs against the same cutoff when the store supports it
func (c *LogsCleaner) cleanupOldLogs(ctx context.Context) {
	retentionDays := c.config.RetentionDays
	if retentionDays < 1 {
		retentionDays = defaultRetentionDays
	}

	// Calculate cutoff time
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	c.logger.Info("starting log cleanup: deleting logs older than %s (retention: %d days)", cutoff.Format(time.RFC3339), retentionDays)

	// Deferred so the MCP sweep runs on every exit path below. The two tables
	// are independent, and the logs loop bails out on the first delete error:
	// letting that starve MCP pruning would leave mcp_tool_logs growing without
	// bound, which is the bug this sweep exists to fix. A cancelled ctx still
	// short-circuits inside cleanupOldMCPToolLogs.
	defer c.cleanupOldMCPToolLogs(ctx, cutoff)

	totalDeleted := int64(0)
	batchCount := 0

	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			c.logger.Warn("log cleanup cancelled: %v", ctx.Err())
			return
		default:
		}

		// Delete logs in batches using the manager
		deleted, err := c.manager.DeleteLogsBatch(ctx, cutoff, batchSize)
		if err != nil {
			c.logger.Error("failed to delete old logs: %v", err)
			return
		}

		if deleted == 0 {
			// No more logs to delete
			break
		}

		totalDeleted += deleted
		batchCount++
		c.logger.Debug("deleted batch %d: %d logs", batchCount, deleted)

		// If we deleted fewer than the batch size, we're done
		if deleted < int64(batchSize) {
			break
		}
	}

	if totalDeleted > 0 {
		c.logger.Info("log cleanup completed: deleted %d logs in %d batches", totalDeleted, batchCount)
	} else {
		c.logger.Debug("log cleanup completed: no old logs to delete")
	}
}

// cleanupOldMCPToolLogs deletes MCP tool logs older than cutoff in batches.
// mcp_tool_logs carries the same retention as logs - ClickHouse applies the
// same TTL to both tables - so the backends without a table TTL need the same
// age-based sweep or mcp_tool_logs grows without bound. Counts are reported
// separately from the logs sweep so operators can tell the two apart. A store
// that does not implement MCPToolLogRetentionManager is skipped rather than
// treated as a failure: MCP pruning is best-effort, log pruning is not.
func (c *LogsCleaner) cleanupOldMCPToolLogs(ctx context.Context, cutoff time.Time) {
	manager, ok := c.manager.(MCPToolLogRetentionManager)
	if !ok {
		c.logger.Debug("mcp tool log cleanup skipped: log store does not support pruning mcp tool logs by age")
		return
	}

	totalDeleted := int64(0)
	batchCount := 0

	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			c.logger.Warn("mcp tool log cleanup cancelled: %v", ctx.Err())
			return
		default:
		}

		// Delete MCP tool logs in batches using the manager
		deleted, err := manager.DeleteMCPToolLogsBatch(ctx, cutoff, batchSize)
		if err != nil {
			c.logger.Error("failed to delete old mcp tool logs: %v", err)
			return
		}

		if deleted == 0 {
			// No more MCP tool logs to delete
			break
		}

		totalDeleted += deleted
		batchCount++
		c.logger.Debug("deleted batch %d: %d mcp tool logs", batchCount, deleted)

		// If we deleted fewer than the batch size, we're done
		if deleted < int64(batchSize) {
			break
		}
	}

	if totalDeleted > 0 {
		c.logger.Info("mcp tool log cleanup completed: deleted %d mcp tool logs in %d batches", totalDeleted, batchCount)
	} else {
		c.logger.Debug("mcp tool log cleanup completed: no old mcp tool logs to delete")
	}
}

// calculateNextRunDuration returns 24 hours plus a random jitter between 15-30 minutes
func calculateNextRunDuration() time.Duration {
	jitter := minJitter + time.Duration(rand.Int63n(int64(maxJitter-minJitter)))
	return cleanupInterval + jitter
}
