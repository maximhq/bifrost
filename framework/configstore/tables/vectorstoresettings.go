package tables

import "time"

// TableVectorStoreSettings is one configured vector store's settings, keyed by the
// store's name in config.json: how long vectors built from logs are kept in it and
// its storage limit. ConfigHash is the hash of the values config.json last gave, so
// a dashboard edit survives restarts until config.json changes them.
type TableVectorStoreSettings struct {
	Name          string    `gorm:"primaryKey;type:varchar(64)" json:"name"`
	RetentionDays int       `gorm:"not null;default:0" json:"retention_days"` // 0 follows the logs' retention
	MaxSizeMB     int       `gorm:"not null;default:0" json:"max_size_mb"`    // 0 sets no limit
	ConfigHash    string    `gorm:"type:varchar(255)" json:"-"`
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time `gorm:"not null" json:"updated_at"`
}

// TableName sets the table name.
func (TableVectorStoreSettings) TableName() string { return "config_vector_store_settings" }
