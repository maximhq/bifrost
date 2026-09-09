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
