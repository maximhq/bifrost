package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// FileUpload uploads a file to Anthropic's Files API.
func (provider *AnthropicProvider) FileUpload(ctx context.Context, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field
	filename := request.Filename
	if filename == "" {
		filename = "file"
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
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/files", schemas.FileUploadRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	req.Header.Set("anthropic-beta", AnthropicFilesAPIBetaHeader)

	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.FileUploadRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicFileResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropicResp.ToBifrostFileUploadResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// FileList lists files from Anthropic's Files API.
func (provider *AnthropicProvider) FileList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no keys provided", providerName)
	}

	key := keys[0]

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	url := provider.buildRequestURL(ctx, "/v1/files", schemas.FileListRequest)
	hasParams := false
	if request.Limit > 0 {
		url += fmt.Sprintf("?limit=%d", request.Limit)
		hasParams = true
	}
	if request.After != nil {
		if hasParams {
			url += "&"
		} else {
			url += "?"
			hasParams = true
		}
		url += fmt.Sprintf("after_id=%s", *request.After)
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	req.Header.Set("anthropic-beta", AnthropicFilesAPIBetaHeader)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.FileListRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicFileListResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		HasMore: anthropicResp.HasMore,
		Data:    make([]schemas.FileObject, len(anthropicResp.Data)),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, file := range anthropicResp.Data {
		bifrostResp.Data[i] = schemas.FileObject{
			ID:        file.ID,
			Object:    file.Type,
			Bytes:     file.SizeBytes,
			CreatedAt: parseAnthropicFileTimestamp(file.CreatedAt),
			Filename:  file.Filename,
			Purpose:   schemas.FilePurpose(file.MimeType),
			Status:    schemas.FileStatusProcessed,
		}
	}

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Anthropic's Files API.
func (provider *AnthropicProvider) FileRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + request.FileID)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	req.Header.Set("anthropic-beta", AnthropicFilesAPIBetaHeader)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.FileRetrieveRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicFileResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropicResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// FileDelete deletes a file from Anthropic's Files API.
func (provider *AnthropicProvider) FileDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + request.FileID)
	req.Header.SetMethod(http.MethodDelete)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	req.Header.Set("anthropic-beta", AnthropicFilesAPIBetaHeader)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.FileDeleteRequest, providerName, "")
	}

	provider.logger.Debug(fmt.Sprintf("response from %d provider: %s", resp.StatusCode(), string(resp.Body())))

	// For 204 No Content, return success without parsing body
	if resp.StatusCode() == fasthttp.StatusNoContent {
		return &schemas.BifrostFileDeleteResponse{
			ID:      request.FileID,
			Object:  "file",
			Deleted: true,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileDeleteRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicFileDeleteResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := &schemas.BifrostFileDeleteResponse{
		ID:      anthropicResp.ID,
		Object:  "file",
		Deleted: anthropicResp.Type == "file_deleted",
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

// FileContent downloads file content from Anthropic's Files API.
// Note: Only files created by skills or the code execution tool can be downloaded.
func (provider *AnthropicProvider) FileContent(ctx context.Context, key schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + request.FileID + "/content")
	req.Header.SetMethod(http.MethodGet)

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	req.Header.Set("anthropic-beta", AnthropicFilesAPIBetaHeader)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.FileContentRequest, providerName, "")
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

// ToAnthropicFileUploadResponse converts a Bifrost file upload response to Anthropic format.
func ToAnthropicFileUploadResponse(resp *schemas.BifrostFileUploadResponse) *AnthropicFileResponse {
	return &AnthropicFileResponse{
		ID:        resp.ID,
		Type:      resp.Object,
		Filename:  resp.Filename,
		MimeType:  resp.Purpose,
		SizeBytes: resp.Bytes,
		CreatedAt: formatAnthropicFileTimestamp(resp.CreatedAt),
	}
}

// ToAnthropicFileListResponse converts a Bifrost file list response to Anthropic format.
func ToAnthropicFileListResponse(resp *schemas.BifrostFileListResponse) *AnthropicFileListResponse {
	data := make([]AnthropicFileResponse, len(resp.Data))
	for i, file := range resp.Data {
		data[i] = AnthropicFileResponse{
			ID:        file.ID,
			Type:      file.Object,
			Filename:  file.Filename,
			MimeType:  string(file.Purpose),
			SizeBytes: file.Bytes,
			CreatedAt: formatAnthropicFileTimestamp(file.CreatedAt),
		}
	}

	return &AnthropicFileListResponse{
		Data:    data,
		HasMore: resp.HasMore,
	}
}

// ToAnthropicFileRetrieveResponse converts a Bifrost file retrieve response to Anthropic format.
func ToAnthropicFileRetrieveResponse(resp *schemas.BifrostFileRetrieveResponse) *AnthropicFileResponse {
	return &AnthropicFileResponse{
		ID:        resp.ID,
		Type:      resp.Object,
		Filename:  resp.Filename,
		MimeType:  resp.Purpose,
		SizeBytes: resp.Bytes,
		CreatedAt: formatAnthropicFileTimestamp(resp.CreatedAt),
	}
}

// ToAnthropicFileDeleteResponse converts a Bifrost file delete response to Anthropic format.
func ToAnthropicFileDeleteResponse(resp *schemas.BifrostFileDeleteResponse) *AnthropicFileDeleteResponse {
	respType := "file"
	if resp.Deleted {
		respType = "file_deleted"
	}
	return &AnthropicFileDeleteResponse{
		ID:      resp.ID,
		Type:    respType,		
	}
}

// formatAnthropicFileTimestamp converts Unix timestamp to Anthropic ISO timestamp format.
func formatAnthropicFileTimestamp(unixTime int64) string {
	if unixTime == 0 {
		return ""
	}
	return time.Unix(unixTime, 0).UTC().Format(time.RFC3339)
}
