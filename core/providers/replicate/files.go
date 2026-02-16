package replicate

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Replicate File API Converters

// ToBifrostFileStatus converts Replicate file status to Bifrost file status.
// Replicate doesn't explicitly provide status, so we infer from the response.
func ToBifrostFileStatus(fileResp *ReplicateFileResponse) schemas.FileStatus {
	// If file has all required fields and is accessible, it's processed
	if fileResp.ID != "" && fileResp.Size > 0 {
		return schemas.FileStatusProcessed
	}
	return schemas.FileStatusUploaded
}

// ToBifrostFileUploadResponse converts Replicate file response to Bifrost file upload response.
func (r *ReplicateFileResponse) ToBifrostFileUploadResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawRequest bool, sendBackRawResponse bool, rawRequest interface{}, rawResponse interface{}) *schemas.BifrostFileUploadResponse {
	resp := schemas.AcquireBifrostFileUploadResponse()
	resp.ID = r.ID
	resp.Object = "file"
	resp.Bytes = r.Size
	resp.CreatedAt = ParseReplicateTimestamp(r.CreatedAt)
	resp.Filename = r.Name
	resp.Purpose = schemas.FilePurposeBatch // Replicate uses files primarily for batch/general purposes
	resp.Status = ToBifrostFileStatus(r)
	resp.StorageBackend = schemas.FileStorageAPI
	resp.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType: schemas.FileUploadRequest,
		Provider:    providerName,
		Latency:     latency.Milliseconds(),
	}

	// Add ExpiresAt if present
	if r.ExpiresAt != "" {
		expiresAt := ParseReplicateTimestamp(r.ExpiresAt)
		if expiresAt > 0 {
			resp.ExpiresAt = &expiresAt
		}
	}

	if sendBackRawRequest {
		resp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// ToBifrostFileRetrieveResponse converts Replicate file response to Bifrost file retrieve response.
func (r *ReplicateFileResponse) ToBifrostFileRetrieveResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawRequest bool, sendBackRawResponse bool, rawRequest interface{}, rawResponse interface{}) *schemas.BifrostFileRetrieveResponse {
	resp := schemas.AcquireBifrostFileRetrieveResponse()
	resp.ID = r.ID
	resp.Object = "file"
	resp.Bytes = r.Size
	resp.CreatedAt = ParseReplicateTimestamp(r.CreatedAt)
	resp.Filename = r.Name
	resp.Purpose = schemas.FilePurposeBatch
	resp.Status = ToBifrostFileStatus(r)
	resp.StorageBackend = schemas.FileStorageAPI
	resp.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType: schemas.FileRetrieveRequest,
		Provider:    providerName,
		Latency:     latency.Milliseconds(),
	}

	// Add ExpiresAt if present
	if r.ExpiresAt != "" {
		expiresAt := ParseReplicateTimestamp(r.ExpiresAt)
		if expiresAt > 0 {
			resp.ExpiresAt = &expiresAt
		}
	}

	if sendBackRawRequest {
		resp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}
