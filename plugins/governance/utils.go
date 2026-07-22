// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
)

// ParseVirtualKeyFromFastHTTPRequest parses the virtual key from FastHTTP request headers.
// Parameters:
//   - req: The FastHTTP request containing headers to parse
//
// Returns:
//   - *string: The virtual key if found, nil otherwise
func ParseVirtualKeyFromFastHTTPRequest(req *fasthttp.RequestCtx) *string {
	vkHeader := string(req.Request.Header.Peek("x-bf-vk"))
	if vkHeader != "" && strings.HasPrefix(strings.ToLower(vkHeader), VirtualKeyPrefix) {
		return bifrost.Ptr(vkHeader)
	}
	authHeader := string(req.Request.Header.Peek("Authorization"))
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
			if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), VirtualKeyPrefix) {
				return bifrost.Ptr(authHeaderValue)
			}
		}
	}
	xAPIKey := string(req.Request.Header.Peek("x-api-key"))
	if xAPIKey != "" && strings.HasPrefix(strings.ToLower(xAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xAPIKey)
	}
	xGoogleAPIKey := string(req.Request.Header.Peek("x-goog-api-key"))
	if xGoogleAPIKey != "" && strings.HasPrefix(strings.ToLower(xGoogleAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xGoogleAPIKey)
	}
	azureAPIKey := string(req.Request.Header.Peek("api-key"))
	if azureAPIKey != "" && strings.HasPrefix(strings.ToLower(azureAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(azureAPIKey)
	}
	return nil
}

// IsModelRequiredForRequest checks if the requested model is required for this request
func IsModelRequiredForRequest(requestType schemas.RequestType) bool {
	// Here we will have to check for some requests which do not need model
	// For example, batches, container, files, videos, passthrough requests
	// For these requests, we will only check for provider filtering
	// Cached content list/retrieve/update/delete target a resource name (cachedContents/{id}),
	// not a model, so they carry no model to filter on; only create binds a cache to a model.
	// Responses retrieve/delete/cancel/input_items target a response_id, not a model.
	if requestType == schemas.ListModelsRequest || requestType == schemas.ListInferenceProfilesRequest || requestType == schemas.MCPToolExecutionRequest || requestType == schemas.BatchCreateRequest || requestType == schemas.BatchListRequest || requestType == schemas.BatchRetrieveRequest || requestType == schemas.BatchCancelRequest || requestType == schemas.BatchResultsRequest || requestType == schemas.FileUploadRequest || requestType == schemas.FileListRequest || requestType == schemas.FileRetrieveRequest || requestType == schemas.FileDeleteRequest || requestType == schemas.FileContentRequest || requestType == schemas.ContainerCreateRequest || requestType == schemas.ContainerListRequest || requestType == schemas.ContainerRetrieveRequest || requestType == schemas.ContainerDeleteRequest || requestType == schemas.ContainerFileCreateRequest || requestType == schemas.ContainerFileListRequest || requestType == schemas.ContainerFileRetrieveRequest || requestType == schemas.ContainerFileContentRequest || requestType == schemas.ContainerFileDeleteRequest || requestType == schemas.CachedContentListRequest || requestType == schemas.CachedContentRetrieveRequest || requestType == schemas.CachedContentUpdateRequest || requestType == schemas.CachedContentDeleteRequest || requestType == schemas.ResponsesRetrieveRequest || requestType == schemas.ResponsesDeleteRequest || requestType == schemas.ResponsesCancelRequest || requestType == schemas.ResponsesInputItemsRequest || requestType == schemas.VideoRetrieveRequest || requestType == schemas.VideoDownloadRequest || requestType == schemas.VideoListRequest || requestType == schemas.VideoDeleteRequest || requestType == schemas.VideoRemixRequest || requestType == schemas.PassthroughRequest || requestType == schemas.PassthroughStreamRequest {
		return false
	}
	return true
}

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	return *w
}

// stampGovernanceCtxFromVK copies team/customer identifiers from the VK onto ctx so
// downstream plugins (logging, observability) see the governance scope.
func stampGovernanceCtxFromVK(ctx *schemas.BifrostContext, vk *configstoreTables.TableVirtualKey) {
	if vk == nil {
		return
	}
	if vk.TeamID != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, *vk.TeamID)
	}
	if vk.Team != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, vk.Team.Name)
		if vk.Team.CustomerID != nil {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, *vk.Team.CustomerID)
			if vk.Team.Customer != nil {
				ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, vk.Team.Customer.Name)
			}
		}
	} else {
		if vk.CustomerID != nil {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, *vk.CustomerID)
		}
		if vk.Customer != nil {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, vk.Customer.Name)
		}
	}
}

