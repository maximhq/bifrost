package heimdall

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func wantAPIError(t *testing.T, name string, err error, status int, code string) {
	t.Helper()
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Errorf("%s: err = %v, want an API error", name, err)
		return
	}
	if apiErr.Status != status || apiErr.Code != code || apiErr.Message == "" {
		t.Errorf("%s: err = %+v, want %d %s with a message", name, apiErr, status, code)
	}
}

// withMoreProviders makes anthropic and perplexity configured too, pointing at the same fake.
func withMoreProviders(f *pluginFixture) {
	acct := f.fake.account()
	acct.add(schemas.Anthropic, f.fake.URL)
	acct.add(schemas.Perplexity, f.fake.URL)
	f.deps.Account = acct
}

func TestStartRun_RejectsABadRequest(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	withMoreProviders(f)
	start := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	zero := 0

	cases := []struct {
		name   string
		mutate func(*CreateRunRequest)
		status int
		code   string
	}{
		{"no candidate model", func(r *CreateRunRequest) { r.Candidate.Model = "" }, 400, "invalid_request"},
		{"no baseline provider", func(r *CreateRunRequest) { r.Baseline.Provider = "" }, 400, "invalid_request"},
		{"the same route twice", func(r *CreateRunRequest) { r.Candidate = r.Baseline }, 400, "invalid_request"},
		{"a provider that is not configured", func(r *CreateRunRequest) { r.Candidate.Provider = schemas.Mistral }, 400, "invalid_request"},
		{"a baseline provider that is not configured", func(r *CreateRunRequest) { r.Baseline.Provider = schemas.Mistral }, 400, "invalid_request"},
		{"perplexity", func(r *CreateRunRequest) {
			r.Candidate = Route{Provider: schemas.Perplexity, Model: "sonar"}
			r.DataEgressAck = true
		}, 400, "unsupported_candidate"},
		{"no budget", func(r *CreateRunRequest) { r.MaxUSD = 0 }, 400, "invalid_request"},
		{"a negative budget", func(r *CreateRunRequest) { r.MaxUSD = -1 }, 400, "invalid_request"},
		{"a zero call cap", func(r *CreateRunRequest) { r.MaxCalls = &zero }, 400, "invalid_request"},
		{"another provider without consent", func(r *CreateRunRequest) { r.Candidate = Route{Provider: schemas.Anthropic, Model: "claude"} }, 400, "egress_not_acknowledged"},
		{"no selection", func(r *CreateRunRequest) { r.Selection = Selection{} }, 400, "invalid_request"},
		{"a window with no end", func(r *CreateRunRequest) { r.Selection = Selection{StartTime: &start} }, 400, "invalid_request"},
		{"a window that ends before it starts", func(r *CreateRunRequest) {
			earlier := start.Add(-time.Hour)
			r.Selection = Selection{StartTime: &start, EndTime: &earlier}
		}, 400, "invalid_request"},
		{"more sessions than allowed", func(r *CreateRunRequest) {
			r.Selection.SessionIDs = make([]string, f.config.MaxSessions+1)
		}, 400, "invalid_request"},
		{"a limit above the maximum", func(r *CreateRunRequest) {
			end := start.Add(time.Hour)
			r.Selection = Selection{StartTime: &start, EndTime: &end, Limit: f.config.MaxSessions + 1}
		}, 400, "invalid_request"},
		{"a margin of one", func(r *CreateRunRequest) { r.Params.Margin = 1 }, 400, "invalid_request"},
		{"one baseline run", func(r *CreateRunRequest) { r.Params.BaselineRuns = 1 }, 400, "invalid_request"},
		{"a coverage above one", func(r *CreateRunRequest) { r.Params.MinCoverage = 2 }, 400, "invalid_request"},
		{"a tool with two rules", func(r *CreateRunRequest) { r.ToolRules = append(r.ToolRules, ToolRule{Tool: toolRefund}) }, 400, "invalid_request"},
		{"a rule with no tool", func(r *CreateRunRequest) { r.ToolRules = append(r.ToolRules, ToolRule{}) }, 400, "invalid_request"},
		{"an unknown match mode", func(r *CreateRunRequest) { r.ToolRules = []ToolRule{{Tool: "x", Match: "fuzzy"}} }, 400, "invalid_request"},
	}
	for _, tc := range cases {
		req := demoRequest("good", orders)
		tc.mutate(&req)
		run, err := f.StartRun(context.Background(), req)
		wantAPIError(t, tc.name, err, tc.status, tc.code)
		if run != nil {
			t.Errorf("%s: a run was created", tc.name)
		}
	}
	if page, _ := f.ListRuns(context.Background(), RunQuery{}); page.Total != 0 {
		t.Errorf("%d runs exist after only bad requests", page.Total)
	}
	if f.fake.count() != 0 {
		t.Error("a bad request reached the provider")
	}
}

