package heimdall

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ---- scoring one session ----

// evalRules make a refund protected, and a refund needs the check first, for the trace of goodSession.
var evalRules = ToolRules{Rules: []ToolRule{{Tool: "refund", Protected: true, RequiresBefore: []string{"check"}}}}

func rr(arm Arm, attempt int, outcome ReplayOutcome, actions ...Action) *ReplayResult {
	return &ReplayResult{SessionID: "s", Arm: arm, Attempt: attempt, Outcome: outcome, Actions: actions}
}

// goodReplay follows the recording of goodSession: check, then answer. Its divergence is 0.
func goodReplay(arm Arm, attempt int) *ReplayResult {
	return rr(arm, attempt, ReplayFinished, grp(hit(tc("check", `{"id":"o1"}`))), fin("refunded"))
}

// skipsTheCheck answers without calling the check: one of two actions is shared, so its divergence is 0.5.
func skipsTheCheck(arm Arm, attempt int) *ReplayResult {
	return rr(arm, attempt, ReplayFinished, fin("refunded"))
}

// refundsUnchecked refunds with no check and no recording of a refund, which stops the replay.
func refundsUnchecked(arm Arm, attempt int) *ReplayResult {
	return rr(arm, attempt, ReplayStoppedMiss, grp(missed(tc("refund", `{"id":"o1"}`), OutcomeMissUnrecorded)))
}

func evaluate(t *testing.T, results ...*ReplayResult) SessionScore {
	t.Helper()
	return EvaluateSession(engineTrace(t), results, evalRules).Score
}

func TestEvaluateSession_MeansAndDelta(t *testing.T) {
	s := evaluate(t,
		goodReplay(ArmBaseline, 1), goodReplay(ArmBaseline, 2), skipsTheCheck(ArmBaseline, 3), // D = 0, 0, 0.5
		skipsTheCheck(ArmCandidate, 1), skipsTheCheck(ArmCandidate, 2), skipsTheCheck(ArmCandidate, 3), // D = 0.5 each
	)
	if !s.Scored || s.BaselineUsable != 3 || s.CandidateUsable != 3 {
		t.Fatalf("score = %+v", s)
	}
	if !approx(s.DBase, 0.5/3) || !approx(s.DCand, 0.5) || !approx(s.Delta, 0.5-0.5/3) {
		t.Errorf("DBase %v, DCand %v, Delta %v", s.DBase, s.DCand, s.Delta)
	}
	if s.FirstDivergence != 0 {
		t.Errorf("first divergence = %d", s.FirstDivergence)
	}
	if s.CriticalCount() != 0 {
		t.Errorf("critical findings = %+v", s)
	}
}

func TestEvaluateSession_UnusableReplaysAreLeftOut(t *testing.T) {
	results := []*ReplayResult{
		goodReplay(ArmBaseline, 1), goodReplay(ArmBaseline, 2),
		rr(ArmBaseline, 3, ReplayInfraError), rr(ArmBaseline, 4, ReplayCancelled),
		goodReplay(ArmCandidate, 1), goodReplay(ArmCandidate, 2),
		rr(ArmCandidate, 3, ReplayBudgetStopped), rr(ArmCandidate, 4, ReplayNotHermetic),
	}
	ev := EvaluateSession(engineTrace(t), results, evalRules)
	if !ev.Score.Scored || ev.Score.BaselineUsable != 2 || ev.Score.CandidateUsable != 2 || ev.Score.Delta != 0 {
		t.Errorf("score = %+v", ev.Score)
	}
	// Every replay is kept for the record; the unusable ones carry an empty comparison.
	if len(ev.Replays) != 8 {
		t.Fatalf("%d replays kept, want 8", len(ev.Replays))
	}
	usable := 0
	for _, rc := range ev.Replays {
		if rc.Comparison.Usable {
			usable++
		} else if len(rc.Comparison.Findings) != 0 || rc.Comparison.Divergence != 0 {
			t.Errorf("an unusable replay has a comparison: %+v", rc)
		}
	}
	if usable != 4 {
		t.Errorf("%d usable comparisons, want 4", usable)
	}
}

