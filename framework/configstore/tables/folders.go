// Package tables provides tables for the configstore
package tables

import (
	"time"
)

// TableFolder represents a generic folder that can contain prompts
type TableFolder struct {
	ID          string  `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name        string  `gorm:"type:varchar(255);not null" json:"name"`
	Description *string `gorm:"type:text" json:"description,omitempty"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`
	ConfigHash  string    `gorm:"type:varchar(64)" json:"-"`

	// Virtual fields (not stored in DB)
	PromptsCount int `gorm:"-" json:"prompts_count,omitempty"`
}

// TableName for TableFolder
func (TableFolder) TableName() string { return "folders" }
