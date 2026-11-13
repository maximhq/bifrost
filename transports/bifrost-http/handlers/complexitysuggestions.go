package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggestrun"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ComplexitySuggestionManager backs the phrase-suggestion endpoints. The
// routing manager implements it when the deployment can keep suggestions; the
// endpoints answer 503 otherwise.
type ComplexitySuggestionManager interface {
	// RoutingPluginLoaded reports whether the routing plugin, which runs and
	// tests suggestions, is loaded.
	RoutingPluginLoaded() bool
	// ComplexitySuggestionStore is nil without a log store.
	ComplexitySuggestionStore() logstore.ComplexitySuggestionStore
	// ComplexitySuggestionTexts reads example request text within the
	// caller's access scope; a log the caller cannot see gives no text.
	ComplexitySuggestionTexts(ctx context.Context, routerID string, logIDs []string) (map[string]string, error)
	// ComplexitySuggestionRouter reports whether a router exists and the
	// phrase bank version it serves.
	ComplexitySuggestionRouter(routerID string) (version string, ok bool)
	StartComplexitySuggestionRun(ctx context.Context, routerID string, window time.Duration) (string, error)
	EstimateComplexitySuggestionRun(ctx context.Context, routerID string, window time.Duration) (suggestrun.Estimate, error)
	TestComplexitySuggestionPhrases(ctx context.Context, routerID string, phrases []complexity.SuggestionPhrase, examples []suggestrun.TestExample) (suggestrun.PhraseTest, error)
	// CancelComplexitySuggestionRun stops a queued or running run and returns
	// its record as it now stands.
	CancelComplexitySuggestionRun(ctx context.Context, routerID, runID string) (logstore.ComplexitySuggestionRun, error)
	// ComplexitySuggestionImpact replays the window's captured requests
	// against a draft phrase bank and prices what changes.
	ComplexitySuggestionImpact(ctx context.Context, routerID string, window time.Duration, draft []complexity.SuggestionPhrase, draftMinSimilarity *float64) (ComplexitySuggestionImpactReport, error)
}

const (
	defaultComplexitySuggestionRunsLimit = 20
	maxComplexitySuggestionRunsLimit     = 200
	defaultComplexitySuggestionsLimit    = 100
	maxComplexitySuggestionsLimit        = 500
)

// complexitySuggestionView is a stored suggestion as the API serves it: the
// row's own fields, with its JSON-encoded lists decoded. The centroid is not
// served.
type complexitySuggestionView struct {
	logstore.ComplexitySuggestion
	Phrases       []string `json:"phrases"`
	ExampleLogIDs []string `json:"example_log_ids"`
	// Stale is true when the suggestion was computed against an earlier
	// version of the router than the one it serves now.
	Stale bool `json:"stale"`
	// CurrentRouting is how many of the cluster's requests routed to each tier
	// when its last run computed it ("none": no phrase matched them). Empty
	// for a row written before it was recorded.
	CurrentRouting map[string]int64 `json:"current_routing"`
}

func newComplexitySuggestionView(row logstore.ComplexitySuggestion, currentVersion string) complexitySuggestionView {
	row.Centroid = nil
	view := complexitySuggestionView{ComplexitySuggestion: row, Phrases: row.Phrases(), ExampleLogIDs: row.ExampleLogIDs(),
		Stale: currentVersion != "" && row.RouterVersion != "" && row.RouterVersion != currentVersion}
	if view.Phrases == nil {
		view.Phrases = []string{}
	}
	if view.ExampleLogIDs == nil {
		view.ExampleLogIDs = []string{}
	}
	view.CurrentRouting = row.CurrentRouting()
	if view.CurrentRouting == nil {
		view.CurrentRouting = map[string]int64{}
	}
	return view
}

// complexitySuggestionsResponse is the list payload: one page of the
// suggestions the status filter selects, how many it selects in all (Total),
// and how many are stored per status whatever the filter (Counts), so every
// list's count can be shown from one response.
type complexitySuggestionsResponse struct {
	RouterID      string                            `json:"router_id"`
	RouterVersion string                            `json:"router_version,omitempty"` // served now; a suggestion with another is stale
	Suggestions   []complexitySuggestionView        `json:"suggestions"`
	Total         int                               `json:"total"`  // selected by the status filter, across every page
	Limit         int                               `json:"limit"`  // the page served
	Offset        int                               `json:"offset"` // the page served
	Counts        map[string]int                    `json:"counts"` // per status, whatever the filter selected
	Enabled       bool                              `json:"enabled"`
	LastRun       *logstore.ComplexitySuggestionRun `json:"last_run,omitempty"`
	NextRunAt     *time.Time                        `json:"next_run_at,omitempty"`
}

// complexitySuggestionExample is one example request of a suggestion. Text is
// empty for a log that is gone or that the caller cannot see.
type complexitySuggestionExample struct {
	LogID string `json:"log_id"`
	Text  string `json:"text,omitempty"`
}

type complexitySuggestionDetailResponse struct {
	Suggestion complexitySuggestionView      `json:"suggestion"`
	Examples   []complexitySuggestionExample `json:"examples"`
}

type complexitySuggestionStatusRequest struct {
	Status string `json:"status"`
}

