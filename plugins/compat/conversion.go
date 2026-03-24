package compat

import "github.com/maximhq/bifrost/core/schemas"

// applyParameterConversion rewrites request fields in place for provider compatibility.
func applyParameterConversion(req *schemas.BifrostRequest) {
	if req == nil {
		return
	}

	if req.ChatRequest != nil {
		normalizeDeveloperRoleForChatRequest(req.ChatRequest)
	}
}

func normalizeDeveloperRoleForChatRequest(req *schemas.BifrostChatRequest) {
	if req.Provider != schemas.Bedrock && req.Provider != schemas.Vertex && req.Provider != schemas.Gemini {
		return
	}
	for i := range req.Input {
		if req.Input[i].Role == schemas.ChatMessageRoleDeveloper {
			req.Input[i].Role = schemas.ChatMessageRoleSystem
		}
	}
}
