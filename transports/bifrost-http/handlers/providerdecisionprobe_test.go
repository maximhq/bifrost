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
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
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

// probeUpstreamReply is one canned upstream answer. With hold set, the
// upstream answers only once hold is closed or the caller gives up; arrived,
// when set, is signalled (without blocking) as each request comes in.
type probeUpstreamReply struct {
	status  int
	body    string
	hold    <-chan struct{}
	arrived chan<- struct{}
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
	return newDecisionProbeHandlerWithConfig(t, nil, replies...)
}

// newDecisionProbeHandlerWithConfig is newDecisionProbeHandlerWithReplies with
// configure applied to the provider configs and keys, every provider pointed at
// the fake upstream, before the Bifrost client starts.
func newDecisionProbeHandlerWithConfig(t *testing.T, configure func(map[schemas.ModelProvider]configstore.ProviderConfig, map[schemas.ModelProvider][]schemas.Key), replies ...probeUpstreamReply) (*ProviderHandler, *decisionProbeUpstream) {
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
		if reply.arrived != nil {
			select {
			case reply.arrived <- struct{}{}:
			default:
			}
		}
		if reply.hold != nil {
			select {
			case <-reply.hold:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(server.Close)

	providers := map[schemas.ModelProvider]configstore.ProviderConfig{
		probeTestProvider: {
			NetworkConfig:            &schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 10, MaxRetries: 2, RetryBackoffInitial: time.Millisecond, RetryBackoffMax: time.Millisecond},
			ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
			CustomProviderConfig:     &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
		},
		schemas.OpenAI: {
			NetworkConfig:            &schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 10},
			ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
		},
	}
	keys := map[schemas.ModelProvider][]schemas.Key{
		probeTestProvider: {{
			ID:     "probe-key",
			Name:   "probe",
			Value:  *schemas.NewSecretVar(probeTestSecret),
			Models: schemas.WhiteList{"*"},
			Weight: 1,
		}},
		schemas.OpenAI: {{ID: "openai-key", Value: *schemas.NewSecretVar("sk-openai"), Models: schemas.WhiteList{"*"}, Weight: 1}},
	}
	if configure != nil {
		configure(providers, keys)
	}
	store := libtest.WithProviders(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, providers, keys)
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(store),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("failed to init bifrost client: %v", err)
	}
	t.Cleanup(client.Shutdown)
	store.SetBifrostClient(client)
	// No configStore on the handler: the probe reads the live store the client serves.
	return &ProviderHandler{config: store, client: client, modelsManager: &mockModelsManager{}}, upstream
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

// TestProbeDecision_ChecksThePinnedKeysAlias pins that a probe pinned to a key
// checks decision support against that key's alias. Two keys map judge to
// different models and the catalog keeps the other key's chat model, yet the
// probe through the decision key is sent, as gpt-6-luna, on that key.
func TestProbeDecision_ChecksThePinnedKeysAlias(t *testing.T) {
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	keys := []schemas.Key{
		{ID: "decision-key", Value: *schemas.NewSecretVar("sk-decision-key"), Models: schemas.WhiteList{"*"}, Weight: 1, Aliases: schemas.KeyAliases{"judge": {ModelID: "gpt-6-luna"}}},
		{ID: "chat-key", Value: *schemas.NewSecretVar("sk-chat-key"), Models: schemas.WhiteList{"*"}, Weight: 1, Aliases: schemas.KeyAliases{"judge": {ModelID: "gpt-4o"}}},
	}
	h, upstream := newDecisionProbeHandlerWithConfig(t, func(_ map[schemas.ModelProvider]configstore.ProviderConfig, providerKeys map[schemas.ModelProvider][]schemas.Key) {
		providerKeys[schemas.OpenAI] = keys
	}, probeUpstreamReply{status: http.StatusOK, body: `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"probe","probability":0.9}]}`})
	catalog, err := modelcatalog.NewTestCatalog(nil)
	if err != nil {
		t.Fatalf("failed to build test catalog: %v", err)
	}
	catalog.SetKeyConfigForProvider(schemas.OpenAI, keys)
	h.config.ModelCatalog = catalog
	if target := h.aliasTarget(schemas.OpenAI, "judge"); target != "gpt-4o" {
		t.Fatalf("catalog alias target got %q, want the chat key's gpt-4o", target)
	}

	status, body := probeDecisionForTest(h, schemas.OpenAI, `{"model":"judge","key_id":"decision-key"}`)
	if status != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", status, body)
	}
	var result DecisionProbeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, body)
	}
	if !result.OK {
		t.Fatalf("expected ok probe, got %s", body)
	}
	requests, auth := upstream.recorded()
	if len(requests) != 1 || requests[0]["model"] != "gpt-6-luna" || auth[0] != "Bearer sk-decision-key" {
		t.Fatalf("upstream got %v with %v, want one gpt-6-luna request on the decision key", requests, auth)
	}

	status, body = probeDecisionForTest(h, schemas.OpenAI, `{"model":"judge","key_id":"chat-key"}`)
	if status != fasthttp.StatusBadRequest || !strings.Contains(string(body), "not served on the decisions endpoint") {
		t.Fatalf("probe through the chat key got %d %s, want 400 not served", status, body)
	}
}

