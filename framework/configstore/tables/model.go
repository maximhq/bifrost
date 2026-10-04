package tables

import (
	"time"

	"gorm.io/gorm"
)

// TableModel represents a model configuration in the database.
//
// Rows are sparse per-model overrides keyed by (provider_id, name): a row exists only for a
// model that carries operator data (today, tags). Any model name is allowed, including models
// that are not in the pricing datasheet.
type TableModel struct {
	ID         string `gorm:"primaryKey" json:"id"`
	ProviderID uint   `gorm:"index;not null;uniqueIndex:idx_provider_name" json:"provider_id"`
	Name       string `gorm:"uniqueIndex:idx_provider_name" json:"name"`
	// Tags are free-form labels on the model, normalized by NormalizeTags (trimmed,
	// de-duplicated, sorted). An empty list is stored as NULL.
	Tags      []string  `gorm:"type:text;serializer:json" json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName sets the table name for each model
func (TableModel) TableName() string { return "config_models" }

// BeforeSave normalizes and validates the model's tags.
func (m *TableModel) BeforeSave(tx *gorm.DB) error {
	tags, err := NormalizeTags(m.Tags)
	if err != nil {
		return err
	}
	m.Tags = tags
	return nil
}
