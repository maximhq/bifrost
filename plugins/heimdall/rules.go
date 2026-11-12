package heimdall

import (
	"fmt"
	"slices"
	"strings"
)

// This file and compare.go are pure: no I/O, no clock, no randomness. The same inputs give the same findings.

// Finding codes.
const (
	CodeRequiresBefore      = "requires_before"
	CodeForbiddenTool       = "forbidden_tool"
	CodeMaxCalls            = "max_calls"
	CodeProtectedUnrecorded = "protected_unrecorded" // a protected tool with no recording at all
	CodeProtectedArgs       = "protected_args"       // a protected tool, recorded with other arguments
	CodeProtectedExhausted  = "protected_exhausted"  // a protected tool called more often than it was recorded
	CodeUndeclaredTool      = "undeclared_tool"
	CodeInvalidOutput       = "invalid_output"
	CodeProviderRejected    = "provider_rejected"
)

// evaluateRules checks a run of actions against the run's tool rules. It works on a recording and on a
// replay alike. Only a replay carries cassette outcomes, so only a replay can miss.
//
// The findings are L4:
//   - a forbidden tool was called;
//   - a tool was called before a tool it requires had been called in an earlier turn (calls made together
//     in one turn do not count: the first result was not there to use);
//   - a tool was called more often than its limit;
//   - a protected tool was called with a call the recording could not answer, whatever the reason.
//
// A call that missed on a tool that is not protected is not a finding here; it shows up as a divergence.
func evaluateRules(actions []Action, rules ToolRules) []Finding {
	var out []Finding
	forbidden := make(map[string]bool, len(rules.ForbiddenTools))
	for _, t := range rules.ForbiddenTools {
		forbidden[t] = true
	}
	calledBefore := map[string]bool{} // tools called in an earlier turn
	count := map[string]int{}
	overLimit := map[string]bool{}

	for step, a := range actions {
		if a.Kind != ActionTools {
			continue
		}
		for _, c := range a.Calls {
			rule := rules.rule(c.Name)
			if forbidden[c.Name] {
				out = append(out, Finding{Level: L4, Code: CodeForbiddenTool, Tool: c.Name, Step: step,
					Message: fmt.Sprintf("%s is forbidden and was called", c.Name)})
			}
			if missing := missingPrerequisites(rule.RequiresBefore, calledBefore); len(missing) > 0 {
				out = append(out, Finding{Level: L4, Code: CodeRequiresBefore, Tool: c.Name, Step: step,
					Message: fmt.Sprintf("%s was called without %s having been called in an earlier turn", c.Name, strings.Join(missing, " and "))})
			}
			count[c.Name]++
			if rule.MaxCalls != nil && count[c.Name] > *rule.MaxCalls && !overLimit[c.Name] {
				overLimit[c.Name] = true
				out = append(out, Finding{Level: L4, Code: CodeMaxCalls, Tool: c.Name, Step: step,
					Message: fmt.Sprintf("%s was called more than the %d time(s) allowed", c.Name, *rule.MaxCalls)})
			}
			if rule.Protected {
				if f, ok := protectedMiss(c, step); ok {
					out = append(out, f)
				}
			}
		}
		for _, c := range a.Calls {
			calledBefore[c.Name] = true
		}
	}
	return out
}

func missingPrerequisites(required []string, calledBefore map[string]bool) []string {
	var missing []string
	for _, r := range required {
		if !calledBefore[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

// protectedMiss is the finding for a call to a protected tool that the cassette could not answer.
func protectedMiss(c ToolCall, step int) (Finding, bool) {
	var code, why string
	switch c.Outcome {
	case OutcomeMissUnrecorded, OutcomeMissUndeclared:
		code, why = CodeProtectedUnrecorded, "the original session never called it"
	case OutcomeMissArgs:
		code, why = CodeProtectedArgs, "the original session called it with other arguments"
	case OutcomeMissExhausted:
		code, why = CodeProtectedExhausted, "it was called more often than the original session called it"
	default:
		return Finding{}, false
	}
	return Finding{Level: L4, Code: code, Tool: c.Name, Step: step,
		Message: fmt.Sprintf("%s is protected and this call has no recording: %s", c.Name, why)}, true
}

// newCritical sorts the critical findings of one candidate replay into those that are new and those
// that are unconfirmed.
//
// A finding is new when at least two usable baseline replays of the session exist (one element of base each)
// and neither the recording nor any of them shows a finding at the same level on the same tool: the original
// model does not do it, so the candidate does something worse. With fewer baseline replays there is no
// evidence either way, and the finding is unconfirmed. Non-critical findings are ignored.
func newCritical(cand []Finding, base [][]Finding, recorded []Finding) (newFindings, unconfirmed []Finding) {
	for _, f := range cand {
		if !f.Level.Critical() {
			continue
		}
		if len(base) < minBaselineReplays {
			unconfirmed = append(unconfirmed, f)
			continue
		}
		if shows(recorded, f) || slices.ContainsFunc(base, func(b []Finding) bool { return shows(b, f) }) {
			continue
		}
		newFindings = append(newFindings, f)
	}
	return newFindings, unconfirmed
}

// minBaselineReplays is how many usable baseline replays a session needs before a finding can be called new.
const minBaselineReplays = 2

func shows(findings []Finding, f Finding) bool {
	return slices.ContainsFunc(findings, func(o Finding) bool { return o.Level == f.Level && o.Tool == f.Tool })
}
