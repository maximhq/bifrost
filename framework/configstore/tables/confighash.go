// Package tables contains the database tables for the configstore.
package tables

import "time"

// TableConfigHash represents the configuration hash in the database
type TableConfigHash struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Hash      string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"hash"`
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableConfigHash) TableName() string { return "config_hashes" }
