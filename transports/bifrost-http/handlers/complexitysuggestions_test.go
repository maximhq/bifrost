package handlers

import (
	"context"
	"encoding/json"
	"errors"
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
	store       logstore.LogStore
	texts       map[string]string
	runErr      error
	estimateErr error
	tested      []suggestrun.TestExample
	testErr     error
	runCalls    int
	// pluginMissing is a deployment whose routing plugin is not loaded.
	pluginMissing bool
	testCalls     int
	// windows records the window of each run started; version is what the
	// default router serves.
	windows []time.Duration
	version string
	// cancelled is the run ID cancelled; cancelErr answers the cancel.
	cancelled string
	cancelErr error
	// draft is the bank an impact replay was given; impact and impactErr
	// answer it.
	draft []complexity.SuggestionPhrase
	// draftFloor is the floor the last replay judged the draft at.
	draftFloor *float64
	impact     ComplexitySuggestionImpactReport
	impactErr  error
}

var _ ComplexitySuggestionManager = (*suggestionManagerFake)(nil)

func (m *suggestionManagerFake) CancelComplexitySuggestionRun(_ context.Context, routerID, runID string) (logstore.ComplexitySuggestionRun, error) {
	m.cancelled = runID
	if m.cancelErr != nil {
		return logstore.ComplexitySuggestionRun{}, m.cancelErr
	}
	return logstore.ComplexitySuggestionRun{ID: runID, RouterID: routerID, Status: logstore.ComplexitySuggestionRunCancelled}, nil
}

func (m *suggestionManagerFake) ComplexitySuggestionImpact(_ context.Context, _ string, window time.Duration, draft []complexity.SuggestionPhrase, draftMinSimilarity *float64) (ComplexitySuggestionImpactReport, error) {
	m.draft = draft
	m.draftFloor = draftMinSimilarity
	m.windows = append(m.windows, window)
	return m.impact, m.impactErr
}

func (m *suggestionManagerFake) ComplexitySuggestionRouter(routerID string) (string, bool) {
	return m.version, routerID == defaultComplexitySuggestionRouter
}

func (m *suggestionManagerFake) EstimateComplexitySuggestionRun(_ context.Context, _ string, window time.Duration) (suggestrun.Estimate, error) {
	if m.estimateErr != nil {
		return suggestrun.Estimate{}, m.estimateErr
	}
	if window > 7*24*time.Hour {
		return suggestrun.Estimate{}, routing.ErrComplexitySuggestionWindow
	}
	return suggestrun.Estimate{Requests: 900, Clustered: 900, MaxModelCalls: 300}, nil
}

func (m *suggestionManagerFake) RoutingPluginLoaded() bool { return !m.pluginMissing }

func (m *suggestionManagerFake) ComplexitySuggestionStore() logstore.ComplexitySuggestionStore {
	if m.store == nil {
		return nil
	}
	return m.store
}

func (m *suggestionManagerFake) ComplexitySuggestionTexts(_ context.Context, _ string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if text, ok := m.texts[id]; ok {
			out[id] = text
		}
	}
	return out, nil
}

func (m *suggestionManagerFake) StartComplexitySuggestionRun(_ context.Context, _ string, window time.Duration) (string, error) {
	m.runCalls++
	m.windows = append(m.windows, window)
	return "run-now", m.runErr
}

