// Package tables provides tables for the configstore
package tables

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TablePromptVersion represents an immutable version of a prompt
// Once created, a version cannot be modified - to make changes, create a new version
type TablePromptVersion struct {
	ID              uint         `gorm:"primaryKey;autoIncrement" json:"id"`
	PromptID        string       `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_prompt_version" json:"prompt_id"`
	Prompt          *TablePrompt `gorm:"foreignKey:PromptID" json:"prompt,omitempty"`
	VersionNumber   int          `gorm:"not null;uniqueIndex:idx_prompt_version" json:"version_number"`
	CommitMessage   string       `gorm:"type:text" json:"commit_message"`
	ModelParamsJSON *string      `gorm:"type:text;column:model_params_json" json:"-"`
	ModelParams     ModelParams  `gorm:"-" json:"model_params"`
	Provider        string       `gorm:"type:varchar(100)" json:"provider"`
	Model           string       `gorm:"type:varchar(100)" json:"model"`
	IsLatest        bool         `gorm:"not null;default:false" json:"is_latest"`
	CreatedAt       time.Time    `gorm:"not null" json:"created_at"`
	// No UpdatedAt - versions are immutable

	// Relationships
	Messages []TablePromptVersionMessage `gorm:"foreignKey:VersionID;constraint:OnDelete:CASCADE" json:"messages,omitempty"`
}

// TableName for TablePromptVersion
func (TablePromptVersion) TableName() string { return "prompt_versions" }

// ModelParams represents model configuration parameters as a flexible map
// so that any provider-specific params (response_format, seed, logprobs, etc.) are preserved.
type ModelParams map[string]interface{}

// BeforeSave GORM hook to serialize JSON fields
func (v *TablePromptVersion) BeforeSave(tx *gorm.DB) error {
	data, err := json.Marshal(v.ModelParams)
	if err != nil {
		return err
	}
	paramsStr := string(data)
	v.ModelParamsJSON = &paramsStr
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (v *TablePromptVersion) AfterFind(tx *gorm.DB) error {
	if v.ModelParamsJSON != nil && *v.ModelParamsJSON != "" {
		dec := json.NewDecoder(strings.NewReader(*v.ModelParamsJSON))
		dec.UseNumber()
		if err := dec.Decode(&v.ModelParams); err != nil {
			return err
		}
	}
	return nil
}

// TablePromptVersionMessage represents a message in an immutable prompt version
type TablePromptVersionMessage struct {
	ID          uint                `gorm:"primaryKey;autoIncrement" json:"id"`
	PromptID    string              `gorm:"type:varchar(36);not null;index" json:"prompt_id"`
	VersionID   uint                `gorm:"not null;index;uniqueIndex:idx_version_order" json:"version_id"`
	Version     *TablePromptVersion `gorm:"foreignKey:VersionID" json:"-"`
	OrderIndex  int                 `gorm:"not null;uniqueIndex:idx_version_order" json:"order_index"`
	MessageJSON string              `gorm:"type:text;not null;column:message_json" json:"-"`
	Message     PromptMessage       `gorm:"-" json:"message"`
}

// TableName for TablePromptVersionMessage
func (TablePromptVersionMessage) TableName() string { return "prompt_version_messages" }

// PromptMessage is a raw JSON message stored in the database.
// The frontend handles serialization/deserialization of the message format.
// The backend treats it as opaque JSON to remain format-agnostic and backward-compatible.
type PromptMessage = json.RawMessage

// BeforeSave GORM hook to serialize JSON fields
func (m *TablePromptVersionMessage) BeforeSave(tx *gorm.DB) error {
	data, err := json.Marshal(m.Message)
	if err != nil {
		return err
	}
	m.MessageJSON = string(data)
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (m *TablePromptVersionMessage) AfterFind(tx *gorm.DB) error {
	if m.MessageJSON != "" {
		if err := json.Unmarshal([]byte(m.MessageJSON), &m.Message); err != nil {
			return err
		}
	}
	return nil
}
