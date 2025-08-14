package configstore

import (
	"encoding/json"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

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
	KeyID      string    `gorm:"type:varchar(255);index;not null" json:"key_id"` // UUID from schemas.Key
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
	EnableCaching           bool      `gorm:"" json:"enable_caching"`
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
	Enabled         bool      `gorm:"" json:"enabled"`                       // Enable vector store
	Type            *string   `gorm:"type:varchar(50);not null" json:"type"` // "redis"
	TTLSeconds      int       `gorm:"default:300" json:"ttl_seconds"`        // TTL in seconds (default: 5 minutes)
	CacheByModel    bool      `gorm:"" json:"cache_by_model"`                // Include model in cache key
	CacheByProvider bool      `gorm:"" json:"cache_by_provider"`             // Include provider in cache key
	Config          *string   `gorm:"type:text" json:"config"`               // JSON serialized schemas.RedisVectorStoreConfig
	CreatedAt       time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableConfigHash) TableName() string        { return "config_hashes" }
func (TableProvider) TableName() string          { return "config_providers" }
func (TableKey) TableName() string               { return "config_keys" }
func (TableMCPClient) TableName() string         { return "config_mcp_clients" }
func (TableClientConfig) TableName() string      { return "config_client" }
func (TableEnvKey) TableName() string            { return "config_env_keys" }
func (TableVectorStoreConfig) TableName() string { return "config_vector_store" }

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
	}

	if k.AzureKeyConfig != nil && k.AzureKeyConfig.Deployments != nil {
		data, err := json.Marshal(k.AzureKeyConfig.Deployments)
		if err != nil {
			return err
		}
		deployments := string(data)
		k.AzureDeploymentsJSON = &deployments
	}

	if k.BedrockKeyConfig != nil && k.BedrockKeyConfig.Deployments != nil {
		data, err := json.Marshal(k.BedrockKeyConfig.Deployments)
		if err != nil {
			return err
		}
		deployments := string(data)
		k.BedrockDeploymentsJSON = &deployments
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
	}

	if cc.AllowedOrigins != nil {
		data, err := json.Marshal(cc.AllowedOrigins)
		if err != nil {
			return err
		}
		cc.AllowedOriginsJSON = string(data)
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
	if k.VertexProjectID != nil {
		config := &schemas.VertexKeyConfig{
			ProjectID: *k.VertexProjectID,
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
	if k.BedrockAccessKey != nil {
		bedrockConfig := &schemas.BedrockKeyConfig{
			AccessKey:    *k.BedrockAccessKey,
			SessionToken: k.BedrockSessionToken,
			Region:       k.BedrockRegion,
			ARN:          k.BedrockARN,
		}

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
