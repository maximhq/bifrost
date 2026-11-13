package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
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
	// pluginMissing is a deployment whose routing plugin is not loaded.
	pluginMissing bool
	testCalls     int
}

func (m *suggestionManagerFake) RoutingPluginLoaded() bool { return !m.pluginMissing }

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
	m.testCalls++
	if m.testErr != nil {
		return suggestrun.PhraseTest{}, m.testErr
	}
	return suggestrun.PhraseTest{Phrases: []suggestrun.TestedPhrase{{Tier: phrases[0].Tier, Text: phrases[0].Text}}, TakenOver: 1}, nil
}

// suggestionsConfigStore answers the analyzer config read the endpoints use
// for the schedule.
type suggestionsConfigStore struct {
	configstore.ConfigStore
	config   *configstore.ComplexityAnalyzerConfig
	schedule *tables.TableComplexitySuggestionSchedule
}

func (s suggestionsConfigStore) GetComplexityAnalyzerConfig(context.Context) (*configstore.ComplexityAnalyzerConfig, error) {
	return s.config, nil
}

func (s suggestionsConfigStore) GetComplexitySuggestionSchedule(context.Context, string) (*tables.TableComplexitySuggestionSchedule, error) {
	return s.schedule, nil
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

	// A status that does not exist is a bad request, not a refused transition.
	for _, status := range []string{`"approved"`, `""`, `"ACCEPTED"`} {
		code, _ = serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-open/status", `{"status":`+status+`}`)
		assert.Equal(t, fasthttp.StatusBadRequest, code, status)
	}
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
	manager.runErr = complexity.ErrNoServingGeneration
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", "")
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code, "no warmed phrase bank to suggest against")
	assert.Equal(t, 3, manager.runCalls)
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

