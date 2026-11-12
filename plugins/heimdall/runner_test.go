package heimdall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

func ids(runs []RunDetail) []string {
	var out []string
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

// storedGateInput rebuilds what the gate decided from, using only what the run stored.
func storedGateInput(t *testing.T, f *pluginFixture, runID string) GateInput {
	t.Helper()
	ctx := context.Background()
	row, err := f.store.getRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var params RunParams
	if err := json.Unmarshal([]byte(row.ParamsJSON), &params); err != nil {
		t.Fatal(err)
	}
	sessions, _, err := f.store.listRunSessions(ctx, runID, RunSessionQuery{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	in := GateInput{Params: params, Selected: row.SessionsSelected}
	for _, s := range sessions {
		if s.ScoreJSON == "" {
			continue
		}
		var score SessionScore
		if err := json.Unmarshal([]byte(s.ScoreJSON), &score); err != nil {
			t.Fatal(err)
		}
		in.Sessions = append(in.Sessions, score)
	}
	for _, r := range mustReplays(t, f, runID) {
		if r.Outcome == string(ReplayBudgetStopped) {
			in.BudgetStopped = true
		}
	}
	return in
}

func mustReplays(t *testing.T, f *pluginFixture, runID string) []tables.TableHeimdallReplay {
	t.Helper()
	rows, err := f.store.listReplays(context.Background(), runID, "")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRunner_AGoodModelPassesAndEverythingIsStored(t *testing.T) {
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), nil)
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))

	if run.Status != StatusComplete || run.Stage != StageDone || run.Error != nil {
		t.Fatalf("run = %+v", run)
	}
	if run.Decision == nil || run.Decision.Verdict != VerdictPass || run.Decision.ScoredSessions != 30 || run.Decision.CriticalSessions != 0 {
		t.Fatalf("decision = %+v", run.Decision)
	}
	if run.Selection.Selected != 30 || run.Selection.Eligible != 30 || len(run.Selection.IneligibleByReason) != 0 {
		t.Errorf("selection = %+v", run.Selection)
	}
	if run.Progress.ReplaysTotal != 180 || run.Progress.ReplaysDone != 180 {
		t.Errorf("progress = %+v, want 180 of 180", run.Progress)
	}
	if run.StartedAt == nil || run.FinishedAt == nil || run.FinishedAt.Before(*run.StartedAt) {
		t.Errorf("times = %v .. %v", run.StartedAt, run.FinishedAt)
	}
	if run.Metrics == nil || run.ToolActivity == nil || run.ToolActivity.CassetteHits == 0 || run.ToolActivity.StoppedCalls != 0 {
		t.Errorf("metrics %+v, tool activity %+v", run.Metrics, run.ToolActivity)
	}
	if run.Metrics.Baseline.MeanTokens <= 0 || run.Metrics.Candidate.MeanLatencyMS < 0 {
		t.Errorf("metrics = %+v", run.Metrics)
	}
	if run.Decision.ZeroRateUpperBound == nil {
		t.Error("a pass shows what 30 clean sessions can promise")
	}

	// Every replay was stored, with its comparison.
	replays := mustReplays(t, f, run.ID)
	if len(replays) != 180 {
		t.Fatalf("%d replays stored, want 180", len(replays))
	}
	for _, r := range replays {
		if r.ComparisonJSON == "" || r.Divergence == nil || r.ActionsJSON == nil || r.Outcome != string(ReplayFinished) {
			t.Fatalf("replay %s/%s/%d = %+v", r.SessionID, r.Arm, r.Attempt, r)
		}
	}
	// Every session was stored, with its score.
	sessions, total, _ := f.store.listRunSessions(context.Background(), run.ID, RunSessionQuery{Limit: 100})
	if total != 30 {
		t.Fatalf("%d sessions stored", total)
	}
	for _, s := range sessions {
		if s.Eligibility != CodeEligible || s.Delta == nil || s.BaselineDivergence == nil || s.CandidateDivergence == nil || s.ScoreJSON == "" {
			t.Fatalf("session %s = %+v", s.SessionID, s)
		}
	}
	// The verdict can be recomputed from what was stored, and comes out the same.
	if again := Decide(storedGateInput(t, f, run.ID)); !reflect.DeepEqual(again, *run.Decision) {
		t.Errorf("recomputed decision differs:\n%+v\n%+v", again, *run.Decision)
	}
}

