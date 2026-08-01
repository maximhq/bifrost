package compat

import (
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// newTestPlugin returns a plugin instance suitable for exercising the namespace
// helpers directly (they are methods so they can reach p.logger).
func newTestPlugin(t *testing.T) *CompatPlugin {
	t.Helper()
	p, err := Init(Config{ShouldConvertParams: true}, bifrost.NewDefaultLogger(schemas.LogLevelError), nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

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

// historyFunctionCall builds a function_call item as it appears in the request
// input on a follow-up turn: the original name plus the namespace the previous
// PostLLMHook restored.
func historyFunctionCall(namespace, name string) schemas.ResponsesMessage {
	msg := functionCallItem(name)
	if namespace != "" {
		msg.ResponsesToolMessage.Namespace = schemas.Ptr(namespace)
	}
	return msg
}

// toolNames collects the flattened tool names in order.
func toolNames(tools []schemas.ResponsesTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == nil {
			names = append(names, "<nil>")
			continue
		}
		names = append(names, *tool.Name)
	}
	return names
}

// ---------------------------------------------------------------------------
// Group 1: flattening and naming
// ---------------------------------------------------------------------------

func TestFlattenNamespaceTools_PrefixesAndReturnsMapping(t *testing.T) {
	p := newTestPlugin(t)
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

	nsMap := p.flattenNamespaceTools(req)

	if got := len(req.Params.Tools); got != 3 {
		t.Fatalf("flattened tool count = %d, want 3 (%v)", got, toolNames(req.Params.Tools))
	}
	for _, tool := range req.Params.Tools {
		if tool.Type == schemas.ResponsesToolTypeNamespace {
			t.Fatalf("namespace tool survived flattening: %+v", tool)
		}
	}
	want := []string{"top_level", "mcp__node_repl__js", "mcp__node_repl__python"}
	if got := toolNames(req.Params.Tools); !equalStrings(got, want) {
		t.Fatalf("flattened names = %v, want %v", got, want)
	}
	if origin, ok := nsMap.toOrigin["mcp__node_repl__js"]; !ok || origin.Namespace != "mcp__node_repl__" || origin.OriginalName != "js" {
		t.Errorf("toOrigin[mcp__node_repl__js] = %+v (ok=%v), want {mcp__node_repl__ js}", origin, ok)
	}
	if got := nsMap.toPrefixed[namespaceKey("mcp__node_repl__", "python")]; got != "mcp__node_repl__python" {
		t.Errorf("toPrefixed = %q, want mcp__node_repl__python", got)
	}
	if _, ok := nsMap.toOrigin["top_level"]; ok {
		t.Errorf("top_level should not be recorded as namespaced")
	}
}

// The renaming exists precisely so that two namespaces exposing the same tool
// name stay distinguishable: without it the provider receives duplicate function
// names and the response cannot be mapped back to a namespace.
func TestFlattenNamespaceTools_DisambiguatesDuplicateToolNames(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				namespaceTool("ns_a", "search"),
				namespaceTool("ns_b", "search"),
			},
		},
	}

	nsMap := p.flattenNamespaceTools(req)

	names := toolNames(req.Params.Tools)
	if !equalStrings(names, []string{"ns_a__search", "ns_b__search"}) {
		t.Fatalf("flattened names = %v, want [ns_a__search ns_b__search]", names)
	}
	if origin := nsMap.toOrigin["ns_a__search"]; origin.Namespace != "ns_a" {
		t.Errorf("ns_a__search namespace = %q, want ns_a", origin.Namespace)
	}
	if origin := nsMap.toOrigin["ns_b__search"]; origin.Namespace != "ns_b" {
		t.Errorf("ns_b__search namespace = %q, want ns_b", origin.Namespace)
	}
	if got := nsMap.namespacesByName["search"]; len(got) != 2 {
		t.Errorf("namespacesByName[search] = %v, want two namespaces", got)
	}
}

