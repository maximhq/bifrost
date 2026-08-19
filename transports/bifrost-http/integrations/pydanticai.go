package integrations

import (
	"strings"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// PydanticAIRouter holds route registrations for Pydantic AI endpoints.
// It supports standard chat completions, tool calling, streaming, and multi-provider capabilities.
// Pydantic AI uses standard provider SDKs (OpenAI, Anthropic, Google GenAI), so we reuse
// existing route configurations with aliases for clarity and Pydantic AI-specific extensions.
type PydanticAIRouter struct {
	*GenericRouter
}

// NewPydanticAIRouter creates a new PydanticAIRouter with the given bifrost client.
func NewPydanticAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PydanticAIRouter {
	routes := []RouteConfig{}
	// Add OpenAI routes to Pydantic AI for OpenAI API compatibility
	// Supports: chat completions, embeddings, speech, transcriptions, responses
	routes = append(routes, withPydanticResponsesNullNormalization(CreateOpenAIRouteConfigs("/pydanticai", handlerStore))...)
	// Add Anthropic routes to Pydantic AI for Anthropic API compatibility
	// Supports: messages API (Claude models)
	routes = append(routes, CreateAnthropicRouteConfigs("/pydanticai", logger)...)
	// Add GenAI routes to Pydantic AI for Google Gemini API compatibility
	// Supports: generateContent, streamGenerateContent, embedContent
	routes = append(routes, CreateGenAIRouteConfigs("/pydanticai")...)
	// Add Cohere routes to Pydantic AI for Cohere API compatibility
	// Supports: v2/chat (chat completions with streaming), v2/embed (embeddings)
	routes = append(routes, CreateCohereRouteConfigs("/pydanticai")...)
	// Add Bedrock routes to Pydantic AI for AWS Bedrock API compatibility
	// Supports: converse, converse-stream, invoke, invoke-with-response-stream
	routes = append(routes, CreateBedrockRouteConfigs("/pydanticai", handlerStore)...)
	return &PydanticAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, nil, logger),
	}
}

func withPydanticResponsesNullNormalization(routes []RouteConfig) []RouteConfig {
	for i := range routes {
		if !strings.Contains(routes[i].Path, "/responses") {
			continue
		}

		if routes[i].ResponsesResponseConverter != nil {
			routes[i].ResponsesResponseConverter = func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				// For pydantic responses endpoint, prefer normalized bifrost output
				// instead of raw passthrough, to keep null handling consistent.
				return resp.WithDefaults(), nil
			}
		}

		if routes[i].StreamConfig != nil && routes[i].StreamConfig.ResponsesStreamResponseConverter != nil {
			// Match non-stream behavior: prefer normalized output (raw->normalizePydanticResponsesRawStreamChunk, typed->resp.WithDefaults()+ensurePydanticResponsesStreamTextFields).
			routes[i].StreamConfig.ResponsesStreamResponseConverter = func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
				if resp == nil {
					return "", nil, nil
				}

				if resp.ExtraFields.RawResponse != nil {
					normalizedRaw := normalizePydanticResponsesRawStreamChunk(resp.ExtraFields.RawResponse)
					if normalizedRawString, ok := normalizedRaw.(string); ok {
						return string(resp.Type), normalizedRawString, nil
					}
				}

				normalized := resp.WithDefaults()
				ensurePydanticResponsesStreamTextFields(normalized)
				return string(resp.Type), normalized, nil
			}
		}
	}

	return routes
}

func ensurePydanticResponsesStreamTextFields(resp *schemas.BifrostResponsesStreamResponse) {
	if resp == nil {
		return
	}

	switch resp.Type {
	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		if resp.Delta == nil {
			resp.Delta = bifrost.Ptr("")
		}
	case schemas.ResponsesStreamResponseTypeOutputTextDone:
		if resp.Text == nil {
			resp.Text = bifrost.Ptr("")
		}
	}
}

func normalizePydanticResponsesRawStreamChunk(raw interface{}) interface{} {
	rawString, ok := raw.(string)
	if !ok {
		return raw
	}

	var chunk map[string]interface{}
	if err := sonic.UnmarshalString(rawString, &chunk); err != nil {
		return raw
	}

	changed := false
	if chunkType, ok := chunk["type"].(string); ok {
		switch schemas.ResponsesStreamResponseType(chunkType) {
		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if value, exists := chunk["delta"]; exists && value == nil {
				chunk["delta"] = ""
				changed = true
			}
		case schemas.ResponsesStreamResponseTypeOutputTextDone:
			if value, exists := chunk["text"]; exists && value == nil {
				chunk["text"] = ""
				changed = true
			}
		}
	}

	if !changed {
		return raw
	}

	normalized, err := sonic.MarshalString(chunk)
	if err != nil {
		return raw
	}

	return normalized
}
