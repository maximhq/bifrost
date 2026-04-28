package vertex

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// getRequestBodyForAnthropicResponses serializes a BifrostResponsesRequest into the Anthropic wire format for Vertex AI.
// Compared to the native Anthropic path, it strips model/region fields, remaps tool versions, injects beta headers
// into the request body (rather than HTTP headers), and pins the Anthropic API version to DefaultVertexAnthropicVersion.
func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, isStreaming bool, isCountTokens bool, betaHeaderOverrides map[string]bool, providerExtraHeaders map[string]string, shouldSendBackRawRequest bool, shouldSendBackRawResponse bool) ([]byte, *schemas.BifrostError) {
	jsonBody, buildErr := anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
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
	if buildErr != nil {
		return nil, buildErr
	}
	stripped, err := anthropic.StripUnsupportedFieldsFromRawBody(jsonBody, schemas.Vertex, deployment)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(err.Error(), nil)
	}
	return stripped, nil
}

// isVertexMultiRegionEndpoint reports whether the Vertex location uses Google's
// partner-model multi-region pool endpoint host instead of the single-region host.
func isVertexMultiRegionEndpoint(region string) bool {
	return region == "us" || region == "eu"
}

// getVertexAPIHost returns the Vertex API host used for prediction requests.
// For multi-region pool locations (us/eu), returns the rep.googleapis.com host
// unconditionally. Use getVertexModelAwareAPIHost when model-level gating is needed.
func getVertexAPIHost(region string) string {
	if region == "global" {
		return "aiplatform.googleapis.com"
	}
	if isVertexMultiRegionEndpoint(region) {
		return fmt.Sprintf("aiplatform.%s.rep.googleapis.com", region)
	}
	return fmt.Sprintf("%s-aiplatform.googleapis.com", region)
}

// getVertexModelAwareAPIHost returns the Vertex API host for prediction requests,
// consulting the model catalog when the region is a standard single-region location.
//
// For multi-region pool locations ("us", "eu") the rep.googleapis.com host is
// always returned because it is the only valid host for those locations.
//
// For single-region locations (e.g. "us-central1"), models flagged with
// vertex_multi_region_only in the datasheet are automatically promoted to
// the corresponding multi-region pool endpoint — but only for US (us-*) and
// Europe (europe-*) regions that have multi-region pools. Other regions
// (asia-*, me-*, etc.) stay on the single-region host.
func getVertexModelAwareAPIHost(region string, model string) string {
	if region == "global" {
		return "aiplatform.googleapis.com"
	}
	if isVertexMultiRegionEndpoint(region) {
		// rep.googleapis.com is the only valid host for "us"/"eu" locations
		return fmt.Sprintf("aiplatform.%s.rep.googleapis.com", region)
	}
	// Single-region: promote to multi-region pool if the model requires it
	// and the region belongs to a pool that supports multi-region.
	if providerUtils.IsVertexMultiRegionOnlyModel(model) {
		if pool, ok := vertexRegionToPool(region); ok {
			return fmt.Sprintf("aiplatform.%s.rep.googleapis.com", pool)
		}
	}
	return fmt.Sprintf("%s-aiplatform.googleapis.com", region)
}

// vertexRegionToPool maps a single GCP region to its multi-region pool ("us" or "eu").
// Returns (pool, true) for regions that belong to a known pool, or ("", false)
// for regions that have no multi-region pool (asia-*, me-*, etc.).
func vertexRegionToPool(region string) (string, bool) {
	if strings.HasPrefix(region, "us-") {
		return "us", true
	}
	if strings.HasPrefix(region, "europe-") {
		return "eu", true
	}
	return "", false
}

// getVertexModelListingAPIHost returns the Vertex API host used for Model Garden listing.
// The multi-region prediction hosts reject publishers.models.list, so listing stays on the standard Vertex API host.
func getVertexModelListingAPIHost(region string) string {
	if region == "global" || isVertexMultiRegionEndpoint(region) {
		return "aiplatform.googleapis.com"
	}
	return fmt.Sprintf("%s-aiplatform.googleapis.com", region)
}

func getVertexAPIBaseURL(region string, apiVersion string) string {
	return fmt.Sprintf("https://%s/%s", getVertexAPIHost(region), apiVersion)
}

// getVertexModelAwareAPIBaseURL is like getVertexAPIBaseURL but uses model-aware
// host selection for multi-region endpoints.
func getVertexModelAwareAPIBaseURL(region string, apiVersion string, model string) string {
	return fmt.Sprintf("https://%s/%s", getVertexModelAwareAPIHost(region, model), apiVersion)
}

func getVertexProjectLocationURL(region string, apiVersion string, projectID string) string {
	return fmt.Sprintf("%s/projects/%s/locations/%s", getVertexAPIBaseURL(region, apiVersion), projectID, region)
}

func getVertexPublisherModelURL(region string, apiVersion string, projectID string, publisher string, model string, method string) string {
	return fmt.Sprintf("%s/publishers/%s/models/%s%s", getVertexProjectLocationURL(region, apiVersion, projectID), publisher, model, method)
}

// getVertexModelAwarePublisherModelURL is like getVertexPublisherModelURL but
// uses model-aware host selection. Use this for partner model (Anthropic, Mistral)
// inference endpoints that may need multi-region pool hosts.
// When a single-region is promoted to multi-region, both the host AND the
// locations/ path segment are updated to the pool region.
func getVertexModelAwarePublisherModelURL(region string, apiVersion string, projectID string, publisher string, model string, method string) string {
	effectiveRegion := getVertexEffectiveRegion(region, model)
	baseURL := fmt.Sprintf("https://%s/%s", getVertexModelAwareAPIHost(region, model), apiVersion)
	return fmt.Sprintf("%s/projects/%s/locations/%s/publishers/%s/models/%s%s", baseURL, projectID, effectiveRegion, publisher, model, method)
}

// getVertexEffectiveRegion returns the region to use in URL path segments.
// For multi-region locations it returns the region as-is. For single-region
// locations it returns the multi-region pool if the model is flagged, otherwise
// the original region.
func getVertexEffectiveRegion(region string, model string) string {
	if isVertexMultiRegionEndpoint(region) || region == "global" {
		return region
	}
	if providerUtils.IsVertexMultiRegionOnlyModel(model) {
		if pool, ok := vertexRegionToPool(region); ok {
			return pool
		}
	}
	return region
}

func getVertexEndpointURL(region string, apiVersion string, projectID string, endpoint string, method string) string {
	return fmt.Sprintf("%s/endpoints/%s%s", getVertexProjectLocationURL(region, apiVersion, projectID), endpoint, method)
}

// getCompleteURLForGeminiEndpoint constructs the complete URL for the Gemini endpoint, for both streaming and non-streaming requests
// for custom/fine-tuned models, it uses the projectNumber
// for gemini models, it uses the projectID
func getCompleteURLForGeminiEndpoint(deployment string, region string, projectID string, projectNumber string, method string) string {
	deployment = gemini.NormalizeModelName(deployment)
	if schemas.IsAllDigitsASCII(deployment) {
		// Custom/fine-tuned models use projectNumber
		return getVertexEndpointURL(region, "v1beta1", projectNumber, deployment, method)
	}

	// Gemini models use projectID
	return getVertexPublisherModelURL(region, "v1", projectID, "google", deployment, method)
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
