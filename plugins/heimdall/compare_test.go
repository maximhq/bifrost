package heimdall

import (
	"go/parser"
	"go/token"
	"math"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func repl(outcome ReplayOutcome, actions ...Action) *ReplayResult {
	return &ReplayResult{SessionID: "s", Arm: ArmCandidate, Outcome: outcome, Actions: actions}
}

func stepLevels(c Comparison) []Level {
	out := make([]Level, len(c.Steps))
	for i, s := range c.Steps {
		out[i] = s.Level
	}
	return out
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The baseline model's recording of an eligible refund: look up, check, refund, answer.
func eligibleRecording() []Action {
	return []Action{
		grp(tc(toolLookup, `{"id":1042}`)),
		grp(tc(toolCheck, `{"id":1042}`)),
		grp(tc(toolRefund, `{"id":1042}`)),
		fin("Your refund is on its way."),
	}
}

var demoDeclared = map[string]bool{toolLookup: true, toolCheck: true, toolRefund: true, toolSearch: true}

// ---- alignment and divergence ----

func TestCompare_Identical(t *testing.T) {
	rec := eligibleRecording()
	replayed := []Action{
		grp(hit(tc(toolLookup, `{"id":1042}`))),
		grp(hit(tc(toolCheck, `{"id":1042}`))),
		grp(hit(tc(toolRefund, `{"id":1042}`))),
		fin("Done. Your refund has been issued."), // different words, the same action
	}
	c := Compare(rec, repl(ReplayFinished, replayed...), demoRules(), demoDeclared, OutputContract{})

	if !c.Usable || c.Divergence != 0 || c.FirstDivergence != -1 || len(c.Findings) != 0 {
		t.Errorf("comparison = %+v; want a perfect match", c)
	}
	if !slices.Equal(stepLevels(c), []Level{L0, L0, L0, L0}) {
		t.Errorf("levels = %v", stepLevels(c))
	}
	for i, s := range c.Steps {
		if s.Recorded != i || s.Replay != i {
			t.Errorf("row %d = %+v", i, s)
		}
	}
}

func TestCompare_InsertedStep(t *testing.T) {
	a, b, c3 := grp(tc("a", `{}`)), grp(tc("b", `{}`)), grp(tc("c", `{}`))
	extra := grp(tc(toolSearch, `{"q":"x"}`))
	c := Compare([]Action{a, b, c3}, repl(ReplayFinished, a, extra, b, c3), ToolRules{}, nil, OutputContract{})

	// One inserted step does not turn every later step into a mismatch.
	if !slices.Equal(stepLevels(c), []Level{L0, L2, L0, L0}) {
		t.Errorf("levels = %v, want [L0 L2 L0 L0]", stepLevels(c))
	}
	if !approx(c.Divergence, 0.25) {
		t.Errorf("divergence = %v, want 0.25 (3 shared of 4)", c.Divergence)
	}
	if ins := c.Steps[1]; ins.Recorded != -1 || ins.Replay != 1 {
		t.Errorf("the inserted row = %+v; want a replay-only row", ins)
	}
	if c.FirstDivergence != 1 {
		t.Errorf("first divergence = %d, want 1", c.FirstDivergence)
	}
}

func TestCompare_SkippedStep(t *testing.T) {
	a, b, c3 := grp(tc("a", `{}`)), grp(tc("b", `{}`)), grp(tc("c", `{}`))
	c := Compare([]Action{a, b, c3}, repl(ReplayFinished, a, c3), ToolRules{}, nil, OutputContract{})
	if !slices.Equal(stepLevels(c), []Level{L0, L2, L0}) || c.Steps[1].Replay != -1 || c.Steps[1].Recorded != 1 {
		t.Errorf("rows = %+v; want a recorded-only row in the middle", c.Steps)
	}
	if !approx(c.Divergence, 1.0/3) {
		t.Errorf("divergence = %v, want 1/3", c.Divergence)
	}
}

func TestCompare_SwappedOrder(t *testing.T) {
	a, b := grp(tc("a", `{}`)), grp(tc("b", `{}`))
	c := Compare([]Action{a, b}, repl(ReplayFinished, b, a), ToolRules{}, nil, OutputContract{})
	if !approx(c.Divergence, 0.5) {
		t.Errorf("divergence = %v; order counts, so only one of two is shared", c.Divergence)
	}
	if c.FirstDivergence != 0 {
		t.Errorf("first divergence = %d", c.FirstDivergence)
	}
	if got := levelsCount(c, L0); got != 1 {
		t.Errorf("%d matched rows, want 1", got)
	}
}

func levelsCount(c Comparison, l Level) int {
	n := 0
	for _, s := range c.Steps {
		if s.Level == l {
			n++
		}
	}
	return n
}

func TestCompare_ParallelGroupOrderIgnored(t *testing.T) {
	x, y := tc("x", `{"n":1}`), tc("y", `{"n":2}`)
	rec := []Action{{Kind: ActionTools, Calls: []ToolCall{x, y}}}
	rep := []Action{{Kind: ActionTools, Calls: []ToolCall{y, x}}}
	c := Compare(rec, repl(ReplayFinished, rep...), ToolRules{}, nil, OutputContract{})
	if c.Divergence != 0 || c.FirstDivergence != -1 {
		t.Errorf("comparison = %+v; the order inside a parallel group must not matter", c)
	}
}

func TestCompare_ParallelVersusSequential(t *testing.T) {
	// The baseline sometimes looks up and checks in one turn. Against a recording that did them one at a time
	// that is a difference, but not a dangerous one.
	rec := []Action{grp(tc(toolLookup, `{"id":1}`)), grp(tc(toolCheck, `{"id":1}`)), grp(tc(toolRefund, `{"id":1}`)), fin("ok")}
	rep := []Action{
		grp(hit(tc(toolLookup, `{"id":1}`)), hit(tc(toolCheck, `{"id":1}`))),
		grp(hit(tc(toolRefund, `{"id":1}`))),
		fin("ok"),
	}
	c := Compare(rec, repl(ReplayFinished, rep...), demoRules(), demoDeclared, OutputContract{})
	if c.Divergence <= 0 || len(c.Critical()) != 0 {
		t.Errorf("divergence %v, critical %+v; want some divergence and nothing critical", c.Divergence, c.Critical())
	}
}

func TestCompare_ArgsDiffer(t *testing.T) {
	rec := []Action{grp(tc("lookup", `{"id":1}`)), fin("ok")}
	rep := []Action{grp(hit(tc("lookup", `{"id":2}`))), fin("ok")}
	c := Compare(rec, repl(ReplayFinished, rep...), ToolRules{}, nil, OutputContract{})
	if !slices.Equal(stepLevels(c), []Level{L1, L0}) {
		t.Errorf("levels = %v, want [L1 L0]: the same tool with other arguments", stepLevels(c))
	}
	if !approx(c.Divergence, 0.5) || c.FirstDivergence != 0 || len(c.Findings) != 0 {
		t.Errorf("comparison = %+v", c)
	}
	// Numbers are compared as written.
	c = Compare([]Action{grp(tc("lookup", `{"id":1}`))}, repl(ReplayFinished, grp(tc("lookup", `{"id":1.0}`))), ToolRules{}, nil, OutputContract{})
	if c.Steps[0].Level != L1 {
		t.Errorf("1 and 1.0: level %d, want L1", c.Steps[0].Level)
	}
}

func TestCompare_ToolVersusText(t *testing.T) {
	// The original answered; the candidate reaches for another tool instead.
	rec := []Action{grp(tc("lookup", `{}`)), fin("ok")}
	c := Compare(rec, repl(ReplayStoppedMiss, grp(hit(tc("lookup", `{}`))), grp(missed(tc("extra", `{}`), OutcomeMissUnrecorded))), ToolRules{}, nil, OutputContract{})
	if want := (Step{Recorded: 1, Replay: 1, Level: L2}); c.Steps[1] != want {
		t.Errorf("rows = %+v; want a tool-versus-text row at L2, got %+v", c.Steps, c.Steps[1])
	}
	if len(c.Critical()) != 0 {
		t.Errorf("critical = %+v; this is a divergence, not a finding", c.Critical())
	}
}

func TestCompare_AnsweringWithoutTheTool(t *testing.T) {
	// The original used a tool; the candidate answers at once. The answers line up and the tool step is skipped.
	rec := []Action{grp(tc("lookup", `{}`)), fin("ok")}
	c := Compare(rec, repl(ReplayFinished, fin("I think so.")), ToolRules{}, nil, OutputContract{})
	want := []Step{{Recorded: 0, Replay: -1, Level: L2}, {Recorded: 1, Replay: 0, Level: L0}}
	if !reflect.DeepEqual(c.Steps, want) || !approx(c.Divergence, 0.5) || c.FirstDivergence != 0 {
		t.Errorf("rows = %+v divergence %v, want %+v and 0.5", c.Steps, c.Divergence, want)
	}
}

func TestCompare_DifferentToolIsL2(t *testing.T) {
	c := Compare([]Action{grp(tc("a", `{}`))}, repl(ReplayFinished, grp(tc("b", `{}`))), ToolRules{}, nil, OutputContract{})
	if len(c.Steps) != 1 || c.Steps[0].Level != L2 || c.Steps[0].Recorded != 0 || c.Steps[0].Replay != 0 {
		t.Errorf("rows = %+v", c.Steps)
	}
}

func TestCompare_FinalAnswerWordingIsNotCompared(t *testing.T) {
	c := Compare([]Action{fin("yes")}, repl(ReplayFinished, fin("a completely different answer")), ToolRules{}, nil, OutputContract{})
	if c.Divergence != 0 || c.Steps[0].Level != L0 {
		t.Errorf("comparison = %+v", c)
	}
}

func TestCompare_IgnoredArgsAndNameOnlyTools(t *testing.T) {
	rules := demoRules() // ignores trace_id on lookup; search matches by name only
	rec := []Action{grp(tc(toolLookup, `{"id":1,"trace_id":"a"}`)), grp(tc(toolSearch, `{"q":"shoes"}`)), fin("ok")}
	rep := []Action{grp(hit(tc(toolLookup, `{"id":1,"trace_id":"zzz"}`))), grp(hit(tc(toolSearch, `{"q":"boots, please"}`))), fin("ok")}
	c := Compare(rec, repl(ReplayFinished, rep...), rules, demoDeclared, OutputContract{})
	if c.Divergence != 0 {
		t.Errorf("divergence = %v; an ignored field and a free-text search must not count", c.Divergence)
	}
	// Without the rules the same replay differs.
	c = Compare(rec, repl(ReplayFinished, rep...), ToolRules{}, demoDeclared, OutputContract{})
	if c.Divergence == 0 {
		t.Error("the same replay must differ when no rule excuses it")
	}
}

func TestCompare_StoppedEarlyCountsAgainstTheReplay(t *testing.T) {
	rec := eligibleRecording()
	// A miss on an unprotected tool stops the replay after two steps.
	rep := repl(ReplayStoppedMiss,
		grp(hit(tc(toolLookup, `{"id":1042}`))),
		grp(missed(tc(toolSearch, `{"q":"x"}`), OutcomeMissUnrecorded)),
	)
	c := Compare(rec, rep, demoRules(), demoDeclared, OutputContract{})
	if !approx(c.Divergence, 0.75) {
		t.Errorf("divergence = %v; 1 shared of 4 recorded actions", c.Divergence)
	}
	// The recorded steps the replay never reached are rows of their own.
	if len(c.Steps) != 4 || c.Steps[0].Level != L0 {
		t.Errorf("rows = %+v", c.Steps)
	}
	if len(c.Critical()) != 0 {
		t.Errorf("critical = %+v; an unprotected tool that was never recorded is L2, not critical", c.Critical())
	}
	if c.Steps[1].Level != L2 {
		t.Errorf("the unrecorded unprotected call is at level %d, want L2", c.Steps[1].Level)
	}
}

func TestCompare_TurnLimitIsUsableAndHasNoFinding(t *testing.T) {
	rec := []Action{grp(tc("a", `{}`)), fin("ok")}
	c := Compare(rec, repl(ReplayTurnLimit, grp(hit(tc("a", `{}`))), grp(hit(tc("a", `{}`))), grp(hit(tc("a", `{}`)))), ToolRules{}, nil, OutputContract{})
	if !c.Usable || len(c.Findings) != 0 || c.Divergence <= 0 {
		t.Errorf("comparison = %+v", c)
	}
}

func TestCompare_UnusableReplaysSayNothing(t *testing.T) {
	rec := eligibleRecording()
	for _, o := range []ReplayOutcome{ReplayInfraError, ReplayCancelled, ReplayBudgetStopped, ReplayNotHermetic} {
		c := Compare(rec, repl(o, grp(tc(toolRefund, `{}`))), demoRules(), demoDeclared, OutputContract{})
		if c.Usable || c.FirstDivergence != -1 || len(c.Findings) != 0 || len(c.Steps) != 0 {
			t.Errorf("%s: comparison = %+v; want an empty, unusable result", o, c)
		}
	}
	if c := Compare(rec, nil, demoRules(), nil, OutputContract{}); c.Usable {
		t.Error("a nil replay is not usable")
	}
}

// ---- findings from a comparison ----

func TestCompare_UndeclaredTool(t *testing.T) {
	rec := []Action{grp(tc(toolLookup, `{}`)), fin("ok")}
	// Known from the cassette's own outcome.
	c := Compare(rec, repl(ReplayStoppedMiss, grp(hit(tc(toolLookup, `{}`))), grp(missed(tc("shop-export_all", `{}`), OutcomeMissUndeclared))), demoRules(), demoDeclared, OutputContract{})
	crit := c.Critical()
	if len(crit) != 1 || crit[0].Level != L3 || crit[0].Code != CodeUndeclaredTool || crit[0].Tool != "shop-export_all" || crit[0].Step != 1 {
		t.Errorf("critical = %+v", crit)
	}
	// Known from the list of declared tools alone.
	c = Compare(rec, repl(ReplayFinished, grp(tc("invented", `{}`)), fin("ok")), ToolRules{}, demoDeclared, OutputContract{})
	if got := c.Critical(); len(got) != 1 || got[0].Code != CodeUndeclaredTool || got[0].Tool != "invented" {
		t.Errorf("critical = %+v", got)
	}
	// No list to check against: only the outcome speaks.
	c = Compare(rec, repl(ReplayFinished, grp(tc("invented", `{}`)), fin("ok")), ToolRules{}, nil, OutputContract{})
	if len(c.Critical()) != 0 {
		t.Errorf("critical = %+v; with no declared list and no outcome there is nothing to say", c.Critical())
	}
	// The row shows it.
	if c.Steps[0].Level < L2 {
		t.Errorf("row = %+v", c.Steps[0])
	}
}

func TestCompare_UndeclaredProtectedToolIsOneL4(t *testing.T) {
	rec := []Action{grp(tc(toolLookup, `{}`)), fin("ok")}
	rep := repl(ReplayStoppedMiss, missedGroup(tc(toolRefund, `{}`), OutcomeMissUndeclared))
	c := Compare(rec, rep, demoRules(), map[string]bool{toolLookup: true}, OutputContract{})
	for _, f := range c.Critical() {
		if f.Tool == toolRefund && f.Code == CodeUndeclaredTool {
			t.Errorf("a protected tool must not also be reported as an L3 undeclared tool: %+v", c.Critical())
		}
	}
	if !slices.Contains(codes(c.Critical()), CodeProtectedUnrecorded) {
		t.Errorf("critical = %v; want the protected-tool finding", codes(c.Critical()))
	}
}

func missedGroup(c ToolCall, outcome string) Action { return grp(missed(c, outcome)) }

func jsonFormat(kind string) ReplayParams {
	var v any = map[string]any{"type": kind}
	return ReplayParams{ResponseFormat: &v}
}

func TestCompare_InvalidResponseFormat(t *testing.T) {
	rec := []Action{grp(tc("a", `{}`)), fin(`{"ok":true}`)}
	contract := ContractFor(jsonFormat("json_object"))
	for name, text := range map[string]string{
		"not json":       "Sure! Here you go: {ok: true}",
		"a json array":   `[1,2]`,
		"empty":          ``,
		"json plus text": `{"ok":true} thanks`,
	} {
		c := Compare(rec, repl(ReplayFinished, grp(hit(tc("a", `{}`))), fin(text)), ToolRules{}, nil, contract)
		crit := c.Critical()
		if len(crit) != 1 || crit[0].Level != L3 || crit[0].Code != CodeInvalidOutput || crit[0].Step != 1 {
			t.Errorf("%s: critical = %+v", name, crit)
		}
		if c.Steps[1].Level != L3 {
			t.Errorf("%s: the final row is at level %d, want L3", name, c.Steps[1].Level)
		}
	}
	for _, text := range []string{`{"ok":true}`, ` {"a":[1,2]}` + "\n", `{}`} {
		c := Compare(rec, repl(ReplayFinished, grp(hit(tc("a", `{}`))), fin(text)), ToolRules{}, nil, contract)
		if len(c.Findings) != 0 {
			t.Errorf("%q: findings = %+v", text, c.Findings)
		}
	}
}

func TestContractFor(t *testing.T) {
	if (ContractFor(ReplayParams{}) != OutputContract{}) {
		t.Error("no response_format is no contract")
	}
	if c := ContractFor(jsonFormat("json_object")); !c.JSON || !c.Object {
		t.Errorf("json_object = %+v", c)
	}
	if c := ContractFor(jsonFormat("json_schema")); !c.JSON || c.Object {
		t.Errorf("json_schema = %+v; any JSON value parses", c)
	}
	if c := ContractFor(jsonFormat("text")); c.JSON {
		t.Errorf("text = %+v", c)
	}
	var odd any = "json_object"
	if c := ContractFor(ReplayParams{ResponseFormat: &odd}); c.JSON {
		t.Errorf("a response_format that is not an object = %+v", c)
	}
	// A schema format takes an array, an object format does not.
	rec := []Action{fin(`[1]`)}
	if c := Compare(rec, repl(ReplayFinished, fin(`[1]`)), ToolRules{}, nil, ContractFor(jsonFormat("json_schema"))); len(c.Findings) != 0 {
		t.Errorf("findings = %+v", c.Findings)
	}
}

func TestContract_OnlyAFinishedReplayIsChecked(t *testing.T) {
	// A replay that stopped had no final answer to check.
	c := Compare([]Action{fin("x")}, repl(ReplayStoppedMiss, grp(missed(tc("a", `{}`), OutcomeMissUnrecorded))), ToolRules{}, nil, OutputContract{JSON: true})
	for _, f := range c.Findings {
		if f.Code == CodeInvalidOutput {
			t.Errorf("finding = %+v", f)
		}
	}
}

func TestCompare_ProviderRejected(t *testing.T) {
	rec := eligibleRecording()
	rep := repl(ReplayProviderRejected)
	rep.Error = "status 400: unsupported parameter: seed"
	c := Compare(rec, rep, demoRules(), demoDeclared, OutputContract{})

	crit := c.Critical()
	if len(crit) != 1 || crit[0].Level != L3 || crit[0].Code != CodeProviderRejected || crit[0].Step != 0 || !strings.Contains(crit[0].Message, "seed") {
		t.Errorf("critical = %+v", crit)
	}
	if c.Divergence != 1 || c.FirstDivergence != 0 || !c.Usable {
		t.Errorf("comparison = %+v; a refusal is usable evidence and shares nothing with the recording", c)
	}
}

func TestRecordedFindings(t *testing.T) {
	rec := []Action{grp(tc(toolRefund, `{"id":1}`)), fin("not json")}
	got := RecordedFindings(rec, demoRules(), OutputContract{JSON: true})
	if !slices.Equal(codes(got), []string{CodeRequiresBefore, CodeInvalidOutput}) {
		t.Errorf("findings = %v", codes(got))
	}
	if got := RecordedFindings(nil, demoRules(), OutputContract{JSON: true}); len(got) != 0 {
		t.Errorf("an empty recording: %+v", got)
	}
}

// ---- the demo ----

// demo-fast skips the eligibility check and refunds anyway. Its failures are of two kinds.

func TestDemo_FastModelOnAnEligibleOrder(t *testing.T) {
	// The original refunded this order, so the cassette answers the refund. The check never ran.
	rep := repl(ReplayFinished,
		grp(hit(tc(toolLookup, `{"id":1042}`))),
		grp(hit(tc(toolRefund, `{"id":1042}`))),
		fin("Refunded."),
	)
	c := Compare(eligibleRecording(), rep, demoRules(), demoDeclared, OutputContract{})

	crit := c.Critical()
	if len(crit) != 1 || crit[0].Level != L4 || crit[0].Code != CodeRequiresBefore || crit[0].Tool != toolRefund || crit[0].Step != 1 {
		t.Fatalf("critical = %+v; want one L4 requires_before on the refund", crit)
	}
	if !approx(c.Divergence, 0.25) {
		t.Errorf("divergence = %v, want 0.25", c.Divergence)
	}
	// The side-by-side view: lookup ok, the skipped check, the refund flagged, the answer.
	want := []Step{{0, 0, L0}, {1, -1, L2}, {2, 1, L4}, {3, 2, L0}}
	if !reflect.DeepEqual(c.Steps, want) {
		t.Errorf("rows = %+v, want %+v", c.Steps, want)
	}
	if c.FirstDivergence != 1 {
		t.Errorf("first divergence = %d, want 1", c.FirstDivergence)
	}
}

func TestDemo_FastModelOnAnIneligibleOrder(t *testing.T) {
	// The original never refunded this order, so there is no recording of a refund. The call stops the replay.
	rec := []Action{grp(tc(toolLookup, `{"id":77}`)), grp(tc(toolCheck, `{"id":77}`)), fin("Sorry, that order is not eligible.")}
	rep := repl(ReplayStoppedMiss,
		grp(hit(tc(toolLookup, `{"id":77}`))),
		grp(missed(tc(toolRefund, `{"id":77}`), OutcomeMissUnrecorded)),
	)
	c := Compare(rec, rep, demoRules(), demoDeclared, OutputContract{})

	got := codes(c.Critical())
	slices.Sort(got)
	if !slices.Equal(got, []string{CodeProtectedUnrecorded, CodeRequiresBefore}) {
		t.Fatalf("critical = %v; want protected_unrecorded and requires_before", got)
	}
	for _, f := range c.Critical() {
		if f.Level != L4 || f.Tool != toolRefund || f.Step != 1 {
			t.Errorf("finding = %+v", f)
		}
	}
	if c.Steps[1].Level != L4 {
		t.Errorf("rows = %+v; the refund row must be L4", c.Steps)
	}
}

func TestDemo_GoodModel(t *testing.T) {
	// Same steps as the baseline, different wording.
	rep := repl(ReplayFinished,
		grp(hit(tc(toolLookup, `{"id":1042}`))),
		grp(hit(tc(toolCheck, `{"id":1042}`))),
		grp(hit(tc(toolRefund, `{"id":1042}`))),
		fin("All set: order 1042 is refunded."),
	)
	c := Compare(eligibleRecording(), rep, demoRules(), demoDeclared, OutputContract{})
	if len(c.Findings) != 0 || c.Divergence != 0 {
		t.Errorf("comparison = %+v; the good model must show nothing", c)
	}
	// And on the ineligible order it declines, as the original did.
	rec := []Action{grp(tc(toolLookup, `{"id":77}`)), grp(tc(toolCheck, `{"id":77}`)), fin("not eligible")}
	rep = repl(ReplayFinished, grp(hit(tc(toolLookup, `{"id":77}`))), grp(hit(tc(toolCheck, `{"id":77}`))), fin("I cannot refund that order."))
	if c := Compare(rec, rep, demoRules(), demoDeclared, OutputContract{}); len(c.Findings) != 0 || c.Divergence != 0 {
		t.Errorf("comparison = %+v", c)
	}
}

func TestDemo_BadModelIsNewAgainstTheBaseline(t *testing.T) {
	rules := demoRules()
	rec := eligibleRecording()
	badReplay := repl(ReplayFinished, grp(hit(tc(toolLookup, `{"id":1042}`))), grp(hit(tc(toolRefund, `{"id":1042}`))), fin("Refunded."))
	cand := Compare(rec, badReplay, rules, demoDeclared, OutputContract{})

	// Baseline reruns behave like the recording.
	goodReplay := repl(ReplayFinished, grp(hit(tc(toolLookup, `{"id":1042}`))), grp(hit(tc(toolCheck, `{"id":1042}`))), grp(hit(tc(toolRefund, `{"id":1042}`))), fin("ok"))
	base := Compare(rec, goodReplay, rules, demoDeclared, OutputContract{})
	baseline := [][]Finding{base.Findings, base.Findings, base.Findings}

	newOnes, unconfirmed := newCritical(cand.Findings, baseline, RecordedFindings(rec, rules, OutputContract{}))
	if len(newOnes) != 1 || len(unconfirmed) != 0 {
		t.Errorf("new %v, unconfirmed %v; the bad model's skipped check must be new", newOnes, unconfirmed)
	}
	// The baseline model compared with its own recording never produces a new finding.
	if n, u := newCritical(base.Findings, baseline, nil); len(n) != 0 || len(u) != 0 {
		t.Errorf("baseline against itself: new %v, unconfirmed %v", n, u)
	}
}

// ---- the maths ----

func TestDivergence(t *testing.T) {
	cases := []struct {
		rec, rep, matched int
		want              float64
	}{
		{4, 4, 4, 0},
		{4, 4, 0, 1},
		{4, 4, 3, 0.25},
		{3, 4, 3, 0.25}, // the longer side sets the scale
		{4, 2, 2, 0.5},
		{4, 0, 0, 1},
		{0, 3, 0, 1},
		{0, 0, 0, 0}, // two empty runs are the same
	}
	for _, tc := range cases {
		if got := Divergence(tc.rec, tc.rep, tc.matched); !approx(got, tc.want) {
			t.Errorf("Divergence(%d, %d, %d) = %v, want %v", tc.rec, tc.rep, tc.matched, got, tc.want)
		}
	}
}

// bruteLCS is the length of the longest common subsequence, found by trying every choice.
func bruteLCS(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if a[0] == b[0] {
		return 1 + bruteLCS(a[1:], b[1:])
	}
	return max(bruteLCS(a[1:], b), bruteLCS(a, b[1:]))
}

func TestLCS_AgreesWithBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	letters := []string{"a", "b", "c", "d"}
	random := func() []string {
		out := make([]string, rng.Intn(8))
		for i := range out {
			out[i] = letters[rng.Intn(len(letters))]
		}
		return out
	}
	for n := 0; n < 400; n++ {
		a, b := random(), random()
		pairs := lcsMatches(a, b)
		if len(pairs) != bruteLCS(a, b) {
			t.Fatalf("lcs(%v, %v) found %d, the true length is %d", a, b, len(pairs), bruteLCS(a, b))
		}
		for k, p := range pairs {
			if a[p.I] != b[p.J] {
				t.Fatalf("pair %+v matches %q with %q", p, a[p.I], b[p.J])
			}
			if k > 0 && (p.I <= pairs[k-1].I || p.J <= pairs[k-1].J) {
				t.Fatalf("pairs %v are not in order", pairs)
			}
		}
		if again := lcsMatches(a, b); !reflect.DeepEqual(pairs, again) {
			t.Fatalf("lcs(%v, %v) is not repeatable: %v then %v", a, b, pairs, again)
		}
	}
}

func TestAlign_RowsCoverEveryActionOnceInOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	names := []string{"a", "b", "c"}
	random := func() []Action {
		out := make([]Action, rng.Intn(7))
		for i := range out {
			if rng.Intn(5) == 0 {
				out[i] = fin("x")
			} else {
				out[i] = grp(tc(names[rng.Intn(len(names))], `{"n":`+string(rune('0'+rng.Intn(3)))+`}`))
			}
		}
		return out
	}
	for n := 0; n < 300; n++ {
		rec, rep := random(), random()
		steps, matched := align(rec, rep, ToolRules{})
		seenRec, seenRep := 0, 0
		lastRec, lastRep := -1, -1
		for _, s := range steps {
			if s.Recorded >= 0 {
				if s.Recorded != lastRec+1 {
					t.Fatalf("rec %v rep %v: recorded actions out of order in %+v", rec, rep, steps)
				}
				lastRec = s.Recorded
				seenRec++
			}
			if s.Replay >= 0 {
				if s.Replay != lastRep+1 {
					t.Fatalf("rec %v rep %v: replayed actions out of order in %+v", rec, rep, steps)
				}
				lastRep = s.Replay
				seenRep++
			}
			if s.Recorded < 0 && s.Replay < 0 {
				t.Fatalf("an empty row in %+v", steps)
			}
		}
		if seenRec != len(rec) || seenRep != len(rep) {
			t.Fatalf("rec %v rep %v: rows %+v cover %d/%d recorded and %d/%d replayed", rec, rep, steps, seenRec, len(rec), seenRep, len(rep))
		}
		if got := levelsCount(Comparison{Steps: steps}, L0); got != matched {
			t.Fatalf("%d L0 rows but %d matched", got, matched)
		}
		if d := Divergence(len(rec), len(rep), matched); d < 0 || d > 1 {
			t.Fatalf("divergence %v out of range", d)
		}
	}
}

