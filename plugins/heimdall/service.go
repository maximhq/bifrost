package heimdall

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// Error is an API error: the status to answer with and a code a client can switch on.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func apiError(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// internal wraps a failure of the store or the log store, which the client cannot fix.
func internal(err error) *Error {
	return &Error{Status: 500, Code: "internal", Message: err.Error()}
}

// ---- requests and responses ----

// Selection says which sessions a run covers: the listed ones, or those in a time window.
type Selection struct {
	SessionIDs []string   `json:"session_ids"`
	StartTime  *time.Time `json:"start_time"`
	EndTime    *time.Time `json:"end_time"`
	Limit      int        `json:"limit"`
}

// CreateRunRequest starts a run.
type CreateRunRequest struct {
	Name           string     `json:"name"`
	Baseline       Route      `json:"baseline"`
	Candidate      Route      `json:"candidate"`
	Selection      Selection  `json:"selection"`
	Params         RunParams  `json:"params"`
	ToolRules      []ToolRule `json:"tool_rules"`
	ForbiddenTools []string   `json:"forbidden_tools"`
	MaxUSD         float64    `json:"max_usd"`
	// MaxCalls caps model calls. Nil means 1.5 times the estimate.
	MaxCalls *int `json:"max_calls"`
	// DataEgressAck must be true when the candidate's provider differs from the baseline's: the recorded
	// conversations are sent to it.
	DataEgressAck bool   `json:"data_egress_ack"`
	CreatedBy     string `json:"-"`
}

// Requirements are the gateway settings Heimdall needs.
type Requirements struct {
	LogsStore                  bool `json:"logs_store"`
	ContentLogging             bool `json:"content_logging"`
	ConfigStore                bool `json:"config_store"`
	ObjectStorageHiddenContent bool `json:"object_storage_hidden_content"`
}

// Status says whether Heimdall can work here.
type Status struct {
	Enabled        bool         `json:"enabled"`
	ActiveRunID    *string      `json:"active_run_id"`
	ActiveRollouts int          `json:"active_rollouts"`
	Requirements   Requirements `json:"requirements"`
}

// SessionDetail is one logged session, with its trace when it can be replayed.
type SessionDetail struct {
	SessionID   string        `json:"session_id"`
	Eligibility Eligibility   `json:"eligibility"`
	Trace       *SessionTrace `json:"trace,omitempty"`
}

// RunSelection is what a run covered.
type RunSelection struct {
	Selected           int            `json:"selected"`
	Eligible           int            `json:"eligible"`
	IneligibleByReason map[string]int `json:"ineligible_by_reason"`
}

// RunProgress counts replays.
type RunProgress struct {
	ReplaysDone  int `json:"replays_done"`
	ReplaysTotal int `json:"replays_total"`
}

// ToolActivity counts how the cassette answered tool calls across the run.
type ToolActivity struct {
	CassetteHits int `json:"cassette_hits"`
	StoppedCalls int `json:"stopped_calls"`
}

// ArmMetrics are means per replay, shown beside the verdict and not part of it.
type ArmMetrics struct {
	MeanLatencyMS float64 `json:"mean_latency_ms"`
	MeanCostUSD   float64 `json:"mean_cost_usd"`
	MeanTokens    float64 `json:"mean_tokens"`
}

// RunMetrics are the means of each arm.
type RunMetrics struct {
	Baseline  ArmMetrics `json:"baseline"`
	Candidate ArmMetrics `json:"candidate"`
}

// RunError says why a run did not finish.
type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RunDetail is a run as the API shows it.
type RunDetail struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Status       string        `json:"status"`
	Stage        string        `json:"stage"`
	Baseline     Route         `json:"baseline"`
	Candidate    Route         `json:"candidate"`
	Progress     RunProgress   `json:"progress"`
	Selection    RunSelection  `json:"selection"`
	SpendUSD     float64       `json:"spend_usd"`
	MaxUSD       float64       `json:"max_usd"`
	MaxCalls     int           `json:"max_calls"`
	Decision     *GateDecision `json:"decision"`
	ToolActivity *ToolActivity `json:"tool_activity,omitempty"`
	Metrics      *RunMetrics   `json:"metrics,omitempty"`
	Error        *RunError     `json:"error,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	StartedAt    *time.Time    `json:"started_at"`
	FinishedAt   *time.Time    `json:"finished_at"`
}

// RunPage is a page of runs, newest first.
type RunPage struct {
	Runs   []RunDetail `json:"runs"`
	Count  int         `json:"count"`
	Total  int64       `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

