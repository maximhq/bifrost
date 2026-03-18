package vertex

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, providerName schemas.ModelProvider, isStreaming bool, isCountTokens bool) ([]byte, *schemas.BifrostError) {
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
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "temperature")
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", deployment)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
		} else {
			// Add max_tokens if not present
			if !providerUtils.JSONFieldExists(jsonBody, "max_tokens") {
				jsonBody, err = providerUtils.SetJSONField(jsonBody, "max_tokens", anthropic.AnthropicDefaultMaxTokens)
				if err != nil {
					return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
				}
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
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
		}

		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "fallbacks")
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}

		// Add anthropic_version if not present
		if !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", DefaultVertexAnthropicVersion)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
		}
	} else {
		// Convert request to Anthropic format
		reqBody, convErr := anthropic.ToAnthropicResponsesRequest(ctx, request)
		if convErr != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, convErr, providerName)
		}
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
		}
		reqBody.Model = deployment

		if isStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		reqBody.SetStripCacheControlScope(true)

		// Marshal struct to JSON bytes
		jsonBody, err = providerUtils.MarshalSorted(reqBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}

		// Add anthropic_version if not present (using sjson to preserve order)
		if !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", DefaultVertexAnthropicVersion)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
		}

		if isCountTokens {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "max_tokens")
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "temperature")
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
		} else {
			// Remove model field for Vertex API (it's in URL)
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
		}

		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
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
func buildResponseFromConfig(deployments map[string]string, allowedModels schemas.WhiteList) *schemas.BifrostListModelsResponse {
	response := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0),
	}

	addedModelIDs := make(map[string]bool)

	restrictAllowed := allowedModels.IsRestricted()

	// First add models from deployments (filtered by allowedModels when set)
	for alias, deploymentValue := range deployments {
		if restrictAllowed && !allowedModels.Contains(alias) {
			continue
		}
		modelID := string(schemas.Vertex) + "/" + alias
		if addedModelIDs[modelID] {
			continue
		}

		modelName := formatDeploymentName(alias)
		modelEntry := schemas.Model{
			ID:         modelID,
			Name:       schemas.Ptr(modelName),
			Deployment: schemas.Ptr(deploymentValue),
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

		modelName := formatDeploymentName(allowedModel)
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