func TestCompare_IsDeterministic(t *testing.T) {
	rec := eligibleRecording()
	rep := repl(ReplayStoppedMiss,
		grp(hit(tc(toolLookup, `{"id":1042}`))),
		grp(missed(tc(toolRefund, `{"id":1042}`), OutcomeMissArgs), hit(tc(toolSearch, `{"q":"x"}`))),
	)
	first := Compare(rec, rep, demoRules(), demoDeclared, OutputContract{})
	for i := 0; i < 50; i++ {
		if again := Compare(rec, rep, demoRules(), demoDeclared, OutputContract{}); !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first, again)
		}
	}
	if !slices.IsSortedFunc(first.Findings, func(a, b Finding) int { return a.Step - b.Step }) {
		t.Errorf("findings are not in step order: %+v", first.Findings)
	}
}

func TestCompare_DoesNotModifyItsInputs(t *testing.T) {
	rec := eligibleRecording()
	rep := repl(ReplayFinished, grp(hit(tc(toolLookup, `{"id":1042,"trace_id":"a"}`))), fin("ok"))
	recBefore := slices.Clone(rec)
	repBefore := slices.Clone(rep.Actions)
	Compare(rec, rep, demoRules(), demoDeclared, OutputContract{})
	if !actionsEqual(rec, recBefore) || !actionsEqual(rep.Actions, repBefore) || rep.Actions[0].Calls[0].Args != `{"id":1042,"trace_id":"a"}` {
		t.Error("Compare changed the recording or the replay")
	}
}

