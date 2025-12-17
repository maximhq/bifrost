package tables

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TableModelConfig represents a model configuration with rate limiting and budgeting
type TableModelConfig struct {
	ID          string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	ModelName   string  `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_provider" json:"model_name"`
	Provider    *string `gorm:"type:varchar(50);uniqueIndex:idx_model_provider" json:"provider,omitempty"` // Optional provider, nullable
	BudgetID    *string `gorm:"type:varchar(255);index:idx_model_config_budget" json:"budget_id,omitempty"`
	RateLimitID *string `gorm:"type:varchar(255);index:idx_model_config_rate_limit" json:"rate_limit_id,omitempty"`

	// Relationships
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID;onDelete:CASCADE" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableModelConfig) TableName() string {
	return "governance_model_configs"
}

// BeforeSave hook for ModelConfig to validate required fields
func (mc *TableModelConfig) BeforeSave(tx *gorm.DB) error {
	// Validate that ModelName is not empty
	if strings.TrimSpace(mc.ModelName) == "" {
		return fmt.Errorf("model_name cannot be empty")
	}

	// Validate that if BudgetID is provided, it's not an empty string
	if mc.BudgetID != nil && strings.TrimSpace(*mc.BudgetID) == "" {
		return fmt.Errorf("budget_id cannot be an empty string")
	}

	// Validate that if RateLimitID is provided, it's not an empty string
	if mc.RateLimitID != nil && strings.TrimSpace(*mc.RateLimitID) == "" {
		return fmt.Errorf("rate_limit_id cannot be an empty string")
	}

	// Validate that if Provider is provided, it's not an empty string
	if mc.Provider != nil && strings.TrimSpace(*mc.Provider) == "" {
		return fmt.Errorf("provider cannot be an empty string")
	}

	return nil
}
