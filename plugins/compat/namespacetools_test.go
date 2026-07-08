package compat

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// namespaceTool builds a namespace-scoped tool grouping the given function tool
// names under the provided namespace name.
func namespaceTool(namespace string, toolNames ...string) schemas.ResponsesTool {
	inner := make([]schemas.ResponsesTool, 0, len(toolNames))
	for _, name := range toolNames {
		inner = append(inner, schemas.ResponsesTool{
			Type:                  schemas.ResponsesToolTypeFunction,
			Name:                  schemas.Ptr(name),
			ResponsesToolFunction: &schemas.ResponsesToolFunction{},
		})
	}
	return schemas.ResponsesTool{
		Type:                   schemas.ResponsesToolTypeNamespace,
		Name:                   schemas.Ptr(namespace),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: inner},
	}
}

// functionCallItem builds a function_call output item as a provider would return
// it after flattening: a name with no namespace.
func functionCallItem(name string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_1"),
			Name:   schemas.Ptr(name),
		},
	}
}

func TestFlattenNamespaceTools_ReturnsMappingAndFlattens(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("top_level")},
				namespaceTool("mcp__node_repl__", "js", "python"),
			},
		},
	}

	nsMap := flattenNamespaceTools(req)

	// namespace wrapper replaced by its two inner tools, top-level tool preserved.
	if got := len(req.Params.Tools); got != 3 {
		t.Fatalf("flattened tool count = %d, want 3", got)
	}
	for _, tool := range req.Params.Tools {
		if tool.Type == schemas.ResponsesToolTypeNamespace {
			t.Fatalf("namespace tool survived flattening: %+v", tool)
		}
	}
	if nsMap["js"] != "mcp__node_repl__" {
		t.Errorf("nsMap[js] = %q, want mcp__node_repl__", nsMap["js"])
	}
	if nsMap["python"] != "mcp__node_repl__" {
		t.Errorf("nsMap[python] = %q, want mcp__node_repl__", nsMap["python"])
	}
	if _, ok := nsMap["top_level"]; ok {
		t.Errorf("top_level should not be namespaced, got %q", nsMap["top_level"])
	}
}

func TestFlattenNamespaceTools_SkipsOpenAI(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{namespaceTool("mcp__node_repl__", "js")},
		},
	}

	nsMap := flattenNamespaceTools(req)

	if nsMap != nil {
		t.Errorf("expected nil map for OpenAI, got %v", nsMap)
	}
	if len(req.Params.Tools) != 1 || req.Params.Tools[0].Type != schemas.ResponsesToolTypeNamespace {
		t.Errorf("OpenAI namespace tool should be left intact, got %+v", req.Params.Tools)
	}
}

func TestApplyParameterConversion_StoresNamespaceMap(t *testing.T) {
	ctx := newTestContext()
	req := newResponsesRequest(schemas.Anthropic, "claude-sonnet-4", &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{namespaceTool("mcp__node_repl__", "js")},
	})

	applyParameterConversion(ctx, req)

	stored, ok := ctx.Value(namespaceToolMapContextKey).(map[string]string)
	if !ok {
		t.Fatalf("namespace map not stored in context")
	}
	if stored["js"] != "mcp__node_repl__" {
		t.Errorf("stored[js] = %q, want mcp__node_repl__", stored["js"])
	}
}

func TestRestoreNamespaceOnResponse_NonStreaming(t *testing.T) {
	nsMap := map[string]string{"js": "mcp__node_repl__"}
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{functionCallItem("js")},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	got := resp.ResponsesResponse.Output[0].ResponsesToolMessage.Namespace
	if got == nil || *got != "mcp__node_repl__" {
		t.Fatalf("namespace = %v, want mcp__node_repl__", got)
	}
}

