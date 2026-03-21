package compat

import "github.com/maximhq/bifrost/core/schemas"

// applyParameterConversion rewrites request fields in place for provider compatibility.
func applyParameterConversion(req *schemas.BifrostRequest) {
	if req == nil {
		return
	}
	normalizeDeveloperRoleForChatRequest(req)
}

func normalizeDeveloperRoleForChatRequest(req *schemas.BifrostRequest) {
	if req.ChatRequest == nil {
		return
	}
	if req.ChatRequest.Provider != schemas.Bedrock && req.ChatRequest.Provider != schemas.Vertex && req.ChatRequest.Provider != schemas.Gemini {
		return
	}
	for i := range req.ChatRequest.Input {
		if req.ChatRequest.Input[i].Role == schemas.ChatMessageRoleDeveloper {
			req.ChatRequest.Input[i].Role = schemas.ChatMessageRoleSystem
		}
	}
}