// TestProbeDecision_DeadlineCoversQueueWait pins that the provider's request
// timeout bounds the whole probe, including the wait for queue space: with the
// one worker and the one queue slot taken by held requests, the probe gives up
// waiting for space when its deadline passes instead of queueing behind them.
// The held requests run under the 10 second timeout the provider started with;
// the probe reads the 1 second timeout configured after them.
func TestProbeDecision_DeadlineCoversQueueWait(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	h, _ := newDecisionProbeHandlerWithConfig(t, func(providers map[schemas.ModelProvider]configstore.ProviderConfig, _ map[schemas.ModelProvider][]schemas.Key) {
		probe := providers[probeTestProvider]
		probe.ConcurrencyAndBufferSize = &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1}
		providers[probeTestProvider] = probe
	}, probeUpstreamReply{status: http.StatusOK, body: `{"model":"decider","answers":[{"type":"predicate","name":"probe","probability":0.9}]}`, hold: release, arrived: arrived})
	t.Cleanup(func() { close(release) })

	// Each held request is its own value: the request path writes the model it
	// resolves back into the request.
	hold := func() *schemas.BifrostError {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, err := h.client.DecisionRequest(ctx, &schemas.BifrostDecisionRequest{
			Provider:  probeTestProvider,
			Model:     "decider",
			Input:     schemas.DecisionInput{Text: schemas.Ptr("held")},
			Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("held")}},
		})
		return err
	}
	// The worker is busy once the first held request reaches the upstream.
	go hold()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the first held request never reached the upstream")
	}
	// The one queue slot is taken once a request is dropped for a full queue:
	// of two senders with dropping on, one takes the slot and the other is
	// dropped, and nothing dequeues while the worker is held.
	h.client.UpdateDropExcessRequests(true)
	queued := make(chan *schemas.BifrostError, 2)
	for range 2 {
		go func() { queued <- hold() }()
	}
	select {
	case err := <-queued:
		if err == nil || err.Error == nil || err.Error.Type == nil || *err.Error.Type != schemas.RequestDropped {
			t.Fatalf("expected one sender dropped for a full queue, got %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sender was dropped; the queue slot never filled")
	}
	h.client.UpdateDropExcessRequests(false)

	// Written to the store and refreshed into the live store only: the running
	// client keeps the queue and timeout it started with.
	probe, ok := h.config.LiveStore.Collections.Providers.Get(string(probeTestProvider))
	if !ok {
		t.Fatal("probe provider missing from the live store")
	}
	updated := probe.Config
	network := *updated.NetworkConfig
	network.DefaultRequestTimeoutInSeconds = 1
	updated.NetworkConfig = &network
	libtest.SetProvider(t, h.config, probeTestProvider, updated, nil)

	type probeReply struct {
		status int
		body   []byte
	}
	done := make(chan probeReply, 1)
	go func() {
		status, body := probeDecisionForTest(h, probeTestProvider, `{"model":"decider"}`)
		done <- probeReply{status, body}
	}()
	var reply probeReply
	select {
	case reply = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("probe still waiting for queue space after 5s, want it bounded by its 1 second timeout")
	}
	status, body := reply.status, reply.body
	if status != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", status, body)
	}
	var result DecisionProbeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, body)
	}
	if result.OK || result.Error == nil || result.Error.StatusCode != fasthttp.StatusGatewayTimeout || !strings.Contains(result.Error.Message, "waiting for queue space") {
		t.Fatalf("expected the probe to time out waiting for queue space, got %s", body)
	}
}

