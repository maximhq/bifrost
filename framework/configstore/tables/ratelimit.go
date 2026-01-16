package tables

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// TableRateLimit defines rate limiting rules for virtual keys using flexible max+reset approach
type TableRateLimit struct {
	ID string `gorm:"primaryKey;type:varchar(255)" json:"id"`

	// Token limits with flexible duration
	TokenMaxLimit      *int64    `gorm:"default:null" json:"token_max_limit,omitempty"`          // Maximum tokens allowed
	TokenResetDuration *string   `gorm:"type:varchar(50)" json:"token_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	TokenCurrentUsage  int64     `gorm:"default:0" json:"token_current_usage"`                   // Current token usage
	TokenLastReset     time.Time `gorm:"index" json:"token_last_reset"`                          // Last time token counter was reset

	// Request limits with flexible duration
	RequestMaxLimit      *int64    `gorm:"default:null" json:"request_max_limit,omitempty"`          // Maximum requests allowed
	RequestResetDuration *string   `gorm:"type:varchar(50)" json:"request_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	RequestCurrentUsage  int64     `gorm:"default:0" json:"request_current_usage"`                   // Current request usage
	RequestLastReset     time.Time `gorm:"index" json:"request_last_reset"`                          // Last time request counter was reset

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	LastDBTokenUsage   int64 `gorm:"-" json:"-"`
	LastDBRequestUsage int64 `gorm:"-" json:"-"`
}

// TableName sets the table name for each model
func (TableRateLimit) TableName() string { return "governance_rate_limits" }

// BeforeSave hook for RateLimit to validate reset duration formats
func (rl *TableRateLimit) BeforeSave(tx *gorm.DB) error {
	// Validate token reset duration if provided
	if rl.TokenResetDuration != nil {
		if d, err := ParseDuration(*rl.TokenResetDuration); err != nil {
			return fmt.Errorf("invalid token reset duration format: %s", *rl.TokenResetDuration)
		} else if d <= 0 {
			return fmt.Errorf("token reset duration cannot be zero or negative: %s", *rl.TokenResetDuration)
		}
	}

	// Validate request reset duration if provided
	if rl.RequestResetDuration != nil {
		if d, err := ParseDuration(*rl.RequestResetDuration); err != nil {
			return fmt.Errorf("invalid request reset duration format: %s", *rl.RequestResetDuration)
		} else if d <= 0 {
			return fmt.Errorf("request reset duration cannot be zero or negative: %s", *rl.RequestResetDuration)
		}
	}

	// Validate that if a max limit is set, a reset duration is also provided
	if rl.TokenMaxLimit != nil && rl.TokenResetDuration == nil {
		return fmt.Errorf("token_reset_duration is required when token_max_limit is set")
	}

	if rl.RequestMaxLimit != nil && rl.RequestResetDuration == nil {
		return fmt.Errorf("request_reset_duration is required when request_max_limit is set")
	}

	// Making sure token limit is greater than zero
	if rl.TokenMaxLimit != nil && *rl.TokenMaxLimit <= 0 {
		return fmt.Errorf("token_max_limit cannot be zero or negative: %d", *rl.TokenMaxLimit)
	}

	// Making sure request limit is greater than zero
	if rl.RequestMaxLimit != nil && *rl.RequestMaxLimit <= 0 {
		return fmt.Errorf("request_max_limit cannot be zero or negative: %d", *rl.RequestMaxLimit)
	}

	return nil
}

// AfterFind hook for RateLimit to set the LastDBTokenUsage and LastDBRequestUsage virtual fields
func (rl *TableRateLimit) AfterFind(tx *gorm.DB) error {
	rl.LastDBTokenUsage = rl.TokenCurrentUsage
	rl.LastDBRequestUsage = rl.RequestCurrentUsage
	return nil
}
