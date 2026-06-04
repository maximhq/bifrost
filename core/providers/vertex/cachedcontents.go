package vertex

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/tidwall/sjson"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// vertexCachedContent mirrors Vertex AI's CachedContent resource shape.
// Vertex uses the same camelCase keys as Google AI Studio for the body, but
// the `model` field must be the full publisher path (handled in CachedContentCreate).
//
// API ref: https://cloud.google.com/vertex-ai/generative-ai/docs/context-cache/context-cache-create
type vertexCachedContent struct {
	Name              string         `json:"name,omitempty"`
	DisplayName       string         `json:"displayName,omitempty"`
	Model             string         `json:"model,omitempty"`
	SystemInstruction any            `json:"systemInstruction,omitempty"`
	Contents          []any          `json:"contents,omitempty"`
	Tools             []any          `json:"tools,omitempty"`
	ToolConfig        any            `json:"toolConfig,omitempty"`
	CreateTime        string         `json:"createTime,omitempty"`
	UpdateTime        string         `json:"updateTime,omitempty"`
	ExpireTime        string         `json:"expireTime,omitempty"`
	TTL               string         `json:"ttl,omitempty"`
	UsageMetadata     map[string]any `json:"usageMetadata,omitempty"`
}

type vertexCachedContentList struct {
	CachedContents []vertexCachedContent `json:"cachedContents"`
	NextPageToken  string                `json:"nextPageToken,omitempty"`
}

func (v *vertexCachedContent) toBifrostObject() schemas.CachedContentObject {
	return schemas.CachedContentObject{
		Name:              v.Name,
		DisplayName:       v.DisplayName,
		Model:             v.Model,
		SystemInstruction: v.SystemInstruction,
		Contents:          v.Contents,
		Tools:             v.Tools,
		ToolConfig:        v.ToolConfig,
		CreateTime:        v.CreateTime,
		UpdateTime:        v.UpdateTime,
		ExpireTime:        v.ExpireTime,
		UsageMetadata:     v.UsageMetadata,
	}
}

func validateVertexTTLExpireMutex(ttl, expireTime *string) *schemas.BifrostError {
	if ttl != nil && *ttl != "" && expireTime != nil && *expireTime != "" {
		return providerUtils.NewBifrostOperationError("ttl and expire_time are mutually exclusive", nil)
	}
	return nil
}

// expandVertexCachedContentName ensures the name is the full Vertex resource path.
// If the user passes "abc123" or "cachedContents/abc123", rewrite to
// "projects/{p}/locations/{l}/cachedContents/abc123". Idempotent for already-full paths.
func expandVertexCachedContentName(name, projectID, region string) string {
	if strings.HasPrefix(name, "projects/") {
		return name
	}
	id := strings.TrimPrefix(name, "cachedContents/")
	return fmt.Sprintf("projects/%s/locations/%s/cachedContents/%s", projectID, region, id)
}

// expandVertexModelPath rewrites a bare model id ("gemini-2.5-pro") to the full
// Vertex publisher path. Already-expanded paths pass through unchanged.
func expandVertexModelPath(model, projectID, region string) string {
	if strings.HasPrefix(model, "projects/") {
		return model
	}
	model = strings.TrimPrefix(model, "models/")
	return fmt.Sprintf("projects/%s/locations/%s/publishers/google/models/%s", projectID, region, model)
}

// vertexAuthHeaders pulls an OAuth bearer token from the key and applies it.
func vertexAuthHeaders(req *fasthttp.Request, key schemas.Key) *schemas.BifrostError {
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return providerUtils.NewBifrostOperationError("error creating auth token source", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return providerUtils.NewBifrostOperationError("error getting auth token", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	return nil
}

// vertexCachedContentBaseURL builds /v1/projects/{p}/locations/{l}/cachedContents
// using the existing helper from utils.go.
func vertexCachedContentBaseURL(region, projectID string) string {
	return fmt.Sprintf("%s/cachedContents", getVertexProjectLocationURL(region, "v1", projectID))
}

// CachedContentCreate creates a new cached content via Vertex AI's
// /v1/projects/{p}/locations/{l}/cachedContents endpoint.
func (provider *VertexProvider) CachedContentCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentCreateRequest) (*schemas.BifrostCachedContentCreateResponse, *schemas.BifrostError) {
	if err := validateVertexTTLExpireMutex(request.TTL, request.ExpireTime); err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, providerUtils.NewBifrostOperationError("model is required for cached content create", nil)
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project_id is not set in vertex key config")
	}
	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in vertex key config")
	}

	model := expandVertexModelPath(request.Model, projectID, region)
	jsonBody, useRaw := providerUtils.CheckAndGetRawRequestBody(ctx, request)
	if useRaw && len(jsonBody) > 0 {
		var err error
		jsonBody, err = sjson.SetBytes(jsonBody, "model", model)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to set cached content model", err)
		}
	} else {
		body := vertexCachedContent{
			Model:             model,
			SystemInstruction: request.SystemInstruction,
			Contents:          request.Contents,
			Tools:             request.Tools,
			ToolConfig:        request.ToolConfig,
		}
		if request.DisplayName != nil {
			body.DisplayName = *request.DisplayName
		}
		if request.TTL != nil {
			body.TTL = *request.TTL
		}
		if request.ExpireTime != nil {
			body.ExpireTime = *request.ExpireTime
		}

		var err error
		jsonBody, err = sonic.Marshal(body)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to marshal cached content create body", err)
		}
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	requestURL := vertexCachedContentBaseURL(region, projectID)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if authErr := vertexAuthHeaders(req, key); authErr != nil {
		return nil, authErr
	}
	req.SetBody(jsonBody)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseVertexCachedContentError(resp)
	}

	respBody, decErr := providerUtils.CheckAndDecodeBody(resp)
	if decErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decErr)
	}

	var vResp vertexCachedContent
	if err := sonic.Unmarshal(respBody, &vResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	return &schemas.BifrostCachedContentCreateResponse{
		Name:              vResp.Name,
		DisplayName:       vResp.DisplayName,
		Model:             vResp.Model,
		SystemInstruction: vResp.SystemInstruction,
		Contents:          vResp.Contents,
		Tools:             vResp.Tools,
		ToolConfig:        vResp.ToolConfig,
		CreateTime:        vResp.CreateTime,
		UpdateTime:        vResp.UpdateTime,
		ExpireTime:        vResp.ExpireTime,
		UsageMetadata:     vResp.UsageMetadata,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}, nil
}