func TestStartRun_AcceptsAnotherProviderWithConsent(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	withMoreProviders(f)
	req := demoRequest("good", orders)
	req.Candidate = Route{Provider: schemas.Anthropic, Model: "good"}
	req.DataEgressAck = true
	run, err := f.StartRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, f.Plugin, run.ID)
}

func TestStartRun_EstimatesTheCost(t *testing.T) {
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), nil)
	for _, r := range f.logs.rows {
		r.Cost = schemas.Ptr(0.01)
	}
	// 111 logged calls cost $1.11; six replays of each is about $6.66.
	req := demoRequest("good", orders)
	req.MaxUSD = 5
	run, err := f.StartRun(context.Background(), req)
	wantAPIError(t, "over budget", err, 409, "estimate_exceeds_budget")
	if err != nil && !strings.Contains(err.Error(), "6.66") {
		t.Errorf("the message should carry the estimate: %v", err)
	}
	if run != nil || f.fake.count() != 0 {
		t.Error("an over-budget run must not start")
	}

	req.MaxUSD = 10
	run, err = f.StartRun(context.Background(), req)
	if err != nil {
		t.Fatalf("within budget: %v", err)
	}
	// The call cap defaults to 1.5 times the estimated calls: 111 x 6 x 1.5.
	if run.MaxCalls != 999 || run.MaxUSD != 10 {
		t.Errorf("max calls %d, max usd %v; want 999 and 10", run.MaxCalls, run.MaxUSD)
	}
	waitForRun(t, f.Plugin, run.ID)

	// An explicit cap wins.
	f2 := newPluginFixture(t, orders[:3], demoScript(), nil)
	req = demoRequest("good", orders[:3])
	req.MaxCalls = schemas.Ptr(40)
	run, err = f2.StartRun(context.Background(), req)
	if err != nil || run.MaxCalls != 40 {
		t.Fatalf("explicit cap: %+v, %v", run, err)
	}
	waitForRun(t, f2.Plugin, run.ID)
}

func TestStartRun_WarnsWhenItCannotPriceTheCandidate(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	run, err := f.StartRun(context.Background(), demoRequest("good", orders))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Warnings) != 1 || !strings.Contains(run.Warnings[0], "no price") || !strings.Contains(run.Warnings[0], "call cap") {
		t.Errorf("warnings = %v; want one about the missing price and the call cap", run.Warnings)
	}
	waitForRun(t, f.Plugin, run.ID)
	// The warning is for the response that starts the run, not for later reads.
	if later, _ := f.GetRun(context.Background(), run.ID); len(later.Warnings) != 0 {
		t.Errorf("a later read carries warnings: %v", later.Warnings)
	}

	f2 := newPluginFixture(t, nil, demoScript(), nil)
	run, err = f2.StartRun(context.Background(), demoRequest("good", []int{1}))
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContain(run.Warnings, "matched no logged session") {
		t.Errorf("warnings = %v; want one about the empty selection", run.Warnings)
	}
	waitForRun(t, f2.Plugin, run.ID)

	// Some missing is reported as a count.
	f3 := newPluginFixture(t, orders, demoScript(), nil)
	req := demoRequest("good", orders)
	req.Selection.SessionIDs = append(req.Selection.SessionIDs, "ghost-1", "ghost-2")
	run, err = f3.StartRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContain(run.Warnings, "2 of 5 selected sessions were not found") {
		t.Errorf("warnings = %v; want one counting the missing sessions", run.Warnings)
	}
	waitForRun(t, f3.Plugin, run.ID)
}

