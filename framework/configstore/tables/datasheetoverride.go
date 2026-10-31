package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// TableDatasheetOverride is a user edit of one datasheet entry. The datasheet
// sync never writes this table, so the edit is re-applied on top of every
// upstream refresh of governance_model_pricing and governance_model_parameters.
//
// Both patches are RFC 7386 JSON merge patches: PricingPatch targets the
// pricing row (context_length, max_output_tokens, is_deprecated, cost keys,
// ...) and ParamsPatch targets the model-parameters blob (supports_* flags,
// reasoning levels, ...). Baseline records the upstream value of every patched
// key at save time so a later upstream change can be reported as drift.
type TableDatasheetOverride struct {
	ID               string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Provider         string    `gorm:"type:varchar(50);not null;uniqueIndex:idx_datasheet_override_target" json:"provider"`
	Model            string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_datasheet_override_target" json:"model"`
	Mode             string    `gorm:"type:varchar(50);not null;default:'';uniqueIndex:idx_datasheet_override_target" json:"mode"` // empty applies to every mode
	IsCustom         bool      `gorm:"not null;default:false" json:"is_custom"`                                                    // entry absent from the upstream datasheet
	PricingPatchJSON string    `gorm:"type:text" json:"-"`
	ParamsPatchJSON  string    `gorm:"type:text" json:"-"`
	BaselineJSON     string    `gorm:"type:text" json:"-"`
	ConfigHash       string    `gorm:"type:varchar(255);null" json:"config_hash,omitempty"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`

	PricingPatch json.RawMessage `gorm:"-" json:"pricing_patch,omitempty"`
	ParamsPatch  json.RawMessage `gorm:"-" json:"params_patch,omitempty"`
	Baseline     json.RawMessage `gorm:"-" json:"baseline,omitempty"`
}

// TableName returns the backing table name for datasheet overrides.
func (TableDatasheetOverride) TableName() string { return "governance_datasheet_overrides" }

// BeforeSave serializes the virtual patch fields into their text columns.
func (o *TableDatasheetOverride) BeforeSave(tx *gorm.DB) error {
	o.PricingPatchJSON = rawOrEmpty(o.PricingPatch)
	o.ParamsPatchJSON = rawOrEmpty(o.ParamsPatch)
	o.BaselineJSON = rawOrEmpty(o.Baseline)
	return nil
}

// AfterFind restores the virtual patch fields from their text columns.
func (o *TableDatasheetOverride) AfterFind(tx *gorm.DB) error {
	o.PricingPatch = columnToRaw(o.PricingPatchJSON)
	o.ParamsPatch = columnToRaw(o.ParamsPatchJSON)
	o.Baseline = columnToRaw(o.BaselineJSON)
	return nil
}

func rawOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func columnToRaw(column string) json.RawMessage {
	if column == "" {
		return nil
	}
	return json.RawMessage(column)
}
