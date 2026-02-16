package openai

import (
	"bytes"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// OpenAI File API Types

// OpenAIFileResponse represents an OpenAI file response.
type OpenAIFileResponse struct {
	ID            string              `json:"id"`
	Object        string              `json:"object"`
	Bytes         int64               `json:"bytes"`
	CreatedAt     int64               `json:"created_at"`
	Filename      string              `json:"filename"`
	Purpose       schemas.FilePurpose `json:"purpose"`
	Status        string              `json:"status,omitempty"`
	StatusDetails *string             `json:"status_details,omitempty"`
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
	case "processed", "completed":
		return schemas.FileStatusProcessed
	case "processing", "in_progress":
		return schemas.FileStatusProcessing
	case "error", "failed":
		return schemas.FileStatusError
	case "deleted", "cancelled":
		return schemas.FileStatusDeleted
	default:
		return schemas.FileStatus(status)
	}
}

// ToBifrostFileUploadResponse converts OpenAI file response to Bifrost file upload response.
func (r *OpenAIFileResponse) ToBifrostFileUploadResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawRequest bool, sendBackRawResponse bool, rawRequest interface{}, rawResponse interface{}) *schemas.BifrostFileUploadResponse {
	resp := schemas.AcquireBifrostFileUploadResponse()
	resp.ID = r.ID
	resp.Object = r.Object
	resp.Bytes = r.Bytes
	resp.CreatedAt = r.CreatedAt
	resp.Filename = r.Filename
	resp.Purpose = r.Purpose
	resp.Status = ToBifrostFileStatus(r.Status)
	resp.StatusDetails = r.StatusDetails
	resp.StorageBackend = schemas.FileStorageAPI
	resp.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType: schemas.FileUploadRequest,
		Provider:    providerName,
		Latency:     latency.Milliseconds(),
	}

	if sendBackRawRequest {
		resp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// ToBifrostFileRetrieveResponse converts OpenAI file response to Bifrost file retrieve response.
func (r *OpenAIFileResponse) ToBifrostFileRetrieveResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawRequest bool, sendBackRawResponse bool, rawRequest interface{}, rawResponse interface{}) *schemas.BifrostFileRetrieveResponse {
	resp := schemas.AcquireBifrostFileRetrieveResponse()
	resp.ID = r.ID
	resp.Object = r.Object
	resp.Bytes = r.Bytes
	resp.CreatedAt = r.CreatedAt
	resp.Filename = r.Filename
	resp.Purpose = r.Purpose
	resp.Status = ToBifrostFileStatus(r.Status)
	resp.StatusDetails = r.StatusDetails
	resp.StorageBackend = schemas.FileStorageAPI
	resp.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType: schemas.FileRetrieveRequest,
		Provider:    providerName,
		Latency:     latency.Milliseconds(),
	}

	if sendBackRawRequest {
		resp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}
	return resp
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