func (m *suggestionManagerFake) TestComplexitySuggestionPhrases(_ context.Context, _ string, phrases []complexity.SuggestionPhrase, examples []suggestrun.TestExample) (suggestrun.PhraseTest, error) {
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
	manager := &suggestionManagerFake{store: ls, texts: map[string]string{"log-1": "where is my refund for order 12"}, version: "bank-v2"}
	config := &configstore.ComplexityAnalyzerConfig{Suggestions: &configstore.ComplexitySuggestionsConfig{Enabled: true}}
	h, err := NewRoutingHandler(manager, suggestionsConfigStore{config: config})
	require.NoError(t, err)
	r := router.New()
	h.RegisterRoutes(r)

	open := logstore.ComplexitySuggestion{ID: "s-open", RouterID: "default", RouterVersion: "bank-v2", Tier: "SIMPLE", Category: "uncovered", RequestCount: 40, Centroid: logstore.EncodeWarpVector([]float32{1, 0}), EmbeddingSignature: "ns"}
	require.NoError(t, open.SetPhrases([]string{"where is my refund"}))
	require.NoError(t, open.SetExampleLogIDs([]string{"log-1", "log-gone"}))
	require.NoError(t, open.SetCurrentRouting(map[string]int64{"none": 31, "MEDIUM": 9}))
	dismissed := logstore.ComplexitySuggestion{ID: "s-dismissed", RouterID: "default", RouterVersion: "bank-v1", Status: logstore.ComplexitySuggestionStatusDismissed, Tier: "COMPLEX", Category: "misrouted", RequestCount: 9, EmbeddingSignature: "ns"}
	superseded := logstore.ComplexitySuggestion{ID: "s-old", RouterID: "default", Status: logstore.ComplexitySuggestionStatusSuperseded, Tier: "MEDIUM", Category: "weak", RequestCount: 3, EmbeddingSignature: "ns"}
	// Another router's suggestion, for checking that nothing here reaches it.
	elsewhere := logstore.ComplexitySuggestion{ID: "s-office", RouterID: "office", Tier: "SIMPLE", Category: "weak", RequestCount: 99, EmbeddingSignature: "ns"}
	require.NoError(t, ls.UpsertComplexitySuggestions(ctx, []logstore.ComplexitySuggestion{open, dismissed, superseded, elsewhere}))
	require.NoError(t, ls.UpsertComplexitySuggestionRun(ctx, &logstore.ComplexitySuggestionRun{ID: "run-office", RouterID: "office", Status: logstore.ComplexitySuggestionRunSucceeded, EmbeddingSignature: "ns", StartedAt: time.Now()}))
	require.NoError(t, ls.UpsertComplexitySuggestionRun(ctx, &logstore.ComplexitySuggestionRun{ID: "run-1", RouterID: "default", Status: logstore.ComplexitySuggestionRunSucceeded, EmbeddingSignature: "ns", StartedAt: time.Now().Add(-time.Hour)}))
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
	assert.Equal(t, map[string]any{"none": 31.0, "MEDIUM": 9.0}, first["current_routing"], "where the cluster routes today")
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
	manager.runErr = complexity.ErrNoCaptureStore
	code, body = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", "")
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code, "nothing captured to suggest from")
	assert.Contains(t, fmt.Sprint(body), "shared vector store", "the refusal says what is needed")
	assert.Equal(t, 4, manager.runCalls)
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
		{fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs/estimate?window=7d", ""},
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

// Every endpoint speaks for one router: the default when none is named, 404
// for one that does not exist, and another router's suggestions are neither
// listed nor reachable by ID.
func TestComplexitySuggestionsAreScopedToARouter(t *testing.T) {
	r, _ := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?status=open,dismissed,superseded", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, "default", body["router_id"])
	assert.Equal(t, "bank-v2", body["router_version"])
	ids := map[string]bool{}
	stale := map[string]bool{}
	for _, item := range body["suggestions"].([]any) {
		row := item.(map[string]any)
		ids[row["id"].(string)] = true
		stale[row["id"].(string)] = row["stale"].(bool)
	}
	assert.False(t, ids["s-office"], "another router's suggestion is not listed")
	assert.Equal(t, map[string]bool{"s-open": false, "s-dismissed": true, "s-old": false}, stale,
		"computed against an earlier bank is stale; no recorded version is not")
	assert.Equal(t, "run-1", body["last_run"].(map[string]any)["id"], "the last run is this router's")

	code, _ = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/s-office", "")
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = serve(t, r, fasthttp.MethodPut, "/api/routing/complexity-suggestions/s-office/status", `{"status":"dismissed"}`)
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions?router_id=office", "")
	assert.Equal(t, fasthttp.StatusNotFound, code, "a router that does not exist")
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs?router_id=office", "")
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

func TestComplexitySuggestionsRunWindowAndEstimate(t *testing.T) {
	r, manager := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs?window=24h", "")
	require.Equal(t, fasthttp.StatusAccepted, code, body)
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs?window=7d", "")
	require.Equal(t, fasthttp.StatusAccepted, code)
	assert.Equal(t, []time.Duration{24 * time.Hour, 7 * 24 * time.Hour}, manager.windows)
	for _, bad := range []string{"0d", "-1h", "soon"} {
		code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs?window="+bad, "")
		assert.Equal(t, fasthttp.StatusBadRequest, code, bad)
	}

	code, body = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs/estimate?window=7d", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, 900.0, body["requests"])
	assert.Equal(t, 300.0, body["max_model_calls"])
	code, _ = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs/estimate?window=30d", "")
	assert.Equal(t, fasthttp.StatusBadRequest, code, "a window past the lookback")
	manager.estimateErr = complexity.ErrNoCaptureStore
	code, body = serve(t, r, fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs/estimate?window=7d", "")
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code, "Chromem or no store: a 503 with the reason, not a 500")
	assert.Contains(t, fmt.Sprint(body), "shared vector store")
}

// Cancelling a run names it in the path and answers with its record; a run
// that ended, or one the router does not have, is refused.
func TestComplexitySuggestionsCancelRun(t *testing.T) {
	r, manager := suggestionHandler(t)
	code, body := serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs/run-9/cancel", "")
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, "run-9", manager.cancelled)
	assert.Equal(t, "cancelled", body["status"])

	for _, tc := range []struct {
		err  error
		code int
	}{
		{routing.ErrComplexitySuggestionRunEnded, fasthttp.StatusConflict},
		{routing.ErrUnknownComplexitySuggestionRun, fasthttp.StatusNotFound},
		{errors.New("store down"), fasthttp.StatusInternalServerError},
	} {
		manager.cancelErr = tc.err
		code, body = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs/run-9/cancel", "")
		assert.Equal(t, tc.code, code, body)
	}
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs/run-9/cancel?router_id=office", "")
	assert.Equal(t, fasthttp.StatusNotFound, code, "another router's run is not this router's to cancel")
}

// An impact replay takes the whole draft bank, tiers upper-cased and text
// trimmed, and a window; a bank that is empty or a window past the lookback
// is refused.
func TestComplexitySuggestionsImpact(t *testing.T) {
	r, manager := suggestionHandler(t)
	manager.impact = ComplexitySuggestionImpactReport{Requests: 10, Total: 10, ChangedCount: 2, Savings: 1.5}
	code, body := serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact?window=7d",
		`{"phrases":[{"tier":"simple","text":"  where is my refund "},{"tier":"COMPLEX","text":"write a poem"}]}`)
	require.Equal(t, fasthttp.StatusOK, code, body)
	assert.Equal(t, []complexity.SuggestionPhrase{{Tier: "SIMPLE", Text: "where is my refund"}, {Tier: "COMPLEX", Text: "write a poem"}}, manager.draft)
	assert.Equal(t, []time.Duration{7 * 24 * time.Hour}, manager.windows)
	assert.Equal(t, 2.0, body["changed_count"])
	assert.Equal(t, 1.5, body["savings"])
	assert.Nil(t, manager.draftFloor, "no floor in the body replays the draft at the serving one")

	// A draft that changes the floor is replayed at it; a floor the router
	// would refuse is refused here too.
	code, body = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact",
		`{"phrases":[{"tier":"SIMPLE","text":"x"}],"min_similarity":0.62}`)
	require.Equal(t, fasthttp.StatusOK, code, body)
	require.NotNil(t, manager.draftFloor)
	assert.InDelta(t, 0.62, *manager.draftFloor, 1e-9)
	for _, bad := range []string{"-0.1", "1", "1.5"} {
		code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact", `{"phrases":[{"tier":"SIMPLE","text":"x"}],"min_similarity":`+bad+`}`)
		assert.Equal(t, fasthttp.StatusBadRequest, code, bad)
	}

	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact", `{"phrases":[]}`)
	assert.Equal(t, fasthttp.StatusBadRequest, code, "an empty bank")
	// The saved bank's limits: its phrase count and one phrase's length.
	tooMany := make([]map[string]string, configstore.MaxComplexitySemanticPhrases+1)
	for i := range tooMany {
		tooMany[i] = map[string]string{"tier": "SIMPLE", "text": fmt.Sprintf("phrase %d", i)}
	}
	payload, err := json.Marshal(map[string]any{"phrases": tooMany})
	require.NoError(t, err)
	manager.draft = nil
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact", string(payload))
	assert.Equal(t, fasthttp.StatusBadRequest, code, "more phrases than a bank can hold")
	long := strings.Repeat("a", configstore.MaxComplexitySemanticPhraseCharacters+1)
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact", `{"phrases":[{"tier":"SIMPLE","text":"`+long+`"}]}`)
	assert.Equal(t, fasthttp.StatusBadRequest, code, "a phrase longer than a bank allows")
	assert.Nil(t, manager.draft, "neither is replayed")
	code, _ = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact?window=soon", `{"phrases":[{"tier":"SIMPLE","text":"x"}]}`)
	assert.Equal(t, fasthttp.StatusBadRequest, code, "an unreadable window")
	for _, tc := range []struct {
		err  error
		code int
	}{
		{routing.ErrComplexitySuggestionWindow, fasthttp.StatusBadRequest},
		{suggestrun.ErrInvalidPhraseTest, fasthttp.StatusBadRequest},
		{routing.ErrComplexitySuggestionsOff, fasthttp.StatusConflict},
		{complexity.ErrNoServingGeneration, fasthttp.StatusServiceUnavailable},
		{errors.New("embedding provider down"), fasthttp.StatusBadGateway},
	} {
		manager.impactErr = tc.err
		code, body = serve(t, r, fasthttp.MethodPost, "/api/routing/complexity-suggestions/impact", `{"phrases":[{"tier":"SIMPLE","text":"x"}]}`)
		assert.Equal(t, tc.code, code, "%v: %v", tc.err, body)
	}
}

