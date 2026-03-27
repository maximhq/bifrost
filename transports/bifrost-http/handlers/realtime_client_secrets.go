package handlers

import (
	"fmt"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// RealtimeClientSecretsHandler exposes OpenAI-compatible HTTP routes for
// minting short-lived Realtime client secrets.
type RealtimeClientSecretsHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	routeSpecs   map[string]schemas.RealtimeSessionRoute
}

func NewRealtimeClientSecretsHandler(client *bifrost.Bifrost, config *lib.Config) *RealtimeClientSecretsHandler {
	return &RealtimeClientSecretsHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		routeSpecs:   make(map[string]schemas.RealtimeSessionRoute),
	}
}

func (h *RealtimeClientSecretsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleRequest, middlewares...)
	for _, route := range h.realtimeSessionRoutes() {
		h.routeSpecs[route.Path] = route
		r.POST(route.Path, handler)
	}
}

func (h *RealtimeClientSecretsHandler) handleRequest(ctx *fasthttp.RequestCtx) {
	if !isJSONContentType(string(ctx.Request.Header.ContentType())) {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"Content-Type must be application/json",
			nil,
		))
		return
	}

	body := append([]byte(nil), ctx.Request.Body()...)
	route, ok := h.routeSpecs[string(ctx.Path())]
	if !ok {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusNotFound,
			"invalid_request_error",
			"unsupported realtime client secret route",
			nil,
		))
		return
	}

	providerKey, model, err := resolveRealtimeClientSecretTarget(route, body)
	if err != nil {
		SendBifrostError(ctx, err)
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(
		ctx,
		h.handlerStore.ShouldAllowDirectKeys(),
		h.config.GetHeaderMatcher(),
		h.config.GetMCPHeaderCombinedAllowlist(),
	)
	defer cancel()
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	if route.DefaultProvider == schemas.OpenAI {
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}

	provider := h.client.GetProviderByKey(providerKey)
	if provider == nil {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"provider not found: "+string(providerKey),
			nil,
		))
		return
	}

	key, keyErr := h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.RealtimeRequest, providerKey, model)
	if keyErr != nil {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			keyErr.Error(),
			keyErr,
		))
		return
	}

	sessionProvider, ok := provider.(schemas.RealtimeSessionProvider)
	if !ok {
		SendBifrostError(ctx, realtimeSessionNotSupportedError(providerKey, provider))
		return
	}

	resp, bifrostErr := sessionProvider.CreateRealtimeClientSecret(bifrostCtx, key, route.EndpointType, body)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	writeRealtimeClientSecretResponse(ctx, resp)
}

func (h *RealtimeClientSecretsHandler) realtimeSessionRoutes() []schemas.RealtimeSessionRoute {
	routes := []schemas.RealtimeSessionRoute{
		{
			Path:         "/v1/realtime/client_secrets",
			EndpointType: schemas.RealtimeSessionEndpointClientSecrets,
		},
		{
			Path:         "/v1/realtime/sessions",
			EndpointType: schemas.RealtimeSessionEndpointSessions,
		},
	}

	for _, path := range integrations.OpenAIRealtimeClientSecretPaths("/openai") {
		endpointType := schemas.RealtimeSessionEndpointClientSecrets
		if strings.HasSuffix(path, "/realtime/sessions") {
			endpointType = schemas.RealtimeSessionEndpointSessions
		}
		routes = append(routes, schemas.RealtimeSessionRoute{
			Path:            path,
			EndpointType:    endpointType,
			DefaultProvider: schemas.OpenAI,
		})
	}
	return routes
}

func resolveRealtimeClientSecretTarget(route schemas.RealtimeSessionRoute, body []byte) (schemas.ModelProvider, string, *schemas.BifrostError) {
	root, err := schemas.ParseRealtimeClientSecretBody(body)
	if err != nil {
		return "", "", err
	}

	rawModel, err := schemas.ExtractRealtimeClientSecretModel(root)
	if err != nil {
		return "", "", err
	}

	defaultProvider := route.DefaultProvider
	providerKey, model := schemas.ParseModelString(rawModel, defaultProvider)
	if defaultProvider == "" && providerKey == "" {
		return "", "", newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"session.model must use provider/model on /v1 realtime client secret routes",
			nil,
		)
	}
	if providerKey == "" || model == "" {
		return "", "", newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"session.model is required",
			nil,
		)
	}

	return providerKey, model, nil
}

func realtimeSessionNotSupportedError(providerKey schemas.ModelProvider, provider schemas.Provider) *schemas.BifrostError {
	if rtProvider, ok := provider.(schemas.RealtimeProvider); ok && rtProvider.SupportsRealtimeAPI() {
		return newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			fmt.Sprintf("provider %s supports realtime websocket connections but not realtime client secret creation", providerKey),
			nil,
		)
	}

	return newRealtimeClientSecretHandlerError(
		fasthttp.StatusBadRequest,
		"invalid_request_error",
		fmt.Sprintf("provider %s does not support realtime client secret creation", providerKey),
		nil,
	)
}

func newRealtimeClientSecretHandlerError(status int, errorType, message string, err error) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(status),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(errorType),
			Message: message,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.RealtimeRequest,
		},
	}
}

func writeRealtimeClientSecretResponse(ctx *fasthttp.RequestCtx, resp *schemas.BifrostPassthroughResponse) {
	if resp == nil {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusInternalServerError,
			"server_error",
			"provider returned an empty realtime client secret response",
			nil,
		))
		return
	}

	for key, value := range resp.Headers {
		ctx.Response.Header.Set(key, value)
	}
	if len(ctx.Response.Header.ContentType()) == 0 {
		ctx.SetContentType("application/json")
	}
	ctx.SetStatusCode(resp.StatusCode)
	ctx.SetBody(resp.Body)
}

func isJSONContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" {
		return false
	}
	if strings.HasPrefix(contentType, "application/json") {
		return true
	}
	return strings.HasSuffix(contentType, "+json")
}
