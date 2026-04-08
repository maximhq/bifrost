package tables

import "time"

// TableUserGroup represents a named group of users, supporting manual
// creation and SCIM-synced external groups.
type TableUserGroup struct {
	ID          string     `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name        string     `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	Description string     `gorm:"type:text" json:"description"`
	Role        string     `gorm:"type:varchar(255)" json:"role"` // RBAC role inherited by all members
	ExternalID  string     `gorm:"type:varchar(512);index" json:"external_id,omitempty"` // IdP group ID for SCIM
	SyncedAt    *time.Time `gorm:"index" json:"synced_at,omitempty"`

	// Per-group budget override
	BudgetMaxLimit      *float64 `gorm:"type:decimal(20,6)" json:"budget_max_limit,omitempty"`
	BudgetResetDuration *int64   `gorm:"type:bigint" json:"budget_reset_duration,omitempty"` // seconds

	// Per-group rate limit override
	RateLimitRequestsPerMin *int64 `gorm:"type:bigint" json:"rate_limit_requests_per_min,omitempty"`
	RateLimitTokensPerMin   *int64 `gorm:"type:bigint" json:"rate_limit_tokens_per_min,omitempty"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for TableUserGroup
func (TableUserGroup) TableName() string { return "user_groups" }

// TableUserGroupMember is the join table linking users to groups.
type TableUserGroupMember struct {
	ID      string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	GroupID string    `gorm:"type:varchar(255);index;not null" json:"group_id"`
	UserID  string    `gorm:"type:varchar(255);index;not null" json:"user_id"` // ExternalUser.ID
	AddedBy string    `gorm:"type:varchar(255)" json:"added_by,omitempty"`     // empty = SCIM-provisioned
	AddedAt time.Time `gorm:"index;not null" json:"added_at"`
}

// TableName sets the table name for TableUserGroupMember
func (TableUserGroupMember) TableName() string { return "user_group_members" }

// TableUserGroupVirtualKey assigns a virtual key to a user group with an
// optional per-group budget override.
type TableUserGroupVirtualKey struct {
	ID             string   `gorm:"primaryKey;type:varchar(255)" json:"id"`
	GroupID        string   `gorm:"type:varchar(255);index;not null" json:"group_id"`
	VirtualKeyID   string   `gorm:"type:varchar(255);index;not null" json:"virtual_key_id"`
	BudgetOverride *float64 `gorm:"type:decimal(20,6)" json:"budget_override,omitempty"`
}

// TableName sets the table name for TableUserGroupVirtualKey
func (TableUserGroupVirtualKey) TableName() string { return "user_group_virtual_keys" }

// TableUserGroupMCPGroup links user groups to MCP tool groups.
type TableUserGroupMCPGroup struct {
	ID         string `gorm:"primaryKey;type:varchar(255)" json:"id"`
	GroupID    string `gorm:"type:varchar(255);index;not null" json:"group_id"`
	MCPGroupID string `gorm:"type:varchar(255);index;not null" json:"mcp_group_id"`
}

// TableName sets the table name for TableUserGroupMCPGroup
func (TableUserGroupMCPGroup) TableName() string { return "user_group_mcp_groups" }