// A generated prefix must not shadow a tool the caller already declared at the
// top level (or that the MCP layer injected).
func TestFlattenNamespaceTools_AvoidsCollisionWithTopLevelTool(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("ns__search")},
				namespaceTool("ns", "search"),
			},
		},
	}

	nsMap := p.flattenNamespaceTools(req)

	names := toolNames(req.Params.Tools)
	if len(names) != 2 {
		t.Fatalf("flattened names = %v, want 2 entries", names)
	}
	if names[0] != "ns__search" {
		t.Fatalf("top-level tool renamed to %q, want it untouched", names[0])
	}
	if names[1] == "ns__search" {
		t.Fatalf("namespaced tool collided with the top-level tool: %v", names)
	}
	if origin, ok := nsMap.toOrigin[names[1]]; !ok || origin.Namespace != "ns" || origin.OriginalName != "search" {
		t.Errorf("toOrigin[%s] = %+v (ok=%v), want {ns search}", names[1], origin, ok)
	}
}

func TestFlattenNamespaceTools_SanitizesIllegalCharacters(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{namespaceTool("my ns/v1", "read.file")},
		},
	}

	p.flattenNamespaceTools(req)

	got := toolNames(req.Params.Tools)
	if !equalStrings(got, []string{"my_ns_v1__read_file"}) {
		t.Fatalf("sanitized name = %v, want [my_ns_v1__read_file]", got)
	}
}

func TestFlattenNamespaceTools_TruncatesOverlongNames(t *testing.T) {
	p := newTestPlugin(t)
	longNS := strings.Repeat("n", 60)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{namespaceTool(longNS, "alpha", "beta")},
		},
	}

	nsMap := p.flattenNamespaceTools(req)

	names := toolNames(req.Params.Tools)
	for _, name := range names {
		if len(name) > maxToolNameLen {
			t.Fatalf("name %q is %d chars, want <= %d", name, len(name), maxToolNameLen)
		}
	}
	// Both tools share a 60-char namespace prefix; truncation must not collapse
	// them onto the same name.
	if names[0] == names[1] {
		t.Fatalf("truncation collapsed distinct tools onto %q", names[0])
	}
	if nsMap.len() != 2 {
		t.Fatalf("mapping has %d entries, want 2", nsMap.len())
	}
}

func TestFlattenNamespaceTools_UnnamedEntriesPassThrough(t *testing.T) {
	p := newTestPlugin(t)
	nsWithoutName := namespaceTool("", "js")
	nsWithoutName.Name = nil
	toolWithoutName := namespaceTool("ns", "js")
	toolWithoutName.ResponsesToolNamespace.Tools[0].Name = nil

	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{nsWithoutName, toolWithoutName},
		},
	}

	nsMap := p.flattenNamespaceTools(req)

	if got := len(req.Params.Tools); got != 2 {
		t.Fatalf("flattened tool count = %d, want 2", got)
	}
	if req.Params.Tools[0].Name == nil || *req.Params.Tools[0].Name != "js" {
		t.Errorf("tool under an unnamed namespace was renamed: %v", toolNames(req.Params.Tools))
	}
	if req.Params.Tools[1].Name != nil {
		t.Errorf("unnamed tool gained a name: %v", *req.Params.Tools[1].Name)
	}
	if nsMap.len() != 0 {
		t.Errorf("mapping = %d entries, want 0", nsMap.len())
	}
}

func TestFlattenNamespaceTools_NoNamespaceToolsReturnsNil(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("plain")},
			},
		},
	}

	if nsMap := p.flattenNamespaceTools(req); nsMap != nil {
		t.Errorf("expected nil map when no namespace tool is present, got %+v", nsMap)
	}
	if got := toolNames(req.Params.Tools); !equalStrings(got, []string{"plain"}) {
		t.Errorf("tools = %v, want [plain] unchanged", got)
	}
}

// ---------------------------------------------------------------------------
// Group 2: providers that natively support namespace tools
// ---------------------------------------------------------------------------

