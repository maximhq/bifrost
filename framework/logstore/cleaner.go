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

// CleanerConfig holds configuration for the log cleaner
type CleanerConfig struct {
	RetentionDays int
}

// RetentionTarget is another table the logs cleaner expires on the logs retention, for data that
// explains the logs and should never outlive them. DeleteBatch follows the DeleteLogsBatch
// contract: delete at most batchSize rows created before cutoff and return how many went.
type RetentionTarget struct {
	Name        string
	BatchSize   int // 0 uses the cleaner's own batch size
	DeleteBatch func(ctx context.Context, cutoff time.Time, batchSize int) (int64, error)
}

// LogsCleaner manages the cleanup of old logs
type LogsCleaner struct {
	manager     LogRetentionManager
	config      CleanerConfig
	logger      schemas.Logger
	stopCleanup chan struct{}
	mu          sync.Mutex
	targets     []RetentionTarget
}

// NewLogsCleaner creates a new LogsCleaner instance
func NewLogsCleaner(manager LogRetentionManager, config CleanerConfig, logger schemas.Logger) *LogsCleaner {
	return &LogsCleaner{
		manager: manager,
		config:  config,
		logger:  logger,
	}
}

// AddRetentionTarget registers another table to clean on every run, after the logs and with the
// same cutoff. A target registered while a run is in progress is picked up by the next run.
func (c *LogsCleaner) AddRetentionTarget(target RetentionTarget) {
	if c == nil || target.DeleteBatch == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.targets = append(c.targets, target)
}

// StartCleanupRoutine starts a goroutine that periodically cleans up old logs
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

// cleanupOldLogs deletes logs older than the retention period in batches, then does the same for
// every registered retention target with the same cutoff.
func (c *LogsCleaner) cleanupOldLogs(ctx context.Context) {
	retentionDays := c.config.RetentionDays
	if retentionDays < 1 {
		retentionDays = defaultRetentionDays
	}

	// Calculate cutoff time
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	c.logger.Info("starting log cleanup: deleting logs older than %s (retention: %d days)", cutoff.Format(time.RFC3339), retentionDays)

	// The targets are read before any delete, so one registered while this run is draining is left
	// to the next run, as AddRetentionTarget promises.
	c.mu.Lock()
	targets := append([]RetentionTarget(nil), c.targets...)
	c.mu.Unlock()

	if !c.drain(ctx, "logs", batchSize, cutoff, c.manager.DeleteLogsBatch) {
		return
	}

	for _, target := range targets {
		size := target.BatchSize
		if size <= 0 {
			size = batchSize
		}
		if !c.drain(ctx, target.Name, size, cutoff, target.DeleteBatch) {
			return
		}
	}
}

// drain deletes one table's rows older than cutoff in batches of size until a batch comes back
// short. It reports false when the run should stop: the context ended or a delete failed.
func (c *LogsCleaner) drain(ctx context.Context, name string, size int, cutoff time.Time, deleteBatch func(context.Context, time.Time, int) (int64, error)) bool {
	totalDeleted := int64(0)
	batchCount := 0

	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			c.logger.Warn("%s cleanup cancelled: %v", name, ctx.Err())
			return false
		default:
		}

		deleted, err := deleteBatch(ctx, cutoff, size)
		if err != nil {
			c.logger.Error("failed to delete old %s: %v", name, err)
			return false
		}

		if deleted == 0 {
			// Nothing more to delete
			break
		}

		totalDeleted += deleted
		batchCount++
		c.logger.Debug("deleted batch %d: %d %s", batchCount, deleted, name)

		// A full batch means more rows may remain; anything else means the
		// store is done. The SQL stores return at most the batch size. ClickHouse
		// deletes the whole expired range in one lightweight statement and
		// returns that count, so a count above the batch size must end the loop
		// too instead of re-issuing the delete (#7098).
		if deleted != int64(size) {
			break
		}
	}

	if totalDeleted > 0 {
		c.logger.Info("%s cleanup completed: deleted %d rows in %d batches", name, totalDeleted, batchCount)
	} else {
		c.logger.Debug("%s cleanup completed: nothing to delete", name)
	}
	return true
}

// calculateNextRunDuration returns 24 hours plus a random jitter between 15-30 minutes
func calculateNextRunDuration() time.Duration {
	jitter := minJitter + time.Duration(rand.Int63n(int64(maxJitter-minJitter)))
	return cleanupInterval + jitter
}
