package tables

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// TableProvider represents a provider configuration in the database
// NOTE: Any changes to the provider configuration should be reflected in the GenerateConfigHash function
// That helps us detect changes between config file and database config
type TableProvider struct {
	ID                       uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name                     string    `gorm:"type:varchar(50);uniqueIndex;not null" json:"name"` // ModelProvider as string
	NetworkConfigJSON        string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.NetworkConfig
	ConcurrencyBufferJSON    string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.ConcurrencyAndBufferSize
	ProxyConfigJSON          string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.ProxyConfig
	CustomProviderConfigJSON string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.CustomProviderConfig
	SendBackRawRequest       bool      `json:"send_back_raw_request"`
	SendBackRawResponse      bool      `json:"send_back_raw_response"`
	CreatedAt                time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt                time.Time `gorm:"index;not null" json:"updated_at"`

	// Relationships
	Keys []TableKey `gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE" json:"keys"`

	// Virtual fields for runtime use (not stored in DB)
	NetworkConfig            *schemas.NetworkConfig            `gorm:"-" json:"network_config,omitempty"`
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `gorm:"-" json:"concurrency_and_buffer_size,omitempty"`
	ProxyConfig              *schemas.ProxyConfig              `gorm:"-" json:"proxy_config,omitempty"`

	// Custom provider fields
	CustomProviderConfig *schemas.CustomProviderConfig `gorm:"-" json:"custom_provider_config,omitempty"`

	// Foreign keys
	Models []TableModel `gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE" json:"models"`

	// Governance fields - Budget and Rate Limit for provider-level governance
	BudgetID    *string `gorm:"type:varchar(255);index:idx_provider_budget" json:"budget_id,omitempty"`
	RateLimitID *string `gorm:"type:varchar(255);index:idx_provider_rate_limit" json:"rate_limit_id,omitempty"`

	// Governance relationships
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID;onDelete:CASCADE" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`
}

// TableName represents a provider configuration in the database
func (TableProvider) TableName() string { return "config_providers" }

// BeforeSave hooks for serialization
func (p *TableProvider) BeforeSave(tx *gorm.DB) error {
	if p.NetworkConfig != nil {
		data, err := json.Marshal(p.NetworkConfig)
		if err != nil {
			return err
		}
		p.NetworkConfigJSON = string(data)
	}
	if p.ConcurrencyAndBufferSize != nil {
		data, err := json.Marshal(p.ConcurrencyAndBufferSize)
		if err != nil {
			return err
		}
		p.ConcurrencyBufferJSON = string(data)
	}
	if p.ProxyConfig != nil {
		data, err := json.Marshal(p.ProxyConfig)
		if err != nil {
			return err
		}
		p.ProxyConfigJSON = string(data)
	}
	if p.CustomProviderConfig != nil && p.CustomProviderConfig.BaseProviderType == "" {
		return fmt.Errorf("base_provider_type is required when custom_provider_config is set")
	}
	if p.CustomProviderConfig != nil {
		data, err := json.Marshal(p.CustomProviderConfig)
		if err != nil {
			return err
		}
		p.CustomProviderConfigJSON = string(data)
	}

	// Validate governance fields
	if p.BudgetID != nil && strings.TrimSpace(*p.BudgetID) == "" {
		return fmt.Errorf("budget_id cannot be an empty string")
	}
	if p.RateLimitID != nil && strings.TrimSpace(*p.RateLimitID) == "" {
		return fmt.Errorf("rate_limit_id cannot be an empty string")
	}

	return nil
}

// AfterFind hooks for deserialization
func (p *TableProvider) AfterFind(tx *gorm.DB) error {
	if p.NetworkConfigJSON != "" {
		var config schemas.NetworkConfig
		if err := json.Unmarshal([]byte(p.NetworkConfigJSON), &config); err != nil {
			return err
		}
		p.NetworkConfig = &config
	}

	if p.ConcurrencyBufferJSON != "" {
		var config schemas.ConcurrencyAndBufferSize
		if err := json.Unmarshal([]byte(p.ConcurrencyBufferJSON), &config); err != nil {
			return err
		}
		p.ConcurrencyAndBufferSize = &config
	}

	if p.ProxyConfigJSON != "" {
		var proxyConfig schemas.ProxyConfig
		if err := json.Unmarshal([]byte(p.ProxyConfigJSON), &proxyConfig); err != nil {
			return err
		}
		p.ProxyConfig = &proxyConfig
	}

	if p.CustomProviderConfigJSON != "" {
		var customConfig schemas.CustomProviderConfig
		if err := json.Unmarshal([]byte(p.CustomProviderConfigJSON), &customConfig); err != nil {
			return err
		}
		p.CustomProviderConfig = &customConfig
	}

	return nil
}
