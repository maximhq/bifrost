package tables

import (
	"encoding/json"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// TableMCPClient represents an MCP client configuration in the database
type TableMCPClient struct {
	ID                     uint      `gorm:"primaryKey;autoIncrement" json:"id"` // ID is used as the internal primary key and is also accessed by public methods, so it must be present.
	ClientID               string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"client_id"`
	Name                   string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	IsCodeModeClient       bool      `gorm:"default:false" json:"is_code_mode_client"` // Whether the client is a code mode client
	ConnectionType         string    `gorm:"type:varchar(20);not null" json:"connection_type"` // schemas.MCPConnectionType
	ConnectionString       *string   `gorm:"type:text" json:"connection_string,omitempty"`
	StdioConfigJSON        *string   `gorm:"type:text" json:"-"` // JSON serialized schemas.MCPStdioConfig
	ToolsToExecuteJSON     string    `gorm:"type:text" json:"-"` // JSON serialized []string
	ToolsToAutoExecuteJSON string    `gorm:"type:text" json:"-"` // JSON serialized []string
	HeadersJSON            string    `gorm:"type:text" json:"-"` // JSON serialized map[string]string
	CreatedAt              time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt              time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	StdioConfig        *schemas.MCPStdioConfig `gorm:"-" json:"stdio_config,omitempty"`
	ToolsToExecute     []string                `gorm:"-" json:"tools_to_execute"`
	ToolsToAutoExecute []string                `gorm:"-" json:"tools_to_auto_execute"`
	Headers            map[string]string       `gorm:"-" json:"headers"`
}

// TableName sets the table name for each model
func (TableMCPClient) TableName() string { return "config_mcp_clients" }

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
		data, err := json.Marshal(c.Headers)
		if err != nil {
			return err
		}
		c.HeadersJSON = string(data)
	} else {
		c.HeadersJSON = "{}"
	}

	return nil
}

// AfterFind hooks for deserialization
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

	if c.ToolsToAutoExecuteJSON != "" {
		if err := json.Unmarshal([]byte(c.ToolsToAutoExecuteJSON), &c.ToolsToAutoExecute); err != nil {
			return err
		}
	}

	if c.HeadersJSON != "" {
		if err := json.Unmarshal([]byte(c.HeadersJSON), &c.Headers); err != nil {
			return err
		}
	}

	return nil
}
