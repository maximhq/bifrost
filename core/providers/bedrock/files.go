package bedrock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// AWS Bedrock uses S3 for batch input/output files.
// These file operations use S3 REST API with AWS Signature V4 authentication.

// FileUpload uploads a file to S3 for Bedrock batch processing.
func (provider *BedrockProvider) FileUpload(ctx context.Context, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		provider.logger.Error("file upload operation not allowed: %s", err.Error.Message)
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		provider.logger.Error("bedrock key config is is missing in file upload request")
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Get S3 bucket from storage config or extra params
	s3Bucket := ""
	s3Prefix := ""
	if request.StorageConfig != nil && request.StorageConfig.S3 != nil {
		if request.StorageConfig.S3.Bucket != "" {
			s3Bucket = request.StorageConfig.S3.Bucket
		}
		if request.StorageConfig.S3.Prefix != "" {
			s3Prefix = request.StorageConfig.S3.Prefix
		}
	} else if request.ExtraParams != nil {
		if bucket, ok := request.ExtraParams["s3_bucket"].(string); ok && bucket != "" {
			s3Bucket = bucket
		}
		if prefix, ok := request.ExtraParams["s3_prefix"].(string); ok && prefix != "" {
			s3Prefix = prefix
		}
	}

	if s3Bucket == "" {
		provider.logger.Error("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)")
		return nil, providerUtils.NewBifrostOperationError("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)", nil, providerName)
	}

	// Parse bucket name and optional prefix from s3Bucket (could be "bucket-name" or "s3://bucket-name/prefix/")
	bucketName, bucketPrefix := parseS3URI(s3Bucket)
	if bucketPrefix != "" {
		s3Prefix = bucketPrefix + s3Prefix
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Generate S3 key for the file
	filename := request.Filename
	if filename == "" {
		filename = fmt.Sprintf("file-%d.jsonl", time.Now().UnixNano())
	}
	s3Key := s3Prefix + "/" + filename

	provider.logger.Debug("uploading file to s3: %s", s3Key)

	// Build S3 PUT request URL
	// Note: Don't use url.PathEscape on the whole path as it escapes "/" to "%2F"
	// which causes signature mismatch (signer uses decoded path, but raw path is sent)
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, s3Key)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(request.File))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	httpReq.Header.Set("Content-Type", "application/octet-stream")
	httpReq.ContentLength = int64(len(request.File))

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
		provider.logger.Error("error signing request: %s", err.Error.Message)
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		provider.logger.Error("s3 upload failed: %d", resp.StatusCode)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 upload failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
	}

	// Return S3 URI as the file ID
	s3URI := fmt.Sprintf("s3://%s/%s", bucketName, s3Key)

	return &schemas.BifrostFileUploadResponse{
		ID:             s3URI,
		Object:         "file",
		Bytes:          int64(len(request.File)),
		CreatedAt:      time.Now().Unix(),
		Filename:       filename,
		Purpose:        request.Purpose,
		Status:         schemas.FileStatusProcessed,
		StorageBackend: schemas.FileStorageS3,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileUploadRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileList lists files in the S3 bucket used for Bedrock batch processing.
func (provider *BedrockProvider) FileList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no keys provided", providerName)
	}

	key := keys[0]
	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Get S3 bucket from storage config or extra params
	s3Bucket := ""
	s3Prefix := "bifrost-files/"
	if request.StorageConfig != nil && request.StorageConfig.S3 != nil {
		if request.StorageConfig.S3.Bucket != "" {
			s3Bucket = request.StorageConfig.S3.Bucket
		}
		if request.StorageConfig.S3.Prefix != "" {
			s3Prefix = request.StorageConfig.S3.Prefix
		}
	}
	if request.ExtraParams != nil {
		if bucket, ok := request.ExtraParams["s3_bucket"].(string); ok && bucket != "" {
			s3Bucket = bucket
		}
		if prefix, ok := request.ExtraParams["s3_prefix"].(string); ok && prefix != "" {
			s3Prefix = prefix
		}
	}

	if s3Bucket == "" {
		return nil, providerUtils.NewBifrostOperationError("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)", nil, providerName)
	}

	bucketName, bucketPrefix := parseS3URI(s3Bucket)
	if bucketPrefix != "" {
		s3Prefix = bucketPrefix + s3Prefix
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Build S3 ListObjectsV2 request
	params := url.Values{}
	params.Set("list-type", "2")
	params.Set("prefix", s3Prefix)
	if request.Limit > 0 {
		params.Set("max-keys", fmt.Sprintf("%d", request.Limit))
	}
	if request.After != nil {
		params.Set("continuation-token", *request.After)
	}

	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/?%s", bucketName, region, params.Encode())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, providerName)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 list failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
	}

	// Parse S3 ListObjectsV2 XML response
	var listResp S3ListObjectsResponse
	if err := parseS3ListResponse(body, &listResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError("error parsing S3 response", err, providerName)
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    make([]schemas.FileObject, len(listResp.Contents)),
		HasMore: listResp.IsTruncated,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, obj := range listResp.Contents {
		s3URI := fmt.Sprintf("s3://%s/%s", bucketName, obj.Key)
		filename := obj.Key
		if idx := strings.LastIndex(obj.Key, "/"); idx >= 0 {
			filename = obj.Key[idx+1:]
		}
		bifrostResp.Data[i] = schemas.FileObject{
			ID:        s3URI,
			Object:    "file",
			Bytes:     obj.Size,
			CreatedAt: obj.LastModified.Unix(),
			Filename:  filename,
			Purpose:   schemas.FilePurposeBatch,
			Status:    schemas.FileStatusProcessed,
		}
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves S3 object metadata for Bedrock batch processing.
func (provider *BedrockProvider) FileRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil, providerName)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil, providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Build S3 HEAD request
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, s3Key)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodHead, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 HEAD failed with status %d", resp.StatusCode), nil, resp.StatusCode, providerName, nil, nil)
	}

	// Extract metadata from headers
	filename := s3Key
	if idx := strings.LastIndex(s3Key, "/"); idx >= 0 {
		filename = s3Key[idx+1:]
	}

	var createdAt int64
	if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
		if t, err := time.Parse(time.RFC1123, lastMod); err == nil {
			createdAt = t.Unix()
		}
	}

	return &schemas.BifrostFileRetrieveResponse{
		ID:             request.FileID,
		Object:         "file",
		Bytes:          resp.ContentLength,
		CreatedAt:      createdAt,
		Filename:       filename,
		Purpose:        string(schemas.FilePurposeBatch),
		Status:         schemas.FileStatusProcessed,
		StorageBackend: schemas.FileStorageS3,
		StorageURI:     request.FileID,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileDelete deletes an S3 object used for Bedrock batch processing.
func (provider *BedrockProvider) FileDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil, providerName)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil, providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Build S3 DELETE request
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, s3Key)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	// S3 DELETE returns 204 No Content on success
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 DELETE failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
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