func TestFlattenNamespaceTools_SkipsOpenAI(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{namespaceTool("mcp__node_repl__", "js")},
		},
	}

	nsMap := p.flattenNamespaceTools(req)

	if nsMap != nil {
		t.Errorf("expected nil map for OpenAI, got %+v", nsMap)
	}
	if len(req.Params.Tools) != 1 || req.Params.Tools[0].Type != schemas.ResponsesToolTypeNamespace {
		t.Errorf("OpenAI namespace tool should be left intact, got %+v", req.Params.Tools)
	}
}

func TestFlattenNamespaceTools_SkipsAzureOpenAIModel(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{namespaceTool("ns", "js")},
		},
	}

	if nsMap := p.flattenNamespaceTools(req); nsMap != nil {
		t.Errorf("expected nil map for Azure-hosted OpenAI model, got %+v", nsMap)
	}
	if req.Params.Tools[0].Type != schemas.ResponsesToolTypeNamespace {
		t.Errorf("Azure-hosted OpenAI namespace tool should be left intact")
	}
}

func TestFlattenNamespaceTools_FlattensAzureAnthropicModel(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Azure,
		Model:    "claude-sonnet-4",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{namespaceTool("ns", "js")},
		},
	}

	nsMap := p.flattenNamespaceTools(req)

	if nsMap.len() != 1 {
		t.Fatalf("mapping = %d entries, want 1", nsMap.len())
	}
	if got := toolNames(req.Params.Tools); !equalStrings(got, []string{"ns__js"}) {
		t.Errorf("flattened names = %v, want [ns__js]", got)
	}
}

func TestApplyParameterConversion_StoresNamespaceMap(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newTestContext()
	req := newResponsesRequest(schemas.Anthropic, "claude-sonnet-4", &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{namespaceTool("mcp__node_repl__", "js")},
	})

	p.applyParameterConversion(ctx, req)

	stored, ok := ctx.Value(schemas.BifrostContextKeyCompatNamespaceToolMap).(*namespaceToolMap)
	if !ok {
		t.Fatalf("namespace map not stored in context")
	}
	origin, found := stored.toOrigin["mcp__node_repl__js"]
	if !found || origin.Namespace != "mcp__node_repl__" || origin.OriginalName != "js" {
		t.Errorf("stored mapping = %+v (found=%v), want {mcp__node_repl__ js}", origin, found)
	}
}

// ---------------------------------------------------------------------------
// Group 3: restoring the response
// ---------------------------------------------------------------------------

// testNamespaceMap builds a mapping equivalent to flattening `name` under `namespace`.
func testNamespaceMap(namespace, name, prefixed string) *namespaceToolMap {
	m := newNamespaceToolMap()
	m.add(namespace, name, prefixed)
	return m
}

func TestRestoreNamespaceOnResponse_NonStreaming(t *testing.T) {
	nsMap := testNamespaceMap("mcp__node_repl__", "js", "mcp__node_repl__js")
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{functionCallItem("mcp__node_repl__js")},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	tm := resp.ResponsesResponse.Output[0].ResponsesToolMessage
	if tm.Namespace == nil || *tm.Namespace != "mcp__node_repl__" {
		t.Fatalf("namespace = %v, want mcp__node_repl__", tm.Namespace)
	}
	if tm.Name == nil || *tm.Name != "js" {
		t.Fatalf("name = %v, want js", tm.Name)
	}
}

func TestRestoreNamespaceOnResponse_StreamingItem(t *testing.T) {
	nsMap := testNamespaceMap("mcp__node_repl__", "js", "mcp__node_repl__js")
	item := functionCallItem("mcp__node_repl__js")
	resp := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &item,
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	tm := resp.ResponsesStreamResponse.Item.ResponsesToolMessage
	if tm.Namespace == nil || *tm.Namespace != "mcp__node_repl__" {
		t.Fatalf("streaming item namespace = %v, want mcp__node_repl__", tm.Namespace)
	}
	if tm.Name == nil || *tm.Name != "js" {
		t.Fatalf("streaming item name = %v, want js", tm.Name)
	}
}