type complexitySuggestionTestRequest struct {
	Phrases []complexitySuggestionTestPhrase `json:"phrases"`
	// SuggestionID routes that suggestion's examples; LogIDs routes these
	// logs instead or as well.
	SuggestionID string   `json:"suggestion_id,omitempty"`
	LogIDs       []string `json:"log_ids,omitempty"`
}

// complexitySuggestionTestPhrase is a phrase to test: its text and tier. The
// gateway embeds it; a client never supplies a vector.
type complexitySuggestionTestPhrase struct {
	Tier string `json:"tier"`
	Text string `json:"text"`
}

type complexitySuggestionRunsResponse struct {
	Runs      []logstore.ComplexitySuggestionRun `json:"runs"`
	NextRunAt *time.Time                         `json:"next_run_at,omitempty"`
}

// registerComplexitySuggestionRoutes registers the phrase-suggestion
// endpoints, under the model router they are for. They are new, so they have
// no /api/governance alias.
func (h *RoutingHandler) registerComplexitySuggestionRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handle := func(method, path string, handler fasthttp.RequestHandler) {
		r.Handle(method, path, lib.ChainMiddlewares(h.visibleModelRouter(path, handler), middlewares...))
	}
	const base = "/api/routing/model-routers/{router_id}/suggestions"
	handle(fasthttp.MethodGet, base, h.listComplexitySuggestions)
	handle(fasthttp.MethodPost, base+"/test", h.testComplexitySuggestionPhrases)
	handle(fasthttp.MethodGet, base+"/runs", h.listComplexitySuggestionRuns)
	handle(fasthttp.MethodGet, base+"/runs/estimate", h.estimateComplexitySuggestionRun)
	handle(fasthttp.MethodPost, base+"/runs", h.startComplexitySuggestionRun)
	handle(fasthttp.MethodPost, base+"/runs/{run_id}/cancel", h.cancelComplexitySuggestionRun)
	handle(fasthttp.MethodPost, base+"/impact", h.complexitySuggestionImpact)
	handle(fasthttp.MethodGet, base+"/{suggestion_id}", h.getComplexitySuggestion)
	handle(fasthttp.MethodPut, base+"/{suggestion_id}/status", h.updateComplexitySuggestionStatus)
	handle(fasthttp.MethodPost, "/api/routing/model-routers/{router_id}/phrases", h.addModelRouterPhrases)
}

// suggestionManager returns the suggestion manager and its store, or answers
// 503 and returns false.
func (h *RoutingHandler) suggestionManager(ctx *fasthttp.RequestCtx) (ComplexitySuggestionManager, logstore.ComplexitySuggestionStore, bool) {
	manager, ok := h.routingManager.(ComplexitySuggestionManager)
	if !ok {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "phrase suggestions are not available on this deployment")
		return nil, nil, false
	}
	if !manager.RoutingPluginLoaded() {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "phrase suggestions need the routing plugin, which is not loaded")
		return nil, nil, false
	}
	store := manager.ComplexitySuggestionStore()
	if store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "phrase suggestions need a log store; enable logging to use them")
		return nil, nil, false
	}
	return manager, store, true
}

// suggestionRouterID reads the model router a request is about, from its
// path, and its current version. A router the plugin does not serve is
// answered as its status endpoint answers it: 404 when it is not saved, 503
// when it is saved but not loaded. A call the plugin then answers with
// ErrUnknownComplexityRouter (the router was unloaded meanwhile) is answered
// the same way.
func (h *RoutingHandler) suggestionRouterID(ctx *fasthttp.RequestCtx, manager ComplexitySuggestionManager) (string, string, bool) {
	routerID := pathID(ctx, "router_id")
	version, ok := manager.ComplexitySuggestionRouter(routerID)
	if !ok {
		h.sendModelRouterNotServed(ctx, routerID)
		return "", "", false
	}
	return routerID, version, true
}

// parseSuggestionWindow reads a run window: a Go duration ("36h") or a
// number of days ("7d"). Empty is zero, the whole lookback.
func parseSuggestionWindow(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("window %q is not a positive number of days", raw)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("window %q must look like 24h or 7d", raw)
	}
	return d, nil
}

// suggestionSchedule reports whether a router's suggestions are on and when
// its next scheduled run is, from the model router's own config: suggestions
// are set per router, never on the global complexity analyzer config. A
// router that cannot be read reports them off.
func (h *RoutingHandler) suggestionSchedule(ctx context.Context, routerID string) (bool, *time.Time) {
	router, err := h.configStore.GetModelRouter(ctx, routerID)
	if err != nil || router == nil || !router.SuggestionsEnabled() {
		return false, nil
	}
	// The schedule's state, once a node has set it, has the next run as
	// catch-ups and schedule changes left it.
	next := routing.NextComplexitySuggestionRun(ctx, h.configStore, routerID, router.Suggestions.EffectiveSchedule(), time.Now())
	return true, &next
}

