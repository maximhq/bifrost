package configstore

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// TRANSPORT OPERATION TABLES

type TableConfigHash struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Hash      string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"hash"`
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableProvider represents a provider configuration in the database
type TableProvider struct {
	ID                    uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name                  string    `gorm:"type:varchar(50);uniqueIndex;not null" json:"name"` // ModelProvider as string
	NetworkConfigJSON     string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.NetworkConfig
	ConcurrencyBufferJSON string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.ConcurrencyAndBufferSize
	ProxyConfigJSON       string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.ProxyConfig
	SendBackRawResponse   bool      `json:"send_back_raw_response"`
	CreatedAt             time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt             time.Time `gorm:"index;not null" json:"updated_at"`

	// Relationships
	Keys []TableKey `gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE" json:"keys"`

	// Virtual fields for runtime use (not stored in DB)
	NetworkConfig            *schemas.NetworkConfig            `gorm:"-" json:"network_config,omitempty"`
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `gorm:"-" json:"concurrency_and_buffer_size,omitempty"`
	ProxyConfig              *schemas.ProxyConfig              `gorm:"-" json:"proxy_config,omitempty"`
	// Foreign keys
	Models []TableModel `gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE" json:"models"`
}

// TableModel represents a model configuration in the database
type TableModel struct {
	ID         string    `gorm:"primaryKey" json:"id"`
	ProviderID uint      `gorm:"index;not null;uniqueIndex:idx_provider_name" json:"provider_id"`
	Name       string    `gorm:"uniqueIndex:idx_provider_name" json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableKey represents an API key configuration in the database
type TableKey struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ProviderID uint      `gorm:"index;not null" json:"provider_id"`
	Provider   string    `gorm:"index;type:varchar(50)" json:"provider"`                          // ModelProvider as string
	KeyID      string    `gorm:"type:varchar(255);uniqueIndex:idx_key_id;not null" json:"key_id"` // UUID from schemas.Key
	Value      string    `gorm:"type:text;not null" json:"value"`
	ModelsJSON string    `gorm:"type:text" json:"-"` // JSON serialized []string
	Weight     float64   `gorm:"default:1.0" json:"weight"`
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"index;not null" json:"updated_at"`

	// Azure config fields (embedded instead of separate table for simplicity)
	AzureEndpoint        *string `gorm:"type:text" json:"azure_endpoint,omitempty"`
	AzureAPIVersion      *string `gorm:"type:varchar(50)" json:"azure_api_version,omitempty"`
	AzureDeploymentsJSON *string `gorm:"type:text" json:"-"` // JSON serialized map[string]string

	// Vertex config fields (embedded)
	VertexProjectID       *string `gorm:"type:varchar(255)" json:"vertex_project_id,omitempty"`
	VertexRegion          *string `gorm:"type:varchar(100)" json:"vertex_region,omitempty"`
	VertexAuthCredentials *string `gorm:"type:text" json:"vertex_auth_credentials,omitempty"`

	// Bedrock config fields (embedded)
	BedrockAccessKey       *string `gorm:"type:varchar(255)" json:"bedrock_access_key,omitempty"`
	BedrockSecretKey       *string `gorm:"type:text" json:"bedrock_secret_key,omitempty"`
	BedrockSessionToken    *string `gorm:"type:text" json:"bedrock_session_token,omitempty"`
	BedrockRegion          *string `gorm:"type:varchar(100)" json:"bedrock_region,omitempty"`
	BedrockARN             *string `gorm:"type:text" json:"bedrock_arn,omitempty"`
	BedrockDeploymentsJSON *string `gorm:"type:text" json:"-"` // JSON serialized map[string]string

	// Virtual fields for runtime use (not stored in DB)
	Models           []string                  `gorm:"-" json:"models"`
	AzureKeyConfig   *schemas.AzureKeyConfig   `gorm:"-" json:"azure_key_config,omitempty"`
	VertexKeyConfig  *schemas.VertexKeyConfig  `gorm:"-" json:"vertex_key_config,omitempty"`
	BedrockKeyConfig *schemas.BedrockKeyConfig `gorm:"-" json:"bedrock_key_config,omitempty"`
}

