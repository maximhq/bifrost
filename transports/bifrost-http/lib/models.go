// Package lib provides GORM model definitions for Bifrost configuration storage
package lib

import (
	"encoding/json"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
	"gorm.io/gorm"
)

type DBConfigHash struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Hash      string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"hash"`
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// DBProvider represents a provider configuration in the database
type DBProvider struct {
	ID                    uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name                  string    `gorm:"type:varchar(50);uniqueIndex;not null" json:"name"` // ModelProvider as string
	NetworkConfigJSON     string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.NetworkConfig
	ConcurrencyBufferJSON string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.ConcurrencyAndBufferSize
	MetaConfigJSON        string    `gorm:"type:text" json:"-"`                                // JSON serialized schemas.MetaConfig
	MetaConfigType        string    `gorm:"type:varchar(20)" json:"-"`                         // Type of meta config ("bedrock", etc.)
	CreatedAt             time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt             time.Time `gorm:"index;not null" json:"updated_at"`

	// Relationships
	Keys []DBKey `gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE" json:"keys"`

	// Virtual fields for runtime use (not stored in DB)
	NetworkConfig            *schemas.NetworkConfig            `gorm:"-" json:"network_config,omitempty"`
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `gorm:"-" json:"concurrency_and_buffer_size,omitempty"`
	MetaConfig               *schemas.MetaConfig               `gorm:"-" json:"meta_config,omitempty"`
}

// DBKey represents an API key configuration in the database
type DBKey struct {
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

	// Virtual fields for runtime use (not stored in DB)
	Models          []string                 `gorm:"-" json:"models"`
	AzureKeyConfig  *schemas.AzureKeyConfig  `gorm:"-" json:"azure_key_config,omitempty"`
	VertexKeyConfig *schemas.VertexKeyConfig `gorm:"-" json:"vertex_key_config,omitempty"`
}

// DBMCPClient represents an MCP client configuration in the database
type DBMCPClient struct {
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

// DBClientConfig represents global client configuration in the database
type DBClientConfig struct {
	ID                   uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	DropExcessRequests   bool      `gorm:"default:false" json:"drop_excess_requests"`
	PrometheusLabelsJSON string    `gorm:"type:text" json:"-"` // JSON serialized []string
	InitialPoolSize      int       `gorm:"default:300" json:"initial_pool_size"`
	EnableLogging        bool      `json:"enable_logging"`
	CreatedAt            time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt            time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	PrometheusLabels []string `gorm:"-" json:"prometheus_labels"`
}

// DBEnvKey represents environment variable tracking in the database
type DBEnvKey struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	EnvVar     string    `gorm:"type:varchar(255);index;not null" json:"env_var"`
	Provider   string    `gorm:"type:varchar(50);index" json:"provider"`        // Empty for MCP/client configs
	KeyType    string    `gorm:"type:varchar(50);not null" json:"key_type"`     // "api_key", "azure_config", "vertex_config", "meta_config", "connection_string"
	ConfigPath string    `gorm:"type:varchar(500);not null" json:"config_path"` // Descriptive path of where this env var is used
	KeyID      string    `gorm:"type:varchar(255);index" json:"key_id"`         // Key UUID (empty for non-key configs)
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
}

// TableName sets the table name for each model
func (DBConfigHash) TableName() string   { return "config_hashes" }
func (DBProvider) TableName() string     { return "config_providers" }
func (DBKey) TableName() string          { return "config_keys" }
func (DBMCPClient) TableName() string    { return "config_mcp_clients" }
func (DBClientConfig) TableName() string { return "config_client" }
func (DBEnvKey) TableName() string       { return "config_env_keys" }

// GORM Hooks for JSON serialization/deserialization

// BeforeSave hooks for serialization
func (p *DBProvider) BeforeSave(tx *gorm.DB) error {
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

	if p.MetaConfig != nil {
		data, err := json.Marshal(*p.MetaConfig)
		if err != nil {
			return err
		}
		p.MetaConfigJSON = string(data)

		// Set meta config type for proper deserialization
		switch (*p.MetaConfig).(type) {
		case *meta.BedrockMetaConfig:
			p.MetaConfigType = "bedrock"
		default:
			
		}
	}

	return nil
}

func (k *DBKey) BeforeSave(tx *gorm.DB) error {
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

	return nil
}

func (c *DBMCPClient) BeforeSave(tx *gorm.DB) error {
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

func (cc *DBClientConfig) BeforeSave(tx *gorm.DB) error {
	if cc.PrometheusLabels != nil {
		data, err := json.Marshal(cc.PrometheusLabels)
		if err != nil {
			return err
		}
		cc.PrometheusLabelsJSON = string(data)
	}

	return nil
}

// AfterFind hooks for deserialization
func (p *DBProvider) AfterFind(tx *gorm.DB) error {
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

	if p.MetaConfigJSON != "" {
		var metaConfig schemas.MetaConfig

		switch p.MetaConfigType {
		case "bedrock":
			var bedrockConfig meta.BedrockMetaConfig
			if err := json.Unmarshal([]byte(p.MetaConfigJSON), &bedrockConfig); err != nil {
				return err
			}
			metaConfig = &bedrockConfig
		default:
			// Unknown meta config type, skip
			return nil
		}

		p.MetaConfig = &metaConfig
	}

	return nil
}

func (k *DBKey) AfterFind(tx *gorm.DB) error {
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

	return nil
}

func (c *DBMCPClient) AfterFind(tx *gorm.DB) error {
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

func (cc *DBClientConfig) AfterFind(tx *gorm.DB) error {
	if cc.PrometheusLabelsJSON != "" {
		if err := json.Unmarshal([]byte(cc.PrometheusLabelsJSON), &cc.PrometheusLabels); err != nil {
			return err
		}
	}

	return nil
}
