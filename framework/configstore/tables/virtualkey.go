package tables

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// TableVirtualKeyProviderConfig represents a provider configuration for a virtual key
type TableVirtualKeyProviderConfig struct {
	ID            uint     `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualKeyID  string   `gorm:"type:varchar(255);not null" json:"virtual_key_id"`
	Provider      string   `gorm:"type:varchar(50);not null" json:"provider"`
	Weight        float64  `gorm:"default:1.0" json:"weight"`
	AllowedModels []string `gorm:"type:text;serializer:json" json:"allowed_models"` // Empty means all models allowed
	BudgetID      *string  `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`
	RateLimitID   *string  `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	// Relationships
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID;onDelete:CASCADE" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`
}

// TableName sets the table name for each model
func (TableVirtualKeyProviderConfig) TableName() string {
	return "governance_virtual_key_provider_configs"
}

type TableVirtualKeyMCPConfig struct {
	ID             uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualKeyID   string         `gorm:"type:varchar(255);not null;uniqueIndex:idx_vk_mcpclient" json:"virtual_key_id"`
	MCPClientID    uint           `gorm:"not null;uniqueIndex:idx_vk_mcpclient" json:"mcp_client_id"`
	MCPClient      TableMCPClient `gorm:"foreignKey:MCPClientID" json:"mcp_client"`
	ToolsToExecute []string       `gorm:"type:text;serializer:json" json:"tools_to_execute"`
}

// TableName sets the table name for each model
func (TableVirtualKeyMCPConfig) TableName() string {
	return "governance_virtual_key_mcp_configs"
}

// TableVirtualKey represents a virtual key with budget, rate limits, and team/customer association
type TableVirtualKey struct {
	ID              string                          `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name            string                          `gorm:"uniqueIndex:idx_virtual_key_name;type:varchar(255);not null" json:"name"`
	Description     string                          `gorm:"type:text" json:"description,omitempty"`
	Value           string                          `gorm:"uniqueIndex:idx_virtual_key_value;type:varchar(255);not null" json:"value"` // The virtual key value
	IsActive        bool                            `gorm:"default:true" json:"is_active"`
	ProviderConfigs []TableVirtualKeyProviderConfig `gorm:"foreignKey:VirtualKeyID;constraint:OnDelete:CASCADE" json:"provider_configs"` // Empty means all providers allowed
	MCPConfigs      []TableVirtualKeyMCPConfig      `gorm:"foreignKey:VirtualKeyID;constraint:OnDelete:CASCADE" json:"mcp_configs"`

	// Foreign key relationships (mutually exclusive: either TeamID or CustomerID, not both)
	TeamID      *string    `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	CustomerID  *string    `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`
	BudgetID    *string    `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`
	RateLimitID *string    `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`
	Keys        []TableKey `gorm:"many2many:governance_virtual_key_keys;constraint:OnDelete:CASCADE" json:"keys"`

	// Relationships
	Team      *TableTeam      `gorm:"foreignKey:TeamID" json:"team,omitempty"`
	Customer  *TableCustomer  `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID;onDelete:CASCADE" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableVirtualKey) TableName() string { return "governance_virtual_keys" }

// BeforeSave hook for VirtualKey to enforce mutual exclusion
func (vk *TableVirtualKey) BeforeSave(tx *gorm.DB) error {
	// Enforce mutual exclusion: VK can belong to either Team OR Customer, not both
	if vk.TeamID != nil && vk.CustomerID != nil {
		return fmt.Errorf("virtual key cannot belong to both team and customer")
	}
	return nil
}

// AfterFind hook for VirtualKey to clear sensitive data from associated keys
func (vk *TableVirtualKey) AfterFind(tx *gorm.DB) error {
	if vk.Keys != nil {
		// Clear sensitive data from associated keys, keeping only key IDs and non-sensitive metadata
		for i := range vk.Keys {
			key := &vk.Keys[i]

			// Clear the actual API key value
			key.Value = ""

			// Clear all Azure-related sensitive fields
			key.AzureEndpoint = nil
			key.AzureAPIVersion = nil
			key.AzureDeploymentsJSON = nil
			key.AzureKeyConfig = nil

			// Clear all Vertex-related sensitive fields
			key.VertexProjectID = nil
			key.VertexProjectNumber = nil
			key.VertexRegion = nil
			key.VertexAuthCredentials = nil
			key.VertexKeyConfig = nil

			// Clear all Bedrock-related sensitive fields
			key.BedrockAccessKey = nil
			key.BedrockSecretKey = nil
			key.BedrockSessionToken = nil
			key.BedrockRegion = nil
			key.BedrockARN = nil
			key.BedrockDeploymentsJSON = nil
			key.BedrockKeyConfig = nil

			vk.Keys[i] = *key
		}
	}
	return nil
}