// RunSessionRow is one session of a run in a list.
type RunSessionRow struct {
	SessionID           string   `json:"session_id"`
	Eligibility         string   `json:"eligibility"`
	BaselineDivergence  *float64 `json:"baseline_divergence"`
	CandidateDivergence *float64 `json:"candidate_divergence"`
	Delta               *float64 `json:"delta"`
	CriticalCount       int      `json:"critical_count"`
	FirstDivergence     int      `json:"first_divergence"`
}

// RunSessionPage is a page of a run's sessions.
type RunSessionPage struct {
	Sessions []RunSessionRow `json:"sessions"`
	Count    int             `json:"count"`
	Total    int64           `json:"total"`
	Limit    int             `json:"limit"`
	Offset   int             `json:"offset"`
}

// ReplayDetail is one replay of a session with its comparison.
type ReplayDetail struct {
	Arm        Arm           `json:"arm"`
	Attempt    int           `json:"attempt"`
	Outcome    ReplayOutcome `json:"outcome"`
	Actions    []Action      `json:"actions"` // nil once the run's content has expired
	Comparison *Comparison   `json:"comparison"`
	Usage      Usage         `json:"usage"`
	Error      string        `json:"error,omitempty"`
}

// RunSessionDetail is everything the side-by-side view of one session needs.
type RunSessionDetail struct {
	SessionID      string         `json:"session_id"`
	Eligibility    string         `json:"eligibility"`
	ContentExpired bool           `json:"content_expired"` // the recording and the replay actions were cleared by retention
	Recorded       []Action       `json:"recorded"`
	RecordedCalls  []RecordedCall `json:"recorded_calls,omitempty"`
	Score          *SessionScore  `json:"score"`
	Replays        []ReplayDetail `json:"replays"`
}

// ---- sessions ----

// ListSessions lists logged sessions newest first, without their eligibility, which needs the full rows.
func (p *Plugin) ListSessions(ctx context.Context, q SessionQuery) (*SessionPage, error) {
	if q.Limit > 200 {
		q.Limit = 200
	}
	page, err := p.recon.ListSessions(ctx, q)
	if err != nil {
		return nil, internal(err)
	}
	return page, nil
}

// GetSession rebuilds one session and says whether it can be replayed.
func (p *Plugin) GetSession(ctx context.Context, id string) (*SessionDetail, error) {
	trace, elig, err := p.recon.Build(ctx, id)
	if err != nil {
		return nil, internal(err)
	}
	if elig.Code == CodeUnknownSession {
		return nil, apiError(404, "not_found", "no session %q in the logs", id)
	}
	return &SessionDetail{SessionID: id, Eligibility: elig, Trace: trace}, nil
}

// ---- status ----

// Status reports whether Heimdall has what it needs and whether a run is going.
func (p *Plugin) Status(ctx context.Context) (*Status, error) {
	st := &Status{Enabled: true, Requirements: Requirements{LogsStore: p.deps.LogStore != nil, ConfigStore: p.deps.ConfigStore != nil, ContentLogging: true}}
	if p.deps.ClientSettings != nil {
		settings := p.deps.ClientSettings()
		st.Requirements.ContentLogging = !settings.ContentLoggingDisabled
		st.Requirements.ObjectStorageHiddenContent = settings.ObjectStorageHiddenContent
	}
	active, err := p.store.activeRun(ctx)
	if err != nil {
		return nil, internal(err)
	}
	if active != nil {
		st.ActiveRunID = &active.ID
	}
	return st, nil
}

// AllowUnauthenticatedMutations says whether POST routes may be used when dashboard auth is off.
func (p *Plugin) AllowUnauthenticatedMutations() bool { return p.config.AllowUnauthMutations }

// ---- starting a run ----

