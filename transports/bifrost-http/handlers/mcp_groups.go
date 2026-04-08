package handlers

import (
	"encoding/json"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MCPGroupsHandler handles MCP tool group CRUD endpoints.
type MCPGroupsHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewMCPGroupsHandler creates a new MCP tool group handler.
func NewMCPGroupsHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *MCPGroupsHandler {
	return &MCPGroupsHandler{store: store, rbac: rbac}
}

// RegisterRoutes wires all MCP tool group HTTP endpoints.
func (h *MCPGroupsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)

	r.POST("/api/enterprise/mcp-groups", lib.ChainMiddlewares(h.createGroup, adminMW...))
	r.GET("/api/enterprise/mcp-groups", lib.ChainMiddlewares(h.listGroups, adminMW...))
	r.GET("/api/enterprise/mcp-groups/{id}", lib.ChainMiddlewares(h.getGroup, adminMW...))
	r.PUT("/api/enterprise/mcp-groups/{id}", lib.ChainMiddlewares(h.updateGroup, adminMW...))
	r.DELETE("/api/enterprise/mcp-groups/{id}", lib.ChainMiddlewares(h.deleteGroup, adminMW...))

	r.POST("/api/enterprise/mcp-groups/{id}/members", lib.ChainMiddlewares(h.addMember, adminMW...))
	r.DELETE("/api/enterprise/mcp-groups/{id}/members/{memberId}", lib.ChainMiddlewares(h.removeMember, adminMW...))

	r.POST("/api/enterprise/mcp-groups/{id}/virtual-keys/{vkId}", lib.ChainMiddlewares(h.assignVK, adminMW...))
	r.DELETE("/api/enterprise/mcp-groups/{id}/virtual-keys/{vkId}", lib.ChainMiddlewares(h.unassignVK, adminMW...))
}

func (h *MCPGroupsHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureMCPToolGroups) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "mcp_tool_groups feature not included in current license")
		return false
	}
	return true
}

func (h *MCPGroupsHandler) createGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var g tables.TableMCPToolGroup
	if err := json.Unmarshal(ctx.PostBody(), &g); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.CreateMCPToolGroup(ctx, &g); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, g, fasthttp.StatusCreated)
}

func (h *MCPGroupsHandler) listGroups(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	groups, err := h.store.ListMCPToolGroups(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, groups)
}

func (h *MCPGroupsHandler) getGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	g, err := h.store.GetMCPToolGroup(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	members, _ := h.store.GetMCPToolGroupMembers(ctx, id)
	SendJSON(ctx, map[string]any{"group": g, "members": members})
}

func (h *MCPGroupsHandler) updateGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateMCPToolGroup(ctx, id, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "updated"})
}

func (h *MCPGroupsHandler) deleteGroup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	if err := h.store.DeleteMCPToolGroup(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

func (h *MCPGroupsHandler) addMember(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	groupID := ctx.UserValue("id").(string)
	var body struct {
		ClientID string `json:"client_id"`
		ToolName string `json:"tool_name"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.AddMCPToolGroupMember(ctx, groupID, body.ClientID, body.ToolName); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, map[string]any{"status": "added"}, fasthttp.StatusCreated)
}

func (h *MCPGroupsHandler) removeMember(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	memberID := ctx.UserValue("memberId").(string)
	if err := h.store.RemoveMCPToolGroupMember(ctx, memberID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "removed"})
}

func (h *MCPGroupsHandler) assignVK(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	groupID := ctx.UserValue("id").(string)
	vkID := ctx.UserValue("vkId").(string)
	if err := h.store.AssignVirtualKeyMCPGroup(ctx, vkID, groupID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "assigned"})
}

func (h *MCPGroupsHandler) unassignVK(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	groupID := ctx.UserValue("id").(string)
	vkID := ctx.UserValue("vkId").(string)
	if err := h.store.UnassignVirtualKeyMCPGroup(ctx, vkID, groupID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "unassigned"})
}