func slicesContain(list []string, fragment string) bool {
	for _, s := range list {
		if strings.Contains(s, fragment) {
			return true
		}
	}
	return false
}

func TestStartRun_MergesParametersAndFixesTheSeed(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), func(c *Config) {
		c.Defaults = RunParams{Margin: 0.2, MinSessions: 3, CandidateRuns: 2}
	})
	ctx := context.Background()

	req := demoRequest("good", orders)
	req.Params = RunParams{MinSessions: 2} // the request wins over the configured default
	first, err := f.StartRun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, f.Plugin, first.ID)
	row, _ := f.store.getRun(ctx, first.ID)
	var got RunParams
	mustUnmarshal(t, row.ParamsJSON, &got)
	if got.MinSessions != 2 || got.Margin != 0.2 || got.CandidateRuns != 2 || got.BaselineRuns != 3 || got.Resamples != 2000 || got.MaxNoise != 0.30 {
		t.Errorf("stored params = %+v; want the request's, then the configured, then the built-in", got)
	}
	if got.Seed == 0 {
		t.Error("an unset seed must become a random one and be stored")
	}
	if run, _ := f.GetRun(ctx, first.ID); run.Progress.ReplaysTotal != 3*(3+2) {
		t.Errorf("replays total = %d, want 15 for 3 sessions and 3+2 runs each", run.Progress.ReplaysTotal)
	}

	// A seed given in the request is kept.
	req.Params.Seed = 777
	second, err := f.StartRun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, f.Plugin, second.ID)
	row, _ = f.store.getRun(ctx, second.ID)
	mustUnmarshal(t, row.ParamsJSON, &got)
	if got.Seed != 777 {
		t.Errorf("seed = %d, want 777", got.Seed)
	}
	// Two runs without a seed do not share one.
	req.Params.Seed = 0
	a, _ := f.StartRun(ctx, req)
	waitForRun(t, f.Plugin, a.ID)
	b, _ := f.StartRun(ctx, req)
	waitForRun(t, f.Plugin, b.ID)
	ra, _ := f.store.getRun(ctx, a.ID)
	rb, _ := f.store.getRun(ctx, b.ID)
	if ra.ParamsJSON == rb.ParamsJSON {
		t.Error("two runs got the same seed")
	}
}

func TestStartRun_SelectsByTimeWindowAndLimit(t *testing.T) {
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), nil)
	ctx := context.Background()
	start, end := t0.Add(-time.Hour), t0.Add(time.Hour)

	req := demoRequest("good", orders)
	req.Selection = Selection{StartTime: &start, EndTime: &end, Limit: 8}
	run := startAndWait(t, f.Plugin, req)
	if run.Selection.Selected != 8 || run.Progress.ReplaysTotal != 8*6 {
		t.Errorf("selection %+v, replays %+v; want the limit of 8 sessions", run.Selection, run.Progress)
	}
	req.Selection.Limit = 0
	run = startAndWait(t, f.Plugin, req)
	if run.Selection.Selected != 30 {
		t.Errorf("selected %d with no limit, want all 30", run.Selection.Selected)
	}
	_ = ctx
}

func mustUnmarshal(t *testing.T, raw string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}

func TestMergeParams(t *testing.T) {
	def := RunParams{Margin: 0.2}
	got := mergeParams(RunParams{Margin: 0.05, MinSessions: 10, Seed: 4}, def)
	d := DefaultRunParams()
	if got.Margin != 0.05 || got.MinSessions != 10 || got.Seed != 4 || got.BaselineRuns != d.BaselineRuns || got.MaxNoise != d.MaxNoise {
		t.Errorf("merged = %+v", got)
	}
	if got := mergeParams(RunParams{}, RunParams{}); got.Seed != 0 || got.Margin != d.Margin {
		t.Errorf("empty merge = %+v", got)
	}
}

