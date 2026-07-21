package tables

import (
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
)

// BudgetOverrideMode identifies how long a budget override remains active.
type BudgetOverrideMode string

const (
	// BudgetOverrideModeCycles keeps an override active for a finite number of reset cycles.
	BudgetOverrideModeCycles BudgetOverrideMode = "cycles"
	// BudgetOverrideModeForever keeps an override active until it is explicitly removed.
	BudgetOverrideModeForever BudgetOverrideMode = "forever"
)

// TableBudget defines spending limits with configurable reset periods
type TableBudget struct {
	ID            string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	MaxLimit      float64   `gorm:"not null" json:"max_limit"`                       // Maximum budget in dollars
	ResetDuration string    `gorm:"type:varchar(50);not null" json:"reset_duration"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	LastReset     time.Time `gorm:"index" json:"last_reset"`                         // Last time budget was reset
	CurrentUsage  float64   `gorm:"default:0" json:"current_usage"`                  // Current usage in dollars

	// OverrideAmount is added to MaxLimit while the override is active.
	OverrideAmount float64 `gorm:"not null;default:0" json:"override_amount,omitempty"`
	// OverrideMode distinguishes finite cycle-based overrides from permanent overrides.
	OverrideMode BudgetOverrideMode `gorm:"type:varchar(20);not null;default:''" json:"override_mode,omitempty"`
	// OverrideCyclesRemaining includes the current cycle and is positive only for cycle-based overrides.
	OverrideCyclesRemaining int `gorm:"not null;default:0" json:"override_cycles_remaining,omitempty"`

	// Owner FKs: a budget belongs to at most one Team, VK, ProviderConfig, ModelConfig, or Customer
	TeamID           *string `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	VirtualKeyID     *string `gorm:"type:varchar(255);index" json:"virtual_key_id,omitempty"`
	ProviderConfigID *uint   `gorm:"index" json:"provider_config_id,omitempty"`
	ModelConfigID    *string `gorm:"type:varchar(255);index" json:"model_config_id,omitempty"`
	CustomerID       *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`

	// Deprecated: set calendar_aligned on the parent access profile / VK / team
	// instead. Kept for backward compatibility with older config.json files;
	// the OSS applyV1Compat path and the enterprise access-profile reconciler
	// promote any true value here to the owner's top-level CalendarAligned at
	// load time.
	CalendarAlignedInput *bool `gorm:"-" json:"calendar_aligned,omitempty"`

	// Derived from the owning entity (VK / PC's parent VK / Team). Populated by
	// the owner's AfterFind hook on cold load and by the governance store's
	// Create/Update *InMemory methods on write. Never persisted; consumed by
	// the reset path to decide rolling vs. calendar-aligned window.
	IsCalendarAligned bool `gorm:"-" json:"-"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableBudget) TableName() string { return "governance_budgets" }

// validateOverride checks that the persisted override fields form one unambiguous state.
func (b *TableBudget) validateOverride() error {
	if math.IsNaN(b.OverrideAmount) || math.IsInf(b.OverrideAmount, 0) {
		return fmt.Errorf("budget override_amount must be finite")
	}
	switch b.OverrideMode {
	case "":
		if b.OverrideAmount != 0 || b.OverrideCyclesRemaining != 0 {
			return fmt.Errorf("budget override fields require override_mode")
		}
	case BudgetOverrideModeCycles:
		if b.OverrideAmount <= 0 {
			return fmt.Errorf("budget override_amount must be > 0 for cycles mode")
		}
		if b.OverrideCyclesRemaining <= 0 {
			return fmt.Errorf("budget override_cycles_remaining must be > 0 for cycles mode")
		}
	case BudgetOverrideModeForever:
		if b.OverrideAmount <= 0 {
			return fmt.Errorf("budget override_amount must be > 0 for forever mode")
		}
		if b.OverrideCyclesRemaining != 0 {
			return fmt.Errorf("budget override_cycles_remaining must be 0 for forever mode")
		}
	default:
		return fmt.Errorf("invalid budget override_mode: %s", b.OverrideMode)
	}
	return nil
}

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
	if b.ModelConfigID != nil {
		owners++
	}
	if b.CustomerID != nil {
		owners++
	}
	if owners > 1 {
		return fmt.Errorf("budget cannot have more than one owner (team/virtual key/provider config/model config/customer)")
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
	if err := b.validateOverride(); err != nil {
		return err
	}

	return nil
}
