package tables

import "time"

// TableModel represents a model configuration in the database
type TableModel struct {
	ID         string    `gorm:"primaryKey" json:"id"`
	ProviderID uint      `gorm:"index;not null;uniqueIndex:idx_provider_name" json:"provider_id"`
	Name       string    `gorm:"uniqueIndex:idx_provider_name" json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName sets the table name for each model
func (TableModel) TableName() string { return "config_models" }
