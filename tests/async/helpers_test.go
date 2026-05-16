package async

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
	"time"
)

const (
	defaultBaseURL      = "http://localhost:8080"
	defaultPollTimeout  = 30 * time.Second
	defaultPollInterval = 500 * time.Millisecond
)

// httpClient is used for all test HTTP calls; the 15s timeout prevents CI hangs.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// cfg holds e2e configuration sourced from environment variables at startup.
var cfg = struct {
	BaseURL      string
	VK           string // BIFROST_VK — primary virtual key
	AltVK        string // BIFROST_ALT_VK — a second, different virtual key for auth tests
	PollTimeout  time.Duration
	PollInterval time.Duration
}{
	BaseURL:      envOr("BIFROST_BASE_URL", defaultBaseURL),
	VK:           os.Getenv("BIFROST_VK"),
	AltVK:        os.Getenv("BIFROST_ALT_VK"),
	PollTimeout:  parseDuration(os.Getenv("BIFROST_POLL_TIMEOUT"), defaultPollTimeout),
	PollInterval: parseDuration(os.Getenv("BIFROST_POLL_INTERVAL"), defaultPollInterval),
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// testMode describes one execution round for the core test suites.
type testMode struct {
	name    string
	headers map[string]string // headers to attach to every submit and poll call
}

// testModes returns the rounds every core test must execute.
// When BIFROST_VK is unset, only the global (no-VK) round runs.
func testModes() []testMode {
	modes := []testMode{
		{name: "global", headers: nil},
	}
	if cfg.VK != "" {
		modes = append(modes, testMode{name: "with_vk", headers: vkHeaders(cfg.VK)})
	}
	return modes
}

// --- Response types ---

// AsyncJobResponse mirrors the gateway's JSON envelope for async job responses.
type AsyncJobResponse struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	ExpiresAt   *time.Time      `json:"expires_at"`
	StatusCode  int             `json:"status_code"`
	Result      json.RawMessage `json:"result"`
	Error       json.RawMessage `json:"error"`
}

func (j AsyncJobResponse) isTerminal() bool {
	return j.Status == "completed" || j.Status == "failed"
}

// --- HTTP helpers ---

// submitJSON POSTs a JSON body and returns the HTTP status code, decoded response, and raw body.
func submitJSON(t *testing.T, path string, body any, headers map[string]string) (int, AsyncJobResponse, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("submitJSON: marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("submitJSON: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRequest(t, req)
}

// submitRaw POSTs arbitrary bytes — used for malformed-JSON validation tests.
func submitRaw(t *testing.T, path string, raw []byte, contentType string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("submitRaw: new request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	code, _, body := doRequest(t, req)
	return code, body
}

// submitMultipart POSTs a multipart/form-data body.
func submitMultipart(t *testing.T, path string, mp *multipartCase, headers map[string]string) (int, AsyncJobResponse, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range mp.fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("submitMultipart: write field %q: %v", k, err)
		}
	}
	for fieldName, ff := range mp.files {
		fw, err := w.CreateFormFile(fieldName, ff.filename)
		if err != nil {
			t.Fatalf("submitMultipart: create form file %q: %v", fieldName, err)
		}
		if _, err := fw.Write(ff.data); err != nil {
			t.Fatalf("submitMultipart: write file %q: %v", fieldName, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("submitMultipart: close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+path, &buf)
	if err != nil {
		t.Fatalf("submitMultipart: new request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRequest(t, req)
}

// submitCase dispatches to submitJSON or submitMultipart based on the fixture type.
func submitCase(t *testing.T, ec endpointCase, headers map[string]string) (int, AsyncJobResponse, []byte) {
	t.Helper()
	if ec.multipart != nil {
		return submitMultipart(t, ec.submitPath, ec.multipart, headers)
	}
	return submitJSON(t, ec.submitPath, ec.body, headers)
}

// pollOnce performs a single GET and returns HTTP status, decoded response, and raw body.
func pollOnce(t *testing.T, pollPath string, headers map[string]string) (int, AsyncJobResponse, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, cfg.BaseURL+pollPath, nil)
	if err != nil {
		t.Fatalf("pollOnce: new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRequest(t, req)
}

// pollUntilTerminal polls every cfg.PollInterval until the job is completed/failed or cfg.PollTimeout elapses.
func pollUntilTerminal(t *testing.T, pollPath string, headers map[string]string) (int, AsyncJobResponse) {
	t.Helper()
	deadline := time.Now().Add(cfg.PollTimeout)
	for time.Now().Before(deadline) {
		code, job, _ := pollOnce(t, pollPath, headers)
		if job.isTerminal() {
			return code, job
		}
		if code != http.StatusAccepted {
			t.Fatalf("unexpected HTTP %d while polling %s (status=%q)", code, pollPath, job.Status)
		}
		time.Sleep(cfg.PollInterval)
	}
	t.Fatalf("timed out after %s waiting for terminal status on %s", cfg.PollTimeout, pollPath)
	return 0, AsyncJobResponse{}
}

// --- Path / header helpers ---

// jobPollPath builds the GET path for a job: /pollBase/{jobID}.
func jobPollPath(base, jobID string) string {
	return base + "/" + jobID
}

// vkHeaders returns a header map carrying the given virtual key.
// Returns nil when vk is empty so callers can safely pass it to submitCase.
func vkHeaders(vk string) map[string]string {
	if vk == "" {
		return nil
	}
	return map[string]string{"x-bf-vk": vk}
}

// withTTLHeader copies headers and appends x-bf-async-job-result-ttl.
func withTTLHeader(headers map[string]string, ttlSeconds int) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out["x-bf-async-job-result-ttl"] = fmt.Sprintf("%d", ttlSeconds)
	return out
}

// withRawHeader copies headers and appends a single key/value pair.
func withRawHeader(headers map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out[key] = value
	return out
}

// doRequest executes an HTTP request and returns (statusCode, decoded AsyncJobResponse, rawBody).
func doRequest(t *testing.T, req *http.Request) (int, AsyncJobResponse, []byte) {
	t.Helper()
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP %s %s failed: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var job AsyncJobResponse
	_ = json.Unmarshal(body, &job)
	return resp.StatusCode, job, body
}

// chatCompletionCase returns the chat_completions fixture — used as a representative
// endpoint in auth and TTL tests where endpoint variety is not the focus.
func chatCompletionCase() endpointCase {
	for _, ec := range endpointCases() {
		if ec.name == "chat_completions" {
			return ec
		}
	}
	panic("chatCompletionCase: fixture not found")
}

// TestMain checks that the Bifrost gateway is reachable before running any tests.
// Set BIFROST_BASE_URL to override the default http://localhost:8080.
func TestMain(m *testing.M) {
	resp, err := httpClient.Get(cfg.BaseURL + "/health")
	if err != nil || resp.StatusCode >= 500 {
		fmt.Printf("SKIP: Bifrost gateway not reachable at %s (err=%v)\n", cfg.BaseURL, err)
		os.Exit(0)
	}
	resp.Body.Close()
	os.Exit(m.Run())
}
