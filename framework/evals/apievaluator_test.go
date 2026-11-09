package evals

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// scorerServer is a stand-in for a customer's scoring endpoint.
func scorerServer(t *testing.T, status int, reply string, hold time.Duration) (*httptest.Server, *[]capturedRequest, *atomic.Int32) {
	t.Helper()
	var mu sync.Mutex
	var captured []capturedRequest
	var inFlight, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		captured = append(captured, capturedRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body), header: r.Header.Clone()})
		mu.Unlock()
		time.Sleep(hold)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(server.Close)
	return server, &captured, &peak
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

	require.Len(t, *captured, 1)
	req := (*captured)[0]
	assert.Equal(t, "POST", req.method)
	assert.Equal(t, "/score/run 7/a", req.path, "a value in the path is escaped, so it cannot add a query or change the host")
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
	assert.Empty(t, *captured, "the endpoint is not called without its inputs")

	ok := engine.Run(context.Background(), def, Input{Variables: map[string]string{"output": "x"}})
	require.Equal(t, ScoreStatusOK, ok.Score.Status, ok.Reasoning)
	assert.JSONEq(t, `{"a":"x","b":""}`, (*captured)[0].body, "an optional variable with no value renders empty")
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

// A response body is read up to a limit, so a hostile endpoint cannot exhaust memory.
func TestAPIEvaluator_LimitsResponseSize(t *testing.T) {
	server, _, _ := scorerServer(t, http.StatusOK, `{"score": 1, "pad": "`+string(make([]byte, 4096))+`"}`, 0)
	result := newAPIEngine(APIOptions{Client: server.Client(), MaxResponseBytes: 512}).Run(context.Background(),
		apiDefinition(t, "", map[string]any{"method": "GET", "url": server.URL, "output_mapping": map[string]any{"score": "score"}}), Input{})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "512")
}

// The evaluator's concurrency caps calls in flight to the customer's endpoint.
func TestAPIEvaluator_RespectsConcurrency(t *testing.T) {
	server, captured, peak := scorerServer(t, http.StatusOK, `{"score": 1}`, 15*time.Millisecond)
	engine := newAPIEngine(APIOptions{Client: server.Client()})
	def := apiDefinition(t, "api-capped", map[string]any{
		"method": "GET", "url": server.URL, "concurrency": 2, "output_mapping": map[string]any{"score": "score"},
	})
	defs := make([]Definition, 10)
	for i := range defs {
		defs[i] = def
	}
	for _, result := range engine.RunMany(context.Background(), defs, Input{}) {
		require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	}
	assert.Len(t, *captured, 10)
	assert.LessOrEqual(t, peak.Load(), int32(2))
	assert.Greater(t, peak.Load(), int32(1))
}

// The default client refuses private and loopback addresses, which is where an
// attacker-supplied URL would point to reach internal services.
func TestAPIEvaluator_DefaultClientBlocksPrivateAddresses(t *testing.T) {
	server, captured, _ := scorerServer(t, http.StatusOK, `{"score": 1}`, 0)
	result := newAPIEngine(APIOptions{}).Run(context.Background(),
		apiDefinition(t, "", map[string]any{"method": "GET", "url": server.URL, "output_mapping": map[string]any{"score": "score"}}), Input{})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Empty(t, *captured, "the loopback test server was never reached")
}
