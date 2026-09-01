package tables

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

// MCPToolSpec is one entry in a Virtual MCP: a source client and the tools it
// exposes. Empty ToolNames means all of the client's tools, current and future.
type MCPToolSpec struct {
	MCPClientID string   `json:"mcp_client_id"` // source MCP client ID
	ToolNames   []string `json:"tool_names"`    // specific tools; empty = all tools from the client
}

// TableVirtualMCP is a Virtual MCP server: a named bundle of tool specs from one
// or more source clients, served at /mcp/<slug> and assignable to virtual keys.
// The physical table name is kept from the enterprise tool-group table so
// existing rows are reused with no data migration.
type TableVirtualMCP struct {
	ID           uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name         string  `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	EndpointSlug string  `gorm:"column:endpoint_slug;type:varchar(255);uniqueIndex" json:"endpoint_slug"` // URL-safe, immutable after creation; serves /mcp/<slug>
	Description  *string `gorm:"type:text" json:"description,omitempty"`
	Enabled      bool    `gorm:"not null;default:true" json:"enabled"`

	Tools       *string       `gorm:"type:text" json:"-"` // JSON of ParsedTools stored in DB
	ParsedTools []MCPToolSpec `gorm:"-" json:"tools"`     // decoded tool specs
	ConfigHash  string        `gorm:"type:varchar(255);null" json:"config_hash,omitempty"`

	// CreatedByUserID records the creator, like TableVirtualKey. It is populated in enterprise (where
	// DAC scopes rows by it) and left null in pure OSS; the DAC scoping logic lives in enterprise.
	CreatedByUserID *string   `gorm:"column:created_by_user_id;type:varchar(255);index:idx_mcp_tool_groups_created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName retains the original physical name so existing rows are reused.
func (TableVirtualMCP) TableName() string { return "enterprise_mcp_tool_groups" }

// BeforeSave serializes ParsedTools into the Tools JSON column.
func (g *TableVirtualMCP) BeforeSave(tx *gorm.DB) error {
	if g.ParsedTools != nil {
		for i := range g.ParsedTools {
			if g.ParsedTools[i].ToolNames == nil {
				// Keep "all tools" as [] not null, for clean JSON downstream.
				g.ParsedTools[i].ToolNames = []string{}
			}
		}
		data, err := json.Marshal(g.ParsedTools)
		if err != nil {
			return err
		}
		s := string(data)
		g.Tools = &s
	} else {
		g.Tools = nil
	}
	return nil
}

// AfterFind decodes the Tools JSON column into ParsedTools.
func (g *TableVirtualMCP) AfterFind(tx *gorm.DB) error {
	if g.Tools != nil && strings.TrimSpace(*g.Tools) != "" {
		if err := json.Unmarshal([]byte(*g.Tools), &g.ParsedTools); err != nil {
			return err
		}
	}
	return nil
}

// TableVirtualKeyVirtualMCP is the VK-to-Virtual-MCP assignment. Table and
// columns are kept from the enterprise tool-group join (the FK stays
// tool_group_id) so existing rows are reused.
type TableVirtualKeyVirtualMCP struct {
	ID           uint            `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualMCPID uint            `gorm:"column:tool_group_id;not null;uniqueIndex:idx_tool_group_virtual_key" json:"virtual_mcp_id"`
	VirtualKeyID string          `gorm:"type:varchar(255);not null;uniqueIndex:idx_tool_group_virtual_key" json:"virtual_key_id"`
	VirtualMCP   TableVirtualMCP `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;foreignKey:VirtualMCPID;references:ID" json:"-"`
	VirtualKey   TableVirtualKey `gorm:"foreignKey:VirtualKeyID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName retains the original physical name so existing rows are reused.
func (TableVirtualKeyVirtualMCP) TableName() string {
	return "enterprise_mcp_tool_group_virtual_keys"
}