func TestRunner_ABadModelFailsOnTheNineOrders(t *testing.T) {
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), nil)
	run := startAndWait(t, f.Plugin, demoRequest("fast", orders))

	if run.Status != StatusComplete || run.Decision == nil || run.Decision.Verdict != VerdictFail || run.Decision.CriticalSessions != 9 {
		t.Fatalf("run = %+v, decision = %+v", run, run.Decision)
	}
	if !strings.HasPrefix(run.Decision.Reasons[len(run.Decision.Reasons)-1], "critical_findings") {
		t.Errorf("reasons = %v", run.Decision.Reasons)
	}
	if run.ToolActivity.StoppedCalls == 0 {
		t.Errorf("tool activity = %+v; the refunds the original never made must show as stopped calls", run.ToolActivity)
	}

	page, err := f.ListRunSessions(context.Background(), run.ID, RunSessionQuery{Filter: FilterCritical, Limit: 50})
	if err != nil || page.Total != 9 || len(page.Sessions) != 9 {
		t.Fatalf("critical sessions = %+v, %v; want the nine fixed orders", page, err)
	}
	for _, s := range page.Sessions {
		if s.CriticalCount == 0 || s.FirstDivergence < 0 {
			t.Errorf("session %+v", s)
		}
	}
	// The side-by-side view of an order the bad model got wrong.
	detail, err := f.GetRunSession(context.Background(), run.ID, demoSessionID(23))
	if err != nil || detail.Score == nil || len(detail.Score.NewCritical) == 0 {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	if len(detail.Recorded) == 0 || len(detail.RecordedCalls) == 0 || len(detail.Replays) != 6 {
		t.Errorf("recorded %d, calls %d, replays %d", len(detail.Recorded), len(detail.RecordedCalls), len(detail.Replays))
	}
	var sawStopped bool
	for _, r := range detail.Replays {
		if r.Comparison == nil || !r.Comparison.Usable || len(r.Actions) == 0 {
			t.Errorf("replay %+v", r)
		}
		if r.Arm == ArmCandidate && r.Outcome == ReplayStoppedMiss && len(r.Comparison.Critical()) > 0 && r.Comparison.Steps[r.Comparison.FirstDivergence].Level == L4 {
			sawStopped = true
		}
	}
	if !sawStopped {
		t.Error("the candidate's stopped refund should show as an L4 row")
	}
}

func TestRunner_IneligibleSessionsAreCountedAndLowerCoverage(t *testing.T) {
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), nil)
	// Ten more sessions: five on another model, five with hidden content.
	var extra []string
	for n := 31; n <= 40; n++ {
		rows := demoRows(n)
		for _, r := range rows {
			if n <= 35 {
				r.Model = "other-model"
			} else {
				r.ContentHidden = true
			}
		}
		f.logs.rows = append(f.logs.rows, rows...)
		extra = append(extra, demoSessionID(n))
	}
	req := demoRequest("good", orders)
	req.Selection.SessionIDs = append(req.Selection.SessionIDs, extra...)
	req.Selection.SessionIDs = append(req.Selection.SessionIDs, "no-such-session")
	run := startAndWait(t, f.Plugin, req)

	want := map[string]int{CodeRouteMismatch: 5, CodeContentHidden: 5, CodeUnknownSession: 1}
	if !reflect.DeepEqual(run.Selection.IneligibleByReason, want) || run.Selection.Selected != 41 || run.Selection.Eligible != 30 {
		t.Errorf("selection = %+v, want %v of 41 selected", run.Selection, want)
	}
	// 30 of 41 is 73%, under the 80% a verdict needs.
	if run.Decision == nil || run.Decision.Verdict != VerdictInconclusive || !strings.HasPrefix(run.Decision.Reasons[len(run.Decision.Reasons)-1], "low_coverage") {
		t.Errorf("decision = %+v", run.Decision)
	}
	if run.Progress.ReplaysTotal != 180 {
		t.Errorf("replays total = %d; ineligible sessions are not replayed", run.Progress.ReplaysTotal)
	}
	// The ineligible ones can be listed with their reasons.
	page, _ := f.ListRunSessions(context.Background(), run.ID, RunSessionQuery{Filter: FilterIneligible, Limit: 50})
	if page.Total != 11 {
		t.Errorf("%d ineligible sessions listed, want 11", page.Total)
	}
	for _, s := range page.Sessions {
		if s.Eligibility == CodeEligible || s.Delta != nil {
			t.Errorf("ineligible session %+v", s)
		}
	}
}

func TestRunner_ReplaysTheBaselineBeforeTheCandidate(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))
	if run.Status != StatusComplete {
		t.Fatalf("run = %+v", run)
	}
	seenCandidate := false
	for _, r := range f.fake.requests() {
		switch r.Model {
		case "good":
			seenCandidate = true
		case "base":
			if seenCandidate {
				t.Fatal("a baseline call came after the first candidate call")
			}
		}
	}
	if !seenCandidate {
		t.Error("the candidate was never replayed")
	}
}

