package compat

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// applyParameterConversion rewrites request fields in place for provider compatibility.
func (p *CompatPlugin) applyParameterConversion(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) {
	if req == nil {
		return
	}
	if req.ResponsesRequest != nil {
		nsMap := p.flattenNamespaceTools(req.ResponsesRequest)
		// Historical function_call items still carry the pre-flattening names, so
		// bring them in line with the tool list this turn declares.
		p.rewriteHistoryToolNames(req.ResponsesRequest, nsMap)
		// Always record the (possibly nil) mapping for the current attempt so a
		// later fallback that does not flatten (e.g. OpenAI) does not observe a
		// stale mapping from a previous attempt in PostLLMHook.
		if ctx != nil {
			ctx.SetValue(schemas.BifrostContextKeyCompatNamespaceToolMap, nsMap)
		}
	}
}
