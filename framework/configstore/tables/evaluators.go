package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// TableEvaluator is an evaluator definition. Its config lives on its version rows;
// CurrentVersion points at the one in use.
type TableEvaluator struct {
	ID             string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name           string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_evals_evaluator_name" json:"name"`
	Description    string    `gorm:"type:text" json:"description"`
	Type           string    `gorm:"type:varchar(32);not null;index" json:"type"`
	BuiltinKey     *string   `gorm:"type:varchar(64);index" json:"builtin_key,omitempty"` // catalog key for installed built-ins
	CurrentVersion int       `gorm:"not null;default:1" json:"current_version"`
	TagsJSON       *string   `gorm:"type:text;column:tags_json" json:"-"`
	Tags           []string  `gorm:"-" json:"tags,omitempty"`
	CreatedBy      string    `gorm:"type:varchar(255)" json:"created_by,omitempty"`
	DeletedAt      int64     `gorm:"column:deleted_at_unix_nano;not null;default:0;uniqueIndex:idx_evals_evaluator_name" json:"-"` // 0 while live
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time `gorm:"not null" json:"updated_at"`
}

// TableName for TableEvaluator
func (TableEvaluator) TableName() string { return "evals_evaluators" }

// BeforeSave GORM hook to serialize JSON fields
func (e *TableEvaluator) BeforeSave(tx *gorm.DB) error {
	if e.Tags == nil {
		e.TagsJSON = nil
		return nil
	}
	data, err := json.Marshal(e.Tags)
	if err != nil {
		return err
	}
	e.TagsJSON = new(string(data))
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (e *TableEvaluator) AfterFind(tx *gorm.DB) error {
	if e.TagsJSON != nil && *e.TagsJSON != "" {
		if err := json.Unmarshal([]byte(*e.TagsJSON), &e.Tags); err != nil {
			return err
		}
	}
	return nil
}
