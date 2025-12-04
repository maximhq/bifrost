package gemini

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Gemini Files API types
// The Gemini Files API allows uploading files for use with multimodal models.

// GeminiFileResponse represents a file object from Gemini's API.
type GeminiFileResponse struct {
	Name           string                   `json:"name"`           // Resource name (e.g., "files/abc123")
	DisplayName    string                   `json:"displayName"`    // User-provided display name
	MimeType       string                   `json:"mimeType"`       // MIME type of the file
	SizeBytes      string                   `json:"sizeBytes"`      // Size in bytes (as string)
	CreateTime     string                   `json:"createTime"`     // RFC3339 timestamp
	UpdateTime     string                   `json:"updateTime"`     // RFC3339 timestamp
	ExpirationTime string                   `json:"expirationTime"` // RFC3339 timestamp when file will be deleted
	SHA256Hash     string                   `json:"sha256Hash"`     // Base64 encoded SHA256 hash
	URI            string                   `json:"uri"`            // URI for accessing the file
	State          string                   `json:"state"`          // "PROCESSING", "ACTIVE", "FAILED"
	VideoMetadata  *GeminiFileVideoMetadata `json:"videoMetadata,omitempty"`
}

// GeminiFileVideoMetadata contains video-specific metadata.
type GeminiFileVideoMetadata struct {
	VideoDuration string `json:"videoDuration"` // Duration in seconds
}

// GeminiFileListResponse represents the response from listing files.
type GeminiFileListResponse struct {
	Files         []GeminiFileResponse `json:"files"`
	NextPageToken string               `json:"nextPageToken,omitempty"`
}

// ToBifrostFileStatus converts Gemini file state to Bifrost status.
func ToBifrostFileStatus(state string) schemas.FileStatus {
	switch state {
	case "PROCESSING":
		return schemas.FileStatusProcessing
	case "ACTIVE":
		return schemas.FileStatusProcessed
	case "FAILED":
		return schemas.FileStatusError
	default:
		return schemas.FileStatus(strings.ToLower(state))
	}
}

