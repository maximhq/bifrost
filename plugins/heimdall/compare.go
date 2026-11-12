package heimdall

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// OutputContract is what a final answer must satisfy because the session asked for structured output.
type OutputContract struct {
	JSON   bool // the answer must parse as one JSON value
	Object bool // and that value must be an object
}

// ContractFor reads the contract from the session's response_format. Anything but a JSON format is no
// contract. For a JSON schema the answer must parse; it is not validated against the schema.
func ContractFor(p ReplayParams) OutputContract {
	if p.ResponseFormat == nil {
		return OutputContract{}
	}
	format, ok := (*p.ResponseFormat).(map[string]any)
	if !ok {
		return OutputContract{}
	}
	switch format["type"] {
	case "json_object":
		return OutputContract{JSON: true, Object: true}
	case "json_schema":
		return OutputContract{JSON: true}
	}
	return OutputContract{}
}

// check reports why text breaks the contract, or "".
func (c OutputContract) check(text string) string {
	if !c.JSON {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &v); err != nil {
		return "the answer is not valid JSON"
	}
	if _, isObject := v.(map[string]any); c.Object && !isObject {
		return "the answer is JSON but not an object"
	}
	return ""
}

// RecordedFindings are the findings the recording itself has. A finding the original session also had
// is not the candidate's doing.
func RecordedFindings(recorded []Action, rules ToolRules, contract OutputContract) []Finding {
	out := evaluateRules(recorded, rules)
	if n := len(recorded); n > 0 && recorded[n-1].Kind == ActionFinal {
		if why := contract.check(recorded[n-1].Text); why != "" {
			out = append(out, Finding{Level: L3, Code: CodeInvalidOutput, Step: n - 1, Message: why})
		}
	}
	return out
}

// Compare sets a replay against its recording. recorded holds the recording's actions. declared names the
// tools the session offered; it may be nil when the replay's own outcomes are enough.
//
// The replay must be usable. An unusable one says nothing about the model, so the result is empty and
// marked as not usable, never a perfect match.
func Compare(recorded []Action, replay *ReplayResult, rules ToolRules, declared map[string]bool, contract OutputContract) Comparison {
	cmp := Comparison{FirstDivergence: -1, Steps: []Step{}, Findings: []Finding{}}
	if replay == nil || !replay.Outcome.Usable() {
		return cmp
	}
	cmp.Usable = true

	steps, matched := align(recorded, replay.Actions, rules)
	cmp.Steps = steps
	cmp.Divergence = Divergence(len(recorded), len(replay.Actions), matched)

	cmp.Findings = evaluateRules(replay.Actions, rules)
	cmp.Findings = append(cmp.Findings, undeclaredFindings(replay.Actions, rules, declared)...)
	n := len(replay.Actions)
	if replay.Outcome == ReplayProviderRejected {
		cmp.Findings = append(cmp.Findings, Finding{Level: L3, Code: CodeProviderRejected, Step: n,
			Message: "the provider refused the request: " + replay.Error})
	}
	if replay.Outcome == ReplayFinished && n > 0 && replay.Actions[n-1].Kind == ActionFinal {
		if why := contract.check(replay.Actions[n-1].Text); why != "" {
			cmp.Findings = append(cmp.Findings, Finding{Level: L3, Code: CodeInvalidOutput, Step: n - 1, Message: why})
		}
	}
	slices.SortStableFunc(cmp.Findings, func(a, b Finding) int { return a.Step - b.Step })

	// A row is as bad as the worst finding on its replayed action.
	for _, f := range cmp.Findings {
		for i := range cmp.Steps {
			if cmp.Steps[i].Replay == f.Step && f.Level > cmp.Steps[i].Level {
				cmp.Steps[i].Level = f.Level
			}
		}
	}
	for i, s := range cmp.Steps {
		if s.Level > L0 {
			cmp.FirstDivergence = i
			break
		}
	}
	return cmp
}

// undeclaredFindings flags tools the session never offered. A protected tool is left to evaluateRules,
// which reports it as the worse L4.
func undeclaredFindings(actions []Action, rules ToolRules, declared map[string]bool) []Finding {
	var out []Finding
	for step, a := range actions {
		for _, c := range a.Calls {
			isUndeclared := c.Outcome == OutcomeMissUndeclared || (declared != nil && !declared[c.Name])
			if isUndeclared && !rules.rule(c.Name).Protected {
				out = append(out, Finding{Level: L3, Code: CodeUndeclaredTool, Tool: c.Name, Step: step,
					Message: fmt.Sprintf("%s was not one of the tools offered to the model", c.Name)})
			}
		}
	}
	return out
}

