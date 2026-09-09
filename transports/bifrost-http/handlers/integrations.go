// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains integration management handlers for AI provider integrations.
package handlers

import (
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// IntegrationHandler manages HTTP requests for AI provider integrations
type IntegrationHandler struct {
	extensions            []integrations.ExtensionRouter
	wsResponses           *WSResponsesHandler
	wsRealtime            *WSRealtimeHandler
	webrtcRealtime        *WebRTCRealtimeHandler
	realtimeClientSecrets *RealtimeClientSecretsHandler
}

// AccessResolver narrows the provider fan-out of a models listing to what the request may reach
type AccessResolver interface {
	NarrowListModelsProviders(bifrostCtx *schemas.BifrostContext)
}

// NewIntegrationHandler creates a new integration handler instance.
// WebSocket handlers may be nil if WebSocket support is not configured.
// accessResolver answers what a request may reach, the same way the inference handler's models
// manager does; nil leaves every listing with the fan-out it already had.
func NewIntegrationHandler(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, wsResponses *WSResponsesHandler, wsRealtime *WSRealtimeHandler, webrtcRealtime *WebRTCRealtimeHandler, realtimeClientSecrets *RealtimeClientSecretsHandler) *IntegrationHandler {
	// Initialize all available integration routers
	extensions := []integrations.ExtensionRouter{
		integrations.NewOpenAIRouter(client, handlerStore, accessResolver, logger),
		integrations.NewAnthropicRouter(client, handlerStore, accessResolver, logger),
		integrations.NewGenAIRouter(client, handlerStore, accessResolver, logger),
		integrations.NewLiteLLMRouter(client, handlerStore, accessResolver, logger),
		integrations.NewCohereRouter(client, handlerStore, accessResolver, logger),
		integrations.NewLangChainRouter(client, handlerStore, accessResolver, logger),
		integrations.NewPydanticAIRouter(client, handlerStore, accessResolver, logger),
		integrations.NewBedrockRouter(client, handlerStore, accessResolver, logger),
		// passthrough routers
		integrations.NewGenAIPassthroughRouter(client, handlerStore, accessResolver, logger),
		integrations.NewChatGPTPassthroughRouter(client, handlerStore, accessResolver, logger),
		integrations.NewOpenAIPassthroughRouter(client, handlerStore, accessResolver, logger),
		integrations.NewAnthropicPassthroughRouter(client, handlerStore, accessResolver, logger),
		integrations.NewAzurePassthroughRouter(client, handlerStore, accessResolver, logger),
		integrations.NewRunwarePassthroughRouter(client, handlerStore, accessResolver, logger),
		integrations.NewCursorRouter(client, handlerStore, accessResolver, logger),
	}

	return &IntegrationHandler{
		extensions:            extensions,
		wsResponses:           wsResponses,
		wsRealtime:            wsRealtime,
		webrtcRealtime:        webrtcRealtime,
		realtimeClientSecrets: realtimeClientSecrets,
	}
}

// RegisterRoutes registers all integration routes for AI provider compatibility endpoints
func (h *IntegrationHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Register routes for each integration extension
	for _, extension := range h.extensions {
		extension.RegisterRoutes(r, middlewares...)
	}
	// Register WebSocket routes (base path + integration paths)
	if h.wsResponses != nil {
		h.wsResponses.RegisterRoutes(r, middlewares...)
	}
	if h.wsRealtime != nil {
		h.wsRealtime.RegisterRoutes(r, middlewares...)
	}
	if h.webrtcRealtime != nil {
		h.webrtcRealtime.RegisterRoutes(r, middlewares...)
	}
	if h.realtimeClientSecrets != nil {
		h.realtimeClientSecrets.RegisterRoutes(r, middlewares...)
	}
}

func (h *IntegrationHandler) Close() {
	if h == nil {
		return
	}
	if h.wsResponses != nil {
		h.wsResponses.Close()
	}
	if h.wsRealtime != nil {
		h.wsRealtime.Close()
	}
	if h.webrtcRealtime != nil {
		h.webrtcRealtime.Close()
	}
}

// SetLargePayloadHook sets the large payload detection hook on all integration routers
// that support it. This is used by enterprise to inject large payload optimization.
func (h *IntegrationHandler) SetLargePayloadHook(hook integrations.LargePayloadHook) {
	for _, extension := range h.extensions {
		if setter, ok := extension.(interface {
			SetLargePayloadHook(integrations.LargePayloadHook)
		}); ok {
			setter.SetLargePayloadHook(hook)
		}
	}
}

// SetLargeResponseHook sets the large response scanning hook on all integration routers
// that support it. Enterprise uses this to inject Phase B usage extraction into the
// response stream without embedding scanning logic in the OSS router.
func (h *IntegrationHandler) SetLargeResponseHook(hook integrations.LargeResponseHook) {
	for _, extension := range h.extensions {
		if setter, ok := extension.(interface {
			SetLargeResponseHook(integrations.LargeResponseHook)
		}); ok {
			setter.SetLargeResponseHook(hook)
		}
	}
}