func TestRestoreNamespaceOnResponse_StreamingCompletedOutput(t *testing.T) {
	nsMap := testNamespaceMap("mcp__node_repl__", "js", "mcp__node_repl__js")
	resp := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				Output: []schemas.ResponsesMessage{functionCallItem("mcp__node_repl__js")},
			},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	tm := resp.ResponsesStreamResponse.Response.Output[0].ResponsesToolMessage
	if tm.Namespace == nil || *tm.Namespace != "mcp__node_repl__" {
		t.Fatalf("completed output namespace = %v, want mcp__node_repl__", tm.Namespace)
	}
	if tm.Name == nil || *tm.Name != "js" {
		t.Fatalf("completed output name = %v, want js", tm.Name)
	}
}

// A namespace the provider supplied itself wins, but the name must still be
// restored because the provider only ever saw the prefixed form.
func TestRestoreNamespaceOnResponse_DoesNotOverwriteExistingNamespace(t *testing.T) {
	nsMap := testNamespaceMap("mcp__node_repl__", "js", "mcp__node_repl__js")
	item := functionCallItem("mcp__node_repl__js")
	item.ResponsesToolMessage.Namespace = schemas.Ptr("provider_supplied")
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{item},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	tm := resp.ResponsesResponse.Output[0].ResponsesToolMessage
	if tm.Namespace == nil || *tm.Namespace != "provider_supplied" {
		t.Fatalf("namespace = %v, want provider_supplied (not overwritten)", tm.Namespace)
	}
	if tm.Name == nil || *tm.Name != "js" {
		t.Fatalf("name = %v, want js", tm.Name)
	}
}

func TestRestoreNamespaceOnResponse_UnknownToolUntouched(t *testing.T) {
	nsMap := testNamespaceMap("mcp__node_repl__", "js", "mcp__node_repl__js")
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{functionCallItem("not_namespaced")},
		},
	}

	restoreNamespaceOnResponse(resp, nsMap)

	tm := resp.ResponsesResponse.Output[0].ResponsesToolMessage
	if tm.Namespace != nil {
		t.Fatalf("namespace = %v, want nil for a tool that was never namespaced", tm.Namespace)
	}
	if tm.Name == nil || *tm.Name != "not_namespaced" {
		t.Fatalf("name = %v, want not_namespaced", tm.Name)
	}
}

// ---------------------------------------------------------------------------
// Group 4: full round trip
// ---------------------------------------------------------------------------

// TestCompatPlugin_NamespaceRoundTrip exercises the full flatten (PreLLMHook) →
// restore (PostLLMHook) flow across a shared context for both non-streaming and
// streaming responses.
func TestCompatPlugin_NamespaceRoundTrip(t *testing.T) {
	p := newTestPlugin(t)

	ctx := newTestContext()
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-4",
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{
					namespaceTool("ns_a", "search"),
					namespaceTool("ns_b", "search"),
				},
			},
		},
	}

	modifiedReq, _, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	// The request forwarded to the provider must carry two distinctly named
	// function tools, not two identically named ones.
	forwarded := toolNames(modifiedReq.ResponsesRequest.Params.Tools)
	if !equalStrings(forwarded, []string{"ns_a__search", "ns_b__search"}) {
		t.Fatalf("forwarded tools = %v, want [ns_a__search ns_b__search]", forwarded)
	}
	for _, tool := range modifiedReq.ResponsesRequest.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeFunction {
			t.Fatalf("forwarded tool type = %s, want function", tool.Type)
		}
	}
	// The caller's original request must not have been mutated.
	if req.ResponsesRequest.Params.Tools[0].Type != schemas.ResponsesToolTypeNamespace {
		t.Fatalf("caller's tools were mutated: %+v", req.ResponsesRequest.Params.Tools)
	}
	if got := req.ResponsesRequest.Params.Tools[1].ResponsesToolNamespace.Tools[0].Name; got == nil || *got != "search" {
		t.Fatalf("caller's inner tool name was mutated: %v", got)
	}

	// Non-streaming response naming the second namespace's tool.
	nonStream := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{functionCallItem("ns_b__search")},
		},
	}
	nonStream, _, err = p.PostLLMHook(ctx, nonStream, nil)
	if err != nil {
		t.Fatalf("PostLLMHook (non-stream): %v", err)
	}
	tm := nonStream.ResponsesResponse.Output[0].ResponsesToolMessage
	if tm.Namespace == nil || *tm.Namespace != "ns_b" {
		t.Fatalf("non-stream namespace = %v, want ns_b", tm.Namespace)
	}
	if tm.Name == nil || *tm.Name != "search" {
		t.Fatalf("non-stream name = %v, want search", tm.Name)
	}

	// Streaming chunk carrying the function_call item.
	item := functionCallItem("ns_a__search")
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
	tm = stream.ResponsesStreamResponse.Item.ResponsesToolMessage
	if tm.Namespace == nil || *tm.Namespace != "ns_a" {
		t.Fatalf("stream namespace = %v, want ns_a", tm.Namespace)
	}
	if tm.Name == nil || *tm.Name != "search" {
		t.Fatalf("stream name = %v, want search", tm.Name)
	}
}