// Divergence is the share of actions the replay and the recording do not have in common:
// 1 - matched / max(recorded, replay). Two empty runs are the same.
func Divergence(recorded, replay, matched int) float64 {
	longest := max(recorded, replay)
	if longest == 0 {
		return 0
	}
	return 1 - float64(matched)/float64(longest)
}

// matchPair says that recorded[I] and replay[J] are the same action.
type matchPair struct{ I, J int }

// lcsMatches finds the longest run of actions two lists share in order, so one inserted or skipped step does
// not make every later step a mismatch. The result is the same every time: of equally long runs, the one
// that matches earlier actions first wins.
func lcsMatches(a, b []string) []matchPair {
	n, m := len(a), len(b)
	// best[i][j] is the length of the longest shared run of a[i:] and b[j:].
	best := make([][]int, n+1)
	for i := range best {
		best[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				best[i][j] = best[i+1][j+1] + 1
			} else {
				best[i][j] = max(best[i+1][j], best[i][j+1])
			}
		}
	}
	var pairs []matchPair
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j] && best[i][j] == best[i+1][j+1]+1:
			pairs = append(pairs, matchPair{i, j})
			i++
			j++
		case best[i+1][j] >= best[i][j+1]:
			i++
		default:
			j++
		}
	}
	return pairs
}

// align pairs the recording with the replay into rows, and counts the actions they share. Between two
// shared actions the unshared ones are paired in order, and what is left over stands alone.
func align(recorded, replay []Action, rules ToolRules) (steps []Step, matched int) {
	recKeys := make([]string, len(recorded))
	for i, a := range recorded {
		recKeys[i] = actionKey(a, rules)
	}
	repKeys := make([]string, len(replay))
	for i, a := range replay {
		repKeys[i] = actionKey(a, rules)
	}
	pairs := lcsMatches(recKeys, repKeys)

	steps = []Step{}
	gap := func(i0, i1, j0, j1 int) {
		for k := 0; k < max(i1-i0, j1-j0); k++ {
			s := Step{Recorded: -1, Replay: -1, Level: L2}
			if i0+k < i1 {
				s.Recorded = i0 + k
			}
			if j0+k < j1 {
				s.Replay = j0 + k
			}
			if s.Recorded >= 0 && s.Replay >= 0 && sameTools(recorded[s.Recorded], replay[s.Replay]) {
				s.Level = L1
			}
			steps = append(steps, s)
		}
	}
	i, j := 0, 0
	for _, p := range pairs {
		gap(i, p.I, j, p.J)
		steps = append(steps, Step{Recorded: p.I, Replay: p.J, Level: L0})
		i, j = p.I+1, p.J+1
	}
	gap(i, len(recorded), j, len(replay))
	return steps, len(pairs)
}

// actionKey is what two actions must share to be the same. A final answer matches any other; its wording
// is not compared. A group of tool calls matches when it has the same calls, in any order. Arguments
// are compared as the run's rules say: with ignored fields removed, and not at all for a name-only tool.
func actionKey(a Action, rules ToolRules) string {
	if a.Kind != ActionTools {
		return "final"
	}
	parts := make([]string, len(a.Calls))
	for i, c := range a.Calls {
		rule := rules.rule(c.Name)
		args := ""
		if rule.Match != MatchNameOnly {
			args = CanonicalArgs(c.Args, rule.IgnoreArgs)
		}
		parts[i] = c.Name + "\x00" + args
	}
	slices.Sort(parts)
	return "tools\x01" + strings.Join(parts, "\x01")
}

// sameTools reports whether two actions call the same tools, whatever the arguments: the difference between
// L1 and L2.
func sameTools(a, b Action) bool {
	if a.Kind != ActionTools || b.Kind != ActionTools {
		return false
	}
	names := func(x Action) []string {
		out := make([]string, len(x.Calls))
		for i, c := range x.Calls {
			out[i] = c.Name
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(names(a), names(b))
}
