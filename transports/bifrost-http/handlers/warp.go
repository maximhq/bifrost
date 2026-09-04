package handlers

import (
	"errors"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// WarpHandler is the HTTP face of the Warp service. It parses requests, maps
// service errors to status codes and writes responses; everything Warp actually
// does lives in framework/warp.
type WarpHandler struct {
	service         *warp.Service
	unsubscribeLogs func()
}

// NewWarpLogReader adapts a log manager to what Warp reads through. Exported so
// the server can rebind it when the logging plugin is reloaded.
func NewWarpLogReader(manager logging.LogManager) warp.LogReader {
	if manager == nil {
		return nil
	}
	return warpLogReader{manager}
}

// NewWarpHandler builds the handler and the service behind it.
//
// A nil loggerPlugin is a supported deployment (logging disabled): Warp then
// serves only its configuration routes, because its tools would have nothing to
// read. A nil catalog is likewise supported and simply leaves Warp's own spend
// unpriced. logsStore is separate from loggerPlugin because the two answer
// different questions - the plugin is what Warp researches through, the store
// is where it files what was said - and a deployment can have the store without
// the plugin.
func NewWarpHandler(store configstore.ConfigStore, loggerPlugin *logging.LoggerPlugin, client *bifrost.Bifrost, logsStore logstore.LogStore, vectors vectorstore.VectorStore, catalog *modelcatalog.ModelCatalog, logger schemas.Logger) *WarpHandler {
	opts := []warp.Option{warp.WithLogger(logger), warp.WithModelCatalog(catalog), warp.WithVectorStore(vectors)}
	if client != nil {
		opts = append(opts, warp.WithEmbeddingExecutor(client.EmbeddingRequest))
	}
	if loggerPlugin != nil {
		opts = append(opts, warp.WithLogReader(warpLogReader{loggerPlugin.GetPluginLogManager()}))
	}
	if logsStore != nil {
		opts = append(opts, warp.WithConversationStore(logsStore))
	}
	handler := &WarpHandler{service: warp.NewService(store, opts...)}
	if loggerPlugin != nil {
		handler.unsubscribeLogs = loggerPlugin.SubscribeLogCallback(handler.service.IndexLog)
	}
	// Retention runs on a timer rather than on write: what expires is age, so a
	// deployment nobody has chatted on for a month is exactly where a
	// write-triggered sweep would never fire. It is a no-op without a history
	// store, and Shutdown stops it.
	handler.service.StartHistoryCleanup()
	return handler
}

// Shutdown releases the service's model client.
func (h *WarpHandler) Shutdown() {
	if h.unsubscribeLogs != nil {
		h.unsubscribeLogs()
		h.unsubscribeLogs = nil
	}
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

	// Registered unconditionally, and 503 while Warp cannot answer.
	//
	// This runs once, at startup. Gating the route on CanChat() meant a
	// deployment that turned logging on afterwards kept answering 405 for the
	// life of the process, with nothing to say the feature had become available.
	// The 503 body carries a machine-readable reason, which is what keeps
	// "present but unusable" distinguishable from "absent" - the concern the
	// gate was there for in the first place.
	r.POST("/api/warp/chat", lib.ChainMiddlewares(h.chat, middlewares...))

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
	case errors.Is(err, warp.ErrNoVectorStore):
		h.sendUnavailable(ctx, schemas.WarpUnavailableNoVectorStore, err.Error())
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
