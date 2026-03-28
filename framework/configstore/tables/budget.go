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

	// CalendarAligned snaps LastReset to the start of the current calendar period (day, week, month, year)
	// instead of the exact creation/update time, so budgets reset at clean calendar boundaries.
	CalendarAligned bool `gorm:"default:false" json:"calendar_aligned"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableBudget) TableName() string { return "governance_budgets" }

// TableVirtualKeyBudget is a junction table for VK-level multi-budget support
type TableVirtualKeyBudget struct {
	ID           uint        `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualKeyID string      `gorm:"type:varchar(255);not null;uniqueIndex:idx_vk_budget" json:"virtual_key_id"`
	BudgetID     string      `gorm:"type:varchar(255);not null;uniqueIndex:idx_vk_budget" json:"budget_id"`
	Budget       TableBudget `gorm:"foreignKey:BudgetID;constraint:OnDelete:CASCADE" json:"budget"`
}

// TableName for TableVirtualKeyBudget
func (TableVirtualKeyBudget) TableName() string { return "governance_virtual_key_budgets" }

// TableVirtualKeyProviderConfigBudget is a junction table for provider-config-level multi-budget support
type TableVirtualKeyProviderConfigBudget struct {
	ID               uint        `gorm:"primaryKey;autoIncrement" json:"id"`
	ProviderConfigID uint        `gorm:"not null;uniqueIndex:idx_pc_budget" json:"provider_config_id"`
	BudgetID         string      `gorm:"type:varchar(255);not null;uniqueIndex:idx_pc_budget" json:"budget_id"`
	Budget           TableBudget `gorm:"foreignKey:BudgetID;constraint:OnDelete:CASCADE" json:"budget"`
}

// TableName for TableVirtualKeyProviderConfigBudget
func (TableVirtualKeyProviderConfigBudget) TableName() string {
	return "governance_virtual_key_provider_config_budgets"
}

// BeforeSave hook for Budget to validate reset duration format and max limit
func (b *TableBudget) BeforeSave(tx *gorm.DB) error {
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