// listComplexitySuggestions lists stored suggestions, largest first, filtered
// by ?status= (comma-separated; default open) and paged by ?limit= (default
// 100, at most 500) and ?offset=.
func (h *RoutingHandler) listComplexitySuggestions(ctx *fasthttp.RequestCtx) {
	manager, store, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, version, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	statuses := []string{logstore.ComplexitySuggestionStatusOpen}
	if raw := strings.TrimSpace(string(ctx.QueryArgs().Peek("status"))); raw != "" {
		statuses = nil
		for _, status := range strings.Split(raw, ",") {
			status = strings.TrimSpace(status)
			if !validComplexitySuggestionStatus(status) {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("unknown suggestion status %q", status))
				return
			}
			// Repeated, a status would be counted into Total twice while the
			// list selects its rows once.
			if !slices.Contains(statuses, status) {
				statuses = append(statuses, status)
			}
		}
	}
	limit, ok := queryInt(ctx, "limit", defaultComplexitySuggestionsLimit, 1, maxComplexitySuggestionsLimit)
	if !ok {
		return
	}
	offset, ok := queryInt(ctx, "offset", 0, 0, math.MaxInt32)
	if !ok {
		return
	}
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: routerID, Statuses: statuses, OmitCentroids: true, Limit: limit, Offset: offset})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list suggestions: %v", err))
		return
	}
	counts, err := store.CountComplexitySuggestionsByStatus(ctx, logstore.ComplexitySuggestionFilter{RouterID: routerID})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to count suggestions: %v", err))
		return
	}
	response := complexitySuggestionsResponse{RouterID: routerID, RouterVersion: version, Suggestions: make([]complexitySuggestionView, 0, len(rows)), Counts: counts, Limit: limit, Offset: offset}
	for _, row := range rows {
		response.Suggestions = append(response.Suggestions, newComplexitySuggestionView(row, version))
	}
	for _, status := range statuses {
		response.Total += counts[status]
	}
	if runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: routerID}, 1); err == nil && len(runs) > 0 {
		response.LastRun = &runs[0]
	}
	response.Enabled, response.NextRunAt = h.suggestionSchedule(ctx, routerID)
	SendJSON(ctx, response)
}

// getComplexitySuggestion returns one suggestion with its examples' text.
func (h *RoutingHandler) getComplexitySuggestion(ctx *fasthttp.RequestCtx) {
	manager, store, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, version, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	row, ok := h.loadComplexitySuggestion(ctx, store, routerID)
	if !ok {
		return
	}
	ids := row.ExampleLogIDs()
	texts, err := manager.ComplexitySuggestionTexts(ctx, routerID, ids)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read suggestion examples: %v", err))
		return
	}
	examples := make([]complexitySuggestionExample, 0, len(ids))
	for _, id := range ids {
		examples = append(examples, complexitySuggestionExample{LogID: id, Text: texts[id]})
	}
	SendJSON(ctx, complexitySuggestionDetailResponse{Suggestion: newComplexitySuggestionView(*row, version), Examples: examples})
}

// updateComplexitySuggestionStatus records the user's call on a suggestion:
// accepted (its phrases were added to the bank) or dismissed, from open; or
// open again, undoing either. A run leaves accepted and dismissed
// suggestions alone, so the call sticks.
func (h *RoutingHandler) updateComplexitySuggestionStatus(ctx *fasthttp.RequestCtx) {
	manager, store, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, version, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	var req complexitySuggestionStatusRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if !validComplexitySuggestionStatus(req.Status) {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("unknown suggestion status %q", req.Status))
		return
	}
	row, ok := h.loadComplexitySuggestion(ctx, store, routerID)
	if !ok {
		return
	}
	if !complexitySuggestionTransitionAllowed(row.Status, req.Status) {
		SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("%s %s suggestion cannot be marked %q", suggestionStatusArticle(row.Status), row.Status, req.Status))
		return
	}
	before := row.Status
	row.Status = req.Status
	if req.Status == logstore.ComplexitySuggestionStatusOpen {
		row.ResolvedAt = nil
	} else {
		now := time.Now().UTC()
		row.ResolvedAt = &now
	}
	// Only from the status it was read at: a run superseding it, or another
	// decision, since then wins, and this one is refused.
	moved, err := store.UpdateComplexitySuggestionStatus(ctx, row.ID, before, req.Status, row.ResolvedAt)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update suggestion: %v", err))
		return
	}
	if !moved {
		SendError(ctx, fasthttp.StatusConflict, "the suggestion changed since it was read; reload it and try again")
		return
	}
	lib.RecordAuditChange(ctx, "model_routing_suggestion."+req.Status, "model_routing_suggestion", row.ID, strings.Join(row.Phrases(), ", "),
		map[string]any{"status": before}, map[string]any{"status": req.Status})
	SendJSON(ctx, newComplexitySuggestionView(*row, version))
}

// maxComplexitySuggestionTestLogIDs bounds the log IDs one phrase test may
// send, and so the logs it may read looking for readable examples.
const maxComplexitySuggestionTestLogIDs = 256

