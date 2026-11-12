package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// Statuses of an evaluation execution.
const (
	EvalExecutionQueued    = "queued"
	EvalExecutionRunning   = "running"
	EvalExecutionCompleted = "completed"
)

// TableEvalExecution is one evaluator run requested through the API. It holds the
// input while the job waits in the queue and the result once it has run. The
// definition is a snapshot, so an unsaved draft can be run and a later edit to the
// evaluator cannot change what a queued job runs.
type TableEvalExecution struct {
	ID               string          `gorm:"type:varchar(36);primaryKey" json:"id"`
	EvaluatorID      *string         `gorm:"type:varchar(36);index" json:"evaluator_id,omitempty"` // nil for an unsaved draft
	EvaluatorVersion int             `json:"evaluator_version,omitempty"`
	DefinitionJSON   string          `gorm:"type:text;not null;column:definition_json" json:"-"`
	Definition       json.RawMessage `gorm:"-" json:"definition"` // opaque here; framework/evals owns the typed form
	VariablesJSON    string          `gorm:"type:text;not null;column:variables_json" json:"-"`
	Variables        json.RawMessage `gorm:"-" json:"variables"`
	Status           string          `gorm:"type:varchar(16);not null;index" json:"status"`
	ResultJSON       *string         `gorm:"type:text;column:result_json" json:"-"`
	Result           json.RawMessage `gorm:"-" json:"result,omitempty"`
	KeyID            string          `gorm:"type:varchar(255)" json:"key_id,omitempty"` // provider key the run used, if any
	CreatedBy        string          `gorm:"type:varchar(255)" json:"created_by,omitempty"`
	CreatedAt        time.Time       `gorm:"not null;index" json:"created_at"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
}

// TableName for TableEvalExecution
func (TableEvalExecution) TableName() string { return "evals_executions" }

// BeforeSave GORM hook to serialize JSON fields
func (e *TableEvalExecution) BeforeSave(tx *gorm.DB) error {
	e.DefinitionJSON = rawJSONOrEmptyObject(e.Definition)
	e.VariablesJSON = rawJSONOrEmptyObject(e.Variables)
	if len(e.Result) > 0 {
		e.ResultJSON = new(string(e.Result))
	}
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (e *TableEvalExecution) AfterFind(tx *gorm.DB) error {
	if e.DefinitionJSON != "" {
		e.Definition = json.RawMessage(e.DefinitionJSON)
	}
	if e.VariablesJSON != "" {
		e.Variables = json.RawMessage(e.VariablesJSON)
	}
	if e.ResultJSON != nil && *e.ResultJSON != "" {
		e.Result = json.RawMessage(*e.ResultJSON)
	}
	return nil
}

// rawJSONOrEmptyObject renders raw JSON for a not-null text column.
func rawJSONOrEmptyObject(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}