// StartRun validates a request, estimates its cost, and starts the run in the background. The returned run is queued.
func (p *Plugin) StartRun(ctx context.Context, req CreateRunRequest) (*RunDetail, error) {
	params, rules, err := p.validate(&req)
	if err != nil {
		return nil, err
	}

	summaries, err := p.selectSessions(ctx, req.Selection)
	if err != nil {
		return nil, internal(err)
	}
	var passCost float64
	var passCalls int
	for _, s := range summaries {
		passCost += s.TotalCost
		passCalls += s.Turns
	}
	replays := params.BaselineRuns + params.CandidateRuns
	estimateUSD := passCost * float64(replays)
	estimatedCalls := passCalls * replays
	maxCalls := int(math.Ceil(1.5 * float64(estimatedCalls)))
	if req.MaxCalls != nil {
		maxCalls = *req.MaxCalls
	}
	if estimateUSD > req.MaxUSD {
		return nil, apiError(409, "estimate_exceeds_budget", "the run is estimated to cost about $%.2f (%d replays of %d sessions) and the budget is $%.2f", estimateUSD, replays, len(summaries), req.MaxUSD)
	}
	var warnings []string
	missing := 0
	for _, s := range summaries {
		if s.Turns == 0 {
			missing++
		}
	}
	switch {
	case missing == len(summaries):
		warnings = append(warnings, "the selection matched no logged session")
	case missing > 0:
		warnings = append(warnings, fmt.Sprintf("%d of %d selected sessions were not found in the logs and will be reported as %s", missing, len(summaries), CodeUnknownSession))
	}
	if !p.hasPrice(req.Candidate) {
		warning := fmt.Sprintf("the model catalog has no price for %s/%s, so its cost is not tracked", req.Candidate.Provider, req.Candidate.Model)
		if maxCalls > 0 {
			warning += fmt.Sprintf(" and only the call cap (%d) limits the run", maxCalls)
		}
		warnings = append(warnings, warning)
	}

	selectionJSON, _ := json.Marshal(req.Selection)
	paramsJSON, _ := json.Marshal(params)
	rulesJSON, _ := json.Marshal(rules)
	row := &tables.TableHeimdallRun{
		ID: newID(), Name: strings.TrimSpace(req.Name), Status: StatusQueued, Stage: StagePlanning,
		BaselineProvider: string(req.Baseline.Provider), BaselineModel: req.Baseline.Model,
		CandidateProvider: string(req.Candidate.Provider), CandidateModel: req.Candidate.Model,
		SelectionJSON: string(selectionJSON), ParamsJSON: string(paramsJSON), RulesJSON: string(rulesJSON),
		MaxUSD: req.MaxUSD, MaxCalls: maxCalls, CreatedBy: req.CreatedBy, CreatedAt: p.now(),
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.store.createRunIfIdle(ctx, row); err != nil {
		if errors.Is(err, errRunActive) {
			active, _ := p.store.activeRun(ctx)
			id := ""
			if active != nil {
				id = active.ID
			}
			return nil, apiError(409, "run_active", "run %s is still going; one run at a time", id)
		}
		return nil, internal(err)
	}
	p.launchLocked(row.ID)

	detail := runDetail(row)
	detail.Warnings = warnings
	return detail, nil
}

// validate checks a request and fills its numbers: the request's own, then the configured defaults, then the
// built-in ones. It returns the parameters and tool rules the run will use.
func (p *Plugin) validate(req *CreateRunRequest) (RunParams, ToolRules, error) {
	bad := func(format string, args ...any) error { return apiError(400, "invalid_request", format, args...) }

	if req.Baseline.Provider == "" || req.Baseline.Model == "" || req.Candidate.Provider == "" || req.Candidate.Model == "" {
		return RunParams{}, ToolRules{}, bad("baseline and candidate each need a provider and a model")
	}
	if req.Baseline == req.Candidate {
		return RunParams{}, ToolRules{}, bad("baseline and candidate are the same route")
	}
	configured, err := p.deps.Account.GetConfiguredProviders()
	if err != nil {
		return RunParams{}, ToolRules{}, internal(err)
	}
	for _, r := range []Route{req.Baseline, req.Candidate} {
		if !slices.Contains(configured, r.Provider) {
			return RunParams{}, ToolRules{}, bad("provider %q is not configured", r.Provider)
		}
	}
	if err := CheckCandidate(req.Candidate); err != nil {
		var denied *UnsupportedCandidateError
		if errors.As(err, &denied) {
			return RunParams{}, ToolRules{}, apiError(400, "unsupported_candidate", "provider %q cannot be replayed: %s", denied.Provider, denied.Reason)
		}
		return RunParams{}, ToolRules{}, err
	}
	if req.MaxUSD <= 0 {
		return RunParams{}, ToolRules{}, bad("max_usd must be above 0")
	}
	if req.MaxCalls != nil && *req.MaxCalls <= 0 {
		return RunParams{}, ToolRules{}, bad("max_calls must be above 0 when set")
	}
	if req.Candidate.Provider != req.Baseline.Provider && !req.DataEgressAck {
		return RunParams{}, ToolRules{}, apiError(400, "egress_not_acknowledged", "the recorded conversations would be sent to %q; set data_egress_ack to confirm", req.Candidate.Provider)
	}

	sel := req.Selection
	switch {
	case len(sel.SessionIDs) > 0:
		if len(sel.SessionIDs) > p.config.MaxSessions {
			return RunParams{}, ToolRules{}, bad("%d sessions selected; the limit is %d", len(sel.SessionIDs), p.config.MaxSessions)
		}
	case sel.StartTime != nil && sel.EndTime != nil:
		if !sel.EndTime.After(*sel.StartTime) {
			return RunParams{}, ToolRules{}, bad("end_time must be after start_time")
		}
	default:
		return RunParams{}, ToolRules{}, bad("select sessions by session_ids, or by start_time and end_time")
	}
	if sel.Limit < 0 || sel.Limit > p.config.MaxSessions {
		return RunParams{}, ToolRules{}, bad("limit must be between 0 and %d", p.config.MaxSessions)
	}

	seen := map[string]bool{}
	for _, tr := range req.ToolRules {
		if tr.Tool == "" {
			return RunParams{}, ToolRules{}, bad("a tool rule has no tool")
		}
		if seen[tr.Tool] {
			return RunParams{}, ToolRules{}, bad("tool %q has more than one rule", tr.Tool)
		}
		seen[tr.Tool] = true
		if tr.Match != "" && tr.Match != MatchNameOnly {
			return RunParams{}, ToolRules{}, bad("tool %q: match must be empty or %q", tr.Tool, MatchNameOnly)
		}
		if tr.MaxCalls != nil && *tr.MaxCalls < 0 {
			return RunParams{}, ToolRules{}, bad("tool %q: max_calls must not be negative", tr.Tool)
		}
	}

	params := mergeParams(req.Params, p.config.Defaults)
	if params.Seed == 0 {
		params.Seed = randomSeed()
	}
	if err := params.Validate(); err != nil {
		return RunParams{}, ToolRules{}, bad("%v", err)
	}
	return params, ToolRules{Rules: req.ToolRules, ForbiddenTools: req.ForbiddenTools}, nil
}

// mergeParams takes each number from the request, or from the defaults where the request leaves it unset.
func mergeParams(req, def RunParams) RunParams {
	def = def.withDefaults()
	pick := func(v, d int) int {
		if v == 0 {
			return d
		}
		return v
	}
	pickF := func(v, d float64) float64 {
		if v == 0 {
			return d
		}
		return v
	}
	return RunParams{
		BaselineRuns:        pick(req.BaselineRuns, def.BaselineRuns),
		CandidateRuns:       pick(req.CandidateRuns, def.CandidateRuns),
		Margin:              pickF(req.Margin, def.Margin),
		MinSessions:         pick(req.MinSessions, def.MinSessions),
		MinCoverage:         pickF(req.MinCoverage, def.MinCoverage),
		MaxNoise:            pickF(req.MaxNoise, def.MaxNoise),
		MaxCriticalSessions: pick(req.MaxCriticalSessions, def.MaxCriticalSessions),
		Resamples:           pick(req.Resamples, def.Resamples),
		Seed:                req.Seed,
	}
}

// hasPrice reports whether the catalog can price a route.
func (p *Plugin) hasPrice(r Route) bool {
	if p.pricing == nil {
		return false
	}
	info := p.pricing.GetModelInfo(r.Provider, r.Model)
	return info != nil && info.Pricing != nil
}

// selectSessions lists the sessions a selection covers, with their size and cost as logged.
func (p *Plugin) selectSessions(ctx context.Context, sel Selection) ([]SessionSummary, error) {
	if len(sel.SessionIDs) > 0 {
		var out []SessionSummary
		seen := map[string]bool{}
		for _, id := range sel.SessionIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			s, found, err := p.recon.Summary(ctx, id)
			if err != nil {
				return nil, err
			}
			if !found {
				// Kept in the run, where it is reported as unknown_session.
				s = SessionSummary{SessionID: id}
			}
			out = append(out, s)
		}
		return out, nil
	}
	limit := sel.Limit
	if limit == 0 {
		limit = p.config.MaxSessions
	}
	page, err := p.recon.ListSessions(ctx, SessionQuery{StartTime: sel.StartTime, EndTime: sel.EndTime, Limit: limit})
	if err != nil {
		return nil, err
	}
	return page.Sessions, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func randomSeed() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	if seed := binary.BigEndian.Uint64(b[:]); seed != 0 {
		return seed
	}
	return 1
}

