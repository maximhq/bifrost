package configstore

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// NotificationStore is intentionally a narrow optional interface so adding
// notifications does not force every ConfigStore test double to grow methods.
type NotificationStore interface {
	CreateNotification(ctx context.Context, notification *tables.TableNotification) error
	ListNotifications(ctx context.Context, before time.Time, beforeID string, limit int) ([]tables.TableNotification, error)
	DeleteExpiredNotifications(ctx context.Context, now time.Time) (int64, error)
}

func (s *RDBConfigStore) CreateNotification(ctx context.Context, notification *tables.TableNotification) error {
	return s.DB().WithContext(ctx).Create(notification).Error
}

func (s *RDBConfigStore) ListNotifications(ctx context.Context, before time.Time, beforeID string, limit int) ([]tables.TableNotification, error) {
	if limit <= 0 {
		limit = 50
	}
	query := s.DB().WithContext(ctx).Where("expires_at > ?", time.Now().UTC())
	if !before.IsZero() {
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", before, before, beforeID)
	}
	var notifications []tables.TableNotification
	err := query.Order("created_at DESC").Order("id DESC").Limit(limit).Find(&notifications).Error
	return notifications, err
}

func (s *RDBConfigStore) DeleteExpiredNotifications(ctx context.Context, now time.Time) (int64, error) {
	result := s.DB().WithContext(ctx).Where("expires_at <= ?", now).Delete(&tables.TableNotification{})
	return result.RowsAffected, result.Error
}
