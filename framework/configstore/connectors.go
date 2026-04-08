package configstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ─── Connector CRUD ───────────────────────────────────────────────────────────

func (s *RDBConfigStore) CreateConnector(ctx context.Context, c *tables.TableConnector) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(c).Error
}

func (s *RDBConfigStore) GetConnector(ctx context.Context, id string) (*tables.TableConnector, error) {
	var c tables.TableConnector
	if err := s.db.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *RDBConfigStore) ListConnectors(ctx context.Context, connType string) ([]tables.TableConnector, error) {
	var connectors []tables.TableConnector
	q := s.db.WithContext(ctx).Order("name")
	if connType != "" {
		q = q.Where("type = ?", connType)
	}
	return connectors, q.Find(&connectors).Error
}

func (s *RDBConfigStore) UpdateConnector(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&tables.TableConnector{}).Where("id = ?", id).Updates(updates).Error
}

func (s *RDBConfigStore) DeleteConnector(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TableConnector{}).Error
}

func (s *RDBConfigStore) MarkConnectorTested(ctx context.Context, id string, ok bool) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&tables.TableConnector{}).Where("id = ?", id).Updates(map[string]any{
		"last_tested_at": now,
		"last_test_ok":   ok,
		"updated_at":     now,
	}).Error
}
