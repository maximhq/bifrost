// Package tables provides tables for the configstore
package tables

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TablePromptSession represents a mutable working draft/session for a prompt
// Sessions belong to a prompt and can optionally be based on a specific version
type TablePromptSession struct {
	ID              uint                `gorm:"primaryKey;autoIncrement" json:"id"`
	PromptID        string              `gorm:"type:varchar(36);not null;index" json:"prompt_id"`
	Prompt          *TablePrompt        `gorm:"foreignKey:PromptID" json:"prompt,omitempty"`
	VersionID       *uint               `gorm:"index" json:"version_id,omitempty"` // Optional - session may or may not be based on a version
	Version         *TablePromptVersion `gorm:"foreignKey:VersionID;constraint:OnDelete:SET NULL" json:"version,omitempty"`
	Name            string              `gorm:"type:varchar(255)" json:"name"`
	ModelParamsJSON *string             `gorm:"type:text;column:model_params_json" json:"-"`
	ModelParams     ModelParams         `gorm:"-" json:"model_params"`
	Provider        string              `gorm:"type:varchar(100)" json:"provider"`
	Model           string              `gorm:"type:varchar(100)" json:"model"`
	VariablesJSON   *string             `gorm:"type:text;column:variables_json" json:"-"`
	Variables       PromptVariables     `gorm:"-" json:"variables,omitempty"` // {key: value} map for Jinja2 variables
	CreatedAt       time.Time           `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time           `gorm:"not null" json:"updated_at"`

	// Relationships
	Messages []TablePromptSessionMessage `gorm:"foreignKey:SessionID;constraint:OnDelete:CASCADE" json:"messages,omitempty"`
}

// TableName for TablePromptSession
func (TablePromptSession) TableName() string { return "prompt_sessions" }

// BeforeSave GORM hook to serialize JSON fields
func (s *TablePromptSession) BeforeSave(tx *gorm.DB) error {
	data, err := json.Marshal(s.ModelParams)
	if err != nil {
		return err
	}
	paramsStr := string(data)
	s.ModelParamsJSON = &paramsStr

	if s.Variables != nil {
		varsData, err := json.Marshal(s.Variables)
		if err != nil {
			return err
		}
		varsStr := string(varsData)
		s.VariablesJSON = &varsStr
	} else {
		s.VariablesJSON = nil
	}
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (s *TablePromptSession) AfterFind(tx *gorm.DB) error {
	if s.ModelParamsJSON != nil && *s.ModelParamsJSON != "" {
		dec := json.NewDecoder(strings.NewReader(*s.ModelParamsJSON))
		dec.UseNumber()
		if err := dec.Decode(&s.ModelParams); err != nil {
			return err
		}
	}
	if s.VariablesJSON != nil && *s.VariablesJSON != "" {
		var vars PromptVariables
		if err := json.Unmarshal([]byte(*s.VariablesJSON), &vars); err != nil {
			return err
		}
		s.Variables = vars
	} else {
		s.Variables = nil
	}
	return nil
}

// TablePromptSessionMessage represents a message in a mutable prompt session
type TablePromptSessionMessage struct {
	ID          uint                `gorm:"primaryKey;autoIncrement" json:"id"`
	PromptID    string              `gorm:"type:varchar(36);not null;index" json:"prompt_id"`
	SessionID   uint                `gorm:"not null;index;uniqueIndex:idx_session_order" json:"session_id"`
	Session     *TablePromptSession `gorm:"foreignKey:SessionID" json:"-"`
	OrderIndex  int                 `gorm:"not null;uniqueIndex:idx_session_order" json:"order_index"`
	MessageJSON string              `gorm:"type:text;not null;column:message_json" json:"-"`
	Message     PromptMessage       `gorm:"-" json:"message"`
}

// TableName for TablePromptSessionMessage
func (TablePromptSessionMessage) TableName() string { return "prompt_session_messages" }

// BeforeSave GORM hook to serialize JSON fields
func (m *TablePromptSessionMessage) BeforeSave(tx *gorm.DB) error {
	data, err := json.Marshal(m.Message)
	if err != nil {
		return err
	}
	m.MessageJSON = string(data)
	return nil
}

// AfterFind GORM hook to deserialize JSON fields
func (m *TablePromptSessionMessage) AfterFind(tx *gorm.DB) error {
	if m.MessageJSON != "" {
		if err := json.Unmarshal([]byte(m.MessageJSON), &m.Message); err != nil {
			return err
		}
	}
	return nil
}
