package configstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ─── MCP Tool Group CRUD ──────────────────────────────────────────────────────

func (s *RDBConfigStore) CreateMCPToolGroup(ctx context.Context, g *tables.TableMCPToolGroup) error {
	if g.ID == "" {
		g.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(g).Error
}

func (s *RDBConfigStore) GetMCPToolGroup(ctx context.Context, id string) (*tables.TableMCPToolGroup, error) {
	var g tables.TableMCPToolGroup
	if err := s.db.WithContext(ctx).First(&g, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *RDBConfigStore) ListMCPToolGroups(ctx context.Context) ([]tables.TableMCPToolGroup, error) {
	var groups []tables.TableMCPToolGroup
	return groups, s.db.WithContext(ctx).Order("name").Find(&groups).Error
}

func (s *RDBConfigStore) UpdateMCPToolGroup(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&tables.TableMCPToolGroup{}).Where("id = ?", id).Updates(updates).Error
}

func (s *RDBConfigStore) DeleteMCPToolGroup(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", id).Delete(&tables.TableMCPToolGroupMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&tables.TableVirtualKeyMCPGroup{}).Error; err != nil {
			return err
		}
		// Also clean up user_group_mcp_groups (references the same GroupID field)
		if err := tx.Where("mcp_group_id = ?", id).Delete(&tables.TableUserGroupMCPGroup{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&tables.TableMCPToolGroup{}).Error
	})
}

// ─── MCP Tool Group Member CRUD ───────────────────────────────────────────────

func (s *RDBConfigStore) AddMCPToolGroupMember(ctx context.Context, groupID, clientID, toolName string) error {
	m := tables.TableMCPToolGroupMember{
		ID:       uuid.New().String(),
		GroupID:  groupID,
		ClientID: clientID,
		ToolName: toolName,
	}
	return s.db.WithContext(ctx).Create(&m).Error
}

func (s *RDBConfigStore) RemoveMCPToolGroupMember(ctx context.Context, memberID string) error {
	return s.db.WithContext(ctx).Where("id = ?", memberID).Delete(&tables.TableMCPToolGroupMember{}).Error
}

func (s *RDBConfigStore) GetMCPToolGroupMembers(ctx context.Context, groupID string) ([]tables.TableMCPToolGroupMember, error) {
	var members []tables.TableMCPToolGroupMember
	return members, s.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&members).Error
}

// ─── VK ↔ MCP Tool Group assignment ──────────────────────────────────────────

func (s *RDBConfigStore) AssignVirtualKeyMCPGroup(ctx context.Context, vkID, groupID string) error {
	link := tables.TableVirtualKeyMCPGroup{
		VirtualKeyID: vkID,
		GroupID:      groupID,
		AssignedAt:   time.Now(),
	}
	return s.db.WithContext(ctx).
		Where("virtual_key_id = ? AND group_id = ?", vkID, groupID).
		FirstOrCreate(&link).Error
}

func (s *RDBConfigStore) UnassignVirtualKeyMCPGroup(ctx context.Context, vkID, groupID string) error {
	return s.db.WithContext(ctx).
		Where("virtual_key_id = ? AND group_id = ?", vkID, groupID).
		Delete(&tables.TableVirtualKeyMCPGroup{}).Error
}

func (s *RDBConfigStore) GetVirtualKeyMCPToolGroups(ctx context.Context, vkID string) ([]tables.TableMCPToolGroup, error) {
	var links []tables.TableVirtualKeyMCPGroup
	if err := s.db.WithContext(ctx).Where("virtual_key_id = ?", vkID).Find(&links).Error; err != nil {
		return nil, err
	}
	groupIDs := make([]string, len(links))
	for i, l := range links {
		groupIDs[i] = l.GroupID
	}
	var groups []tables.TableMCPToolGroup
	return groups, s.db.WithContext(ctx).Where("id IN ?", groupIDs).Find(&groups).Error
}
