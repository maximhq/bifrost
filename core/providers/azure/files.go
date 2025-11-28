package azure

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// FileUpload uploads a file to Azure OpenAI.
func (provider *AzureProvider) FileUpload(ctx context.Context, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	if request.Purpose == "" {
		return nil, providerUtils.NewBifrostOperationError("purpose is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add purpose field
	if err := writer.WriteField("purpose", request.Purpose); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write purpose field", err, providerName)
	}

	// Add file field
	filename := request.Filename
	if filename == "" {
		filename = "file.jsonl"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file content", err, providerName)
	}

	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	url := fmt.Sprintf("%s/openai/files?api-version=%s", key.AzureKeyConfig.Endpoint, *apiVersion)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileUploadRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileUploadResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// FileList lists files from Azure OpenAI.
func (provider *AzureProvider) FileList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no keys provided", provider.GetProviderKey())
	}

	key := keys[0]
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	url := fmt.Sprintf("%s/openai/files?api-version=%s", key.AzureKeyConfig.Endpoint, *apiVersion)
	if request.Purpose != "" {
		url += fmt.Sprintf("&purpose=%s", request.Purpose)
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileListRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileListResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  openAIResp.Object,
		HasMore: openAIResp.HasMore,
		Data:    make([]schemas.FileObject, len(openAIResp.Data)),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, file := range openAIResp.Data {
		bifrostResp.Data[i] = schemas.FileObject{
			ID:            file.ID,
			Object:        file.Object,
			Bytes:         file.Bytes,
			CreatedAt:     file.CreatedAt,
			Filename:      file.Filename,
			Purpose:       schemas.FilePurpose(file.Purpose),
			Status:        openai.ToBifrostFileStatus(file.Status),
			StatusDetails: file.StatusDetails,
		}
	}

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Azure OpenAI.
func (provider *AzureProvider) FileRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	url := fmt.Sprintf("%s/openai/files/%s?api-version=%s", key.AzureKeyConfig.Endpoint, request.FileID, *apiVersion)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileRetrieveRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// FileDelete deletes a file from Azure OpenAI.
func (provider *AzureProvider) FileDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	url := fmt.Sprintf("%s/openai/files/%s?api-version=%s", key.AzureKeyConfig.Endpoint, request.FileID, *apiVersion)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodDelete)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileDeleteRequest, providerName, "")
	}

	if resp.StatusCode() == fasthttp.StatusNoContent {
		return &schemas.BifrostFileDeleteResponse{
			ID:      request.FileID,
			Object:  "file",
			Deleted: true,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileUploadRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileDeleteResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := &schemas.BifrostFileDeleteResponse{
		ID:      openAIResp.ID,
		Object:  openAIResp.Object,
		Deleted: openAIResp.Deleted,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileDeleteRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if sendBackRawResponse {
		result.ExtraFields.RawResponse = rawResponse
	}

	return result, nil
}

// FileContent downloads file content from Azure OpenAI.
func (provider *AzureProvider) FileContent(ctx context.Context, key schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	url := fmt.Sprintf("%s/openai/files/%s/content?api-version=%s", key.AzureKeyConfig.Endpoint, request.FileID, *apiVersion)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileContentRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Get content type from response
	contentType := string(resp.Header.ContentType())
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return &schemas.BifrostFileContentResponse{
		FileID:      request.FileID,
		Content:     body,
		ContentType: contentType,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileContentRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// validateKeyConfigForFiles validates the key configuration for file operations.
func (provider *AzureProvider) validateKeyConfigForFiles(key schemas.Key) *schemas.BifrostError {
	if key.AzureKeyConfig == nil {
		return providerUtils.NewConfigurationError("azure key config not set", provider.GetProviderKey())
	}

	if key.AzureKeyConfig.Endpoint == "" {
		return providerUtils.NewConfigurationError("endpoint not set", provider.GetProviderKey())
	}

	return nil
}

// setAzureAuth sets the Azure authentication header on the request.
func (provider *AzureProvider) setAzureAuth(ctx context.Context, req *fasthttp.Request, key schemas.Key) {
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
		req.Header.Del("api-key")
	} else {
		req.Header.Set("api-key", key.Value)
	}
}

// AzureFileResponse represents an Azure file response (same as OpenAI).
type AzureFileResponse struct {
	ID            string  `json:"id"`
	Object        string  `json:"object"`
	Bytes         int64   `json:"bytes"`
	CreatedAt     int64   `json:"created_at"`
	Filename      string  `json:"filename"`
	Purpose       string  `json:"purpose"`
	Status        string  `json:"status,omitempty"`
	StatusDetails *string `json:"status_details,omitempty"`
}

// ToBifrostFileUploadResponse converts Azure file response to Bifrost response.
func (r *AzureFileResponse) ToBifrostFileUploadResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawResponse bool, rawResponse interface{}) *schemas.BifrostFileUploadResponse {
	resp := &schemas.BifrostFileUploadResponse{
		ID:             r.ID,
		Object:         r.Object,
		Bytes:          r.Bytes,
		CreatedAt:      r.CreatedAt,
		Filename:       r.Filename,
		Purpose:        r.Purpose,
		Status:         openai.ToBifrostFileStatus(r.Status),
		StatusDetails:  r.StatusDetails,
		StorageBackend: schemas.FileStorageAPI,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileUploadRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}
