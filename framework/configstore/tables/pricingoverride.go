package tables

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/pricingoverrides"
	"gorm.io/gorm"
)

// TablePricingOverride is the persistence model for governance pricing
// overrides.
type TablePricingOverride struct {
	ID               string                     `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name             string                     `gorm:"type:varchar(255);not null" json:"name"`
	ScopeKind        pricingoverrides.ScopeKind `gorm:"type:varchar(50);index:idx_pricing_override_scope;not null" json:"scope_kind"`
	VirtualKeyID     *string                    `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"virtual_key_id,omitempty"`
	ProviderID       *string                    `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"provider_id,omitempty"`
	ProviderKeyID    *string                    `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"provider_key_id,omitempty"`
	MatchType        pricingoverrides.MatchType `gorm:"type:varchar(20);index:idx_pricing_override_match;not null" json:"match_type"`
	Pattern          string                     `gorm:"type:varchar(255);not null" json:"pattern"`
	RequestTypesJSON string                     `gorm:"type:text" json:"-"`
	PricingPatchJSON string                     `gorm:"type:text" json:"-"`
	ConfigHash       string                     `gorm:"type:varchar(255);null" json:"config_hash,omitempty"`
	CreatedAt        time.Time                  `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time                  `gorm:"index;not null" json:"updated_at"`

	RequestTypes []schemas.RequestType  `gorm:"-" json:"request_types,omitempty"`
	Patch        pricingoverrides.Patch `gorm:"-" json:"patch,omitempty"`
}

// TableName returns the backing table name for governance pricing overrides.
func (TablePricingOverride) TableName() string { return "governance_pricing_overrides" }

// BeforeSave validates and serializes the sparse pricing override fields before
// the row is persisted.
func (p *TablePricingOverride) BeforeSave(tx *gorm.DB) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}

	if err := pricingoverrides.ValidateScopeKind(p.ScopeKind, p.VirtualKeyID, p.ProviderID, p.ProviderKeyID); err != nil {
		return err
	}

	normalizedPattern, err := pricingoverrides.ValidatePattern(p.MatchType, p.Pattern)
	if err != nil {
		return err
	}
	p.Pattern = normalizedPattern

	if err := pricingoverrides.ValidateRequestTypes(p.RequestTypes); err != nil {
		return err
	}

	if err := pricingoverrides.ValidatePatchNonNegative(p.Patch); err != nil {
		return err
	}

	if len(p.RequestTypes) > 0 {
		b, err := json.Marshal(p.RequestTypes)
		if err != nil {
			return err
		}
		p.RequestTypesJSON = string(b)
	} else {
		p.RequestTypesJSON = ""
	}

	b, err := json.Marshal(p.Patch)
	if err != nil {
		return err
	}
	p.PricingPatchJSON = string(b)

	return nil
}

// AfterFind restores the request type and patch fields from their persisted
// JSON columns.
func (p *TablePricingOverride) AfterFind(tx *gorm.DB) error {
	if p.RequestTypesJSON != "" {
		if err := json.Unmarshal([]byte(p.RequestTypesJSON), &p.RequestTypes); err != nil {
			return err
		}
	}
	if p.PricingPatchJSON != "" {
		if err := json.Unmarshal([]byte(p.PricingPatchJSON), &p.Patch); err != nil {
			return err
		}
	}
	return nil
}

// ToPricingOverride converts the persisted row into the shared pricing override
// contract used by runtime components.
func (p TablePricingOverride) ToPricingOverride() pricingoverrides.Override {
	return pricingoverrides.Override{
		ID:            p.ID,
		Name:          p.Name,
		ScopeKind:     p.ScopeKind,
		VirtualKeyID:  p.VirtualKeyID,
		ProviderID:    p.ProviderID,
		ProviderKeyID: p.ProviderKeyID,
		MatchType:     p.MatchType,
		Pattern:       p.Pattern,
		RequestTypes:  p.RequestTypes,
		Patch:         p.Patch,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}

// TablePricingOverrideFromPricingOverride converts the shared runtime override
// contract into its persistence representation.
func TablePricingOverrideFromPricingOverride(override pricingoverrides.Override) TablePricingOverride {
	return TablePricingOverride{
		ID:            override.ID,
		Name:          override.Name,
		ScopeKind:     override.ScopeKind,
		VirtualKeyID:  override.VirtualKeyID,
		ProviderID:    override.ProviderID,
		ProviderKeyID: override.ProviderKeyID,
		MatchType:     override.MatchType,
		Pattern:       override.Pattern,
		RequestTypes:  override.RequestTypes,
		Patch:         override.Patch,
		CreatedAt:     override.CreatedAt,
		UpdatedAt:     override.UpdatedAt,
	}
}
