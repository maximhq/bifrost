package compat

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// applyParameterConversion rewrites request fields in place for provider compatibility.
func applyParameterConversion(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) {
	if req == nil {
		return
	}
	if req.ResponsesRequest != nil {
		namespaceByTool := flattenNamespaceTools(req.ResponsesRequest)
		// Always record the (possibly nil) mapping for the current attempt so a
		// later fallback that does not flatten (e.g. OpenAI) does not observe a
		// stale mapping from a previous attempt in PostLLMHook.
		if ctx != nil {
			ctx.SetValue(schemas.BifrostContextKeyCompatNamespaceToolMap, namespaceByTool)
		}
		disableThinkingWithToolChoiceForResponses(req.ResponsesRequest)
	}
	if req.ChatRequest != nil {
		disableThinkingWithToolChoice(req.ChatRequest)
	}
}

// restoreNamespaceOnResponse re-attaches the namespace to function_call items in a
// responses result (streaming or non-streaming). When flattenNamespaceTools expands
// namespace-scoped tools for providers that don't support the "namespace" tool type,
// the provider returns function_call items without the namespace field. Clients that
// rely on namespace tools (e.g. Codex) identify a tool by its (namespace, name) pair,
// so a missing namespace surfaces as an "unsupported tool <name>" error. This restores
// the namespace that flattening stripped.
func restoreNamespaceOnResponse(result *schemas.BifrostResponse, namespaceByTool map[string]string) {
	if result == nil || len(namespaceByTool) == 0 {
		return
	}
	if result.ResponsesResponse != nil {
		restoreNamespaceOnMessages(result.ResponsesResponse.Output, namespaceByTool)
	}
	if stream := result.ResponsesStreamResponse; stream != nil {
		// output_item.added / output_item.done events carry the function_call item.
		restoreNamespaceOnMessage(stream.Item, namespaceByTool)
		// response.completed carries the full output array.
		if stream.Response != nil {
			restoreNamespaceOnMessages(stream.Response.Output, namespaceByTool)
		}
	}
}

// restoreNamespaceOnMessages re-attaches namespaces to every function_call item in the slice.
func restoreNamespaceOnMessages(messages []schemas.ResponsesMessage, namespaceByTool map[string]string) {
	for i := range messages {
		restoreNamespaceOnMessage(&messages[i], namespaceByTool)
	}
}

// restoreNamespaceOnMessage re-attaches the namespace to a single function_call item when
// its name maps to a flattened namespace and the provider did not already supply one.
func restoreNamespaceOnMessage(msg *schemas.ResponsesMessage, namespaceByTool map[string]string) {
	if msg == nil || msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeFunctionCall {
		return
	}
	tm := msg.ResponsesToolMessage
	if tm == nil || tm.Name == nil {
		return
	}
	// Never overwrite a namespace the provider already supplied.
	if tm.Namespace != nil && *tm.Namespace != "" {
		return
	}
	if namespace, ok := namespaceByTool[*tm.Name]; ok && namespace != "" {
		tm.Namespace = &namespace
	}
}

// disableThinkingWithToolChoice disables thinking when tool_choice forces a tool call.
func disableThinkingWithToolChoice(req *schemas.BifrostChatRequest) {
	if req.Provider != schemas.DeepSeek || req.Params == nil || req.Params.ToolChoice == nil {
		return
	}
	tc := req.Params.ToolChoice
	if tc.ChatToolChoiceStr != nil && *tc.ChatToolChoiceStr == string(schemas.ChatToolChoiceTypeRequired) {
		req.Params.ExtraParams = disableThinking(req.Params.ExtraParams)
	}
}

// disableThinkingWithToolChoiceForResponses disables thinking when tool_choice forces a tool call.
func disableThinkingWithToolChoiceForResponses(req *schemas.BifrostResponsesRequest) {
	if req.Provider != schemas.DeepSeek || req.Params == nil || req.Params.ToolChoice == nil {
		return
	}
	tc := req.Params.ToolChoice
	if tc.ResponsesToolChoiceStr != nil && *tc.ResponsesToolChoiceStr == string(schemas.ResponsesToolChoiceTypeRequired) {
		req.Params.ExtraParams = disableThinking(req.Params.ExtraParams)
	}
}

// disableThinking sets thinking {"type": "disabled"} in extraParams, overwriting
// any caller-provided value. DeepSeek models run with thinking enabled by default,
// and thinking mode rejects forced tool_choice — so a forced tool call requires
// thinking off.
func disableThinking(extraParams map[string]any) map[string]any {
	if extraParams == nil {
		extraParams = make(map[string]any, 1)
	}
	extraParams["thinking"] = map[string]any{"type": "disabled"}
	return extraParams
}

// flattenNamespaceTools expands namespace scoped tools into a flat list of tools.
// It returns a map from each expanded tool's name to its originating namespace
// name, so the namespace can be re-attached to the provider's function_call
// items later (see restoreNamespaceOnResponse). Returns nil when no namespace
// tool was flattened.
func flattenNamespaceTools(req *schemas.BifrostResponsesRequest) map[string]string {
	if req == nil || req.Params == nil {
		return nil
	}
	// ignore openai models or azure hosted openai models
	if req.Provider == schemas.OpenAI || (req.Provider == schemas.Azure && !schemas.IsAnthropicModel(req.Model)) {
		return nil
	}
	hasNamespace := false
	finalSize := len(req.Params.Tools)
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace || tool.ResponsesToolNamespace == nil || tool.ResponsesToolNamespace.Tools == nil {
			continue
		}
		finalSize += len(tool.ResponsesToolNamespace.Tools)
		hasNamespace = true
	}
	if !hasNamespace {
		return nil
	}
	var namespaceByTool map[string]string
	flattened := make([]schemas.ResponsesTool, 0, finalSize)
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace {
			flattened = append(flattened, tool)
			continue
		}
		if tool.ResponsesToolNamespace == nil || tool.ResponsesToolNamespace.Tools == nil {
			continue
		}
		namespace := ""
		if tool.Name != nil {
			namespace = *tool.Name
		}
		for _, inner := range tool.ResponsesToolNamespace.Tools {
			flattened = append(flattened, inner)
			if namespace == "" || inner.Name == nil || *inner.Name == "" {
				continue
			}
			if namespaceByTool == nil {
				namespaceByTool = make(map[string]string)
			}
			namespaceByTool[*inner.Name] = namespace
		}
	}
	req.Params.Tools = flattened
	return namespaceByTool
}
