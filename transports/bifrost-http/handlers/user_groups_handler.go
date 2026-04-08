package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// UserGroupHandler manages HTTP endpoints for user group management.
type UserGroupHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewUserGroupHandler creates a new user group handler.
func NewUserGroupHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *UserGroupHandler {
	return &UserGroupHandler{store: store, rbac: rbac}
}

// RegisterRoutes registers user group management endpoints.
func (h *UserGroupHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)
	superMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("super_admin")}, middlewares...)

	r.GET("/api/user-groups", lib.ChainMiddlewares(h.listGroups, adminMW...))
	r.POST("/api/user-groups", lib.ChainMiddlewares(h.createGroup, adminMW...))
	r.GET("/api/user-groups/{group_id}", lib.ChainMiddlewares(h.getGroup, adminMW...))
	r.PUT("/api/user-groups/{group_id}", lib.ChainMiddlewares(h.updateGroup, adminMW...))
	r.DELETE("/api/user-groups/{group_id}", lib.ChainMiddlewares(h.deleteGroup, superMW...))

	r.GET("/api/user-groups/{group_id}/members", lib.ChainMiddlewares(h.listMembers, adminMW...))
	r.POST("/api/user-groups/{group_id}/members", lib.ChainMiddlewares(h.addMember, adminMW...))
	r.DELETE("/api/user-groups/{group_id}/members/{user_id}", lib.ChainMiddlewares(h.removeMember, adminMW...))

	r.GET("/api/user-groups/{group_id}/virtual-keys", lib.ChainMiddlewares(h.listVirtualKeys, adminMW...))
	r.POST("/api/user-groups/{group_id}/virtual-keys", lib.ChainMiddlewares(h.assignVirtualKey, adminMW...))
	r.DELETE("/api/user-groups/{group_id}/virtual-keys/{vk_id}", lib.ChainMiddlewares(h.unassignVirtualKey, adminMW...))

	r.GET("/api/user-groups/{group_id}/mcp-groups", lib.ChainMiddlewares(h.listMCPGroups, adminMW...))
	r.POST("/api/user-groups/{group_id}/mcp-groups", lib.ChainMiddlewares(h.assignMCPGroup, adminMW...))
	r.DELETE("/api/user-groups/{group_id}/mcp-groups/{mcp_group_id}", lib.ChainMiddlewares(h.unassignMCPGroup, adminMW...))

	r.GET("/api/users/{user_id}/groups", lib.ChainMiddlewares(h.getUserGroups, adminMW...))
}

func (h *UserGroupHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureUserGroups) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "user_groups feature not included in current license")
		return false
	}
	return true
}

// GET /api/user-groups
func (h *UserGroupHandler) listGroups(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groups, err := h.store.ListUserGroups(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list groups: %v", err))
		return
	}
	SendJSON(ctx, groups)
}

// POST /api/user-groups
func (h *UserGroupHandler) createGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	var group tables.TableUserGroup
	if err := json.Unmarshal(ctx.PostBody(), &group); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if err := h.store.CreateUserGroup(ctx, &group); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to create group: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, group)
}

// GET /api/user-groups/{group_id}
func (h *UserGroupHandler) getGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	group, err := h.store.GetUserGroup(ctx, groupID)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, "group not found")
		return
	}
	SendJSON(ctx, group)
}

// PUT /api/user-groups/{group_id}
func (h *UserGroupHandler) updateGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.UpdateUserGroup(ctx, groupID, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update group: %v", err))
		return
	}
	group, _ := h.store.GetUserGroup(ctx, groupID)
	SendJSON(ctx, group)
}

// DELETE /api/user-groups/{group_id}
func (h *UserGroupHandler) deleteGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	if err := h.store.DeleteUserGroup(ctx, groupID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete group: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "group deleted"})
}

// GET /api/user-groups/{group_id}/members
func (h *UserGroupHandler) listMembers(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	members, err := h.store.GetUserGroupMembers(ctx, groupID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list members: %v", err))
		return
	}
	SendJSON(ctx, members)
}

// POST /api/user-groups/{group_id}/members
func (h *UserGroupHandler) addMember(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	addedBy, _ := ctx.UserValue(schemas.BifrostContextKeySessionToken).(string)
	if err := h.store.AddUserToGroup(ctx, groupID, req.UserID, addedBy); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to add member: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]string{"message": "member added"})
}

// DELETE /api/user-groups/{group_id}/members/{user_id}
func (h *UserGroupHandler) removeMember(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	userID := ctx.UserValue("user_id").(string)
	if err := h.store.RemoveUserFromGroup(ctx, groupID, userID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to remove member: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "member removed"})
}

// GET /api/user-groups/{group_id}/virtual-keys
func (h *UserGroupHandler) listVirtualKeys(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	vks, err := h.store.GetUserGroupVirtualKeys(ctx, groupID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list virtual keys: %v", err))
		return
	}
	SendJSON(ctx, vks)
}

// POST /api/user-groups/{group_id}/virtual-keys
func (h *UserGroupHandler) assignVirtualKey(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	var req struct {
		VirtualKeyID   string   `json:"virtual_key_id"`
		BudgetOverride *float64 `json:"budget_override,omitempty"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.AssignVirtualKeyToGroup(ctx, groupID, req.VirtualKeyID, req.BudgetOverride); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to assign virtual key: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]string{"message": "virtual key assigned"})
}

// DELETE /api/user-groups/{group_id}/virtual-keys/{vk_id}
func (h *UserGroupHandler) unassignVirtualKey(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	vkID := ctx.UserValue("vk_id").(string)
	if err := h.store.UnassignVirtualKeyFromGroup(ctx, groupID, vkID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to unassign virtual key: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "virtual key unassigned"})
}

// GET /api/user-groups/{group_id}/mcp-groups
func (h *UserGroupHandler) listMCPGroups(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	links, err := h.store.GetUserGroupMCPGroups(ctx, groupID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list MCP groups: %v", err))
		return
	}
	SendJSON(ctx, links)
}

// POST /api/user-groups/{group_id}/mcp-groups
func (h *UserGroupHandler) assignMCPGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	var req struct {
		MCPGroupID string `json:"mcp_group_id"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.AssignMCPGroupToUserGroup(ctx, groupID, req.MCPGroupID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to assign MCP group: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]string{"message": "MCP group assigned"})
}

// DELETE /api/user-groups/{group_id}/mcp-groups/{mcp_group_id}
func (h *UserGroupHandler) unassignMCPGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	groupID := ctx.UserValue("group_id").(string)
	mcpGroupID := ctx.UserValue("mcp_group_id").(string)
	if err := h.store.UnassignMCPGroupFromUserGroup(ctx, groupID, mcpGroupID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to unassign MCP group: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "MCP group unassigned"})
}

// GET /api/users/{user_id}/groups
func (h *UserGroupHandler) getUserGroups(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	userID := ctx.UserValue("user_id").(string)
	groups, err := h.store.GetUserGroups(ctx, userID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get user groups: %v", err))
		return
	}
	SendJSON(ctx, groups)
}
