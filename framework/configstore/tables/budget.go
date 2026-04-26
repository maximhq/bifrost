package tables

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// TableBudget defines spending limits with configurable reset periods
type TableBudget struct {
	ID            string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	MaxLimit      float64   `gorm:"not null" json:"max_limit"`                       // Maximum budget in dollars
	ResetDuration string    `gorm:"type:varchar(50);not null" json:"reset_duration"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	LastReset     time.Time `gorm:"index" json:"last_reset"`                         // Last time budget was reset
	CurrentUsage  float64   `gorm:"default:0" json:"current_usage"`                  // Current usage in dollars

	// Owner FKs: a budget belongs to at most one Team, one VK, or one ProviderConfig
	TeamID           *string `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	VirtualKeyID     *string `gorm:"type:varchar(255);index" json:"virtual_key_id,omitempty"`
	ProviderConfigID *uint   `gorm:"index" json:"provider_config_id,omitempty"`

	CalendarAligned bool `gorm:"default:false" json:"calendar_aligned"` // When true, all budgets under this VK reset at clean calendar boundaries

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableBudget) TableName() string { return "governance_budgets" }

// BeforeSave hook for Budget to validate reset duration format and max limit
func (b *TableBudget) BeforeSave(tx *gorm.DB) error {
	// A budget belongs to at most one owner type
	owners := 0
	if b.TeamID != nil {
		owners++
	}
	if b.VirtualKeyID != nil {
		owners++
	}
	if b.ProviderConfigID != nil {
		owners++
	}
	if owners > 1 {
		return fmt.Errorf("budget cannot have more than one owner (team/virtual key/provider config)")
	}
	// Validate that ResetDuration is in correct format (e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y")
	if d, err := ParseDuration(b.ResetDuration); err != nil {
		return fmt.Errorf("invalid reset duration format: %s", b.ResetDuration)
	} else if d <= 0 {
		return fmt.Errorf("reset duration must be > 0: %s", b.ResetDuration)
	}
	// Validate that MaxLimit is not negative (budgets should be positive)
	if b.MaxLimit < 0 {
		return fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)
	}

	return nil
}
