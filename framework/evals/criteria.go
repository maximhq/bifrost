package evals

import (
	"slices"
	"strings"
)

// EvaluateEntry applies an entry-level criterion to one score. ok is false when the
// score is not an ok score, in which case there is no verdict to report.
func EvaluateEntry(criterion Criterion, score Score) (pass bool, ok bool) {
	if score.Status != ScoreStatusOK {
		return false, false
	}
	switch criterion.Operator {
	case OperatorGreater, OperatorLess, OperatorGreaterOrEqual, OperatorLessOrEqual:
		// Ordering is defined for numbers only; a boolean or a word never passes.
		if score.Bool != nil {
			return false, true
		}
		got, gotOK := score.numeric()
		want, wantOK := toFloat(criterion.Value)
		if !gotOK || !wantOK {
			return false, true
		}
		return compareNumbers(criterion.Operator, got, want), true
	case OperatorEqual:
		return scoreEquals(criterion.Value, score), true
	case OperatorNotEqual:
		return !scoreEquals(criterion.Value, score), true
	case OperatorIsOneOf:
		if score.String == nil {
			return false, true
		}
		return slices.Contains(stringList(criterion.Value), *score.String), true
	}
	return false, true
}

// compareNumbers applies a numeric operator.
func compareNumbers(operator string, got, want float64) bool {
	switch operator {
	case OperatorGreater:
		return got > want
	case OperatorLess:
		return got < want
	case OperatorGreaterOrEqual:
		return got >= want
	case OperatorLessOrEqual:
		return got <= want
	case OperatorEqual:
		return got == want
	case OperatorNotEqual:
		return got != want
	}
	return false
}

// scoreEquals reports whether a score equals a criterion value. A boolean score matches
// the labels "Yes" and "No", which is how yes/no criteria are written.
func scoreEquals(want any, score Score) bool {
	switch {
	case score.Bool != nil:
		switch w := want.(type) {
		case bool:
			return w == *score.Bool
		case string:
			switch strings.ToLower(strings.TrimSpace(w)) {
			case "yes", "true":
				return *score.Bool
			case "no", "false":
				return !*score.Bool
			}
		}
		return false
	case score.Number != nil:
		w, ok := toFloat(want)
		return ok && w == *score.Number
	case score.String != nil:
		if w, ok := want.(string); ok && w == *score.String {
			return true
		}
		got, gotOK := parseNumber(*score.String)
		w, wantOK := toFloat(want)
		return gotOK && wantOK && got == w
	}
	return false
}

// stringList reads a criterion value as a list of strings, skipping anything else.
func stringList(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ApplyNormalizer rewrites a score. Arithmetic touches numbers only; a mapping replaces
// any score that has an entry and leaves the rest as they are.
func ApplyNormalizer(normalizer *ScoreNormalizer, score Score) Score {
	if normalizer == nil || score.Status != ScoreStatusOK {
		return score
	}
	if normalizer.Action == NormalizerMapping {
		label, ok := score.label()
		if !ok {
			return score
		}
		if mapped, found := normalizer.Mapping[label]; found {
			return NumberScore(mapped)
		}
		return score
	}
	if score.Number == nil || normalizer.Value == nil {
		return score
	}
	operand := *normalizer.Value
	switch normalizer.Action {
	case NormalizerMultiply:
		return NumberScore(*score.Number * operand)
	case NormalizerDivide:
		if operand == 0 {
			return score
		}
		return NumberScore(*score.Number / operand)
	case NormalizerAdd:
		return NumberScore(*score.Number + operand)
	case NormalizerSubtract:
		return NumberScore(*score.Number - operand)
	}
	return score
}

// Aggregation summarises one evaluator's results across the entries of a run.
type Aggregation struct {
	Total     int            `json:"total"`  // every result, scored or not
	Scored    int            `json:"scored"` // results with an ok score
	NA        int            `json:"na"`
	Errors    int            `json:"errors"`
	Passed    int            `json:"passed"`
	PassRate  float64        `json:"pass_rate"` // percent of scored results that passed
	Mean      *float64       `json:"mean,omitempty"`
	Median    *float64       `json:"median,omitempty"`
	Min       *float64       `json:"min,omitempty"`
	Max       *float64       `json:"max,omitempty"`
	Histogram map[string]int `json:"histogram,omitempty"` // count per label, for label scores
}

// Aggregate summarises results. Numbers are averaged, booleans are averaged as 1 and 0,
// and labels are counted. Results that are na or error are counted but not summarised.
func Aggregate(results []Result) Aggregation {
	agg := Aggregation{Total: len(results)}
	values := make([]float64, 0, len(results))
	for _, result := range results {
		switch result.Score.Status {
		case ScoreStatusNA:
			agg.NA++
			continue
		case ScoreStatusOK:
		default:
			agg.Errors++
			continue
		}
		agg.Scored++
		if result.Pass != nil && *result.Pass {
			agg.Passed++
		}
		switch {
		case result.Score.Number != nil:
			values = append(values, *result.Score.Number)
		case result.Score.Bool != nil:
			if *result.Score.Bool {
				values = append(values, 1)
			} else {
				values = append(values, 0)
			}
		case result.Score.String != nil:
			if agg.Histogram == nil {
				agg.Histogram = make(map[string]int)
			}
			agg.Histogram[*result.Score.String]++
		}
	}
	if agg.Scored > 0 {
		agg.PassRate = float64(agg.Passed) / float64(agg.Scored) * 100
	}
	if len(values) == 0 {
		return agg
	}
	slices.Sort(values)
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mid := len(values) / 2
	median := values[mid]
	if len(values)%2 == 0 {
		median = (values[mid-1] + values[mid]) / 2
	}
	agg.Mean = new(sum / float64(len(values)))
	agg.Median = new(median)
	agg.Min = new(values[0])
	agg.Max = new(values[len(values)-1])
	return agg
}

// RunVerdict is the run-level outcome for one evaluator.
type RunVerdict struct {
	Pass       bool `json:"pass"`
	PassCount  int  `json:"pass_count"`
	TotalCount int  `json:"total_count"` // scored results only
}

// EvaluateRun applies a run-level criterion to one evaluator's results. A run with
// nothing scored fails, and so does a statistic that the scores cannot provide.
func EvaluateRun(criterion Criterion, results []Result) RunVerdict {
	return VerdictOf(criterion, Aggregate(results))
}

// VerdictOf applies a run-level criterion to results that are already summarised, so
// a caller that needs both the summary and the verdict reads the results once.
func VerdictOf(criterion Criterion, agg Aggregation) RunVerdict {
	verdict := RunVerdict{PassCount: agg.Passed, TotalCount: agg.Scored}
	if agg.Scored == 0 {
		return verdict
	}
	var got *float64
	switch criterion.Name {
	case RunCriterionQueriesPassed:
		got = &agg.PassRate
	case RunCriterionMeanScore:
		got = agg.Mean
	case RunCriterionMedianScore:
		got = agg.Median
	case RunCriterionMinimumScore:
		got = agg.Min
	case RunCriterionMaximumScore:
		got = agg.Max
	}
	want, ok := toFloat(criterion.Value)
	if got == nil || !ok {
		return verdict
	}
	verdict.Pass = compareNumbers(criterion.Operator, *got, want)
	return verdict
}
