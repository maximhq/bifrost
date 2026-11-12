package evals

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ScoreStatus says whether an evaluator produced a score.
type ScoreStatus string

const (
	ScoreStatusOK    ScoreStatus = "ok"    // a score was produced
	ScoreStatusNA    ScoreStatus = "na"    // not applicable, e.g. a required variable is missing
	ScoreStatusError ScoreStatus = "error" // the evaluator failed
)

// Score is an evaluator's verdict. Exactly one of Number, Bool and String is set when
// Status is ok; none is set otherwise.
type Score struct {
	Status ScoreStatus `json:"status"`
	Number *float64    `json:"number,omitempty"`
	Bool   *bool       `json:"bool,omitempty"`
	String *string     `json:"string,omitempty"`
}

// NumberScore returns an ok score holding a number.
func NumberScore(v float64) Score { return Score{Status: ScoreStatusOK, Number: &v} }

// BoolScore returns an ok score holding a boolean.
func BoolScore(v bool) Score { return Score{Status: ScoreStatusOK, Bool: &v} }

// StringScore returns an ok score holding a label.
func StringScore(v string) Score { return Score{Status: ScoreStatusOK, String: &v} }

// NAScore returns a score for an evaluation that did not apply.
func NAScore() Score { return Score{Status: ScoreStatusNA} }

// ErrorScore returns a score for an evaluation that failed.
func ErrorScore() Score { return Score{Status: ScoreStatusError} }

// Usage is the token usage of the model calls an evaluator made.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Result is the outcome of running one evaluator on one input.
type Result struct {
	EvaluatorID      string   `json:"evaluator_id,omitempty"`
	EvaluatorVersion int      `json:"evaluator_version,omitempty"`
	Score            Score    `json:"score"`
	Reasoning        string   `json:"reasoning,omitempty"`
	Pass             *bool    `json:"pass,omitempty"`  // nil unless Score.Status is ok
	Usage            *Usage   `json:"usage,omitempty"` // model tokens spent by the evaluator
	Cost             *float64 `json:"cost,omitempty"`  // model cost spent by the evaluator
	LatencyMs        int64    `json:"latency_ms"`
	Logs             []string `json:"logs,omitempty"`
}

// label renders an ok score as the text a mapping or a histogram keys on.
func (s Score) label() (string, bool) {
	switch {
	case s.Status != ScoreStatusOK:
		return "", false
	case s.String != nil:
		return *s.String, true
	case s.Number != nil:
		return strconv.FormatFloat(*s.Number, 'f', -1, 64), true
	case s.Bool != nil:
		if *s.Bool {
			return ScoreLabelYes, true
		}
		return ScoreLabelNo, true
	}
	return "", false
}

// numeric returns the score as a number. A label that parses as a number counts, since
// evaluators outside our control (API, user code) often return numbers as text.
func (s Score) numeric() (float64, bool) {
	switch {
	case s.Status != ScoreStatusOK:
		return 0, false
	case s.Number != nil:
		return *s.Number, true
	case s.String != nil:
		return parseNumber(*s.String)
	}
	return 0, false
}

// toFloat converts a criterion value to a number, in any form a Go caller or the JSON
// decoder can produce.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		return parseNumber(n)
	}
	return 0, false
}

func parseNumber(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}
