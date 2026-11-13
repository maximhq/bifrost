// Package runs holds test runs: a target run on every row of one dataset version and
// scored by a set of evaluators.
package runs

import (
	"encoding/json"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
)

// TargetType is what a run tests.
type TargetType string

const (
	TargetDataset TargetType = "dataset" // the outputs are already in the dataset
	TargetPrompt  TargetType = "prompt"  // a version from the prompt repository
	TargetModel   TargetType = "model"   // a plain model, with an optional system message
	TargetHTTP    TargetType = "http"    // an HTTP endpoint
)

// Variables a run adds to what the dataset row provides.
const (
	VariableInput     = "input"
	VariableOutput    = "output"
	VariableToolCalls = "tool_calls"
	VariableContext   = "context"
	VariableHistory   = "history"
)

// noToolCalls is the tool_calls value of a target that called no tool. Tool call
// accuracy needs it to score "no call expected, none made".
const noToolCalls = "[]"

// Target is what a run calls for each row. A run stores it with everything it needs
// filled in, so the run does not follow later changes to a prompt.
type Target struct {
	Type TargetType `json:"type"`
	// Prompt targets. PromptVersion is the version number; 0 in a request means the latest.
	PromptID      string `json:"prompt_id,omitempty"`
	PromptVersion int    `json:"prompt_version,omitempty"`
	// Messages is the prompt version's messages, copied when the run starts.
	Messages []json.RawMessage `json:"messages,omitempty"`
	// Model and prompt targets. A prompt target takes these from its version.
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model,omitempty"`
	System   string         `json:"system,omitempty"` // model targets only
	Params   map[string]any `json:"params,omitempty"`
	// HTTP targets: the request, in the shape of an API evaluator's config. The value
	// at output_mapping.score is the output. Header values are encrypted at rest.
	HTTP json.RawMessage `json:"http,omitempty"`
}

// EvaluatorRef names a saved evaluator. Version 0 means its current version.
type EvaluatorRef struct {
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
}

// RunInput starts a run.
type RunInput struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	Target Target   `json:"target"`
	// DatasetVersion 0 means the dataset as it is now: unsaved edits are saved as a
	// version first, and the run reads that version.
	DatasetID      string         `json:"dataset_id"`
	DatasetVersion int            `json:"dataset_version"`
	SplitID        string         `json:"split_id"`
	Evaluators     []EvaluatorRef `json:"evaluators"`
	// TargetConcurrency bounds calls in flight to an HTTP target. 0 takes the default.
	TargetConcurrency int    `json:"target_concurrency"`
	CreatedBy         string `json:"-"`
}

// UpdateInput renames a run and replaces its tags.
type UpdateInput struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// Rerun scopes.
const (
	RerunFailed = "failed" // entries that failed or were stopped
	RerunAll    = "all"
)

// RerunInput picks the entries to run again: a scope, or a list of entry IDs.
type RerunInput struct {
	Scope    string   `json:"scope"`
	EntryIDs []string `json:"entry_ids"`
}

// ReviewInput is a reviewer's score for a human evaluator: true or false, a number,
// or one of the evaluator's choices.
type ReviewInput struct {
	Score     any    `json:"score"`
	Reasoning string `json:"reasoning"`
}

// Check is the outcome of validating a run's settings without starting it.
type Check struct {
	Valid    bool     `json:"valid"`
	Problems []string `json:"problems"`
	// Variables is what the run provides to its evaluators.
	Variables []string `json:"variables"`
}

// EvaluatorSummary is one evaluator's results across a run.
type EvaluatorSummary struct {
	EvaluatorID string              `json:"evaluator_id"`
	Name        string              `json:"name"`
	Type        evals.EvaluatorType `json:"type"`
	Version     int                 `json:"version"`
	evals.Aggregation
	// Pending is how many results are still queued, running or waiting for review.
	Pending int `json:"pending"`
	// RunPass is the run-level verdict. Nil while the run is active.
	RunPass *bool `json:"run_pass,omitempty"`
}

// LatencySummary describes the target's latency over the completed entries, in
// milliseconds.
type LatencySummary struct {
	Count int     `json:"count"`
	Min   int64   `json:"min"`
	Max   int64   `json:"max"`
	Mean  float64 `json:"mean"`
	P50   int64   `json:"p50"`
	P90   int64   `json:"p90"`
	P95   int64   `json:"p95"`
	P99   int64   `json:"p99"`
}

// Summary is a run's aggregates. A finished run stores it; an active run gets a
// lighter one built from counts.
type Summary struct {
	Evaluators []EvaluatorSummary        `json:"evaluators"`
	Latency    *LatencySummary           `json:"latency,omitempty"`
	Totals     configstore.EvalRunTotals `json:"totals"`
}

// RunView is a run as the API returns it.
type RunView struct {
	tables.TableEvalRun
	// Counts is how many entries are in each status.
	Counts map[string]int64 `json:"counts"`
	// Passed is how many completed entries passed every evaluator that has a pass rule.
	Passed  int64    `json:"passed"`
	Summary *Summary `json:"summary,omitempty"`
}

// EntryView is an entry with its results.
type EntryView struct {
	tables.TableEvalRunEntry
	Results []tables.TableEvalRunResult `json:"results"`
	// Baseline is the baseline run's entry for the same dataset row, when the entries
	// were read against a baseline and that run has the row.
	Baseline *EntryView `json:"baseline,omitempty"`
}

// EntryQuery selects a page of a run's entries.
type EntryQuery struct {
	Statuses []string
	Passed   *bool
	Search   string
	// Baseline is another run on the same dataset to read the entries against. Each
	// entry then carries that run's entry for the same row.
	Baseline string
	// Change narrows the entries to those that scored lower ("worse") or higher
	// ("better") than the baseline on any evaluator. It needs Baseline.
	Change string
	Offset int
	Limit  int
}

// EntriesPage is a page of entries with the total the filters match.
type EntriesPage struct {
	Entries []EntryView `json:"entries"`
	Total   int64       `json:"total"`
}

// ComparedRow is one dataset row across the compared runs. Entries is in the order of
// the runs; an entry is nil for a run that does not have the row.
type ComparedRow struct {
	DatasetRowID string            `json:"dataset_row_id"`
	Variables    map[string]string `json:"variables"`
	Entries      []*EntryView      `json:"entries"`
}

// ScoreChange counts the dataset rows a run scored higher and lower than the
// baseline, for one evaluator. Compared is how many rows both have a score for.
type ScoreChange struct {
	Improved  int64 `json:"improved"`
	Regressed int64 `json:"regressed"`
	Compared  int64 `json:"compared"`
}

// Comparison puts runs on the same dataset side by side. The first run is the
// baseline the others are measured against.
type Comparison struct {
	Runs  []RunView     `json:"runs"`
	Rows  []ComparedRow `json:"rows"`
	Total int64         `json:"total"`
	// Changes is keyed by run ID, then evaluator ID: how that run's scores moved
	// against the baseline, over every row and not just this page. The baseline has
	// no entry.
	Changes map[string]map[string]ScoreChange `json:"changes"`
}
