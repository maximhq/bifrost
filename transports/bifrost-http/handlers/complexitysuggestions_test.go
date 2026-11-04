package handlers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggestrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// suggestionManagerFake is a routing manager that keeps suggestions in a real
// log store.
type suggestionManagerFake struct {
	mockRoutingManager
	store    logstore.LogStore
	texts    map[string]string
	runErr   error
	tested   []suggestrun.TestExample
	testErr  error
	runCalls int
}

func (m *suggestionManagerFake) ComplexitySuggestionStore() logstore.ComplexitySuggestionStore {
	if m.store == nil {
		return nil
	}
	return m.store
}

func (m *suggestionManagerFake) ComplexitySuggestionTexts(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if text, ok := m.texts[id]; ok {
			out[id] = text
		}
	}
	return out, nil
}

func (m *suggestionManagerFake) StartComplexitySuggestionRun(context.Context) (string, error) {
	m.runCalls++
	return "run-now", m.runErr
}

func (m *suggestionManagerFake) TestComplexitySuggestionPhrases(_ context.Context, phrases []complexity.SuggestionPhrase, examples []suggestrun.TestExample) (suggestrun.PhraseTest, error) {
	m.tested = examples
	if m.testErr != nil {
		return suggestrun.PhraseTest{}, m.testErr
	}
	return suggestrun.PhraseTest{Phrases: []suggestrun.TestedPhrase{{Tier: phrases[0].Tier, Text: phrases[0].Text}}, TakenOver: 1}, nil
}

// suggestionsConfigStore answers the analyzer config read the endpoints use
// for the schedule.
type suggestionsConfigStore struct {
	configstore.ConfigStore
	config *configstore.ComplexityAnalyzerConfig
}

func (s suggestionsConfigStore) GetComplexityAnalyzerConfig(context.Context) (*configstore.ComplexityAnalyzerConfig, error) {
	return s.config, nil
}

func suggestionHandler(t *testing.T) (*router.Router, *suggestionManagerFake) {
	t.Helper()
	ctx := context.Background()
	ls, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(ctx) })
	manager := &suggestionManagerFake{store: ls, texts: map[string]string{"log-1": "where is my refund for order 12"}}
	config := &configstore.ComplexityAnalyzerConfig{Suggestions: &configstore.ComplexitySuggestionsConfig{Enabled: true}}
	h, err := NewRoutingHandler(manager, suggestionsConfigStore{config: config})
	require.NoError(t, err)
	r := router.New()
	h.RegisterRoutes(r)

	open := logstore.ComplexitySuggestion{ID: "s-open", Tier: "SIMPLE", Category: "uncovered", RequestCount: 40, Centroid: logstore.EncodeWarpVector([]float32{1, 0}), EmbeddingSignature: "ns"}
	require.NoError(t, open.SetPhrases([]string{"where is my refund"}))
	require.NoError(t, open.SetExampleLogIDs([]string{"log-1", "log-gone"}))
	dismissed := logstore.ComplexitySuggestion{ID: "s-dismissed", Status: logstore.ComplexitySuggestionStatusDismissed, Tier: "COMPLEX", Category: "misrouted", RequestCount: 9, EmbeddingSignature: "ns"}
	superseded := logstore.ComplexitySuggestion{ID: "s-old", Status: logstore.ComplexitySuggestionStatusSuperseded, Tier: "MEDIUM", Category: "weak", RequestCount: 3, EmbeddingSignature: "ns"}
	require.NoError(t, ls.UpsertComplexitySuggestions(ctx, []logstore.ComplexitySuggestion{open, dismissed, superseded}))
	require.NoError(t, ls.UpsertComplexitySuggestionRun(ctx, &logstore.ComplexitySuggestionRun{ID: "run-1", Status: logstore.ComplexitySuggestionRunSucceeded, EmbeddingSignature: "ns", StartedAt: time.Now().Add(-time.Hour)}))
	return r, manager
}

func serve(t *testing.T, r *router.Router, method, uri, body string) (int, map[string]any) {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)
	if body != "" {
		req.SetBodyString(body)
	}
	// Init gives the context fasthttp's stand-in server: a bare RequestCtx
	// panics in Done, which the store's queries call.
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, nil, nil)
	r.Handler(ctx)
	var decoded map[string]any
	_ = json.Unmarshal(ctx.Response.Body(), &decoded)
	return ctx.Response.StatusCode(), decoded
}

