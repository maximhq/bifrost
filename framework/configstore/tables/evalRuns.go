package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// Statuses of a test run.
const (
	EvalRunQueued    = "queued"
	EvalRunRunning   = "running"
	EvalRunCompleted = "completed"
	EvalRunFailed    = "failed" // no entry completed
	EvalRunStopped   = "stopped"
)

// Statuses of a test run entry.
const (
	EvalRunEntryQueued     = "queued"
	EvalRunEntryRunning    = "running"    // the target is being called
	EvalRunEntryEvaluating = "evaluating" // the evaluators are scoring the output
	EvalRunEntryCompleted  = "completed"
	EvalRunEntryFailed     = "failed" // the target call failed or returned nothing
	EvalRunEntryStopped    = "stopped"
)

// Statuses of one evaluator's result on one entry.
const (
	EvalRunResultQueued         = "queued"
	EvalRunResultRunning        = "running"
	EvalRunResultCompleted      = "completed"
	EvalRunResultFailed         = "failed"
	EvalRunResultNotApplicable  = "not_applicable"  // a variable the evaluator needs was missing
	EvalRunResultAwaitingReview = "awaiting_review" // a human evaluator nobody has scored yet
)

// TableEvalRun is a test run: one target, run on every row of one dataset version and
// scored by a set of evaluators. The target and the evaluators are copied onto the run
// when it starts, so a later edit to a prompt or an evaluator does not change what the
// run measured, and a rerun measures the same thing.
type TableEvalRun struct {
	ID       string   `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name     string   `gorm:"type:varchar(255);not null;index" json:"name"`
	TagsJSON *string  `gorm:"type:text;column:tags_json" json:"-"`
	Tags     []string `gorm:"-" json:"tags,omitempty"`
	Status   string   `gorm:"type:varchar(16);not null;index" json:"status"`
	// TargetType is "dataset", "prompt", "model" or "http". Target is its settings.
	TargetType string          `gorm:"type:varchar(16);not null" json:"target_type"`
	TargetJSON string          `gorm:"type:text;not null;column:target_json" json:"-"`
	Target     json.RawMessage `gorm:"-" json:"target"`
	// The exact dataset version the run reads. A version a run points at is kept.
	DatasetID      string  `gorm:"type:varchar(36);not null;index:idx_evals_run_dataset" json:"dataset_id"`
	DatasetVersion int     `gorm:"not null;index:idx_evals_run_dataset" json:"dataset_version"`
	SplitID        *string `gorm:"type:varchar(36)" json:"split_id,omitempty"`
	// Evaluators is the list of evaluator definitions the run scores with.
	EvaluatorsJSON string          `gorm:"type:text;not null;column:evaluators_json" json:"-"`
	Evaluators     json.RawMessage `gorm:"-" json:"evaluators"`
	// TargetConcurrency bounds calls in flight to an HTTP target, per instance.
	TargetConcurrency int `gorm:"not null;default:0" json:"target_concurrency"`
	// Total is how many entries the run has.
	Total int64 `gorm:"not null;default:0" json:"total"`
	// Summary holds the aggregates, written when the run finishes.
	SummaryJSON *string         `gorm:"type:text;column:summary_json" json:"-"`
	Summary     json.RawMessage `gorm:"-" json:"summary,omitempty"`
	Error       string          `gorm:"type:text" json:"error,omitempty"`
	CreatedBy   string          `gorm:"type:varchar(255)" json:"created_by,omitempty"`
	DeletedAt   int64           `gorm:"column:deleted_at_unix_nano;not null;default:0;index" json:"-"` // 0 while live
	CreatedAt   time.Time       `gorm:"not null;index" json:"created_at"`
	UpdatedAt   time.Time       `gorm:"not null" json:"updated_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	EndedAt     *time.Time      `json:"ended_at,omitempty"`
}

// TableName for TableEvalRun
func (TableEvalRun) TableName() string { return "evals_runs" }

