package tables

import "time"

// TableLogStoreConfig represents the configuration for the log store in the database
type TableLogStoreConfig struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Enabled   bool      `json:"enabled"`
	Type      string    `gorm:"type:varchar(50);not null" json:"type"` // "sqlite"
	Config    *string   `gorm:"type:text" json:"config"`               // JSON serialized logstore.Config
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableLogStoreConfig) TableName() string { return "config_log_store" }
