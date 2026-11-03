package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCtx(t testing.TB) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

// fakePipeline records what the hooks saw and can refuse a call in the pre-hook.
type fakePipeline struct {
	mu        sync.Mutex
	pre       []*schemas.BifrostRelayRequest
	post      []*schemas.BifrostRelayResponse
	postErr   []*schemas.BifrostError
	deny      *schemas.BifrostError
	denyAfter time.Duration                 // how long the refusing pre-hook takes
	preDelay  time.Duration                 // how long an admitting pre-hook takes
	recover   *schemas.BifrostRelayResponse // replaces an error in the post-hook
	drop      bool                          // the post-hook clears the response
}

func (f *fakePipeline) RunRelayPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostRelayRequest, entered func(int)) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pre = append(f.pre, req)
	entered(1)
	if f.deny != nil {
		time.Sleep(f.denyAfter)
		return req, &schemas.RelayPluginShortCircuit{Error: f.deny}, 1
	}
	time.Sleep(f.preDelay)
	return req, nil, 1
}

func (f *fakePipeline) RunRelayPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bErr *schemas.BifrostError, _ int) (*schemas.BifrostRelayResponse, *schemas.BifrostError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.post = append(f.post, resp)
	f.postErr = append(f.postErr, bErr)
	if bErr != nil && f.recover != nil {
		return f.recover, nil
	}
	if f.drop {
		return nil, nil
	}
	return resp, bErr
}

func newTestManager(t testing.TB, cfg *schemas.RelayConfig) (*Manager, *fakePipeline) {
	t.Helper()
	m := NewManager(nil, nil)
	pipeline := &fakePipeline{}
	m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})
	require.NoError(t, m.Upsert(cfg))
	return m, pipeline
}

func bearerConfig(name, baseURL string) *schemas.RelayConfig {
	return &schemas.RelayConfig{
		Name:       name,
		BaseURL:    baseURL,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
	}
}

// recordingProxy is a forward proxy for absolute-form requests that records each target host.
func recordingProxy(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var hosts []string
	transport := &http.Transport{Proxy: nil}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			http.Error(w, "absolute-form request required", http.StatusBadRequest)
			return
		}
		mu.Lock()
		hosts = append(hosts, r.URL.Host)
		mu.Unlock()
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		resp, err := transport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hosts...)
	}
}

