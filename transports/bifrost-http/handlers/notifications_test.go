package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type recordingNotificationStore struct {
	created []tables.TableNotification
	rows    []tables.TableNotification
}

func (s *recordingNotificationStore) CreateNotification(_ context.Context, notification *tables.TableNotification) error {
	s.created = append(s.created, *notification)
	return nil
}

func (s *recordingNotificationStore) ListNotifications(context.Context, time.Time, string, int) ([]tables.TableNotification, error) {
	return s.rows, nil
}

func TestNotificationListFiltersByRole(t *testing.T) {
	now := time.Now().UTC()
	store := &recordingNotificationStore{rows: []tables.TableNotification{
		{ID: "global", Audience: "all", Severity: "info", Title: "Global", Message: "message", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "matching", Audience: "roles", RoleIDs: []uint{7}, Severity: "info", Title: "Matching", Message: "message", CreatedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Hour)},
		{ID: "hidden", Audience: "roles", RoleIDs: []uint{8}, Severity: "info", Title: "Hidden", Message: "message", CreatedAt: now.Add(-2 * time.Second), ExpiresAt: now.Add(time.Hour)},
	}}
	service := &NotificationService{store: store, now: func() time.Time { return now }}
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyUserRoleID, uint(7))
	service.list(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var response notificationListResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Len(t, response.Notifications, 2)
	require.Equal(t, "global", response.Notifications[0].ID)
	require.Equal(t, "matching", response.Notifications[1].ID)
}

