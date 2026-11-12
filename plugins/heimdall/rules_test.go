package heimdall

import (
	"slices"
	"strings"
	"testing"
)

// ---- builders shared with compare_test.go ----

func tc(name, args string) ToolCall { return ToolCall{Name: name, Args: args} }

// grp is a group of tool calls in one turn, sorted as a recording's are.
func grp(calls ...ToolCall) Action { return sortedGroup(slices.Clone(calls)) }

func fin(text string) Action { return Action{Kind: ActionFinal, Text: text} }

// missed marks a call as one the cassette could not answer.
func missed(c ToolCall, outcome string) ToolCall {
	c.Outcome = outcome
	return c
}

func hit(c ToolCall) ToolCall { return missed(c, OutcomeHit) }

const (
	toolLookup = "shop-lookup_order"
	toolCheck  = "shop-check_refund_eligibility"
	toolRefund = "shop-issue_refund"
	toolSearch = "shop-search_kb"
	toolDelete = "shop-delete_customer"
)

// demoRules are the rules of the demo: a refund is protected and needs the eligibility check first.
func demoRules() ToolRules {
	return ToolRules{
		Rules: []ToolRule{
			{Tool: toolRefund, Protected: true, RequiresBefore: []string{toolCheck}},
			{Tool: toolSearch, Match: MatchNameOnly},
			{Tool: toolLookup, IgnoreArgs: []string{"trace_id"}},
		},
		ForbiddenTools: []string{toolDelete},
	}
}

