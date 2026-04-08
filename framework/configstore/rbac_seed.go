package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// defaultPermissions is the canonical permission matrix for system roles.
// Key = permissionID, Value = set of roleIDs that should have it.
var defaultPermissions = []tables.TablePermission{
	{ID: "providers:read", Resource: "providers", Action: "read"},
	{ID: "providers:write", Resource: "providers", Action: "write"},
	{ID: "virtual_keys:read", Resource: "virtual_keys", Action: "read"},
	{ID: "virtual_keys:write", Resource: "virtual_keys", Action: "write"},
	{ID: "plugins:write", Resource: "plugins", Action: "write"},
	{ID: "users:write", Resource: "users", Action: "write"},
	{ID: "roles:write", Resource: "roles", Action: "write"},
	{ID: "license:write", Resource: "license", Action: "write"},
	{ID: "audit_logs:read", Resource: "audit_logs", Action: "read"},
	{ID: "system:admin", Resource: "system", Action: "admin"},
}

type rolePermissionSeed struct {
	roleID      string
	permissions []string // permissionIDs
}

var defaultRoles = []tables.TableRole{
	{ID: "viewer", Name: "Viewer", Description: "Read-only access to resources", IsSystem: true},
	{ID: "operator", Name: "Operator", Description: "Can manage virtual keys and providers", IsSystem: true},
	{ID: "admin", Name: "Admin", Description: "Full admin access except role and license management", IsSystem: true},
	{ID: "super_admin", Name: "Super Admin", Description: "Unrestricted access", IsSystem: true},
}

var defaultRolePermissions = []rolePermissionSeed{
	{
		roleID: "viewer",
		permissions: []string{"providers:read", "virtual_keys:read"},
	},
	{
		roleID: "operator",
		permissions: []string{"providers:read", "virtual_keys:read", "virtual_keys:write"},
	},
	{
		roleID: "admin",
		permissions: []string{
			"providers:read", "providers:write",
			"virtual_keys:read", "virtual_keys:write",
			"plugins:write", "users:write", "audit_logs:read",
		},
	},
	{
		roleID: "super_admin",
		permissions: []string{
			"providers:read", "providers:write",
			"virtual_keys:read", "virtual_keys:write",
			"plugins:write", "users:write",
			"roles:write", "license:write",
			"audit_logs:read", "system:admin",
		},
	},
}

// SeedRBACDefaults inserts the 4 system roles and their default permissions
// into the database. All operations use upsert semantics so re-seeding is safe.
// This function must be called after database migrations have run.
func SeedRBACDefaults(ctx context.Context, store ConfigStore) error {
	// 1. Upsert permissions
	for i := range defaultPermissions {
		if err := store.UpsertPermission(ctx, &defaultPermissions[i]); err != nil {
			return fmt.Errorf("seeding permission %s: %w", defaultPermissions[i].ID, err)
		}
	}

	// 2. Upsert roles
	for i := range defaultRoles {
		existing, err := store.GetRole(ctx, defaultRoles[i].ID)
		if err != nil || existing == nil {
			// Role doesn't exist — create it
			if createErr := store.CreateRole(ctx, &defaultRoles[i]); createErr != nil {
				return fmt.Errorf("seeding role %s: %w", defaultRoles[i].ID, createErr)
			}
		}
		// Role exists — leave it as-is (don't overwrite admin-changed fields)
	}

	// 3. Assign permissions to roles (idempotent — duplicate assignments ignored)
	for _, rp := range defaultRolePermissions {
		for _, permID := range rp.permissions {
			// AssignPermissionToRole uses INSERT OR IGNORE / ON CONFLICT DO NOTHING
			_ = store.AssignPermissionToRole(ctx, rp.roleID, permID)
		}
	}

	return nil
}