// TestProbedRoute_ReportsKeyAliasTarget pins the model named when the provider
// reports none: a key alias's target, which is what went upstream, rather than
// the alias name the probe was asked for.
func TestProbedRoute_ReportsKeyAliasTarget(t *testing.T) {
	routing := schemas.RoutingInfo{Provider: probeTestProvider, Model: "judge", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "decider-2026-01"}}
	if _, model := probedRoute(probeTestProvider, "judge", routing, ""); model != "decider-2026-01" {
		t.Fatalf("model got %q, want the alias target decider-2026-01", model)
	}
	if _, model := probedRoute(probeTestProvider, "judge", routing, "decider-2026-02"); model != "decider-2026-02" {
		t.Fatalf("model got %q, want the provider-reported decider-2026-02", model)
	}
	if _, model := probedRoute(probeTestProvider, "judge", schemas.RoutingInfo{Model: "judge"}, ""); model != "judge" {
		t.Fatalf("model got %q, want the routed model judge", model)
	}
}

// TestDecisionProbeErrorFrom_RedactsShortCredentials pins that a key or header
// value shorter than decisionProbeMinSecretLength is removed from every
// returned field, together with any longer token it sits inside, while words
// that do not contain it are left alone.
func TestDecisionProbeErrorFrom_RedactsShortCredentials(t *testing.T) {
	config := &configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{"x-team": *schemas.NewSecretVar("t-42")}},
	}
	keys := []schemas.Key{
		{ID: "short", Value: *schemas.NewSecretVar("tok1234")},
		{ID: "empty", Value: *schemas.NewSecretVar("")},
	}
	got := decisionProbeErrorFrom(&schemas.BifrostError{
		StatusCode: schemas.Ptr(fasthttp.StatusUnauthorized),
		Error: &schemas.ErrorField{
			Message: "key tok1234 for team t-42 rejected; stok12345 and t-421 are other ids, t-4 is not",
			Type:    schemas.Ptr("auth_error tok1234"),
			Code:    schemas.Ptr("bad_key:tok1234"),
		},
	}, decisionProbeSecrets(config, keys))

	if want := "key [redacted] for team [redacted] rejected; [redacted] and [redacted] are other ids, t-4 is not"; got.Message != want {
		t.Fatalf("message got %q, want %q", got.Message, want)
	}
	if got.Type == nil || *got.Type != "auth_error [redacted]" {
		t.Fatalf("type got %v, want auth_error [redacted]", got.Type)
	}
	if got.Code == nil || *got.Code != "bad_key:[redacted]" {
		t.Fatalf("code got %v, want bad_key:[redacted]", got.Code)
	}
}

// TestDecisionProbeErrorFrom_RedactsOverlappingCredentialsInAnyOrder pins that
// a credential overlapping a longer one, as its prefix or as a short token at
// its end, does not cut the longer one apart and leave its rest in the text,
// whichever order the provider config lists them in.
func TestDecisionProbeErrorFrom_RedactsOverlappingCredentialsInAnyOrder(t *testing.T) {
	text := "keys abcdefgh-secret-value and abcdefgh.xyz rejected"
	want := "keys [redacted] and [redacted] rejected"
	for _, secrets := range [][]string{
		{"abcdefgh", "abcdefgh-secret-value", "xyz", "abcdefgh.xyz"},
		{"abcdefgh.xyz", "xyz", "abcdefgh-secret-value", "abcdefgh"},
	} {
		got := decisionProbeErrorFrom(&schemas.BifrostError{
			StatusCode: schemas.Ptr(fasthttp.StatusUnauthorized),
			Error:      &schemas.ErrorField{Message: text, Type: schemas.Ptr(text), Code: schemas.Ptr(text)},
		}, secrets)
		if got.Message != want || got.Type == nil || *got.Type != want || got.Code == nil || *got.Code != want {
			t.Fatalf("secrets %q: message %q, type %v, code %v; want %q in each", secrets, got.Message, got.Type, got.Code, want)
		}
	}
}

