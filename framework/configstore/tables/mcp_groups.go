package tables

import "time"

// TableMCPToolGroup is a named bundle of MCP client+tool pairs.
type TableMCPToolGroup struct {
	ID               string     `gorm:"primaryKey;type:text"`
	Name             string     `gorm:"uniqueIndex;not null"`
	Description      string     `gorm:"type:text"`
	Enabled          bool       `gorm:"default:true"`
	MaxCallsPerHour  *int64     // nil = unlimited
	MaxCallsPerDay   *int64     // nil = unlimited
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (TableMCPToolGroup) TableName() string { return "mcp_tool_groups" }

// TableMCPToolGroupMember links a tool (or whole client) into a group.
type TableMCPToolGroupMember struct {
	ID       string `gorm:"primaryKey;type:text"`
	GroupID  string `gorm:"index;not null"` // references mcp_tool_groups.id
	ClientID string `gorm:"type:text;not null"`
	ToolName string `gorm:"type:text"` // empty = all tools from this client
}

func (TableMCPToolGroupMember) TableName() string { return "mcp_tool_group_members" }

// TableVirtualKeyMCPGroup joins a virtual key with an MCP tool group.
type TableVirtualKeyMCPGroup struct {
	VirtualKeyID string `gorm:"primaryKey;type:text"`
	GroupID      string `gorm:"primaryKey;type:text"`
	AssignedAt   time.Time
}

func (TableVirtualKeyMCPGroup) TableName() string { return "virtual_key_mcp_groups" }