// filterModelsForVirtualKey filters models based on virtual key's provider configs
// Returns only models that are allowed by the virtual key's ProviderConfigs
func (p *GovernancePlugin) filterModelsForVirtualKey(
	ctx context.Context,
	models []schemas.Model,
	virtualKeyValue string,
) []schemas.Model {
	// Get virtual key configuration
	vk, exists := p.store.GetVirtualKey(ctx, virtualKeyValue)
	if !exists {
		p.logger.Warn("[Governance] Virtual key not found for list models filtering: %s", virtualKeyValue)
		return []schemas.Model{} // VK not found, return empty list
	}

	// Empty ProviderConfigs means no models are allowed (deny-by-default)
	if len(vk.ProviderConfigs) == 0 {
		return []schemas.Model{}
	}

	// Filter models based on ProviderConfigs
	filteredModels := make([]schemas.Model, 0, len(models))
	for _, model := range models {
		provider, modelName := schemas.ParseModelString(model.ID, "")

		// Pre-pass: if any matching config blacklists the model, block it entirely.
		isBlocked := false
		for _, pc := range vk.ProviderConfigs {
			if pc.Provider == string(provider) && pc.BlacklistedModels.IsBlocked(modelName) {
				isBlocked = true
				break
			}
		}
		if isBlocked {
			continue
		}

		// Allowlist check — model is allowed if any matching config permits it.
		isAllowed := false
		for _, pc := range vk.ProviderConfigs {
			if pc.Provider == string(provider) {
				if p.modelCatalog != nil && p.inMemoryStore != nil {
					providerConfig, ok := p.inMemoryStore.GetConfiguredProviders()[provider]
					providerConfigPtr := &providerConfig
					if !ok {
						providerConfigPtr = nil
					}
					if p.modelCatalog.IsModelAllowedForProvider(provider, modelName, providerConfigPtr, pc.AllowedModels) {
						isAllowed = true
						break
					}
				} else {
					if pc.AllowedModels.IsAllowed(modelName) {
						isAllowed = true
						break
					}
				}
			}
		}

		if isAllowed {
			filteredModels = append(filteredModels, model)
		}
	}

	return filteredModels
}

// filterInferenceProfilesForVirtualKey applies virtual-key model policy to the
// inference profile identifier callers submit to Bedrock runtime endpoints.
func (p *GovernancePlugin) filterInferenceProfilesForVirtualKey(
	ctx context.Context,
	profiles []schemas.BifrostInferenceProfileSummary,
	virtualKeyValue string,
) []schemas.BifrostInferenceProfileSummary {
	models := make([]schemas.Model, 0, len(profiles))
	for _, profile := range profiles {
		models = append(models, schemas.Model{ID: string(schemas.Bedrock) + "/" + profile.InferenceProfileID})
	}
	filteredModels := p.filterModelsForVirtualKey(ctx, models, virtualKeyValue)
	allowed := make(map[string]struct{}, len(filteredModels))
	for _, model := range filteredModels {
		_, modelID := schemas.ParseModelString(model.ID, "")
		allowed[modelID] = struct{}{}
	}
	filteredProfiles := make([]schemas.BifrostInferenceProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		if _, ok := allowed[profile.InferenceProfileID]; ok {
			filteredProfiles = append(filteredProfiles, profile)
		}
	}
	return filteredProfiles
}

// validateRequiredHeaders checks that all configured required headers are present in the request.
// Headers are compared case-insensitively (both sides lowercased).
// Returns a BifrostError with status 400 if any required headers are missing, or nil if all present.
func (p *GovernancePlugin) validateRequiredHeaders(ctx *schemas.BifrostContext) *schemas.BifrostError {
	if p.requiredHeaders == nil || len(*p.requiredHeaders) == 0 {
		return nil
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if headers == nil {
		headers = map[string]string{}
	}
	var missing []string
	for _, h := range *p.requiredHeaders {
		if _, ok := headers[strings.ToLower(h)]; !ok {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		return &schemas.BifrostError{
			Type:       bifrost.Ptr("missing_required_headers"),
			StatusCode: bifrost.Ptr(400),
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("missing required headers: %s", strings.Join(missing, ", ")),
			},
		}
	}
	return nil
}
