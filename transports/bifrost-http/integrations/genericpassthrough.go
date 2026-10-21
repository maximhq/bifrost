package integrations

import (
	"regexp"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/generic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// genericPassthroughPrefix is the route every generic custom provider is reached under:
// /passthrough/{provider}/{path:*}. The provider segment is resolved per request because custom
// providers are added and removed at runtime while routes are registered once.
const genericPassthroughPrefix = "/passthrough"

// genericProviderNameRe bounds the provider segment before any lookup happens.
var genericProviderNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// GenericPassthroughRouter relays requests to custom providers whose base type is generic. Only
// such providers are served: a standard provider, a custom provider of another base type, or an
// unknown name gets a 404, never a forwarded request.
type GenericPassthroughRouter struct {
	*GenericRouter
}

// NewGenericPassthroughRouter creates the /passthrough/{provider} router.
func NewGenericPassthroughRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *GenericPassthroughRouter {
	return &GenericPassthroughRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, accessResolver, nil, nil, logger),
	}
}

// RegisterRoutes registers the catch-all for every method a relay may carry.
func (g *GenericPassthroughRouter) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(g.handleGenericPassthrough, middlewares...)
	for _, method := range []string{fasthttp.MethodGet, fasthttp.MethodPost, fasthttp.MethodPut, fasthttp.MethodDelete, fasthttp.MethodPatch, fasthttp.MethodHead} {
		r.Handle(method, genericPassthroughPrefix+"/{provider}/{path:*}", handler)
	}
}

// resolveGenericProvider maps a provider segment to a running generic provider. The known-provider
// map read and the provider slice scan are the only per-request work; nothing is allocated.
func resolveGenericProvider(client *bifrost.Bifrost, name string) (schemas.ModelProvider, generic.PassthroughProvider, bool) {
	if !genericProviderNameRe.MatchString(name) || !schemas.IsKnownProvider(name) {
		return "", nil, false
	}
	provider := schemas.ModelProvider(name)
	gp, ok := client.GetProviderByKey(provider).(generic.PassthroughProvider)
	if !ok || gp == nil {
		return "", nil, false
	}
	return provider, gp, true
}

// extractGenericModel reads the model a JSON body names at modelPath; an empty path reads none.
// It is lenient on purpose: a non-LLM API has no model, and a body that is not JSON names none.
func extractGenericModel(body []byte, contentType, modelPath string) string {
	if modelPath == "" || len(body) == 0 || !looksLikeJSON(contentType, body) {
		return ""
	}
	if v := gjson.GetBytes(body, modelPath); v.Type == gjson.String {
		return strings.TrimSpace(v.Str)
	}
	return ""
}

// genericBodyStreams reports a JSON body asking for a stream with "stream": true.
func genericBodyStreams(body []byte, contentType string) bool {
	return len(body) > 0 && looksLikeJSON(contentType, body) && gjson.GetBytes(body, "stream").Type == gjson.True
}

// looksLikeJSON accepts any JSON-derived media type (application/json, +json suffixes,
// application/x-amz-json-1.1) and sniffs the body when none is declared.
func looksLikeJSON(contentType string, body []byte) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType != "" {
		return strings.Contains(mediaType, "json")
	}
	return gjson.ValidBytes(body)
}

func (g *GenericPassthroughRouter) handleGenericPassthrough(ctx *fasthttp.RequestCtx) {
	passthroughErr := func(_ *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
		return err
	}

	name, _ := ctx.UserValue("provider").(string)
	provider, gp, ok := resolveGenericProvider(g.client, name)
	if !ok {
		g.sendError(ctx, nil, passthroughErr, newBifrostErrorWithCode(nil, "no generic passthrough provider named "+name, fasthttp.StatusNotFound))
		return
	}

	// The remainder is concatenated onto the provider's base URL, so it is checked for shape before
	// anything else happens; a rejected request never reaches key selection or the upstream.
	rest, _ := ctx.UserValue("path").(string)
	path := "/" + rest
	if err := validatePassthroughPath(path); err != nil {
		g.sendError(ctx, nil, passthroughErr, newBifrostErrorWithCode(err, err.Error(), fasthttp.StatusBadRequest))
		return
	}

	// With strict_routes, an operation the operator did not list is refused before anything else
	// is looked at, the same way the path guard refuses a malformed path.
	method := string(ctx.Method())
	if !gp.RouteAllowed(method, path) {
		g.sendError(ctx, nil, passthroughErr, newBifrostErrorWithCode(nil, "no route for "+method+" "+path+" on provider "+name+" (strict_routes)", fasthttp.StatusNotFound))
		return
	}

	// x-model-provider, when sent, must name the provider the route resolved to.
	if _, err := getPassthroughProvider(ctx, g.handlerStore, provider, false); err != nil {
		g.sendError(ctx, nil, passthroughErr, newBifrostErrorWithCode(err, err.Error(), fasthttp.StatusBadRequest))
		return
	}

	safeHeaders, _ := collectPassthroughHeaders(ctx)
	body := ctx.Request.Body()
	contentType := string(ctx.Request.Header.ContentType())
	model := extractGenericModel(body, contentType, gp.ModelPath(method, path))

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, g.handlerStore)
	passthroughReq := &schemas.BifrostPassthroughRequest{
		Method:        method,
		Path:          path,
		RawQuery:      string(ctx.URI().QueryString()),
		Body:          body,
		SafeHeaders:   safeHeaders,
		Provider:      provider,
		Model:         model,
		ModelResolved: true,
	}

	accept := string(ctx.Request.Header.Peek("Accept"))
	if gp.Streams(method, path, accept, genericBodyStreams(body, contentType) || passthroughQueryStreams(method, passthroughReq.RawQuery)) {
		g.handlePassthroughStream(ctx, bifrostCtx, cancel, provider, passthroughReq)
	} else {
		g.handlePassthroughNonStream(ctx, bifrostCtx, cancel, provider, passthroughReq)
	}
}