// TestDecisionProbeErrorFrom_RedactsUnparseableURLs pins that a URL url.Parse
// rejects keeps only its scheme in the message, type, and code: a password can
// hold '@' and '/', and so can a path, so no part after the scheme survives.
func TestDecisionProbeErrorFrom_RedactsUnparseableURLs(t *testing.T) {
	const want = "see https://redacted now"
	cases := []struct {
		name    string
		text    string
		secrets []string
	}{
		{name: "@ in the password", text: "see https://user:pa@ss%zz@host.example/path?sig=query-secret&x=1#top now", secrets: []string{"user", "pa@ss", "ss%zz", "query-secret"}},
		{name: "@ in the path", text: "see https://user:pa@ss%zz@host.example/a@b?sig=query-secret now", secrets: []string{"user", "pa@ss", "ss%zz", "query-secret"}},
		{name: "/ in the password", text: "see https://user:p%zz/ss@host.example/a?sig=q now", secrets: []string{"user", "p%zz", "zz/ss", "sig=q"}},
		{name: "@ and / in the password", text: "see https://user:pa@ss/p%zz@host.example/a?sig=q now", secrets: []string{"user", "pa@ss", "ss/p%zz", "p%zz", "sig=q"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decisionProbeErrorFrom(&schemas.BifrostError{
				StatusCode: schemas.Ptr(fasthttp.StatusUnauthorized),
				Error:      &schemas.ErrorField{Message: tc.text, Type: schemas.Ptr(tc.text), Code: schemas.Ptr(tc.text)},
			}, nil)
			if got.Type == nil || got.Code == nil {
				t.Fatalf("type %v, code %v; want both set", got.Type, got.Code)
			}
			for field, text := range map[string]string{"message": got.Message, "type": *got.Type, "code": *got.Code} {
				if text != want {
					t.Fatalf("%s got %q, want %q", field, text, want)
				}
				for _, secret := range tc.secrets {
					if strings.Contains(text, secret) {
						t.Fatalf("%s leaks %q: %q", field, secret, text)
					}
				}
			}
		})
	}
}

// TestDecisionProbeErrorFrom_MasksURLsBeforeShortCredentials pins that a
// short credential found inside a URL's scheme (a header value "http" inside
// "https") does not stop that URL from being masked: its userinfo and query
// values are removed, and the credential is removed too.
func TestDecisionProbeErrorFrom_MasksURLsBeforeShortCredentials(t *testing.T) {
	text := "upstream fetch of https://alice:url-password@example.invalid/path?token=query-secret failed"
	got := decisionProbeErrorFrom(&schemas.BifrostError{
		StatusCode: schemas.Ptr(fasthttp.StatusUnauthorized),
		Error:      &schemas.ErrorField{Message: text, Type: schemas.Ptr(text), Code: schemas.Ptr(text)},
	}, []string{"http"})
	if got.Type == nil || got.Code == nil {
		t.Fatalf("type %v, code %v; want both set", got.Type, got.Code)
	}
	for field, value := range map[string]string{"message": got.Message, "type": *got.Type, "code": *got.Code} {
		for _, leaked := range []string{"alice", "url-password", "query-secret", "http"} {
			if strings.Contains(value, leaked) {
				t.Fatalf("%s leaks %q: %q", field, leaked, value)
			}
		}
		if !strings.Contains(value, "example.invalid/path") {
			t.Fatalf("%s lost the host and path: %q", field, value)
		}
	}
}

// TestProbeDecision_RejectsUnusableAnswers pins that a reply nothing could act
// on is a failed probe: a refusal, a predicate answer with no probability or
// one outside 0..1, and a distribution that does not sum to about 1 each come
// back as ok false with a 502 naming the problem, not as a working route.
func TestProbeDecision_RejectsUnusableAnswers(t *testing.T) {
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	cases := []struct {
		name     string
		answer   string
		wantText string
	}{
		{name: "refusal", answer: `{"type":"refusal","name":"probe"}`, wantText: "refused"},
		{name: "probability above 1", answer: `{"type":"predicate","name":"probe","probability":1.7}`, wantText: "outside 0..1"},
		{name: "negative probability", answer: `{"type":"predicate","name":"probe","probability":-0.2}`, wantText: "outside 0..1"},
		{name: "no probability", answer: `{"type":"predicate","name":"probe"}`, wantText: "no probability"},
		{name: "distribution off 1", answer: `{"type":"predicate","name":"probe","probability":0.9,"probabilities":[{"value":true,"probability":0.9},{"value":false,"probability":0.4}]}`, wantText: "sum to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newDecisionProbeHandler(t, http.StatusOK, `{"model":"decider-2026-01","answers":[`+tc.answer+`]}`)
			status, body := probeDecisionForTest(h, probeTestProvider, `{"model":"decider"}`)
			if status != fasthttp.StatusOK {
				t.Fatalf("status got %d, want 200; body=%s", status, body)
			}
			var result DecisionProbeResponse
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatalf("failed to decode response: %v; body=%s", err, body)
			}
			if result.OK || result.Error == nil || result.Error.StatusCode != fasthttp.StatusBadGateway || !strings.Contains(result.Error.Message, tc.wantText) {
				t.Fatalf("got %s, want ok false with a 502 containing %q", body, tc.wantText)
			}
			if result.Model != "decider-2026-01" {
				t.Fatalf("model got %q, want the provider-reported decider-2026-01", result.Model)
			}
		})
	}
}