func (s *recordingNotificationStore) DeleteExpiredNotifications(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestNotificationServicePublishValidatesAndPersists(t *testing.T) {
	store := &recordingNotificationStore{}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	service := &NotificationService{store: store, now: func() time.Time { return now }}

	notification, err := service.Publish(context.Background(), schemas.NotificationInput{
		Audience: schemas.NotificationAudienceRoles, RoleIDs: []uint{7, 2, 7}, Severity: schemas.NotificationSeverityWarning,
		Title: "  Provider needs attention  ", Message: " Check its credentials. ", ActionLabel: "  Open provider  ", ActionPath: "/workspace/providers  ",
	})
	require.NoError(t, err)
	require.Equal(t, []uint{2, 7}, notification.RoleIDs)
	// Padding survives: validation judges the trimmed value but never writes it
	// back, so what the caller sent is what gets stored.
	require.Equal(t, "  Provider needs attention  ", notification.Title)
	require.Equal(t, " Check its credentials. ", notification.Message)
	require.Equal(t, "  Open provider  ", notification.ActionLabel)
	require.Equal(t, "/workspace/providers  ", notification.ActionPath)
	require.Equal(t, now.Add(notificationRetention), notification.ExpiresAt)
	require.Len(t, store.created, 1)
	require.Equal(t, notification.ID, store.created[0].ID)
	require.Equal(t, "  Provider needs attention  ", store.created[0].Title)
	require.Equal(t, " Check its credentials. ", store.created[0].Message)
	require.Equal(t, "  Open provider  ", store.created[0].ActionLabel)
	require.Equal(t, "/workspace/providers  ", store.created[0].ActionPath)

	// A title that is only whitespace is still rejected.
	_, err = service.Publish(context.Background(), schemas.NotificationInput{
		Audience: schemas.NotificationAudienceAll, Severity: schemas.NotificationSeverityInfo, Title: "   ", Message: "Blank title",
	})
	require.ErrorContains(t, err, "title is required")
	require.Len(t, store.created, 1)

	_, err = service.Publish(context.Background(), schemas.NotificationInput{
		Audience: schemas.NotificationAudienceRoles, Severity: schemas.NotificationSeverityInfo, Title: "Invalid", Message: "No roles",
	})
	require.ErrorContains(t, err, "role_ids is required")
	require.Len(t, store.created, 1)

	_, err = service.Publish(context.Background(), schemas.NotificationInput{
		Audience: schemas.NotificationAudienceAll, Severity: schemas.NotificationSeverityInfo, Title: "Invalid", Message: "External", ActionLabel: "Open", ActionPath: "https://example.com",
	})
	require.ErrorContains(t, err, "internal absolute path")

	// A backslash reads as a path separator to the browser, so these resolve
	// off-site despite clearing the "//" check.
	for _, path := range []string{`/\evil.example`, `/%5Cevil.example`, `/workspace\..\..\evil`} {
		_, err = service.Publish(context.Background(), schemas.NotificationInput{
			Audience: schemas.NotificationAudienceAll, Severity: schemas.NotificationSeverityInfo, Title: "Invalid", Message: "Backslash", ActionLabel: "Open", ActionPath: path,
		})
		require.ErrorContainsf(t, err, "internal absolute path", "action_path %q must be rejected", path)
	}
	require.Len(t, store.created, 1)
}

// endlessNotificationStore always hands back a full batch of rows no caller can
// see, which is the shape that used to walk the whole table: the outer loop kept
// asking for more because it never filled a page.
type endlessNotificationStore struct{ calls int }

func (s *endlessNotificationStore) CreateNotification(context.Context, *tables.TableNotification) error {
	return nil
}

func (s *endlessNotificationStore) DeleteExpiredNotifications(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (s *endlessNotificationStore) ListNotifications(_ context.Context, before time.Time, _ string, limit int) ([]tables.TableNotification, error) {
	s.calls++
	if before.IsZero() {
		before = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	}
	rows := make([]tables.TableNotification, limit)
	for i := range rows {
		rows[i] = tables.TableNotification{
			ID: fmt.Sprintf("call-%d-row-%d", s.calls, i), Audience: "roles", RoleIDs: []uint{99},
			Severity: "info", Title: "Invisible", Message: "message",
			CreatedAt: before.Add(-time.Duration(i+1) * time.Second), ExpiresAt: before.Add(time.Hour),
		}
	}
	return rows, nil
}

func TestNotificationListStopsAtScanBudget(t *testing.T) {
	store := &endlessNotificationStore{}
	service := &NotificationService{store: store, now: time.Now}
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyUserRoleID, uint(7))
	service.list(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Equal(t, maxNotificationScanTotal/maxNotificationScan, store.calls, "scan must stop at the request-wide budget")

	var response notificationListResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Empty(t, response.Notifications)
	// A short page cut by the budget must still carry a cursor, or the caller
	// reads it as the end of the feed and loses everything behind it.
	require.NotEmpty(t, response.NextCursor)

	_, cursorID, err := decodeNotificationCursor(response.NextCursor)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("call-%d-row-%d", store.calls, maxNotificationScan-1), cursorID, "cursor must resume from the last row scanned")
}

func TestNotificationCreateRequiresLocalAdmin(t *testing.T) {
	store := &recordingNotificationStore{}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	service := &NotificationService{store: store, now: func() time.Time { return now }}
	body, err := sonic.Marshal(schemas.NotificationInput{
		Audience: schemas.NotificationAudienceAll, Severity: schemas.NotificationSeverityInfo, Title: "Broadcast", Message: "To everyone",
	})
	require.NoError(t, err)

	// A non-admin role is authenticated but must not be able to reach every
	// dashboard on the deployment.
	nonAdmin := &fasthttp.RequestCtx{}
	nonAdmin.SetUserValue(schemas.BifrostContextKeyUserRoleID, uint(7))
	nonAdmin.Request.SetBody(body)
	service.create(nonAdmin)
	require.Equal(t, fasthttp.StatusForbidden, nonAdmin.Response.StatusCode())
	require.Empty(t, store.created)

	admin := &fasthttp.RequestCtx{}
	admin.SetUserValue(schemas.IsLocalAdminContextKey, true)
	admin.Request.SetBody(body)
	service.create(admin)
	require.Equal(t, fasthttp.StatusCreated, admin.Response.StatusCode())
	require.Len(t, store.created, 1)
}

func TestNotificationVisibleToRole(t *testing.T) {
	require.True(t, notificationVisibleToRole(&schemas.Notification{Audience: schemas.NotificationAudienceAll}, 0, false))
	require.True(t, notificationVisibleToRole(&schemas.Notification{Audience: schemas.NotificationAudienceRoles, RoleIDs: []uint{2, 7}}, 7, true))
	require.False(t, notificationVisibleToRole(&schemas.Notification{Audience: schemas.NotificationAudienceRoles, RoleIDs: []uint{2, 7}}, 3, true))
	require.False(t, notificationVisibleToRole(&schemas.Notification{Audience: schemas.NotificationAudienceRoles, RoleIDs: []uint{2, 7}}, 0, false))
}

func TestNotificationCursorRoundTrip(t *testing.T) {
	wantTime := time.Date(2026, 8, 17, 12, 0, 0, 123456789, time.UTC)
	wantID := "notification-id"
	gotTime, gotID, err := decodeNotificationCursor(encodeNotificationCursor(wantTime, wantID))
	require.NoError(t, err)
	require.Equal(t, wantTime, gotTime)
	require.Equal(t, wantID, gotID)
}
