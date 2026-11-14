package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	probeTestProvider = schemas.ModelProvider("probe-openai")
	probeTestSecret   = "sk-probe-secret-value"
)

// decisionProbeUpstream is a fake OpenAI-compatible decisions server that
// records the requests it receives.
type decisionProbeUpstream struct {
	mu       sync.Mutex
	requests []map[string]any
	auth     []string
}

func (u *decisionProbeUpstream) recorded() ([]map[string]any, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.requests...), append([]string(nil), u.auth...)
}

// probeUpstreamReply is one canned upstream answer.
type probeUpstreamReply struct {
	status int
	body   string
}

// newDecisionProbeHandler serves status and body from a fake upstream behind
// a custom provider on the OpenAI base, with one key, and returns a handler
// whose Bifrost client reaches it.
func newDecisionProbeHandler(t *testing.T, status int, body string) (*ProviderHandler, *decisionProbeUpstream) {
	t.Helper()
	return newDecisionProbeHandlerWithReplies(t, probeUpstreamReply{status: status, body: body})
}

// newDecisionProbeHandlerWithReplies is newDecisionProbeHandler with the
// upstream answering each decision request with the next reply in turn, the
// last one repeating. The provider allows two retries, so a probe that made
// more than one call would reach the later replies.
func newDecisionProbeHandlerWithReplies(t *testing.T, replies ...probeUpstreamReply) (*ProviderHandler, *decisionProbeUpstream) {
	t.Helper()
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	upstream := &decisionProbeUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		upstream.mu.Lock()
		reply := replies[min(len(upstream.requests), len(replies)-1)]
		if r.URL.Path == "/v1/decisions" {
			upstream.requests = append(upstream.requests, decoded)
			upstream.auth = append(upstream.auth, r.Header.Get("Authorization"))
		}
		upstream.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(server.Close)

	store := &lib.Config{
		ClientConfig: &configstore.ClientConfig{},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			probeTestProvider: {
				Keys: []schemas.Key{{
					ID:     "probe-key",
					Name:   "probe",
					Value:  *schemas.NewSecretVar(probeTestSecret),
					Models: schemas.WhiteList{"*"},
					Weight: 1,
				}},
				NetworkConfig:            &schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 10, MaxRetries: 2, RetryBackoffInitial: time.Millisecond, RetryBackoffMax: time.Millisecond},
				ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
				CustomProviderConfig:     &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
			},
			schemas.OpenAI: {
				Keys:                     []schemas.Key{{ID: "openai-key", Value: *schemas.NewSecretVar("sk-openai"), Models: schemas.WhiteList{"*"}, Weight: 1}},
				ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
			},
		},
	}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(store),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("failed to init bifrost client: %v", err)
	}
	t.Cleanup(client.Shutdown)
	store.SetBifrostClient(client)
	return &ProviderHandler{inMemoryStore: store, client: client, modelsManager: &mockModelsManager{}}, upstream
}

// probeDecisionForTest posts body to the probe route of provider and returns
// the status and raw response body.
func probeDecisionForTest(h *ProviderHandler, provider schemas.ModelProvider, body string) (int, []byte) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers/" + string(provider) + "/decisions/probe")
	ctx.Request.SetBody([]byte(body))
	ctx.SetUserValue("provider", string(provider))
	h.probeDecision(ctx)
	return ctx.Response.StatusCode(), ctx.Response.Body()
}

