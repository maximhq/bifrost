package vertex

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// getRequestBodyForAnthropicResponses serializes a BifrostResponsesRequest into the Anthropic wire format for Vertex AI.
// Compared to the native Anthropic path, it strips model/region fields, remaps tool versions, injects beta headers
// into the request body (rather than HTTP headers), and pins the Anthropic API version to DefaultVertexAnthropicVersion.
func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, isStreaming bool, isCountTokens bool, betaHeaderOverrides map[string]bool, providerExtraHeaders map[string]string, shouldSendBackRawRequest bool, shouldSendBackRawResponse bool) ([]byte, *schemas.BifrostError) {
	return anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.Vertex,
		Deployment:                deployment,
		DeleteModelField:          true,
		DeleteRegionField:         true,
		IsStreaming:               isStreaming,
		IsCountTokens:             isCountTokens,
		AddAnthropicVersion:       true,
		AnthropicVersion:          DefaultVertexAnthropicVersion,
		StripCacheControlScope:    true,
		RemapToolVersions:         true,
		InjectBetaHeadersIntoBody: true,
		BetaHeaderOverrides:       betaHeaderOverrides,
		ProviderExtraHeaders:      providerExtraHeaders,
		ValidateTools:             true,
		ShouldSendBackRawRequest:  shouldSendBackRawRequest,
		ShouldSendBackRawResponse: shouldSendBackRawResponse,
	})
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
