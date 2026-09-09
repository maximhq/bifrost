package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	notificationRetention = 30 * 24 * time.Hour
	notificationPageSize  = 50
	maxNotificationScan   = 250
	// Ceiling on rows read across a whole list request, not just one store call.
	// Role visibility cannot be pushed into the query - TableNotification.RoleIDs
	// is a JSON-serialised text column and no containment predicate is portable
	// across sqlite, postgres, and mysql - so a caller whose role matches nothing
	// would otherwise walk the entire table looking for a page it will never
	// fill. Four store calls is the budget; past that the request returns what it
	// has plus a cursor to resume from.
	maxNotificationScanTotal = 4 * maxNotificationScan
)

var ErrNotificationsUnavailable = errors.New("notification storage is unavailable")

type NotificationService struct {
	store     configstore.NotificationStore
	websocket *WebSocketHandler
	now       func() time.Time
}

func NewNotificationService(store configstore.ConfigStore, websocket *WebSocketHandler) *NotificationService {
	notificationStore, _ := store.(configstore.NotificationStore)
	return &NotificationService{store: notificationStore, websocket: websocket, now: func() time.Time { return time.Now().UTC() }}
}

func (s *NotificationService) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/notifications", lib.ChainMiddlewares(s.list, middlewares...))
	r.POST("/api/notifications", lib.ChainMiddlewares(s.create, middlewares...))
}

func (s *NotificationService) Start(ctx context.Context) {
	if s.store == nil {
		return
	}
	_, _ = s.store.DeleteExpiredNotifications(ctx, s.now())
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.store.DeleteExpiredNotifications(ctx, s.now()); err != nil {
					logger.Warn("failed to prune expired notifications: %v", err)
				}
			}
		}
	}()
}

func (s *NotificationService) Publish(ctx context.Context, input schemas.NotificationInput) (*schemas.Notification, error) {
	if s.store == nil {
		return nil, ErrNotificationsUnavailable
	}
	if err := validateNotificationInput(&input); err != nil {
		return nil, err
	}
	now := s.now()
	notification := &schemas.Notification{
		ID: uuid.NewString(), Audience: input.Audience, RoleIDs: append([]uint(nil), input.RoleIDs...),
		Severity: input.Severity, Title: input.Title, Message: input.Message,
		ActionLabel: input.ActionLabel, ActionPath: input.ActionPath,
		CreatedAt: now, ExpiresAt: now.Add(notificationRetention),
	}
	row := notificationToTable(notification)
	if err := s.store.CreateNotification(ctx, &row); err != nil {
		return nil, fmt.Errorf("persist notification: %w", err)
	}
	if s.websocket != nil {
		s.websocket.BroadcastNotification(notification)
	}
	return notification, nil
}