// TestProbeDecision_ReportsTheAnsweringRoute pins a successful probe: one
// request reaches the upstream for exactly the requested model, with the
// configured key and no fallbacks, and the result names the model the
// upstream reported along with its usage.
func TestProbeDecision_ReportsTheAnsweringRoute(t *testing.T) {
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	h, upstream := newDecisionProbeHandler(t, http.StatusOK,
		`{"id":"dec_1","model":"decider-2026-01","answers":[{"type":"predicate","name":"probe","probability":0.9}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)

	status, body := probeDecisionForTest(h, probeTestProvider, `{"model":"decider"}`)
	if status != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", status, body)
	}
	var result DecisionProbeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, body)
	}
	if !result.OK || result.Error != nil {
		t.Fatalf("expected ok probe, got %s", body)
	}
	if result.Provider != probeTestProvider || result.Model != "decider-2026-01" {
		t.Fatalf("answering route got %s/%s, want %s/decider-2026-01", result.Provider, result.Model, probeTestProvider)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 4 {
		t.Fatalf("usage got %+v, want total_tokens 4", result.Usage)
	}

	requests, auth := upstream.recorded()
	if len(requests) != 1 {
		t.Fatalf("upstream got %d decision requests, want 1", len(requests))
	}
	if requests[0]["model"] != "decider" {
		t.Fatalf("upstream model got %v, want decider", requests[0]["model"])
	}
	if auth[0] != "Bearer "+probeTestSecret {
		t.Fatalf("upstream authorization got %q, want the configured key", auth[0])
	}
}

// TestProbeDecision_MapsUpstreamErrorsWithoutCredentials pins a failed probe:
// it is reported with 200 and ok false, carrying the upstream status, type,
// code, and message, with the key value and URL credentials removed.
func TestProbeDecision_MapsUpstreamErrorsWithoutCredentials(t *testing.T) {
	h, upstream := newDecisionProbeHandler(t, http.StatusUnauthorized,
		`{"error":{"message":"Incorrect API key provided: `+probeTestSecret+`. Manage keys at https://admin:hunter22@keys.example.com/manage?token=tok-leak","type":"invalid_request_error for `+probeTestSecret+`","code":"invalid_api_key https://ops:codepass@keys.example.com/c?sig=code-sig-leak"}}`)

	status, body := probeDecisionForTest(h, probeTestProvider, `{"model":"decider","key_id":"probe-key"}`)
	if status != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", status, body)
	}
	var result DecisionProbeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, body)
	}
	if result.OK || result.Error == nil {
		t.Fatalf("expected failed probe, got %s", body)
	}
	if result.Error.StatusCode != fasthttp.StatusUnauthorized {
		t.Fatalf("status_code got %d, want 401", result.Error.StatusCode)
	}
	if result.Error.Type == nil || *result.Error.Type != "invalid_request_error for [redacted]" {
		t.Fatalf("type got %v, want invalid_request_error for [redacted]", result.Error.Type)
	}
	if result.Error.Code == nil || !strings.HasPrefix(*result.Error.Code, "invalid_api_key https://redacted:redacted@keys.example.com/c?sig=redacted") {
		t.Fatalf("code got %v, want invalid_api_key with the URL credentials masked", result.Error.Code)
	}
	if result.Provider != probeTestProvider || result.Model != "decider" {
		t.Fatalf("attempted route got %s/%s, want %s/decider", result.Provider, result.Model, probeTestProvider)
	}
	for _, leaked := range []string{probeTestSecret, "hunter22", "tok-leak", "codepass", "code-sig-leak"} {
		if strings.Contains(string(body), leaked) {
			t.Fatalf("response leaks %q: %s", leaked, body)
		}
	}
	if !strings.Contains(result.Error.Message, "Incorrect API key provided: [redacted]") || !strings.Contains(result.Error.Message, "keys.example.com/manage") {
		t.Fatalf("message lost its diagnostic text: %q", result.Error.Message)
	}
	if requests, _ := upstream.recorded(); len(requests) != 1 {
		t.Fatalf("upstream got %d decision requests, want 1 (no fallbacks or retries on a 401)", len(requests))
	}
}

// TestProbeDecision_RedactsURLQueriesContainingApostrophes pins that an
// apostrophe, which RFC 3986 allows in a URL, does not end the URL early: the
// query values after it are masked too, in the type, code, and message.
func TestProbeDecision_RedactsURLQueriesContainingApostrophes(t *testing.T) {
	h, _ := newDecisionProbeHandler(t, http.StatusUnauthorized,
		`{"error":{"message":"See https://keys.example.com/m?note=it's&sig=msg-apos-leak for details","type":"invalid_request_error https://keys.example.com/t?x=o'k&sig=type-apos-leak","code":"invalid_api_key https://keys.example.com/c?x=o'k&sig=code-apos-leak"}}`)

	status, body := probeDecisionForTest(h, probeTestProvider, `{"model":"decider","key_id":"probe-key"}`)
	if status != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", status, body)
	}
	var result DecisionProbeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, body)
	}
	if result.OK || result.Error == nil || result.Error.Type == nil || result.Error.Code == nil {
		t.Fatalf("expected failed probe with type and code, got %s", body)
	}
	for field, text := range map[string]string{"message": result.Error.Message, "type": *result.Error.Type, "code": *result.Error.Code} {
		for _, leaked := range []string{"msg-apos-leak", "type-apos-leak", "code-apos-leak"} {
			if strings.Contains(text, leaked) {
				t.Fatalf("%s leaks %q: %q", field, leaked, text)
			}
		}
		if !strings.Contains(text, "keys.example.com/") {
			t.Fatalf("%s lost its URL host and path: %q", field, text)
		}
	}
}

// TestProbeDecision_RejectsRoutesItCannotProbe pins the checks made before
// any request is sent: an unknown provider, a missing model, a model the
// provider does not serve on its decisions endpoint, and an unknown key.
func TestProbeDecision_RejectsRoutesItCannotProbe(t *testing.T) {
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	h, upstream := newDecisionProbeHandler(t, http.StatusOK, `{}`)

	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		body       string
		wantStatus int
		wantText   string
	}{
		{name: "unknown provider", provider: "missing", body: `{"model":"decider"}`, wantStatus: fasthttp.StatusNotFound, wantText: "Provider not found"},
		{name: "missing model", provider: probeTestProvider, body: `{}`, wantStatus: fasthttp.StatusBadRequest, wantText: "model is required"},
		{name: "chat model", provider: schemas.OpenAI, body: `{"model":"gpt-4o"}`, wantStatus: fasthttp.StatusBadRequest, wantText: "not served on the decisions endpoint"},
		{name: "unknown key", provider: probeTestProvider, body: `{"model":"decider","key_id":"nope"}`, wantStatus: fasthttp.StatusBadRequest, wantText: "is not configured for provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := probeDecisionForTest(h, tc.provider, tc.body)
			if status != tc.wantStatus || !strings.Contains(string(body), tc.wantText) {
				t.Fatalf("got %d %s, want %d containing %q", status, body, tc.wantStatus, tc.wantText)
			}
		})
	}
	if requests, _ := upstream.recorded(); len(requests) != 0 {
		t.Fatalf("upstream got %d decision requests, want none", len(requests))
	}
}

// TestProbeDecision_MakesOneUpstreamCall pins that a probe is never retried:
// the provider allows retries and its first answer is a retryable 503, yet the
// probe makes exactly one call and reports that 503 rather than the success a
// retry would have reached.
func TestProbeDecision_MakesOneUpstreamCall(t *testing.T) {
	h, upstream := newDecisionProbeHandlerWithReplies(t,
		probeUpstreamReply{status: http.StatusServiceUnavailable, body: `{"error":{"message":"overloaded","type":"server_error"}}`},
		probeUpstreamReply{status: http.StatusOK, body: `{"model":"decider","answers":[{"type":"predicate","name":"probe","probability":0.9}]}`},
	)

	status, body := probeDecisionForTest(h, probeTestProvider, `{"model":"decider"}`)
	if status != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", status, body)
	}
	var result DecisionProbeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, body)
	}
	if requests, _ := upstream.recorded(); len(requests) != 1 {
		t.Fatalf("upstream got %d decision requests, want exactly 1", len(requests))
	}
	if result.OK || result.Error == nil || result.Error.StatusCode != fasthttp.StatusServiceUnavailable {
		t.Fatalf("expected the first attempt's 503, got %s", body)
	}
}