func (provider *VertexProvider) cachedContentListByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, time.Duration, *schemas.BifrostError) {
	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, 0, providerUtils.NewConfigurationError("project_id is not set in vertex key config")
	}
	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, 0, providerUtils.NewConfigurationError("region is not set in vertex key config")
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	requestURL := vertexCachedContentBaseURL(region, projectID)
	queryArgs := url.Values{}
	if request.PageSize > 0 {
		queryArgs.Set("pageSize", strconv.Itoa(request.PageSize))
	}
	if request.PageToken != nil && *request.PageToken != "" {
		queryArgs.Set("pageToken", *request.PageToken)
	}
	if len(queryArgs) > 0 {
		requestURL += "?" + queryArgs.Encode()
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if authErr := vertexAuthHeaders(req, key); authErr != nil {
		return nil, 0, authErr
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, latency, parseVertexCachedContentError(resp)
	}

	respBody, decErr := providerUtils.CheckAndDecodeBody(resp)
	if decErr != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decErr)
	}

	var vList vertexCachedContentList
	if err := sonic.Unmarshal(respBody, &vList); err != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	bifrostObjects := make([]schemas.CachedContentObject, 0, len(vList.CachedContents))
	for i := range vList.CachedContents {
		bifrostObjects = append(bifrostObjects, vList.CachedContents[i].toBifrostObject())
	}

	return &schemas.BifrostCachedContentListResponse{
		CachedContents: bifrostObjects,
		NextPageToken:  vList.NextPageToken,
	}, latency, nil
}

func (provider *VertexProvider) CachedContentList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("no keys provided for cached content list", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		resp, latency, bifrostErr := provider.cachedContentListByKey(ctx, key, request)
		if bifrostErr == nil {
			resp.ExtraFields = schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()}
			return resp, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

func (provider *VertexProvider) cachedContentRetrieveByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, time.Duration, *schemas.BifrostError) {
	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, 0, providerUtils.NewConfigurationError("project_id is not set in vertex key config")
	}
	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, 0, providerUtils.NewConfigurationError("region is not set in vertex key config")
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	name := expandVertexCachedContentName(request.Name, projectID, region)
	requestURL := fmt.Sprintf("%s/%s", getVertexAPIBaseURL(region, "v1"), name)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if authErr := vertexAuthHeaders(req, key); authErr != nil {
		return nil, 0, authErr
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, latency, parseVertexCachedContentError(resp)
	}

	respBody, decErr := providerUtils.CheckAndDecodeBody(resp)
	if decErr != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decErr)
	}

	var vResp vertexCachedContent
	if err := sonic.Unmarshal(respBody, &vResp); err != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	return &schemas.BifrostCachedContentRetrieveResponse{
		Name:              vResp.Name,
		DisplayName:       vResp.DisplayName,
		Model:             vResp.Model,
		SystemInstruction: vResp.SystemInstruction,
		Contents:          vResp.Contents,
		Tools:             vResp.Tools,
		ToolConfig:        vResp.ToolConfig,
		CreateTime:        vResp.CreateTime,
		UpdateTime:        vResp.UpdateTime,
		ExpireTime:        vResp.ExpireTime,
		UsageMetadata:     vResp.UsageMetadata,
	}, latency, nil
}

