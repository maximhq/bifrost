package logstore

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type asyncTestLogger struct{}

func (asyncTestLogger) Debug(string, ...any)                   {}
func (asyncTestLogger) Info(string, ...any)                    {}
func (asyncTestLogger) Warn(string, ...any)                    {}
func (asyncTestLogger) Error(string, ...any)                   {}
func (asyncTestLogger) Fatal(string, ...any)                   {}
func (asyncTestLogger) SetLevel(schemas.LogLevel)              {}
func (asyncTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (asyncTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

type testGovernanceStore struct {
	virtualKeys map[string]*configstoreTables.TableVirtualKey
}

func (t *testGovernanceStore) GetVirtualKey(_ context.Context, vkValue string) (*configstoreTables.TableVirtualKey, bool) {
	vk, ok := t.virtualKeys[vkValue]
	return vk, ok
}

func newTestAsyncExecutor(t *testing.T) *AsyncJobExecutor {
	t.Helper()
	ctx := context.Background()

	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: ":memory:"}, asyncTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })

	govStore := &testGovernanceStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"sk-bf-test": {ID: "vk-123", Value: *schemas.NewSecretVar("sk-bf-test")},
		},
	}

	return NewAsyncJobExecutor(store, govStore, nil, nil, asyncTestLogger{})
}

// waitForJobCompletion polls until the operation callback has been invoked.
func waitForJobCompletion(t *testing.T, done *atomic.Bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for async job execution")
}

// waitForJobStatus polls FindAsyncJobByID until the job reaches a terminal
// status (completed or failed), or times out. This avoids a fragile time.Sleep
// between the operation callback completing and the DB update finishing.
// Processing is intermediate and must not be treated as terminal.
func waitForJobStatus(t *testing.T, store LogStore, jobID string) *AsyncJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.FindAsyncJobByID(context.Background(), jobID)
		if err == nil && (job.Status == schemas.AsyncJobStatusCompleted || job.Status == schemas.AsyncJobStatusFailed) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for async job to reach terminal status")
	return nil
}

func TestSubmitJob_PropagatesContextValues(t *testing.T) {
	executor := newTestAsyncExecutor(t)

	capturedCtx := schemas.NewBifrostContext(context.Background(), time.Now().Add(1*time.Minute))
	capturedCtx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	capturedCtx.SetValue(schemas.BifrostContextKey("x-bf-eh-custom"), "custom-value")
	capturedCtx.SetValue(schemas.BifrostContextKey("x-bf-prom-env"), "production")
	var done atomic.Bool

	operation := func(bgCtx *schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		capturedCtx = bgCtx
		done.Store(true)
		return map[string]string{"status": "ok"}, nil
	}

	job, err := executor.SubmitJob(capturedCtx, 3600, operation, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	assert.Equal(t, "sk-bf-test", capturedCtx.Value(schemas.BifrostContextKeyVirtualKey))
	assert.Equal(t, "production", capturedCtx.Value(schemas.BifrostContextKey("x-bf-prom-env")))
	assert.Equal(t, "custom-value", capturedCtx.Value(schemas.BifrostContextKey("x-bf-eh-custom")))
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))
}

func TestSubmitJob_StoresRequestID(t *testing.T) {
	// File-backed store: the test polls the job row across goroutines, and a
	// :memory: DSN gives each pooled connection its own database.
	executor := newWebhookTestExecutor(t, nil)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-123")

	job, err := executor.SubmitJob(ctx, 3600, func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		return map[string]string{"status": "ok"}, nil
	}, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	assert.Equal(t, "req-123", job.RequestID, "the submit call's request id keys the job's LLM log entry")

	stored := waitForJobStatus(t, executor.logstore, job.ID)
	assert.Equal(t, "req-123", stored.RequestID)
}

func TestSubmitJob_NilContextValues(t *testing.T) {
	executor := newTestAsyncExecutor(t)

	var capturedCtx *schemas.BifrostContext
	var done atomic.Bool

	operation := func(bgCtx *schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		capturedCtx = bgCtx
		done.Store(true)
		return map[string]string{"status": "ok"}, nil
	}

	job, err := executor.SubmitJob(capturedCtx, 3600, operation, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	assert.NotNil(t, capturedCtx)
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))
}

func TestSubmitJob_EmptyContextValues(t *testing.T) {
	executor := newTestAsyncExecutor(t)

	var capturedCtx *schemas.BifrostContext
	var done atomic.Bool

	operation := func(bgCtx *schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		capturedCtx = bgCtx
		done.Store(true)
		return map[string]string{"status": "ok"}, nil
	}

	job, err := executor.SubmitJob(capturedCtx, 3600, operation, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	assert.NotNil(t, capturedCtx)
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))
}