// FileUpload uploads a file to Gemini.
func (provider *GeminiProvider) FileUpload(ctx context.Context, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	// Create multipart request
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file metadata as JSON
	metadataField, err := writer.CreateFormField("metadata")
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create metadata field", err, providerName)
	}
	metadata := map[string]interface{}{
		"file": map[string]string{
			"displayName": request.Filename,
		},
	}
	metadataJSON, _ := sonic.Marshal(metadata)
	metadataField.Write(metadataJSON)

	// Add file content
	filename := request.Filename
	if filename == "" {
		filename = "file.bin"
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

	// Build URL - use upload endpoint
	baseURL := strings.Replace(provider.networkConfig.BaseURL, "/v1beta", "/upload/v1beta", 1)
	url := fmt.Sprintf("%s/files", baseURL)
	if key.Value != "" {
		url += "?key=" + key.Value
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())
	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse response - wrapped in "file" object
	var responseWrapper struct {
		File GeminiFileResponse `json:"file"`
	}
	if err := sonic.Unmarshal(body, &responseWrapper); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	geminiResp := responseWrapper.File

	// Parse size
	var sizeBytes int64
	fmt.Sscanf(geminiResp.SizeBytes, "%d", &sizeBytes)

	// Parse creation time
	var createdAt int64
	if t, err := time.Parse(time.RFC3339, geminiResp.CreateTime); err == nil {
		createdAt = t.Unix()
	}

	// Parse expiration time
	var expiresAt *int64
	if geminiResp.ExpirationTime != "" {
		if t, err := time.Parse(time.RFC3339, geminiResp.ExpirationTime); err == nil {
			exp := t.Unix()
			expiresAt = &exp
		}
	}	
	return &schemas.BifrostFileUploadResponse{
		ID:             geminiResp.Name,
		Object:         "file",
		Bytes:          sizeBytes,
		CreatedAt:      createdAt,
		Filename:       geminiResp.DisplayName,
		Purpose:        request.Purpose,
		Status:         ToBifrostFileStatus(geminiResp.State),
		StorageBackend: schemas.FileStorageAPI,
		StorageURI:     geminiResp.URI,
		ExpiresAt:      expiresAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileUploadRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileList lists files from Gemini.
func (provider *GeminiProvider) FileList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileListRequest); err != nil {
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

	// Build URL with pagination
	url := fmt.Sprintf("%s/files", provider.networkConfig.BaseURL)
	params := ""
	if request.Limit > 0 {
		params += fmt.Sprintf("pageSize=%d", request.Limit)
	}
	if request.After != nil && *request.After != "" {
		if params != "" {
			params += "&"
		}
		params += "pageToken=" + *request.After
	}
	if key.Value != "" {
		if params != "" {
			params += "&"
		}
		params += "key=" + key.Value
	}
	if params != "" {
		url += "?" + params
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiFileListResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    make([]schemas.FileObject, len(geminiResp.Files)),
		HasMore: geminiResp.NextPageToken != "",
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, file := range geminiResp.Files {
		var sizeBytes int64
		fmt.Sscanf(file.SizeBytes, "%d", &sizeBytes)

		var createdAt int64
		if t, err := time.Parse(time.RFC3339, file.CreateTime); err == nil {
			createdAt = t.Unix()
		}

		var expiresAt *int64
		if file.ExpirationTime != "" {
			if t, err := time.Parse(time.RFC3339, file.ExpirationTime); err == nil {
				exp := t.Unix()
				expiresAt = &exp
			}
		}

		bifrostResp.Data[i] = schemas.FileObject{
			ID:        file.Name,
			Object:    "file",
			Bytes:     sizeBytes,
			CreatedAt: createdAt,
			Filename:  file.DisplayName,
			Purpose:   schemas.FilePurposeVision,
			Status:    ToBifrostFileStatus(file.State),
			ExpiresAt: expiresAt,
		}
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Gemini.
func (provider *GeminiProvider) FileRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
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

	// Build URL - file ID is the full resource name (e.g., "files/abc123")
	fileID := request.FileID
	if !strings.HasPrefix(fileID, "files/") {
		fileID = "files/" + fileID
	}
	url := fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, fileID)
	if key.Value != "" {
		url += "?key=" + key.Value
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiFileResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	var sizeBytes int64
	fmt.Sscanf(geminiResp.SizeBytes, "%d", &sizeBytes)

	var createdAt int64
	if t, err := time.Parse(time.RFC3339, geminiResp.CreateTime); err == nil {
		createdAt = t.Unix()
	}

	var expiresAt *int64
	if geminiResp.ExpirationTime != "" {
		if t, err := time.Parse(time.RFC3339, geminiResp.ExpirationTime); err == nil {
			exp := t.Unix()
			expiresAt = &exp
		}
	}

	return &schemas.BifrostFileRetrieveResponse{
		ID:             geminiResp.Name,
		Object:         "file",
		Bytes:          sizeBytes,
		CreatedAt:      createdAt,
		Filename:       geminiResp.DisplayName,
		Purpose:        string(schemas.FilePurposeVision),
		Status:         ToBifrostFileStatus(geminiResp.State),
		StorageBackend: schemas.FileStorageAPI,
		StorageURI:     geminiResp.URI,
		ExpiresAt:      expiresAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileDelete deletes a file from Gemini.
func (provider *GeminiProvider) FileDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
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

	// Build URL
	fileID := request.FileID
	if !strings.HasPrefix(fileID, "files/") {
		fileID = "files/" + fileID
	}
	url := fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, fileID)
	if key.Value != "" {
		url += "?key=" + key.Value
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodDelete)
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response - DELETE returns 200 with empty body on success
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
		return nil, parseGeminiError(providerName, resp)
	}

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

// FileContent downloads file content from Gemini.
// Note: Gemini Files API doesn't support direct content download.
// Files are accessed via their URI in API requests.
func (provider *GeminiProvider) FileContent(ctx context.Context, key schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Gemini doesn't support direct file content download
	// Files are referenced by their URI in requests
	return nil, providerUtils.NewBifrostOperationError(
		"Gemini Files API doesn't support direct content download. Use the file URI in your requests instead.",
		nil,
		providerName,
	)
}

// ToGeminiFileUploadResponse converts a Bifrost file upload response to Gemini format.
// Uses snake_case field names to match Google's API format.
// GeminiFileUploadResponseWrapper is a wrapper that contains the file response for the upload API.
type GeminiFileUploadResponseWrapper struct {
	File GeminiFileResponse `json:"file"`
}

func ToGeminiFileUploadResponse(resp *schemas.BifrostFileUploadResponse) *GeminiFileUploadResponseWrapper {
	return &GeminiFileUploadResponseWrapper{
		File: GeminiFileResponse{
			Name:           resp.ID,
			DisplayName:    resp.Filename,
			MimeType:       "application/octet-stream",
			SizeBytes:      fmt.Sprintf("%d", resp.Bytes),
			CreateTime:     formatGeminiTimestamp(resp.CreatedAt),
			State:          toGeminiFileState(resp.Status),
			URI:            resp.StorageURI,
			ExpirationTime: formatGeminiTimestamp(safeDerefInt64(resp.ExpiresAt)),
		},
	}
}

// ToGeminiFileListResponse converts a Bifrost file list response to Gemini format.
func ToGeminiFileListResponse(resp *schemas.BifrostFileListResponse) *GeminiFileListResponse {
	files := make([]GeminiFileResponse, len(resp.Data))
	for i, f := range resp.Data {
		files[i] = GeminiFileResponse{
			Name:           f.ID,
			DisplayName:    f.Filename,
			SizeBytes:      fmt.Sprintf("%d", f.Bytes),
			CreateTime:     formatGeminiTimestamp(f.CreatedAt),
			State:          toGeminiFileState(f.Status),
			ExpirationTime: formatGeminiTimestamp(safeDerefInt64(f.ExpiresAt)),
		}
	}

	result := &GeminiFileListResponse{
		Files: files,
	}

	return result
}

// ToGeminiFileRetrieveResponse converts a Bifrost file retrieve response to Gemini format.
func ToGeminiFileRetrieveResponse(resp *schemas.BifrostFileRetrieveResponse) *GeminiFileResponse {
	return &GeminiFileResponse{
		Name:           resp.ID,
		DisplayName:    resp.Filename,
		SizeBytes:      fmt.Sprintf("%d", resp.Bytes),
		CreateTime:     formatGeminiTimestamp(resp.CreatedAt),
		State:          toGeminiFileState(resp.Status),
		URI:            resp.StorageURI,
		ExpirationTime: formatGeminiTimestamp(safeDerefInt64(resp.ExpiresAt)),
	}
}

// toGeminiFileState converts Bifrost file status to Gemini state.
func toGeminiFileState(status schemas.FileStatus) string {
	switch status {
	case schemas.FileStatusProcessing:
		return "PROCESSING"
	case schemas.FileStatusProcessed:
		return "ACTIVE"
	case schemas.FileStatusError:
		return "FAILED"
	default:
		return strings.ToUpper(string(status))
	}
}

// formatGeminiTimestamp converts Unix timestamp to Gemini RFC3339 format.
func formatGeminiTimestamp(unixTime int64) string {
	if unixTime == 0 {
		return ""
	}
	return time.Unix(unixTime, 0).UTC().Format(time.RFC3339)
}

// safeDerefInt64 safely dereferences an int64 pointer.
func safeDerefInt64(ptr *int64) int64 {
	if ptr == nil {
		return 0
	}
	return *ptr
}
