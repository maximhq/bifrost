package evals

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedRequest struct {
	method, path, query, body string
	header                    http.Header
}

// scorerServer is a stand-in for a customer's scoring endpoint. captured returns a copy of
// the requests received so far, read under the lock the handler appends under.
func scorerServer(t *testing.T, status int, reply string, hold time.Duration) (server *httptest.Server, captured func() []capturedRequest, peak *atomic.Int32) {
	t.Helper()
	var mu sync.Mutex
	var requests []capturedRequest
	var inFlight atomic.Int32
	peak = &atomic.Int32{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := inFlight.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		defer inFlight.Add(-1)
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, capturedRequest{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, body: string(body), header: r.Header.Clone()})
		mu.Unlock()
		time.Sleep(hold)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(server.Close)
	captured = func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(requests)
	}
	return server, captured, peak
}

func newAPIEngine(opts APIOptions) *Engine {
	engine := NewEngine(EngineOptions{})
	RegisterAPIEvaluator(engine, opts)
	return engine
}

func apiDefinition(t *testing.T, id string, config map[string]any) Definition {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	return Definition{ID: id, Type: EvaluatorTypeAPI, Version: 1, Config: raw}
}

func TestAPIEvaluator_SendsTemplatedRequestAndMapsScore(t *testing.T) {
	server, captured, _ := scorerServer(t, http.StatusOK, `{"data":{"score":0.92,"why":"close match"}}`, 0)
	engine := newAPIEngine(APIOptions{Client: server.Client()})

	result := engine.Run(context.Background(), apiDefinition(t, "api-1", map[string]any{
		"method":         "post",
		"url":            server.URL + "/score/{{run}}",
		"headers":        []map[string]any{{"key": "Authorization", "value": "Bearer secret-token"}, {"key": "X-Trace", "value": "{{run}}"}},
		"params":         []map[string]any{{"key": "q", "value": "{{input}}"}},
		"payload":        `{"text":"{{output}}","expected":"{{expected_output}}"}`,
		"output_mapping": map[string]any{"score": "data.score", "reasoning": "data.why"},
	}), Input{Variables: map[string]string{
		"run":             "run 7/a",
		"input":           "what & why?",
		"output":          "she said \"hi\"\nthen left",
		"expected_output": "hi",
	}})

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, NumberScore(0.92), result.Score)
	assert.Equal(t, "close match", result.Reasoning)
	require.NotNil(t, result.Pass)
	assert.True(t, *result.Pass, "an API evaluator passes at 0.8 by default")

	require.Len(t, captured(), 1)
	req := captured()[0]
	assert.Equal(t, "POST", req.method)
	assert.Equal(t, "/score/run%207%2Fa", req.path, "a value in the path is escaped, slash included, so it cannot add a segment or a query or change the host")
	assert.Equal(t, "q=what+%26+why%3F", req.query)
	assert.Equal(t, "Bearer secret-token", req.header.Get("Authorization"))
	assert.Equal(t, "run 7/a", req.header.Get("X-Trace"))
	assert.Equal(t, "application/json", req.header.Get("Content-Type"))
	assert.JSONEq(t, `{"text":"she said \"hi\"\nthen left","expected":"hi"}`, req.body, "values are JSON-escaped so quotes and newlines cannot break the payload")
}

func TestAPIEvaluator_ScoreTypes(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  Score
	}{
		{"number", `{"score": 1}`, NumberScore(1)},
		{"boolean", `{"score": true}`, BoolScore(true)},
		{"string", `{"score": "good"}`, StringScore("good")},
		{"nested array element", `{"score": {"skip": true}, "results": [{"score": 0.5}]}`, NumberScore(0.5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := scorerServer(t, http.StatusOK, tc.reply, 0)
			path := "score"
			if tc.name == "nested array element" {
				path = "results.0.score"
			}
			result := newAPIEngine(APIOptions{Client: server.Client()}).Run(context.Background(), apiDefinition(t, "", map[string]any{
				"method": "GET", "url": server.URL, "output_mapping": map[string]any{"score": path},
				"pass_criteria": map[string]any{
					"entry_level": map[string]any{"name": "score", "operator": "!=", "value": "never"},
					"run_level":   map[string]any{"name": "queries_passed", "operator": ">=", "value": 80},
				},
			}), Input{})
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, tc.want, result.Score)
		})
	}
}

