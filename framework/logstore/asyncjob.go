package logstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
)

const (
	// DefaultAsyncJobResultTTL is the default TTL for async job results in seconds (1 hour).
	DefaultAsyncJobResultTTL = 3600
)

const (
	asyncJobCleanupInterval      = 1 * time.Minute
	asyncJobCleanupTimeout       = 1 * time.Minute
	asyncJobStaleProcessingHours = 24
)

// --- AsyncJobExecutor ---

// AsyncOperation represents a function that can be executed asynchronously.
// It returns the response and an optional BifrostError.
type AsyncOperation func(ctx *schemas.BifrostContext) (interface{}, *schemas.BifrostError)

// GovernanceStore is an interface that provides access to the governance store.
type GovernanceStore interface {
	GetVirtualKey(vkValue string) (*configstoreTables.TableVirtualKey, bool)
}

// AsyncJobExecutor manages async job creation and background execution.
type AsyncJobExecutor struct {
	logstore        LogStore
	governanceStore GovernanceStore
	logger          schemas.Logger
}

// NewAsyncJobExecutor creates a new AsyncJobExecutor.
func NewAsyncJobExecutor(logstore LogStore, governanceStore GovernanceStore, logger schemas.Logger) *AsyncJobExecutor {
	return &AsyncJobExecutor{
		logstore:        logstore,
		governanceStore: governanceStore,
		logger:          logger,
	}
}

// RetrieveJob retrieves a job by its ID.
func (e *AsyncJobExecutor) RetrieveJob(ctx context.Context, jobID string, vkValue *string, operationType schemas.RequestType) (*AsyncJob, error) {
	job, err := e.logstore.FindAsyncJobByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.VirtualKeyID != nil {
		if vkValue == nil {
			return nil, fmt.Errorf("virtual key is required")
		}
		vk, ok := e.governanceStore.GetVirtualKey(*vkValue)
		if !ok {
			return nil, fmt.Errorf("virtual key not found")
		}
		if *job.VirtualKeyID != vk.ID {
			return nil, fmt.Errorf("virtual key mismatch")
		}
	}
	if job.RequestType != operationType {
		return nil, fmt.Errorf("operation type mismatch")
	}
	return job, nil
}

// SubmitJob creates a pending job, starts background execution, and returns the job record.
func (e *AsyncJobExecutor) SubmitJob(virtualKeyValue *string, resultTTL int, operation AsyncOperation, operationType schemas.RequestType) (*AsyncJob, error) {
	if resultTTL <= 0 {
		resultTTL = DefaultAsyncJobResultTTL
	}

	var virtualKeyID *string
	if virtualKeyValue != nil {
		vk, ok := e.governanceStore.GetVirtualKey(*virtualKeyValue)
		if !ok {
			return nil, fmt.Errorf("virtual key not found")
		}
		virtualKeyID = &vk.ID
	}

	now := time.Now().UTC()
	job := &AsyncJob{
		ID:           uuid.New().String(),
		Status:       schemas.AsyncJobStatusPending,
		RequestType:  operationType,
		VirtualKeyID: virtualKeyID,
		ResultTTL:    resultTTL,
		CreatedAt:    now,
	}

	ctx := context.Background()
	if err := e.logstore.CreateAsyncJob(ctx, job); err != nil {
		return nil, fmt.Errorf("failed to create async job: %w", err)
	}

	go e.executeJob(job.ID, job.ResultTTL, operation)

	return job, nil
}

