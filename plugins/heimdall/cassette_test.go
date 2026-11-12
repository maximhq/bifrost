package heimdall

import (
	"encoding/json"
	"strings"
	"testing"
)

var testDeclared = map[string]bool{"lookup": true, "refund": true, "search": true, "never_called": true}

func newTestCassette(rules ToolRules, recorded ...recordedCall) *Cassette {
	return NewCassette(recorded, testDeclared, rules)
}

func wantLookup(t *testing.T, c *Cassette, name, args, wantResult, wantOutcome string) {
	t.Helper()
	got, _, outcome := c.Lookup(name, args)
	if got != wantResult || outcome != wantOutcome {
		t.Errorf("Lookup(%s, %s) = %q, %s; want %q, %s", name, args, got, outcome, wantResult, wantOutcome)
	}
}

func TestLookup_ExactHit(t *testing.T) {
	c := newTestCassette(ToolRules{},
		recordedCall{Name: "lookup", Args: `{"id": "o1"}`, Result: "order one"},
		recordedCall{Name: "refund", Args: `{"id":"o1"}`, Result: "refunded", IsError: true},
	)
	// Argument layout does not matter.
	wantLookup(t, c, "lookup", `{"id":"o1"}`, "order one", OutcomeHit)
	result, isError, outcome := c.Lookup("refund", "{ \"id\" : \"o1\" }")
	if result != "refunded" || !isError || outcome != OutcomeHit {
		t.Errorf("refund = %q, %v, %s; want the recorded error result", result, isError, outcome)
	}
}

func TestLookup_RepeatCallsInOrder(t *testing.T) {
	c := newTestCassette(ToolRules{},
		recordedCall{Name: "lookup", Args: `{"id":"o1"}`, Result: "first"},
		recordedCall{Name: "lookup", Args: `{"id":"o2"}`, Result: "other"},
		recordedCall{Name: "lookup", Args: `{"id":"o1"}`, Result: "second"},
	)
	wantLookup(t, c, "lookup", `{"id":"o1"}`, "first", OutcomeHit)
	wantLookup(t, c, "lookup", `{"id":"o1"}`, "second", OutcomeHit)
	wantLookup(t, c, "lookup", `{"id":"o2"}`, "other", OutcomeHit)
}

func TestLookup_Exhausted(t *testing.T) {
	c := newTestCassette(ToolRules{}, recordedCall{Name: "refund", Args: `{"id":"o1"}`, Result: "refunded"})
	wantLookup(t, c, "refund", `{"id":"o1"}`, "refunded", OutcomeHit)
	wantLookup(t, c, "refund", `{"id":"o1"}`, "", OutcomeMissExhausted) // a second refund is not recorded
}

func TestLookup_ArgsDiffer(t *testing.T) {
	c := newTestCassette(ToolRules{}, recordedCall{Name: "refund", Args: `{"id":"o1","amount":10}`, Result: "refunded"})
	wantLookup(t, c, "refund", `{"id":"o1","amount":11}`, "", OutcomeMissArgs)
	wantLookup(t, c, "refund", `{"id":"o1","amount":10.0}`, "", OutcomeMissArgs)
	wantLookup(t, c, "refund", `{"id":"o1","amount":10}`, "refunded", OutcomeHit) // a miss consumes nothing
}

func TestLookup_Unrecorded(t *testing.T) {
	c := newTestCassette(ToolRules{}, recordedCall{Name: "lookup", Args: `{}`, Result: "x"})
	wantLookup(t, c, "never_called", `{}`, "", OutcomeMissUnrecorded)
}

func TestLookup_Undeclared(t *testing.T) {
	c := newTestCassette(ToolRules{}, recordedCall{Name: "lookup", Args: `{}`, Result: "x"})
	wantLookup(t, c, "delete_customer", `{}`, "", OutcomeMissUndeclared)
	wantLookup(t, NewCassette(nil, nil, ToolRules{}), "lookup", `{}`, "", OutcomeMissUndeclared)
}