func TestAPIEvaluator_VariablesInTemplatesAreRequired(t *testing.T) {
	server, captured, _ := scorerServer(t, http.StatusOK, `{"score": 1}`, 0)
	engine := newAPIEngine(APIOptions{Client: server.Client()})
	def := apiDefinition(t, "", map[string]any{
		"method": "POST", "url": server.URL, "payload": `{"a":"{{output}}","b":"{{context}}"}`,
		"optional_variables": map[string]string{"context": "retrieved text"},
		"output_mapping":     map[string]any{"score": "score"},
	})

	missing := engine.Run(context.Background(), def, Input{})
	assert.Equal(t, ScoreStatusNA, missing.Score.Status)
	assert.Equal(t, "Missing variables: output", missing.Reasoning)
	assert.Empty(t, captured(), "the endpoint is not called without its inputs")

	ok := engine.Run(context.Background(), def, Input{Variables: map[string]string{"output": "x"}})
	require.Equal(t, ScoreStatusOK, ok.Score.Status, ok.Reasoning)
	assert.JSONEq(t, `{"a":"x","b":""}`, captured()[0].body, "an optional variable with no value renders empty")
}

func TestAPIEvaluator_Failures(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		reply   string
		hold    time.Duration
		timeout int
		wantErr string
	}{
		{"error status", http.StatusInternalServerError, `{"error":"boom"}`, 0, 0, "500"},
		{"client error status", http.StatusUnauthorized, `denied`, 0, 0, "401"},
		{"score path missing", http.StatusOK, `{"other": 1}`, 0, 0, "data.score"},
		{"score is an object", http.StatusOK, `{"data":{"score":{"a":1}}}`, 0, 0, "boolean, a number or a string"},
		{"body is not json", http.StatusOK, `<html>hi</html>`, 0, 0, "JSON"},
		{"timeout", http.StatusOK, `{"data":{"score":1}}`, 300 * time.Millisecond, 50, "timed out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := scorerServer(t, tc.status, tc.reply, tc.hold)
			config := map[string]any{"method": "GET", "url": server.URL, "output_mapping": map[string]any{"score": "data.score"}}
			if tc.timeout > 0 {
				config["timeout_ms"] = tc.timeout
			}
			result := newAPIEngine(APIOptions{Client: server.Client()}).Run(context.Background(), apiDefinition(t, "", config), Input{})
			assert.Equal(t, ScoreStatusError, result.Score.Status)
			assert.Contains(t, result.Reasoning, tc.wantErr)
		})
	}
}

// lateBodyReader answers before reading the request body, the way net/http can reply
// to an early 413 while its transport is still writing the body.
type lateBodyReader struct {
	mu  sync.Mutex
	req *http.Request
}

func (rt *lateBodyReader) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.req = req
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusRequestEntityTooLarge,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`too large`)),
		Request:    req,
	}, nil
}

