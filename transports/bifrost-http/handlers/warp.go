package handlers

import (
	"errors"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// WarpHandler is the HTTP face of the Warp service. It parses requests, maps
// service errors to status codes and writes responses; everything Warp actually
// does lives in framework/warp.
type WarpHandler struct {
	service *warp.Service
}

// NewWarpHandler builds the handler and the service behind it. A nil logManager
// is a supported deployment (logging disabled): Warp then serves only its
// configuration routes, because its tools would have nothing to read.
func NewWarpHandler(store configstore.ConfigStore, logManager logging.LogManager, logger schemas.Logger) *WarpHandler {
	opts := []warp.Option{warp.WithLogger(logger)}
	if logManager != nil {
		opts = append(opts, warp.WithLogReader(warpLogReader{logManager}))
	}
	return &WarpHandler{service: warp.NewService(store, opts...)}
}

// Shutdown releases the service's model client.
func (h *WarpHandler) Shutdown() {
	h.service.Shutdown()
}

// Service exposes the underlying service to in-process callers.
func (h *WarpHandler) Service() *warp.Service {
	return h.service
}

// RegisterRoutes wires the configuration API. Every route goes through the
// standard middleware chain: Warp reads the deployment's own telemetry, so it
// must never be reachable on an unauthenticated route the way the OAuth2
// issuance handler deliberately is.
func (h *WarpHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/warp/config", lib.ChainMiddlewares(h.getConfig, middlewares...))
	r.PUT("/api/warp/config", lib.ChainMiddlewares(h.putConfig, middlewares...))

	// The chat route exists only where Warp has data to read and a way to reach
	// a model; see Service.CanChat for why absent beats always-failing.
	if h.service.CanChat() {
		r.POST("/api/warp/chat", lib.ChainMiddlewares(h.chat, middlewares...))
	}

	// History rides on the same middleware chain. Every route resolves its owner
	// from the request context, so an unauthenticated deployment shares one
	// history and an authenticated one gives each person their own, with no
	// second code path between them.
	if h.service.HasHistory() {
		r.GET("/api/warp/conversations", lib.ChainMiddlewares(h.listConversations, middlewares...))
		r.GET("/api/warp/conversations/{id}", lib.ChainMiddlewares(h.getConversation, middlewares...))
		r.DELETE("/api/warp/conversations/{id}", lib.ChainMiddlewares(h.deleteConversation, middlewares...))
	}
}

// getConfig serves the settings page. It is safe for any authenticated caller
// because the stored credential is never part of the response.
func (h *WarpHandler) getConfig(ctx *fasthttp.RequestCtx) {
	view, err := h.service.ConfigView(ctx)
	if errors.Is(err, warp.ErrUnavailable) {
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		logger.Warn("failed to read warp configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read warp configuration")
		return
	}
	SendJSON(ctx, view)
}

// putConfig is admin-only, on the same reasoning notifications.go applies to
// publishing: RBAC in this transport is enterprise-only and path-based, so the
// OSS floor is enforced in the handler. A single PUT plants a credential that
// the server will then use to make outbound calls, which is not something an
// ordinary dashboard user should be able to do.
func (h *WarpHandler) putConfig(ctx *fasthttp.RequestCtx) {
	if localAdmin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool); !localAdmin {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can configure Warp")
		return
	}
	var input warp.ConfigInput
	if err := sonic.Unmarshal(ctx.PostBody(), &input); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	view, err := h.service.SaveConfig(ctx, &input)
	switch {
	case errors.Is(err, warp.ErrUnavailable):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, warp.ErrInvalidConfig):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	case err != nil:
		// Log the cause. The client gets a generic message because a raw driver
		// error can name columns and constraints, but swallowing it entirely
		// leaves an operator staring at a 500 with nothing to act on - which is
		// exactly what a schema drift between the table and the row struct
		// produces.
		logger.Warn("failed to save warp configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to save warp configuration")
		return
	}
	SendJSON(ctx, view)
}
