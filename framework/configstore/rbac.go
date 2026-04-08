package configstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ─── Role CRUD ───────────────────────────────────────────────────────────────

// CreateRole inserts a new RBAC role.
func (s *RDBConfigStore) CreateRole(ctx context.Context, role *tables.TableRole) error {
	if role.ID == "" {
		role.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	role.CreatedAt = now
	role.UpdatedAt = now
	if err := s.db.WithContext(ctx).Create(role).Error; err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	return nil
}

// GetRole returns a role by ID.
func (s *RDBConfigStore) GetRole(ctx context.Context, id string) (*tables.TableRole, error) {
	var role tables.TableRole
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&role).Error; err != nil {
		return nil, fmt.Errorf("get role %s: %w", id, err)
	}
	return &role, nil
}

// ListRoles returns all RBAC roles.
func (s *RDBConfigStore) ListRoles(ctx context.Context) ([]tables.TableRole, error) {
	var roles []tables.TableRole
	if err := s.db.WithContext(ctx).Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return roles, nil
}

// UpdateRole updates a role by ID (non-system roles only).
func (s *RDBConfigStore) UpdateRole(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&tables.TableRole{}).
		Where("id = ? AND is_system = false", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update role %s: %w", id, err)
	}
	return nil
}

// DeleteRole deletes a non-system role.
func (s *RDBConfigStore) DeleteRole(ctx context.Context, id string) error {
	if err := s.db.WithContext(ctx).
		Where("id = ? AND is_system = false", id).
		Delete(&tables.TableRole{}).Error; err != nil {
		return fmt.Errorf("delete role %s: %w", id, err)
	}
	return nil
}

// ─── Permission CRUD ─────────────────────────────────────────────────────────

// UpsertPermission creates a permission if it does not already exist.
func (s *RDBConfigStore) UpsertPermission(ctx context.Context, perm *tables.TablePermission) error {
	return s.db.WithContext(ctx).
		Where("id = ?", perm.ID).
		FirstOrCreate(perm).Error
}

// ListPermissions returns all registered permissions.
func (s *RDBConfigStore) ListPermissions(ctx context.Context) ([]tables.TablePermission, error) {
	var perms []tables.TablePermission
	if err := s.db.WithContext(ctx).Find(&perms).Error; err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}
	return perms, nil
}

// ─── Role-Permission assignment ───────────────────────────────────────────────

// AssignPermissionToRole adds a permission to a role.
func (s *RDBConfigStore) AssignPermissionToRole(ctx context.Context, roleID, permissionID string) error {
	rp := tables.TableRolePermission{RoleID: roleID, PermissionID: permissionID}
	return s.db.WithContext(ctx).
		Where("role_id = ? AND permission_id = ?", roleID, permissionID).
		FirstOrCreate(&rp).Error
}

// RevokePermissionFromRole removes a permission from a role.
func (s *RDBConfigStore) RevokePermissionFromRole(ctx context.Context, roleID, permissionID string) error {
	return s.db.WithContext(ctx).
		Where("role_id = ? AND permission_id = ?", roleID, permissionID).
		Delete(&tables.TableRolePermission{}).Error
}

// GetRolePermissions returns all permissions assigned to a role.
func (s *RDBConfigStore) GetRolePermissions(ctx context.Context, roleID string) ([]tables.TablePermission, error) {
	var perms []tables.TablePermission
	if err := s.db.WithContext(ctx).
		Joins("JOIN rbac_role_permissions rp ON rp.permission_id = rbac_permissions.id").
		Where("rp.role_id = ?", roleID).
		Find(&perms).Error; err != nil {
		return nil, fmt.Errorf("get role permissions for %s: %w", roleID, err)
	}
	return perms, nil
}

// ─── User-Role assignment ─────────────────────────────────────────────────────

// AssignRoleToUser grants a role to a user.
func (s *RDBConfigStore) AssignRoleToUser(ctx context.Context, userID, roleID, grantedBy string) error {
	ur := tables.TableUserRole{
		ID:        uuid.New().String(),
		UserID:    userID,
		RoleID:    roleID,
		GrantedBy: grantedBy,
		GrantedAt: time.Now().UTC(),
	}
	return s.db.WithContext(ctx).
		Where("user_id = ? AND role_id = ?", userID, roleID).
		FirstOrCreate(&ur).Error
}

// RevokeRoleFromUser removes a role assignment from a user.
func (s *RDBConfigStore) RevokeRoleFromUser(ctx context.Context, userID, roleID string) error {
	return s.db.WithContext(ctx).
		Where("user_id = ? AND role_id = ?", userID, roleID).
		Delete(&tables.TableUserRole{}).Error
}

// GetUserRoles returns all roles assigned to a user.
func (s *RDBConfigStore) GetUserRoles(ctx context.Context, userID string) ([]tables.TableRole, error) {
	var roles []tables.TableRole
	if err := s.db.WithContext(ctx).
		Joins("JOIN rbac_user_roles ur ON ur.role_id = rbac_roles.id").
		Where("ur.user_id = ?", userID).
		Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("get user roles for %s: %w", userID, err)
	}
	return roles, nil
}

// GetUserPermissions returns the flattened union of all permissions across a user's roles.
func (s *RDBConfigStore) GetUserPermissions(ctx context.Context, userID string) ([]tables.TablePermission, error) {
	var perms []tables.TablePermission
	if err := s.db.WithContext(ctx).
		Distinct("rbac_permissions.*").
		Joins("JOIN rbac_role_permissions rp ON rp.permission_id = rbac_permissions.id").
		Joins("JOIN rbac_user_roles ur ON ur.role_id = rp.role_id").
		Where("ur.user_id = ?", userID).
		Find(&perms).Error; err != nil {
		return nil, fmt.Errorf("get user permissions for %s: %w", userID, err)
	}
	return perms, nil
}
