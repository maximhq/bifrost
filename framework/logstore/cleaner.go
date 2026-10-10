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
	defaultRetentionDays = 365

	// batchSize is how many expired rows one retention statement deletes. At
	// ~5.6M expired rows per day, 100-row batches under a 30-minute cap could
	// never catch up; 5000 keeps each statement short (one index range walk,
	// a bounded WAL burst) while finishing a day of expiry in about a thousand
	// statements.
	batchSize = 5000

	// cleanupRunCeiling bounds one retention pass. The pass normally runs until
	// no expired rows remain; the ceiling only stops a pathological backlog
	// (for example after retention was shortened) from holding a connection
	// indefinitely. The next daily run continues where this one stopped.
	cleanupRunCeiling = 6 * time.Hour

	// cleanupLogsShareNum/cleanupLogsShareDen is the share of a pass's remaining
	// time the logs table may use when MCP tool logs are cleaned too; the rest is
	// reserved so a logs backlog cannot starve mcp_tool_logs retention.
	cleanupLogsShareNum = 5
	cleanupLogsShareDen = 6

	// cleanupProgressEvery is how many batches pass between Debug progress lines.
	cleanupProgressEvery = 100
)

// LogRetentionManager defines the interface for managing log retention and deletion
type LogRetentionManager interface {
	DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)
}

// MCPToolLogRetentionManager is implemented by stores that can also expire
// mcp_tool_logs rows. It is a separate, optional interface so wrappers that
// only implement LogRetentionManager keep compiling; the cleaner applies MCP
// retention when the manager it was given implements this too.
type MCPToolLogRetentionManager interface {
	DeleteMCPToolLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)
}

// CleanerConfig holds configuration for the log cleaner
type CleanerConfig struct {
	RetentionDays int
}

// LogsCleaner manages the cleanup of old logs
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

// UpdateRetentionDays updates the retention period used by subsequent cleanup
// passes. A pass already in progress keeps its cutoff for both log tables.
func (c *LogsCleaner) UpdateRetentionDays(retentionDays int) {
	c.mu.Lock()
	c.config.RetentionDays = retentionDays
	c.mu.Unlock()
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
		c.runCleanupPass(stopCh)
		// Calculate initial delay with jitter
		timer := time.NewTimer(calculateNextRunDuration())
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				// Run cleanup
				c.runCleanupPass(stopCh)

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

// runCleanupPass runs one retention pass bounded by cleanupRunCeiling. Closing
// stopCh cancels the pass between batches so StopCleanupRoutine does not wait
// for a long backlog to drain.
func (c *LogsCleaner) runCleanupPass(stopCh <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupRunCeiling)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-done:
		}
	}()
	c.cleanupOldLogs(ctx)
}

// cleanupOldLogs deletes logs, and MCP tool logs when the manager supports it,
// older than the retention period. Each table is drained in batches until no
// expired rows remain or ctx ends.
func (c *LogsCleaner) cleanupOldLogs(ctx context.Context) {
	c.mu.Lock()
	retentionDays := c.config.RetentionDays
	c.mu.Unlock()
	if retentionDays < 1 {
		retentionDays = defaultRetentionDays
	}

	// Calculate cutoff time
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	c.logger.Info("starting log cleanup: deleting logs older than %s (retention: %d days)", cutoff.Format(time.RFC3339), retentionDays)

	mcp, hasMCP := c.manager.(MCPToolLogRetentionManager)
	// A logs backlog can outlast the whole pass, and a logs delete can fail on
	// every run. Neither may starve mcp_tool_logs of retention, so logs get their
	// own sub-deadline that leaves a reserved share of the pass for MCP, and MCP
	// runs whatever the logs outcome, unless the pass itself has ended (stopped
	// or past its deadline).
	logsCtx, cancelLogs := ctx, context.CancelFunc(func() {})
	if deadline, ok := ctx.Deadline(); ok && hasMCP {
		logsBudget := time.Until(deadline) * cleanupLogsShareNum / cleanupLogsShareDen
		logsCtx, cancelLogs = context.WithTimeout(ctx, logsBudget)
	}
	c.drainExpired(logsCtx, "logs", cutoff, c.manager.DeleteLogsBatch)
	cancelLogs()
	if !hasMCP || ctx.Err() != nil {
		return
	}
	c.drainExpired(ctx, "MCP tool logs", cutoff, mcp.DeleteMCPToolLogsBatch)
}

// drainExpired calls deleteBatch until it reports a short batch, logging
// progress at Debug. It returns false when the table stopped early
// (cancellation or an error).
func (c *LogsCleaner) drainExpired(ctx context.Context, label string, cutoff time.Time, deleteBatch func(context.Context, time.Time, int) (int64, error)) bool {
	totalDeleted := int64(0)
	batchCount := 0

	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			c.logger.Warn("%s cleanup stopped after %d rows in %d batches: %v", label, totalDeleted, batchCount, ctx.Err())
			return false
		default:
		}

		deleted, err := deleteBatch(ctx, cutoff, batchSize)
		if err != nil {
			c.logger.Error("failed to delete old %s: %v", label, err)
			return false
		}

		if deleted == 0 {
			// No more logs to delete
			break
		}

		totalDeleted += deleted
		batchCount++
		if batchCount%cleanupProgressEvery == 0 {
			c.logger.Debug("%s cleanup progress: %d rows deleted in %d batches", label, totalDeleted, batchCount)
		}

		// A full batch means more rows may remain; anything else means the
		// store is done. The SQL stores return at most batchSize. ClickHouse
		// deletes the whole expired range in one lightweight statement and
		// returns that count, so a count above batchSize must end the loop
		// too instead of re-issuing the delete (#7098).
		if deleted != int64(batchSize) {
			break
		}
	}

	if totalDeleted > 0 {
		c.logger.Info("%s cleanup completed: deleted %d rows in %d batches", label, totalDeleted, batchCount)
	} else {
		c.logger.Debug("%s cleanup completed: nothing older than the cutoff", label)
	}
	return true
}

// calculateNextRunDuration returns 24 hours plus a random jitter between 15-30 minutes
func calculateNextRunDuration() time.Duration {
	jitter := minJitter + time.Duration(rand.Int63n(int64(maxJitter-minJitter)))
	return cleanupInterval + jitter
}