// executeJob runs the operation in the background and updates the job record.
func (e *AsyncJobExecutor) executeJob(jobID string, resultTTL int, operation AsyncOperation) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Mark as processing
	if err := e.logstore.UpdateAsyncJob(ctx, jobID, map[string]interface{}{
		"status": schemas.AsyncJobStatusProcessing,
	}); err != nil {
		e.logger.Warn("failed to update async job: %v", err)
	}

	ctx.SetValue(schemas.BifrostIsAsyncRequest, true)

	// Execute the operation
	resp, bifrostErr := operation(ctx)

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(resultTTL) * time.Second)

	if bifrostErr != nil {
		errJSON, err := sonic.Marshal(bifrostErr)
		if err != nil {
			e.logger.Warn("failed to marshal bifrost error: %v", err)
			return
		}
		statusCode := fasthttp.StatusInternalServerError
		if bifrostErr.StatusCode != nil {
			statusCode = *bifrostErr.StatusCode
		}
		if err := e.logstore.UpdateAsyncJob(ctx, jobID, map[string]interface{}{
			"status":       schemas.AsyncJobStatusFailed,
			"status_code":  statusCode,
			"error":        string(errJSON),
			"completed_at": now,
			"expires_at":   expiresAt,
		}); err != nil {
			e.logger.Warn("failed to update async job: %v", err)
		}
		return
	}

	respJSON, err := sonic.Marshal(resp)
	if err != nil {
		e.logger.Warn("failed to marshal result: %v", err)
		return
	}
	if err := e.logstore.UpdateAsyncJob(ctx, jobID, map[string]interface{}{
		"status":       schemas.AsyncJobStatusCompleted,
		"status_code":  fasthttp.StatusOK,
		"response":     string(respJSON),
		"completed_at": now,
		"expires_at":   expiresAt,
	}); err != nil {
		e.logger.Warn("failed to update async job: %v", err)
	}
}

// --- Cleaner ---

// AsyncJobCleaner manages the cleanup of expired async jobs.
type AsyncJobCleaner struct {
	store       LogStore
	logger      schemas.Logger
	stopCleanup chan struct{}
	mu          sync.Mutex
}

// NewAsyncJobCleaner creates a new AsyncJobCleaner instance.
func NewAsyncJobCleaner(store LogStore, logger schemas.Logger) *AsyncJobCleaner {
	return &AsyncJobCleaner{
		store:  store,
		logger: logger,
	}
}

// StartCleanupRoutine starts a goroutine that periodically cleans up expired async jobs.
func (c *AsyncJobCleaner) StartCleanupRoutine() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopCleanup != nil {
		return
	}

	c.stopCleanup = make(chan struct{})
	stopCh := c.stopCleanup

	go func() {
		// Run initial cleanup
		ctx, cancel := context.WithTimeout(context.Background(), asyncJobCleanupTimeout)
		c.cleanupExpiredJobs(ctx)
		cancel()

		ticker := time.NewTicker(asyncJobCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), asyncJobCleanupTimeout)
				c.cleanupExpiredJobs(ctx)
				cancel()
			case <-stopCh:
				c.logger.Debug("async job cleanup routine stopped")
				return
			}
		}
	}()
	c.logger.Debug("async job cleanup routine started (interval: %s)", asyncJobCleanupInterval)
}

// StopCleanupRoutine gracefully stops the cleanup goroutine.
func (c *AsyncJobCleaner) StopCleanupRoutine() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopCleanup == nil {
		c.logger.Debug("async job cleanup routine already stopped")
		return
	}

	close(c.stopCleanup)
	c.stopCleanup = nil
}

// cleanupExpiredJobs deletes expired async jobs and stale processing jobs.
func (c *AsyncJobCleaner) cleanupExpiredJobs(ctx context.Context) {
	deleted, err := c.store.DeleteExpiredAsyncJobs(ctx)
	if err != nil {
		c.logger.Warn("failed to delete expired async jobs: %v", err)
	} else if deleted > 0 {
		c.logger.Debug("async job cleanup completed: deleted %d expired jobs", deleted)
	}

	// Clean up jobs stuck in "processing" for more than 24 hours
	// This handles edge cases like marshal failures or server crashes
	staleSince := time.Now().UTC().Add(-asyncJobStaleProcessingHours * time.Hour)
	staleDeleted, err := c.store.DeleteStaleAsyncJobs(ctx, staleSince)
	if err != nil {
		c.logger.Warn("failed to delete stale processing async jobs: %v", err)
	} else if staleDeleted > 0 {
		c.logger.Warn("async job cleanup: deleted %d stale processing jobs (stuck > %dh)", staleDeleted, asyncJobStaleProcessingHours)
	}
}