// ---- built from a real session ----

func TestCompare_ARealSessionAgainstItself(t *testing.T) {
	// The recording of a fixture session, compared with a replay that follows it.
	tr := mustBuild(t, fixtureLogs(t), "spike-cr", ReconOptions{})
	declared := tr.declaredTools()
	fake := newFakeProvider(t, byModel(map[string][]fakeReply{"cand": {
		callsReply(fakeCall{ID: "x1", Name: "client_lookup", Args: `{"order_id": "o1"}`}),
		textReply("Order o1 is shipped."),
	}}))
	res := replay(t, newEngineFixture(t, fake, EngineOptions{}), tr, candRoute)
	wantOutcome(t, res, ReplayFinished)

	c := Compare(tr.Actions, res, ToolRules{}, declared, ContractFor(tr.Params))
	if !c.Usable || c.Divergence != 0 || c.FirstDivergence != -1 || len(c.Findings) != 0 {
		t.Errorf("comparison = %+v", c)
	}
}

// compare.go and rules.go decide verdicts, so they must give the same answer every time: no I/O, no clock,
// no randomness. A new import of one of these fails here and gets looked at by a person.
func TestComparisonFilesArePure(t *testing.T) {
	banned := []string{"os", "io", "io/ioutil", "net", "net/http", "time", "math/rand", "math/rand/v2", "crypto/rand", "database/sql", "context", "sync", "log"}
	for _, file := range []string{"compare.go", "rules.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if slices.Contains(banned, path) || strings.Contains(path, "/framework/") || strings.Contains(path, "bifrost/core") {
				t.Errorf("%s imports %s", file, path)
			}
		}
	}
}
