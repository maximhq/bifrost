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
	ResolveNotifications(ctx context.Context, subjectType, subjectID string, resolvedAt time.Time, resolution string) ([]tables.TableNotification, error)
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

// ResolveNotifications stamps every unresolved notification for the subject and
// returns the rows it changed. Each row is claimed by a guarded update, so when
// two calls race only the one whose update lands returns it, and the first
// outcome is kept.
func (s *RDBConfigStore) ResolveNotifications(ctx context.Context, subjectType, subjectID string, resolvedAt time.Time, resolution string) ([]tables.TableNotification, error) {
	db := s.DB().WithContext(ctx)
	var open []tables.TableNotification
	if err := db.Where("subject_type = ? AND subject_id = ? AND resolved_at IS NULL", subjectType, subjectID).
		Order("created_at DESC").Find(&open).Error; err != nil {
		return nil, err
	}
	resolved := make([]tables.TableNotification, 0, len(open))
	for _, row := range open {
		result := db.Model(&tables.TableNotification{}).
			Where("id = ? AND resolved_at IS NULL", row.ID).
			Updates(map[string]any{"resolved_at": resolvedAt, "resolution": resolution})
		if result.Error != nil {
			return resolved, result.Error
		}
		if result.RowsAffected == 1 {
			row.ResolvedAt, row.Resolution = &resolvedAt, resolution
			resolved = append(resolved, row)
		}
	}
	return resolved, nil
}