// Pricing covers every changed request: at its live tier's model, and at its
// new tier's model. A change into no match, to a tier with no model, or of a
// gone log is unpriced; a sampled replay is scaled to its window.
func TestPriceComplexitySuggestionImpact(t *testing.T) {
	impact := suggestrun.Impact{
		Requests: 5, Total: 10,
		Live:  map[string]int{"MEDIUM": 4, "none": 1},
		Draft: map[string]int{"SIMPLE": 3, "MEDIUM": 1, "none": 1},
		Changed: []suggestrun.ImpactChange{
			{LogID: "a", From: "MEDIUM", To: "SIMPLE", Phrase: "where is my refund"},
			{LogID: "b", From: "MEDIUM", To: "SIMPLE", Phrase: "where is my refund"},
			{LogID: "c", From: "none", To: "SIMPLE"},
			{LogID: "gone", From: "MEDIUM", To: "SIMPLE"},
			{LogID: "d", From: "MEDIUM", To: "COMPLEX"},
			{LogID: "e", From: "MEDIUM", To: "none"},
			{LogID: "f", From: "MEDIUM", To: "SIMPLE"},
		},
		Attention: []suggestrun.ImpactAttention{{Phrase: "where is my refund", Tier: "SIMPLE", FromTier: "MEDIUM", Requests: 2, LogID: "a"}},
	}
	usage := []logstore.ComplexityReplayUsage{
		{ID: "a", Provider: "openai", Model: "gpt-big", PromptTokens: 1000, CompletionTokens: 100},
		{ID: "b", Provider: "openai", Model: "gpt-big", PromptTokens: 1000, CompletionTokens: 100},
		{ID: "c", Provider: "openai", Model: "gpt-big", PromptTokens: 1000, CompletionTokens: 100},
		{ID: "d", Provider: "openai", Model: "gpt-big", PromptTokens: 1000, CompletionTokens: 100},
		{ID: "e", Provider: "openai", Model: "gpt-big", PromptTokens: 1000, CompletionTokens: 100},
		{ID: "f", Provider: "custom", Model: "unpriced", PromptTokens: 1000, CompletionTokens: 100},
	}
	models := map[string]ComplexityImpactModel{"SIMPLE": {Provider: "openai", Model: "gpt-small"}, "MEDIUM": {Provider: "openai", Model: "gpt-big"}}
	price := func(_, model string, prompt, completion int64) (float64, bool) {
		perToken := map[string]float64{"gpt-big": 0.01, "gpt-small": 0.001}[model]
		if perToken == 0 {
			return 0, false
		}
		return perToken * float64(prompt+completion), true
	}
	got := PriceComplexitySuggestionImpact(impact, 7*24*time.Hour, usage, models, price, map[string]string{"a": "where is my refund for order 9"})

	assert.EqualValues(t, 7*24*3600, got.WindowSeconds)
	assert.Equal(t, 4, got.Priced, "a, b, c, and f: served by an unpriced model, but priced at MEDIUM's target")
	assert.Equal(t, 3, got.Unpriced, "gone, d (no COMPLEX model), e (into no match)")
	assert.InDelta(t, 2*4*11.0, got.LiveCost, 1e-9, "four at 11 each, doubled for a half sample")
	assert.InDelta(t, 2*4*1.1, got.DraftCost, 1e-9)
	assert.InDelta(t, got.LiveCost-got.DraftCost, got.Savings, 1e-9)
	assert.Equal(t, 7, got.ChangedCount)
	require.Len(t, got.Changed, 7)
	assert.Equal(t, "where is my refund for order 9", got.Changed[0].Text)
	require.Len(t, got.Attention, 1)
	assert.Equal(t, "where is my refund for order 9", got.Attention[0].Text)
	require.Len(t, got.Tiers, 4)
	assert.Equal(t, ComplexityImpactTier{Tier: "SIMPLE", Live: 0, Draft: 3, Model: &ComplexityImpactModel{Provider: "openai", Model: "gpt-small"}}, got.Tiers[0])
	assert.Nil(t, got.Tiers[2].Model, "COMPLEX has no model to price at")
	assert.Equal(t, ComplexityImpactTier{Tier: "none", Live: 1, Draft: 1}, got.Tiers[3])
}