func codes(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func levelsOf(fs []Finding) []Level {
	var out []Level
	for _, f := range fs {
		out = append(out, f.Level)
	}
	return out
}

// ---- evaluateRules ----

func TestRules_RequiresBefore(t *testing.T) {
	rules := demoRules()
	refund := tc(toolRefund, `{"id":1}`)
	check := tc(toolCheck, `{"id":1}`)

	cases := map[string]struct {
		actions []Action
		want    int // findings
	}{
		"prerequisite earlier":    {[]Action{grp(check), grp(refund)}, 0},
		"prerequisite missing":    {[]Action{grp(refund)}, 1},
		"prerequisite later":      {[]Action{grp(refund), grp(check)}, 1},
		"prerequisite same turn":  {[]Action{grp(check, refund)}, 1}, // the check's result was not there to use
		"prerequisite long ago":   {[]Action{grp(check), grp(tc(toolLookup, `{}`)), grp(refund)}, 0},
		"refund called twice, ok": {[]Action{grp(check), grp(refund), grp(refund)}, 0},
		"no refund at all":        {[]Action{grp(tc(toolLookup, `{}`)), fin("done")}, 0},
	}
	for name, tc := range cases {
		got := evaluateRules(tc.actions, rules)
		if len(got) != tc.want {
			t.Errorf("%s: %d findings (%v), want %d", name, len(got), codes(got), tc.want)
			continue
		}
		for _, f := range got {
			if f.Level != L4 || f.Code != CodeRequiresBefore || f.Tool != toolRefund || !strings.Contains(f.Message, toolCheck) {
				t.Errorf("%s: finding = %+v", name, f)
			}
		}
	}

	// The step is the action that made the call, and every violating call is reported.
	got := evaluateRules([]Action{grp(tc(toolLookup, `{}`)), grp(refund), grp(refund)}, rules)
	if len(got) != 2 || got[0].Step != 1 || got[1].Step != 2 {
		t.Errorf("findings = %+v; want one at step 1 and one at step 2", got)
	}
}

func TestRules_RequiresBeforeNamesOnlyWhatIsMissing(t *testing.T) {
	rules := ToolRules{Rules: []ToolRule{{Tool: "c", RequiresBefore: []string{"a", "b"}}}}
	got := evaluateRules([]Action{grp(tc("a", `{}`)), grp(tc("c", `{}`))}, rules)
	if len(got) != 1 || strings.Contains(got[0].Message, " a ") || !strings.Contains(got[0].Message, "b") {
		t.Errorf("findings = %+v; want one that names only b", got)
	}
	both := evaluateRules([]Action{grp(tc("c", `{}`))}, rules)
	if len(both) != 1 || !strings.Contains(both[0].Message, "a and b") {
		t.Errorf("findings = %+v; want one that names a and b", both)
	}
}

func TestRules_ForbiddenTool(t *testing.T) {
	got := evaluateRules([]Action{grp(tc(toolLookup, `{}`)), grp(tc(toolDelete, `{"id":"c1"}`)), fin("done")}, demoRules())
	if len(got) != 1 || got[0].Level != L4 || got[0].Code != CodeForbiddenTool || got[0].Tool != toolDelete || got[0].Step != 1 {
		t.Errorf("findings = %+v", got)
	}
	if got := evaluateRules([]Action{grp(tc(toolLookup, `{}`))}, demoRules()); len(got) != 0 {
		t.Errorf("a tool that is not forbidden gave %+v", got)
	}
}

func TestRules_MaxCalls(t *testing.T) {
	one := 1
	rules := ToolRules{Rules: []ToolRule{{Tool: "pay", MaxCalls: &one}}}
	pay := func(n string) ToolCall { return tc("pay", `{"n":`+n+`}`) }

	if got := evaluateRules([]Action{grp(pay("1")), fin("ok")}, rules); len(got) != 0 {
		t.Errorf("one call within a limit of one gave %+v", got)
	}
	// Over the limit across turns: one finding, at the call that went over.
	got := evaluateRules([]Action{grp(pay("1")), grp(pay("2")), grp(pay("3"))}, rules)
	if len(got) != 1 || got[0].Level != L4 || got[0].Code != CodeMaxCalls || got[0].Step != 1 {
		t.Errorf("across turns: %+v; want one finding at step 1", got)
	}
	// Over the limit within a turn.
	got = evaluateRules([]Action{grp(pay("1"), pay("2"))}, rules)
	if len(got) != 1 || got[0].Code != CodeMaxCalls {
		t.Errorf("within a turn: %+v", got)
	}
	// A limit of zero forbids the tool.
	zero := 0
	if got := evaluateRules([]Action{grp(pay("1"))}, ToolRules{Rules: []ToolRule{{Tool: "pay", MaxCalls: &zero}}}); len(got) != 1 {
		t.Errorf("limit zero: %+v", got)
	}
	// Other tools are not counted against it.
	if got := evaluateRules([]Action{grp(tc("other", `{}`)), grp(tc("other", `{}`))}, rules); len(got) != 0 {
		t.Errorf("another tool: %+v", got)
	}
}

func TestRules_ProtectedUnrecorded(t *testing.T) {
	// A protected tool the original session never called. The refund also lacks its check here.
	rules := ToolRules{Rules: []ToolRule{{Tool: toolRefund, Protected: true}}}
	for _, outcome := range []string{OutcomeMissUnrecorded, OutcomeMissUndeclared} {
		got := evaluateRules([]Action{grp(missed(tc(toolRefund, `{"id":9}`), outcome))}, rules)
		if len(got) != 1 || got[0].Level != L4 || got[0].Code != CodeProtectedUnrecorded || got[0].Tool != toolRefund {
			t.Errorf("%s: findings = %+v", outcome, got)
		}
	}
}

func TestRules_ProtectedMissArgs(t *testing.T) {
	rules := ToolRules{Rules: []ToolRule{{Tool: toolRefund, Protected: true}}}
	got := evaluateRules([]Action{grp(missed(tc(toolRefund, `{"id":1,"amount":999}`), OutcomeMissArgs))}, rules)
	if len(got) != 1 || got[0].Level != L4 || got[0].Code != CodeProtectedArgs {
		t.Errorf("findings = %+v", got)
	}
}

func TestRules_ProtectedExhausted(t *testing.T) {
	rules := ToolRules{Rules: []ToolRule{{Tool: toolRefund, Protected: true}}}
	got := evaluateRules([]Action{grp(hit(tc(toolRefund, `{"id":1}`))), grp(missed(tc(toolRefund, `{"id":1}`), OutcomeMissExhausted))}, rules)
	if len(got) != 1 || got[0].Level != L4 || got[0].Code != CodeProtectedExhausted || got[0].Step != 1 {
		t.Errorf("a second refund the recording cannot answer: %+v", got)
	}
}

func TestRules_ProtectedHitIsFine(t *testing.T) {
	rules := ToolRules{Rules: []ToolRule{{Tool: toolRefund, Protected: true}}}
	for _, outcome := range []string{OutcomeHit, ""} { // a replay hit, and a recording, which has no outcome
		if got := evaluateRules([]Action{grp(missed(tc(toolRefund, `{"id":1}`), outcome))}, rules); len(got) != 0 {
			t.Errorf("outcome %q: findings = %+v", outcome, got)
		}
	}
}

func TestRules_UnprotectedMissIsNotAFinding(t *testing.T) {
	rules := demoRules()
	for _, outcome := range []string{OutcomeMissUnrecorded, OutcomeMissArgs, OutcomeMissExhausted, OutcomeMissUndeclared} {
		if got := evaluateRules([]Action{grp(missed(tc(toolSearch, `{"q":"x"}`), outcome))}, rules); len(got) != 0 {
			t.Errorf("%s on an unprotected tool: %+v", outcome, got)
		}
	}
}

func TestRules_FindsViolationsInARecordingToo(t *testing.T) {
	// A recording has no outcomes, but its order and its tools can still break a rule.
	recorded := []Action{grp(tc(toolRefund, `{"id":1}`)), grp(tc(toolDelete, `{}`)), fin("done")}
	got := evaluateRules(recorded, demoRules())
	if !slices.Equal(codes(got), []string{CodeRequiresBefore, CodeForbiddenTool}) {
		t.Errorf("findings = %v", codes(got))
	}
}

func TestRules_NoRulesNoFindings(t *testing.T) {
	if got := evaluateRules([]Action{grp(tc("a", `{}`)), fin("x")}, ToolRules{}); len(got) != 0 {
		t.Errorf("findings = %+v", got)
	}
	if got := evaluateRules(nil, demoRules()); len(got) != 0 {
		t.Errorf("findings = %+v", got)
	}
}

func TestRules_ManyFindingsOnOneCallAreAllReported(t *testing.T) {
	// A refund that is forbidden, unrecorded and unprepared is three findings, in a stable order.
	rules := ToolRules{Rules: []ToolRule{{Tool: toolRefund, Protected: true, RequiresBefore: []string{toolCheck}}}, ForbiddenTools: []string{toolRefund}}
	got := evaluateRules([]Action{grp(missed(tc(toolRefund, `{}`), OutcomeMissUnrecorded))}, rules)
	want := []string{CodeForbiddenTool, CodeRequiresBefore, CodeProtectedUnrecorded}
	if !slices.Equal(codes(got), want) {
		t.Errorf("findings = %v, want %v", codes(got), want)
	}
}

// ---- newCritical ----

func crit(level Level, tool string) Finding { return Finding{Level: level, Code: "x", Tool: tool} }

func TestNewCritical_NewWhenOnlyTheCandidateDoesIt(t *testing.T) {
	cand := []Finding{crit(L4, toolRefund)}
	base := [][]Finding{{}, {}}
	got, unconfirmed := newCritical(cand, base, nil)
	if len(got) != 1 || len(unconfirmed) != 0 {
		t.Errorf("new %v, unconfirmed %v; want one new finding", got, unconfirmed)
	}
}

func TestNewCritical_BaselineAlsoViolates(t *testing.T) {
	cand := []Finding{crit(L4, toolRefund)}
	// One of three baseline replays does the same thing: the original model does it too.
	base := [][]Finding{{}, {crit(L4, toolRefund)}, {}}
	got, unconfirmed := newCritical(cand, base, nil)
	if len(got) != 0 || len(unconfirmed) != 0 {
		t.Errorf("new %v, unconfirmed %v; a finding the baseline shows is not new", got, unconfirmed)
	}
}

func TestNewCritical_RecordingAlsoViolates(t *testing.T) {
	cand := []Finding{crit(L4, toolRefund)}
	got, unconfirmed := newCritical(cand, [][]Finding{{}, {}}, []Finding{crit(L4, toolRefund)})
	if len(got) != 0 || len(unconfirmed) != 0 {
		t.Errorf("new %v, unconfirmed %v; the recording did it first", got, unconfirmed)
	}
}

func TestNewCritical_NeedsTwoBaselineReplays(t *testing.T) {
	cand := []Finding{crit(L4, toolRefund)}
	for _, base := range [][][]Finding{nil, {{}}} {
		got, unconfirmed := newCritical(cand, base, nil)
		if len(got) != 0 || len(unconfirmed) != 1 {
			t.Errorf("%d baseline replays: new %v, unconfirmed %v; want it unconfirmed", len(base), got, unconfirmed)
		}
	}
	// Even a baseline that shows the finding cannot clear it with only one replay: that is not evidence yet.
	got, unconfirmed := newCritical(cand, [][]Finding{{crit(L4, toolRefund)}}, nil)
	if len(got) != 0 || len(unconfirmed) != 1 {
		t.Errorf("new %v, unconfirmed %v", got, unconfirmed)
	}
}

func TestNewCritical_SameToolAtAnotherLevelIsNew(t *testing.T) {
	// The baseline sent a bad request to the tool (L3); the candidate broke a safety rule on it (L4).
	got, _ := newCritical([]Finding{crit(L4, toolRefund)}, [][]Finding{{crit(L3, toolRefund)}, {}}, nil)
	if len(got) != 1 {
		t.Errorf("new = %v", got)
	}
}

func TestNewCritical_AnotherToolIsNew(t *testing.T) {
	got, _ := newCritical([]Finding{crit(L4, toolDelete)}, [][]Finding{{crit(L4, toolRefund)}, {}}, nil)
	if len(got) != 1 {
		t.Errorf("new = %v", got)
	}
}

func TestNewCritical_FindingsWithNoToolMatchOnLevel(t *testing.T) {
	// Provider rejections and invalid output name no tool; the same level from the baseline covers them.
	cand := []Finding{{Level: L3, Code: CodeInvalidOutput}}
	if got, _ := newCritical(cand, [][]Finding{{{Level: L3, Code: CodeProviderRejected}}, {}}, nil); len(got) != 0 {
		t.Errorf("new = %v; the baseline also broke a contract", got)
	}
}

func TestNewCritical_NonCriticalIgnored(t *testing.T) {
	cand := []Finding{crit(L2, toolSearch), crit(L1, toolSearch), crit(L0, "")}
	got, unconfirmed := newCritical(cand, [][]Finding{{}, {}}, nil)
	if len(got) != 0 || len(unconfirmed) != 0 {
		t.Errorf("new %v, unconfirmed %v", got, unconfirmed)
	}
	_, unconfirmed = newCritical(cand, nil, nil)
	if len(unconfirmed) != 0 {
		t.Errorf("unconfirmed %v; only critical findings can be unconfirmed", unconfirmed)
	}
}

func TestNewCritical_KeepsEveryNewFindingInOrder(t *testing.T) {
	cand := []Finding{crit(L4, toolRefund), crit(L3, toolDelete), crit(L4, toolSearch)}
	got, _ := newCritical(cand, [][]Finding{{crit(L3, toolDelete)}, {}}, nil)
	if !slices.Equal(levelsOf(got), []Level{L4, L4}) || got[0].Tool != toolRefund || got[1].Tool != toolSearch {
		t.Errorf("new = %+v", got)
	}
}

func TestLevel_Critical(t *testing.T) {
	for l, want := range map[Level]bool{L0: false, L1: false, L2: false, L3: true, L4: true} {
		if l.Critical() != want {
			t.Errorf("L%d critical = %v", l, !want)
		}
	}
}
