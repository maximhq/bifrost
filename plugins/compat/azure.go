package compat

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// isAzureDeepSeekResponsesRequest reports whether req is a Responses request
// against a DeepSeek deployment on Azure.
func isAzureDeepSeekResponsesRequest(req *schemas.BifrostRequest) bool {
	if req == nil || req.ResponsesRequest == nil {
		return false
	}
	if req.RequestType != schemas.ResponsesRequest && req.RequestType != schemas.ResponsesStreamRequest {
		return false
	}
	return req.ResponsesRequest.Provider == schemas.Azure && schemas.IsDeepSeekModel(req.ResponsesRequest.Model)
}

// isCodingHarnessRequest reports whether the caller is Claude Code or Codex.
func isCodingHarnessRequest(ctx *schemas.BifrostContext) bool {
	userAgent, _ := ctx.Value(schemas.BifrostContextKeyUserAgent).(string)
	return schemas.ClaudeCLI.Matches(userAgent) ||
		schemas.CodexCLI.Matches(userAgent) ||
		schemas.ClaudeDesktop.Matches(userAgent) ||
		schemas.CodexDesktop.Matches(userAgent) ||
		schemas.Cursor.Matches(userAgent) ||
		schemas.OpenCode.Matches(userAgent)
}

// shouldConvertAzureDeepSeekResponsesToChat reports whether this request should
// be handed to chat completions by core instead of the Responses endpoint.
func shouldConvertAzureDeepSeekResponsesToChat(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) bool {
	return isAzureDeepSeekResponsesRequest(req) && isCodingHarnessRequest(ctx)
}

// isConvertedToChatCompletions reports whether an earlier hook already marked the
// request for conversion to chat completions, where the dropped-on-Responses
// params are supported again.
func isConvertedToChatCompletions(ctx *schemas.BifrostContext) bool {
	changeType, ok := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType)
	return ok && changeType == schemas.ChatCompletionRequest
}