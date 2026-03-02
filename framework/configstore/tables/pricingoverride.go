package tables

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

type TablePricingOverride struct {
	ID               string                           `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name             string                           `gorm:"type:varchar(255);not null" json:"name"`
	ScopeKind        schemas.PricingOverrideScopeKind `gorm:"type:varchar(50);index:idx_pricing_override_scope;not null" json:"scope_kind"`
	VirtualKeyID     *string                          `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"virtual_key_id,omitempty"`
	ProviderID       *string                          `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"provider_id,omitempty"`
	ProviderKeyID    *string                          `gorm:"type:varchar(255);index:idx_pricing_override_scope" json:"provider_key_id,omitempty"`
	MatchType        schemas.PricingOverrideMatchType `gorm:"type:varchar(20);index:idx_pricing_override_match;not null" json:"match_type"`
	Pattern          string                           `gorm:"type:varchar(255);not null" json:"pattern"`
	RequestTypesJSON string                           `gorm:"type:text" json:"-"`
	PricingPatchJSON string                           `gorm:"type:text" json:"-"`
	ConfigHash       string                           `gorm:"type:varchar(255);null" json:"config_hash,omitempty"`
	CreatedAt        time.Time                        `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time                        `gorm:"index;not null" json:"updated_at"`

	RequestTypes []schemas.RequestType        `gorm:"-" json:"request_types,omitempty"`
	Patch        schemas.PricingOverridePatch `gorm:"-" json:"patch,omitempty"`
}

func (TablePricingOverride) TableName() string { return "governance_pricing_overrides" }

func (p *TablePricingOverride) BeforeSave(tx *gorm.DB) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}

	if err := schemas.ValidatePricingOverrideScopeKind(p.ScopeKind, p.VirtualKeyID, p.ProviderID, p.ProviderKeyID); err != nil {
		return err
	}

	normalizedPattern, err := schemas.ValidatePricingOverridePattern(p.MatchType, p.Pattern)
	if err != nil {
		return err
	}
	p.Pattern = normalizedPattern

	if err := schemas.ValidatePricingOverrideRequestTypes(p.RequestTypes); err != nil {
		return err
	}

	if err := schemas.ValidatePricingOverridePatchNonNegative(p.Patch); err != nil {
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
