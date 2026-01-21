package azure

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, providerName schemas.ModelProvider, isStreaming bool) ([]byte, *schemas.BifrostError) {
	var jsonBody []byte
	var err error

	// Check if raw request body should be used
	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		jsonBody = request.GetRawRequestBody()
		// Unmarshal and check if model and region are present
		var requestBody map[string]interface{}
		if err := sonic.Unmarshal(jsonBody, &requestBody); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("failed to unmarshal request body: %w", err), providerName)
		}
		// Add max_tokens if not present
		if _, exists := requestBody["max_tokens"]; !exists {
			requestBody["max_tokens"] = anthropic.AnthropicDefaultMaxTokens
		}
		// Replace model with deployment
		requestBody["model"] = deployment
		delete(requestBody, "fallbacks")
		// Add stream if not present
		if isStreaming {
			requestBody["stream"] = true
		}
		jsonBody, err = sonic.Marshal(requestBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
	} else {
		// Convert request to Anthropic format
		request.Model = deployment
		reqBody, err := anthropic.ToAnthropicResponsesRequest(ctx, request)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
		}
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
		}

		if isStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		// Convert struct to map
		jsonBody, err = sonic.Marshal(reqBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("failed to marshal request body: %w", err), providerName)
		}
	}

	return jsonBody, nil
}
