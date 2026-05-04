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
	}
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
