package logstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
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
type AsyncOperation func(ctx *schemas.BifrostContext) (any, *schemas.BifrostError)

// GovernanceStore is an interface that provides access to the governance store.
type GovernanceStore interface {
	GetVirtualKey(ctx context.Context, vkValue string) (*configstoreTables.TableVirtualKey, bool)
}

// WebhookDispatcher queues a webhook notification for a job that reached a
// terminal state. Implementations must not block on receiver I/O.
type WebhookDispatcher interface {
	EnqueueJobEvent(ctx context.Context, job *AsyncJob)
}

// WebhookManager resolves registered webhook endpoints so submit-time
// references can be validated before a job is accepted.
type WebhookManager interface {
	WebhookEndpointByName(endpointName string) (*configstoreTables.TableWebhookEndpoint, bool)
}

// AsyncJobExecutor manages async job creation and background execution.
type AsyncJobExecutor struct {
	logstore          LogStore
	governanceStore   GovernanceStore
	webhookDispatcher WebhookDispatcher
	webhookManager    WebhookManager
	logger            schemas.Logger
}

// NewAsyncJobExecutor creates a new AsyncJobExecutor. A nil webhookDispatcher
// leaves webhook notification disabled; a nil webhookManager rejects any
// submit that references a webhook endpoint.
func NewAsyncJobExecutor(logstore LogStore, governanceStore GovernanceStore, webhookDispatcher WebhookDispatcher, webhookManager WebhookManager, logger schemas.Logger) *AsyncJobExecutor {
	return &AsyncJobExecutor{
		logstore:          logstore,
		governanceStore:   governanceStore,
		webhookDispatcher: webhookDispatcher,
		webhookManager:    webhookManager,
		logger:            logger,
	}
}

// RetrieveJob retrieves a job by its ID.
func (e *AsyncJobExecutor) RetrieveJob(ctx context.Context, jobID string, vkValue *string, operationType schemas.RequestType) (*AsyncJob, error) {
	job, err := e.logstore.FindAsyncJobByID(ctx, jobID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("job not found or expired")
		}
		return nil, fmt.Errorf("%w: %w", ErrJobInternal, err)
	}
	if job.VirtualKeyID != nil {
		if vkValue == nil {
			return nil, fmt.Errorf("virtual key is required")
		}
		vk, ok := e.governanceStore.GetVirtualKey(ctx, *vkValue)
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
func (e *AsyncJobExecutor) SubmitJob(bifrostCtx *schemas.BifrostContext, resultTTL int, operation AsyncOperation, operationType schemas.RequestType) (*AsyncJob, error) {
	if resultTTL <= 0 {
		resultTTL = DefaultAsyncJobResultTTL
	}

	virtualKeyValue := getVirtualKeyFromContext(bifrostCtx)

	var virtualKeyID *string
	if virtualKeyValue != nil {
		vk, ok := e.governanceStore.GetVirtualKey(bifrostCtx, *virtualKeyValue)
		if !ok {
			return nil, fmt.Errorf("virtual key not found")
		}
		virtualKeyID = &vk.ID
	}

	endpoint, err := e.getWebhookEndpointIfPresent(bifrostCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve webhook endpoint: %w", err)
	}

	// The background execution inherits the submit call's request id, so its
	// LLM log row is keyed by it — store it for reconciliation.
	requestID := ""
	if bifrostCtx != nil {
		requestID = bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyRequestID)
	}

	now := time.Now().UTC()
	job := &AsyncJob{
		ID:           uuid.New().String(),
		RequestID:    requestID,
		Status:       schemas.AsyncJobStatusPending,
		RequestType:  operationType,
		VirtualKeyID: virtualKeyID,
		ResultTTL:    resultTTL,
		CreatedAt:    now,
	}

	if endpoint != nil {
		job.WebhookEndpointID = &endpoint.ID
	}

	ctx := context.Background()
	if err := e.logstore.CreateAsyncJob(ctx, job); err != nil {
		return nil, fmt.Errorf("failed to create async job: %w", err)
	}

	var contextValues map[any]any
	if bifrostCtx != nil {
		contextValues = bifrostCtx.GetUserValues()
	}
	go e.executeJob(job, operation, contextValues)

	return job, nil
}

