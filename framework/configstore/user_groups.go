package configstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ─── User Group CRUD ──────────────────────────────────────────────────────────

// CreateUserGroup inserts a new user group.
func (s *RDBConfigStore) CreateUserGroup(ctx context.Context, group *tables.TableUserGroup) error {
	if group.ID == "" {
		group.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	group.CreatedAt = now
	group.UpdatedAt = now
	if err := s.db.WithContext(ctx).Create(group).Error; err != nil {
		return fmt.Errorf("create user group: %w", err)
	}
	return nil
}

// GetUserGroup returns a group by ID.
func (s *RDBConfigStore) GetUserGroup(ctx context.Context, id string) (*tables.TableUserGroup, error) {
	var g tables.TableUserGroup
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&g).Error; err != nil {
		return nil, fmt.Errorf("get user group %s: %w", id, err)
	}
	return &g, nil
}

// ListUserGroups returns all user groups.
func (s *RDBConfigStore) ListUserGroups(ctx context.Context) ([]tables.TableUserGroup, error) {
	var groups []tables.TableUserGroup
	if err := s.db.WithContext(ctx).Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("list user groups: %w", err)
	}
	return groups, nil
}

// UpdateUserGroup applies a partial update to a group.
func (s *RDBConfigStore) UpdateUserGroup(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&tables.TableUserGroup{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("update user group %s: %w", id, err)
	}
	return nil
}

// DeleteUserGroup deletes a group and cascade-removes all member / VK / MCP assignments.
func (s *RDBConfigStore) DeleteUserGroup(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", id).Delete(&tables.TableUserGroupMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&tables.TableUserGroupVirtualKey{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&tables.TableUserGroupMCPGroup{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&tables.TableUserGroup{}).Error
	})
}

// UpsertUserGroup creates or updates a group keyed on ExternalID (for SCIM sync).
func (s *RDBConfigStore) UpsertUserGroup(ctx context.Context, group *tables.TableUserGroup) (*tables.TableUserGroup, error) {
	if group.ExternalID == "" {
		return nil, fmt.Errorf("upsert user group: ExternalID is required")
	}
	var existing tables.TableUserGroup
	err := s.db.WithContext(ctx).
		Where("external_id = ?", group.ExternalID).
		First(&existing).Error
	if err == nil {
		group.ID = existing.ID
		group.CreatedAt = existing.CreatedAt
		group.UpdatedAt = time.Now().UTC()
		now := time.Now().UTC()
		group.SyncedAt = &now
		if err2 := s.db.WithContext(ctx).Save(group).Error; err2 != nil {
			return nil, fmt.Errorf("update user group for external_id %s: %w", group.ExternalID, err2)
		}
		return group, nil
	}
	if err2 := s.CreateUserGroup(ctx, group); err2 != nil {
		return nil, err2
	}
	return group, nil
}

// FindUserGroupByExternalID looks up a group by the IdP external ID.
func (s *RDBConfigStore) FindUserGroupByExternalID(ctx context.Context, externalID string) (*tables.TableUserGroup, error) {
	var g tables.TableUserGroup
	if err := s.db.WithContext(ctx).Where("external_id = ?", externalID).First(&g).Error; err != nil {
		return nil, fmt.Errorf("find user group by external_id %s: %w", externalID, err)
	}
	return &g, nil
}

// ─── Member management ────────────────────────────────────────────────────────

// AddUserToGroup adds a user to a group.
func (s *RDBConfigStore) AddUserToGroup(ctx context.Context, groupID, userID, addedBy string) error {
	m := tables.TableUserGroupMember{
		ID:      uuid.New().String(),
		GroupID: groupID,
		UserID:  userID,
		AddedBy: addedBy,
		AddedAt: time.Now().UTC(),
	}
	return s.db.WithContext(ctx).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		FirstOrCreate(&m).Error
}

// RemoveUserFromGroup removes a user from a group.
func (s *RDBConfigStore) RemoveUserFromGroup(ctx context.Context, groupID, userID string) error {
	return s.db.WithContext(ctx).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Delete(&tables.TableUserGroupMember{}).Error
}

