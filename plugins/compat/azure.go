package compat

import (
	"slices"

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

// isConvertedToResponses reports whether the request has been marked for
// conversion to the Responses API, where reasoning and function tools are
// accepted together.
func isConvertedToResponses(ctx *schemas.BifrostContext) bool {
	changeType, ok := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType)
	return ok && changeType == schemas.ResponsesRequest
}

// shouldConvertChatWithToolsToResponses reports whether a chat completion
// carrying function tools should be served through /responses because the
// model reasons but does not accept reasoning alongside tools on chat
// completions. Azure Foundry's gpt-6-astra / gpt-5.6 deployments reject that
// combination on /chat/completions and also reject reasoning_effort=none, so
// the only wire that serves the request is the Responses API (#7275).
// supportedParams is the model's compat allowlist; a nil list means the
// catalog knows nothing about the model and the request is left alone.
func shouldConvertChatWithToolsToResponses(req *schemas.BifrostRequest, supportedParams []string, responsesSupported bool) bool {
	if req == nil || req.ChatRequest == nil || req.ChatRequest.Params == nil || len(req.ChatRequest.Params.Tools) == 0 {
		return false
	}
	if req.RequestType != schemas.ChatCompletionRequest && req.RequestType != schemas.ChatCompletionStreamRequest {
		return false
	}
	if !responsesSupported || supportedParams == nil {
		return false
	}
	return slices.Contains(supportedParams, "reasoning") &&
		slices.Contains(supportedParams, "tools") &&
		!slices.Contains(supportedParams, "reasoning_with_tool_calls")
}