// ---------------------------------------------------------------------------
// Group 5: rewriting conversation history
// ---------------------------------------------------------------------------

func TestRewriteHistoryToolNames_UsesNamespaceWhenPresent(t *testing.T) {
	p := newTestPlugin(t)
	nsMap := newNamespaceToolMap()
	nsMap.add("ns_a", "search", "ns_a__search")
	nsMap.add("ns_b", "search", "ns_b__search")

	req := &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{historyFunctionCall("ns_b", "search")},
	}

	p.rewriteHistoryToolNames(req, nsMap)

	if got := req.Input[0].ResponsesToolMessage.Name; got == nil || *got != "ns_b__search" {
		t.Fatalf("history name = %v, want ns_b__search", got)
	}
	if got := req.Input[0].ResponsesToolMessage.Namespace; got == nil || *got != "ns_b" {
		t.Fatalf("history namespace = %v, want ns_b preserved", got)
	}
}

func TestRewriteHistoryToolNames_ResolvesBareNameWhenUnambiguous(t *testing.T) {
	p := newTestPlugin(t)
	nsMap := testNamespaceMap("ns_a", "search", "ns_a__search")

	req := &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{historyFunctionCall("", "search")},
	}

	p.rewriteHistoryToolNames(req, nsMap)

	if got := req.Input[0].ResponsesToolMessage.Name; got == nil || *got != "ns_a__search" {
		t.Fatalf("history name = %v, want ns_a__search", got)
	}
}

// With the namespace missing and the name declared by more than one namespace
// there is no safe choice, so the item is left alone.
func TestRewriteHistoryToolNames_LeavesAmbiguousBareNameAlone(t *testing.T) {
	p := newTestPlugin(t)
	nsMap := newNamespaceToolMap()
	nsMap.add("ns_a", "search", "ns_a__search")
	nsMap.add("ns_b", "search", "ns_b__search")

	req := &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{historyFunctionCall("", "search")},
	}

	p.rewriteHistoryToolNames(req, nsMap)

	if got := req.Input[0].ResponsesToolMessage.Name; got == nil || *got != "search" {
		t.Fatalf("history name = %v, want search (unchanged)", got)
	}
}

// cloneBifrostReq does not copy Input, so the rewrite must be copy-on-write or
// it would corrupt the caller's request.
func TestRewriteHistoryToolNames_DoesNotMutateCallerInput(t *testing.T) {
	p := newTestPlugin(t)
	nsMap := testNamespaceMap("ns_a", "search", "ns_a__search")

	original := []schemas.ResponsesMessage{historyFunctionCall("ns_a", "search")}
	sharedItem := original[0].ResponsesToolMessage
	req := &schemas.BifrostResponsesRequest{Input: original}

	p.rewriteHistoryToolNames(req, nsMap)

	if got := sharedItem.Name; got == nil || *got != "search" {
		t.Fatalf("caller's tool message was mutated: name = %v, want search", got)
	}
	if got := original[0].ResponsesToolMessage.Name; got == nil || *got != "search" {
		t.Fatalf("caller's input slice was mutated: name = %v, want search", got)
	}
	if got := req.Input[0].ResponsesToolMessage.Name; got == nil || *got != "ns_a__search" {
		t.Fatalf("rewritten name = %v, want ns_a__search", got)
	}
}