func TestSubmitJob_AsyncFlagOverridesContextValues(t *testing.T) {
	executor := newTestAsyncExecutor(t)

	inputCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	inputCtx.SetValue(schemas.BifrostIsAsyncRequest, false)

	var capturedCtx *schemas.BifrostContext
	var done atomic.Bool
	operation := func(bgCtx *schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		capturedCtx = bgCtx
		done.Store(true)
		return map[string]string{"status": "ok"}, nil
	}

	job, err := executor.SubmitJob(inputCtx, 3600, operation, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	// BifrostIsAsyncRequest must be true — set AFTER restoring context values
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))
}

func TestSubmitJob_OperationFailure_PreservesContext(t *testing.T) {
	executor := newTestAsyncExecutor(t)

	inputCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	inputCtx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")

	var capturedCtx *schemas.BifrostContext
	var done atomic.Bool

	statusCode := fasthttp.StatusBadRequest
	operation := func(bgCtx *schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		capturedCtx = bgCtx
		done.Store(true)
		return nil, &schemas.BifrostError{
			StatusCode: &statusCode,
			Error:      &schemas.ErrorField{Message: "test error"},
		}
	}

	job, err := executor.SubmitJob(inputCtx, 3600, operation, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	// Context values should still be available even when operation fails
	assert.Equal(t, "sk-bf-test", capturedCtx.Value(schemas.BifrostContextKeyVirtualKey))
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))

	// Verify job was marked as failed — poll until DB update completes
	retrievedJob := waitForJobStatus(t, executor.logstore, job.ID)
	assert.Equal(t, schemas.AsyncJobStatusFailed, retrievedJob.Status)
}

// recordingWebhookDispatcher captures every terminal-state notification.
type recordingWebhookDispatcher struct {
	mu   sync.Mutex
	jobs []AsyncJob
}

func (r *recordingWebhookDispatcher) EnqueueJobEvent(_ context.Context, job *AsyncJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, *job)
}

func (r *recordingWebhookDispatcher) enqueued() []AsyncJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AsyncJob(nil), r.jobs...)
}

// waitForWebhookEnqueue polls until the dispatcher has received n
// notifications; the enqueue happens right after the terminal DB update, so
// terminal job status alone does not guarantee it already fired.
func waitForWebhookEnqueue(t *testing.T, dispatcher *recordingWebhookDispatcher, n int) []AsyncJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if jobs := dispatcher.enqueued(); len(jobs) >= n {
			return jobs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for webhook enqueue")
	return nil
}

// newWebhookTestExecutor builds an executor over a file-backed SQLite store:
// tests below poll job rows from the test goroutine while executeJob writes
// from another, and a :memory: DSN gives each pooled connection its own
// database.
func newWebhookTestExecutor(t *testing.T, dispatcher WebhookDispatcher) *AsyncJobExecutor {
	t.Helper()
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "asyncwebhooks.db"),
	}, asyncTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })
	return NewAsyncJobExecutor(store, &testGovernanceStore{}, dispatcher, testWebhookManager{}, asyncTestLogger{})
}

// testWebhookManager resolves the single endpoint used by webhook tests.
type testWebhookManager struct{}

func (testWebhookManager) WebhookEndpointByName(name string) (*configstoreTables.TableWebhookEndpoint, bool) {
	switch name {
	case "receiver":
		return &configstoreTables.TableWebhookEndpoint{ID: "ep-1", Name: "receiver"}, true
	case "off":
		return &configstoreTables.TableWebhookEndpoint{ID: "ep-2", Name: "off", Disabled: true}, true
	default:
		return nil, false
	}
}

func submitWebhookTestJob(t *testing.T, executor *AsyncJobExecutor, operation AsyncOperation) *AsyncJob {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyAsyncWebhookEndpoint, "receiver")
	job, err := executor.SubmitJob(ctx, 3600, operation, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

func TestSubmitJob_StampsWebhookEndpointID(t *testing.T) {
	// A non-nil dispatcher is required to reach endpoint-name resolution: the
	// submit path fails fast when delivery is unwired, before stamping.
	executor := newWebhookTestExecutor(t, &recordingWebhookDispatcher{})

	job := submitWebhookTestJob(t, executor, func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		return map[string]string{"status": "ok"}, nil
	})
	require.NotNil(t, job.WebhookEndpointID)
	assert.Equal(t, "ep-1", *job.WebhookEndpointID)

	stored := waitForJobStatus(t, executor.logstore, job.ID)
	require.NotNil(t, stored.WebhookEndpointID, "the endpoint reference must be persisted on the job row")
	assert.Equal(t, "ep-1", *stored.WebhookEndpointID)
}

func TestSubmitJob_RejectsUnusableWebhookReference(t *testing.T) {
	// A non-nil dispatcher lets the submit path reach WebhookEndpointByName so
	// the unknown/disabled reference branches are exercised rather than short-
	// circuiting on the fail-fast "dispatcher not available" guard.
	executor := newWebhookTestExecutor(t, &recordingWebhookDispatcher{})

	operation := func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		return map[string]string{"status": "ok"}, nil
	}
	for name, reference := range map[string]string{"unknown": "no-such-endpoint", "disabled": "off"} {
		t.Run(name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyAsyncWebhookEndpoint, reference)
			job, err := executor.SubmitJob(ctx, 3600, operation, schemas.ChatCompletionRequest)
			require.Error(t, err)
			assert.Nil(t, job)
		})
	}
}

