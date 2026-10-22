package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CallerHandler serves GET /api/session/me: who the request acts for and what
// it may see. It resolves the caller the same way Warp's tools are scoped, so
// an API or MCP client learns the identity and reach Warp would assume for it.
type CallerHandler struct {
	resolve warp.CallerRestrictionResolver
}

// NewCallerHandler returns the handler. resolve is the deployment's row-level
// access control, nil when it has none.
func NewCallerHandler(resolve warp.CallerRestrictionResolver) *CallerHandler {
	return &CallerHandler{resolve: resolve}
}

// CallerResponse describes the caller of a request.
type CallerResponse struct {
	// Identified reports that the request acts for a known user.
	Identified bool   `json:"identified"`
	UserID     string `json:"user_id,omitempty"`
	// Unrestricted reports that nothing narrows what the caller may read, so
	// everything they see is the whole deployment.
	Unrestricted bool `json:"unrestricted"`
	// Visibility says, in a few words, which slice a restricted caller may
	// read, when the deployment can say.
	Visibility string `json:"visibility,omitempty"`
}

// RegisterRoutes registers GET /api/session/me.
func (h *CallerHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/session/me", lib.ChainMiddlewares(h.getCaller, middlewares...))
}

func (h *CallerHandler) getCaller(ctx *fasthttp.RequestCtx) {
	scope := warp.CallerScope(ctx, h.resolve)
	SendJSON(ctx, CallerResponse{
		Identified:   scope.HasIdentity,
		UserID:       scope.UserID,
		Unrestricted: scope.Unrestricted,
		Visibility:   scope.Visibility,
	})
}