// testComplexitySuggestionPhrases routes examples with and without phrases
// against the serving bank, so an edited phrase can be tried before it is
// staged. Examples are a suggestion's, or given log IDs, read within the
// caller's access scope.
func (h *RoutingHandler) testComplexitySuggestionPhrases(ctx *fasthttp.RequestCtx) {
	manager, store, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, _, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	var req complexitySuggestionTestRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if len(req.Phrases) == 0 || len(req.Phrases) > suggestrun.MaxTestPhrases {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("test between 1 and %d phrases", suggestrun.MaxTestPhrases))
		return
	}
	if len(req.LogIDs) > maxComplexitySuggestionTestLogIDs {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("send at most %d log IDs", maxComplexitySuggestionTestLogIDs))
		return
	}
	// The caller's own log IDs come first, then the suggestion's examples.
	ids := make([]string, 0, len(req.LogIDs))
	seen := map[string]struct{}{}
	addID := func(id string) {
		if _, dup := seen[id]; !dup && id != "" {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, id := range req.LogIDs {
		addID(id)
	}
	if req.SuggestionID != "" {
		rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: routerID, IDs: []string{req.SuggestionID}, OmitCentroids: true})
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read suggestion: %v", err))
			return
		}
		if len(rows) == 0 {
			SendError(ctx, fasthttp.StatusNotFound, "suggestion not found")
			return
		}
		for _, id := range rows[0].ExampleLogIDs() {
			addID(id)
		}
	}
	// The cap applies to the examples that can be read: logs are read a
	// batch at a time until it is reached, so a log that is gone or hidden
	// never takes a readable one's place.
	examples := make([]suggestrun.TestExample, 0, min(len(ids), suggestrun.MaxTestExamples))
	for start := 0; start < len(ids) && len(examples) < suggestrun.MaxTestExamples; start += suggestrun.MaxTestExamples {
		batch := ids[start:min(start+suggestrun.MaxTestExamples, len(ids))]
		texts, err := manager.ComplexitySuggestionTexts(ctx, routerID, batch)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read examples: %v", err))
			return
		}
		for _, id := range batch {
			if text := texts[id]; text != "" && len(examples) < suggestrun.MaxTestExamples {
				examples = append(examples, suggestrun.TestExample{LogID: id, Text: text})
			}
		}
	}
	phrases := make([]complexity.SuggestionPhrase, len(req.Phrases))
	for i, p := range req.Phrases {
		phrases[i] = complexity.SuggestionPhrase{Tier: strings.ToUpper(strings.TrimSpace(p.Tier)), Text: strings.TrimSpace(p.Text)}
		// The bank's own limit, checked before anything is embedded.
		if n := utf8.RuneCountInString(phrases[i].Text); n > configstore.MaxComplexitySemanticPhraseCharacters {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("phrase %d is %d characters; a phrase can be at most %d", i+1, n, configstore.MaxComplexitySemanticPhraseCharacters))
			return
		}
	}
	result, err := manager.TestComplexitySuggestionPhrases(ctx, routerID, phrases, examples)
	switch {
	case errors.Is(err, routing.ErrUnknownComplexityRouter):
		h.sendModelRouterNotServed(ctx, routerID)
	case errors.Is(err, complexity.ErrNoServingGeneration):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
	case errors.Is(err, suggestrun.ErrInvalidPhraseTest):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
	case err != nil:
		// The embedding provider failed the test's phrases or examples.
		SendError(ctx, fasthttp.StatusBadGateway, err.Error())
	default:
		if result.Examples == nil {
			result.Examples = []suggestrun.TestedExample{}
		}
		SendJSON(ctx, result)
	}
}

// listComplexitySuggestionRuns lists runs newest first (?limit=, default 20).
func (h *RoutingHandler) listComplexitySuggestionRuns(ctx *fasthttp.RequestCtx) {
	manager, store, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, _, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	limit := defaultComplexitySuggestionRunsLimit
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxComplexitySuggestionRunsLimit {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxComplexitySuggestionRunsLimit))
			return
		}
		limit = n
	}
	runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: routerID}, limit)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list suggestion runs: %v", err))
		return
	}
	if runs == nil {
		runs = []logstore.ComplexitySuggestionRun{}
	}
	_, next := h.suggestionSchedule(ctx, routerID)
	SendJSON(ctx, complexitySuggestionRunsResponse{Runs: runs, NextRunAt: next})
}

// startComplexitySuggestionRun starts a run now. It answers 202 with the
// run's ID; the run record appears in the runs listing once it starts.
func (h *RoutingHandler) startComplexitySuggestionRun(ctx *fasthttp.RequestCtx) {
	manager, _, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, _, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	window, err := parseSuggestionWindow(string(ctx.QueryArgs().Peek("window")))
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	runID, err := manager.StartComplexitySuggestionRun(ctx, routerID, window)
	switch {
	case errors.Is(err, routing.ErrComplexitySuggestionWindow):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	case errors.Is(err, routing.ErrUnknownComplexityRouter):
		h.sendModelRouterNotServed(ctx, routerID)
		return
	case errors.Is(err, routing.ErrComplexitySuggestionsOff), errors.Is(err, routing.ErrComplexitySuggestionRunInFlight):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrRoutingPluginUnavailable), errors.Is(err, complexity.ErrNoServingGeneration), errors.Is(err, complexity.ErrNoCaptureStore):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to start suggestion run: %v", err))
		return
	}
	lib.RecordAuditChange(ctx, "model_routing_suggestion_run.started", "model_routing_suggestion_run", runID, runID, nil,
		map[string]any{"run_id": runID, "router_id": routerID, "window_seconds": int64(window / time.Second)})
	SendJSONWithStatus(ctx, map[string]string{"run_id": runID}, fasthttp.StatusAccepted)
}