func TestIDsAndSeeds(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := newID()
		if !uuid.MatchString(id) || len(id) != 36 || seen[id] {
			t.Fatalf("id %q is not a fresh version 4 UUID", id)
		}
		seen[id] = true
		if randomSeed() == 0 {
			t.Fatal("a random seed of zero")
		}
	}
}

// ---- reading ----

func TestListRunsAndGetRun(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	ctx := context.Background()

	_, err := f.GetRun(ctx, "nope")
	wantAPIError(t, "unknown run", err, 404, "not_found")

	var made []string
	for _, candidate := range []string{"good", "good", "fast"} {
		run := startAndWait(t, f.Plugin, demoRequest(candidate, orders))
		made = append(made, run.ID)
	}
	page, err := f.ListRuns(ctx, RunQuery{})
	if err != nil || page.Total != 3 || page.Count != 3 || page.Limit != 25 {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if got := ids(page.Runs); got[0] != made[2] || got[2] != made[0] {
		t.Errorf("order = %v, want newest first %v", got, made)
	}
	if page, _ := f.ListRuns(ctx, RunQuery{Limit: 1, Offset: 1}); page.Count != 1 || page.Total != 3 || page.Runs[0].ID != made[1] {
		t.Errorf("page 2 = %+v", page)
	}
	if page, _ := f.ListRuns(ctx, RunQuery{Limit: 10000}); page.Limit != 200 {
		t.Errorf("limit = %d, want it capped at 200", page.Limit)
	}
	if page, _ := f.ListRuns(ctx, RunQuery{Status: StatusComplete, Verdict: string(VerdictFail), Limit: 10}); page.Total != 1 || page.Runs[0].ID != made[2] {
		t.Errorf("filtered = %+v", page)
	}
	if page, _ := f.ListRuns(ctx, RunQuery{Offset: -5}); page.Offset != 0 {
		t.Errorf("a negative offset = %d", page.Offset)
	}
}

func TestListRunSessions(t *testing.T) {
	orders := allOrders()[:6]
	f := newPluginFixture(t, orders, demoScript(), nil)
	ctx := context.Background()
	run := startAndWait(t, f.Plugin, demoRequest("fast", orders)) // orders 2 and 5 are among its failures

	all, err := f.ListRunSessions(ctx, run.ID, RunSessionQuery{})
	if err != nil || all.Total != 6 || all.Limit != 25 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	critical, _ := f.ListRunSessions(ctx, run.ID, RunSessionQuery{Filter: FilterCritical})
	if critical.Total != 2 {
		t.Errorf("critical = %d, want 2", critical.Total)
	}
	diverged, _ := f.ListRunSessions(ctx, run.ID, RunSessionQuery{Filter: FilterDiverged})
	if diverged.Total < 2 {
		t.Errorf("diverged = %d, want at least the two critical ones", diverged.Total)
	}
	if ineligible, _ := f.ListRunSessions(ctx, run.ID, RunSessionQuery{Filter: FilterIneligible}); ineligible.Total != 0 {
		t.Errorf("ineligible = %d", ineligible.Total)
	}
	_, err = f.ListRunSessions(ctx, run.ID, RunSessionQuery{Filter: "everything"})
	wantAPIError(t, "bad filter", err, 400, "invalid_request")
	_, err = f.ListRunSessions(ctx, "nope", RunSessionQuery{})
	wantAPIError(t, "unknown run", err, 404, "not_found")
}

func TestGetRunSession(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	ctx := context.Background()
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))

	_, err := f.GetRunSession(ctx, run.ID, "nope")
	wantAPIError(t, "unknown session", err, 404, "not_found")
	_, err = f.GetRunSession(ctx, "nope", demoSessionID(1))
	wantAPIError(t, "unknown run", err, 404, "not_found")

	d, err := f.GetRunSession(ctx, run.ID, demoSessionID(1))
	if err != nil {
		t.Fatal(err)
	}
	if d.ContentExpired || len(d.Recorded) != 4 || len(d.RecordedCalls) != 3 || len(d.Replays) != 6 || d.Score == nil || !d.Score.Scored {
		t.Fatalf("detail = %+v", d)
	}
	if d.RecordedCalls[0].Name != toolLookup || d.RecordedCalls[0].Result != "order 1" {
		t.Errorf("recorded calls = %+v", d.RecordedCalls)
	}
	for _, r := range d.Replays {
		if r.Comparison == nil || len(r.Actions) != 4 || r.Outcome != ReplayFinished || r.Usage.PromptTokens == 0 {
			t.Errorf("replay = %+v", r)
		}
	}

	// After retention the content is gone and the scores stay.
	if _, _, err := f.store.expireContent(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	d, err = f.GetRunSession(ctx, run.ID, demoSessionID(1))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ContentExpired || len(d.Recorded) != 0 || d.Score == nil || len(d.Replays) != 6 {
		t.Fatalf("expired detail = %+v", d)
	}
	for _, r := range d.Replays {
		if len(r.Actions) != 0 || r.Comparison == nil || r.Outcome != ReplayFinished {
			t.Errorf("an expired replay = %+v; want the comparison kept and the actions gone", r)
		}
	}
	if kept, _ := f.GetRun(ctx, run.ID); kept.Decision == nil || kept.Decision.Verdict == "" {
		t.Error("the run lost its verdict")
	}
}

