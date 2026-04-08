package handlers

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/valyala/fasthttp"
)

// RBACMiddleware provides per-endpoint RBAC enforcement.
// It reads the session token from context (set by AuthMiddleware), resolves the
// user's roles from the config store, and checks whether any role has the
// requested permission.
//
// When the license feature "rbac" is disabled, the middleware is a no-op so
// existing single-admin setups continue working.
type RBACMiddleware struct {
	store configstore.ConfigStore
}

// NewRBACMiddleware creates a new RBAC middleware backed by the given config store.
func NewRBACMiddleware(store configstore.ConfigStore) *RBACMiddleware {
	return &RBACMiddleware{store: store}
}

// RequirePermission returns a Bifrost HTTP middleware that enforces a specific
// permission (resource:action) on the caller.
//
// Short-circuit conditions:
//  - RBAC feature disabled → pass through (no-op)
//  - Session token missing → 401 Unauthorized
//  - User has no role with the needed permission → 403 Forbidden
func (m *RBACMiddleware) RequirePermission(permissionID string) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Feature gate — no license → no RBAC enforcement.
			if !license.IsFeatureEnabled(license.FeatureRBAC) {
				next(ctx)
				return
			}

			// Resolve the caller's user ID from session token stored by AuthMiddleware.
			userID := m.resolveUserID(ctx)
			if userID == "" {
				SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
				return
			}

			// Check permission.
			allowed, err := m.hasPermission(ctx, userID, permissionID)
			if err != nil {
				logger.Warn("RBAC permission check failed for user %s: %v", userID, err)
				// Fail closed on error.
				SendError(ctx, fasthttp.StatusForbidden, "Forbidden")
				return
			}
			if !allowed {
				SendError(ctx, fasthttp.StatusForbidden, "Insufficient permissions: "+permissionID)
				return
			}

			next(ctx)
		}
	}
}

// RequireRole returns a middleware that enforces that the caller has (at minimum)
// the named role. Role precedence: viewer < operator < admin < super_admin.
func (m *RBACMiddleware) RequireRole(minRole string) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if !license.IsFeatureEnabled(license.FeatureRBAC) {
				next(ctx)
				return
			}

			userID := m.resolveUserID(ctx)
			if userID == "" {
				SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
				return
			}

			hasRole, err := m.hasMinRole(ctx, userID, minRole)
			if err != nil {
				logger.Warn("RBAC role check failed for user %s: %v", userID, err)
				SendError(ctx, fasthttp.StatusForbidden, "Forbidden")
				return
			}
			if !hasRole {
				SendError(ctx, fasthttp.StatusForbidden, "Insufficient role: need "+minRole)
				return
			}

			next(ctx)
		}
	}
}

// resolveUserID extracts the user ID from context.
// For SSO users the user ID is stored as BifrostContextKeyExternalUserID.
// For admin sessions it falls back to the session token itself as the identity.
func (m *RBACMiddleware) resolveUserID(ctx *fasthttp.RequestCtx) string {
	if uid, ok := ctx.UserValue("bifrost-external-user-id").(string); ok && uid != "" {
		return uid
	}
	// Fallback: use session token as stable identity for the built-in admin.
	if token, ok := ctx.UserValue(schemas.BifrostContextKeySessionToken).(string); ok && token != "" {
		return token
	}
	return ""
}

// hasPermission returns true if the user has a role that includes permissionID.
func (m *RBACMiddleware) hasPermission(ctx context.Context, userID, permissionID string) (bool, error) {
	perms, err := m.store.GetUserPermissions(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, p := range perms {
		if p.ID == permissionID {
			return true, nil
		}
	}
	return false, nil
}

var roleRanks = map[string]int{
	"viewer":      1,
	"operator":    2,
	"admin":       3,
	"super_admin": 4,
}

// hasMinRole returns true if the user's highest role meets or exceeds minRole.
func (m *RBACMiddleware) hasMinRole(ctx context.Context, userID, minRole string) (bool, error) {
	minRank, ok := roleRanks[minRole]
	if !ok {
		return false, nil
	}

	roles, err := m.store.GetUserRoles(ctx, userID)
	if err != nil {
		return false, err
	}

	for _, r := range roles {
		if rank, ok := roleRanks[r.Name]; ok && rank >= minRank {
			return true, nil
		}
	}
	return false, nil
}