// estimateComplexitySuggestionRun sizes a run over ?window= without starting
// it: the requests it would read, cluster and count in, and at most how many
// suggestion-model calls it would make.
func (h *RoutingHandler) estimateComplexitySuggestionRun(ctx *fasthttp.RequestCtx) {
	manager, _, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, _, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	window, err := parseSuggestionWindow(string(ctx.QueryArgs().Peek("window")))
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	estimate, err := manager.EstimateComplexitySuggestionRun(ctx, routerID, window)
	switch {
	case errors.Is(err, routing.ErrComplexitySuggestionWindow):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
	case errors.Is(err, routing.ErrUnknownComplexityRouter):
		h.sendModelRouterNotServed(ctx, routerID)
	case errors.Is(err, routing.ErrComplexitySuggestionsOff):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
	case errors.Is(err, ErrRoutingPluginUnavailable), errors.Is(err, complexity.ErrNoServingGeneration), errors.Is(err, complexity.ErrNoCaptureStore):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to estimate suggestion run: %v", err))
	default:
		SendJSON(ctx, estimate)
	}
}

// cancelComplexitySuggestionRun stops a queued or running run and returns its
// record. A running run records itself as cancelled when it stops, so the
// record returned may still read running.
func (h *RoutingHandler) cancelComplexitySuggestionRun(ctx *fasthttp.RequestCtx) {
	manager, _, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, _, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	runID, _ := ctx.UserValue("run_id").(string)
	if strings.TrimSpace(runID) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "run id is required")
		return
	}
	run, err := manager.CancelComplexitySuggestionRun(ctx, routerID, runID)
	switch {
	case errors.Is(err, routing.ErrUnknownComplexityRouter):
		h.sendModelRouterNotServed(ctx, routerID)
		return
	case errors.Is(err, routing.ErrUnknownComplexitySuggestionRun):
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	case errors.Is(err, routing.ErrComplexitySuggestionRunEnded), errors.Is(err, routing.ErrComplexitySuggestionsOff):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
		return
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to cancel suggestion run: %v", err))
		return
	}
	lib.RecordAuditChange(ctx, "model_routing_suggestion_run.cancelled", "model_routing_suggestion_run", runID, runID, nil,
		map[string]any{"run_id": runID, "router_id": routerID})
	SendJSON(ctx, run)
}

// complexitySuggestionImpactRequest is phrases to try, by tier (SIMPLE,
// MEDIUM, COMPLEX): they are added to the router's saved phrases for one
// replay, and nothing is saved.
type complexitySuggestionImpactRequest struct {
	Phrases map[string][]string `json:"phrases"`
	// MinSimilarity is the floor the draft would save, when the draft changes
	// it; omitted replays the draft at the serving floor.
	MinSimilarity *float64 `json:"min_similarity,omitempty"`
}

// complexitySuggestionImpact replays the last ?window= of a router's captured
// requests against a draft phrase bank and the live one, and prices what
// would change tier. Nothing is saved.
func (h *RoutingHandler) complexitySuggestionImpact(ctx *fasthttp.RequestCtx) {
	manager, _, ok := h.suggestionManager(ctx)
	if !ok {
		return
	}
	routerID, _, ok := h.suggestionRouterID(ctx, manager)
	if !ok {
		return
	}
	window, err := parseSuggestionWindow(string(ctx.QueryArgs().Peek("window")))
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	var req complexitySuggestionImpactRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if m := req.MinSimilarity; m != nil && (math.IsNaN(*m) || *m < 0 || *m >= 1) {
		SendError(ctx, fasthttp.StatusBadRequest, "min_similarity must be at least 0 and less than 1")
		return
	}
	router, ok := h.loadModelRouterForPhrases(ctx, routerID)
	if !ok {
		return
	}
	keywords, appended, err := complexity.AppendPhrases(router.Keywords, req.Phrases)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	draft := complexity.BankPhrases(keywords)
	if len(draft) > suggestrun.MaxImpactPhrases {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("a draft bank holds at most %d phrases", suggestrun.MaxImpactPhrases))
		return
	}
	report, err := manager.ComplexitySuggestionImpact(ctx, routerID, window, draft, req.MinSimilarity)
	switch {
	case errors.Is(err, routing.ErrComplexitySuggestionWindow), errors.Is(err, suggestrun.ErrInvalidPhraseTest):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
	case errors.Is(err, routing.ErrUnknownComplexityRouter):
		h.sendModelRouterNotServed(ctx, routerID)
	case errors.Is(err, routing.ErrComplexitySuggestionsOff):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
	case errors.Is(err, complexity.ErrNoServingGeneration), errors.Is(err, complexity.ErrNoCaptureStore):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
	case err != nil:
		// The embedding provider failed the draft's new phrases, or the logs
		// could not be read.
		SendError(ctx, fasthttp.StatusBadGateway, err.Error())
	default:
		SendJSON(ctx, complexitySuggestionImpactResponse{ComplexitySuggestionImpactReport: report, PhraseAppend: appended})
	}
}

// complexitySuggestionImpactResponse is a replay's report with what the
// phrases tried did: added to the draft, or skipped as already there.
type complexitySuggestionImpactResponse struct {
	ComplexitySuggestionImpactReport
	complexity.PhraseAppend
}

// addModelRouterPhrasesRequest is phrases to add to a router's phrase bank,
// by tier, and the suggestions they came from, which are marked accepted.
type addModelRouterPhrasesRequest struct {
	Phrases       map[string][]string `json:"phrases"`
	SuggestionIDs []string            `json:"suggestion_ids,omitempty"`
}

// addModelRouterPhrasesResponse is the router as saved, what was added or
// skipped, and the suggestions marked accepted.
type addModelRouterPhrasesResponse struct {
	Router *configstore.ModelRouter `json:"router"`
	complexity.PhraseAppend
	Accepted []string `json:"accepted"`
}

