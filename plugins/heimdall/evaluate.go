package heimdall

import (
	"slices"
	"strings"
)

// minUsablePerArm is how many usable replays each arm needs before a session is scored.
const minUsablePerArm = 2

// SessionScore is what one session contributes to the gate. It holds only numbers and findings, no
// content, so a verdict can be recomputed from the stored scores.
type SessionScore struct {
	SessionID       string  `json:"session_id"`
	BaselineUsable  int     `json:"baseline_usable"`  // usable replays on the original model
	CandidateUsable int     `json:"candidate_usable"` // usable replays on the candidate
	Scored          bool    `json:"scored"`           // both arms have enough usable replays
	DBase           float64 `json:"baseline_divergence"`
	DCand           float64 `json:"candidate_divergence"`
	Delta           float64 `json:"delta"`            // DCand - DBase
	FirstDivergence int     `json:"first_divergence"` // earliest over the candidate's replays, or -1
	// NewCritical are critical findings the original model does not show on this session.
	NewCritical []Finding `json:"new_critical,omitempty"`
	// Unconfirmed are critical findings with too few baseline replays to say either way.
	Unconfirmed []Finding `json:"unconfirmed_critical,omitempty"`
}

// CriticalCount is how many critical findings the session has that count against the candidate or wait for evidence.
func (s SessionScore) CriticalCount() int { return len(s.NewCritical) + len(s.Unconfirmed) }

// ReplayComparison is one replay with its comparison, kept so the run can store both.
type ReplayComparison struct {
	Result     *ReplayResult `json:"result"`
	Comparison Comparison    `json:"comparison"`
}

// SessionEvaluation is a session's score and the comparison of each of its replays.
type SessionEvaluation struct {
	Score   SessionScore       `json:"score"`
	Replays []ReplayComparison `json:"replays"`
}

// Evaluate groups a run's replays by session and scores each session against its recording. The result has
// one entry per trace, ordered by session ID. A replay of a session that is not in traces is ignored.
func Evaluate(traces []*SessionTrace, results []*ReplayResult, rules ToolRules) []SessionEvaluation {
	bySession := make(map[string][]*ReplayResult)
	for _, r := range results {
		if r != nil {
			bySession[r.SessionID] = append(bySession[r.SessionID], r)
		}
	}
	sorted := slices.Clone(traces)
	slices.SortStableFunc(sorted, func(a, b *SessionTrace) int { return strings.Compare(a.SessionID, b.SessionID) })

	out := make([]SessionEvaluation, len(sorted))
	for i, t := range sorted {
		out[i] = EvaluateSession(t, bySession[t.SessionID], rules)
	}
	return out
}

// EvaluateSession scores one session from its replays.
//
// A replay is usable when its outcome shows how the model behaved. D is the share of actions a replay does
// not have in common with the recording. DBase and DCand are the means of D over the usable replays of each
// arm, and Delta is how much worse the candidate is than the original model is against its own recording.
// A session is scored when both arms have at least two usable replays.
//
// Critical findings are sorted into new and unconfirmed (see newCritical) per candidate replay, and the same
// finding in several replays counts once.
func EvaluateSession(t *SessionTrace, results []*ReplayResult, rules ToolRules) SessionEvaluation {
	declared := t.declaredTools()
	contract := ContractFor(t.Params)
	recorded := RecordedFindings(t.Actions, rules, contract)

	// Stable order, so the sums below do not depend on the order replays finished in.
	ordered := slices.Clone(results)
	slices.SortStableFunc(ordered, func(a, b *ReplayResult) int {
		if a.Arm != b.Arm {
			return strings.Compare(string(a.Arm), string(b.Arm))
		}
		return a.Attempt - b.Attempt
	})

	eval := SessionEvaluation{Score: SessionScore{SessionID: t.SessionID, FirstDivergence: -1}}
	var baseD, candD []float64
	var baseFindings [][]Finding
	var candidates []Comparison
	for _, r := range ordered {
		cmp := Compare(t.Actions, r, rules, declared, contract)
		eval.Replays = append(eval.Replays, ReplayComparison{Result: r, Comparison: cmp})
		if !cmp.Usable {
			continue
		}
		switch r.Arm {
		case ArmBaseline:
			baseD = append(baseD, cmp.Divergence)
			baseFindings = append(baseFindings, cmp.Findings)
		case ArmCandidate:
			candD = append(candD, cmp.Divergence)
			candidates = append(candidates, cmp)
		}
	}

	s := &eval.Score
	s.BaselineUsable, s.CandidateUsable = len(baseD), len(candD)
	s.DBase, s.DCand = mean(baseD), mean(candD)
	s.Scored = len(baseD) >= minUsablePerArm && len(candD) >= minUsablePerArm
	if s.Scored {
		s.Delta = s.DCand - s.DBase
	}
	for _, c := range candidates {
		if c.FirstDivergence >= 0 && (s.FirstDivergence < 0 || c.FirstDivergence < s.FirstDivergence) {
			s.FirstDivergence = c.FirstDivergence
		}
		fresh, unconfirmed := newCritical(c.Findings, baseFindings, recorded)
		s.NewCritical = appendOnce(s.NewCritical, fresh)
		s.Unconfirmed = appendOnce(s.Unconfirmed, unconfirmed)
	}
	return eval
}

// appendOnce adds findings that the list does not already hold, where "the same" is the same level, code and tool.
func appendOnce(list, add []Finding) []Finding {
	for _, f := range add {
		if !slices.ContainsFunc(list, func(o Finding) bool { return o.Level == f.Level && o.Code == f.Code && o.Tool == f.Tool }) {
			list = append(list, f)
		}
	}
	return list
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}