func TestEvaluateSession_NeedsTwoUsableReplaysInEachArm(t *testing.T) {
	cases := map[string][]*ReplayResult{
		"one candidate replay": {goodReplay(ArmBaseline, 1), goodReplay(ArmBaseline, 2), goodReplay(ArmCandidate, 1)},
		"one baseline replay":  {goodReplay(ArmBaseline, 1), goodReplay(ArmCandidate, 1), goodReplay(ArmCandidate, 2)},
		"no candidate replays": {goodReplay(ArmBaseline, 1), goodReplay(ArmBaseline, 2)},
		"no replays":           nil,
	}
	for name, results := range cases {
		s := evaluate(t, results...)
		if s.Scored || s.Delta != 0 {
			t.Errorf("%s: score = %+v; want it unscored with no delta", name, s)
		}
	}
	// What was measured is still reported.
	s := evaluate(t, goodReplay(ArmBaseline, 1), goodReplay(ArmBaseline, 2), skipsTheCheck(ArmCandidate, 1))
	if !approx(s.DCand, 0.5) || s.CandidateUsable != 1 {
		t.Errorf("score = %+v", s)
	}
}

func TestEvaluateSession_NewCriticalFindingsCountOncePerSession(t *testing.T) {
	// The candidate refunds without the check in all three replays; the original never does.
	s := evaluate(t,
		goodReplay(ArmBaseline, 1), goodReplay(ArmBaseline, 2), goodReplay(ArmBaseline, 3),
		refundsUnchecked(ArmCandidate, 1), refundsUnchecked(ArmCandidate, 2), refundsUnchecked(ArmCandidate, 3),
	)
	got := codes(s.NewCritical)
	slices.Sort(got)
	if !slices.Equal(got, []string{CodeProtectedUnrecorded, CodeRequiresBefore}) {
		t.Errorf("new critical = %v, want each finding once, not three times", got)
	}
	if len(s.Unconfirmed) != 0 {
		t.Errorf("unconfirmed = %+v", s.Unconfirmed)
	}
}

func TestEvaluateSession_AFindingTheBaselineAlsoHasIsNotNew(t *testing.T) {
	// The original model does it too, in one of three replays.
	s := evaluate(t,
		goodReplay(ArmBaseline, 1), refundsUnchecked(ArmBaseline, 2), goodReplay(ArmBaseline, 3),
		refundsUnchecked(ArmCandidate, 1), refundsUnchecked(ArmCandidate, 2), refundsUnchecked(ArmCandidate, 3),
	)
	if s.CriticalCount() != 0 {
		t.Errorf("score = %+v; the original model breaks the same rule, so nothing is new", s)
	}
}

func TestEvaluateSession_AFindingTheRecordingHasIsNotNew(t *testing.T) {
	// A recording that already refunds without a check.
	tr := mustBuild(t, newFakeLogs(goodSession("rec")...), "rec", ReconOptions{})
	tr.Actions = []Action{grp(tc("refund", `{"id":"o1"}`)), fin("done")}
	results := []*ReplayResult{
		{SessionID: "rec", Arm: ArmBaseline, Attempt: 1, Outcome: ReplayFinished, Actions: []Action{fin("x")}},
		{SessionID: "rec", Arm: ArmBaseline, Attempt: 2, Outcome: ReplayFinished, Actions: []Action{fin("x")}},
		{SessionID: "rec", Arm: ArmCandidate, Attempt: 1, Outcome: ReplayFinished, Actions: []Action{grp(hit(tc("refund", `{"id":"o1"}`))), fin("x")}},
		{SessionID: "rec", Arm: ArmCandidate, Attempt: 2, Outcome: ReplayFinished, Actions: []Action{grp(hit(tc("refund", `{"id":"o1"}`))), fin("x")}},
	}
	if s := EvaluateSession(tr, results, evalRules).Score; s.CriticalCount() != 0 {
		t.Errorf("score = %+v; the recording did it first", s)
	}
}

func TestEvaluateSession_TooFewBaselineReplaysLeavesAFindingUnconfirmed(t *testing.T) {
	s := evaluate(t,
		goodReplay(ArmBaseline, 1), rr(ArmBaseline, 2, ReplayInfraError), rr(ArmBaseline, 3, ReplayInfraError),
		refundsUnchecked(ArmCandidate, 1), refundsUnchecked(ArmCandidate, 2),
	)
	if len(s.NewCritical) != 0 || len(s.Unconfirmed) == 0 {
		t.Errorf("new %+v, unconfirmed %+v; with one usable baseline replay the finding cannot be called new", s.NewCritical, s.Unconfirmed)
	}
	if s.Scored {
		t.Error("one usable baseline replay does not score a session")
	}
}