// Without the routing plugin every suggestion endpoint answers 503, as the
// API documents, rather than serving stored rows as if suggestions worked or
// reporting suggestions as switched off.
func TestComplexitySuggestionsWithoutTheRoutingPlugin(t *testing.T) {
	r, manager := suggestionHandler(t)
	manager.pluginMissing = true
	for _, c := range []struct{ method, uri, body string }{
		{fasthttp.MethodGet, "/api/routing/complexity-suggestions", ""},
		{fasthttp.MethodGet, "/api/routing/complexity-suggestions/s-open", ""},
		{fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-open/status", `{"status":"accepted"}`},
		{fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs", ""},
		{fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", ""},
	} {
		code, _ := serve(t, r, c.method, c.uri, c.body)
		assert.Equal(t, fasthttp.StatusServiceUnavailable, code, "%s %s", c.method, c.uri)
	}
	assert.Zero(t, manager.runCalls)

	// The plugin going away between the check and the run is a 503 too.
	manager.pluginMissing = false
	manager.runErr = fmt.Errorf("%w: not registered", ErrRoutingPluginUnavailable)
	code, _ := serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", "")
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code)
	manager.runErr = routing.ErrComplexitySuggestionsOff
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", "")
	assert.Equal(t, fasthttp.StatusConflict, code, "suggestions switched off stay a conflict")
}

// The list is filtered and paged in the query; the counts cover every status
// and the total the selected ones, whatever page was served.
func TestComplexitySuggestionsListPages(t *testing.T) {
	r, _ := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?status=open,dismissed,superseded&limit=2", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	page := body["suggestions"].([]any)
	require.Len(t, page, 2)
	assert.Equal(t, "s-open", page[0].(map[string]any)["id"], "largest first")
	assert.Equal(t, 3.0, body["total"])
	assert.Equal(t, map[string]any{"open": 1.0, "dismissed": 1.0, "superseded": 1.0}, body["counts"])

	code, body = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?status=open,dismissed,superseded&limit=2&offset=2", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	require.Len(t, body["suggestions"].([]any), 1)
	assert.Equal(t, "s-old", body["suggestions"].([]any)[0].(map[string]any)["id"])

	// A status asked for twice selects its rows once, so it counts once.
	code, body = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?status=open,open,dismissed", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	require.Len(t, body["suggestions"].([]any), 2)
	assert.Equal(t, 2.0, body["total"], "the total matches the rows the list selects")

	for _, q := range []string{"limit=0", "limit=501", "offset=-1", "limit=x"} {
		code, _ = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?"+q, "")
		assert.Equal(t, fasthttp.StatusBadRequest, code, q)
	}
}

// racingStore lets another write land between the handler's read of a
// suggestion and its decision, as a run superseding it would.
type racingStore struct {
	logstore.LogStore
	afterRead func()
}

func (s *racingStore) ListComplexitySuggestions(ctx context.Context, filter logstore.ComplexitySuggestionFilter) ([]logstore.ComplexitySuggestion, error) {
	rows, err := s.LogStore.ListComplexitySuggestions(ctx, filter)
	if s.afterRead != nil {
		after := s.afterRead
		s.afterRead = nil
		after()
	}
	return rows, err
}

// A decision applies only to the suggestion as it was read: one that a run
// superseded in between is refused, and the run's status stands.
func TestComplexitySuggestionsStatusConflictsWithAChangeSinceRead(t *testing.T) {
	r, manager := suggestionHandler(t)
	ls := manager.store
	racing := &racingStore{LogStore: ls}
	manager.store = racing
	racing.afterRead = func() {
		rows, err := ls.ListComplexitySuggestions(context.Background(), logstore.ComplexitySuggestionFilter{IDs: []string{"s-open"}})
		require.NoError(t, err)
		rows[0].Status = logstore.ComplexitySuggestionStatusSuperseded
		require.NoError(t, ls.UpsertComplexitySuggestions(context.Background(), rows))
	}
	code, _ := serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-open/status", `{"status":"accepted"}`)
	assert.Equal(t, fasthttp.StatusConflict, code)
	rows, err := ls.ListComplexitySuggestions(context.Background(), logstore.ComplexitySuggestionFilter{IDs: []string{"s-open"}})
	require.NoError(t, err)
	assert.Equal(t, logstore.ComplexitySuggestionStatusSuperseded, rows[0].Status, "the run's change is not overwritten")
}

// The caller's own log IDs are routed first, and logs that cannot be read do
// not use up the example limit; a phrase longer than the bank allows is
// refused before anything is embedded.
func TestComplexitySuggestionsTestPhrasesExamplesAndLimits(t *testing.T) {
	r, manager := suggestionHandler(t)
	manager.texts["log-2"] = "cancel my subscription"
	// More unreadable logs than the example cap, then a readable one: the cap
	// counts readable examples, so log-2 is still reached.
	ids := make([]string, 0, suggestrun.MaxTestExamples+9)
	for i := range suggestrun.MaxTestExamples + 8 {
		ids = append(ids, fmt.Sprintf("gone-%d", i))
	}
	ids = append(ids, "log-2")
	payload, err := json.Marshal(map[string]any{
		"phrases":       []map[string]string{{"tier": "SIMPLE", "text": "where is my refund"}},
		"suggestion_id": "s-open",
		"log_ids":       ids,
	})
	require.NoError(t, err)
	code, body := serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/test", string(payload))
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, []suggestrun.TestExample{
		{LogID: "log-2", Text: "cancel my subscription"},
		{LogID: "log-1", Text: "where is my refund for order 12"},
	}, manager.tested, "unreadable logs leave room for readable ones, the caller's first")

	tooMany := make([]string, maxComplexitySuggestionTestLogIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("log-%d", i)
	}
	payload, err = json.Marshal(map[string]any{"phrases": []map[string]string{{"tier": "SIMPLE", "text": "x"}}, "log_ids": tooMany})
	require.NoError(t, err)
	calls := manager.testCalls
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/test", string(payload))
	assert.Equal(t, fasthttp.StatusBadRequest, code, "the logs a test may read are bounded")
	assert.Equal(t, calls, manager.testCalls)

	calls = manager.testCalls
	long := strings.Repeat("a", configstore.MaxComplexitySemanticPhraseCharacters+1)
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/test", `{"phrases":[{"tier":"SIMPLE","text":"`+long+`"}]}`)
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.Equal(t, calls, manager.testCalls, "an oversized phrase is never embedded")
	atLimit := strings.Repeat("a", configstore.MaxComplexitySemanticPhraseCharacters)
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/test", `{"phrases":[{"tier":"SIMPLE","text":"  `+atLimit+`  "}]}`)
	assert.Equal(t, fasthttp.StatusOK, code, "the limit applies after trimming")
}