// FileContent downloads S3 object content for Bedrock batch processing.
func (provider *BedrockProvider) FileContent(ctx context.Context, key schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil, providerName)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil, providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Build S3 GET request
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, s3Key)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 GET failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading S3 object content", err, providerName)
	}

	contentType := resp.Header.Get("Content-Type")
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

// parseS3URI parses an S3 URI (s3://bucket/key or bucket-name) and returns bucket name and key.
func parseS3URI(uri string) (bucket, key string) {
	if strings.HasPrefix(uri, "s3://") {
		uri = strings.TrimPrefix(uri, "s3://")
		parts := strings.SplitN(uri, "/", 2)
		bucket = parts[0]
		if len(parts) > 1 {
			key = parts[1]
		}
	} else {
		// Assume it's just a bucket name
		bucket = uri
	}
	return
}

// S3ListObjectsResponse represents S3 ListObjectsV2 response.
type S3ListObjectsResponse struct {
	Contents              []S3Object `json:"contents"`
	IsTruncated           bool       `json:"isTruncated"`
	NextContinuationToken string     `json:"nextContinuationToken,omitempty"`
}

// S3Object represents an S3 object in list response.
type S3Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
}

// parseS3ListResponse parses S3 ListObjectsV2 XML response.
func parseS3ListResponse(body []byte, resp *S3ListObjectsResponse) error {
	// S3 returns XML, so we need to parse it
	// Try JSON first (some S3-compatible services return JSON)
	if err := sonic.Unmarshal(body, resp); err == nil && len(resp.Contents) > 0 {
		return nil
	}

	// Parse XML using simple string matching for key fields
	// This is a lightweight approach that doesn't require encoding/xml
	bodyStr := string(body)

	// Parse IsTruncated
	if strings.Contains(bodyStr, "<IsTruncated>true</IsTruncated>") {
		resp.IsTruncated = true
	}

	// Parse NextContinuationToken
	if start := strings.Index(bodyStr, "<NextContinuationToken>"); start >= 0 {
		start += len("<NextContinuationToken>")
		if end := strings.Index(bodyStr[start:], "</NextContinuationToken>"); end >= 0 {
			resp.NextContinuationToken = bodyStr[start : start+end]
		}
	}

	// Parse Contents
	contents := bodyStr
	for {
		start := strings.Index(contents, "<Contents>")
		if start < 0 {
			break
		}
		end := strings.Index(contents[start:], "</Contents>")
		if end < 0 {
			break
		}

		contentBlock := contents[start : start+end+len("</Contents>")]
		contents = contents[start+end+len("</Contents>"):]

		obj := S3Object{}

		// Parse Key
		if keyStart := strings.Index(contentBlock, "<Key>"); keyStart >= 0 {
			keyStart += len("<Key>")
			if keyEnd := strings.Index(contentBlock[keyStart:], "</Key>"); keyEnd >= 0 {
				obj.Key = contentBlock[keyStart : keyStart+keyEnd]
			}
		}

		// Parse Size
		if sizeStart := strings.Index(contentBlock, "<Size>"); sizeStart >= 0 {
			sizeStart += len("<Size>")
			if sizeEnd := strings.Index(contentBlock[sizeStart:], "</Size>"); sizeEnd >= 0 {
				sizeStr := contentBlock[sizeStart : sizeStart+sizeEnd]
				fmt.Sscanf(sizeStr, "%d", &obj.Size)
			}
		}

		// Parse LastModified
		if lmStart := strings.Index(contentBlock, "<LastModified>"); lmStart >= 0 {
			lmStart += len("<LastModified>")
			if lmEnd := strings.Index(contentBlock[lmStart:], "</LastModified>"); lmEnd >= 0 {
				lmStr := contentBlock[lmStart : lmStart+lmEnd]
				if t, err := time.Parse(time.RFC3339, lmStr); err == nil {
					obj.LastModified = t
				}
			}
		}

		if obj.Key != "" {
			resp.Contents = append(resp.Contents, obj)
		}
	}

	return nil
}