// executeJob runs the operation in the background and updates the job record.
func (e *AsyncJobExecutor) executeJob(job *AsyncJob, operation AsyncOperation, contextValues map[any]any) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Restore original request context values (virtual key, tracing headers, etc.)
	for k, v := range contextValues {
		ctx.SetValue(k, v)
	}

	// Clear trace context inherited from the original HTTP request.
	ctx.ClearValue(schemas.BifrostContextKeyTraceID)
	ctx.ClearValue(schemas.BifrostContextKeyParentSpanID)
	ctx.ClearValue(schemas.BifrostContextKeySpanID)

	markFailed := func(msg string) {
		now := time.Now().UTC()
		expiresAt := now.Add(time.Duration(job.ResultTTL) * time.Second)
		errJSON, _ := sonic.Marshal(&schemas.BifrostError{Error: &schemas.ErrorField{Message: msg}})
		if err := e.logstore.UpdateAsyncJob(ctx, job.ID, map[string]any{
			"status":       schemas.AsyncJobStatusFailed,
			"status_code":  fasthttp.StatusInternalServerError,
			"error":        string(errJSON),
			"completed_at": now,
			"expires_at":   expiresAt,
		}); err != nil {
			e.logger.Warn("failed to update async job to failed: %v", err)
			return
		}
		e.notifyWebhook(ctx, job, schemas.AsyncJobStatusFailed)
	}

	// The bifrost execution flow is very stable and panics are not expected.
	// This recover is purely defensive to ensure the job always reaches a terminal
	// state rather than being stuck in "processing" if an unexpected panic occurs.
	defer func() {
		if r := recover(); r != nil {
			e.logger.Warn("async job %s panicked: %v", job.ID, r)
			markFailed(fmt.Sprintf("internal error: %v", r))
		}
	}()

	// Mark as processing
	if err := e.logstore.UpdateAsyncJob(ctx, job.ID, map[string]any{
		"status": schemas.AsyncJobStatusProcessing,
	}); err != nil {
		e.logger.Warn("failed to update async job: %v", err)
	}

	ctx.SetValue(schemas.BifrostIsAsyncRequest, true)

	// Execute the operation
	resp, bifrostErr := operation(ctx)

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(job.ResultTTL) * time.Second)

	if bifrostErr != nil {
		errJSON, err := sonic.Marshal(bifrostErr)
		if err != nil {
			e.logger.Warn("failed to marshal bifrost error: %v", err)
			markFailed(fmt.Sprintf("failed to serialize error response: %v", err))
			return
		}
		statusCode := fasthttp.StatusInternalServerError
		if bifrostErr.StatusCode != nil {
			statusCode = *bifrostErr.StatusCode
		}
		if err := e.logstore.UpdateAsyncJob(ctx, job.ID, map[string]interface{}{
			"status":       schemas.AsyncJobStatusFailed,
			"status_code":  statusCode,
			"error":        string(errJSON),
			"completed_at": now,
			"expires_at":   expiresAt,
		}); err != nil {
			e.logger.Warn("failed to update async job: %v", err)
			return
		}
		e.notifyWebhook(ctx, job, schemas.AsyncJobStatusFailed)
		return
	}

	respJSON, err := sonic.Marshal(resp)
	if err != nil {
		e.logger.Warn("failed to marshal result: %v", err)
		markFailed(fmt.Sprintf("failed to serialize result: %v", err))
		return
	}
	if err := e.logstore.UpdateAsyncJob(ctx, job.ID, map[string]interface{}{
		"status":       schemas.AsyncJobStatusCompleted,
		"status_code":  fasthttp.StatusOK,
		"response":     string(respJSON),
		"completed_at": now,
		"expires_at":   expiresAt,
	}); err != nil {
		e.logger.Warn("failed to update async job: %v", err)
		return
	}
	e.notifyWebhook(ctx, job, schemas.AsyncJobStatusCompleted)
}

// notifyWebhook hands a job that just reached a terminal state to the
// webhook dispatcher. It only fires after the terminal update committed: a
// job whose terminal write failed still reads as processing, so notifying
// for it would contradict what polling callers see.
func (e *AsyncJobExecutor) notifyWebhook(ctx context.Context, job *AsyncJob, status schemas.AsyncJobStatus) {
	if e.webhookDispatcher == nil || job.WebhookEndpointID == nil {
		return
	}
	// The job's terminal state is already committed; a dispatcher panic must
	// not reach executeJob's recovery, which would overwrite a completed job
	// as failed.
	defer func() {
		if r := recover(); r != nil {
			e.logger.Warn("async job %s webhook enqueue panicked: %v", job.ID, r)
		}
	}()
	notified := *job
	notified.Status = status
	e.webhookDispatcher.EnqueueJobEvent(ctx, &notified)
}

// getWebhookEndpointIfPresent resolves the webhook endpoint name the caller
// attached to the request context, if any. A reference that does not resolve
// to an existing, enabled endpoint is an error: accepting the job anyway
// would silently drop the notification the caller asked for.
func (e *AsyncJobExecutor) getWebhookEndpointIfPresent(ctx *schemas.BifrostContext) (*configstoreTables.TableWebhookEndpoint, error) {
	if ctx == nil {
		return nil, nil
	}
	webhookEndpointName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyAsyncWebhookEndpoint)
	if webhookEndpointName == "" {
		return nil, nil
	}
	if e.webhookManager == nil {
		return nil, fmt.Errorf("webhook manager is not available")
	}
	endpoint, ok := e.webhookManager.WebhookEndpointByName(webhookEndpointName)
	if !ok {
		return nil, fmt.Errorf("%w: unknown webhook endpoint with name %q", ErrInvalidWebhookReference, webhookEndpointName)
	}
	if endpoint.Disabled {
		return nil, fmt.Errorf("%w: webhook endpoint %q is disabled", ErrInvalidWebhookReference, endpoint.Name)
	}
	return endpoint, nil
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

	// Reap webhook delivery history whose retention window has passed.
	deliveriesDeleted, err := c.store.DeleteExpiredWebhookDeliveries(ctx)
	if err != nil {
		c.logger.Warn("failed to delete expired webhook deliveries: %v", err)
	} else if deliveriesDeleted > 0 {
		c.logger.Debug("webhook delivery cleanup completed: deleted %d expired records", deliveriesDeleted)
	}
}

// getVirtualKeyFromContext extracts the virtual key value from context.
// Returns nil if no VK is present (e.g., direct key mode or no governance),
// or if the context itself is nil (callers like SubmitJob may be invoked with
// a nil ctx by background paths that don't carry a VK).
func getVirtualKeyFromContext(ctx *schemas.BifrostContext) *string {
	if ctx == nil {
		return nil
	}
	vkValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	if vkValue == "" {
		return nil
	}
	return &vkValue
}
