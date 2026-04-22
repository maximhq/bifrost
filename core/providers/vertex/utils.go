package vertex

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func enrichBifrostOperationError(
	ctx *schemas.BifrostContext,
	message string,
	err error,
	requestBody []byte,
	responseBody []byte,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
) *schemas.BifrostError {
	return providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(message, err), requestBody, responseBody, sendBackRawRequest, sendBackRawResponse)
}

func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, isStreaming bool, isCountTokens bool, betaHeaderOverrides map[string]bool, providerExtraHeaders map[string]string, shouldSendBackRawRequest bool, shouldSendBackRawResponse bool) ([]byte, *schemas.BifrostError) {
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

		if isCountTokens {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "max_tokens")
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "temperature")
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", deployment)
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
		} else {
			// Add max_tokens if not present
			if !providerUtils.JSONFieldExists(jsonBody, "max_tokens") {
				jsonBody, err = providerUtils.SetJSONField(jsonBody, "max_tokens", providerUtils.GetMaxOutputTokensOrDefault(deployment, anthropic.AnthropicDefaultMaxTokens))
				if err != nil {
					return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
				}
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
			// Add stream if streaming
			if isStreaming {
				jsonBody, err = providerUtils.SetJSONField(jsonBody, "stream", true)
				if err != nil {
					return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
				}
			}
		}

		// Strip auto-injectable server-side tools to prevent conflicts with API auto-injection
		jsonBody, err = anthropic.StripAutoInjectableTools(jsonBody)
		if err != nil {
			return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}

		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
		if err != nil {
			return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}

		// Remap unsupported tool versions for Vertex (e.g., web_search_20260209 → web_search_20250305)
		jsonBody, err = anthropic.RemapRawToolVersionsForProvider(jsonBody, schemas.Vertex)
		if err != nil {
			return nil, enrichBifrostOperationError(ctx, err.Error(), nil, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}

		// Strip unsupported fields for Vertex — parity with the typed path's stripUnsupportedAnthropicFields.
		jsonBody, err = anthropic.StripUnsupportedFieldsFromRawBody(jsonBody, schemas.Vertex, "")
		if err != nil {
			return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}

		// Add anthropic_version if not present
		if !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", DefaultVertexAnthropicVersion)
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
		}

		// Probe-unmarshal to auto-inject any beta headers required by fields that survived stripping.
		// Mirrors the Anthropic typed path so raw-body callers don't need to specify headers manually.
		var probe anthropic.AnthropicMessageRequest
		if unmarshalErr := schemas.Unmarshal(jsonBody, &probe); unmarshalErr == nil {
			anthropic.AddMissingBetaHeadersToContext(ctx, &probe, schemas.Vertex)
		}
	} else {
		// Validate tools are supported by Vertex
		if request.Params != nil && request.Params.Tools != nil {
			if toolErr := anthropic.ValidateToolsForProvider(request.Params.Tools, schemas.Vertex); toolErr != nil {
				return nil, enrichBifrostOperationError(ctx, toolErr.Error(), nil, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
		}

		// Convert request to Anthropic format
		reqBody, convErr := anthropic.ToAnthropicResponsesRequest(ctx, request)
		if convErr != nil {
			return nil, enrichBifrostOperationError(ctx, schemas.ErrRequestBodyConversion, convErr, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}
		if reqBody == nil {
			return nil, enrichBifrostOperationError(ctx, "request body is not provided", nil, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}
		reqBody.Model = deployment

		if isStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		reqBody.SetStripCacheControlScope(true)

		// Add provider-aware beta headers
		anthropic.AddMissingBetaHeadersToContext(ctx, reqBody, schemas.Vertex)

		// Marshal struct to JSON bytes
		jsonBody, err = providerUtils.MarshalSorted(reqBody)
		if err != nil {
			return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}

		if passthrough, _ := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams).(bool); passthrough {
			extraParams := reqBody.GetExtraParams()
			if len(extraParams) > 0 {
				// Use MergeExtraParamsIntoJSON which preserves key order
				jsonBody, err = providerUtils.MergeExtraParamsIntoJSON(jsonBody, extraParams)
				if err != nil {
					return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
				}
			}
		}

		// Add anthropic_version if not present (using sjson to preserve order)
		if !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", DefaultVertexAnthropicVersion)
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
		}

		if isCountTokens {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "max_tokens")
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "temperature")
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
		} else {
			// Remove model field for Vertex API (it's in URL)
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
			if err != nil {
				return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
			}
		}

		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
		if err != nil {
			return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}
	}

	// Delete fallbacks field (both raw and typed paths)
	jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "fallbacks")
	if err != nil {
		return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
	}

	if betaHeaders := anthropic.FilterBetaHeadersForProvider(anthropic.MergeBetaHeaders(providerExtraHeaders, ctx), schemas.Vertex, betaHeaderOverrides); len(betaHeaders) > 0 {
		jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_beta", betaHeaders)
		if err != nil {
			return nil, enrichBifrostOperationError(ctx, schemas.ErrProviderRequestMarshal, err, jsonBody, nil, shouldSendBackRawRequest, shouldSendBackRawResponse)
		}
	}

	return jsonBody, nil
}

