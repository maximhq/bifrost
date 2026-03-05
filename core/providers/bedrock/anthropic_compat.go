package bedrock

import (
	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// getBedrockAnthropicChatRequestBody prepares the Anthropic Messages API-compatible request body
// for Bedrock's InvokeModel endpoint. It adds the required anthropic_version body field and
// removes the model field (which is specified in the URL path, not the body).
// Note: streaming is determined by the URL endpoint (invoke vs invoke-with-response-stream),
// NOT by a "stream" field in the request body — so isStreaming only affects caller routing.
func getBedrockAnthropicChatRequestBody(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, deployment string, providerName schemas.ModelProvider) ([]byte, *schemas.BifrostError) {
	// Handle raw request body passthrough
	if rawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && rawBody {
		rawJSON := request.GetRawRequestBody()
		if !gjson.GetBytes(rawJSON, "max_tokens").Exists() {
			rawJSON, _ = sjson.SetBytes(rawJSON, "max_tokens", anthropic.AnthropicDefaultMaxTokens)
		}
		if !gjson.GetBytes(rawJSON, "anthropic_version").Exists() {
			rawJSON, _ = sjson.SetBytes(rawJSON, "anthropic_version", DefaultBedrockAnthropicVersion)
		}
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "model")
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "fallbacks")
		// Do NOT add "stream" to the body — Bedrock uses the endpoint path for streaming
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "stream")
		return rawJSON, nil
	}

	reqBody, err := anthropic.ToAnthropicChatRequest(ctx, request)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
	}
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
	}
	reqBody.Model = deployment
	// Do NOT set Stream — Bedrock uses the endpoint path for streaming

	return marshalBedrockAnthropicBody(reqBody, reqBody.GetExtraParams(), ctx, providerName)
}

// getBedrockAnthropicResponsesRequestBody prepares the Anthropic Messages API-compatible request body
// for Bedrock's InvokeModel endpoint when handling Responses API requests.
// Note: streaming is determined by the URL endpoint, NOT a "stream" body field.
func getBedrockAnthropicResponsesRequestBody(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, providerName schemas.ModelProvider) ([]byte, *schemas.BifrostError) {
	// Validate tools are supported by Bedrock before building the request
	if request.Params != nil && len(request.Params.Tools) > 0 {
		if toolErr := anthropic.ValidateToolsForProvider(request.Params.Tools, schemas.Bedrock); toolErr != nil {
			return nil, providerUtils.NewBifrostOperationError(toolErr.Error(), nil, providerName)
		}
	}

	// Handle raw request body passthrough
	if rawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && rawBody {
		rawJSON := request.GetRawRequestBody()
		if !gjson.GetBytes(rawJSON, "max_tokens").Exists() {
			rawJSON, _ = sjson.SetBytes(rawJSON, "max_tokens", anthropic.AnthropicDefaultMaxTokens)
		}
		if !gjson.GetBytes(rawJSON, "anthropic_version").Exists() {
			rawJSON, _ = sjson.SetBytes(rawJSON, "anthropic_version", DefaultBedrockAnthropicVersion)
		}
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "model")
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "fallbacks")
		// Do NOT add "stream" to the body — Bedrock uses the endpoint path for streaming
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "stream")
		return rawJSON, nil
	}

	reqBody, err := anthropic.ToAnthropicResponsesRequest(ctx, request)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
	}
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
	}
	reqBody.Model = deployment
	// Do NOT set Stream — Bedrock uses the endpoint path for streaming

	return marshalBedrockAnthropicBody(reqBody, reqBody.GetExtraParams(), ctx, providerName)
}

// marshalBedrockAnthropicBody converts an AnthropicMessageRequest to JSON suitable for
// Bedrock's InvokeModel endpoint. It adds anthropic_version, removes the model field
// (specified in the URL path), and merges extra params if passthrough is enabled.
func marshalBedrockAnthropicBody(reqBody *anthropic.AnthropicMessageRequest, extraParams map[string]interface{}, ctx *schemas.BifrostContext, providerName schemas.ModelProvider) ([]byte, *schemas.BifrostError) {
	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	// Add Bedrock-specific anthropic_version if not already present
	if !gjson.GetBytes(jsonBody, "anthropic_version").Exists() {
		jsonBody, _ = sjson.SetBytes(jsonBody, "anthropic_version", DefaultBedrockAnthropicVersion)
	}

	// Remove model and stream — model is in URL path; streaming is via endpoint path, not body field
	jsonBody, _ = sjson.DeleteBytes(jsonBody, "model")
	jsonBody, _ = sjson.DeleteBytes(jsonBody, "stream")

	// Merge extra params if passthrough is enabled
	if ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) != nil && ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
		if len(extraParams) > 0 {
			jsonBody, err = providerUtils.MergeExtraParamsIntoJSON(jsonBody, extraParams)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
			// Keep Bedrock Anthropic invariants intact after merge
			jsonBody, _ = sjson.DeleteBytes(jsonBody, "model")
			jsonBody, _ = sjson.DeleteBytes(jsonBody, "stream")
			jsonBody, _ = sjson.DeleteBytes(jsonBody, "fallbacks")
		}
	}

	return jsonBody, nil
}
