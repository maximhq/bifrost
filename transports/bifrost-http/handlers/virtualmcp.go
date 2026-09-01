// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains Virtual MCP server management handlers.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// VirtualMCPHandler manages CRUD for Virtual MCP servers and their VK assignments.
type VirtualMCPHandler struct {
	store             *lib.Config
	client            *bifrost.Bifrost
	governanceManager GovernanceManager
	logger            schemas.Logger
}

// NewVirtualMCPHandler creates a Virtual MCP handler.
func NewVirtualMCPHandler(store *lib.Config, client *bifrost.Bifrost, governanceManager GovernanceManager, logger schemas.Logger) *VirtualMCPHandler {
	return &VirtualMCPHandler{store: store, client: client, governanceManager: governanceManager, logger: logger}
}

// RegisterRoutes registers the Virtual MCP routes.
func (h *VirtualMCPHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/mcp/virtual-mcps", lib.ChainMiddlewares(h.listVirtualMCPs, middlewares...))
	r.GET("/api/mcp/virtual-mcps/{id}", lib.ChainMiddlewares(h.getVirtualMCP, middlewares...))
	r.POST("/api/mcp/virtual-mcps", lib.ChainMiddlewares(h.createVirtualMCP, middlewares...))
	r.PUT("/api/mcp/virtual-mcps/{id}", lib.ChainMiddlewares(h.updateVirtualMCP, middlewares...))
	r.DELETE("/api/mcp/virtual-mcps/{id}", lib.ChainMiddlewares(h.deleteVirtualMCP, middlewares...))
	r.POST("/api/mcp/virtual-mcps/{id}/virtual-keys/{vkId}", lib.ChainMiddlewares(h.attachVirtualKey, middlewares...))
	r.DELETE("/api/mcp/virtual-mcps/{id}/virtual-keys/{vkId}", lib.ChainMiddlewares(h.detachVirtualKey, middlewares...))
}

// VirtualMCPRequest is the create/update body. EndpointSlug is honored only on create (immutable
// after); Enabled is a pointer so a false is not lost to the column default.
type VirtualMCPRequest struct {
	Name         string                          `json:"name"`
	EndpointSlug string                          `json:"endpoint_slug,omitempty"`
	Description  *string                         `json:"description,omitempty"`
	Enabled      *bool                           `json:"enabled,omitempty"`
	Tools        []configstoreTables.MCPToolSpec `json:"tools"`
}

// VirtualMCPResponse is a Virtual MCP with its VK assignments.
type VirtualMCPResponse struct {
	ID            uint                            `json:"id"`
	Name          string                          `json:"name"`
	EndpointSlug  string                          `json:"endpoint_slug"`
	Description   *string                         `json:"description,omitempty"`
	Enabled       bool                            `json:"enabled"`
	Tools         []configstoreTables.MCPToolSpec `json:"tools"`
	VirtualKeyIDs []string                        `json:"virtual_key_ids"`
	CreatedAt     string                          `json:"created_at"`
	UpdatedAt     string                          `json:"updated_at"`
}

