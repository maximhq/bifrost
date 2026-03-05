package bedrock

import (
	"fmt"

	"github.com/bytedance/sonic"
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
		var requestBody map[string]interface{}
		if err := sonic.Unmarshal(rawJSON, &requestBody); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("failed to unmarshal request body: %w", err), providerName)
		}
		if _, exists := requestBody["max_tokens"]; !exists {
			requestBody["max_tokens"] = anthropic.AnthropicDefaultMaxTokens
		}
		if _, exists := requestBody["anthropic_version"]; !exists {
			requestBody["anthropic_version"] = DefaultBedrockAnthropicVersion
		}
		delete(requestBody, "model")
		delete(requestBody, "fallbacks")
		// Do NOT add "stream" to the body — Bedrock uses the endpoint path for streaming
		delete(requestBody, "stream")
		jsonBody, err := sonic.Marshal(requestBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
		return jsonBody, nil
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
	// Handle raw request body passthrough
	if rawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && rawBody {
		rawJSON := request.GetRawRequestBody()
		var requestBody map[string]interface{}
		if err := sonic.Unmarshal(rawJSON, &requestBody); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("failed to unmarshal request body: %w", err), providerName)
		}
		if _, exists := requestBody["max_tokens"]; !exists {
			requestBody["max_tokens"] = anthropic.AnthropicDefaultMaxTokens
		}
		if _, exists := requestBody["anthropic_version"]; !exists {
			requestBody["anthropic_version"] = DefaultBedrockAnthropicVersion
		}
		delete(requestBody, "model")
		delete(requestBody, "fallbacks")
		// Do NOT add "stream" to the body — Bedrock uses the endpoint path for streaming
		delete(requestBody, "stream")
		jsonBody, err := sonic.Marshal(requestBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
		return jsonBody, nil
	}

	// Mutate the model before conversion so converters see the resolved deployment name
	request.Model = deployment
	reqBody, err := anthropic.ToAnthropicResponsesRequest(ctx, request)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
	}
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
	}
	// Do NOT set Stream — Bedrock uses the endpoint path for streaming

	return marshalBedrockAnthropicBody(reqBody, reqBody.GetExtraParams(), ctx, providerName)
}

// marshalBedrockAnthropicBody converts an AnthropicMessageRequest to JSON suitable for
// Bedrock's InvokeModel endpoint. It adds anthropic_version, removes the model field
// (specified in the URL path), and merges extra params if passthrough is enabled.
func marshalBedrockAnthropicBody(reqBody *anthropic.AnthropicMessageRequest, extraParams map[string]interface{}, ctx *schemas.BifrostContext, providerName schemas.ModelProvider) ([]byte, *schemas.BifrostError) {
	reqBytes, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	var requestBody map[string]interface{}
	if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	// Add Bedrock-specific anthropic_version if not already present
	if _, exists := requestBody["anthropic_version"]; !exists {
		requestBody["anthropic_version"] = DefaultBedrockAnthropicVersion
	}

	// Remove model and stream — model is in URL path; streaming is via endpoint path, not body field
	delete(requestBody, "model")
	delete(requestBody, "stream")

	// Merge extra params if passthrough is enabled
	if ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) != nil && ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
		if len(extraParams) > 0 {
			providerUtils.MergeExtraParams(requestBody, extraParams)
		}
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}
	return jsonBody, nil
}
