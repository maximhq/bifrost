package tables

import (
	"encoding/json"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// TablePricingOverride is the persistence model for governance pricing overrides.
type TablePricingOverride struct {
	ID               string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name             string    `gorm:"type:varchar(255);not null" json:"name"`
	ScopeKind        string    `gorm:"type:varchar(50);index:idx_pricing_override_scope;not null" json:"scope_kind"`
	VirtualKeyID     *string   `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"virtual_key_id,omitempty"`
	ProviderID       *string   `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"provider_id,omitempty"`
	ProviderKeyID    *string   `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"provider_key_id,omitempty"`
	MatchType        string    `gorm:"type:varchar(20);index:idx_pricing_override_match;not null" json:"match_type"`
	Pattern          string    `gorm:"type:varchar(255);not null" json:"pattern"`
	RequestTypesJSON string    `gorm:"type:text" json:"-"`
	PricingPatchJSON string    `gorm:"type:text" json:"pricing_patch,omitempty"`
	ConfigHash       string    `gorm:"type:varchar(255);null" json:"config_hash,omitempty"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`

	RequestTypes []schemas.RequestType `gorm:"-" json:"request_types,omitempty"`
}

// TableName returns the backing table name for governance pricing overrides.
func (TablePricingOverride) TableName() string { return "governance_pricing_overrides" }

// BeforeSave serializes virtual fields into their JSON columns before persistence.
func (p *TablePricingOverride) BeforeSave(tx *gorm.DB) error {
	if len(p.RequestTypes) > 0 {
		b, err := json.Marshal(p.RequestTypes)
		if err != nil {
			return err
		}
		p.RequestTypesJSON = string(b)
	} else {
		p.RequestTypesJSON = "[]"
	}
	return nil
}

// AfterFind restores virtual fields from their persisted JSON columns.
func (p *TablePricingOverride) AfterFind(tx *gorm.DB) error {
	if p.RequestTypesJSON != "" {
		if err := json.Unmarshal([]byte(p.RequestTypesJSON), &p.RequestTypes); err != nil {
			return err
		}
	}
	return nil
}
