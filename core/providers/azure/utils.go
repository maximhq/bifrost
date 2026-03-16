package azure

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, providerName schemas.ModelProvider, isStreaming bool) ([]byte, *schemas.BifrostError) {
	// Large payload mode: body streams directly from the LP reader — skip all body building
	// (matches CheckContextAndGetRequestBody guard).
	if providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil, nil
	}

	var jsonBody []byte
	var err error

	// Check if raw request body should be used
	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		jsonBody = request.GetRawRequestBody()

		// Add max_tokens if not present (using sjson to preserve key order for prompt caching)
		if !providerUtils.JSONFieldExists(jsonBody, "max_tokens") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "max_tokens", anthropic.AnthropicDefaultMaxTokens)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
		}
		// Replace model with deployment
		jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", deployment)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
		// Delete fallbacks field
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "fallbacks")
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
		// Add stream if streaming
		if isStreaming {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "stream", true)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
		}
	} else {
		// Convert request to Anthropic format
		request.Model = deployment
		reqBody, convErr := anthropic.ToAnthropicResponsesRequest(ctx, request)
		if convErr != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, convErr, providerName)
		}
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
		}

		if isStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		// Marshal struct to JSON bytes, preserving field order
		jsonBody, err = providerUtils.MarshalSorted(reqBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("failed to marshal request body: %w", err), providerName)
		}
	}

	return jsonBody, nil
}

// getCleanedScopes returns cleaned scopes or default scope if none are valid.
// It filters out empty/whitespace-only strings and returns the default scope if no valid scopes remain.
func getAzureScopes(configuredScopes []string) []string {
	scopes := []string{DefaultAzureScope}
	if len(configuredScopes) > 0 {
		cleaned := make([]string, 0, len(configuredScopes))
		for _, s := range configuredScopes {
			if strings.TrimSpace(s) != "" {
				cleaned = append(cleaned, strings.TrimSpace(s))
			}
		}
		if len(cleaned) > 0 {
			scopes = cleaned
		}
	}
	return scopes
}