func TestRestoreNamespaceOnResponse_StreamingItem(t *testing.T) {
	nsMap := map[string]string{"js": "mcp__node_repl__"}
	item := functionCallItem("js")
	resp := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &item,
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	got := resp.ResponsesStreamResponse.Item.ResponsesToolMessage.Namespace
	if got == nil || *got != "mcp__node_repl__" {
		t.Fatalf("streaming item namespace = %v, want mcp__node_repl__", got)
	}
}

func TestRestoreNamespaceOnResponse_StreamingCompletedOutput(t *testing.T) {
	nsMap := map[string]string{"js": "mcp__node_repl__"}
	resp := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				Output: []schemas.ResponsesMessage{functionCallItem("js")},
			},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	got := resp.ResponsesStreamResponse.Response.Output[0].ResponsesToolMessage.Namespace
	if got == nil || *got != "mcp__node_repl__" {
		t.Fatalf("completed output namespace = %v, want mcp__node_repl__", got)
	}
}

func TestRestoreNamespaceOnResponse_DoesNotOverwriteExisting(t *testing.T) {
	nsMap := map[string]string{"js": "mcp__node_repl__"}
	item := functionCallItem("js")
	item.ResponsesToolMessage.Namespace = schemas.Ptr("provider_supplied")
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{item},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	got := resp.ResponsesResponse.Output[0].ResponsesToolMessage.Namespace
	if got == nil || *got != "provider_supplied" {
		t.Fatalf("namespace = %v, want provider_supplied (not overwritten)", got)
	}
}

func TestRestoreNamespaceOnResponse_UnknownToolUntouched(t *testing.T) {
	nsMap := map[string]string{"js": "mcp__node_repl__"}
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{functionCallItem("not_namespaced")},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	if got := resp.ResponsesResponse.Output[0].ResponsesToolMessage.Namespace; got != nil {
		t.Fatalf("namespace = %v, want nil for a tool that was never namespaced", got)
	}
}

// TestCompatPlugin_NamespaceRoundTrip exercises the full flatten (PreLLMHook) →
// restore (PostLLMHook) flow across a shared context for both non-streaming and
// streaming responses.
func TestCompatPlugin_NamespaceRoundTrip(t *testing.T) {
	p, err := Init(Config{ShouldConvertParams: true}, bifrost.NewDefaultLogger(schemas.LogLevelError), nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx := newTestContext()
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-4",
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{namespaceTool("mcp__node_repl__", "js")},
			},
		},
	}

	modifiedReq, _, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	// The request forwarded to the provider must carry the flattened function tool.
	if got := len(modifiedReq.ResponsesRequest.Params.Tools); got != 1 {
		t.Fatalf("forwarded tool count = %d, want 1", got)
	}
	if modifiedReq.ResponsesRequest.Params.Tools[0].Type != schemas.ResponsesToolTypeFunction {
		t.Fatalf("forwarded tool type = %s, want function", modifiedReq.ResponsesRequest.Params.Tools[0].Type)
	}

	// Non-streaming response from the provider: function_call without a namespace.
	nonStream := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{functionCallItem("js")},
		},
	}
	nonStream, _, err = p.PostLLMHook(ctx, nonStream, nil)
	if err != nil {
		t.Fatalf("PostLLMHook (non-stream): %v", err)
	}
	if got := nonStream.ResponsesResponse.Output[0].ResponsesToolMessage.Namespace; got == nil || *got != "mcp__node_repl__" {
		t.Fatalf("non-stream namespace = %v, want mcp__node_repl__", got)
	}

	// Streaming chunk carrying the function_call item.
	item := functionCallItem("js")
	stream := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &item,
		},
	}
	stream, _, err = p.PostLLMHook(ctx, stream, nil)
	if err != nil {
		t.Fatalf("PostLLMHook (stream): %v", err)
	}
	if got := stream.ResponsesStreamResponse.Item.ResponsesToolMessage.Namespace; got == nil || *got != "mcp__node_repl__" {
		t.Fatalf("stream namespace = %v, want mcp__node_repl__", got)
	}
}
