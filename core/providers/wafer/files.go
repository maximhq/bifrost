package wafer

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Wafer File API Types

// WaferFileResponse represents a Wafer file response.
type WaferFileResponse struct {
	ID        string              `json:"id"`
	Purpose   schemas.FilePurpose `json:"purpose"`
	Filename  string              `json:"filename"`
	MimeType  string              `json:"mime_type"`
	Bytes     int64               `json:"bytes"`
	Status    string              `json:"status,omitempty"`
	CreatedAt string              `json:"created_at,omitempty"`
	ExpiresAt string              `json:"expires_at,omitempty"`
}

// ToBifrostFileStatus converts Wafer status to Bifrost status.
func ToBifrostFileStatus(status string) schemas.FileStatus {
	switch status {
	case "ready":
		return schemas.FileStatusProcessed
	case "pending":
		return schemas.FileStatusUploaded
	case "processing":
		return schemas.FileStatusProcessing
	default:
		return schemas.FileStatus(status)
	}
}

// ToBifrostFileUploadResponse converts Wafer file response to Bifrost file upload response.
func (r *WaferFileResponse) ToBifrostFileUploadResponse(latency time.Duration, sendBackRawRequest bool, sendBackRawResponse bool, rawRequest interface{}, rawResponse interface{}) *schemas.BifrostFileUploadResponse {
	// Wafer reports timestamps as ISO 8601 strings; Bifrost carries them as Unix seconds.
	var createdAt int64
	if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
		createdAt = t.Unix()
	}

	var expiresAt *int64
	if t, err := time.Parse(time.RFC3339, r.ExpiresAt); err == nil {
		exp := t.Unix()
		expiresAt = &exp
	}

	resp := &schemas.BifrostFileUploadResponse{
		ID:             r.ID,
		Object:         "file",
		Bytes:          r.Bytes,
		CreatedAt:      createdAt,
		Filename:       r.Filename,
		Purpose:        r.Purpose,
		Status:         ToBifrostFileStatus(r.Status),
		ExpiresAt:      expiresAt,
		StorageBackend: schemas.FileStorageAPI,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}

	if sendBackRawRequest {
		resp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}
