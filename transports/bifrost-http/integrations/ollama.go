package integrations

import (
	"context"
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	ollama "github.com/maximhq/bifrost/core/providers/ollama"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OllamaRouter handles Ollama-compatible API endpoints.
type OllamaRouter struct {
	*GenericRouter
}

// OllamaErrorResponse is the error response for Ollama.
type OllamaErrorResponse struct {
	Error string `json:"error"`
}

func toOllamaErrorResponse(err *schemas.BifrostError) *OllamaErrorResponse {
	if err.Error != nil {
		return &OllamaErrorResponse{Error: err.Error.Message}
	}
	return &OllamaErrorResponse{Error: "an unknown error occurred"}
}

// CreateOllamaRouteConfigs returns route configurations for Ollama-compatible endpoints.
func CreateOllamaRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	// POST /api/generate → text completion
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeOllama,
		Path:   pathPrefix + "/api/generate",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.TextCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) any {
			return &ollama.OllamaGenerateRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req any) (*schemas.BifrostRequest, error) {
			r, ok := req.(*ollama.OllamaGenerateRequest)
			if !ok {
				return nil, errors.New("invalid request type for ollama generate")
			}
			return &schemas.BifrostRequest{
				TextCompletionRequest: r.ToBifrostTextCompletionRequest(),
			}, nil
		},
		TextResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (any, error) {
			return ollama.ToOllamaGenerateResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) any {
			return toOllamaErrorResponse(err)
		},
		StreamConfig: &StreamConfig{
			TextStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (string, any, error) {
				return ollama.ToOllamaGenerateStreamChunk(resp)
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) any {
				line, _ := ollama.NdjsonLine(toOllamaErrorResponse(err))
				return line
			},
		},
	})

	// POST /api/chat → chat completion
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeOllama,
		Path:   pathPrefix + "/api/chat",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ChatCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) any {
			return &ollama.OllamaChatRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req any) (*schemas.BifrostRequest, error) {
			r, ok := req.(*ollama.OllamaChatRequest)
			if !ok {
				return nil, errors.New("invalid request type for ollama chat")
			}
			return &schemas.BifrostRequest{
				ChatRequest: r.ToBifrostChatRequest(),
			}, nil
		},
		ChatResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (any, error) {
			return ollama.ToOllamaChatResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) any {
			return toOllamaErrorResponse(err)
		},
		StreamConfig: &StreamConfig{
			ChatStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (string, any, error) {
				return ollama.ToOllamaChatStreamChunk(resp)
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) any {
				line, _ := ollama.NdjsonLine(toOllamaErrorResponse(err))
				return line
			},
		},
	})

	// POST /api/embed → embeddings
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeOllama,
		Path:   pathPrefix + "/api/embed",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.EmbeddingRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) any {
			return &ollama.OllamaEmbedRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req any) (*schemas.BifrostRequest, error) {
			r, ok := req.(*ollama.OllamaEmbedRequest)
			if !ok {
				return nil, errors.New("invalid request type for ollama embed")
			}
			bifrostReq, err := r.ToBifrostEmbeddingRequest()
			if err != nil {
				return nil, err
			}
			return &schemas.BifrostRequest{EmbeddingRequest: bifrostReq}, nil
		},
		EmbeddingResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostEmbeddingResponse) (any, error) {
			return ollama.ToOllamaEmbedResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) any {
			return toOllamaErrorResponse(err)
		},
	})

	return routes
}

// NewOllamaRouter creates a new OllamaRouter.
func NewOllamaRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *OllamaRouter {
	return &OllamaRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateOllamaRouteConfigs("/ollama"), nil, logger),
	}
}