func (h *VirtualMCPHandler) toResponse(ctx context.Context, def *configstoreTables.TableVirtualMCP) VirtualMCPResponse {
	tools := def.ParsedTools
	if tools == nil {
		tools = []configstoreTables.MCPToolSpec{}
	}
	vkIDs, err := h.store.ConfigStore.GetVirtualKeyIDsForVirtualMCP(ctx, def.ID)
	if err != nil {
		h.logger.Error("failed to load VK assignments for virtual MCP %d: %v", def.ID, err)
	}
	if vkIDs == nil {
		vkIDs = []string{}
	}
	return VirtualMCPResponse{
		ID:            def.ID,
		Name:          def.Name,
		EndpointSlug:  def.EndpointSlug,
		Description:   def.Description,
		Enabled:       def.Enabled,
		Tools:         tools,
		VirtualKeyIDs: vkIDs,
		CreatedAt:     def.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     def.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// validateToolSpecs rejects specs whose MCP client is not configured. Tool names are not checked
// here: a client's advertised set can change between save and use, so names are enforced at call time.
func (h *VirtualMCPHandler) validateToolSpecs(tools []configstoreTables.MCPToolSpec) string {
	if len(tools) == 0 {
		return ""
	}
	known := map[string]bool{}
	if h.client != nil {
		clients, err := h.client.GetMCPClients()
		if err != nil {
			h.logger.Error("failed to list MCP clients for validation: %v", err)
			return "could not validate MCP clients"
		}
		for _, c := range clients {
			if c.Config != nil {
				known[c.Config.ID] = true
				known[c.Config.Name] = true
			}
		}
	}
	var invalid []string
	for _, spec := range tools {
		if spec.MCPClientID == "" || !known[spec.MCPClientID] {
			invalid = append(invalid, spec.MCPClientID)
		}
	}
	if len(invalid) > 0 {
		return "invalid MCP client IDs: " + strings.Join(invalid, ", ")
	}
	return ""
}

func (h *VirtualMCPHandler) listVirtualMCPs(ctx *fasthttp.RequestCtx) {
	limit, _ := strconv.Atoi(string(ctx.QueryArgs().Peek("limit")))
	offset, _ := strconv.Atoi(string(ctx.QueryArgs().Peek("offset")))
	limit, offset = ClampPaginationParams(limit, offset)
	search := string(ctx.QueryArgs().Peek("search"))

	defs, total, err := h.store.ConfigStore.GetVirtualMCPsPaginated(ctx, configstore.VirtualMCPsQueryParams{
		Limit:  limit,
		Offset: offset,
		Search: search,
	})
	if err != nil {
		h.logger.Error("failed to list virtual MCPs: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	responses := make([]VirtualMCPResponse, len(defs))
	for i := range defs {
		responses[i] = h.toResponse(ctx, &defs[i])
	}
	SendJSON(ctx, map[string]any{
		"virtual_mcps": responses,
		"count":        len(responses),
		"total_count":  total,
		"limit":        limit,
		"offset":       offset,
	})
}

func (h *VirtualMCPHandler) getVirtualMCP(ctx *fasthttp.RequestCtx) {
	id, ok := h.parseID(ctx)
	if !ok {
		return
	}
	def, err := h.store.ConfigStore.GetVirtualMCPByID(ctx, id)
	if err != nil {
		h.sendLookupError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"virtual_mcp": h.toResponse(ctx, def)})
}

func (h *VirtualMCPHandler) createVirtualMCP(ctx *fasthttp.RequestCtx) {
	var req VirtualMCPRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "name is required")
		return
	}
	if msg := h.validateToolSpecs(req.Tools); msg != "" {
		SendError(ctx, fasthttp.StatusBadRequest, msg)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	def := &configstoreTables.TableVirtualMCP{
		Name:         req.Name,
		EndpointSlug: req.EndpointSlug,
		Description:  req.Description,
		Enabled:      enabled,
		ParsedTools:  req.Tools,
	}
	if err := h.store.ConfigStore.CreateVirtualMCP(ctx, def); err != nil {
		if code, msg, ok := virtualMCPConflict(err); ok {
			SendError(ctx, code, msg)
			return
		}
		h.logger.Error("failed to create virtual MCP: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	h.refreshDefinition(ctx, def.ID)
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]any{"virtual_mcp": h.toResponse(ctx, def)})
}

func (h *VirtualMCPHandler) updateVirtualMCP(ctx *fasthttp.RequestCtx) {
	id, ok := h.parseID(ctx)
	if !ok {
		return
	}
	var req VirtualMCPRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	def, err := h.store.ConfigStore.GetVirtualMCPByID(ctx, id)
	if err != nil {
		h.sendLookupError(ctx, err)
		return
	}
	if req.Tools != nil {
		if msg := h.validateToolSpecs(req.Tools); msg != "" {
			SendError(ctx, fasthttp.StatusBadRequest, msg)
			return
		}
		def.ParsedTools = req.Tools
	}
	if req.Name != "" {
		def.Name = req.Name
	}
	if req.Description != nil {
		def.Description = req.Description
	}
	if req.Enabled != nil {
		def.Enabled = *req.Enabled
	}
	// EndpointSlug is immutable: ignored even if the body carries one.
	if err := h.store.ConfigStore.UpdateVirtualMCP(ctx, def); err != nil {
		if code, msg, ok := virtualMCPConflict(err); ok {
			SendError(ctx, code, msg)
			return
		}
		h.logger.Error("failed to update virtual MCP: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	h.refreshDefinition(ctx, def.ID)
	SendJSON(ctx, map[string]any{"virtual_mcp": h.toResponse(ctx, def)})
}

func (h *VirtualMCPHandler) deleteVirtualMCP(ctx *fasthttp.RequestCtx) {
	id, ok := h.parseID(ctx)
	if !ok {
		return
	}
	if _, err := h.store.ConfigStore.GetVirtualMCPByID(ctx, id); err != nil {
		h.sendLookupError(ctx, err)
		return
	}
	if err := h.store.ConfigStore.DeleteVirtualMCP(ctx, id); err != nil {
		h.logger.Error("failed to delete virtual MCP: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	h.removeDefinition(ctx, id)
	SendJSON(ctx, map[string]any{"success": true})
}

func (h *VirtualMCPHandler) attachVirtualKey(ctx *fasthttp.RequestCtx) {
	id, ok := h.parseID(ctx)
	if !ok {
		return
	}
	vkID, _ := ctx.UserValue("vkId").(string)
	if vkID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "virtual key id is required")
		return
	}
	if _, err := h.store.ConfigStore.GetVirtualMCPByID(ctx, id); err != nil {
		h.sendLookupError(ctx, err)
		return
	}
	if _, err := h.store.ConfigStore.GetVirtualKey(ctx, vkID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "virtual key not found")
			return
		}
		h.logger.Error("failed to look up virtual key %s: %v", vkID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	if err := h.store.ConfigStore.AttachVirtualMCPToVirtualKey(ctx, id, vkID); err != nil {
		h.logger.Error("failed to attach virtual MCP %d to VK %s: %v", id, vkID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	h.refreshAttachment(ctx, vkID, id, true)
	SendJSON(ctx, map[string]any{"success": true})
}

func (h *VirtualMCPHandler) detachVirtualKey(ctx *fasthttp.RequestCtx) {
	id, ok := h.parseID(ctx)
	if !ok {
		return
	}
	vkID, _ := ctx.UserValue("vkId").(string)
	if vkID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "virtual key id is required")
		return
	}
	// Gate on the vMCP like attach does: no visibility (404 under DAC), no detach.
	if _, err := h.store.ConfigStore.GetVirtualMCPByID(ctx, id); err != nil {
		h.sendLookupError(ctx, err)
		return
	}
	if err := h.store.ConfigStore.DetachVirtualMCPFromVirtualKey(ctx, id, vkID); err != nil {
		h.logger.Error("failed to detach virtual MCP %d from VK %s: %v", id, vkID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	h.refreshAttachment(ctx, vkID, id, false)
	SendJSON(ctx, map[string]any{"success": true})
}

// The refresh helpers update the governance cache after a write. A failure is logged, not returned:
// the write already persisted, and the cache still rebuilds at startup.
func (h *VirtualMCPHandler) refreshDefinition(ctx context.Context, id uint) {
	if h.governanceManager == nil {
		return
	}
	if _, err := h.governanceManager.ReloadVirtualMCP(ctx, id); err != nil {
		h.logger.Error("failed to refresh virtual MCP %d in cache: %v", id, err)
	}
}

func (h *VirtualMCPHandler) removeDefinition(ctx context.Context, id uint) {
	if h.governanceManager == nil {
		return
	}
	if err := h.governanceManager.RemoveVirtualMCP(ctx, id); err != nil {
		h.logger.Error("failed to drop virtual MCP %d from cache: %v", id, err)
	}
}

func (h *VirtualMCPHandler) refreshAttachment(ctx context.Context, vkID string, id uint, attached bool) {
	if h.governanceManager == nil {
		return
	}
	var err error
	if attached {
		err = h.governanceManager.AttachVirtualMCPToVirtualKeyInMemory(ctx, vkID, id)
	} else {
		err = h.governanceManager.DetachVirtualMCPFromVirtualKeyInMemory(ctx, vkID, id)
	}
	if err != nil {
		h.logger.Error("failed to refresh virtual MCP %d / VK %s assignment in cache: %v", id, vkID, err)
	}
}

func (h *VirtualMCPHandler) parseID(ctx *fasthttp.RequestCtx) (uint, bool) {
	idStr, _ := ctx.UserValue("id").(string)
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid virtual MCP id")
		return 0, false
	}
	return uint(id), true
}

// virtualMCPConflict maps a uniqueness conflict to a 409 with a clear message: an explicit endpoint
// clash, or a name clash surfaced by the table's own unique index.
func virtualMCPConflict(err error) (int, string, bool) {
	switch {
	case errors.Is(err, configstore.ErrVirtualMCPEndpointExists):
		return fasthttp.StatusConflict, configstore.ErrVirtualMCPEndpointExists.Error(), true
	case IsUniqueConstraintError(err):
		return fasthttp.StatusConflict, "a virtual MCP with this name or endpoint already exists", true
	}
	return 0, "", false
}

func (h *VirtualMCPHandler) sendLookupError(ctx *fasthttp.RequestCtx, err error) {
	if errors.Is(err, configstore.ErrNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, "virtual MCP not found")
		return
	}
	h.logger.Error("failed to load virtual MCP: %v", err)
	SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
}
