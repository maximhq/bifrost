package bedrock

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const bedrockInferenceProfilesPath = "inference-profiles"

// ListInferenceProfiles lists Bedrock inference profiles using the first
// eligible key selected by core. Unlike ListModels, this deliberately does not
// merge results from multiple keys: AWS pagination tokens belong to one
// credential context and must be forwarded unchanged to the client.
func (provider *BedrockProvider) ListInferenceProfiles(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListInferenceProfilesRequest) (*schemas.BifrostListInferenceProfilesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(provider.GetProviderKey(), provider.customProviderConfig, schemas.ListInferenceProfilesRequest); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("no Bedrock keys available to list inference profiles", nil)
	}

	return provider.listInferenceProfilesByKey(ctx, keys[0], request)
}

// GetInferenceProfile retrieves a single Bedrock inference profile. Core has
// already selected key based on the profile identifier, so a guessed profile
// cannot bypass normal model allow/deny policy.
func (provider *BedrockProvider) GetInferenceProfile(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostGetInferenceProfileRequest) (*schemas.BifrostGetInferenceProfileResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(provider.GetProviderKey(), provider.customProviderConfig, schemas.GetInferenceProfileRequest); err != nil {
		return nil, err
	}
	if !inferenceProfileAllowedForKey(request.InferenceProfileIdentifier, key) {
		return nil, providerUtils.NewBifrostOperationError("inference profile is not allowed for the selected Bedrock key", nil)
	}

	region := resolveBedrockRegion(ctx, key, request.InferenceProfileIdentifier)
	path := bedrockInferenceProfilesPath + "/" + url.PathEscape(request.InferenceProfileIdentifier)
	body, latency, headers, bifrostErr := provider.executeInferenceProfileRequest(ctx, key, region, path, nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostGetInferenceProfileResponse{}
	if err := sonic.Unmarshal(body, response); err != nil {
		return nil, providerUtils.NewBifrostOperationError("error parsing Bedrock inference profile response", err)
	}
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = headers
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError("error parsing raw Bedrock inference profile response", err)
		}
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

func (provider *BedrockProvider) listInferenceProfilesByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListInferenceProfilesRequest) (*schemas.BifrostListInferenceProfilesResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, "")
	params := url.Values{}
	if request.MaxResults != nil {
		params.Set("maxResults", fmt.Sprintf("%d", *request.MaxResults))
	}
	if request.NextToken != nil && *request.NextToken != "" {
		params.Set("nextToken", *request.NextToken)
	}
	if request.Type != nil && *request.Type != "" {
		params.Set("type", *request.Type)
	}

	body, latency, headers, bifrostErr := provider.executeInferenceProfileRequest(ctx, key, region, bedrockInferenceProfilesPath, params)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostListInferenceProfilesResponse{}
	if err := sonic.Unmarshal(body, response); err != nil {
		return nil, providerUtils.NewBifrostOperationError("error parsing Bedrock inference profiles response", err)
	}
	response.InferenceProfileSummaries = filterInferenceProfilesForKey(response.InferenceProfileSummaries, key)
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = headers
	// Do not include the raw AWS response here. It contains unfiltered profiles
	// and would bypass both provider-key and virtual-key model policy filtering.
	return response, nil
}

func (provider *BedrockProvider) executeInferenceProfileRequest(ctx *schemas.BifrostContext, key schemas.Key, region, path string, params url.Values) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	requestURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/%s", region, path)
	if len(params) > 0 {
		requestURL += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0, nil, providerUtils.NewBifrostOperationError("error creating Bedrock inference profile request", err)
	}
	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
	} else if bifrostErr := signAWSRequest(ctx, req, key.BedrockKeyConfig, region, bedrockSigningService); bifrostErr != nil {
		return nil, 0, nil, bifrostErr
	}
	return provider.executeBedrockRequest(req)
}

func filterInferenceProfilesForKey(profiles []schemas.BifrostInferenceProfileSummary, key schemas.Key) []schemas.BifrostInferenceProfileSummary {
	filtered := make([]schemas.BifrostInferenceProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		if inferenceProfileAllowedForKey(profile.InferenceProfileID, key) {
			filtered = append(filtered, profile)
		}
	}
	return filtered
}

// inferenceProfileAllowedForKey applies the same identifier users supply to
// Bedrock runtime calls. Alias keys are accepted for configured aliases, but
// the AWS profile identifier itself is never rewritten in the response.
func inferenceProfileAllowedForKey(identifier string, key schemas.Key) bool {
	if key.BlacklistedModels.IsBlocked(identifier) {
		return false
	}
	if key.Models.IsAllowed(identifier) {
		return true
	}
	for alias, config := range key.Aliases {
		if strings.EqualFold(config.ModelID, identifier) && !key.BlacklistedModels.IsBlocked(alias) && key.Models.IsAllowed(alias) {
			return true
		}
	}
	return false
}