// getCompleteURLForGeminiEndpoint constructs the complete URL for the Gemini endpoint, for both streaming and non-streaming requests
// for custom/fine-tuned models, it uses the projectNumber
// for gemini models, it uses the projectID
func getCompleteURLForGeminiEndpoint(deployment string, region string, projectID string, projectNumber string, method string) string {
	var url string
	if schemas.IsAllDigitsASCII(deployment) {
		// Custom/fine-tuned models use projectNumber
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/%s%s", projectNumber, deployment, method)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/%s%s", region, projectNumber, region, deployment, method)
		}
	} else {
		// Gemini models use projectID
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s%s", projectID, deployment, method)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s%s", region, projectID, region, deployment, method)
		}
	}
	return url
}

// buildResponseFromConfig builds a list models response from configured deployments and allowedModels.
// This is used when the user has explicitly configured which models they want to use.
func buildResponseFromConfig(deployments map[string]string, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList) *schemas.BifrostListModelsResponse {
	response := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0),
	}

	if blacklistedModels.IsBlockAll() {
		return response
	}

	addedModelIDs := make(map[string]bool)

	restrictAllowed := allowedModels.IsRestricted()

	// First add models from deployments (filtered by allowedModels when set)
	for alias, deploymentValue := range deployments {
		if restrictAllowed && !allowedModels.Contains(alias) {
			continue
		}
		if blacklistedModels.IsBlocked(alias) {
			continue
		}
		modelID := string(schemas.Vertex) + "/" + alias
		if addedModelIDs[modelID] {
			continue
		}

		modelName := providerUtils.ToDisplayName(alias)
		modelEntry := schemas.Model{
			ID:    modelID,
			Name:  schemas.Ptr(modelName),
			Alias: schemas.Ptr(deploymentValue),
		}

		response.Data = append(response.Data, modelEntry)
		addedModelIDs[modelID] = true
	}

	// Then add models from allowedModels that aren't already in deployments (only when restricted)
	if !restrictAllowed {
		return response
	}
	for _, allowedModel := range allowedModels {
		modelID := string(schemas.Vertex) + "/" + allowedModel
		if addedModelIDs[modelID] {
			continue
		}
		if blacklistedModels.IsBlocked(allowedModel) {
			continue
		}

		modelName := providerUtils.ToDisplayName(allowedModel)
		modelEntry := schemas.Model{
			ID:   modelID,
			Name: schemas.Ptr(modelName),
		}

		response.Data = append(response.Data, modelEntry)
		addedModelIDs[modelID] = true
	}

	return response
}

// extractModelIDFromName extracts the model ID from a full resource name.
// Format: "publishers/google/models/gemini-1.5-pro" -> "gemini-1.5-pro"
func extractModelIDFromName(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) >= 4 && parts[2] == "models" {
		return parts[3]
	}
	// Fallback: return last segment
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