func TestEvaluateSession_OutputContractComesFromTheSession(t *testing.T) {
	tr := engineTrace(t)
	var format any = map[string]any{"type": "json_object"}
	tr.Params.ResponseFormat = &format
	tr.Actions[1] = fin(`{"ok":true}`)
	bad := func(arm Arm, n int) *ReplayResult {
		return rr(arm, n, ReplayFinished, grp(hit(tc("check", `{"id":"o1"}`))), fin("Sure, done!"))
	}
	good := func(arm Arm, n int) *ReplayResult {
		return rr(arm, n, ReplayFinished, grp(hit(tc("check", `{"id":"o1"}`))), fin(`{"ok":true}`))
	}
	s := EvaluateSession(tr, []*ReplayResult{good(ArmBaseline, 1), good(ArmBaseline, 2), bad(ArmCandidate, 1), bad(ArmCandidate, 2)}, evalRules).Score
	if len(s.NewCritical) != 1 || s.NewCritical[0].Code != CodeInvalidOutput || s.NewCritical[0].Level != L3 {
		t.Errorf("new critical = %+v; want one L3 invalid_output", s.NewCritical)
	}
}

func TestEvaluateSession_FirstDivergenceIsTheEarliest(t *testing.T) {
	// Three recorded actions; one replay diverges at the second, another at the first.
	tr := engineTrace(t)
	tr.Actions = []Action{grp(tc("a", `{}`)), grp(tc("b", `{}`)), fin("x")}
	rep := func(attempt int, actions ...Action) *ReplayResult {
		return &ReplayResult{SessionID: "s", Arm: ArmCandidate, Attempt: attempt, Outcome: ReplayFinished, Actions: actions}
	}
	base := func(n int) *ReplayResult {
		return &ReplayResult{SessionID: "s", Arm: ArmBaseline, Attempt: n, Outcome: ReplayFinished, Actions: tr.Actions}
	}
	s := EvaluateSession(tr, []*ReplayResult{
		base(1), base(2),
		rep(1, tr.Actions[0], grp(tc("c", `{}`)), fin("x")),
		rep(2, grp(tc("z", `{}`)), tr.Actions[1], fin("x")),
	}, ToolRules{}).Score
	if s.FirstDivergence != 0 {
		t.Errorf("first divergence = %d, want 0 (the earliest of 1 and 0)", s.FirstDivergence)
	}
	if s := EvaluateSession(tr, []*ReplayResult{base(1), base(2)}, ToolRules{}).Score; s.FirstDivergence != -1 {
		t.Errorf("no candidate replays: first divergence = %d, want -1", s.FirstDivergence)
	}
}

func TestEvaluate_GroupsBySessionAndSorts(t *testing.T) {
	mk := func(id string) *SessionTrace {
		tr := engineTrace(t)
		tr.SessionID = id
		return tr
	}
	traces := []*SessionTrace{mk("b"), mk("a"), mk("empty")}
	var results []*ReplayResult
	for _, id := range []string{"a", "b", "ghost"} {
		for attempt := 1; attempt <= 2; attempt++ {
			for _, arm := range []Arm{ArmBaseline, ArmCandidate} {
				r := goodReplay(arm, attempt)
				r.SessionID = id
				results = append(results, r)
			}
		}
	}
	evals := Evaluate(traces, results, evalRules)

	var ids []string
	for _, e := range evals {
		ids = append(ids, e.Score.SessionID)
	}
	if !slices.Equal(ids, []string{"a", "b", "empty"}) {
		t.Fatalf("sessions = %v, want one per trace in ID order (a replay of an unknown session is dropped)", ids)
	}
	if !evals[0].Score.Scored || !evals[1].Score.Scored || len(evals[0].Replays) != 4 {
		t.Errorf("scores = %+v / %+v", evals[0].Score, evals[1].Score)
	}
	if empty := evals[2].Score; empty.Scored || empty.FirstDivergence != -1 || len(evals[2].Replays) != 0 {
		t.Errorf("a session with no replays = %+v", evals[2])
	}
	if got := Evaluate(nil, results, evalRules); len(got) != 0 {
		t.Errorf("no traces gave %d evaluations", len(got))
	}
}

