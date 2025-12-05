package openai

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// OpenAI File API Types

// OpenAIFileResponse represents an OpenAI file response.
type OpenAIFileResponse struct {
	ID            string  `json:"id"`
	Object        string  `json:"object"`
	Bytes         int64   `json:"bytes"`
	CreatedAt     int64   `json:"created_at"`
	Filename      string  `json:"filename"`
	Purpose       string  `json:"purpose"`
	Status        string  `json:"status,omitempty"`
	StatusDetails *string `json:"status_details,omitempty"`
}

// OpenAIFileListResponse represents the response from listing files.
type OpenAIFileListResponse struct {
	Object  string               `json:"object"`
	Data    []OpenAIFileResponse `json:"data"`
	HasMore bool                 `json:"has_more,omitempty"`
}

// OpenAIFileDeleteResponse represents the response from deleting a file.
type OpenAIFileDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// ToBifrostFileStatus converts OpenAI status to Bifrost status.
func ToBifrostFileStatus(status string) schemas.FileStatus {
	switch status {
	case "uploaded":
		return schemas.FileStatusUploaded
	case "processed":
		return schemas.FileStatusProcessed
	case "processing":
		return schemas.FileStatusProcessing
	case "error":
		return schemas.FileStatusError
	case "deleted":
		return schemas.FileStatusDeleted
	default:
		return schemas.FileStatus(status)
	}
}

// ToBifrostFileUploadResponse converts OpenAI file response to Bifrost file upload response.
func (r *OpenAIFileResponse) ToBifrostFileUploadResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawResponse bool, rawResponse interface{}) *schemas.BifrostFileUploadResponse {
	resp := &schemas.BifrostFileUploadResponse{
		ID:             r.ID,
		Object:         r.Object,
		Bytes:          r.Bytes,
		CreatedAt:      r.CreatedAt,
		Filename:       r.Filename,
		Purpose:        r.Purpose,
		Status:         ToBifrostFileStatus(r.Status),
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

// ToBifrostFileRetrieveResponse converts OpenAI file response to Bifrost file retrieve response.
func (r *OpenAIFileResponse) ToBifrostFileRetrieveResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawResponse bool, rawResponse interface{}) *schemas.BifrostFileRetrieveResponse {
	resp := &schemas.BifrostFileRetrieveResponse{
		ID:             r.ID,
		Object:         r.Object,
		Bytes:          r.Bytes,
		CreatedAt:      r.CreatedAt,
		Filename:       r.Filename,
		Purpose:        r.Purpose,
		Status:         ToBifrostFileStatus(r.Status),
		StatusDetails:  r.StatusDetails,
		StorageBackend: schemas.FileStorageAPI,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// FileUpload uploads a file to OpenAI.
func (provider *OpenAIProvider) FileUpload(ctx context.Context, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	if request.Purpose == "" {
		return nil, providerUtils.NewBifrostOperationError("purpose is required", nil, providerName)
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
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/files", schemas.FileUploadRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.FileUploadRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	fmt.Printf("body: %s\n", string(body))

	var openAIResp OpenAIFileResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileUploadResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// FileList lists files from OpenAI.
func (provider *OpenAIProvider) FileList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	url := provider.buildRequestURL(ctx, "/v1/files", schemas.FileListRequest)
	queryParams := ""
	if request.Purpose != "" {
		queryParams += fmt.Sprintf("purpose=%s", request.Purpose)
	}
	if request.Limit > 0 {
		if queryParams != "" {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("limit=%d", request.Limit)
	}
	if request.After != nil {
		if queryParams != "" {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("after=%s", *request.After)
	}
	if request.Order != nil {
		if queryParams != "" {
			queryParams += "&"
		}
		queryParams += fmt.Sprintf("order=%s", *request.Order)
	}
	if queryParams != "" {
		url += "?" + queryParams
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Use first key if available
	if len(keys) > 0 && keys[0].Value != "" {
		req.Header.Set("Authorization", "Bearer "+keys[0].Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.FileListRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIFileListResponse
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
			Status:        ToBifrostFileStatus(file.Status),
			StatusDetails: file.StatusDetails,
		}
	}

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from OpenAI.
func (provider *OpenAIProvider) FileRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
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
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.FileRetrieveRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIFileResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// FileDelete deletes a file from OpenAI.
func (provider *OpenAIProvider) FileDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
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
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.FileDeleteRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIFileDeleteResponse
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

// FileContent downloads file content from OpenAI.
func (provider *OpenAIProvider) FileContent(ctx context.Context, key schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
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
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.FileContentRequest, providerName, "")
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

// ConvertRequestsToJSONL converts batch request items to JSONL format.
func ConvertRequestsToJSONL(requests []schemas.BatchRequestItem) ([]byte, error) {
	var buf bytes.Buffer
	for _, req := range requests {
		line, err := sonic.Marshal(req)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

