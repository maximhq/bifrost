package tables

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableMCPClient represents an MCP client configuration in the database
type TableMCPClient struct {
	ID                     uint            `gorm:"primaryKey;autoIncrement" json:"id"` // ID is used as the internal primary key and is also accessed by public methods, so it must be present.
	ClientID               string          `gorm:"type:varchar(255);uniqueIndex;not null" json:"client_id"`
	Name                   string          `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	IsCodeModeClient       bool            `gorm:"default:false" json:"is_code_mode_client"`         // Whether the client is a code mode client
	ConnectionType         string          `gorm:"type:varchar(20);not null" json:"connection_type"` // schemas.MCPConnectionType
	ConnectionString       *schemas.EnvVar `gorm:"type:text" json:"connection_string,omitempty"`
	StdioConfigJSON        *string         `gorm:"type:text" json:"-"`                              // JSON serialized schemas.MCPStdioConfig
	ToolsToExecuteJSON     string          `gorm:"type:text" json:"-"`                              // JSON serialized []string
	ToolsToAutoExecuteJSON string          `gorm:"type:text" json:"-"`                              // JSON serialized []string
	HeadersJSON            string          `gorm:"type:text" json:"-"`                              // JSON serialized map[string]string
	IsPingAvailable        *bool           `gorm:"default:true" json:"is_ping_available,omitempty"` // Whether the MCP server supports ping for health checks
	ToolPricingJSON        string          `gorm:"type:text" json:"-"`                              // JSON serialized map[string]float64
	ToolSyncInterval       int             `gorm:"default:0" json:"tool_sync_interval"`             // Per-client tool sync interval in minutes (0 = use global, -1 = disabled)

	// OAuth authentication fields
	AuthType      string            `gorm:"type:varchar(20);default:'headers'" json:"auth_type"`                         // "none", "headers", "oauth"
	OauthConfigID *string           `gorm:"type:varchar(255);index;constraint:OnDelete:CASCADE" json:"oauth_config_id"`  // Foreign key to oauth_configs.ID with CASCADE delete
	OauthConfig   *TableOauthConfig `gorm:"foreignKey:OauthConfigID;references:ID;constraint:OnDelete:CASCADE" json:"-"` // Gorm relationship

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'" json:"-"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	StdioConfig        *schemas.MCPStdioConfig   `gorm:"-" json:"stdio_config,omitempty"`
	ToolsToExecute     []string                  `gorm:"-" json:"tools_to_execute"`
	ToolsToAutoExecute []string                  `gorm:"-" json:"tools_to_auto_execute"`
	Headers            map[string]schemas.EnvVar `gorm:"-" json:"headers"`
	ToolPricing        map[string]float64        `gorm:"-" json:"tool_pricing"`
}

// TableName sets the table name for each model
func (TableMCPClient) TableName() string { return "config_mcp_clients" }

// BeforeSave is a GORM hook that serializes runtime fields (stdio config, tools, headers,
// pricing) into JSON columns and encrypts the connection string and headers before writing
// to the database. Environment-variable-backed connection strings are not encrypted.
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

	if c.ToolsToAutoExecute != nil {
		data, err := json.Marshal(c.ToolsToAutoExecute)
		if err != nil {
			return err
		}
		c.ToolsToAutoExecuteJSON = string(data)
	} else {
		c.ToolsToAutoExecuteJSON = "[]"
	}

	if c.Headers != nil {
		headersToSerialize := make(map[string]string, len(c.Headers))
		for key, value := range c.Headers {
			if value.IsFromEnv() {
				headersToSerialize[key] = value.EnvVar
			} else {
				headersToSerialize[key] = value.GetValue()
			}
		}
		data, err := json.Marshal(headersToSerialize)
		if err != nil {
			return err
		}
		c.HeadersJSON = string(data)
	} else {
		c.HeadersJSON = "{}"
	}

	if c.ToolPricing != nil {
		data, err := json.Marshal(c.ToolPricing)
		if err != nil {
			return err
		}
		c.ToolPricingJSON = string(data)
	} else {
		c.ToolPricingJSON = "{}"
	}

	// Encrypt sensitive fields after serialization.
	// Always set EncryptionStatus when encryption is enabled so the startup
	// batch pass does not re-process this row indefinitely.
	if encrypt.IsEnabled() {
		if c.ConnectionString != nil && !c.ConnectionString.IsFromEnv() && c.ConnectionString.GetValue() != "" {
			// Copy to avoid encrypting the shared ConnectionString through the pointer
			cs := *c.ConnectionString
			enc, err := encrypt.Encrypt(cs.Val)
			if err != nil {
				return fmt.Errorf("failed to encrypt mcp connection string: %w", err)
			}
			cs.Val = enc
			c.ConnectionString = &cs
		}
		if c.HeadersJSON != "" && c.HeadersJSON != "{}" {
			enc, err := encrypt.Encrypt(c.HeadersJSON)
			if err != nil {
				return fmt.Errorf("failed to encrypt mcp headers: %w", err)
			}
			c.HeadersJSON = enc
		}
		c.EncryptionStatus = EncryptionStatusEncrypted
	}

	return nil
}

// AfterFind is a GORM hook that decrypts the connection string and headers (if encrypted)
// and deserializes JSON columns back into runtime structs after reading from the database.
func (c *TableMCPClient) AfterFind(tx *gorm.DB) error {
	if c.EncryptionStatus == "encrypted" {
		if c.HeadersJSON != "" && c.HeadersJSON != "{}" {
			decrypted, err := encrypt.Decrypt(c.HeadersJSON)
			if err != nil {
				return fmt.Errorf("failed to decrypt mcp headers: %w", err)
			}
			c.HeadersJSON = decrypted
		}
		if c.ConnectionString != nil && !c.ConnectionString.IsFromEnv() && c.ConnectionString.GetValue() != "" {
			decrypted, err := encrypt.Decrypt(c.ConnectionString.Val)
			if err != nil {
				return fmt.Errorf("failed to decrypt mcp connection string: %w", err)
			}
			c.ConnectionString.Val = decrypted
		}
	}
	if c.StdioConfigJSON != nil {
		var config schemas.MCPStdioConfig
		if err := sonic.Unmarshal([]byte(*c.StdioConfigJSON), &config); err != nil {
			return err
		}
		c.StdioConfig = &config
	}
	if c.ToolsToExecuteJSON != "" {
		if err := sonic.Unmarshal([]byte(c.ToolsToExecuteJSON), &c.ToolsToExecute); err != nil {
			return err
		}
	}
	if c.ToolsToAutoExecuteJSON != "" {
		if err := sonic.Unmarshal([]byte(c.ToolsToAutoExecuteJSON), &c.ToolsToAutoExecute); err != nil {
			return err
		}
	}
	if c.HeadersJSON != "" {
		if err := sonic.Unmarshal([]byte(c.HeadersJSON), &c.Headers); err != nil {
			return err
		}
	}

	if c.ToolPricingJSON != "" {
		if err := json.Unmarshal([]byte(c.ToolPricingJSON), &c.ToolPricing); err != nil {
			return err
		}
	}
	return nil
}