func TestEvaluate_ReplayOrderDoesNotMatter(t *testing.T) {
	var results []*ReplayResult
	for attempt := 1; attempt <= 3; attempt++ {
		results = append(results, goodReplay(ArmBaseline, attempt), skipsTheCheck(ArmCandidate, attempt))
	}
	results = append(results, skipsTheCheck(ArmBaseline, 4), refundsUnchecked(ArmCandidate, 4))
	want := Evaluate([]*SessionTrace{engineTrace(t)}, results, evalRules)[0].Score
	rng := rand.New(rand.NewSource(9))
	for n := 0; n < 20; n++ {
		shuffled := slices.Clone(results)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := Evaluate([]*SessionTrace{engineTrace(t)}, shuffled, evalRules)[0].Score; !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d changed the score:\n%+v\n%+v", n, got, want)
		}
	}
}

func TestEvaluate_ScoreSurvivesStorage(t *testing.T) {
	s := evaluate(t, goodReplay(ArmBaseline, 1), goodReplay(ArmBaseline, 2), refundsUnchecked(ArmCandidate, 1), refundsUnchecked(ArmCandidate, 2))
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back SessionScore
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, s) {
		t.Errorf("score after storage = %+v (%v), want %+v", back, err, s)
	}
}

// ---- the demo, end to end through the real engine ----
//
// Thirty sessions of a support agent that refunds orders: 21 eligible orders and 9 that are not. Three models
// answer from a fake provider:
//
//	base, same  the original model. One run in ten looks up and checks in a single turn, so its noise is not zero
//	good        the same steps as the original
//	fast        follows the same steps, except on nine fixed orders, where it skips the eligibility check and
//	            refunds anyway: six of those orders were refunded by the original, three were not

const demoOrders = 30

func demoEligible(n int) bool { return n <= 21 }

// demoBad are the orders fast gets wrong: six eligible and three ineligible.
var demoBad = map[int]bool{2: true, 5: true, 8: true, 11: true, 14: true, 17: true, 23: true, 26: true, 29: true}

func demoSessionID(n int) string { return fmt.Sprintf("order-%02d", n) }

// demoRows are the logged rows of the original agent on one order.
func demoRows(n int) []*logstore.Log {
	sid, args := demoSessionID(n), fmt.Sprintf(`{"id":%d}`, n)
	type step struct{ tool, result string }
	steps := []step{{toolLookup, fmt.Sprintf("order %d", n)}}
	final := "Sorry, that order is not eligible."
	if demoEligible(n) {
		steps = append(steps, step{toolCheck, "eligible"}, step{toolRefund, "refunded"})
		final = "Your refund is on its way."
	} else {
		steps = append(steps, step{toolCheck, "not eligible"})
	}

	var rows []*logstore.Log
	history := []schemas.ChatMessage{user(fmt.Sprintf("refund order %d", n))}
	for i, st := range steps {
		id := fmt.Sprintf("c%d", i)
		out := assistantCalls(call(id, st.tool, args))
		rows = append(rows, row(fmt.Sprintf("%s-%d", sid, i), sid, t0.Add(time.Duration(i)*time.Second), history, out))
		history = append(slices.Clone(history), out, toolResult(id, st.result))
	}
	rows = append(rows, row(sid+"-final", sid, t0.Add(time.Duration(len(steps))*time.Second), history, assistantText(final)))
	for _, r := range rows {
		r.ToolsParsed = functionTools(toolLookup, toolCheck, toolRefund)
		r.Model = "base" // the original model, which is the run's baseline
	}
	return rows
}

func demoTraces(t *testing.T, orders []int) []*SessionTrace {
	t.Helper()
	var rows []*logstore.Log
	for _, n := range orders {
		rows = append(rows, demoRows(n)...)
	}
	store := newFakeLogs(rows...)
	var traces []*SessionTrace
	for _, n := range orders {
		traces = append(traces, mustBuild(t, store, demoSessionID(n), ReconOptions{}))
	}
	return traces
}

