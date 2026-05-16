package async

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Integration route tests verify that x-bf-async and x-bf-async-id headers work on
// provider integration routes. These routes apply a provider-specific response converter,
// so the envelope differs from /v1/async/* endpoints:
//
//	Submit  (x-bf-async: true)     → HTTP 200 (not 202)
//	Retrieve (x-bf-async-id: <id>) → HTTP 200 for any job state
//
// Optional env:
//
//	BIFROST_INTEGRATION_PATH  — override the default /openai/v1/responses
//	BIFROST_INTEGRATION_MODEL — model string; defaults to ASYNC_RESPONSES_MODEL default
//
// Note: only routes with AsyncResponsesResponseConverter support x-bf-async.
// AsyncChatResponseConverter is not implemented on any route — the Responses API
// path (/openai/v1/responses) is the only integration route that supports async.
func integrationPath() string {
	return envOr("BIFROST_INTEGRATION_PATH", "/openai/v1/responses")
}

func integrationModel() string {
	if v := os.Getenv("BIFROST_INTEGRATION_MODEL"); v != "" {
		return v
	}
	return modelFor("ASYNC_RESPONSES_MODEL")
}

// assert4xx fails the test unless code is a 4xx client error, catching 5xx regressions.
func assert4xx(t *testing.T, code int, body []byte) {
	t.Helper()
	if code < 400 || code >= 500 {
		t.Fatalf("expected 4xx, got %d: %s", code, body)
	}
}

// integrationJobID extracts the job UUID from an integration route response body.
// All integration converters preserve the async job ID in the top-level "id" field.
func integrationJobID(t *testing.T, body []byte) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	if id, ok := m["id"].(string); ok {
		return id
	}
	return ""
}

// pollIntegration POSTs to an integration path with x-bf-async-id header to retrieve a job.
// Integration routes use the same POST method for both submit and retrieve.
func pollIntegration(t *testing.T, path, jobID string, headers map[string]string) (int, []byte) {
	t.Helper()
	h := make(map[string]string, len(headers)+1)
	maps.Copy(h, headers)
	h["x-bf-async-id"] = jobID
	code, body := submitRaw(t, path, []byte("{}"), "application/json", h)
	return code, body
}

// integrationSubmitBody returns a minimal Responses API body for the integration path.
func integrationSubmitBody() map[string]any {
	return map[string]any{
		"model": integrationModel(),
		"input": "Say hello in one word.",
	}
}

// TestIntegration_AsyncCreate_Returns200WithJobID submits a chat request via an integration
// route with x-bf-async header and confirms the response is 200 OK with a job UUID.
// Integration routes return 200 (not 202) because the response passes through the
// provider-specific converter before being sent.
func TestIntegration_AsyncCreate_Returns200WithJobID(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			headers := withRawHeader(mode.headers, "x-bf-async", "true")
			code, _, body := submitJSON(t, integrationPath(), integrationSubmitBody(), headers)
			if code != http.StatusOK {
				t.Fatalf("expected 200 from integration async submit, got %d: %s", code, body)
			}
			jobID := integrationJobID(t, body)
			if jobID == "" {
				t.Fatalf("no job id in integration route response: %s", body)
			}
			parts := strings.Split(jobID, "-")
			if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 ||
				len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
				t.Errorf("id %q does not look like a UUID", jobID)
			}
		})
	}
}

// TestIntegration_AsyncRetrieve_Returns200 submits an async job on an integration route
// and polls it via x-bf-async-id header, confirming retrieve also returns 200 OK.
func TestIntegration_AsyncRetrieve_Returns200(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			headers := withRawHeader(mode.headers, "x-bf-async", "true")
			code, _, body := submitJSON(t, integrationPath(), integrationSubmitBody(), headers)
			if code != http.StatusOK {
				t.Fatalf("submit failed with %d: %s", code, body)
			}
			jobID := integrationJobID(t, body)
			if jobID == "" {
				t.Fatalf("no job id in submit response: %s", body)
			}

			pollCode, pollBody := pollIntegration(t, integrationPath(), jobID, mode.headers)
			if pollCode != http.StatusOK {
				t.Errorf("expected 200 on integration retrieve, got %d: %s", pollCode, pollBody)
			}
		})
	}
}

// TestIntegration_AsyncRetrieve_NonExistentJob_Returns4xx polls an integration route with
// a fake job ID and confirms a non-success status code is returned.
func TestIntegration_AsyncRetrieve_NonExistentJob_Returns4xx(t *testing.T) {
	const fakeID = "00000000-0000-0000-0000-000000000000"
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			code, body := pollIntegration(t, integrationPath(), fakeID, mode.headers)
			assert4xx(t, code, body)
		})
	}
}

// TestIntegration_AsyncCreate_StreamRejected confirms that submitting a streaming request
// via x-bf-async is rejected — streaming and async are mutually exclusive.
func TestIntegration_AsyncCreate_StreamRejected(t *testing.T) {
	streamBody := map[string]any{
		"model":  integrationModel(),
		"input":  "Hello",
		"stream": true,
	}
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			headers := withRawHeader(mode.headers, "x-bf-async", "true")
			code, _, body := submitJSON(t, integrationPath(), streamBody, headers)
			assert4xx(t, code, body)
		})
	}
}

// TestIntegration_VKScope_DifferentKey_Returns4xx submits an async job on an integration
// route with VK1 and retrieves with VK2, confirming VK isolation works on integration routes.
func TestIntegration_VKScope_DifferentKey_Returns4xx(t *testing.T) {
	if cfg.VK == "" || cfg.AltVK == "" {
		t.Skip("both BIFROST_VK and BIFROST_ALT_VK must be set")
	}
	headers := withRawHeader(vkHeaders(cfg.VK), "x-bf-async", "true")
	code, _, body := submitJSON(t, integrationPath(), integrationSubmitBody(), headers)
	if code != http.StatusOK {
		t.Fatalf("submit failed with %d: %s", code, body)
	}
	jobID := integrationJobID(t, body)
	if jobID == "" {
		t.Fatalf("no job id in submit response: %s", body)
	}

	pollCode, pollBody := pollIntegration(t, integrationPath(), jobID, vkHeaders(cfg.AltVK))
	assert4xx(t, pollCode, pollBody)
}
