package litellm

import (
	"encoding/json"
	"errors"
	"slices"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/providers"
	"github.com/maximhq/bifrost/transports/bifrost-http/providers/anthropic"
	"github.com/maximhq/bifrost/transports/bifrost-http/providers/genai"
	"github.com/maximhq/bifrost/transports/bifrost-http/providers/openai"
	"github.com/valyala/fasthttp"
)

// LiteLLMRequestWrapper wraps any provider-specific request type
type LiteLLMRequestWrapper struct {
	Model         string                `json:"model"`
	ActualRequest interface{}           `json:"-"` // This will hold the actual provider-specific request
	Provider      schemas.ModelProvider `json:"-"`
}

// LiteLLMRouter holds route registrations for LiteLLM endpoints.
// It supports standard chat completions and image-enabled vision capabilities.
// LiteLLM is fully OpenAI-compatible, so we reuse OpenAI types
// with aliases for clarity and minimal LiteLLM-specific extensions
type LiteLLMRouter struct {
	*providers.ProviderRouter
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
		provider := providers.GetProviderFromModel(wrapper.Model)
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

	routes := []providers.RouteConfig{}
	for _, path := range paths {
		routes = append(routes, providers.RouteConfig{
			Path:                   "/litellm" + path,
			Method:                 "POST",
			GetRequestTypeInstance: getRequestTypeInstance,
			RequestConverter:       requestConverter,
			ResponseConverter:      responseConverter,
			PreCallback:            preHook,
		})
	}

	return &LiteLLMRouter{
		ProviderRouter: providers.NewProviderRouter(client, routes),
	}
}