// ---- reading runs ----

// GetRun returns one run.
func (p *Plugin) GetRun(ctx context.Context, id string) (*RunDetail, error) {
	row, err := p.store.getRun(ctx, id)
	if err != nil {
		return nil, storeError(err, "run", id)
	}
	return runDetail(row), nil
}

// ListRuns lists runs newest first.
func (p *Plugin) ListRuns(ctx context.Context, q RunQuery) (*RunPage, error) {
	q.Limit, q.Offset = pageBounds(q.Limit, q.Offset)
	rows, total, err := p.store.listRuns(ctx, q)
	if err != nil {
		return nil, internal(err)
	}
	page := &RunPage{Runs: make([]RunDetail, len(rows)), Total: total, Limit: q.Limit, Offset: q.Offset}
	for i := range rows {
		page.Runs[i] = *runDetail(&rows[i])
	}
	page.Count = len(page.Runs)
	return page, nil
}

// ListRunSessions lists a run's sessions.
func (p *Plugin) ListRunSessions(ctx context.Context, id string, q RunSessionQuery) (*RunSessionPage, error) {
	if _, err := p.store.getRun(ctx, id); err != nil {
		return nil, storeError(err, "run", id)
	}
	switch q.Filter {
	case "", FilterAll, FilterCritical, FilterDiverged, FilterIneligible:
	default:
		return nil, apiError(400, "invalid_request", "filter must be all, critical, diverged or ineligible")
	}
	q.Limit, q.Offset = pageBounds(q.Limit, q.Offset)
	rows, total, err := p.store.listRunSessions(ctx, id, q)
	if err != nil {
		return nil, internal(err)
	}
	page := &RunSessionPage{Sessions: make([]RunSessionRow, len(rows)), Total: total, Limit: q.Limit, Offset: q.Offset}
	for i, r := range rows {
		page.Sessions[i] = RunSessionRow{SessionID: r.SessionID, Eligibility: r.Eligibility, BaselineDivergence: r.BaselineDivergence,
			CandidateDivergence: r.CandidateDivergence, Delta: r.Delta, CriticalCount: r.CriticalCount, FirstDivergence: r.FirstDivergence}
	}
	page.Count = len(page.Sessions)
	return page, nil
}