// The request body must not alias the pooled render buffer: the transport may still be
// reading it after Evaluate returns the buffer to the pool.
func TestAPIEvaluator_RequestBodyOutlivesPooledBuffer(t *testing.T) {
	rt := &lateBodyReader{}
	payload := `{"text":"{{output}}"}`
	result := newAPIEngine(APIOptions{Client: &http.Client{Transport: rt}}).Run(context.Background(), apiDefinition(t, "", map[string]any{
		"method": "POST", "url": "http://scorer.example/score", "payload": payload,
		"output_mapping": map[string]any{"score": "score"},
	}), Input{Variables: map[string]string{"output": "the rendered answer"}})
	require.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "413")

	want := `{"text":"the rendered answer"}`
	junk := strings.Repeat("X", len(want))
	for range 4 {
		buf := getBuffer()
		buf.WriteString(junk)
		defer putBuffer(buf)
	}
	rt.mu.Lock()
	req := rt.req
	rt.mu.Unlock()
	require.NotNil(t, req)
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, want, string(body))
	require.NotNil(t, req.GetBody)
	again, err := req.GetBody()
	require.NoError(t, err)
	replay, err := io.ReadAll(again)
	require.NoError(t, err)
	assert.Equal(t, want, string(replay))
}

// A response body is read up to a limit, so a hostile endpoint cannot exhaust memory.
func TestAPIEvaluator_LimitsResponseSize(t *testing.T) {
	server, _, _ := scorerServer(t, http.StatusOK, `{"score": 1, "pad": "`+string(make([]byte, 4096))+`"}`, 0)
	result := newAPIEngine(APIOptions{Client: server.Client(), MaxResponseBytes: 512}).Run(context.Background(),
		apiDefinition(t, "", map[string]any{"method": "GET", "url": server.URL, "output_mapping": map[string]any{"score": "score"}}), Input{})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "512")
}

