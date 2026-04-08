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

// ConnectorsHandler handles data connector CRUD endpoints.
type ConnectorsHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewConnectorsHandler creates a new data connector handler.
func NewConnectorsHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *ConnectorsHandler {
	return &ConnectorsHandler{store: store, rbac: rbac}
}

// RegisterRoutes wires all connector HTTP endpoints.
func (h *ConnectorsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)

	r.POST("/api/enterprise/connectors", lib.ChainMiddlewares(h.createConnector, adminMW...))
	r.GET("/api/enterprise/connectors", lib.ChainMiddlewares(h.listConnectors, adminMW...))
	r.GET("/api/enterprise/connectors/{id}", lib.ChainMiddlewares(h.getConnector, adminMW...))
	r.PUT("/api/enterprise/connectors/{id}", lib.ChainMiddlewares(h.updateConnector, adminMW...))
	r.DELETE("/api/enterprise/connectors/{id}", lib.ChainMiddlewares(h.deleteConnector, adminMW...))
	r.POST("/api/enterprise/connectors/{id}/test", lib.ChainMiddlewares(h.testConnector, adminMW...))
}

func (h *ConnectorsHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureDataConnectors) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "connectors feature not included in current license")
		return false
	}
	return true
}

func (h *ConnectorsHandler) createConnector(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var c tables.TableConnector
	if err := json.Unmarshal(ctx.PostBody(), &c); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.CreateConnector(ctx, &c); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, c, fasthttp.StatusCreated)
}

func (h *ConnectorsHandler) listConnectors(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	connType := string(ctx.QueryArgs().Peek("type"))
	connectors, err := h.store.ListConnectors(ctx, connType)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, connectors)
}

func (h *ConnectorsHandler) getConnector(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	c, err := h.store.GetConnector(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	SendJSON(ctx, c)
}

func (h *ConnectorsHandler) updateConnector(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateConnector(ctx, id, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "updated"})
}

func (h *ConnectorsHandler) deleteConnector(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	if err := h.store.DeleteConnector(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

func (h *ConnectorsHandler) testConnector(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	c, err := h.store.GetConnector(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	// Stub: perform type-specific connectivity test.
	// The full implementation delegates to pkg/connectors based on c.Type.
	testOK := c.Enabled
	if err := h.store.MarkConnectorTested(ctx, id, testOK); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"ok": testOK, "connector_id": id})
}
