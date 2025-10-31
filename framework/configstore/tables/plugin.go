package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// TablePlugin represents a plugin configuration in the database

type TablePlugin struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name       string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	Enabled    bool      `json:"enabled"`
	Path       *string   `json:"path,omitempty"`
	ConfigJSON string    `gorm:"type:text" json:"-"` // JSON serialized plugin.Config
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	Config any `gorm:"-" json:"config,omitempty"`
}

// TableName sets the table name for each model
func (TablePlugin) TableName() string { return "config_plugins" }

// BeforeSave hooks for serialization
func (p *TablePlugin) BeforeSave(tx *gorm.DB) error {
	if p.Config != nil {
		data, err := json.Marshal(p.Config)
		if err != nil {
			return err
		}
		p.ConfigJSON = string(data)
	} else {
		p.ConfigJSON = "{}"
	}

	return nil
}

// AfterFind hooks for deserialization
func (p *TablePlugin) AfterFind(tx *gorm.DB) error {
	if p.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(p.ConfigJSON), &p.Config); err != nil {
			return err
		}
	} else {
		p.Config = nil
	}

	return nil
}