func TestLookup_NameOnly(t *testing.T) {
	rules := ToolRules{Rules: []ToolRule{{Tool: "search", Match: MatchNameOnly}}}
	c := newTestCassette(rules,
		recordedCall{Name: "search", Args: `{"q":"shoes"}`, Result: "r1"},
		recordedCall{Name: "search", Args: `{"q":"boots"}`, Result: "r2"},
		recordedCall{Name: "lookup", Args: `{"id":"o1"}`, Result: "order"},
	)
	wantLookup(t, c, "search", `{"q":"anything at all"}`, "r1", OutcomeHit)
	wantLookup(t, c, "search", `{"q":"boots"}`, "r2", OutcomeHit)
	wantLookup(t, c, "search", `{"q":"x"}`, "", OutcomeMissExhausted)
	// Other tools stay exact.
	wantLookup(t, c, "lookup", `{"id":"o2"}`, "", OutcomeMissArgs)

	unrecorded := newTestCassette(rules, recordedCall{Name: "lookup", Args: `{}`, Result: "x"})
	wantLookup(t, unrecorded, "search", `{}`, "", OutcomeMissUnrecorded)
}

func TestLookup_IgnoreArgs(t *testing.T) {
	rules := ToolRules{Rules: []ToolRule{{Tool: "lookup", IgnoreArgs: []string{"trace_id"}}}}
	c := newTestCassette(rules, recordedCall{Name: "lookup", Args: `{"id":"o1","trace_id":"a"}`, Result: "order"})
	wantLookup(t, c, "lookup", `{"id":"o1","trace_id":"zzz"}`, "order", OutcomeHit)
}

func TestLookup_HostileInput(t *testing.T) {
	c := newTestCassette(ToolRules{}, recordedCall{Name: "lookup", Args: `{"id":"o1"}`, Result: "order"})
	hostile := []struct{ name, args string }{
		{"lookup\x00", `{"id":"o1"}`},
		{"lookup", "{\"id\":\"o1\"}\x00"},
		{"look\x00up", `{}`},
		{"", ""},
		{strings.Repeat("a", 1<<20), strings.Repeat("{", 1<<16)},
		{"lookup", `{"id":"o1"}` + strings.Repeat(" ", 1<<20) + "x"},
	}
	for _, h := range hostile {
		if result, _, outcome := c.Lookup(h.name, h.args); outcome == OutcomeHit || result != "" {
			t.Errorf("Lookup(%.20q, %.20q) answered %q (%s); want a miss", h.name, h.args, result, outcome)
		}
	}
	wantLookup(t, c, "lookup", `{"id":"o1"}`, "order", OutcomeHit) // still intact
}

func TestKey_LengthPrefixed(t *testing.T) {
	c := newTestCassette(ToolRules{})
	// A name and arguments cannot be shifted across the boundary to collide.
	if c.key("ab", "c") == c.key("a", "bc") {
		t.Error("keys collide across the name/argument boundary")
	}
}

func TestClone_Independent(t *testing.T) {
	c := newTestCassette(ToolRules{}, recordedCall{Name: "refund", Args: `{"id":"o1"}`, Result: "refunded"})
	a, b := c.Clone(), c.Clone()
	wantLookup(t, a, "refund", `{"id":"o1"}`, "refunded", OutcomeHit)
	wantLookup(t, a, "refund", `{"id":"o1"}`, "", OutcomeMissExhausted)
	wantLookup(t, b, "refund", `{"id":"o1"}`, "refunded", OutcomeHit) // b does not see a's use
	wantLookup(t, c, "refund", `{"id":"o1"}`, "refunded", OutcomeHit) // nor does the original
}

func TestCassette_SummaryHoldsNoResults(t *testing.T) {
	c := newTestCassette(ToolRules{},
		recordedCall{Name: "refund", Args: `{}`, Result: "SECRET-RESULT"},
		recordedCall{Name: "lookup", Args: `{"a":1}`, Result: "SECRET-RESULT"},
		recordedCall{Name: "lookup", Args: `{"a":2}`, Result: "SECRET-RESULT"},
	)
	want := []CassetteTool{{Name: "lookup", Calls: 2}, {Name: "refund", Calls: 1}}
	got := c.Summary()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("summary = %v, want %v", got, want)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") || !strings.Contains(string(b), `"lookup"`) {
		t.Errorf("marshalled cassette = %s", b)
	}
}
