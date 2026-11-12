package heimdall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

var storeNow = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) *store {
	t.Helper()
	return &store{cs: newTestConfigStore(t)}
}

func testRun(id, status string, age time.Duration) *tables.TableHeimdallRun {
	return &tables.TableHeimdallRun{
		ID: id, Name: "run " + id, Status: status, Stage: StagePlanning,
		BaselineProvider: "openai", BaselineModel: "base", CandidateProvider: "openai", CandidateModel: "cand",
		CreatedAt: storeNow.Add(-age),
	}
}

func mustCreateRun(t *testing.T, s *store, run *tables.TableHeimdallRun) {
	t.Helper()
	if err := s.createRunIfIdle(context.Background(), run); err != nil {
		t.Fatalf("create %s: %v", run.ID, err)
	}
}

func TestStore_OnlyOneRunIsActive(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mustCreateRun(t, s, testRun("r1", StatusQueued, time.Hour))

	if err := s.createRunIfIdle(ctx, testRun("r2", StatusQueued, 0)); !errors.Is(err, errRunActive) {
		t.Fatalf("a second run while the first is queued: %v, want errRunActive", err)
	}
	if err := s.updateRun(ctx, "r1", map[string]any{"status": StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.createRunIfIdle(ctx, testRun("r2", StatusQueued, 0)); !errors.Is(err, errRunActive) {
		t.Fatalf("a second run while the first is running: %v", err)
	}
	for _, done := range []string{StatusComplete, StatusCancelled, StatusFailed, StatusInterrupted} {
		if err := s.updateRun(ctx, "r1", map[string]any{"status": done}); err != nil {
			t.Fatal(err)
		}
		id := "after-" + done
		if err := s.createRunIfIdle(ctx, testRun(id, StatusComplete, 0)); err != nil {
			t.Fatalf("after a %s run: %v", done, err)
		}
	}
	if active, err := s.activeRun(ctx); err != nil || active != nil {
		t.Errorf("active = %+v, %v; want none", active, err)
	}
	if err := s.updateRun(ctx, "r1", map[string]any{"status": StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if active, err := s.activeRun(ctx); err != nil || active == nil || active.ID != "r1" {
		t.Errorf("active = %+v, %v; want r1", active, err)
	}
}

func TestStore_GetAndUpdateRun(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	if _, err := s.getRun(ctx, "nope"); !errors.Is(err, errNotFound) {
		t.Errorf("get a missing run: %v", err)
	}
	if err := s.updateRun(ctx, "nope", map[string]any{"status": StatusRunning}); !errors.Is(err, errNotFound) {
		t.Errorf("update a missing run: %v", err)
	}
	mustCreateRun(t, s, testRun("r1", StatusQueued, 0))

	started := storeNow.Add(time.Minute)
	verdict := string(VerdictPass)
	err := s.updateRun(ctx, "r1", map[string]any{"status": StatusComplete, "stage": StageDone, "started_at": started, "verdict": verdict,
		"decision_json": `{"verdict":"PASS"}`, "spend_usd": 0.42, "replays_done": 180, "sessions_scored": 30})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.getRun(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusComplete || got.Stage != StageDone || got.Verdict == nil || *got.Verdict != "PASS" || got.SpendUSD != 0.42 ||
		got.ReplaysDone != 180 || got.SessionsScored != 30 || got.DecisionJSON != `{"verdict":"PASS"}` || got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("run = %+v", got)
	}
	if got.CandidateModel != "cand" || got.BaselineModel != "base" || got.Name != "run r1" {
		t.Errorf("update changed fields it was not given: %+v", got)
	}
	// A zero value is stored, not skipped.
	if err := s.updateRun(ctx, "r1", map[string]any{"replays_done": 0, "spend_usd": 0.0}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.getRun(ctx, "r1"); got.ReplaysDone != 0 || got.SpendUSD != 0 {
		t.Errorf("zero values were not written: %+v", got)
	}
}

func TestStore_ListRuns(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mustCreateRun(t, s, testRun("old", StatusComplete, 3*time.Hour))
	mustCreateRun(t, s, testRun("mid", StatusComplete, 2*time.Hour))
	mustCreateRun(t, s, testRun("failed", StatusFailed, time.Hour))
	mustCreateRun(t, s, testRun("new", StatusQueued, 0))
	if err := s.updateRun(ctx, "old", map[string]any{"verdict": "PASS"}); err != nil {
		t.Fatal(err)
	}
	if err := s.updateRun(ctx, "mid", map[string]any{"verdict": "FAIL"}); err != nil {
		t.Fatal(err)
	}
	ids := func(runs []tables.TableHeimdallRun) []string {
		var out []string
		for _, r := range runs {
			out = append(out, r.ID)
		}
		return out
	}

	runs, total, err := s.listRuns(ctx, RunQuery{Limit: 10})
	if err != nil || total != 4 || !slices.Equal(ids(runs), []string{"new", "failed", "mid", "old"}) {
		t.Errorf("all: %v of %d, %v; want newest first", ids(runs), total, err)
	}
	runs, total, _ = s.listRuns(ctx, RunQuery{Limit: 2, Offset: 1})
	if total != 4 || !slices.Equal(ids(runs), []string{"failed", "mid"}) {
		t.Errorf("page: %v of %d", ids(runs), total)
	}
	runs, total, _ = s.listRuns(ctx, RunQuery{Status: StatusComplete, Limit: 10})
	if total != 2 || !slices.Equal(ids(runs), []string{"mid", "old"}) {
		t.Errorf("status filter: %v of %d", ids(runs), total)
	}
	runs, total, _ = s.listRuns(ctx, RunQuery{Verdict: "PASS", Limit: 10})
	if total != 1 || ids(runs)[0] != "old" {
		t.Errorf("verdict filter: %v of %d", ids(runs), total)
	}
	if runs, total, _ = s.listRuns(ctx, RunQuery{Status: "nothing", Limit: 10}); total != 0 || len(runs) != 0 {
		t.Errorf("no match: %v of %d", ids(runs), total)
	}
}

func TestStore_MarkStaleRuns(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mustCreateRun(t, s, testRun("done", StatusComplete, 2*time.Hour))
	mustCreateRun(t, s, testRun("cancelled", StatusCancelled, time.Hour))
	mustCreateRun(t, s, testRun("running", StatusRunning, 0))

	n, err := s.markStaleRuns(ctx, storeNow)
	if err != nil || n != 1 {
		t.Fatalf("marked %d, %v; want 1", n, err)
	}
	for id, want := range map[string]string{"done": StatusComplete, "cancelled": StatusCancelled, "running": StatusInterrupted} {
		if got, _ := s.getRun(ctx, id); got.Status != want {
			t.Errorf("%s = %s, want %s", id, got.Status, want)
		}
	}
	got, _ := s.getRun(ctx, "running")
	if got.FinishedAt == nil || got.ErrorCode != "interrupted" || got.Verdict != nil {
		t.Errorf("an interrupted run = %+v; it needs an end time, a reason and no verdict", got)
	}
	// Once none is left, a new run can start, and a second sweep marks nothing.
	if n, _ := s.markStaleRuns(ctx, storeNow); n != 0 {
		t.Errorf("second sweep marked %d", n)
	}
	mustCreateRun(t, s, testRun("fresh", StatusQueued, 0))
}

func sessionRow(runID, sid, eligibility string, critical, first int) tables.TableHeimdallRunSession {
	snap := `{"trace":"` + sid + `"}`
	return tables.TableHeimdallRunSession{RunID: runID, SessionID: sid, Eligibility: eligibility, SnapshotJSON: &snap, EncryptionStatus: "unencrypted", CriticalCount: critical, FirstDivergence: first}
}

func TestStore_RunSessions(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mustCreateRun(t, s, testRun("r0", StatusComplete, time.Hour))
	mustCreateRun(t, s, testRun("r1", StatusRunning, 0))
	if err := s.createRunSessions(ctx, []tables.TableHeimdallRunSession{
		sessionRow("r1", "a", CodeEligible, 0, -1),
		sessionRow("r1", "b", CodeEligible, 2, 1),
		sessionRow("r1", "c", CodeEligible, 0, 3),
		sessionRow("r1", "d", CodeContentHidden, 0, -1),
		sessionRow("r0", "a", CodeEligible, 1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	ids := func(rows []tables.TableHeimdallRunSession) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.SessionID)
		}
		return out
	}

	rows, total, err := s.listRunSessions(ctx, "r1", RunSessionQuery{Filter: FilterAll, Limit: 10})
	if err != nil || total != 4 || !slices.Equal(ids(rows), []string{"b", "a", "c", "d"}) {
		t.Errorf("all: %v of %d, %v; want the critical session first, then by ID", ids(rows), total, err)
	}
	for _, r := range rows {
		if r.SnapshotJSON != nil {
			t.Errorf("session %s: the list carries a snapshot", r.SessionID)
		}
	}
	for filter, want := range map[string][]string{
		FilterCritical:   {"b"},
		FilterDiverged:   {"b", "c"},
		FilterIneligible: {"d"},
		"":               {"b", "a", "c", "d"},
	} {
		rows, total, _ := s.listRunSessions(ctx, "r1", RunSessionQuery{Filter: filter, Limit: 10})
		if int(total) != len(want) || !slices.Equal(ids(rows), want) {
			t.Errorf("filter %q: %v of %d, want %v", filter, ids(rows), total, want)
		}
	}
	if rows, total, _ := s.listRunSessions(ctx, "r1", RunSessionQuery{Limit: 2, Offset: 1}); total != 4 || !slices.Equal(ids(rows), []string{"a", "c"}) {
		t.Errorf("page: %v of %d", ids(rows), total)
	}

	one, err := s.getRunSession(ctx, "r1", "b")
	if err != nil || one.SnapshotJSON == nil || *one.SnapshotJSON != `{"trace":"b"}` || one.CriticalCount != 2 {
		t.Errorf("get = %+v, %v; it should carry the snapshot", one, err)
	}
	if _, err := s.getRunSession(ctx, "r1", "zzz"); !errors.Is(err, errNotFound) {
		t.Errorf("get a missing session: %v", err)
	}
	if _, err := s.getRunSession(ctx, "r2", "a"); !errors.Is(err, errNotFound) {
		t.Errorf("a session of another run: %v", err)
	}

	err = s.updateRunSession(ctx, "r1", "a", map[string]any{"baseline_divergence": 0.1, "candidate_divergence": 0.3, "delta": 0.2, "critical_count": 3, "first_divergence": 0, "score_json": "{}"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.getRunSession(ctx, "r1", "a")
	if got.Delta == nil || *got.Delta != 0.2 || *got.BaselineDivergence != 0.1 || got.CriticalCount != 3 || got.FirstDivergence != 0 {
		t.Errorf("session after update = %+v", got)
	}
	// Another run's session of the same ID is untouched.
	if other, _ := s.getRunSession(ctx, "r0", "a"); other.Delta != nil || other.CriticalCount != 1 {
		t.Errorf("update leaked into another run: %+v", other)
	}
	if err := s.updateRunSession(ctx, "r1", "zzz", map[string]any{"critical_count": 1}); !errors.Is(err, errNotFound) {
		t.Errorf("update a missing session: %v", err)
	}
	if err := s.createRunSessions(ctx, nil); err != nil {
		t.Errorf("creating no sessions: %v", err)
	}
	if err := s.createRunSessions(ctx, []tables.TableHeimdallRunSession{sessionRow("r1", "a", CodeEligible, 0, -1)}); err == nil {
		t.Error("the same session twice in a run must be rejected")
	}
	if err := s.createRunSessions(ctx, []tables.TableHeimdallRunSession{sessionRow("ghost", "a", CodeEligible, 0, -1)}); err == nil {
		t.Error("a session of a run that does not exist must be rejected")
	}
}

func replayRow(id, runID, sid, arm string, attempt int, at time.Time) *tables.TableHeimdallReplay {
	actions := `[{"kind":"final","text":"secret model text"}]`
	return &tables.TableHeimdallReplay{ID: id, RunID: runID, SessionID: sid, Arm: arm, Attempt: attempt, Outcome: string(ReplayFinished),
		ActionsJSON: &actions, ComparisonJSON: `{"usable":true}`, PromptTokens: 10, CompletionTokens: 5, CostUSD: 0.01, LatencyMS: 12, CreatedAt: at}
}

func TestStore_Replays(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mustCreateRun(t, s, testRun("r1", StatusRunning, 0))
	t1 := storeNow
	for _, row := range []*tables.TableHeimdallReplay{
		replayRow("p1", "r1", "a", "baseline", 1, t1),
		replayRow("p2", "r1", "a", "baseline", 2, t1.Add(time.Second)),
		replayRow("p3", "r1", "a", "candidate", 1, t1.Add(2*time.Second)),
		replayRow("p4", "r1", "b", "baseline", 1, t1.Add(3*time.Second)),
	} {
		if err := s.createReplay(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(rows []tables.TableHeimdallReplay) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	all, err := s.listReplays(ctx, "r1", "")
	if err != nil || !slices.Equal(ids(all), []string{"p1", "p2", "p3", "p4"}) {
		t.Errorf("all replays: %v, %v", ids(all), err)
	}
	if one, _ := s.listReplays(ctx, "r1", "a"); !slices.Equal(ids(one), []string{"p1", "p2", "p3"}) {
		t.Errorf("session a: %v", ids(one))
	}
	if none, _ := s.listReplays(ctx, "r1", "zzz"); len(none) != 0 {
		t.Errorf("unknown session: %v", ids(none))
	}
	if err := s.createReplay(ctx, replayRow("p5", "r1", "a", "baseline", 1, t1)); err == nil {
		t.Error("the same attempt twice must be rejected")
	}
	if err := s.createReplay(ctx, replayRow("p6", "ghost", "a", "baseline", 1, t1)); err == nil {
		t.Error("a replay of a run that does not exist must be rejected")
	}

	div := 0.25
	if err := s.updateReplay(ctx, "p1", map[string]any{"comparison_json": `{"usable":true,"divergence":0.25}`, "divergence": div}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.listReplays(ctx, "r1", "a")
	if got[0].Divergence == nil || *got[0].Divergence != 0.25 || got[0].ComparisonJSON != `{"usable":true,"divergence":0.25}` || got[0].PromptTokens != 10 {
		t.Errorf("replay after update = %+v", got[0])
	}
}

func TestStore_ExpireContentKeepsScoresAndVerdicts(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mustCreateRun(t, s, testRun("old", StatusComplete, 10*24*time.Hour))
	mustCreateRun(t, s, testRun("new", StatusComplete, 24*time.Hour))
	for _, id := range []string{"old", "new"} {
		if err := s.createRunSessions(ctx, []tables.TableHeimdallRunSession{sessionRow(id, "a", CodeEligible, 2, 1)}); err != nil {
			t.Fatal(err)
		}
		if err := s.createReplay(ctx, replayRow("p-"+id, id, "a", "candidate", 1, storeNow)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.updateRun(ctx, "old", map[string]any{"verdict": "FAIL", "decision_json": `{"verdict":"FAIL"}`}); err != nil {
		t.Fatal(err)
	}

	snapshots, replays, err := s.expireContent(ctx, storeNow.Add(-7*24*time.Hour))
	if err != nil || snapshots != 1 || replays != 1 {
		t.Fatalf("expired %d snapshots and %d replays, %v; want 1 and 1", snapshots, replays, err)
	}

	oldSession, _ := s.getRunSession(ctx, "old", "a")
	if oldSession.SnapshotJSON != nil {
		t.Error("the old run's snapshot survived")
	}
	if oldSession.CriticalCount != 2 || oldSession.Eligibility != CodeEligible {
		t.Errorf("the old run lost its scores: %+v", oldSession)
	}
	oldReplays, _ := s.listReplays(ctx, "old", "")
	if oldReplays[0].ActionsJSON != nil || oldReplays[0].ComparisonJSON != `{"usable":true}` || oldReplays[0].CostUSD != 0.01 {
		t.Errorf("old replay = %+v; actions must go, comparison and numbers stay", oldReplays[0])
	}
	if run, _ := s.getRun(ctx, "old"); run.Verdict == nil || *run.Verdict != "FAIL" || run.DecisionJSON == "" {
		t.Errorf("the old run lost its verdict: %+v", run)
	}
	newSession, _ := s.getRunSession(ctx, "new", "a")
	newReplays, _ := s.listReplays(ctx, "new", "")
	if newSession.SnapshotJSON == nil || newReplays[0].ActionsJSON == nil {
		t.Error("a recent run lost its content")
	}
	if snapshots, replays, _ := s.expireContent(ctx, storeNow.Add(-7*24*time.Hour)); snapshots != 0 || replays != 0 {
		t.Errorf("a second sweep cleared %d and %d", snapshots, replays)
	}
}

func TestStore_DeletingARunDeletesItsRows(t *testing.T) {
	cs := newTestConfigStore(t)
	s, ctx := &store{cs: cs}, context.Background()
	mustCreateRun(t, s, testRun("r1", StatusComplete, 0))
	if err := s.createRunSessions(ctx, []tables.TableHeimdallRunSession{sessionRow("r1", "a", CodeEligible, 0, -1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.createReplay(ctx, replayRow("p1", "r1", "a", "baseline", 1, storeNow)); err != nil {
		t.Fatal(err)
	}
	if err := cs.DB().Delete(&tables.TableHeimdallRun{}, "id = ?", "r1").Error; err != nil {
		t.Fatal(err)
	}
	if rows, total, _ := s.listRunSessions(ctx, "r1", RunSessionQuery{Limit: 10}); total != 0 || len(rows) != 0 {
		t.Error("sessions outlived their run")
	}
	if replays, _ := s.listReplays(ctx, "r1", ""); len(replays) != 0 {
		t.Error("replays outlived their run")
	}
}

func TestStore_AsksForTheConnectionOnEveryCall(t *testing.T) {
	counting := &countingStore{ConfigStore: newTestConfigStore(t)}
	s, ctx := &store{cs: counting}, context.Background()

	steps := []func(){
		func() { mustCreateRun(t, s, testRun("r1", StatusQueued, 0)) },
		func() { _, _ = s.getRun(ctx, "r1") },
		func() { _, _ = s.activeRun(ctx) },
		func() { _, _, _ = s.listRuns(ctx, RunQuery{Limit: 5}) },
		func() { _ = s.updateRun(ctx, "r1", map[string]any{"stage": StageReplaying}) },
		func() {
			_ = s.createRunSessions(ctx, []tables.TableHeimdallRunSession{sessionRow("r1", "a", CodeEligible, 0, -1)})
		},
		func() { _, _, _ = s.listRunSessions(ctx, "r1", RunSessionQuery{Limit: 5}) },
		func() { _, _ = s.getRunSession(ctx, "r1", "a") },
		func() { _ = s.createReplay(ctx, replayRow("p1", "r1", "a", "baseline", 1, storeNow)) },
		func() { _, _ = s.listReplays(ctx, "r1", "") },
		func() { _, _ = s.markStaleRuns(ctx, storeNow) },
		func() { _, _, _ = s.expireContent(ctx, storeNow) },
	}
	for i, step := range steps {
		before := counting.calls.Load()
		step()
		if counting.calls.Load() == before {
			t.Errorf("step %d did not ask the config store for its connection; it must never keep one", i)
		}
	}
}

func TestStore_ManyRunsAndSessions(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	mustCreateRun(t, s, testRun("big", StatusRunning, 0))
	var rows []tables.TableHeimdallRunSession
	for i := 0; i < 200; i++ {
		rows = append(rows, sessionRow("big", fmt.Sprintf("s%03d", i), CodeEligible, i%3, -1))
	}
	if err := s.createRunSessions(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.listRunSessions(ctx, "big", RunSessionQuery{Limit: 5}); total != 200 {
		t.Errorf("total = %d, want 200", total)
	}
	if _, total, _ := s.listRunSessions(ctx, "big", RunSessionQuery{Filter: FilterCritical, Limit: 500}); total != 133 {
		t.Errorf("critical = %d, want 133", total)
	}
}