// GetRunSession returns what the side-by-side view of one session needs: the recording, every replay with its
// comparison, and the score. After retention the recording and the replay actions are gone and the rest stays.
func (p *Plugin) GetRunSession(ctx context.Context, id, sessionID string) (*RunSessionDetail, error) {
	row, err := p.store.getRunSession(ctx, id, sessionID)
	if err != nil {
		return nil, storeError(err, "session", sessionID)
	}
	detail := &RunSessionDetail{SessionID: row.SessionID, Eligibility: row.Eligibility, Replays: []ReplayDetail{}}
	if row.ScoreJSON != "" {
		var score SessionScore
		if json.Unmarshal([]byte(row.ScoreJSON), &score) == nil {
			detail.Score = &score
		}
	}
	if row.SnapshotJSON != nil {
		if trace, err := RestoreTrace([]byte(*row.SnapshotJSON)); err == nil {
			detail.Recorded, detail.RecordedCalls = trace.Actions, trace.Recorded()
		}
	} else if row.Eligibility == CodeEligible {
		detail.ContentExpired = true
	}

	replays, err := p.store.listReplays(ctx, id, sessionID)
	if err != nil {
		return nil, internal(err)
	}
	for _, r := range replays {
		d := ReplayDetail{Arm: Arm(r.Arm), Attempt: r.Attempt, Outcome: ReplayOutcome(r.Outcome), Error: r.ErrorMessage,
			Usage: Usage{Calls: 0, PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, TotalTokens: r.PromptTokens + r.CompletionTokens, CostUSD: r.CostUSD, LatencyMS: r.LatencyMS}}
		if r.ActionsJSON != nil {
			_ = json.Unmarshal([]byte(*r.ActionsJSON), &d.Actions)
		}
		if r.ComparisonJSON != "" {
			var c Comparison
			if json.Unmarshal([]byte(r.ComparisonJSON), &c) == nil {
				d.Comparison = &c
			}
		}
		detail.Replays = append(detail.Replays, d)
	}
	return detail, nil
}

