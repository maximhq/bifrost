package tables

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// TableVirtualKeyProviderConfigKey is the join table for the many2many relationship
// between TableVirtualKeyProviderConfig and TableKey
type TableVirtualKeyProviderConfigKey struct {
	TableVirtualKeyProviderConfigID uint `gorm:"primaryKey;uniqueIndex:idx_vk_provider_config_key"`
	TableKeyID                      uint `gorm:"primaryKey;uniqueIndex:idx_vk_provider_config_key"`
}

// TableName sets the table name for the join table
func (TableVirtualKeyProviderConfigKey) TableName() string {
	return "governance_virtual_key_provider_config_keys"
}

// TableVirtualKeyProviderConfig represents a provider configuration for a virtual key
type TableVirtualKeyProviderConfig struct {
	ID            uint     `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualKeyID  string   `gorm:"type:varchar(255);not null" json:"virtual_key_id"`
	Provider      string   `gorm:"type:varchar(50);not null" json:"provider"`
	Weight        *float64 `json:"weight"`
	AllowedModels []string `gorm:"type:text;serializer:json" json:"allowed_models"` // Empty means all models allowed
	BudgetID      *string  `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`
	RateLimitID   *string  `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	// Relationships
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID;onDelete:CASCADE" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`
	Keys      []TableKey      `gorm:"many2many:governance_virtual_key_provider_config_keys;constraint:OnDelete:CASCADE" json:"keys"` // Empty means all keys allowed for this provider
}

// TableName sets the table name for each model
func (TableVirtualKeyProviderConfig) TableName() string {
	return "governance_virtual_key_provider_configs"
}

// UnmarshalJSON custom unmarshaller to handle both "keys" ([]TableKey) and "allowed_keys" ([]string) formats
func (pc *TableVirtualKeyProviderConfig) UnmarshalJSON(data []byte) error {
	// Temporary struct to capture all fields including allowed_keys
	type Alias TableVirtualKeyProviderConfig
	type TempProviderConfig struct {
		Alias
		AllowedKeys []string `json:"allowed_keys"` // Config file format: array of key names
	}

	var temp TempProviderConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	// Copy all standard fields
	*pc = TableVirtualKeyProviderConfig(temp.Alias)

	// If allowed_keys is provided (config file format), convert to Keys
	// This takes precedence if Keys is empty but allowed_keys has values
	if len(temp.AllowedKeys) > 0 && len(pc.Keys) == 0 {
		pc.Keys = make([]TableKey, len(temp.AllowedKeys))
		for i, keyName := range temp.AllowedKeys {
			pc.Keys[i] = TableKey{Name: keyName}
		}
	}

	return nil
}

// MarshalJSON custom marshaller to ensure AllowedModels is always an array (never null)
func (pc TableVirtualKeyProviderConfig) MarshalJSON() ([]byte, error) {
	type Alias TableVirtualKeyProviderConfig

	// Ensure AllowedModels is an empty slice instead of nil
	allowedModels := pc.AllowedModels
	if allowedModels == nil {
		allowedModels = []string{}
	}

	return json.Marshal(&struct {
		Alias
		AllowedModels []string `json:"allowed_models"`
	}{
		Alias:         Alias(pc),
		AllowedModels: allowedModels,
	})
}

// AfterFind hook for TableVirtualKeyProviderConfig to clear sensitive data from associated keys
func (pc *TableVirtualKeyProviderConfig) AfterFind(tx *gorm.DB) error {
	if pc.Keys != nil {
		// Clear sensitive data from associated keys, keeping only key IDs and non-sensitive metadata
		for i := range pc.Keys {
			key := &pc.Keys[i]

			// Clear the actual API key value
			key.Value = *schemas.NewEnvVar("")

			// Clear all Azure-related sensitive fields
			key.AzureEndpoint = nil
			key.AzureAPIVersion = nil
			key.AzureClientID = nil
			key.AzureClientSecret = nil
			key.AzureTenantID = nil
			key.AzureScopesJSON = nil
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

			pc.Keys[i] = *key
		}
	}
	return nil
}

type TableVirtualKeyMCPConfig struct {
	ID             uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualKeyID   string         `gorm:"type:varchar(255);not null;uniqueIndex:idx_vk_mcpclient" json:"virtual_key_id"`
	MCPClientID    uint           `gorm:"not null;uniqueIndex:idx_vk_mcpclient" json:"mcp_client_id"`
	MCPClient      TableMCPClient `gorm:"foreignKey:MCPClientID" json:"mcp_client"`
	ToolsToExecute []string       `gorm:"type:text;serializer:json" json:"tools_to_execute"`

	// MCPClientName is used during config file parsing to resolve the MCP client by name.
	// This field is not persisted to the database - it's only used to capture
	// "mcp_client_name" from config.json and then resolve it to MCPClientID.
	MCPClientName string `gorm:"-" json:"-"`
}

// TableName sets the table name for each model
func (TableVirtualKeyMCPConfig) TableName() string {
	return "governance_virtual_key_mcp_configs"
}

// UnmarshalJSON custom unmarshaller to handle both "mcp_client_id" (database format)
// and "mcp_client_name" (config file format) for MCP client references.
func (mc *TableVirtualKeyMCPConfig) UnmarshalJSON(data []byte) error {
	// Temporary struct to capture all fields including mcp_client_name
	type Alias TableVirtualKeyMCPConfig
	type TempMCPConfig struct {
		Alias
		MCPClientName string `json:"mcp_client_name"` // Config file format: MCP client name
	}

	var temp TempMCPConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	// Copy all standard fields
	*mc = TableVirtualKeyMCPConfig(temp.Alias)

	// Capture mcp_client_name for later resolution to MCPClientID
	if temp.MCPClientName != "" {
		mc.MCPClientName = temp.MCPClientName
	}

	return nil
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
	TeamID      *string `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	CustomerID  *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`
	BudgetID    *string `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`
	RateLimitID *string `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	// Relationships
	Team      *TableTeam      `gorm:"foreignKey:TeamID" json:"team,omitempty"`
	Customer  *TableCustomer  `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID;onDelete:CASCADE" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

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