// TableMCPClient represents an MCP client configuration in the database
type TableMCPClient struct {
	ID                 uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name               string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	ConnectionType     string    `gorm:"type:varchar(20);not null" json:"connection_type"` // schemas.MCPConnectionType
	ConnectionString   *string   `gorm:"type:text" json:"connection_string,omitempty"`
	StdioConfigJSON    *string   `gorm:"type:text" json:"-"` // JSON serialized schemas.MCPStdioConfig
	ToolsToExecuteJSON string    `gorm:"type:text" json:"-"` // JSON serialized []string
	ToolsToSkipJSON    string    `gorm:"type:text" json:"-"` // JSON serialized []string
	CreatedAt          time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt          time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	StdioConfig    *schemas.MCPStdioConfig `gorm:"-" json:"stdio_config,omitempty"`
	ToolsToExecute []string                `gorm:"-" json:"tools_to_execute"`
	ToolsToSkip    []string                `gorm:"-" json:"tools_to_skip"`
}

// TableClientConfig represents global client configuration in the database
type TableClientConfig struct {
	ID                      uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	DropExcessRequests      bool      `gorm:"default:false" json:"drop_excess_requests"`
	PrometheusLabelsJSON    string    `gorm:"type:text" json:"-"` // JSON serialized []string
	AllowedOriginsJSON      string    `gorm:"type:text" json:"-"` // JSON serialized []string
	InitialPoolSize         int       `gorm:"default:300" json:"initial_pool_size"`
	EnableLogging           bool      `gorm:"" json:"enable_logging"`
	EnableGovernance        bool      `gorm:"" json:"enable_governance"`
	EnforceGovernanceHeader bool      `gorm:"" json:"enforce_governance_header"`
	AllowDirectKeys         bool      `gorm:"" json:"allow_direct_keys"`
	CreatedAt               time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt               time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	PrometheusLabels []string `gorm:"-" json:"prometheus_labels"`
	AllowedOrigins   []string `gorm:"-" json:"allowed_origins,omitempty"`
}

// TableEnvKey represents environment variable tracking in the database
type TableEnvKey struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	EnvVar     string    `gorm:"type:varchar(255);index;not null" json:"env_var"`
	Provider   string    `gorm:"type:varchar(50);index" json:"provider"`        // Empty for MCP/client configs
	KeyType    string    `gorm:"type:varchar(50);not null" json:"key_type"`     // "api_key", "azure_config", "vertex_config", "bedrock_config", "connection_string"
	ConfigPath string    `gorm:"type:varchar(500);not null" json:"config_path"` // Descriptive path of where this env var is used
	KeyID      string    `gorm:"type:varchar(255);index" json:"key_id"`         // Key UUID (empty for non-key configs)
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
}

// TableVectorStoreConfig represents Cache plugin configuration in the database
type TableVectorStoreConfig struct {
	ID              uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Enabled         bool      `json:"enabled"`                               // Enable vector store
	Type            string    `gorm:"type:varchar(50);not null" json:"type"` // "redis"
	TTLSeconds      int       `gorm:"default:300" json:"ttl_seconds"`        // TTL in seconds (default: 5 minutes)
	CacheByModel    bool      `gorm:"" json:"cache_by_model"`                // Include model in cache key
	CacheByProvider bool      `gorm:"" json:"cache_by_provider"`             // Include provider in cache key
	Config          *string   `gorm:"type:text" json:"config"`               // JSON serialized schemas.RedisVectorStoreConfig
	CreatedAt       time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableLogStoreConfig represents the configuration for the log store in the database
type TableLogStoreConfig struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Enabled   bool      `json:"enabled"`
	Type      string    `gorm:"type:varchar(50);not null" json:"type"` // "sqlite"
	Config    *string   `gorm:"type:text" json:"config"`               // JSON serialized logstore.Config
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TablePlugin represents a plugin configuration in the database

type TablePlugin struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name       string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	Enabled    bool      `json:"enabled"`
	ConfigJSON string    `gorm:"type:text" json:"-"` // JSON serialized plugin.Config
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	Config any `gorm:"-" json:"config,omitempty"`
}

// TableName sets the table name for each model
func (TableConfigHash) TableName() string        { return "config_hashes" }
func (TableProvider) TableName() string          { return "config_providers" }
func (TableKey) TableName() string               { return "config_keys" }
func (TableModel) TableName() string             { return "config_models" }
func (TableMCPClient) TableName() string         { return "config_mcp_clients" }
func (TableClientConfig) TableName() string      { return "config_client" }
func (TableEnvKey) TableName() string            { return "config_env_keys" }
func (TableVectorStoreConfig) TableName() string { return "config_vector_store" }
func (TableLogStoreConfig) TableName() string    { return "config_log_store" }
func (TablePlugin) TableName() string            { return "config_plugins" }

