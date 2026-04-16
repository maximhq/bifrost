package tables

import (
	"fmt"
	"time"
)

// BudgetExtensionStatus represents the status of a budget extension request
type BudgetExtensionStatus string

const (
	BudgetExtensionStatusPending  BudgetExtensionStatus = "pending"
	BudgetExtensionStatusApproved BudgetExtensionStatus = "approved"
	BudgetExtensionStatusRejected BudgetExtensionStatus = "rejected"
	BudgetExtensionStatusExpired  BudgetExtensionStatus = "expired"
)

// MaxExtensionMultiplier caps the extension amount relative to the base budget's MaxLimit.
// An extension cannot exceed MaxExtensionMultiplier * budget.MaxLimit.
const MaxExtensionMultiplier = 1.0

// MaxExtensionDuration caps how long any single extension can last.
// Prevents effectively permanent overrides (e.g., "365d").
const MaxExtensionDuration = 30 * 24 * time.Hour // 30 days

// TableBudgetExtension represents a temporary budget extension request with approval workflow.
//
// Concurrency policy: only one extension per budget can be in "approved" status at a time.
// Approving a new extension while another is active will be rejected by the handler.
// This prevents unintentional budget spikes from stacking multiple extensions.
//
// Consistency model: the in-memory cache is eventually consistent with the DB.
// On approval/delete, an immediate reload syncs memory. On expiry, the background
// worker (every 10s) expires DB rows and cleans the in-memory cache. Between expiry
// ticks, GetActiveBudgetExtensionAmount filters by wall-clock time as a safety net.
type TableBudgetExtension struct {
	ID       string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	BudgetID string  `gorm:"type:varchar(255);not null;index:idx_budget_ext_active,priority:1" json:"budget_id"`
	Amount   float64 `gorm:"not null" json:"amount"` // Additional budget in dollars

	// Request details
	Reason      string `gorm:"type:text" json:"reason,omitempty"`
	RequestedBy string `gorm:"type:varchar(255);not null" json:"requested_by"`

	// Approval details
	Status     BudgetExtensionStatus `gorm:"type:varchar(20);not null;default:'pending';index:idx_budget_ext_active,priority:2" json:"status"`
	ReviewedBy string                `gorm:"type:varchar(255)" json:"reviewed_by,omitempty"`
	ReviewNote string                `gorm:"type:text" json:"review_note,omitempty"`

	// Duration and expiry
	Duration  string     `gorm:"type:varchar(50);not null" json:"duration"`                                  // Requested duration e.g. "1h", "1d", "1w"
	StartsAt  *time.Time `gorm:"index:idx_budget_ext_active,priority:3" json:"starts_at,omitempty"`          // Set when approved
	ExpiresAt *time.Time `gorm:"index:idx_budget_ext_active,priority:4" json:"expires_at,omitempty"`         // Set when approved

	// Audit timestamps for explicit state transitions
	ApprovedAt *time.Time `json:"approved_at,omitempty"` // When the extension was approved
	RejectedAt *time.Time `json:"rejected_at,omitempty"` // When the extension was rejected
	ExpiredAt  *time.Time `json:"expired_at,omitempty"`  // When the extension was expired (by worker)

	// Relationships
	Budget *TableBudget `gorm:"foreignKey:BudgetID;constraint:OnDelete:CASCADE" json:"budget,omitempty"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

func (TableBudgetExtension) TableName() string { return "governance_budget_extensions" }

// ValidateExtensionAmount checks that the extension amount doesn't exceed the cap relative to the budget.
func ValidateExtensionAmount(amount float64, budgetMaxLimit float64) error {
	if amount <= 0 {
		return fmt.Errorf("extension amount must be positive, got %.2f", amount)
	}
	if budgetMaxLimit <= 0 {
		// No relative cap applies when base limit is zero; allow any positive amount.
		return nil
	}
	cap := budgetMaxLimit * MaxExtensionMultiplier
	if amount > cap {
		return fmt.Errorf("extension amount %.2f exceeds maximum allowed %.2f (%.0f%% of budget limit %.2f)",
			amount, cap, MaxExtensionMultiplier*100, budgetMaxLimit)
	}
	return nil
}

// ValidateExtensionDuration parses the duration string and ensures it doesn't exceed MaxExtensionDuration.
func ValidateExtensionDuration(durationStr string) (time.Duration, error) {
	d, err := ParseDuration(durationStr)
	if err != nil {
		return 0, fmt.Errorf("invalid duration format: %s (valid examples: 1h, 1d, 1w, 1M)", durationStr)
	}
	if d > MaxExtensionDuration {
		return 0, fmt.Errorf("extension duration %s exceeds maximum allowed %s",
			durationStr, MaxExtensionDuration)
	}
	if d <= 0 {
		return 0, fmt.Errorf("extension duration must be positive")
	}
	return d, nil
}