// The evaluator's concurrency caps calls in flight to the customer's endpoint. The first
// two calls are held until both have entered, so the test does not depend on timing:
// two run at once, and a third waits for a slot.
func TestAPIEvaluator_RespectsConcurrency(t *testing.T) {
	var entered, inFlight, peak, served atomic.Int32
	var timedOut atomic.Bool
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		defer inFlight.Add(-1)
		if entered.Add(1) <= 2 {
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				timedOut.Store(true)
			}
		}
		served.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"score": 1}`)
	}))
	t.Cleanup(server.Close)
	engine := newAPIEngine(APIOptions{Client: server.Client()})
	def := apiDefinition(t, "api-capped", map[string]any{
		"method": "GET", "url": server.URL, "concurrency": 2, "output_mapping": map[string]any{"score": "score"},
	})
	defs := make([]Definition, 10)
	for i := range defs {
		defs[i] = def
	}
	results := make(chan []Result, 1)
	go func() { results <- engine.RunMany(context.Background(), defs, Input{}) }()

	require.Eventually(t, func() bool { return entered.Load() == 2 }, 5*time.Second, time.Millisecond, "two calls never ran at once")
	// While both slots are held, no third call reaches the endpoint.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(2), entered.Load(), "a third call entered while two were held")
	assert.LessOrEqual(t, inFlight.Load(), int32(2))
	close(release)

	for _, result := range <-results {
		require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	}
	assert.False(t, timedOut.Load(), "the held calls were released by the test, not by the timeout")
	assert.Equal(t, int32(10), served.Load())
	assert.LessOrEqual(t, peak.Load(), int32(2))
	assert.Equal(t, int32(2), peak.Load())
}

// A changed concurrency applies to the calls already in flight: a call on the old
// version still counts against the new cap, so lowering it holds new calls back.
func TestAPIEvaluator_ConcurrencyAcrossVersions(t *testing.T) {
	limiter := &apiLimiter{slots: make(map[string]*apiSlots)}
	ctx := context.Background()
	releaseV1, err := limiter.acquire(ctx, "api-1", 1)
	require.NoError(t, err)
	releaseV2, err := limiter.acquire(ctx, "api-1", 2)
	require.NoError(t, err, "a cap of 2 has room beside the one call in flight")

	acquired := make(chan func(), 1)
	go func() {
		release, err := limiter.acquire(ctx, "api-1", 1)
		if err == nil {
			acquired <- release
		}
	}()
	select {
	case <-acquired:
		t.Fatal("a call with a cap of 1 ran while two calls were in flight")
	case <-time.After(100 * time.Millisecond):
	}
	releaseV1()
	select {
	case <-acquired:
		t.Fatal("a call with a cap of 1 ran while one call was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	releaseV2()
	select {
	case release := <-acquired:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting call never ran after both calls returned")
	}

	cancelled, cancel := context.WithCancel(ctx)
	hold, err := limiter.acquire(ctx, "api-1", 1)
	require.NoError(t, err)
	cancel()
	_, err = limiter.acquire(cancelled, "api-1", 1)
	assert.ErrorIs(t, err, context.Canceled, "a waiting call gives up when its context ends")
	hold()
}

// The limiter keeps an entry only while an evaluator has calls in flight or waiting, so
// the map does not grow with every evaluator a process ever runs.
func TestAPIEvaluator_LimiterDropsIdleEntries(t *testing.T) {
	limiter := &apiLimiter{slots: make(map[string]*apiSlots)}
	ctx := context.Background()
	entries := func() int {
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		return len(limiter.slots)
	}

	release, err := limiter.acquire(ctx, "ev", 1)
	require.NoError(t, err)
	release()
	assert.Zero(t, entries(), "a released evaluator leaves no entry")

	// A waiter that gives up, with the holder released after it, leaves none either.
	hold, err := limiter.acquire(ctx, "ev", 1)
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(ctx)
	gaveUp := make(chan error, 1)
	go func() {
		_, err := limiter.acquire(cancelled, "ev", 1)
		gaveUp <- err
	}()
	require.Eventually(t, func() bool {
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		return limiter.slots["ev"] != nil && limiter.slots["ev"].waiting == 1
	}, 2*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-gaveUp, context.Canceled)
	hold()
	assert.Zero(t, entries(), "nothing in flight and nothing waiting")

	// While a waiter is parked the entry stays, and the waiter gets the slot on release.
	hold, err = limiter.acquire(ctx, "ev", 1)
	require.NoError(t, err)
	acquired := make(chan func(), 1)
	go func() {
		release, err := limiter.acquire(ctx, "ev", 1)
		if err == nil {
			acquired <- release
		}
	}()
	require.Eventually(t, func() bool {
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		return limiter.slots["ev"] != nil && limiter.slots["ev"].waiting == 1
	}, 2*time.Second, time.Millisecond)
	hold()
	assert.Equal(t, 1, entries(), "the entry stays while a waiter is parked")
	select {
	case release := <-acquired:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never got the slot")
	}
	assert.Zero(t, entries())
}

// The default client refuses private and loopback addresses, which is where an
// attacker-supplied URL would point to reach internal services.
func TestAPIEvaluator_DefaultClientBlocksPrivateAddresses(t *testing.T) {
	server, captured, _ := scorerServer(t, http.StatusOK, `{"score": 1}`, 0)
	result := newAPIEngine(APIOptions{}).Run(context.Background(),
		apiDefinition(t, "", map[string]any{"method": "GET", "url": server.URL, "output_mapping": map[string]any{"score": "score"}}), Input{})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Empty(t, captured(), "the loopback test server was never reached")
}

// A redirect to another host must not carry the evaluator's headers. Go forwards custom
// headers such as X-API-Key on a redirect, so a scoring endpoint that redirects, or is
// taken over, would hand the customer's secret to whatever host it names.
func TestAPIEvaluator_DoesNotFollowCrossHostRedirects(t *testing.T) {
	target, captured, _ := scorerServer(t, http.StatusOK, `{"score": 1}`, 0)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	result := newAPIEngine(APIOptions{Client: &http.Client{}}).Run(context.Background(), apiDefinition(t, "api-redirect", map[string]any{
		"method":         "GET",
		"url":            redirector.URL + "/score",
		"headers":        []map[string]any{{"key": "X-API-Key", "value": "secret-key"}},
		"output_mapping": map[string]any{"score": "score"},
	}), Input{})

	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "redirect")
	assert.Empty(t, captured(), "the other host never receives the request or its secret header")
}