func TestSubmitJob_NoWebhookWithoutContextValue(t *testing.T) {
	dispatcher := &recordingWebhookDispatcher{}
	executor := newWebhookTestExecutor(t, dispatcher)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	job, err := executor.SubmitJob(ctx, 3600, func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		return map[string]string{"status": "ok"}, nil
	}, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	assert.Nil(t, job.WebhookEndpointID)

	waitForJobStatus(t, executor.logstore, job.ID)
	assert.Empty(t, dispatcher.enqueued(), "jobs without a webhook reference must not notify")
}

func TestExecuteJob_WebhookEnqueuedOnSuccess(t *testing.T) {
	dispatcher := &recordingWebhookDispatcher{}
	executor := newWebhookTestExecutor(t, dispatcher)

	job := submitWebhookTestJob(t, executor, func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		return map[string]string{"status": "ok"}, nil
	})

	enqueued := waitForWebhookEnqueue(t, dispatcher, 1)
	require.Len(t, enqueued, 1)
	assert.Equal(t, job.ID, enqueued[0].ID)
	assert.Equal(t, schemas.AsyncJobStatusCompleted, enqueued[0].Status)
	require.NotNil(t, enqueued[0].WebhookEndpointID)
	assert.Equal(t, "ep-1", *enqueued[0].WebhookEndpointID)
}

func TestExecuteJob_WebhookEnqueuedOnFailure(t *testing.T) {
	dispatcher := &recordingWebhookDispatcher{}
	executor := newWebhookTestExecutor(t, dispatcher)

	statusCode := fasthttp.StatusBadRequest
	job := submitWebhookTestJob(t, executor, func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{StatusCode: &statusCode, Error: &schemas.ErrorField{Message: "test error"}}
	})

	enqueued := waitForWebhookEnqueue(t, dispatcher, 1)
	require.Len(t, enqueued, 1)
	assert.Equal(t, job.ID, enqueued[0].ID)
	assert.Equal(t, schemas.AsyncJobStatusFailed, enqueued[0].Status)
}

func TestExecuteJob_WebhookEnqueuedOnPanic(t *testing.T) {
	dispatcher := &recordingWebhookDispatcher{}
	executor := newWebhookTestExecutor(t, dispatcher)

	job := submitWebhookTestJob(t, executor, func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		panic("boom")
	})

	enqueued := waitForWebhookEnqueue(t, dispatcher, 1)
	require.Len(t, enqueued, 1)
	assert.Equal(t, job.ID, enqueued[0].ID)
	assert.Equal(t, schemas.AsyncJobStatusFailed, enqueued[0].Status)

	stored := waitForJobStatus(t, executor.logstore, job.ID)
	assert.Equal(t, schemas.AsyncJobStatusFailed, stored.Status)
}

func TestExecuteJob_NilDispatcherIsSafe(t *testing.T) {
	// No webhook endpoint is set in context: SubmitJob fails fast when one is
	// requested without a dispatcher wired, so this only exercises executeJob's
	// terminal-state notifyWebhook path with a nil dispatcher.
	executor := newWebhookTestExecutor(t, nil)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	job, err := executor.SubmitJob(ctx, 3600, func(*schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		return map[string]string{"status": "ok"}, nil
	}, schemas.ChatCompletionRequest)
	require.NoError(t, err)
	require.NotNil(t, job)

	stored := waitForJobStatus(t, executor.logstore, job.ID)
	assert.Equal(t, schemas.AsyncJobStatusCompleted, stored.Status)
}

func TestAsyncJobCleaner_ReapsExpiredWebhookDeliveries(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: ":memory:"}, asyncTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })

	expiredAt := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, store.CreateWebhookDelivery(ctx, &WebhookDelivery{
		ID: "d-expired", WebhookID: "wh-1", EndpointID: "ep-1", AsyncJobID: "job-1",
		Event: configstoreTables.WebhookEventAsyncJobCompleted, AttemptNo: 1,
		Outcome: WebhookDeliveryOutcomeDelivered, CreatedAt: expiredAt.Add(-time.Hour), ExpiresAt: &expiredAt,
	}))
	require.NoError(t, store.CreateWebhookDelivery(ctx, &WebhookDelivery{
		ID: "d-live", WebhookID: "wh-2", EndpointID: "ep-1", AsyncJobID: "job-2",
		Event: configstoreTables.WebhookEventAsyncJobCompleted, AttemptNo: 1,
		Outcome: WebhookDeliveryOutcomeDelivered, CreatedAt: time.Now().UTC(),
	}))

	cleaner := NewAsyncJobCleaner(store, asyncTestLogger{})
	cleaner.cleanupExpiredJobs(ctx)

	_, err = store.FindWebhookDeliveryByID(ctx, "d-expired")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.FindWebhookDeliveryByID(ctx, "d-live")
	assert.NoError(t, err)
}