// addModelRouterPhrases appends phrases to a model router's saved phrase
// bank and serves it, the one suggestion API that changes routing. A phrase
// its tier already holds is skipped; one another tier holds, or a bank past
// its limit, refuses the request and saves nothing. Suggestions named must be
// this router's and still open, or nothing is saved. The append is made to
// the router as stored, under its row lock, so it never overwrites a save
// that landed since it was read. The router is served again even when every
// phrase was already there, so a retry after a failed reload still reaches
// it. Once the phrases are saved the suggestions are marked accepted; one a
// run superseded meanwhile, or whose status cannot be written, is left out of
// the accepted list rather than failing a save already made.
func (h *RoutingHandler) addModelRouterPhrases(ctx *fasthttp.RequestCtx) {
	routerID := pathID(ctx, "router_id")
	var req addModelRouterPhrasesRequest
	if !decodeStrictJSON(ctx, &req) {
		return
	}
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}
	var suggestions []logstore.ComplexitySuggestion
	var store logstore.ComplexitySuggestionStore
	if ids := uniqueTrimmed(req.SuggestionIDs); len(ids) > 0 {
		var ok bool
		if _, store, ok = h.suggestionManager(ctx); !ok {
			return
		}
		rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: routerID, IDs: ids})
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read suggestions: %v", err))
			return
		}
		found := make(map[string]logstore.ComplexitySuggestion, len(rows))
		for _, row := range rows {
			found[row.ID] = row
		}
		for _, id := range ids {
			row, exists := found[id]
			if !exists {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("suggestion %q not found for model router %q", id, routerID))
				return
			}
			if !complexitySuggestionTransitionAllowed(row.Status, logstore.ComplexitySuggestionStatusAccepted) {
				SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("suggestion %q is %s, not open", id, row.Status))
				return
			}
			suggestions = append(suggestions, row)
		}
	}
	var appended complexity.PhraseAppend
	var before configstore.ModelRouter
	var refusal *phraseAppendRefusal
	edited, err := h.configStore.EditModelRouter(ctx, routerID, func(router *configstore.ModelRouter) (bool, error) {
		before = *router
		keywords, result, err := complexity.AppendPhrases(router.Keywords, req.Phrases)
		if err != nil {
			refusal = &phraseAppendRefusal{status: fasthttp.StatusBadRequest, err: err}
			return false, refusal
		}
		appended = result
		if len(result.Added) == 0 {
			return false, nil
		}
		router.Keywords = keywords
		normalized, err := complexity.ValidateAndNormalize(&router.ComplexityAnalyzerConfig)
		if err != nil {
			refusal = &phraseAppendRefusal{status: fasthttp.StatusBadRequest, err: err}
			return false, refusal
		}
		if err := h.routingManager.ValidateComplexityAnalyzerConfig(ctx, normalized); err != nil {
			status := fasthttp.StatusBadRequest
			if errors.Is(err, ErrRoutingPluginUnavailable) {
				status = fasthttp.StatusInternalServerError
			}
			refusal = &phraseAppendRefusal{status: status, err: err}
			return false, refusal
		}
		router.ComplexityAnalyzerConfig = *normalized
		return true, nil
	})
	switch {
	case refusal != nil:
		SendError(ctx, refusal.status, refusal.err.Error())
		return
	case err != nil:
		sendModelRouterLoadError(ctx, routerID, err)
		return
	}
	if len(appended.Added) > 0 {
		// Recorded once the phrases are saved, before anything that could
		// still fail: the save stands either way.
		lib.RecordAuditChange(ctx, "model_router.phrases_added", "model_router", before.ID, before.Name, &before, edited)
	}
	if !h.reloadModelRouter(ctx, routerID) {
		return
	}
	saved, err := h.configStore.GetModelRouter(ctx, routerID)
	if err != nil {
		sendModelRouterLoadError(ctx, routerID, err)
		return
	}
	accepted := []string{}
	for _, row := range suggestions {
		resolved := time.Now().UTC()
		moved, err := store.UpdateComplexitySuggestionStatus(ctx, row.ID, row.Status, logstore.ComplexitySuggestionStatusAccepted, &resolved)
		if err != nil {
			logger.Warn("model router %s: phrases saved, but suggestion %s could not be marked accepted: %v", routerID, row.ID, err)
			continue
		}
		if !moved {
			continue // a run superseded it meanwhile; left as the run left it
		}
		accepted = append(accepted, row.ID)
		lib.RecordAuditChange(ctx, "model_routing_suggestion."+logstore.ComplexitySuggestionStatusAccepted, "model_routing_suggestion", row.ID, strings.Join(row.Phrases(), ", "),
			map[string]any{"status": row.Status}, map[string]any{"status": logstore.ComplexitySuggestionStatusAccepted})
	}
	SendJSON(ctx, addModelRouterPhrasesResponse{Router: saved, PhraseAppend: appended, Accepted: accepted})
}

// phraseAppendRefusal is a phrase add the router's config refuses, with the
// status it is answered with.
type phraseAppendRefusal struct {
	status int
	err    error
}

func (r *phraseAppendRefusal) Error() string { return r.err.Error() }