// BeforeSave GORM hook to serialize JSON fields
func (r *TableEvalRun) BeforeSave(tx *gorm.DB) error {
	r.TargetJSON = rawJSONOrEmptyObject(r.Target)
	r.EvaluatorsJSON = "[]"
	if len(r.Evaluators) > 0 {
		r.EvaluatorsJSON = string(r.Evaluators)
	}
	if len(r.Summary) > 0 {
		r.SummaryJSON = new(string(r.Summary))
	}
	r.TagsJSON = nil
	if r.Tags != nil {
		data, err := json.Marshal(r.Tags)
		if err != nil {
			return err
		}
		r.TagsJSON = new(string(data))
	}
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (r *TableEvalRun) AfterFind(tx *gorm.DB) error {
	if r.TargetJSON != "" {
		r.Target = json.RawMessage(r.TargetJSON)
	}
	if r.EvaluatorsJSON != "" {
		r.Evaluators = json.RawMessage(r.EvaluatorsJSON)
	}
	if r.SummaryJSON != nil && *r.SummaryJSON != "" {
		r.Summary = json.RawMessage(*r.SummaryJSON)
	}
	if r.TagsJSON != nil && *r.TagsJSON != "" {
		if err := json.Unmarshal([]byte(*r.TagsJSON), &r.Tags); err != nil {
			return err
		}
	}
	return nil
}

// TableEvalRunEntry is one dataset row in a run: the variables the row produced, what
// the target returned for it, and where it stands.
type TableEvalRunEntry struct {
	ID    string `gorm:"type:varchar(36);primaryKey" json:"id"`
	RunID string `gorm:"type:varchar(36);not null;uniqueIndex:idx_evals_run_entry_seq;index:idx_evals_run_entry_status" json:"run_id"`
	// Seq is the entry's position in the run. Entries are listed by it.
	Seq int64 `gorm:"not null;uniqueIndex:idx_evals_run_entry_seq" json:"seq"`
	// DatasetRowID is the row the entry came from. Comparing runs joins on it.
	DatasetRowID string `gorm:"type:varchar(36);not null;index" json:"dataset_row_id"`
	Status       string `gorm:"type:varchar(16);not null;index:idx_evals_run_entry_status" json:"status"`
	// Attempt counts how many times the entry was started. A rerun raises it.
	Attempt int `gorm:"not null;default:0" json:"attempt"`
	// Variables is what the row gives the evaluators, as a JSON object of strings.
	VariablesJSON string            `gorm:"type:text;not null;column:variables_json" json:"-"`
	Variables     map[string]string `gorm:"-" json:"variables"`
	// What the target returned. There is no size cap on any of these.
	Output    string  `gorm:"type:text" json:"output"`
	ToolCalls *string `gorm:"type:text;column:tool_calls_json" json:"tool_calls,omitempty"`
	Context   *string `gorm:"type:text" json:"context,omitempty"`
	// Passed is true when every evaluator that has a pass rule passed. Nil until the
	// entry completes, and when no evaluator has a rule.
	Passed *bool `gorm:"index" json:"passed,omitempty"`
	// The cost of the target call.
	TargetLatencyMs        int64    `gorm:"not null;default:0" json:"target_latency_ms"`
	TargetPromptTokens     int      `gorm:"not null;default:0" json:"target_prompt_tokens"`
	TargetCompletionTokens int      `gorm:"not null;default:0" json:"target_completion_tokens"`
	TargetTotalTokens      int      `gorm:"not null;default:0" json:"target_total_tokens"`
	TargetCost             *float64 `json:"target_cost,omitempty"`
	// KeyID is the eval key whose queue the entry's target call waits on, or "".
	KeyID     string     `gorm:"type:varchar(255)" json:"key_id,omitempty"`
	Error     string     `gorm:"type:text" json:"error,omitempty"`
	QueuedAt  time.Time  `gorm:"not null;index" json:"queued_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// TableName for TableEvalRunEntry
func (TableEvalRunEntry) TableName() string { return "evals_run_entries" }

// BeforeSave GORM hook to serialize JSON fields
func (e *TableEvalRunEntry) BeforeSave(tx *gorm.DB) error {
	e.VariablesJSON = "{}"
	if len(e.Variables) > 0 {
		data, err := json.Marshal(e.Variables)
		if err != nil {
			return err
		}
		e.VariablesJSON = string(data)
	}
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (e *TableEvalRunEntry) AfterFind(tx *gorm.DB) error {
	if e.VariablesJSON != "" && e.VariablesJSON != "{}" {
		return json.Unmarshal([]byte(e.VariablesJSON), &e.Variables)
	}
	return nil
}

// TableEvalRunResult is one evaluator's score for one entry. Scores are rows, not a
// blob on the entry, so aggregates are plain queries and an evaluator can be added to
// a finished run by inserting rows.
type TableEvalRunResult struct {
	ID          string `gorm:"type:varchar(36);primaryKey" json:"id"`
	RunID       string `gorm:"type:varchar(36);not null;index:idx_evals_run_result_evaluator" json:"run_id"`
	EntryID     string `gorm:"type:varchar(36);not null;uniqueIndex:idx_evals_run_result_entry" json:"entry_id"`
	EvaluatorID string `gorm:"type:varchar(36);not null;uniqueIndex:idx_evals_run_result_entry;index:idx_evals_run_result_evaluator" json:"evaluator_id"`
	Status      string `gorm:"type:varchar(16);not null;index" json:"status"`
	// Score is the evaluator's score as the engine produced it.
	ScoreJSON *string         `gorm:"type:text;column:score_json" json:"-"`
	Score     json.RawMessage `gorm:"-" json:"score,omitempty"`
	// ScoreNumber is the score as a number, when it has one: the number itself, or 1
	// and 0 for yes and no. Comparing two runs row by row is a query on it.
	ScoreNumber *float64 `json:"score_number,omitempty"`
	Pass        *bool    `json:"pass,omitempty"`
	Reasoning   string   `gorm:"type:text" json:"reasoning,omitempty"`
	// The cost of the evaluator call.
	LatencyMs        int64    `gorm:"not null;default:0" json:"latency_ms"`
	PromptTokens     int      `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int      `gorm:"not null;default:0" json:"completion_tokens"`
	TotalTokens      int      `gorm:"not null;default:0" json:"total_tokens"`
	Cost             *float64 `json:"cost,omitempty"`
	// KeyID is the eval key whose queue the result's job waits on, or "".
	KeyID string `gorm:"type:varchar(255)" json:"key_id,omitempty"`
	// Attempt counts how many times the evaluator was tried. A failed call is retried.
	Attempt int    `gorm:"not null;default:0" json:"attempt"`
	Error   string `gorm:"type:text" json:"error,omitempty"`
	// ReviewedBy is who scored a human evaluator's result.
	ReviewedBy  string     `gorm:"type:varchar(255)" json:"reviewed_by,omitempty"`
	QueuedAt    time.Time  `gorm:"not null;index" json:"queued_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TableName for TableEvalRunResult
func (TableEvalRunResult) TableName() string { return "evals_run_results" }

// BeforeSave GORM hook to serialize JSON fields
func (r *TableEvalRunResult) BeforeSave(tx *gorm.DB) error {
	if len(r.Score) > 0 {
		r.ScoreJSON = new(string(r.Score))
	}
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (r *TableEvalRunResult) AfterFind(tx *gorm.DB) error {
	if r.ScoreJSON != nil && *r.ScoreJSON != "" {
		r.Score = json.RawMessage(*r.ScoreJSON)
	}
	return nil
}
