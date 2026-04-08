package enterprise_test

// ═══════════════════════════════════════════════════════════════════════════════
// TC-001 — Inference API
// Coverage: Chat completion (stream/non-stream), auth, concurrent safety,
//           model restriction, health check, embeddings, malformed requests.
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// chatBody builds a standard OpenAI-compatible chat completion body.
func chatBody(model, content string, stream bool) map[string]interface{} {
	return map[string]interface{}{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": content}},
		"stream":   stream,
	}
}

// mustMarshal encodes v as JSON, failing the test on error.
func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TC-001-025 — Health check endpoint.
func TestInference025HealthCheck(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{Method: http.MethodGet, Path: "/health"})
	expectStatus(t, resp, http.StatusOK)
	status, _ := resp.Body["status"].(string)
	if status != "ok" && status != "healthy" {
		t.Logf("health status = %q (non-standard but non-error)", status)
	}
}

// TC-001-001 — Chat completion non-streaming happy path.
func TestInference001ChatNonStreaming(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body:   chatBody("gpt-4o-mini", "Hello", false),
		Token:  apiUserToken,
	})
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Skipf("auth not configured for inference (status %d)", resp.StatusCode)
	}
	expectStatus(t, resp, http.StatusOK)
	choices, _ := resp.Body["choices"].([]interface{})
	if len(choices) == 0 {
		t.Errorf("expected at least one choice — body: %s", resp.Raw)
	}
}

// TC-001-002 — Chat completion streaming (SSE).
func TestInference002ChatStreaming(t *testing.T) {
	t.Parallel()
	body := mustMarshal(t, chatBody("gpt-4o-mini", "Count 1 to 3", true))
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiUserToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("streaming request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Skipf("auth not configured (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		t.Skipf("streaming not available: %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected SSE content-type, got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	chunkCount := 0
	sawDone := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				sawDone = true
				break
			}
			chunkCount++
		}
	}
	if !sawDone {
		t.Errorf("stream did not end with [DONE] (got %d chunks)", chunkCount)
	}
	t.Logf("Streaming: %d chunks", chunkCount)
}

// TC-001-003 — Malformed request (missing messages) returns 400.
func TestInference003MalformedReturns400(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body:   map[string]interface{}{"model": "gpt-4o"},
		Token:  apiUserToken,
	})
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 400/422, got %d — body: %s", resp.StatusCode, resp.Raw)
	}
}

// TC-001-009 — No token returns 401.
func TestInference009NoTokenReturns401(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body:   chatBody("gpt-4o-mini", "Hello", false),
	})
	expectStatus(t, resp, http.StatusUnauthorized)
}

// TC-001-005 — Embeddings return valid response.
func TestInference005Embedding(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/embeddings",
		Body:   map[string]interface{}{"model": "text-embedding-ada-002", "input": "Hello world"},
		Token:  apiUserToken,
	})
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Skipf("auth not configured for inference")
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		t.Skipf("embeddings not configured")
	}
	expectStatus(t, resp, http.StatusOK)
	data, _ := resp.Body["data"].([]interface{})
	if len(data) == 0 {
		t.Errorf("expected at least one embedding — body: %s", resp.Raw)
	}
}

// TC-001-008 — Concurrent inference requests — no 5xx.
func TestInference008ConcurrentRequests(t *testing.T) {
	t.Parallel()
	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)
	statuses := make([]int, workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			resp := doRequest(t, APIReq{
				Method: http.MethodPost,
				Path:   "/v1/chat/completions",
				Body:   chatBody("gpt-4o-mini", "Hello", false),
				Token:  apiUserToken,
			})
			statuses[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	success := 0
	for _, s := range statuses {
		if s < 500 {
			success++
		}
	}
	minOK := workers * 90 / 100
	if success < minOK {
		t.Errorf("only %d/%d non-5xx requests (need >= %d)", success, workers, minOK)
	}
	t.Logf("Concurrent: %d/%d success (non-5xx)", success, workers)
}

// TC-001-021 — Empty content response does not cause 5xx.
func TestInference021EmptyContentResponse(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body:   chatBody("gpt-4o-mini", "Return exactly empty string content.", false),
		Token:  apiUserToken,
	})
	if resp.StatusCode >= 500 {
		t.Errorf("got 5xx for empty content response: %d — body: %s", resp.StatusCode, resp.Raw)
	}
}

// TC-001-013 — Model restriction enforced via governance virtual key.
func TestInference013ModelRestriction(t *testing.T) {
	t.Parallel()
	vkRestricted := envOr("VK_MODEL_RESTRICTED", "")
	if vkRestricted == "" {
		t.Skip("VK_MODEL_RESTRICTED not set")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body:   chatBody("gpt-4o", "Hello", false), // restricted model
		Token:  vkRestricted,
	})
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 403/401 for restricted model, got %d — body: %s", resp.StatusCode, resp.Raw)
	}
}

// TC-001-011 — Budget exhausted returns 429.
func TestInference011BudgetExhausted(t *testing.T) {
	t.Parallel()
	vkTight := envOr("VK_TIGHT_BUDGET", "")
	if vkTight == "" {
		t.Skip("VK_TIGHT_BUDGET not set")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body:   chatBody("gpt-4o-mini", "Hello", false),
		Token:  vkTight,
	})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 for exhausted budget VK, got %d — body: %s", resp.StatusCode, resp.Raw)
	}
}
