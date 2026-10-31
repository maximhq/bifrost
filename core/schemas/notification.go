package schemas

import (
	"context"
	"time"
)

type NotificationSeverity string

const (
	NotificationSeverityInfo    NotificationSeverity = "info"
	NotificationSeveritySuccess NotificationSeverity = "success"
	NotificationSeverityWarning NotificationSeverity = "warning"
	NotificationSeverityError   NotificationSeverity = "error"
)

type NotificationAudience string

const (
	NotificationAudienceAll   NotificationAudience = "all"
	NotificationAudienceRoles NotificationAudience = "roles"
)

// Notification is a persisted dashboard notification. Read and dismissal
// state deliberately do not live here; those preferences are local to each UI.
type Notification struct {
	ID          string               `json:"id"`
	Audience    NotificationAudience `json:"audience"`
	RoleIDs     []uint               `json:"role_ids,omitempty"`
	Severity    NotificationSeverity `json:"severity"`
	Title       string               `json:"title"`
	Message     string               `json:"message"`
	ActionLabel string               `json:"action_label,omitempty"`
	ActionPath  string               `json:"action_path,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	ExpiresAt   time.Time            `json:"expires_at"`
}

// NotificationInput is accepted by the in-process publisher and the admin API.
// IDs and timestamps are always assigned by the notification service.
type NotificationInput struct {
	Audience    NotificationAudience `json:"audience"`
	RoleIDs     []uint               `json:"role_ids,omitempty"`
	Severity    NotificationSeverity `json:"severity"`
	Title       string               `json:"title"`
	Message     string               `json:"message"`
	ActionLabel string               `json:"action_label,omitempty"`
	ActionPath  string               `json:"action_path,omitempty"`
}

// NotificationPublisher persists a notification and pushes it to every
// connected dashboard the audience covers.
//
// This is an extension seam, not a feature with in-tree callers. Nothing in the
// OSS transport publishes a notification today; the field on lib.Config exists
// so enterprise builds and plugins can announce events (license expiry, cluster
// membership changes, budget breaches) without importing the handler package.
// The only other way to create one is POST /api/notifications, which is
// admin-gated. If you add the first in-tree producer, publish through this
// func rather than writing to the store directly, so the WebSocket fan-out and
// role scoping stay in one place.
type NotificationPublisher func(context.Context, NotificationInput) (*Notification, error)