// uniqueTrimmed returns ids trimmed, without blanks or repeats, in order.
func uniqueTrimmed(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// loadModelRouterForPhrases reads a router's saved config, whose phrases an
// impact check or an add builds on, answering the error otherwise.
func (h *RoutingHandler) loadModelRouterForPhrases(ctx *fasthttp.RequestCtx, routerID string) (*configstore.ModelRouter, bool) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return nil, false
	}
	router, err := h.configStore.GetModelRouter(ctx, routerID)
	if err != nil {
		sendModelRouterLoadError(ctx, routerID, err)
		return nil, false
	}
	return router, true
}

// loadComplexitySuggestion reads the suggestion named in the path, with its
// centroid, or answers and returns false. A suggestion of another router is
// not found: it is not this router's to read or change.
func (h *RoutingHandler) loadComplexitySuggestion(ctx *fasthttp.RequestCtx, store logstore.ComplexitySuggestionStore, routerID string) (*logstore.ComplexitySuggestion, bool) {
	id, _ := ctx.UserValue("suggestion_id").(string)
	if strings.TrimSpace(id) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "suggestion id is required")
		return nil, false
	}
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: routerID, IDs: []string{id}})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read suggestion: %v", err))
		return nil, false
	}
	if len(rows) == 0 {
		SendError(ctx, fasthttp.StatusNotFound, "suggestion not found")
		return nil, false
	}
	return &rows[0], true
}

func validComplexitySuggestionStatus(status string) bool {
	switch status {
	case logstore.ComplexitySuggestionStatusOpen, logstore.ComplexitySuggestionStatusAccepted,
		logstore.ComplexitySuggestionStatusDismissed, logstore.ComplexitySuggestionStatusSuperseded:
		return true
	}
	return false
}

// suggestionStatusArticle is the article a status takes in a sentence: "an"
// before a vowel (open, accepted), "a" otherwise.
func suggestionStatusArticle(status string) string {
	if status != "" && strings.ContainsRune("aeiou", rune(status[0])) {
		return "an"
	}
	return "a"
}

// complexitySuggestionTransitionAllowed is the user's part of a suggestion's
// life: an open one is accepted or dismissed, and either call can be undone.
// Superseded is the runs' to set, and a superseded suggestion is stale.
func complexitySuggestionTransitionAllowed(from, to string) bool {
	switch to {
	case logstore.ComplexitySuggestionStatusAccepted, logstore.ComplexitySuggestionStatusDismissed:
		return from == logstore.ComplexitySuggestionStatusOpen
	case logstore.ComplexitySuggestionStatusOpen:
		return from == logstore.ComplexitySuggestionStatusAccepted || from == logstore.ComplexitySuggestionStatusDismissed
	}
	return false
}

// queryInt reads an integer query parameter in [lo, hi], fallback when it is
// absent, or answers 400 and returns false.
func queryInt(ctx *fasthttp.RequestCtx, name string, fallback, lo, hi int) (int, bool) {
	raw := string(ctx.QueryArgs().Peek(name))
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("%s must be between %d and %d", name, lo, hi))
		return 0, false
	}
	return n, true
}

// maxComplexityImpactChanges bounds the changed requests a report lists; the
// counts and the pricing cover every one of them.
const maxComplexityImpactChanges = 25

// ComplexityImpactModel is the model a tier is priced at: the router's
// highest-weighted target for it.
type ComplexityImpactModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ComplexityImpactChange is one changed request as a report lists it, with
// its text when the caller may read it.
type ComplexityImpactChange struct {
	suggestrun.ImpactChange
	Text string `json:"text,omitempty"`
}

// ComplexityImpactAttention is a flagged draft phrase with its example's text.
type ComplexityImpactAttention struct {
	suggestrun.ImpactAttention
	Text string `json:"text,omitempty"`
}

// ComplexityImpactTier is one tier's row of a report: how many replayed
// requests each bank sends to it, and the model it is priced at, when the
// router names one.
type ComplexityImpactTier struct {
	Tier  string                 `json:"tier"`
	Live  int                    `json:"live"`
	Draft int                    `json:"draft"`
	Model *ComplexityImpactModel `json:"model,omitempty"`
}

// ComplexitySuggestionImpactReport is what a draft phrase bank would do to a
// router's recent traffic. Costs are in dollars over the window, both sides
// at the router's current models: the changed requests at their live tier's
// model (or, with no live target, the model that served them) and at their
// new tier's model, scaled up from the replayed sample when the window held
// more. A change whose new tier has no model to price it at, or whose log is
// gone, is counted as unpriced rather than guessed.
type ComplexitySuggestionImpactReport struct {
	WindowSeconds int64                       `json:"window_seconds"`
	Requests      int                         `json:"requests"`
	Total         int                         `json:"total"`
	Tiers         []ComplexityImpactTier      `json:"tiers"`
	ChangedCount  int                         `json:"changed_count"`
	Changed       []ComplexityImpactChange    `json:"changed"`
	Attention     []ComplexityImpactAttention `json:"attention"`
	LiveCost      float64                     `json:"live_cost"`
	DraftCost     float64                     `json:"draft_cost"`
	Savings       float64                     `json:"savings"`
	Priced        int                         `json:"priced"`
	Unpriced      int                         `json:"unpriced"`
}

// ComplexityImpactPrice prices token counts at a provider's model; ok is false
// when the model has no known price.
type ComplexityImpactPrice func(provider, model string, promptTokens, completionTokens int64) (cost float64, ok bool)