func TestRunner_AnUnusableBaselineSkipsTheCandidate(t *testing.T) {
	orders := allOrders()[:5]
	// The original model is gone: every call to it fails.
	script := func(r fakeRequest) fakeReply {
		if r.Model == "base" {
			return errorReply(401, "invalid api key")
		}
		return demoScript()(r)
	}
	f := newPluginFixture(t, orders, script, nil)
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))

	if run.Status != StatusComplete || run.Decision == nil || run.Decision.Verdict != VerdictInconclusive || run.Decision.ScoredSessions != 0 {
		t.Fatalf("run = %+v, decision = %+v", run, run.Decision)
	}
	for _, r := range f.fake.requests() {
		if r.Model == "good" {
			t.Fatal("the candidate was replayed although the baseline could not be")
		}
	}
	if run.Progress.ReplaysDone != 5*3 {
		t.Errorf("replays done = %d, want only the baseline's 15", run.Progress.ReplaysDone)
	}
	for _, r := range mustReplays(t, f, run.ID) {
		if r.Outcome != string(ReplayInfraError) {
			t.Fatalf("replay outcome = %s", r.Outcome)
		}
	}
}

func TestRunner_TheCallCapStopsARunAndTheGateSaysSo(t *testing.T) {
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), nil)
	req := demoRequest("good", orders)
	maxCalls := 60
	req.MaxCalls = &maxCalls
	run := startAndWait(t, f.Plugin, req)

	if run.Status != StatusComplete || run.Decision == nil || run.Decision.Verdict != VerdictInconclusive {
		t.Fatalf("run = %+v, decision = %+v", run, run.Decision)
	}
	if !strings.HasPrefix(run.Decision.Reasons[len(run.Decision.Reasons)-1], "incomplete_evidence") {
		t.Errorf("reasons = %v", run.Decision.Reasons)
	}
	if got := f.fake.count(); got != 60 {
		t.Errorf("provider saw %d calls, want exactly the cap of 60", got)
	}
	budgetStopped := 0
	for _, r := range mustReplays(t, f, run.ID) {
		if r.Outcome == string(ReplayBudgetStopped) {
			budgetStopped++
		}
	}
	if budgetStopped == 0 {
		t.Error("no replay ended as budget_stopped")
	}
	// The same stored data still gives the same verdict, with the budget flag.
	if again := Decide(storedGateInput(t, f, run.ID)); !reflect.DeepEqual(again, *run.Decision) {
		t.Errorf("recomputed decision differs:\n%+v\n%+v", again, *run.Decision)
	}
}

func TestRunner_OnlyOneRunAtATimeAndCancelling(t *testing.T) {
	orders := allOrders()[:3]
	hang := func(fakeRequest) fakeReply { return fakeReply{Hang: true} }
	f := newPluginFixture(t, orders, hang, nil)
	ctx := context.Background()

	first, err := f.StartRun(ctx, demoRequest("good", orders))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusQueued {
		t.Errorf("a new run is %s, want queued", first.Status)
	}
	_, err = f.StartRun(ctx, demoRequest("good", orders))
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "run_active" || !strings.Contains(apiErr.Message, first.ID) {
		t.Fatalf("a second run: %v; want 409 run_active naming the first", err)
	}

	// Wait for it to be replaying, then cancel.
	deadline := time.Now().Add(20 * time.Second)
	for f.fake.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := f.Status(ctx)
	if status.ActiveRunID == nil || *status.ActiveRunID != first.ID {
		t.Errorf("status = %+v; want the active run", status)
	}
	cancelled, err := f.CancelRun(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled || cancelled.Decision != nil || cancelled.Error == nil || cancelled.Error.Code != "cancelled" || cancelled.FinishedAt == nil {
		t.Errorf("cancelled run = %+v; want cancelled with no verdict", cancelled)
	}
	if _, err := f.CancelRun(ctx, first.ID); !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "run_not_active" {
		t.Errorf("cancelling again: %v", err)
	}
	if _, err := f.CancelRun(ctx, "nope"); !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Errorf("cancelling an unknown run: %v", err)
	}

	// With the first run over, another can start.
	second, err := f.StartRun(ctx, demoRequest("good", orders))
	if err != nil {
		t.Fatalf("a run after the first was cancelled: %v", err)
	}
	if _, err := f.CancelRun(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.Status(ctx); status.ActiveRunID != nil {
		t.Errorf("status = %+v; want no active run", status)
	}
}

func TestRunner_ShuttingDownLeavesTheRunInterrupted(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, func(fakeRequest) fakeReply { return fakeReply{Hang: true} }, nil)
	started, err := f.StartRun(context.Background(), demoRequest("good", orders))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for f.fake.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run never reached the provider")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := f.Cleanup(); err != nil {
		t.Fatal(err)
	}
	row, err := f.store.getRun(context.Background(), started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusInterrupted || row.ErrorCode != "interrupted" || row.FinishedAt == nil || row.Verdict != nil {
		t.Errorf("run after shutdown = %+v", row)
	}
}