// TestDecisionProbeSecrets_IncludesEveryKeyCredential pins that the scrub list
// holds the secrets a key authenticates with besides its value: client
// secrets, OAuth private keys and extra token parameters, AWS access and
// secret keys and session tokens, Vertex service account secrets, and an
// inline identity token, and that a provider error echoing one is redacted.
func TestDecisionProbeSecrets_IncludesEveryKeyCredential(t *testing.T) {
	secret := func(v string) *schemas.SecretVar { return schemas.NewSecretVar(v) }
	keys := []schemas.Key{
		{ID: "azure", AzureKeyConfig: &schemas.AzureKeyConfig{ClientSecret: secret("azure-client-secret")}},
		{ID: "vertex", VertexKeyConfig: &schemas.VertexKeyConfig{AuthCredentials: *secret(`{"type":"service_account","private_key_id":"vertex-private-key-id","private_key":"vertex-private-key-pem"}`)}},
		{ID: "bedrock", BedrockKeyConfig: &schemas.BedrockKeyConfig{AccessKey: *secret("AKIABEDROCKACCESS"), SecretKey: *secret("bedrock-secret-key"), SessionToken: secret("bedrock-session-token")}},
		{ID: "mantle", BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{AccessKey: *secret("AKIAMANTLEACCESS"), SecretKey: *secret("mantle-secret-key"), SessionToken: secret("mantle-session-token")}},
		{ID: "sagemaker", SageMakerKeyConfig: &schemas.SageMakerKeyConfig{AccessKey: *secret("AKIASAGEMAKERACCESS"), SecretKey: *secret("sagemaker-secret-key"), SessionToken: secret("sagemaker-session-token")}},
		{ID: "databricks", DatabricksKeyConfig: &schemas.DatabricksKeyConfig{ClientSecret: secret("databricks-client-secret")}},
		{ID: "oauth", OAuthKeyConfig: &schemas.OAuthKeyConfig{ClientSecret: secret("oauth-client-secret"), PrivateKey: secret("oauth-private-key-pem"), ExtraParams: map[string]string{"resource": "oauth-extra-param-secret"}}},
		{ID: "anthropic", AnthropicKeyConfig: &schemas.AnthropicKeyConfig{WorkloadIdentity: &schemas.AnthropicWorkloadIdentityConfig{IdentitySource: &schemas.IdentitySource{Type: schemas.IdentitySourceInline, Token: secret("anthropic-identity-token")}}}},
	}
	want := []string{
		"azure-client-secret", "vertex-private-key-id", "vertex-private-key-pem",
		"AKIABEDROCKACCESS", "bedrock-secret-key", "bedrock-session-token",
		"AKIAMANTLEACCESS", "mantle-secret-key", "mantle-session-token",
		"AKIASAGEMAKERACCESS", "sagemaker-secret-key", "sagemaker-session-token",
		"databricks-client-secret", "oauth-client-secret", "oauth-private-key-pem", "oauth-extra-param-secret",
		"anthropic-identity-token",
	}
	text := "rejected: " + strings.Join(want, " ")
	got := decisionProbeErrorFrom(&schemas.BifrostError{
		StatusCode: schemas.Ptr(fasthttp.StatusUnauthorized),
		Error:      &schemas.ErrorField{Message: text, Type: schemas.Ptr(text), Code: schemas.Ptr(text)},
	}, decisionProbeSecrets(&configstore.ProviderConfig{}, keys))
	if got.Type == nil || got.Code == nil {
		t.Fatalf("type %v, code %v; want both set", got.Type, got.Code)
	}
	for field, value := range map[string]string{"message": got.Message, "type": *got.Type, "code": *got.Code} {
		for _, secret := range want {
			if strings.Contains(value, secret) {
				t.Fatalf("%s leaks %q: %q", field, secret, value)
			}
		}
	}
}