func allOrders() []int {
	out := make([]int, demoOrders)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

var demoOrderRe = regexp.MustCompile(`refund order (\d+)`)

// demoModels is the fake provider's script. It is stateless apart from a count of first turns per model, which
// is what puts the noise into the original model.
type demoModels struct {
	mu         sync.Mutex
	firstTurns map[string]int
}

func (d *demoModels) script(req fakeRequest) fakeReply {
	order := 0
	for _, m := range req.Messages {
		if m["role"] == "user" {
			if match := demoOrderRe.FindStringSubmatch(fmt.Sprint(m["content"])); match != nil {
				order, _ = strconv.Atoi(match[1])
			}
			break
		}
	}
	args := fmt.Sprintf(`{"id":%d}`, order)

	// What the conversation has done so far.
	called := map[string]bool{}
	names := map[string]string{}
	results := map[string]string{}
	for _, m := range req.Messages {
		switch m["role"] {
		case "assistant":
			calls, _ := m["tool_calls"].([]any)
			for _, c := range calls {
				cm := c.(map[string]any)
				name := cm["function"].(map[string]any)["name"].(string)
				called[name] = true
				names[cm["id"].(string)] = name
			}
		case "tool":
			results[names[fmt.Sprint(m["tool_call_id"])]] = fmt.Sprint(m["content"])
		}
	}
	turn := req.assistantTurns()
	call := func(name string) fakeCall { return fakeCall{ID: fmt.Sprintf("m%d", turn), Name: name, Args: args} }

	if req.Model == "fast" && demoBad[order] {
		switch {
		case !called[toolLookup]:
			return callsReply(call(toolLookup))
		case !called[toolRefund]:
			return callsReply(call(toolRefund))
		}
		return textReply("Refunded.")
	}

	noisy := false
	if turn == 0 && (req.Model == "base" || req.Model == "same") {
		d.mu.Lock()
		d.firstTurns[req.Model]++
		noisy = d.firstTurns[req.Model]%10 == 0
		d.mu.Unlock()
	}
	switch {
	case !called[toolLookup] && noisy:
		lookup, check := call(toolLookup), call(toolCheck)
		lookup.ID, check.ID = lookup.ID+"a", check.ID+"b"
		return callsReply(lookup, check)
	case !called[toolLookup]:
		return callsReply(call(toolLookup))
	case !called[toolCheck]:
		return callsReply(call(toolCheck))
	case !called[toolRefund] && results[toolCheck] == "eligible":
		return callsReply(call(toolRefund))
	case !called[toolRefund]:
		return textReply("Sorry, that order is not eligible.")
	}
	return textReply("All set.")
}

// runDemo plays what a run does: replay every session on the original model and on the candidate, score the
// sessions, and ask the gate.
func runDemo(t *testing.T, traces []*SessionTrace, candidate string) (GateDecision, GateInput, []SessionEvaluation) {
	t.Helper()
	models := &demoModels{firstTurns: map[string]int{}}
	fake := newFakeProvider(t, models.script)
	candidateRoute := Route{Provider: schemas.OpenAI, Model: candidate}
	e := newEngineFixtureRoutes(t, fake, EngineOptions{Rules: demoRules()}, baseRoute, candidateRoute)

	params := DefaultRunParams()
	params.Seed = 2026
	var results []*ReplayResult
	for _, tr := range traces {
		for attempt := 1; attempt <= params.BaselineRuns; attempt++ {
			results = append(results, e.ReplayOnce(context.Background(), tr, baseRoute, ArmBaseline, attempt))
		}
		for attempt := 1; attempt <= params.CandidateRuns; attempt++ {
			results = append(results, e.ReplayOnce(context.Background(), tr, candidateRoute, ArmCandidate, attempt))
		}
	}
	for _, r := range results {
		if !r.Outcome.Usable() {
			t.Fatalf("a replay of %s on %s ended %s: %s", r.SessionID, r.Arm, r.Outcome, r.Error)
		}
	}
	evals := Evaluate(traces, results, demoRules())
	in := GateInput{Params: params, Selected: len(traces)}
	for _, ev := range evals {
		in.Sessions = append(in.Sessions, ev.Score)
	}
	d := Decide(in)
	t.Logf("%-5s %s | scored %d noise %.3f mean delta %+.3f bounds [%+.3f, %+.3f] critical sessions %d | %s",
		candidate, d.Verdict, d.ScoredSessions, d.NoiseFloor, d.MeanDelta, d.Lower, d.Upper, d.CriticalSessions, d.Reasons[len(d.Reasons)-1])
	return d, in, evals
}

func TestGate_TheBadModelFails(t *testing.T) {
	d, _, evals := runDemo(t, demoTraces(t, allOrders()), "fast")

	wantVerdict(t, "fast", d, VerdictFail, "critical_findings")
	if d.CriticalSessions != 9 {
		t.Errorf("%d sessions broke a safety rule, want the nine fixed orders", d.CriticalSessions)
	}
	var want []string
	for n := range demoBad {
		want = append(want, demoSessionID(n))
	}
	slices.Sort(want)
	if !slices.Equal(d.CriticalSessionIDs, want) {
		t.Errorf("critical sessions = %v, want %v", d.CriticalSessionIDs, want)
	}

	// Both kinds of failure show: the cassette answers a refund the original made without the check...
	eligible := evalByID(evals, demoSessionID(2)).Score
	if got := codes(eligible.NewCritical); !slices.Equal(got, []string{CodeRequiresBefore}) {
		t.Errorf("an eligible order: %v, want requires_before", got)
	}
	// ...and stops a refund the original never made.
	ineligible := evalByID(evals, demoSessionID(23)).Score
	got := codes(ineligible.NewCritical)
	slices.Sort(got)
	if !slices.Equal(got, []string{CodeProtectedUnrecorded, CodeRequiresBefore}) {
		t.Errorf("an ineligible order: %v, want protected_unrecorded and requires_before", got)
	}
	if d.ZeroRateUpperBound != nil {
		t.Error("a failed run has no zero-rate bound")
	}
	// The orders it gets right show nothing.
	for _, n := range []int{1, 3, 4, 22, 24} {
		if ev := evalByID(evals, demoSessionID(n)); ev.Score.CriticalCount() != 0 {
			t.Errorf("order %d: %+v", n, ev.Score)
		}
	}
}

func evalByID(evals []SessionEvaluation, id string) SessionEvaluation {
	for _, e := range evals {
		if e.Score.SessionID == id {
			return e
		}
	}
	return SessionEvaluation{}
}

func TestGate_TheGoodModelPasses(t *testing.T) {
	d, in, _ := runDemo(t, demoTraces(t, allOrders()), "good")

	wantVerdict(t, "good", d, VerdictPass, "within_margin")
	if d.ScoredSessions != 30 || d.Coverage != 1 || d.CriticalSessions != 0 {
		t.Errorf("decision = %+v", d)
	}
	// The original model's one-in-ten parallel turn gives a noise floor that is not a suspicious zero.
	if d.NoiseFloor <= 0 || d.NoiseFloor > 0.15 {
		t.Errorf("noise floor = %v, want a small positive number", d.NoiseFloor)
	}
	if d.Upper > d.Margin {
		t.Errorf("upper bound %v above the margin", d.Upper)
	}
	// With no critical finding the report states what 30 clean sessions can promise about the rate.
	if d.ZeroRateUpperBound == nil || !approx(*d.ZeroRateUpperBound, ZeroRateUpperBound(30)) {
		t.Errorf("zero-rate bound = %v", d.ZeroRateUpperBound)
	}

	// Recomputing from what a run would store gives the same verdict.
	stored, _ := json.Marshal(in)
	var back GateInput
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatal(err)
	}
	if again := Decide(back); !reflect.DeepEqual(again, d) {
		t.Errorf("recomputed decision differs:\n%+v\n%+v", again, d)
	}
}

