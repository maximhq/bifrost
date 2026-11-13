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
	ComplexitySuggestionTexts(ctx context.Context, logIDs []string) (map[string]string, error)
	StartComplexitySuggestionRun(ctx context.Context) (string, error)
	TestComplexitySuggestionPhrases(ctx context.Context, phrases []complexity.SuggestionPhrase, examples []suggestrun.TestExample) (suggestrun.PhraseTest, error)
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
}

func newComplexitySuggestionView(row logstore.ComplexitySuggestion) complexitySuggestionView {
	row.Centroid = nil
	view := complexitySuggestionView{ComplexitySuggestion: row, Phrases: row.Phrases(), ExampleLogIDs: row.ExampleLogIDs()}
	if view.Phrases == nil {
		view.Phrases = []string{}
	}
	if view.ExampleLogIDs == nil {
		view.ExampleLogIDs = []string{}
	}
	return view
}

// complexitySuggestionsResponse is the list payload: one page of the
// suggestions the status filter selects, how many it selects in all (Total),
// and how many are stored per status whatever the filter (Counts), so every
// list's count can be shown from one response.
type complexitySuggestionsResponse struct {
	Suggestions []complexitySuggestionView        `json:"suggestions"`
	Total       int                               `json:"total"`  // selected by the status filter, across every page
	Limit       int                               `json:"limit"`  // the page served
	Offset      int                               `json:"offset"` // the page served
	Counts      map[string]int                    `json:"counts"` // per status, whatever the filter selected
	Enabled     bool                              `json:"enabled"`
	LastRun     *logstore.ComplexitySuggestionRun `json:"last_run,omitempty"`
	NextRunAt   *time.Time                        `json:"next_run_at,omitempty"`
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

// suggestionSchedule reports whether suggestions are on and when the next
// scheduled run is. A config that cannot be read reports them off.
func (h *RoutingHandler) suggestionSchedule(ctx context.Context) (bool, *time.Time) {
	config, err := h.configStore.GetComplexityAnalyzerConfig(ctx)
	if err != nil || !config.SuggestionsEnabled() {
		return false, nil
	}
	// The schedule's state, once a node has set it, has the next run as
	// catch-ups and schedule changes left it.
	next := routing.NextComplexitySuggestionRun(ctx, h.configStore, config.Suggestions.EffectiveSchedule(), time.Now())
	return true, &next
}

// listComplexitySuggestions lists stored suggestions, largest first, filtered
// by ?status= (comma-separated; default open) and paged by ?limit= (default
// 100, at most 500) and ?offset=.
func (h *RoutingHandler) listComplexitySuggestions(ctx *fasthttp.RequestCtx) {
	_, store, ok := h.suggestionManager(ctx)
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
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{Statuses: statuses, OmitCentroids: true, Limit: limit, Offset: offset})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list suggestions: %v", err))
		return
	}
	counts, err := store.CountComplexitySuggestionsByStatus(ctx, logstore.ComplexitySuggestionFilter{})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to count suggestions: %v", err))
		return
	}
	response := complexitySuggestionsResponse{Suggestions: make([]complexitySuggestionView, 0, len(rows)), Counts: counts, Limit: limit, Offset: offset}
	for _, row := range rows {
		response.Suggestions = append(response.Suggestions, newComplexitySuggestionView(row))
	}
	for _, status := range statuses {
		response.Total += counts[status]
	}
	if runs, err := store.ListComplexitySuggestionRuns(ctx, nil, 1); err == nil && len(runs) > 0 {
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
	row, ok := h.loadComplexitySuggestion(ctx, store)
	if !ok {
		return
	}
	ids := row.ExampleLogIDs()
	texts, err := manager.ComplexitySuggestionTexts(ctx, ids)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read suggestion examples: %v", err))
		return
	}
	examples := make([]complexitySuggestionExample, 0, len(ids))
	for _, id := range ids {
		examples = append(examples, complexitySuggestionExample{LogID: id, Text: texts[id]})
	}
	SendJSON(ctx, complexitySuggestionDetailResponse{Suggestion: newComplexitySuggestionView(*row), Examples: examples})
}

// updateComplexitySuggestionStatus records the user's call on a suggestion:
// accepted (its phrases were added to the bank) or dismissed, from open; or
// open again, undoing either. A run leaves accepted and dismissed
// suggestions alone, so the call sticks.
func (h *RoutingHandler) updateComplexitySuggestionStatus(ctx *fasthttp.RequestCtx) {
	_, store, ok := h.suggestionManager(ctx)
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
	row, ok := h.loadComplexitySuggestion(ctx, store)
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
	SendJSON(ctx, newComplexitySuggestionView(*row))
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
		rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{IDs: []string{req.SuggestionID}, OmitCentroids: true})
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
		texts, err := manager.ComplexitySuggestionTexts(ctx, batch)
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
	result, err := manager.TestComplexitySuggestionPhrases(ctx, phrases, examples)
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
	_, store, ok := h.suggestionManager(ctx)
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
	runs, err := store.ListComplexitySuggestionRuns(ctx, nil, limit)
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
	runID, err := manager.StartComplexitySuggestionRun(ctx)
	switch {
	case errors.Is(err, routing.ErrComplexitySuggestionsOff), errors.Is(err, routing.ErrComplexitySuggestionRunInFlight):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrRoutingPluginUnavailable), errors.Is(err, complexity.ErrNoServingGeneration):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to start suggestion run: %v", err))
		return
	}
	lib.RecordAuditChange(ctx, "model_routing_suggestion_run.started", "model_routing_suggestion_run", runID, runID, nil, map[string]any{"run_id": runID})
	SendJSONWithStatus(ctx, map[string]string{"run_id": runID}, fasthttp.StatusAccepted)
}

// loadComplexitySuggestion reads the suggestion named in the path, with its
// centroid, or answers and returns false.
func (h *RoutingHandler) loadComplexitySuggestion(ctx *fasthttp.RequestCtx, store logstore.ComplexitySuggestionStore) (*logstore.ComplexitySuggestion, bool) {
	id, _ := ctx.UserValue("suggestion_id").(string)
	if strings.TrimSpace(id) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "suggestion id is required")
		return nil, false
	}
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{IDs: []string{id}})
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
