package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCtx(t *testing.T) *schemas.BifrostContext {
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

func newTestManager(t *testing.T, cfg *schemas.RelayConfig) (*Manager, *fakePipeline) {
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

func TestServeReadsModelUnderOperatorContentType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer upstream.Close()
	cfg := bearerConfig("llm", upstream.URL)
	cfg.SkipRouteCheck = true
	cfg.ExtraHeaders = map[string]schemas.SecretVar{"Content-Type": *schemas.NewSecretVar("application/json")}
	cfg.Usage = &schemas.RelayUsageConfig{ModelPath: "model"}
	m, pipeline := newTestManager(t, cfg)

	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "llm", Method: "POST", Path: "/chat", Headers: map[string]string{"content-type": "text/plain"}, Body: []byte(`{"model":"gpt-x"}`)})
	require.Nil(t, bErr)
	require.Len(t, pipeline.pre, 1)
	assert.Equal(t, "gpt-x", pipeline.pre[0].Model, "the upstream receives JSON, so the model is read as JSON")
}