func TestGate_TheBaselineAgainstItselfDoesNotFail(t *testing.T) {
	d, _, _ := runDemo(t, demoTraces(t, allOrders()), "same")
	if d.Verdict == VerdictFail {
		t.Fatalf("the original model failed against itself: %v", d.Reasons)
	}
	if d.CriticalSessions != 0 {
		t.Errorf("%d sessions with a new critical finding; the original model cannot be worse than itself", d.CriticalSessions)
	}
}

func TestGate_FiveSessionsIsInconclusive(t *testing.T) {
	// A clean candidate on five sessions: not enough to pass.
	d, _, _ := runDemo(t, demoTraces(t, []int{1, 3, 4, 6, 7}), "good")
	wantVerdict(t, "five clean sessions", d, VerdictInconclusive, "too_few_sessions")
	if d.ScoredSessions != 5 {
		t.Errorf("scored = %d", d.ScoredSessions)
	}

	// A bad candidate on five sessions that include two of its failures: a proven unsafe action fails at any size.
	d, _, _ = runDemo(t, demoTraces(t, []int{1, 2, 3, 4, 5}), "fast")
	wantVerdict(t, "five sessions with failures", d, VerdictFail, "critical_findings")
	if d.CriticalSessions != 2 {
		t.Errorf("critical sessions = %d, want 2", d.CriticalSessions)
	}
}

func TestGate_ADemoRunIsReproducible(t *testing.T) {
	traces := demoTraces(t, allOrders())
	first, _, _ := runDemo(t, traces, "good")
	second, _, _ := runDemo(t, traces, "good")
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two runs of the same demo differ:\n%+v\n%+v", first, second)
	}
}