func TestSessionsAndStatus(t *testing.T) {
	orders := allOrders()[:5]
	f := newPluginFixture(t, orders, demoScript(), nil)
	ctx := context.Background()

	page, err := f.ListSessions(ctx, SessionQuery{Limit: 1000})
	if err != nil || page.Limit != 200 || page.Count != 5 {
		t.Fatalf("sessions = %+v, %v", page, err)
	}
	d, err := f.GetSession(ctx, demoSessionID(2))
	if err != nil || !d.Eligibility.Eligible() || d.Trace == nil || len(d.Trace.Actions) != 4 {
		t.Fatalf("session = %+v, %v", d, err)
	}
	if d.Trace.Cassette == nil || len(d.Trace.Cassette.Summary()) != 3 {
		t.Errorf("cassette = %+v", d.Trace.Cassette)
	}
	_, err = f.GetSession(ctx, "nope")
	wantAPIError(t, "unknown session", err, 404, "not_found")

	// An ineligible session is an answer with a reason and no trace.
	f.logs.rows[0].ContentHidden = true
	hidden, err := f.GetSession(ctx, *f.logs.rows[0].SessionID)
	if err != nil || hidden.Eligibility.Code != CodeContentHidden || hidden.Trace != nil {
		t.Errorf("hidden session = %+v, %v", hidden, err)
	}

	st, err := f.Status(ctx)
	if err != nil || !st.Enabled || st.ActiveRunID != nil || st.ActiveRollouts != 0 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if !st.Requirements.LogsStore || !st.Requirements.ConfigStore || !st.Requirements.ContentLogging || st.Requirements.ObjectStorageHiddenContent {
		t.Errorf("requirements = %+v", st.Requirements)
	}
	f.deps.ClientSettings = func() ClientSettings {
		return ClientSettings{ContentLoggingDisabled: true, ObjectStorageHiddenContent: true}
	}
	if st, _ := f.Status(ctx); st.Requirements.ContentLogging || !st.Requirements.ObjectStorageHiddenContent {
		t.Errorf("requirements with content logging off = %+v", st.Requirements)
	}
	if f.AllowUnauthenticatedMutations() {
		t.Error("unauthenticated mutations must default to off")
	}
}