func TestRewriteHistoryToolNames_IgnoresNonFunctionCallAndUnknownTools(t *testing.T) {
	p := newTestPlugin(t)
	nsMap := testNamespaceMap("ns_a", "search", "ns_a__search")

	userMsg := schemas.ResponsesMessage{
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
	}
	// function_call_output carries no name, so it can never be rewritten.
	callOutput := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_1"),
		},
	}
	req := &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{userMsg, callOutput, historyFunctionCall("", "unrelated")},
	}
	inputBefore := req.Input

	p.rewriteHistoryToolNames(req, nsMap)

	if &req.Input[0] != &inputBefore[0] {
		t.Errorf("input slice was cloned even though nothing needed rewriting")
	}
	if req.Input[1].ResponsesToolMessage.Name != nil {
		t.Errorf("function_call_output gained a name: %v", *req.Input[1].ResponsesToolMessage.Name)
	}
	if got := req.Input[2].ResponsesToolMessage.Name; got == nil || *got != "unrelated" {
		t.Fatalf("unknown tool name = %v, want unrelated", got)
	}
}

func TestRewriteHistoryToolNames_NoopWithoutMapping(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{historyFunctionCall("ns_a", "search")},
	}
	inputBefore := req.Input

	p.rewriteHistoryToolNames(req, nil)

	if &req.Input[0] != &inputBefore[0] {
		t.Errorf("input slice was cloned for a nil mapping")
	}
	if got := req.Input[0].ResponsesToolMessage.Name; got == nil || *got != "search" {
		t.Fatalf("name = %v, want search", got)
	}
}

// ---------------------------------------------------------------------------
// Naming helpers
// ---------------------------------------------------------------------------

func TestSanitizeNameSegment(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"with-dash", "with-dash"},
		{"mcp__node_repl__", "mcp__node_repl"},
		{"__leading", "leading"},
		{"my ns/v1", "my_ns_v1"},
		{"a.b:c", "a_b_c"},
		{"日本語", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := sanitizeNameSegment(tt.in); got != tt.want {
			t.Errorf("sanitizeNameSegment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMakePrefixedName(t *testing.T) {
	if got := makePrefixedName("mcp__node_repl__", "bash"); got != "mcp__node_repl__bash" {
		t.Errorf("got %q, want mcp__node_repl__bash", got)
	}
	// An empty namespace leaves the (sanitized) name alone.
	if got := makePrefixedName("", "bash"); got != "bash" {
		t.Errorf("got %q, want bash", got)
	}
	// A name that sanitizes to nothing still yields a usable identifier.
	if got := makePrefixedName("", "日本語"); got != "tool" {
		t.Errorf("got %q, want tool", got)
	}
	// The first character must not be a digit (Gemini rejects those).
	if got := makePrefixedName("1ns", "bash"); got != "_1ns__bash" {
		t.Errorf("got %q, want _1ns__bash", got)
	}
	// Over-long names are truncated but stay distinct.
	long := strings.Repeat("n", 80)
	a := makePrefixedName(long, "alpha")
	b := makePrefixedName(long, "beta")
	if len(a) != maxToolNameLen || len(b) != maxToolNameLen {
		t.Errorf("truncated lengths = %d and %d, want %d", len(a), len(b), maxToolNameLen)
	}
	if a == b {
		t.Errorf("truncation collapsed distinct names onto %q", a)
	}
}

func TestResolveUniqueName(t *testing.T) {
	taken := map[string]struct{}{"ns__search": {}}
	got := resolveUniqueName("ns__search", taken)
	if got == "ns__search" {
		t.Fatalf("resolveUniqueName returned a taken name")
	}
	if len(got) > maxToolNameLen {
		t.Fatalf("resolved name %q is %d chars, want <= %d", got, len(got), maxToolNameLen)
	}
	// A free name is returned untouched.
	if got := resolveUniqueName("ns__other", taken); got != "ns__other" {
		t.Fatalf("got %q, want ns__other", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