func TestComplexitySuggestionsList(t *testing.T) {
	r, _ := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	suggestions := body["suggestions"].([]any)
	require.Len(t, suggestions, 1, "open only by default")
	first := suggestions[0].(map[string]any)
	assert.Equal(t, "s-open", first["id"])
	assert.Equal(t, []any{"where is my refund"}, first["phrases"])
	assert.NotContains(t, first, "centroid")
	assert.Equal(t, map[string]any{"open": 1.0, "dismissed": 1.0, "superseded": 1.0}, body["counts"])
	assert.Equal(t, true, body["enabled"])
	assert.NotEmpty(t, body["next_run_at"])
	assert.Equal(t, "run-1", body["last_run"].(map[string]any)["id"])

	code, body = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?status=dismissed,superseded", "")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Len(t, body["suggestions"].([]any), 2)
	code, _ = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?status=maybe", "")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
}

func TestComplexitySuggestionsDetailReadsVisibleExamples(t *testing.T) {
	r, _ := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/s-open", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, []any{
		map[string]any{"log_id": "log-1", "text": "where is my refund for order 12"},
		map[string]any{"log_id": "log-gone"},
	}, body["examples"], "a log the caller cannot read gives no text")
	code, _ = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/missing", "")
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

// A decision sticks across runs because the row keeps its centroid when its
// status changes; only the user's transitions are allowed.
func TestComplexitySuggestionsStatusTransitions(t *testing.T) {
	r, manager := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-open/status", `{"status":"accepted"}`)
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, "accepted", body["status"])
	assert.NotEmpty(t, body["resolved_at"])
	rows, err := manager.store.ListComplexitySuggestions(context.Background(), logstore.ComplexitySuggestionFilter{IDs: []string{"s-open"}})
	require.NoError(t, err)
	assert.NotEmpty(t, rows[0].Centroid, "the centroid is kept for the next run's match")

	code, _ = serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-open/status", `{"status":"dismissed"}`)
	assert.Equal(t, fasthttp.StatusConflict, code, "accepted to dismissed goes through open")
	code, body = serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-open/status", `{"status":"open"}`)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.NotContains(t, body, "resolved_at", "undoing clears when it was resolved")
	code, _ = serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-old/status", `{"status":"accepted"}`)
	assert.Equal(t, fasthttp.StatusConflict, code, "a superseded suggestion is stale")
	code, _ = serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-open/status", `{"status":"superseded"}`)
	assert.Equal(t, fasthttp.StatusConflict, code, "superseding is the runs' call")
}

func TestComplexitySuggestionsTestPhrases(t *testing.T) {
	r, manager := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/test",
		`{"phrases":[{"tier":"simple","text":"where is my refund"}],"suggestion_id":"s-open"}`)
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, []suggestrun.TestExample{{LogID: "log-1", Text: "where is my refund for order 12"}}, manager.tested,
		"the suggestion's readable examples are routed")
	assert.Equal(t, 1.0, body["taken_over"])
	assert.Equal(t, "SIMPLE", body["phrases"].([]any)[0].(map[string]any)["tier"], "the tier is read case-insensitively")

	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/test", `{"phrases":[]}`)
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	manager.testErr = complexity.ErrNoServingGeneration
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/test", `{"phrases":[{"tier":"SIMPLE","text":"x"}]}`)
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code)
}

func TestComplexitySuggestionsRuns(t *testing.T) {
	r, manager := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs?limit=5", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Len(t, body["runs"].([]any), 1)
	assert.NotEmpty(t, body["next_run_at"])
	code, _ = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs?limit=0", "")
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	code, body = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", "")
	require.Equal(t, fasthttp.StatusAccepted, code, body)
	assert.Equal(t, "run-now", body["run_id"])
	manager.runErr = routing.ErrComplexitySuggestionRunInFlight
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", "")
	assert.Equal(t, fasthttp.StatusConflict, code)
	assert.Equal(t, 2, manager.runCalls)
}

func TestComplexitySuggestionsNeedALogStore(t *testing.T) {
	manager := &suggestionManagerFake{}
	h, err := NewRoutingHandler(manager, suggestionsConfigStore{})
	require.NoError(t, err)
	r := router.New()
	h.RegisterRoutes(r)
	code, _ := serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions", "")
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code)

	h, err = NewRoutingHandler(&mockRoutingManager{}, suggestionsConfigStore{})
	require.NoError(t, err)
	r = router.New()
	h.RegisterRoutes(r)
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", "")
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code, "a manager without suggestions")
}