// A relay's proxy_config follows the provider rule: no proxy_config, type "none" and an empty type
// all mean "not configured", so the relay inherits the global proxy when there is one and connects
// directly when there is none. Only a relay's own http/socks5/environment proxy overrides the
// global proxy, and environment variables apply only to type "environment".
func TestRelayProxyConfigFollowsProviderRule(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	global, globalHits := recordingProxy(t)
	own, ownHits := recordingProxy(t)
	withGlobal := func() (*schemas.ProxyConfig, error) {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(global.URL)}, nil
	}

	cases := []struct {
		name      string
		proxy     *schemas.ProxyConfig
		global    func() (*schemas.ProxyConfig, error)
		viaGlobal bool
		viaOwn    bool
	}{
		{name: "no proxy_config inherits the global proxy", global: withGlobal, viaGlobal: true},
		{name: "type none inherits the global proxy", proxy: &schemas.ProxyConfig{Type: schemas.NoProxy}, global: withGlobal, viaGlobal: true},
		{name: "an empty type inherits the global proxy", proxy: &schemas.ProxyConfig{}, global: withGlobal, viaGlobal: true},
		{name: "an own http proxy wins over the global proxy", proxy: &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(own.URL)}, global: withGlobal, viaOwn: true},
		{name: "type none with no global proxy connects directly", proxy: &schemas.ProxyConfig{Type: schemas.NoProxy}},
		{name: "no proxy_config and no global proxy connects directly"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil)
			pipeline := &fakePipeline{}
			m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})
			if tc.global != nil {
				m.SetGlobalProxy(tc.global)
			}
			name := fmt.Sprintf("proxied-%d", i)
			cfg := bearerConfig(name, upstream.URL)
			cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/**"}}
			cfg.ProxyConfig = tc.proxy
			require.NoError(t, m.Upsert(cfg))
			beforeGlobal, beforeOwn := len(globalHits()), len(ownHits())
			res, bErr := m.Serve(testCtx(t), Request{Endpoint: name, Method: "POST", Path: "/x", Headers: map[string]string{}})
			require.Nil(t, bErr)
			require.NotNil(t, res.Response)
			require.Equal(t, http.StatusOK, res.Response.StatusCode)
			gotGlobal := len(globalHits()) - beforeGlobal
			gotOwn := len(ownHits()) - beforeOwn
			assert.Equal(t, tc.viaGlobal, gotGlobal == 1, "went through the global proxy")
			assert.Equal(t, tc.viaOwn, gotOwn == 1, "went through the relay's own proxy")
			if tc.viaGlobal {
				assert.Equal(t, upstreamHost, globalHits()[len(globalHits())-1])
			}
		})
	}
}

// spanRecorder records the attributes and end status of every span, by name; the root span is
// "root", which is what the context's stamps (upstream latency, stream overhead) write to.
type spanRecorder struct {
	schemas.NoOpTracer
	mu     sync.Mutex
	attrs  map[string]map[string]any
	status map[string]schemas.SpanStatus
}

func newSpanRecorder() *spanRecorder {
	return &spanRecorder{attrs: map[string]map[string]any{}, status: map[string]schemas.SpanStatus{}}
}

func (r *spanRecorder) StartSpan(ctx context.Context, name string, _ schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	return ctx, name
}

func (r *spanRecorder) GetSpanHandleByID(_ string, spanID *string) schemas.SpanHandle {
	if spanID == nil {
		return "root"
	}
	return *spanID
}

func (r *spanRecorder) SetAttribute(h schemas.SpanHandle, key string, value any) {
	name, _ := h.(string)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attrs[name] == nil {
		r.attrs[name] = map[string]any{}
	}
	r.attrs[name][key] = value
}

func (r *spanRecorder) EndSpan(h schemas.SpanHandle, status schemas.SpanStatus, _ string) {
	name, _ := h.(string)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status[name] = status
}

func (r *spanRecorder) attr(span, key string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attrs[span][key]
}

func tracedCtx(t *testing.T, rec *spanRecorder) *schemas.BifrostContext {
	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyTracer, schemas.Tracer(rec))
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-relay")
	return ctx
}

// The last bytes of a reply are returned before EOF, never together with it, and the post-hooks
// run only once the reader has taken them; a reader that closes before reading EOF ends the stream
// as cancelled. (The gateway reads ahead of its caller, so this is about the reader, not the caller.)
func TestStreamReturnsLastBytesBeforeEOF(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "15")
		_, _ = io.WriteString(w, "data: {\"a\":1}\n\n")
	}))
	t.Cleanup(upstream.Close)
	cfg := bearerConfig("last", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/s", Stream: schemas.Ptr(true)}}

	m, pipeline := newTestManager(t, cfg)
	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "last", Method: "POST", Path: "/s", Headers: map[string]string{}})
	require.Nil(t, bErr)
	require.NotNil(t, res.Stream)
	buf := make([]byte, 1024)
	n, err := res.Stream.Read(buf)
	require.NoError(t, err, "the last bytes come without EOF")
	require.Equal(t, 15, n)
	require.Empty(t, pipeline.post, "the post-hooks wait until the reader asks for more after the last bytes")
	n, err = res.Stream.Read(buf)
	require.Equal(t, io.EOF, err)
	require.Zero(t, n)
	require.Len(t, pipeline.post, 1)
	require.Nil(t, pipeline.postErr[0])
	require.NoError(t, res.Stream.Close())

	// A reader that closes before reading EOF ends the stream as cancelled.
	m2, pipeline2 := newTestManager(t, cfg)
	res, bErr = m2.Serve(testCtx(t), Request{Endpoint: "last", Method: "POST", Path: "/s", Headers: map[string]string{}})
	require.Nil(t, bErr)
	_, err = res.Stream.Read(buf)
	require.NoError(t, err)
	require.NoError(t, res.Stream.Close())
	require.Len(t, pipeline2.postErr, 1)
	require.NotNil(t, pipeline2.postErr[0])
	assert.Equal(t, schemas.RequestCancelled, *pipeline2.postErr[0].Error.Type)
}

// An upstream error on a streaming route is streamed with the upstream's status, as a passthrough
// stream is, and the post-hooks see the error status once it ends.
func TestStreamedRouteStreamsUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "data: {\"error\":\"slow down\"}\n\n")
	}))
	t.Cleanup(upstream.Close)
	cfg := bearerConfig("err", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/s", Stream: schemas.Ptr(true)}}
	m, pipeline := newTestManager(t, cfg)
	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "err", Method: "POST", Path: "/s", Headers: map[string]string{}})
	require.Nil(t, bErr)
	require.NotNil(t, res.Stream, "an error reply on a streaming route is streamed")
	assert.Equal(t, http.StatusTooManyRequests, res.Stream.StatusCode)
	body, err := io.ReadAll(res.Stream)
	require.NoError(t, err)
	assert.Contains(t, string(body), "slow down")
	require.NoError(t, res.Stream.Close())
	require.Len(t, pipeline.post, 1)
	assert.Equal(t, http.StatusTooManyRequests, pipeline.post[0].StatusCode)
}

// A relay that cannot reach its upstream fails as an LLM call does: 502 provider_connection_failed,
// timed from the start of the call (the pre-hooks included), and named as a relay request.
func TestRelayTransportFailureMatchesLLM(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	cfg := bearerConfig("dead", deadURL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/x"}}
	m, pipeline := newTestManager(t, cfg)
	pipeline.preDelay = 60 * time.Millisecond
	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "dead", Method: "POST", Path: "/x", Headers: map[string]string{}})
	require.NotNil(t, bErr)
	assert.Equal(t, http.StatusBadGateway, bErr.EffectiveHTTPStatus())
	assert.Equal(t, schemas.ErrorTypeProviderConnectionFailed, schemas.ClassifyErrorType(bErr, schemas.RelayRequest))
	assert.Equal(t, schemas.RelayRequest, bErr.ExtraFields.RequestType)
	assert.GreaterOrEqual(t, bErr.ExtraFields.Latency, int64(60), "the error is timed from the start of the call, not the upstream attempt")
}

// The relay span carries the call's whole duration (a refused call included), the upstream time
// in fractional milliseconds, and the error classified as LLM errors are; a streamed call stamps
// the stream's own work on the root span, as an LLM stream does.
func TestRelaySpanTimingAndClassification(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, `data: {"usage":{"credits":2}}`+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(upstream.Close)
	cfg := bearerConfig("spanned", upstream.URL)
	cfg.Usage = &schemas.RelayUsageConfig{CostPath: "usage.credits"}
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/s", Stream: schemas.Ptr(true)}}

	t.Run("refused call", func(t *testing.T) {
		m, pipeline := newTestManager(t, cfg)
		status := http.StatusPaymentRequired
		pipeline.deny = &schemas.BifrostError{StatusCode: &status, Error: &schemas.ErrorField{Message: "budget exceeded"}, ExtraFields: schemas.BifrostErrorExtraFields{ErrorType: schemas.ErrorTypePolicyBudgetExceeded}}
		pipeline.denyAfter = 40 * time.Millisecond
		rec := newSpanRecorder()
		_, bErr := m.Serve(tracedCtx(t, rec), Request{Endpoint: "spanned", Method: "POST", Path: "/s", Headers: map[string]string{}})
		require.NotNil(t, bErr)
		assert.Equal(t, string(schemas.ErrorTypePolicyBudgetExceeded), rec.attr("relay.spanned", schemas.AttrErrorTypeSpec), "the decision, not a bare 402")
		assert.Equal(t, false, rec.attr("relay.spanned", schemas.AttrBifrostRelayStreamed), "a call that got no reply was not streamed, whatever the route's mode")
		duration, _ := rec.attr("relay.spanned", schemas.AttrBifrostRelayDurationMs).(float64)
		assert.GreaterOrEqual(t, duration, 40.0, "a refused call records the time the pre-hooks held it")
	})
	t.Run("upstream error status is classified", func(t *testing.T) {
		limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "data: {\"error\":\"slow down\"}\n\n")
		}))
		t.Cleanup(limited.Close)
		limitedCfg := bearerConfig("limited", limited.URL)
		limitedCfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/s", Stream: schemas.Ptr(true)}}
		m, _ := newTestManager(t, limitedCfg)
		rec := newSpanRecorder()
		res, bErr := m.Serve(tracedCtx(t, rec), Request{Endpoint: "limited", Method: "POST", Path: "/s", Headers: map[string]string{}})
		require.Nil(t, bErr)
		_, _ = io.ReadAll(res.Stream)
		require.NoError(t, res.Stream.Close())
		assert.Equal(t, string(schemas.ErrorTypeProviderRateLimited), rec.attr("relay.limited", schemas.AttrErrorTypeSpec), "a failure class, as an LLM provider's 429 is labelled, not the bare code")
	})
	t.Run("streamed call", func(t *testing.T) {
		m, _ := newTestManager(t, cfg)
		rec := newSpanRecorder()
		res, bErr := m.Serve(tracedCtx(t, rec), Request{Endpoint: "spanned", Method: "POST", Path: "/s", Headers: map[string]string{}})
		require.Nil(t, bErr)
		_, _ = io.ReadAll(res.Stream)
		require.NoError(t, res.Stream.Close())
		upstreamMs, ok := rec.attr("relay.spanned", schemas.AttrBifrostRelayUpstreamDurationMs).(float64)
		assert.True(t, ok, "upstream time is fractional milliseconds")
		assert.Greater(t, upstreamMs, 0.0)
		_, parsed := rec.attr("root", schemas.AttrBifrostStreamParseMs).(float64)
		assert.True(t, parsed, "the stream's event parsing is stamped on the root span, as for an LLM stream")
	})
}

func TestUpsertValidatesEndpoint(t *testing.T) {
	m := NewManager(nil, nil)
	cfg := bearerConfig("tavily", "https://search.example.com")
	require.NoError(t, m.Upsert(cfg))
	assert.Equal(t, []string{"tavily"}, m.Names())

	bad := bearerConfig("bad name", "https://search.example.com")
	assert.ErrorContains(t, m.Upsert(bad), "name")
	for _, dot := range []string{".", ".."} {
		assert.ErrorContains(t, m.Upsert(bearerConfig(dot, "https://search.example.com")), "name", "%q is a dot segment no route can reach", dot)
	}

	noCred := bearerConfig("nocred", "https://search.example.com")
	noCred.Credential = nil
	assert.ErrorContains(t, m.Upsert(noCred), "credential.value is required")

	keyless := bearerConfig("keyless", "https://search.example.com")
	keyless.Auth.Type, keyless.Credential = schemas.RelayAuthNone, nil
	require.NoError(t, m.Upsert(keyless), "auth none needs no credential")

	sigv4 := bearerConfig("textract", "https://textract.example.com")
	sigv4.Auth = schemas.RelayAuthConfig{Type: schemas.RelayAuthAWSSigV4, AWS: &schemas.RelayAWSAuthConfig{Service: "textract"}}
	sigv4.Credential = &schemas.RelayCredential{AWS: &schemas.RelayAWSCredential{}}
	assert.ErrorContains(t, m.Upsert(sigv4), "credential.aws.region")

	relative := bearerConfig("rel", "/v1")
	assert.ErrorContains(t, m.Upsert(relative), "base_url")

	m.RemoveRuntime("tavily")
	assert.Equal(t, []string{"keyless"}, m.Names())
}

func TestServeBufferedPricesAndInjectsCredential(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotQuery = r.Header.Get("Authorization"), r.URL.Path, r.URL.RawQuery
		time.Sleep(5 * time.Millisecond) // so the upstream socket time is visible on the response
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[1,2,3],"usage":{"credits":4}}`)
	}))
	defer upstream.Close()

	cfg := bearerConfig("tavily", upstream.URL+"/base")
	cfg.Usage = &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.001), UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.008)}
	cfg.Routes = []schemas.RelayRouteConfig{
		{Method: "POST", Path: "/search"},
		{Method: "POST", Path: "/extract", Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.0), CostPerUnit: schemas.Ptr(0.016)}},
		{Method: "GET", Path: "/usage"},
	}
	m, pipeline := newTestManager(t, cfg)

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/search", RawQuery: "q=1", Headers: map[string]string{"authorization": "Bearer caller", "content-type": "application/json"}, Body: []byte(`{"query":"x"}`)})
	require.Nil(t, bErr)
	require.NotNil(t, res.Response)
	assert.Equal(t, "Bearer sk-endpoint", gotAuth, "the endpoint credential replaces the caller's")
	assert.Equal(t, "/base/search", gotPath, "the path is pinned under base_url")
	assert.Equal(t, "q=1", gotQuery)
	require.NotNil(t, res.Response.Usage)
	assert.InDelta(t, 0.001+4*0.008, res.Response.Usage.Cost, 1e-12, "fee + credits * rate")

	res, bErr = m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/extract", Headers: map[string]string{}})
	require.Nil(t, bErr)
	assert.InDelta(t, 4*0.016, res.Response.Usage.Cost, 1e-12, "the route overrides the rate and waives the fee, inheriting the units path")

	res, bErr = m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "GET", Path: "/usage", Headers: map[string]string{}})
	require.Nil(t, bErr)
	assert.Nil(t, res.Response.Usage, "a GET is not billable by default: not counted")

	_, bErr = m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/crawl", Headers: map[string]string{}})
	require.NotNil(t, bErr)
	assert.Equal(t, http.StatusNotFound, *bErr.StatusCode, "an unlisted route is refused")

	require.Len(t, pipeline.pre, 3, "a refused route reaches no hook")
	assert.Equal(t, "/search", pipeline.pre[0].RoutePath)
	assert.True(t, pipeline.pre[0].Billable)
	require.Len(t, pipeline.post, 3)
	assert.Equal(t, "tavily", pipeline.post[0].RelayName)
	assert.GreaterOrEqual(t, pipeline.post[0].UpstreamLatency, int64(5), "the upstream socket time is accumulated on the context, as for an LLM call")
	assert.GreaterOrEqual(t, pipeline.post[0].Latency, pipeline.post[0].UpstreamLatency, "latency covers the whole call, so Bifrost's own time is the difference")
}

func TestServeUnknownRelay(t *testing.T) {
	cfg := bearerConfig("known", "https://example.com")
	cfg.SkipRouteCheck = true
	m, _ := newTestManager(t, cfg)
	for _, name := range []string{"missing"} {
		_, bErr := m.Serve(testCtx(t), Request{Endpoint: name, Method: "POST", Path: "/", Headers: map[string]string{}})
		require.NotNil(t, bErr)
		assert.Equal(t, http.StatusNotFound, *bErr.StatusCode)
	}
}

func TestServeDeniedByPreHookNeverCallsUpstream(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()
	cfg := bearerConfig("tavily", upstream.URL)
	cfg.SkipRouteCheck = true
	m, pipeline := newTestManager(t, cfg)
	pipeline.deny = statusError(http.StatusForbidden, "virtual key may not use relay tavily")
	pipeline.denyAfter = 5 * time.Millisecond

	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/search", Headers: map[string]string{}})
	require.NotNil(t, bErr)
	assert.Equal(t, http.StatusForbidden, *bErr.StatusCode)
	assert.False(t, called)
	require.Len(t, pipeline.postErr, 1, "the post-hooks still observe the refusal")
	assert.GreaterOrEqual(t, pipeline.postErr[0].ExtraFields.Latency, int64(5), "a refusal records the time the call spent in Bifrost, like an LLM short-circuit")
}

func TestServeStreamObservesUsageAndRunsPostHookOnce(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i, event := range []string{`{"delta":"a"}`, `{"delta":"b","usage":{"in":3}}`, `{"usage":{"in":3,"out":7}}`} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
			if i == 0 {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()
	cfg := bearerConfig("llm", upstream.URL)
	cfg.Usage = &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.002), InputTokensPath: "usage.in", OutputTokensPath: "usage.out"}
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/chat", Stream: schemas.Ptr(true)}}
	m, pipeline := newTestManager(t, cfg)

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "llm", Method: "POST", Path: "/chat", Headers: map[string]string{}})
	require.Nil(t, bErr)
	require.NotNil(t, res.Stream)
	body, err := io.ReadAll(res.Stream)
	require.NoError(t, err)
	require.NoError(t, res.Stream.Close())
	assert.Equal(t, 3, strings.Count(string(body), "data: "), "bytes are forwarded unchanged")

	require.Len(t, pipeline.post, 1, "EOF then Close runs the post-hooks once")
	final := pipeline.post[0]
	assert.True(t, final.Streamed)
	assert.Equal(t, int64(len(body)), final.BytesOut)
	require.NotNil(t, final.Usage)
	assert.Equal(t, 3, final.Usage.InputTokens)
	assert.Equal(t, 7, final.Usage.OutputTokens, "fields keep the last value reported")
	assert.InDelta(t, 0.002, final.Usage.Cost, 1e-12, "tokens are logged, not priced")
	assert.Nil(t, pipeline.postErr[0])
}

// The streaming client has no whole-response deadline, so an upstream that never answers the
// request must still be given up on by the endpoint timeout.
func TestServeStreamOpenIsBoundedByTimeout(t *testing.T) {
	released := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer upstream.Close()
	defer close(released)
	cfg := bearerConfig("slow", upstream.URL)
	cfg.Network = &schemas.RelayNetworkConfig{TimeoutSeconds: 1}
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/events", Stream: schemas.Ptr(true)}}
	m, _ := newTestManager(t, cfg)

	begin := time.Now()
	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "slow", Method: "POST", Path: "/events", Headers: map[string]string{}})
	require.NotNil(t, bErr)
	assert.Less(t, time.Since(begin), 5*time.Second, "the wait for the headers is bounded by timeout_seconds")
	require.NotNil(t, bErr.Error.Type)
	assert.Equal(t, schemas.RequestTimedOut, *bErr.Error.Type)
}

func TestServeStreamBilledBySizeOnEarlyClose(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer upstream.Close()
	cfg := bearerConfig("tts", upstream.URL)
	cfg.Usage = &schemas.RelayUsageConfig{UnitsMeasure: schemas.RelayUnitsBodyBytes, CostPerUnit: schemas.Ptr(0.001)}
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/tts", Stream: schemas.Ptr(true)}}
	m, pipeline := newTestManager(t, cfg)

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "tts", Method: "POST", Path: "/tts", Headers: map[string]string{}})
	require.Nil(t, bErr)
	buf := make([]byte, 100)
	n, err := io.ReadFull(res.Stream, buf)
	require.NoError(t, err)
	require.NoError(t, res.Stream.Close())

	require.Len(t, pipeline.post, 1)
	require.NotNil(t, pipeline.post[0].Usage, "hanging up early still reports what was forwarded")
	assert.InDelta(t, float64(n)*0.001, pipeline.post[0].Usage.Cost, 1e-12)
	require.NotNil(t, pipeline.postErr[0])
}

// sseUpstream serves one small event stream for the capture tests.
func sseUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

// The logging plugin owns the content policy and stamps one capture decision on ctx; core/relay
// keeps no precedence of its own. Governance's or the per-request keys being present for unrelated
// reasons must not turn capture on by themselves: without the plugin's decision nothing is kept.
func TestServeStreamCaptureFollowsPluginDecision(t *testing.T) {
	const payload = "data: {\"delta\":\"a\"}\n\n"
	for _, tc := range []struct {
		name string
		set  func(ctx *schemas.BifrostContext)
		want string
	}{
		{"nothing stamped: no logging plugin, nothing captured", func(*schemas.BifrostContext) {}, ""},
		{"plugin asked for capture", func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, true)
		}, payload},
		{"plugin declined capture", func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, false)
		}, ""},
		{"governance alone does not turn capture on", func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceDisableContentLogging, false)
			ctx.SetValue(schemas.BifrostContextKeyAllowPerRequestStorageOverride, true)
			ctx.SetValue(schemas.BifrostContextKeyDisableContentLogging, false)
		}, ""},
		{"governance alone does not turn capture off", func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, true)
			ctx.SetValue(schemas.BifrostContextKeyGovernanceDisableContentLogging, true)
		}, payload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := sseUpstream(t, payload)
			cfg := bearerConfig("llm", upstream.URL)
			cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/chat", Stream: schemas.Ptr(true)}}
			m, pipeline := newTestManager(t, cfg)
			ctx := testCtx(t)
			tc.set(ctx)
			res, bErr := m.Serve(ctx, Request{Endpoint: "llm", Method: "POST", Path: "/chat", Headers: map[string]string{}})
			require.Nil(t, bErr)
			body, err := io.ReadAll(res.Stream)
			require.NoError(t, err)
			require.NoError(t, res.Stream.Close())
			require.Len(t, pipeline.post, 1)
			assert.Equal(t, tc.want, string(pipeline.post[0].Body))
			if tc.want != "" {
				assert.Equal(t, body, pipeline.post[0].Body, "the capture matches what was forwarded")
			}
		})
	}
}

// A streamed reply's body reaches the post-hooks in full, as a buffered reply's does and as the
// stream accumulator keeps an LLM stream: there is no bound of the relay's own on the capture.
func TestServeStreamCapturesWholeReply(t *testing.T) {
	body := strings.Repeat("data: {\"chunk\":\"xxxxxxxx\"}\n\n", 160*1024) // ~4.7 MiB
	upstream := sseUpstream(t, body)
	cfg := bearerConfig("llm", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/chat", Stream: schemas.Ptr(true)}}
	m, pipeline := newTestManager(t, cfg)

	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, true)
	res, bErr := m.Serve(ctx, Request{Endpoint: "llm", Method: "POST", Path: "/chat", Headers: map[string]string{}})
	require.Nil(t, bErr)
	_, err := io.ReadAll(res.Stream)
	require.NoError(t, err)
	require.NoError(t, res.Stream.Close())

	require.Len(t, pipeline.post, 1)
	require.Equal(t, len(body), len(pipeline.post[0].Body), "the whole streamed reply is captured")
	assert.True(t, string(pipeline.post[0].Body) == body, "the captured bytes are the reply's bytes")
}

// pastFormerCap is just over the 64 MiB the relay once refused a buffered reply above and cut a
// streamed capture at, so the size tests fail against that bound and pass without it.
const pastFormerCap = 64<<20 + 1<<20

// sizedUpstream serves size bytes in 1 MiB writes, so the test never holds the reply as one string.
func sizedUpstream(t *testing.T, contentType string, size int) *httptest.Server {
	t.Helper()
	chunk := []byte(strings.Repeat("x", 1<<20))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		for sent := 0; sent < size; sent += len(chunk) {
			if _, err := w.Write(chunk[:min(len(chunk), size-sent)]); err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

// A buffered reply has no size bound of the relay's own, as a provider's unary reply has none: a
// large JSON or binary reply on a route that does not stream is relayed whole, never refused.
func TestServeBufferedRelaysLargeReplyWhole(t *testing.T) {
	upstream := sizedUpstream(t, "application/octet-stream", pastFormerCap)
	cfg := bearerConfig("files", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "GET", Path: "/download", Stream: schemas.Ptr(false)}}
	m, pipeline := newTestManager(t, cfg)

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "files", Method: "GET", Path: "/download", Headers: map[string]string{}})
	require.Nil(t, bErr, "a large buffered reply is not refused for its size")
	require.NotNil(t, res.Response)
	assert.Equal(t, http.StatusOK, res.Response.StatusCode)
	assert.Equal(t, pastFormerCap, len(res.Response.Body))
	require.Len(t, pipeline.post, 1)
	assert.Equal(t, int64(pastFormerCap), pipeline.post[0].BytesOut)
}

// A streamed reply is captured whole for the log row past any former bound, as the stream
// accumulator keeps an LLM stream.
func TestServeStreamCapturesPastFormerCap(t *testing.T) {
	upstream := sizedUpstream(t, "application/octet-stream", pastFormerCap)
	cfg := bearerConfig("files", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "GET", Path: "/download", Stream: schemas.Ptr(true)}}
	m, pipeline := newTestManager(t, cfg)

	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, true)
	res, bErr := m.Serve(ctx, Request{Endpoint: "files", Method: "GET", Path: "/download", Headers: map[string]string{}})
	require.Nil(t, bErr)
	_, err := io.Copy(io.Discard, res.Stream)
	require.NoError(t, err)
	require.NoError(t, res.Stream.Close())
	require.Len(t, pipeline.post, 1)
	assert.Equal(t, pastFormerCap, len(pipeline.post[0].Body), "the capture is not cut at a size of the relay's own")
}

// Every forwarded chunk touches the logging plugin's activity slot, so a live stream is never
// reaped as stale, the way PostLLMHook refreshes LastActivity per chunk.
func TestServeStreamTouchesActivityPerChunk(t *testing.T) {
	upstream := sseUpstream(t, "data: {\"delta\":\"a\"}\n\n")
	cfg := bearerConfig("llm", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/chat", Stream: schemas.Ptr(true)}}
	m, _ := newTestManager(t, cfg)

	var activity atomic.Int64
	seed := time.Now().Add(-time.Hour).UnixNano()
	activity.Store(seed)
	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyRelayActivity, &activity)
	res, bErr := m.Serve(ctx, Request{Endpoint: "llm", Method: "POST", Path: "/chat", Headers: map[string]string{}})
	require.Nil(t, bErr)
	_, err := io.ReadAll(res.Stream)
	require.NoError(t, err)
	require.NoError(t, res.Stream.Close())
	assert.Greater(t, activity.Load(), seed, "a forwarded chunk refreshes the activity slot")
}

// A stream that goes quiet past stream_idle_timeout_in_seconds ends as a timeout, the type the
// LLM path gives the same stall, not as a generic transport error.
func TestServeStreamIdleTimeoutIsATimeout(t *testing.T) {
	released := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"delta\":\"a\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer upstream.Close()
	defer close(released)
	cfg := bearerConfig("slow", upstream.URL)
	cfg.Network = &schemas.RelayNetworkConfig{StreamIdleTimeoutInSeconds: 1}
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/events", Stream: schemas.Ptr(true)}}
	m, pipeline := newTestManager(t, cfg)

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "slow", Method: "POST", Path: "/events", Headers: map[string]string{}})
	require.Nil(t, bErr)
	_, err := io.ReadAll(res.Stream)
	require.Error(t, err, "the idle guard ends the stream")
	require.NoError(t, res.Stream.Close())

	require.Len(t, pipeline.postErr, 1)
	require.NotNil(t, pipeline.postErr[0])
	require.NotNil(t, pipeline.postErr[0].Error.Type)
	assert.Equal(t, schemas.RequestTimedOut, *pipeline.postErr[0].Error.Type)
}

func TestServeJobRouteHandsBackJob(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/jobs/j-1" {
			_, _ = io.WriteString(w, `{"status":"SUCCEEDED","pages":3}`)
			return
		}
		_, _ = io.WriteString(w, `{"JobId":"j-1"}`)
	}))
	defer upstream.Close()
	cfg := bearerConfig("textract", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{
		Method: "POST", Path: "/start",
		Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01)},
		Job: &schemas.RelayJobConfig{
			IDPath: "JobId", StatusPath: "status", Succeeded: []string{"SUCCEEDED"},
			Poll:  schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}"},
			Usage: &schemas.RelayUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.05)},
		},
	}}
	m, _ := newTestManager(t, cfg)

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "textract", Method: "POST", Path: "/start", Headers: map[string]string{}})
	require.Nil(t, bErr)
	require.NotNil(t, res.Response.Usage)
	assert.InDelta(t, 0.01, res.Response.Usage.Cost, 1e-12, "the start call carries the route fee")
	job := res.Response.Usage.Job
	require.NotNil(t, job)
	assert.Equal(t, "j-1", job.ID)

	snap, err := ParseJobSnapshot(job.Params)
	require.NoError(t, err)
	poll, bErr := m.PollJob(testCtx(t), "textract", snap.PollRequest(job.ID))
	require.Nil(t, bErr, "a poll skips the route check")
	status := snap.Status(poll.Body)
	done, succeeded := snap.Outcome(status)
	assert.True(t, done && succeeded)
	assert.InDelta(t, 3*0.05, snap.Price(poll.ContentType, poll.Body).Cost, 1e-12, "the route fee was charged on the start call, not again at settlement")
}

func TestServeRequestSideBillingRefusesAmbiguousBody(t *testing.T) {
	cfg := bearerConfig("translate", "https://example.com")
	cfg.SkipRouteCheck = true
	cfg.Usage = &schemas.RelayUsageConfig{UnitsSource: schemas.RelayUnitsFromRequest, UnitsPath: "Text", UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(0.000015)}
	m, pipeline := newTestManager(t, cfg)
	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "translate", Method: "POST", Path: "/", Headers: map[string]string{}, Body: []byte(`{"Text":"a","text":"bbbb"}`)})
	require.NotNil(t, bErr)
	assert.Equal(t, http.StatusBadRequest, *bErr.StatusCode)
	assert.Empty(t, pipeline.pre)
}

func BenchmarkMatchRoute(b *testing.B) {
	cfg := &schemas.RelayConfig{}
	for i := 0; i < 50; i++ {
		cfg.Routes = append(cfg.Routes, schemas.RelayRouteConfig{Method: "POST", Path: fmt.Sprintf("/v1/op%d/*", i)})
	}
	e := &endpoint{cfg: cfg, routes: compileRoutes(cfg)}
	b.ReportAllocs()
	for b.Loop() {
		_ = e.matchRoute("POST", "/v1/op49/x", nil)
	}
}

func BenchmarkExtractUsage(b *testing.B) {
	u := resolveUsage(&schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.001), UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.008)}, nil)
	body := []byte(`{"results":[1,2,3],"usage":{"credits":4}}`)
	b.ReportAllocs()
	for b.Loop() {
		_ = extractUsage(u, "application/json", body, usageFields{})
	}
}

func TestServeDisabledRouteIsRefused(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()
	cfg := bearerConfig("tavily", upstream.URL)
	cfg.SkipRouteCheck = true
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/search", Enabled: schemas.Ptr(false)}}
	m, pipeline := newTestManager(t, cfg)

	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/search", Headers: map[string]string{}})
	require.NotNil(t, bErr)
	assert.Equal(t, http.StatusNotFound, *bErr.StatusCode, "a disabled route refuses its requests even with relay-any-path on")
	assert.False(t, called)
	assert.Empty(t, pipeline.pre)

	_, bErr = m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/other", Headers: map[string]string{}})
	assert.Nil(t, bErr, "other paths still relay")

	out, err := PreviewUsage(cfg, UsagePreviewInput{Method: "POST", Path: "/search"})
	require.NoError(t, err)
	assert.False(t, out.RouteAllowed)
}

func TestServeReturnsResponseRecoveredByPostHook(t *testing.T) {
	cfg := bearerConfig("down", "http://127.0.0.1:1")
	cfg.SkipRouteCheck = true
	m, pipeline := newTestManager(t, cfg)
	pipeline.recover = &schemas.BifrostRelayResponse{StatusCode: http.StatusOK, Body: []byte("cached")}

	for _, headers := range []map[string]string{{}, {"accept": "text/event-stream"}} {
		res, bErr := m.Serve(testCtx(t), Request{Endpoint: "down", Method: "POST", Path: "/x", Headers: headers})
		require.Nil(t, bErr, "the post-hook replaced the error")
		require.NotNil(t, res)
		require.NotNil(t, res.Response, "buffered and stream-opening failures both hand back the recovered response")
		assert.Equal(t, "cached", string(res.Response.Body))
	}
}

func TestServeRefusesEmptyPostHookResult(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer upstream.Close()
	cfg := bearerConfig("tavily", upstream.URL)
	cfg.SkipRouteCheck = true
	m, pipeline := newTestManager(t, cfg)
	pipeline.drop = true

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/search", Headers: map[string]string{}})
	assert.Nil(t, res, "a result with neither a response nor a stream would panic the relay")
	require.NotNil(t, bErr)
}

// TestServeReplyDecidesStreaming pins the streaming decision on the reply: a caller asking for SSE
// from an upstream that answers JSON is still billed from the JSON, and an event-stream reply streams.
func TestServeReplyDecidesStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"usage\":{\"credits\":3}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[],"usage":{"credits":50}}`)
	}))
	defer upstream.Close()
	cfg := bearerConfig("search", upstream.URL)
	cfg.SkipRouteCheck = true
	cfg.ExtraHeaders = map[string]schemas.SecretVar{"Accept": *schemas.NewSecretVar("application/json")}
	cfg.Usage = &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.001), UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.008)}
	m, _ := newTestManager(t, cfg)

	res, bErr := m.Serve(testCtx(t), Request{Endpoint: "search", Method: "POST", Path: "/search", Headers: map[string]string{"accept": "text/event-stream"}, Body: []byte(`{"stream":true}`)})
	require.Nil(t, bErr)
	require.NotNil(t, res.Response, "a JSON reply is buffered whatever the caller asked for")
	require.NotNil(t, res.Response.Usage)
	assert.InDelta(t, 0.001+50*0.008, res.Response.Usage.Cost, 1e-12, "the unit charge is read from the JSON reply")

	res, bErr = m.Serve(testCtx(t), Request{Endpoint: "search", Method: "POST", Path: "/sse", Headers: map[string]string{}})
	require.Nil(t, bErr)
	require.NotNil(t, res.Stream, "an event-stream reply is streamed")
	_, _ = io.ReadAll(res.Stream)
	require.NoError(t, res.Stream.Close())
}

func TestServeReadsModelUnderSentContentType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer upstream.Close()
	cfg := bearerConfig("llm", upstream.URL)
	cfg.SkipRouteCheck = true
	cfg.ExtraHeaders = map[string]schemas.SecretVar{"Content-Type": *schemas.NewSecretVar("application/json")}
	cfg.Usage = &schemas.RelayUsageConfig{ModelPath: "model"}
	m, pipeline := newTestManager(t, cfg)

	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "llm", Method: "POST", Path: "/chat", Body: []byte(`{"model":"gpt-x"}`)})
	require.Nil(t, bErr)
	require.Len(t, pipeline.pre, 1)
	assert.Equal(t, "gpt-x", pipeline.pre[0].Model, "with no caller Content-Type the upstream receives the operator's JSON one")

	// The caller's Content-Type replaces the operator's on the wire, as on a passthrough call, so
	// the body is read under it.
	_, bErr = m.Serve(testCtx(t), Request{Endpoint: "llm", Method: "POST", Path: "/chat", Headers: map[string]string{"content-type": "text/plain"}, Body: []byte(`{"model":"gpt-x"}`)})
	require.Nil(t, bErr)
	require.Len(t, pipeline.pre, 2)
	assert.Empty(t, pipeline.pre[1].Model, "a text/plain body is not read as JSON")
}

// The pre-hook sees, for each header the matched route matches on, the value that selected the
// route: the caller's copy beats the relay's own extra header, as on a passthrough call, a missing
// caller header still shows the operator's value, and a value read from the environment is handed
// over redacted.
func TestServeReportsMatchedHeaderValues(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer upstream.Close()
	t.Setenv("RELAY_TEST_TENANT", "acme")
	cfg := bearerConfig("textract", upstream.URL)
	cfg.ExtraHeaders = map[string]schemas.SecretVar{
		"X-Amz-Target": *schemas.NewSecretVar("Textract.AnalyzeDocument"),
		"X-Tenant":     *schemas.NewSecretVar("env.RELAY_TEST_TENANT"),
	}
	cfg.Routes = []schemas.RelayRouteConfig{
		{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.*", "X-Tenant": "*", "Content-Type": "application/x-amz-json-*"}},
		{Method: "POST", Path: "/plain"},
	}
	m, pipeline := newTestManager(t, cfg)

	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "textract", Method: "POST", Path: "/", Headers: map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis", "content-type": "application/x-amz-json-1.1"}})
	require.Nil(t, bErr)
	require.Len(t, pipeline.pre, 1)
	assert.Equal(t, map[string]string{
		"x-amz-target": "Textract.GetDocumentAnalysis", // the caller's value replaces the operator's, as on a passthrough call
		"x-tenant":     schemas.RedactedAttrValue,      // set by the relay from the environment: never copied into a row
		"content-type": "application/x-amz-json-1.1",
	}, pipeline.pre[0].RouteHeaders)

	_, bErr = m.Serve(testCtx(t), Request{Endpoint: "textract", Method: "POST", Path: "/plain", Headers: map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis"}})
	require.Nil(t, bErr)
	require.Len(t, pipeline.pre, 2)
	assert.Nil(t, pipeline.pre[1].RouteHeaders, "a route without header matchers reports none")
}

// BenchmarkServeStreamBodyCapture compares the streamed relay path with content-log capture
// enabled against the default (capture disabled), per AGENTS.md's request-path cost rule: capture
// is opt-in, so the common case (disabled) must show no meaningful allocation increase.
func BenchmarkServeStreamBodyCapture(b *testing.B) {
	payload := strings.Repeat("data: {\"delta\":\"a\"}\n\n", 20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, payload)
	}))
	defer upstream.Close()
	cfg := bearerConfig("llm", upstream.URL)
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/chat", Stream: schemas.Ptr(true)}}

	run := func(b *testing.B, capture bool) {
		m, pipeline := newTestManager(b, cfg)
		ctx := testCtx(b)
		if capture {
			ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, true)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			res, bErr := m.Serve(ctx, Request{Endpoint: "llm", Method: "POST", Path: "/chat", Headers: map[string]string{}})
			if bErr != nil {
				b.Fatal(bErr)
			}
			_, _ = io.ReadAll(res.Stream)
			_ = res.Stream.Close()
			// fakePipeline retains every request/response it sees (including each iteration's
			// capture buffer); clearing it each iteration keeps the benchmark measuring the
			// Serve/Read/Close path's own cost, not accumulated GC pressure from b.N retained
			// buffers that a real pipeline (which doesn't hold onto finished responses) never pays.
			pipeline.mu.Lock()
			pipeline.pre, pipeline.post, pipeline.postErr = pipeline.pre[:0], pipeline.post[:0], pipeline.postErr[:0]
			pipeline.mu.Unlock()
		}
	}

	b.Run("CaptureDisabled", func(b *testing.B) { run(b, false) })
	b.Run("CaptureEnabled", func(b *testing.B) { run(b, true) })
}

// TestGlobalProxyErrorRefusesEndpoint pins that an endpoint with no proxy_config of its own
// is refused when the global proxy it would inherit cannot be read, instead of going direct
// around a proxy the admin turned on. An endpoint with its own proxy_config is unaffected.
func TestGlobalProxyErrorRefusesEndpoint(t *testing.T) {
	m := NewManager(nil, nil)
	m.SetGlobalProxy(func() (*schemas.ProxyConfig, error) { return nil, errors.New("proxy URL names no host") })

	err := m.Upsert(bearerConfig("tavily", "https://search.example.com"))
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.ErrorContains(t, err, "global proxy")
	assert.Empty(t, m.Names(), "an endpoint that would go direct must not go live")

	assert.ErrorContains(t, m.Replace([]*schemas.RelayConfig{bearerConfig("tavily", "https://search.example.com")}), "global proxy")
	assert.Empty(t, m.Names())

	own := bearerConfig("own", "https://search.example.com")
	own.ProxyConfig = &schemas.ProxyConfig{Type: schemas.EnvProxy}
	require.NoError(t, m.Upsert(own), "an endpoint with its own proxy_config never reads the global proxy")
}

// TestLargeBufferedReplyIsForwardedAsStream pins the large-payload response threshold on the relay,
// as FinalizeResponseWithLargeDetection applies it to an LLM reply: a reply the relay would buffer is
// forwarded as a stream once it is past the threshold (known length or not), arrives whole, and the
// log row keeps only a preview; a reply within it, or any reply with no threshold set, is buffered.
func TestLargeBufferedReplyIsForwardedAsStream(t *testing.T) {
	previous := largeResponsePreviewBytes
	largeResponsePreviewBytes = 16
	t.Cleanup(func() { largeResponsePreviewBytes = previous })
	big := strings.Repeat("a", 4096)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		body := big
		if r.URL.Query().Get("small") == "1" {
			body = "small reply"
		}
		if r.URL.Query().Get("chunked") == "1" {
			_, _ = io.WriteString(w, body[:len(body)/2])
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, body[len(body)/2:])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)
	ctxWith := func(threshold int64) *schemas.BifrostContext {
		ctx := testCtx(t)
		ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, true)
		if threshold > 0 {
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, threshold)
		}
		return ctx
	}
	for _, mode := range []struct {
		name   string
		stream *bool
	}{{"stream off", schemas.Ptr(false)}, {"auto", nil}} {
		for _, query := range []string{"", "chunked=1"} {
			t.Run(mode.name+" "+query, func(t *testing.T) {
				cfg := bearerConfig("big", upstream.URL)
				cfg.Routes = []schemas.RelayRouteConfig{{Method: "GET", Path: "/f", Stream: mode.stream}}
				m, pipeline := newTestManager(t, cfg)

				res, bErr := m.Serve(ctxWith(1024), Request{Endpoint: "big", Method: "GET", Path: "/f", RawQuery: query, Headers: map[string]string{}})
				require.Nil(t, bErr)
				require.NotNil(t, res.Stream, "a reply past the threshold is forwarded, not held")
				got, err := io.ReadAll(res.Stream)
				require.NoError(t, err)
				require.NoError(t, res.Stream.Close())
				assert.Equal(t, big, string(got), "the caller gets the whole reply, the bytes read while deciding included")
				require.Len(t, pipeline.post, 1)
				assert.True(t, pipeline.post[0].LargeResponse)
				assert.True(t, pipeline.post[0].Streamed)
				assert.Equal(t, big[:16], string(pipeline.post[0].Body), "the log row keeps a preview")
				assert.EqualValues(t, len(big), pipeline.post[0].BytesOut)

				small := query + "&small=1"
				res, bErr = m.Serve(ctxWith(1024), Request{Endpoint: "big", Method: "GET", Path: "/f", RawQuery: strings.TrimPrefix(small, "&"), Headers: map[string]string{}})
				require.Nil(t, bErr)
				require.NotNil(t, res.Response, "a reply within the threshold is buffered as before")
				assert.Equal(t, "small reply", string(res.Response.Body))
				assert.False(t, pipeline.post[1].LargeResponse)

				res, bErr = m.Serve(ctxWith(0), Request{Endpoint: "big", Method: "GET", Path: "/f", RawQuery: query, Headers: map[string]string{}})
				require.Nil(t, bErr)
				require.NotNil(t, res.Response, "with no threshold every reply is buffered, as in OSS")
				assert.Equal(t, big, string(res.Response.Body))
			})
		}
	}
}

// TestLargeReplyStaysBufferedWhenReadForUsage pins that a large reply is still read whole when
// the route reads something from its body (a priced field, or a job id): the fields can sit
// anywhere in the reply, so a hand-off would charge the default or lose the job. A route priced
// without reading the body is still handed off.
func TestLargeReplyStaysBufferedWhenReadForUsage(t *testing.T) {
	padding := strings.Repeat("a", 4096)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The fields come after the padding, past any preview.
		_, _ = io.WriteString(w, `{"padding":"`+padding+`","JobId":"j-1","usage":{"credits":5}}`)
	}))
	t.Cleanup(upstream.Close)
	ctx := func() *schemas.BifrostContext {
		c := testCtx(t)
		c.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1024))
		return c
	}
	for _, tc := range []struct {
		name     string
		route    schemas.RelayRouteConfig
		buffered bool
	}{
		{"priced from a reply field", schemas.RelayRouteConfig{Method: "POST", Path: "/r", Usage: &schemas.RelayUsageConfig{UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.5)}}, true},
		{"starts a job", schemas.RelayRouteConfig{Method: "POST", Path: "/r", Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01)}, Job: &schemas.RelayJobConfig{
			IDPath: "JobId", StatusPath: "status", Succeeded: []string{"SUCCEEDED"}, Poll: schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}"},
		}}, true},
		{"flat fee only", schemas.RelayRouteConfig{Method: "POST", Path: "/r", Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := bearerConfig("big", upstream.URL)
			cfg.Routes = []schemas.RelayRouteConfig{tc.route}
			m, pipeline := newTestManager(t, cfg)
			res, bErr := m.Serve(ctx(), Request{Endpoint: "big", Method: "POST", Path: "/r", Headers: map[string]string{}})
			require.Nil(t, bErr)
			if !tc.buffered {
				require.NotNil(t, res.Stream, "nothing is read from the body, so the reply is handed off")
				_, _ = io.ReadAll(res.Stream)
				require.NoError(t, res.Stream.Close())
				require.Len(t, pipeline.post, 1)
				assert.True(t, pipeline.post[0].LargeResponse)
				require.NotNil(t, pipeline.post[0].Usage)
				assert.InDelta(t, 0.01, pipeline.post[0].Usage.Cost, 1e-9)
				return
			}
			require.NotNil(t, res.Response, "the route reads the body, so the reply is held whole")
			require.Len(t, pipeline.post, 1)
			assert.False(t, pipeline.post[0].LargeResponse)
			require.NotNil(t, pipeline.post[0].Usage)
			if tc.route.Job != nil {
				require.NotNil(t, pipeline.post[0].Usage.Job, "the job id is read past the preview")
				assert.Equal(t, "j-1", pipeline.post[0].Usage.Job.ID)
			} else {
				assert.InDelta(t, 2.5, pipeline.post[0].Usage.Cost, 1e-9, "the units field is read past the preview")
			}
		})
	}
}

// TestServeAppliesHeadersInPassthroughOrder pins the header precedence a provider passthrough call
// has (SetExtraHeaders, then the caller's safe headers, then the key): the operator's extra header,
// the caller's x-bf-eh-* header over it, the caller's own header over both, and the credential
// last. x-bf-eh-* headers are forwarded whatever their name, each value on its own line.
func TestServeAppliesHeadersInPassthroughOrder(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()
	cfg := bearerConfig("docs", upstream.URL)
	cfg.SkipRouteCheck = true
	cfg.ExtraHeaders = map[string]schemas.SecretVar{
		"X-Tenant":  *schemas.NewSecretVar("operator"),
		"X-Version": *schemas.NewSecretVar("operator"),
		"X-Region":  *schemas.NewSecretVar("operator"),
	}
	m, _ := newTestManager(t, cfg)
	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-version":        {"extra"},
		"x-tenant":         {"extra"},
		"x-trace":          {"a", "b"},
		"x-upstream-token": {"tok"},
		"authorization":    {"Bearer extra"},
		"keep-alive":       {"timeout=5"},
	})
	_, bErr := m.Serve(ctx, Request{Endpoint: "docs", Method: "POST", Path: "/", Headers: map[string]string{"x-tenant": "caller"}})
	require.Nil(t, bErr)
	assert.Equal(t, "operator", got.Get("X-Region"), "an operator header nobody overrides is sent")
	assert.Equal(t, "extra", got.Get("X-Version"), "x-bf-eh-* replaces the operator's header, as on a passthrough call")
	assert.Equal(t, "caller", got.Get("X-Tenant"), "the caller's own header replaces both")
	assert.Equal(t, []string{"a", "b"}, got.Values("X-Trace"), "each x-bf-eh-* value is sent on its own line")
	assert.Equal(t, "tok", got.Get("X-Upstream-Token"), "an x-bf-eh-* header is forwarded whatever its name looks like")
	assert.Equal(t, "Bearer sk-endpoint", got.Get("Authorization"), "the credential is applied last and always wins")
	assert.Empty(t, got.Get("Keep-Alive"), "connection-level x-bf-eh-* headers are dropped, as SetExtraHeaders drops them")
}

// rawSpanRecorder is a spanRecorder whose connector asks for raw payloads, keeping the one span
// the relay attaches its bodies to.
type rawSpanRecorder struct {
	*spanRecorder
	span *schemas.Span
}

func (r *rawSpanRecorder) A2AContentDemand() (bool, bool)                    { return true, true }
func (r *rawSpanRecorder) SpanFromHandle(_ schemas.SpanHandle) *schemas.Span { return r.span }

// TestRelaySpanAttachesBodiesWhole pins that a relay span keeps the request and response bodies
// whole when a connector asks for raw payloads: the relay sets no size bound of its own on them.
func TestRelaySpanAttachesBodiesWhole(t *testing.T) {
	large := strings.Repeat("r", 2<<20) // past the 1 MiB bound relay spans used to apply
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, large)
	}))
	defer upstream.Close()
	cfg := bearerConfig("big", upstream.URL)
	cfg.SkipRouteCheck = true
	m, _ := newTestManager(t, cfg)
	rec := &rawSpanRecorder{spanRecorder: newSpanRecorder(), span: &schemas.Span{}}
	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyTracer, schemas.Tracer(rec))
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-relay")
	_, bErr := m.Serve(ctx, Request{Endpoint: "big", Method: "POST", Path: "/", Headers: map[string]string{}, Body: []byte(large)})
	require.Nil(t, bErr)
	require.NotNil(t, rec.span.A2A, "the bodies are attached")
	assert.Len(t, rec.span.A2A.RawRequest, len(large))
	assert.Len(t, rec.span.A2A.RawResponse, len(large))
}

// TestStreamUsagePanicIsAnInternalError pins that a panic while reading a stream's usage ends the
// stream as Bifrost's own fault (bifrost_internal), never as a caller cancellation, and that the
// bytes read are still handed over.
func TestStreamUsagePanicIsAnInternalError(t *testing.T) {
	var gotErr *schemas.BifrostError
	finished := false
	s := &Stream{
		ctx:     testCtx(t),
		body:    io.NopCloser(strings.NewReader("")),
		reader:  strings.NewReader("data: {\"usage\":1}\n\n"),
		stop:    func() {},
		counted: true,
		framer:  &providerUtils.SSEFramer{},
		observe: func([]byte) *schemas.RelayUsage { panic("bad usage mapping") },
		finish: func(_ *schemas.BifrostRelayResponse, err *schemas.BifrostError) {
			finished, gotErr = true, err
		},
	}
	buf := make([]byte, 64)
	n, err := s.Read(buf)
	require.Error(t, err)
	assert.Equal(t, len("data: {\"usage\":1}\n\n"), n, "the bytes read are still handed over")
	require.True(t, finished)
	require.NotNil(t, gotErr)
	assert.Equal(t, schemas.ErrorTypeBifrostInternal, schemas.ClassifyErrorType(gotErr, schemas.RelayRequest))
}

// An event past the SSE line ceiling fails the stream, as the native LLM scanner fails a stream
// with bufio.ErrTooLong, instead of being dropped unread and the call charged the default fee.
func TestStreamOversizedEventFailsTheStream(t *testing.T) {
	var gotErr *schemas.BifrostError
	finished := false
	s := &Stream{
		ctx:     testCtx(t),
		body:    io.NopCloser(strings.NewReader("")),
		reader:  strings.NewReader("data: {\"usage\":1}\n\ndata: " + strings.Repeat("x", 11<<20) + "\n\n"),
		stop:    func() {},
		counted: true,
		framer:  &providerUtils.SSEFramer{},
		observe: func([]byte) *schemas.RelayUsage { return &schemas.RelayUsage{Cost: 1} },
		finish: func(_ *schemas.BifrostRelayResponse, err *schemas.BifrostError) {
			finished, gotErr = true, err
		},
	}
	buf := make([]byte, 64<<10)
	var err error
	for err == nil {
		_, err = s.Read(buf)
	}
	require.ErrorIs(t, err, bufio.ErrTooLong)
	require.True(t, finished)
	require.NotNil(t, gotErr)
	assert.True(t, gotErr.IsBifrostError, "classified as the LLM scanner's error is")
	assert.Contains(t, gotErr.Error.Message, "Error reading stream")
	assert.ErrorIs(t, gotErr.Error.Error, bufio.ErrTooLong)
}

// TestStreamUsagePanicAtEOFIsAnInternalError pins the same for the last event, which only completes
// when the upstream ends (no blank line after it) and is read when the framer is flushed: the panic
// does not escape Read, where the gateway would recover it as a cancellation.
func TestStreamUsagePanicAtEOFIsAnInternalError(t *testing.T) {
	var gotErr *schemas.BifrostError
	finished := false
	s := &Stream{
		ctx:     testCtx(t),
		body:    io.NopCloser(strings.NewReader("")),
		reader:  strings.NewReader("data: {\"usage\":1}"),
		stop:    func() {},
		counted: true,
		framer:  &providerUtils.SSEFramer{},
		observe: func([]byte) *schemas.RelayUsage { panic("bad usage mapping") },
		finish: func(_ *schemas.BifrostRelayResponse, err *schemas.BifrostError) {
			finished, gotErr = true, err
		},
	}
	buf := make([]byte, 64)
	var err error
	require.NotPanics(t, func() {
		for err == nil {
			_, err = s.Read(buf)
		}
	})
	require.Error(t, err)
	require.True(t, finished)
	require.NotNil(t, gotErr)
	assert.Equal(t, schemas.ErrorTypeBifrostInternal, schemas.ClassifyErrorType(gotErr, schemas.RelayRequest))
}