func TestRunDetail_ShapesAStoredRun(t *testing.T) {
	verdict := "FAIL"
	row := &tables.TableHeimdallRun{
		ID: "r1", Name: "n", Status: StatusFailed, Stage: StageDone, BaselineProvider: "openai", BaselineModel: "a", CandidateProvider: "openai", CandidateModel: "b",
		ErrorCode: "store", ErrorMessage: "disk full", SessionsSelected: 4, SessionsEligible: 3, IneligibleJSON: `{"content_hidden":1}`,
		DecisionJSON: `{"verdict":"FAIL","reasons":["x"]}`, MetricsJSON: `{"baseline":{"mean_tokens":5},"candidate":{"mean_tokens":7},"tool_activity":{"cassette_hits":9,"stopped_calls":2}}`,
		Verdict: &verdict, ReplaysDone: 3, ReplaysTotal: 18, SpendUSD: 0.5, MaxUSD: 1, MaxCalls: 40,
	}
	d := runDetail(row)
	if d.Decision == nil || d.Decision.Verdict != VerdictFail || d.Error == nil || d.Error.Code != "store" || d.ToolActivity.StoppedCalls != 2 || d.Metrics.Candidate.MeanTokens != 7 {
		t.Errorf("detail = %+v", d)
	}
	if d.Selection.IneligibleByReason[CodeContentHidden] != 1 || d.Progress.ReplaysTotal != 18 || d.MaxCalls != 40 {
		t.Errorf("detail = %+v", d)
	}
	// A run that has not started has none of these, and empty maps rather than nil ones.
	fresh := runDetail(&tables.TableHeimdallRun{ID: "r2", Status: StatusQueued})
	if fresh.Decision != nil || fresh.Metrics != nil || fresh.Error != nil || fresh.Selection.IneligibleByReason == nil {
		t.Errorf("fresh = %+v", fresh)
	}
}

// pricedCatalog knows a price for every model.
type pricedCatalog struct{}

func (pricedCatalog) GetModelInfo(schemas.ModelProvider, string) *schemas.Model {
	return &schemas.Model{Pricing: &schemas.Pricing{}}
}

func (pricedCatalog) CalculateRequestCost(*schemas.BifrostContext, *schemas.BifrostResponse) float64 {
	return 0
}

func (pricedCatalog) CalculateRequestCostBreakdown(*schemas.BifrostContext, *schemas.BifrostResponse) *schemas.BifrostCost {
	return nil
}

func TestStartRun_NoPriceWarningWhenTheCatalogPricesTheCandidate(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	f.pricing = pricedCatalog{}
	run, err := f.StartRun(context.Background(), demoRequest("good", orders))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Warnings) != 0 {
		t.Errorf("warnings = %v; a priced candidate needs none", run.Warnings)
	}
	waitForRun(t, f.Plugin, run.ID)
}

func TestService_AFailingStoreIsAnInternalErrorNotANotFound(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	ctx := context.Background()
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))

	// The log store fails.
	f.logs.searchErr = errors.New("log store down")
	_, err := f.ListSessions(ctx, SessionQuery{})
	wantAPIError(t, "list sessions", err, 500, "internal")
	_, err = f.GetSession(ctx, demoSessionID(1))
	wantAPIError(t, "get session", err, 500, "internal")
	_, err = f.StartRun(ctx, demoRequest("good", orders))
	wantAPIError(t, "start run", err, 500, "internal")
	f.logs.searchErr = nil

	// The config store fails.
	if err := f.cs.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.GetRun(ctx, run.ID)
	wantAPIError(t, "get run", err, 500, "internal")
	_, err = f.ListRuns(ctx, RunQuery{})
	wantAPIError(t, "list runs", err, 500, "internal")
	_, err = f.ListRunSessions(ctx, run.ID, RunSessionQuery{})
	wantAPIError(t, "list run sessions", err, 500, "internal")
	_, err = f.GetRunSession(ctx, run.ID, demoSessionID(1))
	wantAPIError(t, "get run session", err, 500, "internal")
	_, err = f.CancelRun(ctx, run.ID)
	wantAPIError(t, "cancel run", err, 500, "internal")
	_, err = f.Status(ctx)
	wantAPIError(t, "status", err, 500, "internal")
}