// A memoized price probes each model once and prices from its rates, matching
// the exact price below the long-context tiers; a model with no price is
// asked once; a request past the bound is priced exactly.
func TestMemoizeComplexityImpactPrice(t *testing.T) {
	calls := map[string]int{}
	exact := func(_ string, model string, prompt, completion int64) (float64, bool) {
		calls[model]++
		switch model {
		case "gpt-big":
			cost := 0.000002*float64(prompt) + 0.000008*float64(completion)
			if prompt > 128_000 {
				cost *= 2 // a long-context tier
			}
			return cost, true
		}
		return 0, false
	}
	price := MemoizeComplexityImpactPrice(exact)
	for range 50 {
		got, ok := price("openai", "gpt-big", 1200, 80)
		require.True(t, ok)
		assert.InDelta(t, 0.000002*1200+0.000008*80, got, 1e-12)
		_, ok = price("custom", "unpriced", 1200, 80)
		assert.False(t, ok)
	}
	assert.Equal(t, 3, calls["gpt-big"], "probed once each for the flat charge, prompt and completion rates")
	assert.Equal(t, 1, calls["unpriced"], "a model with no price is asked once")

	got, ok := price("openai", "gpt-big", 150_000, 100)
	require.True(t, ok)
	assert.InDelta(t, 2*(0.000002*150_000+0.000008*100), got, 1e-9, "past the bound the exact tiered price")
	assert.Equal(t, 4, calls["gpt-big"])
}