// GORM Hooks for JSON serialization/deserialization

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

	return nil
}

func (k *TableKey) BeforeSave(tx *gorm.DB) error {

	if k.Models != nil {
		data, err := json.Marshal(k.Models)
		if err != nil {
			return err
		}
		k.ModelsJSON = string(data)
	} else {
		k.ModelsJSON = "[]"
	}

	if k.AzureKeyConfig != nil {
		if k.AzureKeyConfig.Endpoint != "" {
			k.AzureEndpoint = &k.AzureKeyConfig.Endpoint
		}
		k.AzureAPIVersion = k.AzureKeyConfig.APIVersion
		if k.AzureKeyConfig.Deployments != nil {
			data, err := json.Marshal(k.AzureKeyConfig.Deployments)
			if err != nil {
				return err
			}
			s := string(data)
			k.AzureDeploymentsJSON = &s
		}
	} else {
		k.AzureEndpoint = nil
		k.AzureAPIVersion = nil
		k.AzureDeploymentsJSON = nil
	}

	if k.VertexKeyConfig != nil {
		if k.VertexKeyConfig.ProjectID != "" {
			k.VertexProjectID = &k.VertexKeyConfig.ProjectID
		}
		if k.VertexKeyConfig.Region != "" {
			k.VertexRegion = &k.VertexKeyConfig.Region
		}
		if k.VertexKeyConfig.AuthCredentials != "" {
			k.VertexAuthCredentials = &k.VertexKeyConfig.AuthCredentials
		}
	} else {
		k.VertexProjectID = nil
		k.VertexRegion = nil
		k.VertexAuthCredentials = nil
	}

	if k.BedrockKeyConfig != nil {
		if k.BedrockKeyConfig.AccessKey != "" {
			k.BedrockAccessKey = &k.BedrockKeyConfig.AccessKey
		}
		if k.BedrockKeyConfig.SecretKey != "" {
			k.BedrockSecretKey = &k.BedrockKeyConfig.SecretKey
		}
		k.BedrockSessionToken = k.BedrockKeyConfig.SessionToken
		k.BedrockRegion = k.BedrockKeyConfig.Region
		k.BedrockARN = k.BedrockKeyConfig.ARN
		if k.BedrockKeyConfig.Deployments != nil {
			data, err := json.Marshal(k.BedrockKeyConfig.Deployments)
			if err != nil {
				return err
			}
			s := string(data)
			k.BedrockDeploymentsJSON = &s
		}
	} else {
		k.BedrockAccessKey = nil
		k.BedrockSecretKey = nil
		k.BedrockSessionToken = nil
		k.BedrockRegion = nil
		k.BedrockARN = nil
		k.BedrockDeploymentsJSON = nil
	}
	return nil
}

func (c *TableMCPClient) BeforeSave(tx *gorm.DB) error {
	if c.StdioConfig != nil {
		data, err := json.Marshal(c.StdioConfig)
		if err != nil {
			return err
		}
		config := string(data)
		c.StdioConfigJSON = &config
	} else {
		c.StdioConfigJSON = nil
	}

	if c.ToolsToExecute != nil {
		data, err := json.Marshal(c.ToolsToExecute)
		if err != nil {
			return err
		}
		c.ToolsToExecuteJSON = string(data)
	} else {
		c.ToolsToExecuteJSON = "[]"
	}

	if c.ToolsToSkip != nil {
		data, err := json.Marshal(c.ToolsToSkip)
		if err != nil {
			return err
		}
		c.ToolsToSkipJSON = string(data)
	} else {
		c.ToolsToSkipJSON = "[]"
	}

	return nil
}

func (cc *TableClientConfig) BeforeSave(tx *gorm.DB) error {
	if cc.PrometheusLabels != nil {
		data, err := json.Marshal(cc.PrometheusLabels)
		if err != nil {
			return err
		}
		cc.PrometheusLabelsJSON = string(data)
	} else {
		cc.PrometheusLabelsJSON = "[]"
	}

	if cc.AllowedOrigins != nil {
		data, err := json.Marshal(cc.AllowedOrigins)
		if err != nil {
			return err
		}
		cc.AllowedOriginsJSON = string(data)
	} else {
		cc.AllowedOriginsJSON = "[]"
	}

	return nil
}