// complexityImpactTiers is the order a report lists tiers in.
var complexityImpactTiers = []string{complexity.TierSimple, complexity.TierMedium, complexity.TierComplex, logstore.ComplexitySuggestionUnmatchedTier}

// PriceComplexitySuggestionImpact turns a replay into a report: each tier's
// counts and model, the first changed requests and the flagged phrases with
// their text, and the changed requests priced as served and at their new
// tier's model. It is pure: usage, models, prices and texts are handed in.
func PriceComplexitySuggestionImpact(impact suggestrun.Impact, window time.Duration, usage []logstore.ComplexityReplayUsage,
	models map[string]ComplexityImpactModel, price ComplexityImpactPrice, texts map[string]string) ComplexitySuggestionImpactReport {
	report := ComplexitySuggestionImpactReport{
		WindowSeconds: int64(window / time.Second), Requests: impact.Requests, Total: impact.Total, ChangedCount: len(impact.Changed),
		Changed: []ComplexityImpactChange{}, Attention: []ComplexityImpactAttention{},
	}
	for _, tier := range complexityImpactTiers {
		row := ComplexityImpactTier{Tier: tier, Live: impact.Live[tier], Draft: impact.Draft[tier]}
		if m, ok := models[tier]; ok && tier != logstore.ComplexitySuggestionUnmatchedTier {
			row.Model = &m
		}
		report.Tiers = append(report.Tiers, row)
	}
	for i, c := range impact.Changed {
		if i == maxComplexityImpactChanges {
			break
		}
		report.Changed = append(report.Changed, ComplexityImpactChange{ImpactChange: c, Text: texts[c.LogID]})
	}
	for _, a := range impact.Attention {
		report.Attention = append(report.Attention, ComplexityImpactAttention{ImpactAttention: a, Text: texts[a.LogID]})
	}

	byID := make(map[string]logstore.ComplexityReplayUsage, len(usage))
	for _, u := range usage {
		byID[u.ID] = u
	}
	var live, draft float64
	for _, c := range impact.Changed {
		u, found := byID[c.LogID]
		target, hasTarget := models[c.To]
		if !found || !hasTarget || c.To == logstore.ComplexitySuggestionUnmatchedTier {
			report.Unpriced++
			continue
		}
		// The live side is priced at the live tier's current model, as the
		// draft side is: the targets may have changed since the request was
		// served, and only the draft's tier change should move the cost. A
		// request no live target covers keeps the model that served it.
		liveProvider, liveModel := u.Provider, u.Model
		if current, ok := models[c.From]; ok && c.From != logstore.ComplexitySuggestionUnmatchedTier {
			liveProvider, liveModel = current.Provider, current.Model
		}
		before, okBefore := price(liveProvider, liveModel, u.PromptTokens, u.CompletionTokens)
		after, okAfter := price(target.Provider, target.Model, u.PromptTokens, u.CompletionTokens)
		if !okBefore || !okAfter {
			report.Unpriced++
			continue
		}
		report.Priced++
		live += before
		draft += after
	}
	// A sampled replay stands for the whole window.
	scale := 1.0
	if impact.Requests > 0 && impact.Total > impact.Requests {
		scale = float64(impact.Total) / float64(impact.Requests)
	}
	report.LiveCost, report.DraftCost = live*scale, draft*scale
	report.Savings = report.LiveCost - report.DraftCost
	return report
}

// complexityImpactLinearTokens bounds the requests a memoized price works out
// from per-token rates. List prices are linear below the long-context tiers
// (the lowest starts at 128k prompt tokens); past this bound a request is
// priced exactly.
const complexityImpactLinearTokens = 100_000

// complexityImpactProbeTokens is the token count rates are measured at, well
// below every long-context tier.
const complexityImpactProbeTokens = 1000

// MemoizeComplexityImpactPrice wraps a price lookup for one report. A replay
// prices every changed request twice, almost always at the same two or three
// models, so each provider/model is probed once (whether it has a price, its
// flat charge per request, and its rate per prompt and per completion token)
// and requests below the long-context tiers are priced from those. The flat
// charge is probed on its own, with no tokens, and taken out of the token
// rates, so it is added once per request and not scaled with its tokens.
// Larger requests take the exact lookup. Not safe for concurrent use: a report
// prices on one goroutine.
func MemoizeComplexityImpactPrice(exact ComplexityImpactPrice) ComplexityImpactPrice {
	type rates struct {
		base, prompt, completion float64
		ok                       bool
	}
	cache := map[string]rates{}
	return func(provider, model string, promptTokens, completionTokens int64) (float64, bool) {
		if promptTokens+completionTokens > complexityImpactLinearTokens {
			return exact(provider, model, promptTokens, completionTokens)
		}
		key := provider + "/" + model
		r, seen := cache[key]
		if !seen {
			base, ok := exact(provider, model, 0, 0)
			if ok {
				promptCost, _ := exact(provider, model, complexityImpactProbeTokens, 0)
				completionCost, _ := exact(provider, model, 0, complexityImpactProbeTokens)
				r = rates{base: base, prompt: (promptCost - base) / complexityImpactProbeTokens,
					completion: (completionCost - base) / complexityImpactProbeTokens, ok: true}
			}
			cache[key] = r
		}
		if !r.ok {
			return 0, false
		}
		return r.base + float64(promptTokens)*r.prompt + float64(completionTokens)*r.completion, true
	}
}