func storeError(err error, what, id string) error {
	if errors.Is(err, errNotFound) {
		return apiError(404, "not_found", "no %s %q", what, id)
	}
	return internal(err)
}

// pageBounds applies the default and the maximum page size.
func pageBounds(limit, offset int) (int, int) {
	switch {
	case limit <= 0:
		limit = 25
	case limit > 200:
		limit = 200
	}
	return limit, max(offset, 0)
}

// runDetail shapes a stored run for the API.
func runDetail(r *tables.TableHeimdallRun) *RunDetail {
	d := &RunDetail{
		ID: r.ID, Name: r.Name, Status: r.Status, Stage: r.Stage,
		Baseline:  Route{Provider: schemas.ModelProvider(r.BaselineProvider), Model: r.BaselineModel},
		Candidate: Route{Provider: schemas.ModelProvider(r.CandidateProvider), Model: r.CandidateModel},
		Progress:  RunProgress{ReplaysDone: r.ReplaysDone, ReplaysTotal: r.ReplaysTotal},
		Selection: RunSelection{Selected: r.SessionsSelected, Eligible: r.SessionsEligible, IneligibleByReason: map[string]int{}},
		SpendUSD:  r.SpendUSD, MaxUSD: r.MaxUSD, MaxCalls: r.MaxCalls,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if r.IneligibleJSON != "" {
		_ = json.Unmarshal([]byte(r.IneligibleJSON), &d.Selection.IneligibleByReason)
	}
	if r.DecisionJSON != "" {
		var dec GateDecision
		if json.Unmarshal([]byte(r.DecisionJSON), &dec) == nil {
			d.Decision = &dec
		}
	}
	if r.MetricsJSON != "" {
		var m storedMetrics
		if json.Unmarshal([]byte(r.MetricsJSON), &m) == nil {
			d.Metrics, d.ToolActivity = &RunMetrics{Baseline: m.Baseline, Candidate: m.Candidate}, &m.ToolActivity
		}
	}
	if r.ErrorCode != "" {
		d.Error = &RunError{Code: r.ErrorCode, Message: r.ErrorMessage}
	}
	return d
}

// storedMetrics is what a run keeps in metrics_json.
type storedMetrics struct {
	Baseline     ArmMetrics   `json:"baseline"`
	Candidate    ArmMetrics   `json:"candidate"`
	ToolActivity ToolActivity `json:"tool_activity"`
}

// CancelRun stops the run in progress. The run ends as cancelled with the replays it had finished kept and no
// verdict. It waits briefly for the run to settle.
func (p *Plugin) CancelRun(ctx context.Context, id string) (*RunDetail, error) {
	row, err := p.store.getRun(ctx, id)
	if err != nil {
		return nil, storeError(err, "run", id)
	}
	if row.Status != StatusQueued && row.Status != StatusRunning {
		return nil, apiError(409, "run_not_active", "run %s is %s", id, row.Status)
	}
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	if active == nil || active.id != id {
		return nil, apiError(409, "run_not_active", "run %s is not being worked on by this process", id)
	}
	active.cancel(errCancelled)
	select {
	case <-active.done:
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
	}
	return p.GetRun(ctx, id)
}