func (p *TablePlugin) BeforeSave(tx *gorm.DB) error {
	if p.Config != nil {
		data, err := json.Marshal(p.Config)
		if err != nil {
			return err
		}
		p.ConfigJSON = string(data)
	} else {
		p.ConfigJSON = "{}"
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

	return nil
}

func (k *TableKey) AfterFind(tx *gorm.DB) error {
	if k.ModelsJSON != "" {
		if err := json.Unmarshal([]byte(k.ModelsJSON), &k.Models); err != nil {
			return err
		}
	}

	// Reconstruct Azure config if fields are present
	if k.AzureEndpoint != nil {
		azureConfig := &schemas.AzureKeyConfig{
			Endpoint:   *k.AzureEndpoint,
			APIVersion: k.AzureAPIVersion,
		}

		if k.AzureDeploymentsJSON != nil {
			var deployments map[string]string
			if err := json.Unmarshal([]byte(*k.AzureDeploymentsJSON), &deployments); err != nil {
				return err
			}
			azureConfig.Deployments = deployments
		}

		k.AzureKeyConfig = azureConfig
	}

	// Reconstruct Vertex config if fields are present
	if k.VertexProjectID != nil || k.VertexRegion != nil || k.VertexAuthCredentials != nil {
		config := &schemas.VertexKeyConfig{}

		if k.VertexProjectID != nil {
			config.ProjectID = *k.VertexProjectID
		}

		if k.VertexRegion != nil {
			config.Region = *k.VertexRegion
		}
		if k.VertexAuthCredentials != nil {
			config.AuthCredentials = *k.VertexAuthCredentials
		}

		k.VertexKeyConfig = config
	}

	// Reconstruct Bedrock config if fields are present
	if k.BedrockAccessKey != nil || k.BedrockSecretKey != nil || k.BedrockSessionToken != nil || k.BedrockRegion != nil || k.BedrockARN != nil || (k.BedrockDeploymentsJSON != nil && *k.BedrockDeploymentsJSON != "") {
		bedrockConfig := &schemas.BedrockKeyConfig{}

		if k.BedrockAccessKey != nil {
			bedrockConfig.AccessKey = *k.BedrockAccessKey
		}

		bedrockConfig.SessionToken = k.BedrockSessionToken
		bedrockConfig.Region = k.BedrockRegion
		bedrockConfig.ARN = k.BedrockARN

		if k.BedrockSecretKey != nil {
			bedrockConfig.SecretKey = *k.BedrockSecretKey
		}

		if k.BedrockDeploymentsJSON != nil {
			var deployments map[string]string
			if err := json.Unmarshal([]byte(*k.BedrockDeploymentsJSON), &deployments); err != nil {
				return err
			}
			bedrockConfig.Deployments = deployments
		}

		k.BedrockKeyConfig = bedrockConfig
	}

	return nil
}

func (c *TableMCPClient) AfterFind(tx *gorm.DB) error {
	if c.StdioConfigJSON != nil {
		var config schemas.MCPStdioConfig
		if err := json.Unmarshal([]byte(*c.StdioConfigJSON), &config); err != nil {
			return err
		}
		c.StdioConfig = &config
	}

	if c.ToolsToExecuteJSON != "" {
		if err := json.Unmarshal([]byte(c.ToolsToExecuteJSON), &c.ToolsToExecute); err != nil {
			return err
		}
	}

	if c.ToolsToSkipJSON != "" {
		if err := json.Unmarshal([]byte(c.ToolsToSkipJSON), &c.ToolsToSkip); err != nil {
			return err
		}
	}

	return nil
}

func (cc *TableClientConfig) AfterFind(tx *gorm.DB) error {
	if cc.PrometheusLabelsJSON != "" {
		if err := json.Unmarshal([]byte(cc.PrometheusLabelsJSON), &cc.PrometheusLabels); err != nil {
			return err
		}
	}

	if cc.AllowedOriginsJSON != "" {
		if err := json.Unmarshal([]byte(cc.AllowedOriginsJSON), &cc.AllowedOrigins); err != nil {
			return err
		}
	}

	return nil
}

func (p *TablePlugin) AfterFind(tx *gorm.DB) error {
	if p.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(p.ConfigJSON), &p.Config); err != nil {
			return err
		}
	} else {
		p.Config = nil
	}

	return nil
}

