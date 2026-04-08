package tables

import "time"

// TableRole represents an RBAC role in the system.
// System roles (viewer, operator, admin, super_admin) cannot be deleted.
type TableRole struct {
	ID          string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name        string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	Description string    `gorm:"type:text" json:"description"`
	IsSystem    bool      `gorm:"default:false" json:"is_system"`
	CreatedAt   time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for TableRole
func (TableRole) TableName() string { return "rbac_roles" }

// TablePermission represents a granular permission node (resource:action).
type TablePermission struct {
	ID       string `gorm:"primaryKey;type:varchar(255)" json:"id"` // e.g. "providers:read"
	Resource string `gorm:"type:varchar(100);not null;index" json:"resource"`
	Action   string `gorm:"type:varchar(100);not null" json:"action"`
}

// TableName sets the table name for TablePermission
func (TablePermission) TableName() string { return "rbac_permissions" }

// TableRolePermission is the join table linking roles to permissions.
type TableRolePermission struct {
	RoleID       string `gorm:"primaryKey;type:varchar(255);index" json:"role_id"`
	PermissionID string `gorm:"primaryKey;type:varchar(255);index" json:"permission_id"`
}

// TableName sets the table name for TableRolePermission
func (TableRolePermission) TableName() string { return "rbac_role_permissions" }

// TableUserRole assigns a role to a user (identified by external user ID or session ID).
type TableUserRole struct {
	ID        string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	UserID    string    `gorm:"type:varchar(255);not null;index" json:"user_id"`
	RoleID    string    `gorm:"type:varchar(255);not null;index" json:"role_id"`
	GrantedBy string    `gorm:"type:varchar(255)" json:"granted_by"` // user ID of granter; empty = system
	GrantedAt time.Time `gorm:"index;not null" json:"granted_at"`
}

// TableName sets the table name for TableUserRole
func (TableUserRole) TableName() string { return "rbac_user_roles" }
