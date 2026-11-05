package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
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
}

// defaultComplexitySuggestionRouter is the router suggestions belong to when
// a request names none: the only one a gateway has until it can define
// several.
const defaultComplexitySuggestionRouter = "default"

const (
	defaultComplexitySuggestionRunsLimit = 20
	maxComplexitySuggestionRunsLimit     = 200
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
	return view
}

// complexitySuggestionsResponse is the list payload.
type complexitySuggestionsResponse struct {
	RouterID string `json:"router_id"`
	// RouterVersion is the version the router serves now; a suggestion whose
	// own router_version differs is stale.
	RouterVersion string                     `json:"router_version,omitempty"`
	Suggestions   []complexitySuggestionView `json:"suggestions"`
	// Counts is the number of stored suggestions per status, whatever the
	// status filter selected.
	Counts    map[string]int                    `json:"counts"`
	Enabled   bool                              `json:"enabled"`
	LastRun   *logstore.ComplexitySuggestionRun `json:"last_run,omitempty"`
	NextRunAt *time.Time                        `json:"next_run_at,omitempty"`
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
// endpoints. They are new, so they have no /api/governance alias.
func (h *RoutingHandler) registerComplexitySuggestionRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handle := func(method, path string, handler fasthttp.RequestHandler) {
		r.Handle(method, path, lib.ChainMiddlewares(handler, middlewares...))
	}
	handle(fasthttp.MethodGet, "/api/routing/complexity-suggestions", h.listComplexitySuggestions)
	handle(fasthttp.MethodPost, "/api/routing/complexity-suggestions/test", h.testComplexitySuggestionPhrases)
	handle(fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs", h.listComplexitySuggestionRuns)
	handle(fasthttp.MethodGet, "/api/routing/complexity-suggestions/runs/estimate", h.estimateComplexitySuggestionRun)
	handle(fasthttp.MethodPost, "/api/routing/complexity-suggestions/runs", h.startComplexitySuggestionRun)
	handle(fasthttp.MethodGet, "/api/routing/complexity-suggestions/{suggestion_id}", h.getComplexitySuggestion)
	handle(fasthttp.MethodPut, "/api/routing/complexity-suggestions/{suggestion_id}/status", h.updateComplexitySuggestionStatus)
}

// suggestionManager returns the suggestion manager and its store, or answers
// 503 and returns false.
func (h *RoutingHandler) suggestionManager(ctx *fasthttp.RequestCtx) (ComplexitySuggestionManager, logstore.ComplexitySuggestionStore, bool) {
	manager, ok := h.routingManager.(ComplexitySuggestionManager)
	if !ok {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "phrase suggestions are not available on this deployment")
		return nil, nil, false
	}
	store := manager.ComplexitySuggestionStore()
	if store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "phrase suggestions need a log store; enable logging to use them")
		return nil, nil, false
	}
	return manager, store, true
}

// suggestionRouterID reads the router a request is about (?router_id=,
// default the gateway's one router) and its current version, or answers 404
// for a router that does not exist.
func (h *RoutingHandler) suggestionRouterID(ctx *fasthttp.RequestCtx, manager ComplexitySuggestionManager) (string, string, bool) {
	routerID := strings.TrimSpace(string(ctx.QueryArgs().Peek("router_id")))
	if routerID == "" {
		routerID = defaultComplexitySuggestionRouter
	}
	version, ok := manager.ComplexitySuggestionRouter(routerID)
	if !ok {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", routerID))
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

// suggestionSchedule reports whether suggestions are on and when the next
// scheduled run is. A config that cannot be read reports them off.
func (h *RoutingHandler) suggestionSchedule(ctx context.Context) (bool, *time.Time) {
	config, err := h.configStore.GetComplexityAnalyzerConfig(ctx)
	if err != nil || !config.SuggestionsEnabled() {
		return false, nil
	}
	next, err := config.Suggestions.EffectiveSchedule().NextSlot(time.Now())
	if err != nil {
		return true, nil
	}
	next = next.UTC()
	return true, &next
}

// listComplexitySuggestions lists stored suggestions, largest first, filtered
// by ?status= (comma-separated; default open).
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
			statuses = append(statuses, status)
		}
	}
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: routerID, OmitCentroids: true})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list suggestions: %v", err))
		return
	}
	response := complexitySuggestionsResponse{RouterID: routerID, RouterVersion: version, Suggestions: []complexitySuggestionView{}, Counts: map[string]int{}}
	for _, row := range rows {
		response.Counts[row.Status]++
		for _, status := range statuses {
			if row.Status == status {
				response.Suggestions = append(response.Suggestions, newComplexitySuggestionView(row, version))
				break
			}
		}
	}
	if runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: routerID}, 1); err == nil && len(runs) > 0 {
		response.LastRun = &runs[0]
	}
	response.Enabled, response.NextRunAt = h.suggestionSchedule(ctx)
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
	row, ok := h.loadComplexitySuggestion(ctx, store, routerID)
	if !ok {
		return
	}
	if !complexitySuggestionTransitionAllowed(row.Status, req.Status) {
		SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("a %s suggestion cannot be marked %q", row.Status, req.Status))
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
	// Written whole, with its centroid: the next run matches on it.
	if err := store.UpsertComplexitySuggestions(ctx, []logstore.ComplexitySuggestion{*row}); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update suggestion: %v", err))
		return
	}
	lib.RecordAuditChange(ctx, "complexity_suggestion."+req.Status, "complexity_suggestion", row.ID, strings.Join(row.Phrases(), ", "),
		map[string]any{"status": before}, map[string]any{"status": req.Status})
	SendJSON(ctx, newComplexitySuggestionView(*row, version))
}

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
	ids := append([]string(nil), req.LogIDs...)
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
		ids = append(rows[0].ExampleLogIDs(), ids...)
	}
	if len(ids) > suggestrun.MaxTestExamples {
		ids = ids[:suggestrun.MaxTestExamples]
	}
	texts, err := manager.ComplexitySuggestionTexts(ctx, routerID, ids)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read examples: %v", err))
		return
	}
	examples := make([]suggestrun.TestExample, 0, len(ids))
	for _, id := range ids {
		if text := texts[id]; text != "" {
			examples = append(examples, suggestrun.TestExample{LogID: id, Text: text})
		}
	}
	phrases := make([]complexity.SuggestionPhrase, len(req.Phrases))
	for i, p := range req.Phrases {
		phrases[i] = complexity.SuggestionPhrase{Tier: strings.ToUpper(strings.TrimSpace(p.Tier)), Text: strings.TrimSpace(p.Text)}
	}
	result, err := manager.TestComplexitySuggestionPhrases(ctx, routerID, phrases, examples)
	switch {
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
	_, next := h.suggestionSchedule(ctx)
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
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	case errors.Is(err, routing.ErrComplexitySuggestionsOff), errors.Is(err, routing.ErrComplexitySuggestionRunInFlight):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
		return
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to start suggestion run: %v", err))
		return
	}
	lib.RecordAuditChange(ctx, "complexity_suggestion_run.started", "complexity_suggestion_run", runID, runID, nil,
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
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
	case errors.Is(err, routing.ErrComplexitySuggestionsOff):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
	case errors.Is(err, complexity.ErrNoServingGeneration):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to estimate suggestion run: %v", err))
	default:
		SendJSON(ctx, estimate)
	}
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
