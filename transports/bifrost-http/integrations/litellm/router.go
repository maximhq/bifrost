package litellm

import (
	"encoding/json"
	"errors"
	"slices"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/anthropic"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/genai"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/openai"
	"github.com/valyala/fasthttp"
)

// LiteLLMRequestWrapper wraps any provider-specific request type
type LiteLLMRequestWrapper struct {
	Model         string                `json:"model"`
	ActualRequest interface{}           `json:"-"` // This will hold the actual provider-specific request
	Provider      schemas.ModelProvider `json:"-"`
}

// IsStreamingRequested implements the StreamingRequest interface
// by delegating to the underlying provider-specific request
func (w *LiteLLMRequestWrapper) IsStreamingRequested() bool {
	if w.ActualRequest == nil {
		return false
	}

	// Delegate to the actual request's streaming method
	if streamingReq, ok := w.ActualRequest.(integrations.StreamingRequest); ok {
		return streamingReq.IsStreamingRequested()
	}

	return false
}

// LiteLLMRouter holds route registrations for LiteLLM endpoints.
// It supports standard chat completions and image-enabled vision capabilities.
// LiteLLM is fully OpenAI-compatible, so we reuse OpenAI types
// with aliases for clarity and minimal LiteLLM-specific extensions
type LiteLLMRouter struct {
	*integrations.GenericRouter
}

// NewLiteLLMRouter creates a new LiteLLMRouter with the given bifrost client.
func NewLiteLLMRouter(client *bifrost.Bifrost) *LiteLLMRouter {
	paths := []string{
		"/chat/completions",
		"/v1/messages",
	}

	getRequestTypeInstance := func() interface{} {
		return &LiteLLMRequestWrapper{}
	}

	availableProviders := []schemas.ModelProvider{
		schemas.OpenAI,
		schemas.Anthropic,
		schemas.Vertex,
		schemas.Azure,
	}

	// Pre-hook to determine provider and parse request with correct type
	preHook := func(ctx *fasthttp.RequestCtx, req interface{}) error {
		wrapper, ok := req.(*LiteLLMRequestWrapper)
		if !ok {
			return errors.New("invalid request wrapper type")
		}

		if wrapper.Model == "" {
			return errors.New("model field is required")
		}

		// Determine provider from model
		provider := integrations.GetProviderFromModel(wrapper.Model)
		if !slices.Contains(availableProviders, provider) {
			return errors.New("unsupported provider: " + string(provider))
		}

		// Get the request body
		body := ctx.Request.Body()
		if len(body) == 0 {
			return errors.New("request body is required")
		}

		// Create the appropriate request type based on provider and re-parse
		var actualReq interface{}
		switch provider {
		case schemas.OpenAI, schemas.Azure:
			actualReq = &openai.OpenAIChatRequest{}
		case schemas.Anthropic:
			actualReq = &anthropic.AnthropicMessageRequest{}
		case schemas.Vertex:
			actualReq = &genai.GeminiChatRequest{}
		default:
			return errors.New("unsupported provider: " + string(provider))
		}

		// Parse the body into the correct request type
		if err := json.Unmarshal(body, actualReq); err != nil {
			return errors.New("failed to parse request for provider " + string(provider) + ": " + err.Error())
		}

		// Store the parsed request and provider in the wrapper
		wrapper.ActualRequest = actualReq
		wrapper.Provider = provider

		return nil
	}

	requestConverter := func(req interface{}) (*schemas.BifrostRequest, error) {
		wrapper, ok := req.(*LiteLLMRequestWrapper)
		if !ok {
			return nil, errors.New("invalid request wrapper type")
		}

		if wrapper.ActualRequest == nil {
			return nil, errors.New("request was not properly processed by pre-hook")
		}

		// Handle different provider-specific request types
		switch actualReq := wrapper.ActualRequest.(type) {
		case *openai.OpenAIChatRequest:
			bifrostReq := actualReq.ConvertToBifrostRequest()
			bifrostReq.Provider = wrapper.Provider
			return bifrostReq, nil

		case *anthropic.AnthropicMessageRequest:
			bifrostReq := actualReq.ConvertToBifrostRequest()
			bifrostReq.Provider = wrapper.Provider
			return bifrostReq, nil

		case *genai.GeminiChatRequest:
			bifrostReq := actualReq.ConvertToBifrostRequest()
			bifrostReq.Provider = wrapper.Provider
			return bifrostReq, nil

		default:
			return nil, errors.New("unsupported request type")
		}
	}

	responseConverter := func(resp *schemas.BifrostResponse) (interface{}, error) {
		switch resp.ExtraFields.Provider {
		case schemas.OpenAI, schemas.Azure:
			return openai.DeriveOpenAIFromBifrostResponse(resp), nil
		case schemas.Anthropic:
			return anthropic.DeriveAnthropicFromBifrostResponse(resp), nil
		case schemas.Vertex:
			return genai.DeriveGenAIFromBifrostResponse(resp), nil
		default:
			return resp, nil
		}
	}

	errorConverter := func(err *schemas.BifrostError) interface{} {
		switch err.Provider {
		case schemas.OpenAI, schemas.Azure:
			return openai.DeriveOpenAIErrorFromBifrostError(err)
		case schemas.Anthropic:
			return anthropic.DeriveAnthropicErrorFromBifrostError(err)
		case schemas.Vertex:
			return genai.DeriveGeminiErrorFromBifrostError(err)
		default:
			return err
		}
	}

	streamResponseConverter := func(resp *schemas.BifrostResponse) (interface{}, error) {
		if resp == nil {
			return nil, errors.New("response is nil")
		}

		provider := resp.ExtraFields.Provider
		if provider == "" && resp.Model != "" {
			provider = integrations.GetProviderFromModel(resp.Model)
		}

		// Route to the appropriate provider's streaming converter based on provider type
		switch provider {
		case schemas.OpenAI, schemas.Azure:
			return openai.DeriveOpenAIStreamFromBifrostResponse(resp), nil
		case schemas.Anthropic:
			return anthropic.DeriveAnthropicStreamFromBifrostResponse(resp), nil
		case schemas.Vertex:
			return genai.DeriveGeminiStreamFromBifrostResponse(resp), nil
		default:
			return resp, nil
		}
	}

	streamErrorConverter := func(err *schemas.BifrostError) interface{} {
		switch err.Provider {
		case schemas.OpenAI, schemas.Azure:
			return openai.DeriveOpenAIStreamFromBifrostError(err)
		case schemas.Anthropic:
			return anthropic.DeriveAnthropicStreamFromBifrostError(err)
		case schemas.Vertex:
			return genai.DeriveGeminiStreamFromBifrostError(err)
		default:
			return err
		}
	}

	routes := []integrations.RouteConfig{}
	for _, path := range paths {
		routes = append(routes, integrations.RouteConfig{
			Path:                   "/litellm" + path,
			Method:                 "POST",
			GetRequestTypeInstance: getRequestTypeInstance,
			RequestConverter:       requestConverter,
			ResponseConverter:      responseConverter,
			ErrorConverter:         errorConverter,
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: streamResponseConverter,
				ErrorConverter:    streamErrorConverter,
			},
			PreCallback: preHook,
		})
	}

	return &LiteLLMRouter{
		GenericRouter: integrations.NewGenericRouter(client, routes),
	}
}
