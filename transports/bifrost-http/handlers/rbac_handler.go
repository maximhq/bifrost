package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// RBACHandler manages HTTP endpoints for RBAC role and permission management.
type RBACHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewRBACHandler creates a new RBAC handler.
func NewRBACHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *RBACHandler {
	return &RBACHandler{store: store, rbac: rbac}
}

// RegisterRoutes registers RBAC management endpoints.
func (h *RBACHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)
	superMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("super_admin")}, middlewares...)

	r.GET("/api/rbac/roles", lib.ChainMiddlewares(h.listRoles, adminMW...))
	r.POST("/api/rbac/roles", lib.ChainMiddlewares(h.createRole, superMW...))
	r.GET("/api/rbac/roles/{role_id}", lib.ChainMiddlewares(h.getRole, adminMW...))
	r.PUT("/api/rbac/roles/{role_id}", lib.ChainMiddlewares(h.updateRole, superMW...))
	r.DELETE("/api/rbac/roles/{role_id}", lib.ChainMiddlewares(h.deleteRole, superMW...))
	r.GET("/api/rbac/roles/{role_id}/permissions", lib.ChainMiddlewares(h.getRolePermissions, adminMW...))
	r.POST("/api/rbac/roles/{role_id}/permissions", lib.ChainMiddlewares(h.assignPermission, superMW...))
	r.DELETE("/api/rbac/roles/{role_id}/permissions/{perm_id}", lib.ChainMiddlewares(h.revokePermission, superMW...))

	r.GET("/api/rbac/permissions", lib.ChainMiddlewares(h.listPermissions, adminMW...))

	r.GET("/api/rbac/users/{user_id}/roles", lib.ChainMiddlewares(h.getUserRoles, adminMW...))
	r.POST("/api/rbac/users/{user_id}/roles", lib.ChainMiddlewares(h.assignUserRole, superMW...))
	r.DELETE("/api/rbac/users/{user_id}/roles/{role_id}", lib.ChainMiddlewares(h.revokeUserRole, superMW...))
}

func (h *RBACHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureRBAC) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "rbac feature not included in current license")
		return false
	}
	return true
}

// GET /api/rbac/roles
func (h *RBACHandler) listRoles(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	roles, err := h.store.ListRoles(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list roles: %v", err))
		return
	}
	SendJSON(ctx, roles)
}

// POST /api/rbac/roles
func (h *RBACHandler) createRole(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	var role tables.TableRole
	if err := json.Unmarshal(ctx.PostBody(), &role); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	role.ID = uuid.New().String()
	role.IsSystem = false
	if err := h.store.CreateRole(ctx, &role); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to create role: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, role)
}

// GET /api/rbac/roles/{role_id}
func (h *RBACHandler) getRole(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	roleID := ctx.UserValue("role_id").(string)
	role, err := h.store.GetRole(ctx, roleID)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("role not found: %v", err))
		return
	}
	SendJSON(ctx, role)
}

// PUT /api/rbac/roles/{role_id}
func (h *RBACHandler) updateRole(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	roleID := ctx.UserValue("role_id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.UpdateRole(ctx, roleID, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update role: %v", err))
		return
	}
	role, _ := h.store.GetRole(ctx, roleID)
	SendJSON(ctx, role)
}

// DELETE /api/rbac/roles/{role_id}
func (h *RBACHandler) deleteRole(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	roleID := ctx.UserValue("role_id").(string)
	if err := h.store.DeleteRole(ctx, roleID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete role: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "role deleted"})
}

// GET /api/rbac/roles/{role_id}/permissions
func (h *RBACHandler) getRolePermissions(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	roleID := ctx.UserValue("role_id").(string)
	perms, err := h.store.GetRolePermissions(ctx, roleID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get permissions: %v", err))
		return
	}
	SendJSON(ctx, perms)
}

// POST /api/rbac/roles/{role_id}/permissions
func (h *RBACHandler) assignPermission(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	roleID := ctx.UserValue("role_id").(string)
	var req struct {
		PermissionID string `json:"permission_id"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.AssignPermissionToRole(ctx, roleID, req.PermissionID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to assign permission: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]string{"message": "permission assigned"})
}

// DELETE /api/rbac/roles/{role_id}/permissions/{perm_id}
func (h *RBACHandler) revokePermission(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	roleID := ctx.UserValue("role_id").(string)
	permID := ctx.UserValue("perm_id").(string)
	if err := h.store.RevokePermissionFromRole(ctx, roleID, permID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to revoke permission: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "permission revoked"})
}

// GET /api/rbac/permissions
func (h *RBACHandler) listPermissions(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	perms, err := h.store.ListPermissions(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list permissions: %v", err))
		return
	}
	SendJSON(ctx, perms)
}

// GET /api/rbac/users/{user_id}/roles
func (h *RBACHandler) getUserRoles(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	userID := ctx.UserValue("user_id").(string)
	roles, err := h.store.GetUserRoles(ctx, userID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get user roles: %v", err))
		return
	}
	SendJSON(ctx, roles)
}

// POST /api/rbac/users/{user_id}/roles
func (h *RBACHandler) assignUserRole(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	userID := ctx.UserValue("user_id").(string)
	var req struct {
		RoleID string `json:"role_id"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	grantedBy, _ := ctx.UserValue(schemas.BifrostContextKeySessionToken).(string)
	if err := h.store.AssignRoleToUser(ctx, userID, req.RoleID, grantedBy); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to assign role: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]string{"message": "role assigned"})
}

// DELETE /api/rbac/users/{user_id}/roles/{role_id}
func (h *RBACHandler) revokeUserRole(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	userID := ctx.UserValue("user_id").(string)
	roleID := ctx.UserValue("role_id").(string)
	if err := h.store.RevokeRoleFromUser(ctx, userID, roleID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to revoke role: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "role revoked"})
}
