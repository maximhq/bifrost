package tables

import (
	"encoding/json"

	"gorm.io/gorm"
)

// TableModelCatalogEntry stores arbitrary per-model key-value attributes
// (e.g. {"description": "..."}). Decoupled from the pricing table so it is
// safe from pricing sync's bulk delete-and-recreate path, and so new
// attributes can be added without a schema migration.
type TableModelCatalogEntry struct {
	ID             uint              `gorm:"primaryKey;autoIncrement" json:"id"`
	Model          string            `gorm:"type:varchar(255);not null;uniqueIndex:idx_catalog_model_provider" json:"model"`
	Provider       string            `gorm:"type:varchar(50);not null;uniqueIndex:idx_catalog_model_provider" json:"provider"`
	AttributesJSON string            `gorm:"type:text;not null;default:'{}'" json:"-"`
	Attributes     map[string]string `gorm:"-" json:"attributes,omitempty"`
}

// TableName sets the table name.
func (TableModelCatalogEntry) TableName() string { return "governance_model_catalog" }

// BeforeSave marshals Attributes → AttributesJSON. A nil/empty map serializes
// to "{}" so the not-null column constraint always holds.
func (e *TableModelCatalogEntry) BeforeSave(tx *gorm.DB) error {
	if e.Attributes == nil {
		e.AttributesJSON = "{}"
		return nil
	}
	data, err := json.Marshal(e.Attributes)
	if err != nil {
		return err
	}
	e.AttributesJSON = string(data)
	return nil
}

// AfterFind unmarshals AttributesJSON → Attributes. Empty/missing JSON resolves
// to a nil map so callers can use len() and idiomatic nil checks.
func (e *TableModelCatalogEntry) AfterFind(tx *gorm.DB) error {
	if e.AttributesJSON == "" || e.AttributesJSON == "{}" {
		e.Attributes = nil
		return nil
	}
	var attrs map[string]string
	if err := json.Unmarshal([]byte(e.AttributesJSON), &attrs); err != nil {
		return err
	}
	e.Attributes = attrs
	return nil
}