// ==================== BEDROCK FILE TYPE CONVERTERS ====================

// ToBedrockFileUploadResponse converts a Bifrost file upload response to Bedrock format.
func ToBedrockFileUploadResponse(resp *schemas.BifrostFileUploadResponse) *BedrockFileUploadResponse {
	if resp == nil {
		return nil
	}

	// Parse S3 URI to get bucket and key
	bucket, key := parseS3URI(resp.ID)

	return &BedrockFileUploadResponse{
		S3Uri:       resp.ID,
		Bucket:      bucket,
		Key:         key,
		SizeBytes:   resp.Bytes,
		ContentType: "application/jsonl",
		CreatedAt:   resp.CreatedAt,
	}
}

// ToBedrockFileListResponse converts a Bifrost file list response to Bedrock format.
func ToBedrockFileListResponse(resp *schemas.BifrostFileListResponse) *BedrockFileListResponse {
	if resp == nil {
		return nil
	}

	files := make([]BedrockFileInfo, len(resp.Data))
	for i, f := range resp.Data {
		_, key := parseS3URI(f.ID)
		files[i] = BedrockFileInfo{
			S3Uri:        f.ID,
			Key:          key,
			SizeBytes:    f.Bytes,
			LastModified: f.CreatedAt,
		}
	}

	return &BedrockFileListResponse{
		Files:       files,
		IsTruncated: resp.HasMore,
	}
}