func (provider *VertexProvider) CachedContentRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, *schemas.BifrostError) {
	if request.Name == "" {
		return nil, providerUtils.NewBifrostOperationError("name is required for cached content retrieve", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("no keys provided for cached content retrieve", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		resp, latency, bifrostErr := provider.cachedContentRetrieveByKey(ctx, key, request)
		if bifrostErr == nil {
			resp.ExtraFields = schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()}
			return resp, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

func (provider *VertexProvider) cachedContentUpdateByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, time.Duration, *schemas.BifrostError) {
	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, 0, providerUtils.NewConfigurationError("project_id is not set in vertex key config")
	}
	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, 0, providerUtils.NewConfigurationError("region is not set in vertex key config")
	}

	body := vertexCachedContent{}
	updateMaskFields := []string{}
	if request.TTL != nil && *request.TTL != "" {
		body.TTL = *request.TTL
		updateMaskFields = append(updateMaskFields, "ttl")
	}
	if request.ExpireTime != nil && *request.ExpireTime != "" {
		body.ExpireTime = *request.ExpireTime
		updateMaskFields = append(updateMaskFields, "expireTime")
	}

	jsonBody, useRaw := providerUtils.CheckAndGetRawRequestBody(ctx, request)
	if !useRaw || len(jsonBody) == 0 {
		var marshalErr error
		jsonBody, marshalErr = sonic.Marshal(body)
		if marshalErr != nil {
			return nil, 0, providerUtils.NewBifrostOperationError("failed to marshal cached content update body", marshalErr)
		}
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	name := expandVertexCachedContentName(request.Name, projectID, region)
	requestURL := fmt.Sprintf("%s/%s", getVertexAPIBaseURL(region, "v1"), name)
	if len(updateMaskFields) > 0 {
		requestURL += "?updateMask=" + strings.Join(updateMaskFields, ",")
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPatch)
	req.Header.SetContentType("application/json")
	if authErr := vertexAuthHeaders(req, key); authErr != nil {
		return nil, 0, authErr
	}
	req.SetBody(jsonBody)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, latency, parseVertexCachedContentError(resp)
	}

	respBody, decErr := providerUtils.CheckAndDecodeBody(resp)
	if decErr != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decErr)
	}

	var vResp vertexCachedContent
	if err := sonic.Unmarshal(respBody, &vResp); err != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	return &schemas.BifrostCachedContentUpdateResponse{
		Name:              vResp.Name,
		DisplayName:       vResp.DisplayName,
		Model:             vResp.Model,
		SystemInstruction: vResp.SystemInstruction,
		Contents:          vResp.Contents,
		Tools:             vResp.Tools,
		ToolConfig:        vResp.ToolConfig,
		CreateTime:        vResp.CreateTime,
		UpdateTime:        vResp.UpdateTime,
		ExpireTime:        vResp.ExpireTime,
		UsageMetadata:     vResp.UsageMetadata,
	}, latency, nil
}

func (provider *VertexProvider) CachedContentUpdate(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, *schemas.BifrostError) {
	if request.Name == "" {
		return nil, providerUtils.NewBifrostOperationError("name is required for cached content update", nil)
	}
	if err := validateVertexTTLExpireMutex(request.TTL, request.ExpireTime); err != nil {
		return nil, err
	}
	if (request.TTL == nil || *request.TTL == "") && (request.ExpireTime == nil || *request.ExpireTime == "") {
		return nil, providerUtils.NewBifrostOperationError("either ttl or expire_time must be set for cached content update", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("no keys provided for cached content update", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		resp, latency, bifrostErr := provider.cachedContentUpdateByKey(ctx, key, request)
		if bifrostErr == nil {
			resp.ExtraFields = schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()}
			return resp, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

func (provider *VertexProvider) cachedContentDeleteByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, time.Duration, *schemas.BifrostError) {
	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, 0, providerUtils.NewConfigurationError("project_id is not set in vertex key config")
	}
	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, 0, providerUtils.NewConfigurationError("region is not set in vertex key config")
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	name := expandVertexCachedContentName(request.Name, projectID, region)
	requestURL := fmt.Sprintf("%s/%s", getVertexAPIBaseURL(region, "v1"), name)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodDelete)
	if authErr := vertexAuthHeaders(req, key); authErr != nil {
		return nil, 0, authErr
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, latency, parseVertexCachedContentError(resp)
	}

	return &schemas.BifrostCachedContentDeleteResponse{
		Name:    name,
		Deleted: true,
	}, latency, nil
}

func (provider *VertexProvider) CachedContentDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, *schemas.BifrostError) {
	if request.Name == "" {
		return nil, providerUtils.NewBifrostOperationError("name is required for cached content delete", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("no keys provided for cached content delete", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		resp, latency, bifrostErr := provider.cachedContentDeleteByKey(ctx, key, request)
		if bifrostErr == nil {
			resp.ExtraFields = schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()}
			return resp, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

// parseVertexCachedContentError parses a Vertex API error response into a BifrostError.
func parseVertexCachedContentError(resp *fasthttp.Response) *schemas.BifrostError {
	respBody := resp.Body()
	statusCode := resp.StatusCode()

	var errorResp VertexError
	if err := sonic.Unmarshal(respBody, &errorResp); err == nil && errorResp.Error.Message != "" {
		return providerUtils.NewProviderAPIError(errorResp.Error.Message, nil, statusCode, nil, nil)
	}
	return providerUtils.NewProviderAPIError(string(respBody), nil, statusCode, nil, nil)
}
