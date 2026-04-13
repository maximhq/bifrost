package logstore

import (
	"context"
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

func (asyncTestLogger) Debug(string, ...any)                                  {}
func (asyncTestLogger) Info(string, ...any)                                   {}
func (asyncTestLogger) Warn(string, ...any)                                   {}
func (asyncTestLogger) Error(string, ...any)                                  {}
func (asyncTestLogger) Fatal(string, ...any)                                  {}
func (asyncTestLogger) SetLevel(schemas.LogLevel)                             {}
func (asyncTestLogger) SetOutputType(schemas.LoggerOutputType)                {}
func (asyncTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

type testGovernanceStore struct {
	virtualKeys map[string]*configstoreTables.TableVirtualKey
}

func (t *testGovernanceStore) GetVirtualKey(vkValue string) (*configstoreTables.TableVirtualKey, bool) {
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
			"sk-bf-test": {ID: "vk-123", Value: "sk-bf-test"},
		},
	}

	return NewAsyncJobExecutor(store, govStore, asyncTestLogger{})
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

	// Simulate original request context values
	contextValues := map[any]any{
		schemas.BifrostContextKeyVirtualKey: "sk-bf-test",
		schemas.BifrostContextKey("x-bf-prom-env"):  "production",
		schemas.BifrostContextKey("x-bf-eh-custom"): "custom-value",
	}

	var capturedCtx *schemas.BifrostContext
	var done atomic.Bool

	operation := func(bgCtx *schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		capturedCtx = bgCtx
		done.Store(true)
		return map[string]string{"status": "ok"}, nil
	}

	job, err := executor.SubmitJob(strPtr("sk-bf-test"), 3600, operation, schemas.ChatCompletionRequest, contextValues)
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	assert.Equal(t, "sk-bf-test", capturedCtx.Value(schemas.BifrostContextKeyVirtualKey))
	assert.Equal(t, "production", capturedCtx.Value(schemas.BifrostContextKey("x-bf-prom-env")))
	assert.Equal(t, "custom-value", capturedCtx.Value(schemas.BifrostContextKey("x-bf-eh-custom")))
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))
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

	job, err := executor.SubmitJob(strPtr("sk-bf-test"), 3600, operation, schemas.ChatCompletionRequest, nil)
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

	job, err := executor.SubmitJob(strPtr("sk-bf-test"), 3600, operation, schemas.ChatCompletionRequest, map[any]any{})
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	assert.NotNil(t, capturedCtx)
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))
}

func TestSubmitJob_AsyncFlagOverridesContextValues(t *testing.T) {
	executor := newTestAsyncExecutor(t)

	// Pass context values that try to set BifrostIsAsyncRequest to false
	contextValues := map[any]any{
		schemas.BifrostIsAsyncRequest: false,
	}

	var capturedCtx *schemas.BifrostContext
	var done atomic.Bool

	operation := func(bgCtx *schemas.BifrostContext) (interface{}, *schemas.BifrostError) {
		capturedCtx = bgCtx
		done.Store(true)
		return map[string]string{"status": "ok"}, nil
	}

	job, err := executor.SubmitJob(strPtr("sk-bf-test"), 3600, operation, schemas.ChatCompletionRequest, contextValues)
	require.NoError(t, err)
	require.NotNil(t, job)

	waitForJobCompletion(t, &done)

	// BifrostIsAsyncRequest must be true — set AFTER restoring context values
	assert.Equal(t, true, capturedCtx.Value(schemas.BifrostIsAsyncRequest))
}

func TestSubmitJob_OperationFailure_PreservesContext(t *testing.T) {
	executor := newTestAsyncExecutor(t)

	contextValues := map[any]any{
		schemas.BifrostContextKeyVirtualKey: "sk-bf-test",
	}

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

	job, err := executor.SubmitJob(strPtr("sk-bf-test"), 3600, operation, schemas.ChatCompletionRequest, contextValues)
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
