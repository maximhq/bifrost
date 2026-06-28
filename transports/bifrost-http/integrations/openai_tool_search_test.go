package integrations

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func findOpenAIResponsesRoute(t *testing.T) RouteConfig {
	for _, route := range CreateOpenAIRouteConfigs("", nil) {
		if route.Path == "/v1/responses" && route.ResponsesResponseConverter != nil && route.StreamConfig != nil && route.StreamConfig.ResponsesStreamResponseConverter != nil {
			return route
		}
	}
	t.Fatal("openai responses route not found")
	return RouteConfig{}
}

func TestOpenAIResponsesConverterPreservesToolSearchWireShape(t *testing.T) {
	route := findOpenAIResponsesRoute(t)

	t.Parallel()

	callType := schemas.ResponsesMessageTypeToolSearchCall
	outputType := schemas.ResponsesMessageTypeToolSearchOutput
	args := `{"query":"codexself tools","limit":20}`
	namespace := "mcp__codexself"

	resp := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: &callType,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("call_123"),
					Arguments: &args,
				},
			},
			{
				Type: &outputType,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("search-1"),
					Tools: []schemas.ResponsesTool{
						{
							Type: schemas.ResponsesToolType("namespace"),
							Name: schemas.Ptr(namespace),
							ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
								Tools: []schemas.ResponsesTool{
									{
										Type: schemas.ResponsesToolType("function"),
										Name: schemas.Ptr("codex_reply"),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	converted, err := route.ResponsesResponseConverter(nil, resp)
	if err != nil {
		t.Fatalf("convert responses response: %v", err)
	}
	encoded, err := schemas.MarshalSorted(converted)
	if err != nil {
		t.Fatalf("marshal converted response: %v", err)
	}
	encodedStr := string(encoded)

	if !strings.Contains(encoded, `"arguments":{"query":"codexself tools","limit":20}`) {
		t.Fatalf("expected object-typed tool_search_call arguments, got %s", encoded)
	}
	if strings.Contains(encoded, `"arguments":"{\"query\":\"codexself tools\",\"limit\":20}"`) {
		t.Fatalf("expected arguments not to remain string-encoded, got %s", encoded)
	}
	if !strings.Contains(encodedStr, `"execution":"client"`) {
		t.Fatalf("expected execution=client in converted output, got %s", encoded)
	}
	if !strings.Contains(encodedStr, `"type":"namespace"`) || !strings.Contains(encodedStr, `"type":"function"`) {
		t.Fatalf("expected tool_search_output tool types to survive conversion, got %s", encoded)
	}
}

func TestOpenAIResponsesStreamConverterPreservesToolSearchWireShape(t *testing.T) {
	route := findOpenAIResponsesRoute(t)

	t.Parallel()

	callType := schemas.ResponsesMessageTypeToolSearchCall
	args := `{"query":"codexself tools","limit":20}`
	streamResp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeOutputItemDone,
		Item: &schemas.ResponsesMessage{
			Type: &callType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr("call_123"),
				Arguments: &args,
			},
		},
	}

	eventType, converted, err := route.StreamConfig.ResponsesStreamResponseConverter(nil, streamResp)
	if err != nil {
		t.Fatalf("convert stream response: %v", err)
	}
	if eventType != string(schemas.ResponsesStreamResponseTypeOutputItemDone) {
		t.Fatalf("unexpected event type %q", eventType)
	}
	encoded, err := schemas.MarshalSorted(converted)
	if err != nil {
		t.Fatalf("marshal converted stream response: %v", err)
	}
	encodedStr := string(encoded)

	if !strings.Contains(encodedStr, `"type":"tool_search_call"`) {
		t.Fatalf("expected tool_search_call item, got %s", encoded)
	}
	if !strings.Contains(encodedStr, `"arguments":{"query":"codexself tools","limit":20}`) {
		t.Fatalf("expected object-typed arguments, got %s", encoded)
	}
	if !strings.Contains(encodedStr, `"execution":"client"`) {
		t.Fatalf("expected execution=client in stream output, got %s", encoded)
	}
}