// GetUserGroups returns all groups a user belongs to.
func (s *RDBConfigStore) GetUserGroups(ctx context.Context, userID string) ([]tables.TableUserGroup, error) {
	var groups []tables.TableUserGroup
	if err := s.db.WithContext(ctx).
		Joins("JOIN user_group_members m ON m.group_id = user_groups.id").
		Where("m.user_id = ?", userID).
		Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("get groups for user %s: %w", userID, err)
	}
	return groups, nil
}

// GetUserGroupMembers returns all members of a group.
func (s *RDBConfigStore) GetUserGroupMembers(ctx context.Context, groupID string) ([]tables.TableUserGroupMember, error) {
	var members []tables.TableUserGroupMember
	if err := s.db.WithContext(ctx).
		Where("group_id = ?", groupID).
		Find(&members).Error; err != nil {
		return nil, fmt.Errorf("get members of group %s: %w", groupID, err)
	}
	return members, nil
}

// ─── VirtualKey assignment ────────────────────────────────────────────────────

// AssignVirtualKeyToGroup assigns a virtual key to a group.
func (s *RDBConfigStore) AssignVirtualKeyToGroup(ctx context.Context, groupID, vkID string, budgetOverride *float64) error {
	gvk := tables.TableUserGroupVirtualKey{
		ID:             uuid.New().String(),
		GroupID:        groupID,
		VirtualKeyID:   vkID,
		BudgetOverride: budgetOverride,
	}
	return s.db.WithContext(ctx).
		Where("group_id = ? AND virtual_key_id = ?", groupID, vkID).
		FirstOrCreate(&gvk).Error
}

// UnassignVirtualKeyFromGroup removes a VK assignment from a group.
func (s *RDBConfigStore) UnassignVirtualKeyFromGroup(ctx context.Context, groupID, vkID string) error {
	return s.db.WithContext(ctx).
		Where("group_id = ? AND virtual_key_id = ?", groupID, vkID).
		Delete(&tables.TableUserGroupVirtualKey{}).Error
}

// GetUserGroupVirtualKeys returns all VK assignments for a group.
func (s *RDBConfigStore) GetUserGroupVirtualKeys(ctx context.Context, groupID string) ([]tables.TableUserGroupVirtualKey, error) {
	var gvks []tables.TableUserGroupVirtualKey
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&gvks).Error; err != nil {
		return nil, fmt.Errorf("get VKs for group %s: %w", groupID, err)
	}
	return gvks, nil
}

// ─── MCP Group assignment ─────────────────────────────────────────────────────

// AssignMCPGroupToUserGroup links an MCP tool group to a user group.
func (s *RDBConfigStore) AssignMCPGroupToUserGroup(ctx context.Context, groupID, mcpGroupID string) error {
	link := tables.TableUserGroupMCPGroup{
		ID:         uuid.New().String(),
		GroupID:    groupID,
		MCPGroupID: mcpGroupID,
	}
	return s.db.WithContext(ctx).
		Where("group_id = ? AND mcp_group_id = ?", groupID, mcpGroupID).
		FirstOrCreate(&link).Error
}

// UnassignMCPGroupFromUserGroup removes the link.
func (s *RDBConfigStore) UnassignMCPGroupFromUserGroup(ctx context.Context, groupID, mcpGroupID string) error {
	return s.db.WithContext(ctx).
		Where("group_id = ? AND mcp_group_id = ?", groupID, mcpGroupID).
		Delete(&tables.TableUserGroupMCPGroup{}).Error
}

// GetUserGroupMCPGroups returns MCP links for a user group.
func (s *RDBConfigStore) GetUserGroupMCPGroups(ctx context.Context, groupID string) ([]tables.TableUserGroupMCPGroup, error) {
	var links []tables.TableUserGroupMCPGroup
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&links).Error; err != nil {
		return nil, fmt.Errorf("get MCP groups for user group %s: %w", groupID, err)
	}
	return links, nil
}