// ToBedrockFileRetrieveResponse converts a Bifrost file retrieve response to Bedrock format.
func ToBedrockFileRetrieveResponse(resp *schemas.BifrostFileRetrieveResponse) *BedrockFileRetrieveResponse {
	if resp == nil {
		return nil
	}

	_, key := parseS3URI(resp.ID)

	return &BedrockFileRetrieveResponse{
		S3Uri:        resp.ID,
		Key:          key,
		SizeBytes:    resp.Bytes,
		LastModified: resp.CreatedAt,
		ContentType:  "application/jsonl",
	}
}

// ToBedrockFileDeleteResponse converts a Bifrost file delete response to Bedrock format.
func ToBedrockFileDeleteResponse(resp *schemas.BifrostFileDeleteResponse) *BedrockFileDeleteResponse {
	if resp == nil {
		return nil
	}

	return &BedrockFileDeleteResponse{
		S3Uri:   resp.ID,
		Deleted: resp.Deleted,
	}
}

// ToBedrockFileContentResponse converts a Bifrost file content response to Bedrock format.
func ToBedrockFileContentResponse(resp *schemas.BifrostFileContentResponse) *BedrockFileContentResponse {
	if resp == nil {
		return nil
	}

	return &BedrockFileContentResponse{
		S3Uri:       resp.FileID,
		Content:     resp.Content,
		ContentType: resp.ContentType,
		SizeBytes:   int64(len(resp.Content)),
	}
}

// ==================== S3 API XML FORMATTERS ====================

// ToS3ListObjectsV2XML converts a Bifrost file list response to S3 ListObjectsV2 XML format.
func ToS3ListObjectsV2XML(resp *schemas.BifrostFileListResponse, bucket, prefix string, maxKeys int) []byte {
	if resp == nil {
		return []byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></ListBucketResult>`)
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	sb.WriteString(fmt.Sprintf("<Name>%s</Name>", bucket))
	sb.WriteString(fmt.Sprintf("<Prefix>%s</Prefix>", prefix))
	sb.WriteString(fmt.Sprintf("<KeyCount>%d</KeyCount>", len(resp.Data)))
	sb.WriteString(fmt.Sprintf("<MaxKeys>%d</MaxKeys>", maxKeys))
	if resp.HasMore {
		sb.WriteString("<IsTruncated>true</IsTruncated>")
	} else {
		sb.WriteString("<IsTruncated>false</IsTruncated>")
	}

	for _, f := range resp.Data {
		// Extract key from S3 URI
		_, key := parseS3URI(f.ID)
		sb.WriteString("<Contents>")
		sb.WriteString(fmt.Sprintf("<Key>%s</Key>", key))
		sb.WriteString(fmt.Sprintf("<Size>%d</Size>", f.Bytes))
		if f.CreatedAt > 0 {
			sb.WriteString(fmt.Sprintf("<LastModified>%s</LastModified>", time.Unix(f.CreatedAt, 0).UTC().Format(time.RFC3339)))
		}
		sb.WriteString("<StorageClass>STANDARD</StorageClass>")
		sb.WriteString("</Contents>")
	}

	sb.WriteString("</ListBucketResult>")
	return []byte(sb.String())
}

// ToS3ErrorXML converts an error to S3 error XML format.
func ToS3ErrorXML(code, message, resource, requestID string) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString("<Error>")
	sb.WriteString(fmt.Sprintf("<Code>%s</Code>", code))
	sb.WriteString(fmt.Sprintf("<Message>%s</Message>", message))
	sb.WriteString(fmt.Sprintf("<Resource>%s</Resource>", resource))
	sb.WriteString(fmt.Sprintf("<RequestId>%s</RequestId>", requestID))
	sb.WriteString("</Error>")
	return []byte(sb.String())
}