// TableConfig represents generic configuration key-value pairs
type TableConfig struct {
	Key   string `gorm:"primaryKey;type:varchar(255)" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// GOVERNANCE TABLES

// TableBudget defines spending limits with configurable reset periods
type TableBudget struct {
	ID            string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	MaxLimit      float64   `gorm:"not null" json:"max_limit"`                       // Maximum budget in dollars
	ResetDuration string    `gorm:"type:varchar(50);not null" json:"reset_duration"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	LastReset     time.Time `gorm:"index" json:"last_reset"`                         // Last time budget was reset
	CurrentUsage  float64   `gorm:"default:0" json:"current_usage"`                  // Current usage in dollars

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

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

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableCustomer represents a customer entity with budget
type TableCustomer struct {
	ID       string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name     string  `gorm:"type:varchar(255);not null" json:"name"`
	BudgetID *string `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`

	// Relationships
	Budget      *TableBudget      `gorm:"foreignKey:BudgetID" json:"budget,omitempty"`
	Teams       []TableTeam       `gorm:"foreignKey:CustomerID" json:"teams"`
	VirtualKeys []TableVirtualKey `gorm:"foreignKey:CustomerID" json:"virtual_keys"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableTeam represents a team entity with budget and customer association
type TableTeam struct {
	ID         string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name       string  `gorm:"type:varchar(255);not null" json:"name"`
	CustomerID *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"` // A team can belong to a customer
	BudgetID   *string `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`

	// Relationships
	Customer    *TableCustomer    `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Budget      *TableBudget      `gorm:"foreignKey:BudgetID" json:"budget,omitempty"`
	VirtualKeys []TableVirtualKey `gorm:"foreignKey:TeamID" json:"virtual_keys"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableVirtualKey represents a virtual key with budget, rate limits, and team/customer association
type TableVirtualKey struct {
	ID               string   `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name             string   `gorm:"uniqueIndex:idx_virtual_key_name;type:varchar(255);not null" json:"name"`
	Description      string   `gorm:"type:text" json:"description,omitempty"`
	Value            string   `gorm:"uniqueIndex:idx_virtual_key_value;type:varchar(255);not null" json:"value"` // The virtual key value
	IsActive         bool     `gorm:"default:true" json:"is_active"`
	AllowedModels    []string `gorm:"type:text;serializer:json" json:"allowed_models"`    // Empty means all models allowed
	AllowedProviders []string `gorm:"type:text;serializer:json" json:"allowed_providers"` // Empty means all providers allowed

	// Foreign key relationships (mutually exclusive: either TeamID or CustomerID, not both)
	TeamID      *string    `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	CustomerID  *string    `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`
	BudgetID    *string    `gorm:"type:varchar(255);index" json:"budget_id,omitempty"`
	RateLimitID *string    `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`
	Keys        []TableKey `gorm:"many2many:governance_virtual_key_keys;constraint:OnDelete:CASCADE" json:"keys"`

	// Relationships
	Team      *TableTeam      `gorm:"foreignKey:TeamID" json:"team,omitempty"`
	Customer  *TableCustomer  `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID" json:"rate_limit,omitempty"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableModelPricing represents pricing information for AI models
type TableModelPricing struct {
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
func (TableBudget) TableName() string       { return "governance_budgets" }
func (TableRateLimit) TableName() string    { return "governance_rate_limits" }
func (TableCustomer) TableName() string     { return "governance_customers" }
func (TableTeam) TableName() string         { return "governance_teams" }
func (TableVirtualKey) TableName() string   { return "governance_virtual_keys" }
func (TableConfig) TableName() string       { return "governance_config" }
func (TableModelPricing) TableName() string { return "governance_model_pricing" }

// GORM Hooks for validation and constraints

// BeforeSave hook for VirtualKey to enforce mutual exclusion
func (vk *TableVirtualKey) BeforeSave(tx *gorm.DB) error {
	// Enforce mutual exclusion: VK can belong to either Team OR Customer, not both
	if vk.TeamID != nil && vk.CustomerID != nil {
		return fmt.Errorf("virtual key cannot belong to both team and customer")
	}
	return nil
}

// BeforeSave hook for Budget to validate reset duration format and max limit
func (b *TableBudget) BeforeSave(tx *gorm.DB) error {
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
func (rl *TableRateLimit) BeforeSave(tx *gorm.DB) error {
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
func (vk *TableVirtualKey) AfterAutoMigrate(tx *gorm.DB) error {
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
