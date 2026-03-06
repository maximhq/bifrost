// Package tables provides tables for the configstore
package tables

import (
	"time"
)

// TablePrompt represents a prompt entity that can have multiple versions and sessions
type TablePrompt struct {
	ID        string       `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name      string       `gorm:"type:varchar(255);not null" json:"name"`
	FolderID  *string      `gorm:"type:varchar(36);index" json:"folder_id,omitempty"`
	Folder    *TableFolder `gorm:"foreignKey:FolderID" json:"folder,omitempty"`
	CreatedAt time.Time    `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time    `gorm:"not null" json:"updated_at"`
	ConfigHash string      `gorm:"type:varchar(64)" json:"-"`

	// Relationships
	Versions []TablePromptVersion `gorm:"foreignKey:PromptID" json:"versions,omitempty"`
	Sessions []TablePromptSession `gorm:"foreignKey:PromptID" json:"sessions,omitempty"`

	// Virtual fields (not stored in DB)
	LatestVersion *TablePromptVersion `gorm:"-" json:"latest_version,omitempty"`
	VersionsCount int                 `gorm:"-" json:"versions_count,omitempty"`
}

// TableName for TablePrompt
func (TablePrompt) TableName() string { return "prompts" }
