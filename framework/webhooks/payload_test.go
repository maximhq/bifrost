package webhooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAsyncJob() *logstore.AsyncJob {
	created := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	completed := created.Add(time.Minute)
	expires := completed.Add(time.Hour)
	return &logstore.AsyncJob{
		ID:          "job-1",
		Status:      schemas.AsyncJobStatusCompleted,
		RequestType: schemas.ChatCompletionRequest,
		Response:    `{"choices":[{"index":0}]}`,
		StatusCode:  200,
		CreatedAt:   created,
		CompletedAt: &completed,
		ExpiresAt:   &expires,
	}
}

func testFailedAsyncJob() *logstore.AsyncJob {
	created := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	completed := created.Add(time.Minute)
	expires := completed.Add(time.Hour)
	return &logstore.AsyncJob{
		ID:          "job-1",
		Status:      schemas.AsyncJobStatusFailed,
		RequestType: schemas.ChatCompletionRequest,
		Error:       `{"error":{"message":"upstream timed out"}}`,
		StatusCode:  500,
		CreatedAt:   created,
		CompletedAt: &completed,
		ExpiresAt:   &expires,
	}
}

func decodeEnvelope(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(body, &envelope))
	return envelope
}

func TestRenderPayloadThin(t *testing.T) {
	now := time.Now().UTC()
	body, err := renderPayload(testAsyncJob(), tables.WebhookEventAsyncJobCompleted, false, 256*1024, now)
	require.NoError(t, err)

	envelope := decodeEnvelope(t, body)
	assert.Equal(t, "async_job.completed", envelope["event"])
	data := envelope["data"].(map[string]any)
	assert.Equal(t, "job-1", data["job_id"])
	assert.Equal(t, "chat_completion", data["request_type"])
	assert.Equal(t, "completed", data["status"])
	assert.Equal(t, float64(200), data["status_code"])
	assert.Equal(t, "/v1/async/chat/completions/job-1", data["result_url"])
	assert.Contains(t, data, "result_expires_at")
	assert.NotContains(t, data, "response")
	assert.NotContains(t, data, "response_omitted")
	assert.NotContains(t, data, "error")
	assert.NotContains(t, data, "error_omitted")
	assert.NotContains(t, data, "result_expired")
}

func TestRenderPayloadIncludeResponse(t *testing.T) {
	now := time.Now().UTC()
	job := testAsyncJob()

	body, err := renderPayload(job, tables.WebhookEventAsyncJobCompleted, true, 256*1024, now)
	require.NoError(t, err)
	data := decodeEnvelope(t, body)["data"].(map[string]any)
	require.Contains(t, data, "response")
	assert.NotContains(t, data, "response_omitted")

	// An oversized response is dropped and flagged instead of inlined.
	job.Response = `{"padding":"` + strings.Repeat("x", 2048) + `"}`
	body, err = renderPayload(job, tables.WebhookEventAsyncJobCompleted, true, 1024, now)
	require.NoError(t, err)
	data = decodeEnvelope(t, body)["data"].(map[string]any)
	assert.NotContains(t, data, "response")
	assert.Equal(t, true, data["response_omitted"])
}

func TestRenderPayloadIncludeError(t *testing.T) {
	now := time.Now().UTC()
	job := testFailedAsyncJob()

	// A failed job's error is inlined the same way a completed job's
	// response is, gated by the same includeResponse toggle.
	body, err := renderPayload(job, tables.WebhookEventAsyncJobFailed, true, 256*1024, now)
	require.NoError(t, err)
	data := decodeEnvelope(t, body)["data"].(map[string]any)
	require.Contains(t, data, "error")
	assert.NotContains(t, data, "error_omitted")
	assert.NotContains(t, data, "response")

	// The toggle gates the error the same way it gates the response.
	body, err = renderPayload(job, tables.WebhookEventAsyncJobFailed, false, 256*1024, now)
	require.NoError(t, err)
	data = decodeEnvelope(t, body)["data"].(map[string]any)
	assert.NotContains(t, data, "error")

	// An oversized error is dropped and flagged instead of inlined.
	job.Error = `{"error":{"message":"` + strings.Repeat("x", 2048) + `"}}`
	body, err = renderPayload(job, tables.WebhookEventAsyncJobFailed, true, 1024, now)
	require.NoError(t, err)
	data = decodeEnvelope(t, body)["data"].(map[string]any)
	assert.NotContains(t, data, "error")
	assert.Equal(t, true, data["error_omitted"])
}

func TestRenderExpiredPayload(t *testing.T) {
	now := time.Now().UTC()
	body, err := renderExpiredPayload(&tables.TableWebhookJob{
		ID:         "wh-1",
		EndpointID: "ep-1",
		AsyncJobID: "job-1",
		Event:      tables.WebhookEventAsyncJobFailed,
	}, now)
	require.NoError(t, err)

	envelope := decodeEnvelope(t, body)
	assert.Equal(t, "async_job.failed", envelope["event"])
	data := envelope["data"].(map[string]any)
	assert.Equal(t, "job-1", data["job_id"])
	assert.Equal(t, "failed", data["status"], "status derives from the queued event")
	assert.Equal(t, true, data["result_expired"])
	// Nothing that lived only on the async row survives — including the
	// result URL, which needs the request type and is dead anyway.
	assert.NotContains(t, data, "result_url")
	assert.NotContains(t, data, "request_type")
	assert.NotContains(t, data, "status_code")
	assert.NotContains(t, data, "result_expires_at")
}

func TestAsyncResultPath(t *testing.T) {
	url, ok := AsyncResultPath(schemas.ChatCompletionRequest, "job-9")
	require.True(t, ok)
	assert.Equal(t, "/v1/async/chat/completions/job-9", url)

	_, ok = AsyncResultPath(schemas.CountTokensRequest, "job-9")
	assert.False(t, ok, "request types without an async result route must return false")

	for requestType, base := range asyncResultPathByType {
		url, ok := AsyncResultPath(requestType, "id")
		require.True(t, ok, requestType)
		assert.Equal(t, base+"/id", url)
	}
}

func TestEventForJobStatus(t *testing.T) {
	event, ok := EventForJobStatus(schemas.AsyncJobStatusCompleted)
	require.True(t, ok)
	assert.Equal(t, tables.WebhookEventAsyncJobCompleted, event)

	event, ok = EventForJobStatus(schemas.AsyncJobStatusFailed)
	require.True(t, ok)
	assert.Equal(t, tables.WebhookEventAsyncJobFailed, event)

	_, ok = EventForJobStatus(schemas.AsyncJobStatusProcessing)
	assert.False(t, ok)
	_, ok = EventForJobStatus(schemas.AsyncJobStatusPending)
	assert.False(t, ok)
}