func TestRunner_ANonHermeticReplayClientFailsTheRun(t *testing.T) { // S7, at the run level
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	f.testHookClient = func(c *bifrost.Bifrost) { c.MCPManager = stubManager{} }
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))

	if run.Status != StatusFailed || run.Error == nil || run.Error.Code != "not_hermetic" || run.Decision != nil {
		t.Fatalf("run = %+v; want failed with not_hermetic and no verdict", run)
	}
	if f.fake.count() != 0 {
		t.Errorf("%d provider calls were made by a client that could run tools", f.fake.count())
	}
}

func TestRunner_AStoreThatKeepsFailingFailsTheRun(t *testing.T) {
	orders := allOrders()[:3]
	f := newPluginFixture(t, orders, demoScript(), nil)
	var attempts atomic.Int32
	err := f.cs.DB().Callback().Create().Before("gorm:create").Register("heimdall:fail-replays", func(tx *gorm.DB) {
		if tx.Statement.Table == "heimdall_replays" {
			attempts.Add(1)
			_ = tx.AddError(errors.New("disk full"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))
	if run.Status != StatusFailed || run.Error == nil || run.Error.Code != "store" || run.Decision != nil {
		t.Fatalf("run = %+v; want failed with a store error and no verdict", run)
	}
	if attempts.Load() < 2 {
		t.Errorf("%d write attempts; a failed write is retried once before the run fails", attempts.Load())
	}
}

func TestRunner_ManyWorkersShareNothing(t *testing.T) {
	// Many workers at once, run under the race detector in CI: nothing is shared between replays.
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), func(c *Config) { c.MaxConcurrency = 8 })
	run := startAndWait(t, f.Plugin, demoRequest("good", orders))
	if run.Status != StatusComplete || run.Progress.ReplaysDone != 180 || run.Decision == nil || run.Decision.ScoredSessions != 30 {
		t.Fatalf("run = %+v", run)
	}
	seen := map[string]bool{}
	for _, r := range mustReplays(t, f, run.ID) {
		key := fmt.Sprintf("%s/%s/%d", r.SessionID, r.Arm, r.Attempt)
		if seen[key] {
			t.Fatalf("replay %s stored twice", key)
		}
		seen[key] = true
	}
}

func TestRunner_TheRunsToolRulesAreApplied(t *testing.T) {
	orders := allOrders()
	f := newPluginFixture(t, orders, demoScript(), nil)
	// The same bad model with no rules: nothing marks the skipped check as dangerous.
	req := demoRequest("fast", orders)
	req.ToolRules, req.ForbiddenTools = nil, nil
	run := startAndWait(t, f.Plugin, req)
	if run.Decision == nil || run.Decision.CriticalSessions != 0 || run.Decision.Verdict == VerdictFail && strings.HasPrefix(run.Decision.Reasons[len(run.Decision.Reasons)-1], "critical_findings") {
		t.Errorf("decision = %+v; without rules the skipped check is only a divergence", run.Decision)
	}
	row, _ := f.store.getRun(context.Background(), run.ID)
	var rules ToolRules
	if err := json.Unmarshal([]byte(row.RulesJSON), &rules); err != nil || len(rules.Rules) != 0 {
		t.Errorf("stored rules = %s, %v", row.RulesJSON, err)
	}
}

func TestMetricsOf(t *testing.T) {
	results := []*ReplayResult{
		{Arm: ArmBaseline, Usage: Usage{LatencyMS: 100, CostUSD: 0.01, TotalTokens: 100}, Actions: []Action{grp(hit(tc("a", `{}`))), fin("x")}},
		{Arm: ArmBaseline, Usage: Usage{LatencyMS: 300, CostUSD: 0.03, TotalTokens: 300}},
		{Arm: ArmCandidate, Usage: Usage{LatencyMS: 50, CostUSD: 0.001, TotalTokens: 80}, Actions: []Action{grp(missed(tc("b", `{}`), OutcomeMissUnrecorded), hit(tc("c", `{}`)))}},
	}
	m := metricsOf(results)
	if m.Baseline.MeanLatencyMS != 200 || !approx(m.Baseline.MeanCostUSD, 0.02) || m.Baseline.MeanTokens != 200 {
		t.Errorf("baseline = %+v", m.Baseline)
	}
	if m.Candidate.MeanLatencyMS != 50 || m.Candidate.MeanTokens != 80 {
		t.Errorf("candidate = %+v", m.Candidate)
	}
	if m.ToolActivity.CassetteHits != 2 || m.ToolActivity.StoppedCalls != 1 {
		t.Errorf("tool activity = %+v", m.ToolActivity)
	}
	if empty := metricsOf(nil); empty != (storedMetrics{}) {
		t.Errorf("no results = %+v", empty)
	}
}