// A model with a flat charge per request is charged it once, not folded into
// its token rates and scaled with every request's tokens.
func TestMemoizeComplexityImpactPriceChargesAFlatFeeOnce(t *testing.T) {
	exact := func(_, _ string, prompt, completion int64) (float64, bool) {
		return 0.05 + 0.000002*float64(prompt) + 0.000008*float64(completion), true
	}
	price := MemoizeComplexityImpactPrice(exact)
	for _, tokens := range [][2]int64{{0, 0}, {1200, 80}, {50_000, 2_000}} {
		got, ok := price("openai", "flat", tokens[0], tokens[1])
		require.True(t, ok)
		want, _ := exact("", "", tokens[0], tokens[1])
		assert.InDelta(t, want, got, 1e-12, "%v", tokens)
	}
}

// Both sides are priced at the router's current models: a request served
// before the tier targets changed is priced at its live tier's target now,
// so only the draft's tier change moves the cost. A request no live tier
// target covers (no match) keeps the model that served it.
func TestPriceComplexitySuggestionImpactUsesCurrentTargetsForLive(t *testing.T) {
	impact := suggestrun.Impact{
		Requests: 3, Total: 3,
		Changed: []suggestrun.ImpactChange{
			{LogID: "old", From: "MEDIUM", To: "SIMPLE"},
			{LogID: "unmatched", From: "none", To: "SIMPLE"},
			{LogID: "untargeted", From: "COMPLEX", To: "SIMPLE"},
		},
	}
	usage := []logstore.ComplexityReplayUsage{
		// Served by a cheap model before MEDIUM had a target.
		{ID: "old", Provider: "openai", Model: "gpt-cheap", PromptTokens: 1000, CompletionTokens: 0},
		{ID: "unmatched", Provider: "openai", Model: "gpt-big", PromptTokens: 1000, CompletionTokens: 0},
		{ID: "untargeted", Provider: "openai", Model: "gpt-big", PromptTokens: 1000, CompletionTokens: 0},
	}
	models := map[string]ComplexityImpactModel{"SIMPLE": {Provider: "openai", Model: "gpt-small"}, "MEDIUM": {Provider: "openai", Model: "gpt-big"}}
	price := func(_, model string, prompt, _ int64) (float64, bool) {
		perToken := map[string]float64{"gpt-big": 0.01, "gpt-small": 0.001, "gpt-cheap": 0.0001}[model]
		return perToken * float64(prompt), perToken > 0
	}
	got := PriceComplexitySuggestionImpact(impact, time.Hour, usage, models, price, nil)
	assert.Equal(t, 3, got.Priced)
	// old: MEDIUM's target now (10), not gpt-cheap as served (0.1);
	// unmatched and untargeted: the model that served them (10 each).
	assert.InDelta(t, 30.0, got.LiveCost, 1e-9)
	assert.InDelta(t, 3.0, got.DraftCost, 1e-9)
	assert.InDelta(t, 27.0, got.Savings, 1e-9, "moving all three down saves money")
}
