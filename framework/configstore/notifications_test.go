package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestNotificationStoreLifecycle(t *testing.T) {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableNotification{}))
	now := time.Now().UTC()
	ctx := context.Background()

	for _, notification := range []tables.TableNotification{
		{ID: "older", Audience: "all", Severity: "info", Title: "Older", Message: "message", CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
		{ID: "newer", Audience: "roles", RoleIDs: []uint{2, 7}, Severity: "warning", Title: "Newer", Message: "message", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "expired", Audience: "all", Severity: "info", Title: "Expired", Message: "message", CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
	} {
		require.NoError(t, store.CreateNotification(ctx, &notification))
	}

	rows, err := store.ListNotifications(ctx, time.Time{}, "", 50)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "newer", rows[0].ID)
	require.Equal(t, []uint{2, 7}, rows[0].RoleIDs)

	rows, err = store.ListNotifications(ctx, rows[0].CreatedAt, rows[0].ID, 50)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "older", rows[0].ID)

	deleted, err := store.DeleteExpiredNotifications(ctx, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
}

func TestResolveNotificationsBySubject(t *testing.T) {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableNotification{}))
	now := time.Now().UTC()
	ctx := context.Background()

	for _, notification := range []tables.TableNotification{
		{ID: "target", Audience: "all", Severity: "info", Title: "Target", Message: "m", SubjectType: "budget_request", SubjectID: "req-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "other-id", Audience: "all", Severity: "info", Title: "Other", Message: "m", SubjectType: "budget_request", SubjectID: "req-2", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "other-type", Audience: "all", Severity: "info", Title: "Other", Message: "m", SubjectType: "job", SubjectID: "req-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "untagged", Audience: "all", Severity: "info", Title: "Untagged", Message: "m", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		require.NoError(t, store.CreateNotification(ctx, &notification))
	}

	resolved, err := store.ResolveNotifications(ctx, "budget_request", "req-1", now, "Approved by Ada")
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Equal(t, "target", resolved[0].ID)
	require.NotNil(t, resolved[0].ResolvedAt)
	require.Equal(t, "Approved by Ada", resolved[0].Resolution)

	// A second resolve finds nothing open and keeps the first outcome.
	resolved, err = store.ResolveNotifications(ctx, "budget_request", "req-1", now.Add(time.Minute), "Denied by Bob")
	require.NoError(t, err)
	require.Empty(t, resolved)

	rows, err := store.ListNotifications(ctx, time.Time{}, "", 50)
	require.NoError(t, err)
	for _, row := range rows {
		if row.ID == "target" {
			require.Equal(t, "Approved by Ada", row.Resolution)
		} else {
			require.Nil(t, row.ResolvedAt, "row %s must stay open", row.ID)
		}
	}
}

func TestMigrationAddNotificationSubjectResolutionColumns(t *testing.T) {
	store := setupRDBTestStore(t)
	db := store.DB()
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&tables.TableNotification{}))
	require.NoError(t, db.Migrator().DropIndex(&tables.TableNotification{}, "idx_notifications_subject"))
	for _, column := range notificationSubjectResolutionColumns {
		require.NoError(t, db.Migrator().DropColumn(&tables.TableNotification{}, column))
	}

	require.NoError(t, migrationAddNotificationSubjectResolutionColumns(ctx, db, testMigrationLogger))
	require.NoError(t, migrationAddNotificationSubjectIndex(ctx, db, testMigrationLogger))
	require.NoError(t, migrationAddNotificationSubjectResolutionColumns(ctx, db, testMigrationLogger))
	require.NoError(t, migrationAddNotificationSubjectIndex(ctx, db, testMigrationLogger))
	for _, column := range notificationSubjectResolutionColumns {
		require.True(t, db.Migrator().HasColumn(&tables.TableNotification{}, column), column)
	}
	require.True(t, db.Migrator().HasIndex(&tables.TableNotification{}, "idx_notifications_subject"))
}
