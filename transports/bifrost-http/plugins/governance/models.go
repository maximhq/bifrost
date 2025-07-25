// Package governance provides governance and rate limiting functionality for Bifrost
package governance

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Budget defines spending limits with configurable reset periods
type Budget struct {
	ID            string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	MaxLimit      float64   `gorm:"not null" json:"max_limit"`                       // Maximum budget in dollars
	ResetDuration string    `gorm:"type:varchar(50);not null" json:"reset_duration"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	LastReset     time.Time `gorm:"index" json:"last_reset"`                         // Last time budget was reset
	CurrentUsage  float64   `gorm:"default:0" json:"current_usage"`                  // Current usage in dollars

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// RateLimit defines rate limiting rules for virtual keys using flexible max+reset approach
type RateLimit struct {
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

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// Customer represents a customer entity with budget
type Customer struct {
	ID       string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name     string  `gorm:"type:varchar(255);not null" json:"name"`
	BudgetID *string `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`

	// Relationships
	Budget      *Budget      `gorm:"foreignKey:BudgetID" json:"budget,omitempty"`
	Teams       []Team       `gorm:"foreignKey:CustomerID" json:"teams"`
	VirtualKeys []VirtualKey `gorm:"foreignKey:CustomerID" json:"virtual_keys"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// Team represents a team entity with budget and customer association
type Team struct {
	ID         string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name       string  `gorm:"type:varchar(255);not null" json:"name"`
	CustomerID *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"` // A team can belong to a customer
	BudgetID   *string `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`

	// Relationships
	Customer    *Customer    `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Budget      *Budget      `gorm:"foreignKey:BudgetID" json:"budget,omitempty"`
	VirtualKeys []VirtualKey `gorm:"foreignKey:TeamID" json:"virtual_keys"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// VirtualKey represents a virtual key with budget, rate limits, and team/customer association
type VirtualKey struct {
	ID               string   `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name             string   `gorm:"unique;type:varchar(255);not null" json:"name"`
	Description      string   `gorm:"type:text" json:"description,omitempty"`
	Value            string   `gorm:"unique;type:varchar(255);not null" json:"value"` // The virtual key value
	IsActive         bool     `gorm:"default:true" json:"is_active"`
	AllowedModels    []string `gorm:"type:text;serializer:json" json:"allowed_models"`    // Empty means all models allowed
	AllowedProviders []string `gorm:"type:text;serializer:json" json:"allowed_providers"` // Empty means all providers allowed

	// Foreign key relationships (mutually exclusive: either TeamID or CustomerID, not both)
	TeamID      *string `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	CustomerID  *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`
	BudgetID    *string `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`
	RateLimitID *string `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	// Relationships
	Team      *Team      `gorm:"foreignKey:TeamID" json:"team,omitempty"`
	Customer  *Customer  `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Budget    *Budget    `gorm:"foreignKey:BudgetID" json:"budget,omitempty"`
	RateLimit *RateLimit `gorm:"foreignKey:RateLimitID" json:"rate_limit,omitempty"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// Config represents generic configuration key-value pairs
type Config struct {
	Key   string `gorm:"primaryKey;type:varchar(255)" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// ModelPricing represents pricing information for AI models
type ModelPricing struct {
	ID                 uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	Model              string  `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_provider_mode" json:"model"`
	Provider           string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"provider"`
	InputCostPerToken  float64 `gorm:"not null" json:"input_cost_per_token"`
	OutputCostPerToken float64 `gorm:"not null" json:"output_cost_per_token"`
	Mode               string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"mode"`

	// Additional pricing for media
	InputCostPerImage          *float64 `gorm:"default:null" json:"input_cost_per_image,omitempty"`
	InputCostPerVideoPerSecond *float64 `gorm:"default:null" json:"input_cost_per_video_per_second,omitempty"`
	InputCostPerAudioPerSecond *float64 `gorm:"default:null" json:"input_cost_per_audio_per_second,omitempty"`

	// Character-based pricing
	InputCostPerCharacter  *float64 `gorm:"default:null" json:"input_cost_per_character,omitempty"`
	OutputCostPerCharacter *float64 `gorm:"default:null" json:"output_cost_per_character,omitempty"`

	// Pricing above 128k tokens
	InputCostPerTokenAbove128kTokens          *float64 `gorm:"default:null" json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerCharacterAbove128kTokens      *float64 `gorm:"default:null" json:"input_cost_per_character_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `gorm:"default:null" json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `gorm:"default:null" json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `gorm:"default:null" json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `gorm:"default:null" json:"output_cost_per_token_above_128k_tokens,omitempty"`
	OutputCostPerCharacterAbove128kTokens     *float64 `gorm:"default:null" json:"output_cost_per_character_above_128k_tokens,omitempty"`

	// Cache and batch pricing
	CacheReadInputTokenCost   *float64 `gorm:"default:null" json:"cache_read_input_token_cost,omitempty"`
	InputCostPerTokenBatches  *float64 `gorm:"default:null" json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches *float64 `gorm:"default:null" json:"output_cost_per_token_batches,omitempty"`
}

// Table names
func (Budget) TableName() string       { return "governance_budgets" }
func (RateLimit) TableName() string    { return "governance_rate_limits" }
func (Customer) TableName() string     { return "governance_customers" }
func (Team) TableName() string         { return "governance_teams" }
func (VirtualKey) TableName() string   { return "governance_virtual_keys" }
func (Config) TableName() string       { return "governance_config" }
func (ModelPricing) TableName() string { return "governance_model_pricing" }

// GORM Hooks for validation and constraints

// BeforeSave hook for VirtualKey to enforce mutual exclusion
func (vk *VirtualKey) BeforeSave(tx *gorm.DB) error {
	// Enforce mutual exclusion: VK can belong to either Team OR Customer, not both
	if vk.TeamID != nil && vk.CustomerID != nil {
		return fmt.Errorf("virtual key cannot belong to both team and customer")
	}
	return nil
}

// BeforeSave hook for Budget to validate reset duration format and max limit
func (b *Budget) BeforeSave(tx *gorm.DB) error {
	// Validate that ResetDuration is in correct format (e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y")
	if _, err := ParseDuration(b.ResetDuration); err != nil {
		return fmt.Errorf("invalid reset duration format: %s", b.ResetDuration)
	}

	// Validate that MaxLimit is not negative (budgets should be positive)
	if b.MaxLimit < 0 {
		return fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)
	}

	return nil
}

// BeforeSave hook for RateLimit to validate reset duration formats
func (rl *RateLimit) BeforeSave(tx *gorm.DB) error {
	// Validate token reset duration if provided
	if rl.TokenResetDuration != nil {
		if _, err := ParseDuration(*rl.TokenResetDuration); err != nil {
			return fmt.Errorf("invalid token reset duration format: %s", *rl.TokenResetDuration)
		}
	}

	// Validate request reset duration if provided
	if rl.RequestResetDuration != nil {
		if _, err := ParseDuration(*rl.RequestResetDuration); err != nil {
			return fmt.Errorf("invalid request reset duration format: %s", *rl.RequestResetDuration)
		}
	}

	// Validate that if a max limit is set, a reset duration is also provided
	if rl.TokenMaxLimit != nil && rl.TokenResetDuration == nil {
		return fmt.Errorf("token_reset_duration is required when token_max_limit is set")
	}
	if rl.RequestMaxLimit != nil && rl.RequestResetDuration == nil {
		return fmt.Errorf("request_reset_duration is required when request_max_limit is set")
	}

	return nil
}

// Database constraints and indexes
func (vk *VirtualKey) AfterAutoMigrate(tx *gorm.DB) error {
	// Ensure only one of TeamID or CustomerID is set
	return tx.Exec(`
		CREATE OR REPLACE FUNCTION check_vk_exclusion() RETURNS TRIGGER AS $$
		BEGIN
			IF NEW.team_id IS NOT NULL AND NEW.customer_id IS NOT NULL THEN
				RAISE EXCEPTION 'Virtual key cannot belong to both team and customer';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		
		DROP TRIGGER IF EXISTS vk_exclusion_trigger ON governance_virtual_keys;
		CREATE TRIGGER vk_exclusion_trigger 
			BEFORE INSERT OR UPDATE ON governance_virtual_keys
			FOR EACH ROW EXECUTE FUNCTION check_vk_exclusion();
	`).Error
}

// Utility function to parse duration strings
func ParseDuration(duration string) (time.Duration, error) {
	if duration == "" {
		return 0, fmt.Errorf("duration is empty")
	}

	// Handle special cases for days, weeks, months, years
	switch {
	case duration[len(duration)-1:] == "d":
		days := duration[:len(duration)-1]
		if d, err := time.ParseDuration(days + "h"); err == nil {
			return d * 24, nil
		}
		return 0, fmt.Errorf("invalid day duration: %s", duration)
	case duration[len(duration)-1:] == "w":
		weeks := duration[:len(duration)-1]
		if w, err := time.ParseDuration(weeks + "h"); err == nil {
			return w * 24 * 7, nil
		}
		return 0, fmt.Errorf("invalid week duration: %s", duration)
	case duration[len(duration)-1:] == "M":
		months := duration[:len(duration)-1]
		if m, err := time.ParseDuration(months + "h"); err == nil {
			return m * 24 * 30, nil // Approximate month as 30 days
		}
		return 0, fmt.Errorf("invalid month duration: %s", duration)
	case duration[len(duration)-1:] == "Y":
		years := duration[:len(duration)-1]
		if y, err := time.ParseDuration(years + "h"); err == nil {
			return y * 24 * 365, nil // Approximate year as 365 days
		}
		return 0, fmt.Errorf("invalid year duration: %s", duration)
	default:
		return time.ParseDuration(duration)
	}
}
