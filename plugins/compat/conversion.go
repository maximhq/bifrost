package compat

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// applyParameterConversion rewrites request fields in place for provider compatibility.
func applyParameterConversion(req *schemas.BifrostRequest) {
	if req == nil {
		return
	}
	if req.ResponsesRequest != nil {
		flattenNamespaceTools(req.ResponsesRequest)
		disableThinkingWithToolChoiceForResponses(req.ResponsesRequest)
	}
	if req.ChatRequest != nil {
		disableThinkingWithToolChoice(req.ChatRequest)
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
func flattenNamespaceTools(req *schemas.BifrostResponsesRequest) {
	if req == nil || req.Params == nil {
		return
	}
	// ignore openai models or azure hosted openai models
	if req.Provider == schemas.OpenAI || (req.Provider == schemas.Azure && !schemas.IsAnthropicModel(req.Model)) {
		return
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
		return
	}
	flattened := make([]schemas.ResponsesTool, 0, finalSize)
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace {
			flattened = append(flattened, tool)
		} else if tool.ResponsesToolNamespace != nil && tool.ResponsesToolNamespace.Tools != nil {
			flattened = append(flattened, tool.ResponsesToolNamespace.Tools...)
		}
	}
	req.Params.Tools = flattened
}
