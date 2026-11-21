package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// TableEvaluatorVersion is an immutable snapshot of an evaluator's config. A config
// change writes a new row, so a result can always name the exact config that produced it.
type TableEvaluatorVersion struct {
	ID          string          `gorm:"type:varchar(36);primaryKey" json:"id"`
	EvaluatorID string          `gorm:"type:varchar(36);not null;uniqueIndex:idx_evals_evaluator_version" json:"evaluator_id"`
	Version     int             `gorm:"not null;uniqueIndex:idx_evals_evaluator_version" json:"version"`
	ConfigJSON  string          `gorm:"type:text;not null;column:config_json" json:"-"`
	Config      json.RawMessage `gorm:"-" json:"config"` // opaque here; framework/evals owns the typed form
	// ModelProvider is the provider this version's model calls go to, or "" when it calls
	// none. It is a copy of what the config says, kept as a column so a listing can
	// filter on it without reading every config.
	ModelProvider string    `gorm:"type:varchar(255);not null;default:'';index" json:"-"`
	CommitMessage string    `gorm:"type:text" json:"commit_message,omitempty"`
	CreatedBy     string    `gorm:"type:varchar(255)" json:"created_by,omitempty"`
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
	// No UpdatedAt - versions are immutable
}

// TableName for TableEvaluatorVersion
func (TableEvaluatorVersion) TableName() string { return "evals_evaluator_versions" }

// BeforeSave GORM hook to serialize JSON fields
func (v *TableEvaluatorVersion) BeforeSave(tx *gorm.DB) error {
	if len(v.Config) == 0 {
		v.ConfigJSON = "{}"
		return nil
	}
	v.ConfigJSON = string(v.Config)
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (v *TableEvaluatorVersion) AfterFind(tx *gorm.DB) error {
	if v.ConfigJSON != "" {
		v.Config = json.RawMessage(v.ConfigJSON)
	}
	return nil
}
