package compat

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// applyParameterConversion rewrites request fields in place for provider compatibility.
// Returns the conversions applied, for logging.
func applyParameterConversion(req *schemas.BifrostRequest) []string {
	if req == nil {
		return nil
	}
	var applied []string
	if req.ResponsesRequest != nil {
		if n := flattenNamespaceTools(req.ResponsesRequest); n > 0 {
			applied = append(applied, fmt.Sprintf("flattened %d namespace tool(s)", n))
		}
	}
	return applied
}

// flattenNamespaceTools expands namespace scoped tools into a flat list of tools.
// Returns the number of namespace tools that were flattened.
func flattenNamespaceTools(req *schemas.BifrostResponsesRequest) int {
	if req == nil || req.Params == nil {
		return 0
	}
	// ignore openai models or azure hosted openai models
	if req.Provider == schemas.OpenAI || (req.Provider == schemas.Azure && !schemas.IsAnthropicModel(req.Model)) {
		return 0
	}
	namespaceCount := 0
	finalSize := len(req.Params.Tools)
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace || tool.ResponsesToolNamespace == nil || tool.ResponsesToolNamespace.Tools == nil {
			continue
		}
		finalSize += len(tool.ResponsesToolNamespace.Tools)
		namespaceCount++
	}
	if namespaceCount == 0 {
		return 0
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
	return namespaceCount
}