// validateNotificationInput rejects bad input without rewriting good input: what
// a caller sends is what gets stored. Emptiness is judged on the trimmed value
// so a whitespace-only title cannot pass, while the length limits deliberately
// measure the raw value, because that is what has to fit the varchar columns on
// TableNotification. RoleIDs is the one field still normalised, since sorting
// and compacting a set changes no content and keeps the stored audience sane.
func validateNotificationInput(input *schemas.NotificationInput) error {
	if strings.TrimSpace(input.Title) == "" || len(input.Title) > 160 {
		return errors.New("title is required and must not exceed 160 characters")
	}
	if strings.TrimSpace(input.Message) == "" || len(input.Message) > 4000 {
		return errors.New("message is required and must not exceed 4000 characters")
	}
	if !slices.Contains([]schemas.NotificationSeverity{schemas.NotificationSeverityInfo, schemas.NotificationSeveritySuccess, schemas.NotificationSeverityWarning, schemas.NotificationSeverityError}, input.Severity) {
		return errors.New("severity must be one of info, success, warning, or error")
	}
	switch input.Audience {
	case schemas.NotificationAudienceAll:
		if len(input.RoleIDs) != 0 {
			return errors.New("role_ids must be empty when audience is all")
		}
	case schemas.NotificationAudienceRoles:
		if len(input.RoleIDs) == 0 {
			return errors.New("role_ids is required when audience is roles")
		}
	default:
		return errors.New("audience must be all or roles")
	}
	hasActionLabel := strings.TrimSpace(input.ActionLabel) != ""
	hasActionPath := strings.TrimSpace(input.ActionPath) != ""
	if hasActionLabel != hasActionPath {
		return errors.New("action_label and action_path must be provided together")
	}
	if hasActionPath {
		// Parsed as sent rather than as trimmed: a padded path would be stored
		// padded, so it has to clear this check in the form it will be stored in.
		parsed, err := url.Parse(input.ActionPath)
		// A backslash is rejected outright: browsers fold "\" to "/" when
		// resolving a location, so "/\evil.example" clears the "//" check here
		// and still navigates off-site. Path is percent-decoded, so a "%5C"
		// spelling of the same trick lands in this check too.
		if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, `\`) {
			return errors.New("action_path must be an internal absolute path")
		}
	}
	sort.Slice(input.RoleIDs, func(i, j int) bool { return input.RoleIDs[i] < input.RoleIDs[j] })
	input.RoleIDs = slices.Compact(input.RoleIDs)
	return nil
}

// create is the admin half of the API. list is safe for any authenticated user
// because it only ever returns rows the caller's role is already entitled to
// see, but publishing is the opposite: a single POST reaches every dashboard
// user on the deployment. RBAC in this transport is enterprise-only and
// path-based, so the floor is enforced here instead - only the local admin (and
// therefore also an auth-disabled deployment, where the middleware marks every
// request local admin) may publish. In-process producers call Publish directly
// and are intentionally not subject to this check.
func (s *NotificationService) create(ctx *fasthttp.RequestCtx) {
	if localAdmin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool); !localAdmin {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can publish notifications")
		return
	}
	var input schemas.NotificationInput
	if err := sonic.Unmarshal(ctx.PostBody(), &input); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	notification, err := s.Publish(ctx, input)
	if err != nil {
		status := fasthttp.StatusBadRequest
		if errors.Is(err, ErrNotificationsUnavailable) || strings.HasPrefix(err.Error(), "persist notification:") {
			status = fasthttp.StatusServiceUnavailable
		}
		SendError(ctx, status, err.Error())
		return
	}
	SendJSONWithStatus(ctx, notification, fasthttp.StatusCreated)
}

type notificationListResponse struct {
	Notifications []schemas.Notification `json:"notifications"`
	NextCursor    string                 `json:"next_cursor,omitempty"`
}

func (s *NotificationService) list(ctx *fasthttp.RequestCtx) {
	if s.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, ErrNotificationsUnavailable.Error())
		return
	}
	limit := notificationPageSize
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			SendError(ctx, fasthttp.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(parsed, notificationPageSize)
	}
	before, beforeID, err := decodeNotificationCursor(string(ctx.QueryArgs().Peek("cursor")))
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid cursor")
		return
	}
	roleID, hasRole := notificationRoleID(ctx)
	localAdmin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool)
	result := make([]schemas.Notification, 0, limit+1)
	scanned := 0
	// True when the scan budget stopped us with rows still unread behind us.
	budgetExhausted := false
	for len(result) < limit+1 {
		rows, listErr := s.store.ListNotifications(ctx, before, beforeID, maxNotificationScan)
		if listErr != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to list notifications")
			return
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			n := notificationFromTable(&rows[i])
			if localAdmin || notificationVisibleToRole(&n, roleID, hasRole) {
				result = append(result, n)
				if len(result) == limit+1 {
					break
				}
			}
		}
		scanned += len(rows)
		last := rows[len(rows)-1]
		before, beforeID = last.CreatedAt, last.ID
		if len(rows) < maxNotificationScan {
			break
		}
		if scanned >= maxNotificationScanTotal {
			budgetExhausted = true
			break
		}
	}
	response := notificationListResponse{Notifications: result}
	switch {
	case len(result) > limit:
		response.Notifications = result[:limit]
		last := response.Notifications[len(response.Notifications)-1]
		response.NextCursor = encodeNotificationCursor(last.CreatedAt, last.ID)
	case budgetExhausted:
		// The page is short because the budget cut the scan, not because the feed
		// ended. The cursor has to come from the last row *scanned* rather than
		// the last one returned, or the caller would re-read everything the scan
		// already skipped. Without a cursor here a short page is indistinguishable
		// from the end of the feed and the rest of the feed is silently lost.
		response.NextCursor = encodeNotificationCursor(before, beforeID)
	}
	SendJSON(ctx, response)
}

func notificationRoleID(ctx *fasthttp.RequestCtx) (uint, bool) {
	switch value := ctx.UserValue(schemas.BifrostContextKeyUserRoleID).(type) {
	case uint:
		return value, value != 0
	case int:
		return uint(value), value > 0
	default:
		return 0, false
	}
}

func notificationVisibleToRole(notification *schemas.Notification, roleID uint, hasRole bool) bool {
	if notification.Audience == schemas.NotificationAudienceAll {
		return true
	}
	return hasRole && slices.Contains(notification.RoleIDs, roleID)
}

func encodeNotificationCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdAt.UnixNano(), 10) + ":" + id))
}

func decodeNotificationCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	nanos, id, ok := strings.Cut(string(decoded), ":")
	if !ok || id == "" {
		return time.Time{}, "", errors.New("malformed cursor")
	}
	value, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, "", err
	}
	return time.Unix(0, value).UTC(), id, nil
}

func notificationToTable(notification *schemas.Notification) tables.TableNotification {
	return tables.TableNotification{ID: notification.ID, Audience: string(notification.Audience), RoleIDs: notification.RoleIDs, Severity: string(notification.Severity), Title: notification.Title, Message: notification.Message, ActionLabel: notification.ActionLabel, ActionPath: notification.ActionPath, CreatedAt: notification.CreatedAt, ExpiresAt: notification.ExpiresAt}
}

func notificationFromTable(row *tables.TableNotification) schemas.Notification {
	return schemas.Notification{ID: row.ID, Audience: schemas.NotificationAudience(row.Audience), RoleIDs: row.RoleIDs, Severity: schemas.NotificationSeverity(row.Severity), Title: row.Title, Message: row.Message, ActionLabel: row.ActionLabel, ActionPath: row.ActionPath, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
}
