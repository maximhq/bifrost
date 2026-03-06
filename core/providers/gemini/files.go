package gemini

import (
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Gemini Files API types
// The Gemini Files API allows uploading files for use with multimodal models.

// GeminiFileResponse represents a file object from Gemini's API.
type GeminiFileResponse struct {
	Name           string                   `json:"name"`                     // Resource name (e.g., "files/abc123")
	DisplayName    string                   `json:"displayName"`              // User-provided display name
	MimeType       string                   `json:"mimeType"`                 // MIME type of the file
	SizeBytes      string                   `json:"sizeBytes"`                // Size in bytes (as string)
	CreateTime     string                   `json:"createTime"`               // RFC3339 timestamp
	UpdateTime     string                   `json:"updateTime"`               // RFC3339 timestamp
	ExpirationTime string                   `json:"expirationTime,omitempty"` // RFC3339 timestamp when file will be deleted
	SHA256Hash     string                   `json:"sha256Hash"`               // Base64 encoded SHA256 hash
	URI            string                   `json:"uri"`                      // URI for accessing the file
	State          string                   `json:"state"`                    // "PROCESSING", "ACTIVE", "FAILED"
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

// ToGeminiFileListResponse converts a Bifrost file list response to Gemini format.
func ToGeminiFileListResponse(resp *schemas.BifrostFileListResponse) *GeminiFileListResponse {
	files := make([]GeminiFileResponse, len(resp.Data))
	for i, f := range resp.Data {
		updateAt := f.UpdatedAt
		if updateAt == 0 {
			updateAt = f.CreatedAt
		}
		files[i] = GeminiFileResponse{
			Name:           f.ID,
			DisplayName:    f.Filename,
			SizeBytes:      fmt.Sprintf("%d", f.Bytes),
			CreateTime:     formatGeminiTimestamp(f.CreatedAt),
			UpdateTime:     formatGeminiTimestamp(updateAt),
			State:          toGeminiFileState(f.Status),
			ExpirationTime: formatGeminiTimestamp(safeDerefInt64(f.ExpiresAt)),
		}
	}
	result := &GeminiFileListResponse{Files: files}
	if resp.After != nil && *resp.After != "" {
		result.NextPageToken = *resp.After
	}
	return result
}

// ToGeminiFileRetrieveResponse converts a Bifrost file retrieve response to Gemini format.
func ToGeminiFileRetrieveResponse(resp *schemas.BifrostFileRetrieveResponse) *GeminiFileResponse {
	updateAt := resp.UpdatedAt
	if updateAt == 0 {
		updateAt = resp.CreatedAt
	}
	return &GeminiFileResponse{
		Name:           resp.ID,
		DisplayName:    resp.Filename,
		SizeBytes:      fmt.Sprintf("%d", resp.Bytes),
		CreateTime:     formatGeminiTimestamp(resp.CreatedAt),
		UpdateTime:     formatGeminiTimestamp(updateAt),
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

// ToGeminiFileUploadResponse converts a Bifrost file upload response to Gemini format.
func ToGeminiFileUploadResponse(resp *schemas.BifrostFileUploadResponse) map[string]interface{} {
	file := map[string]interface{}{
		"name":        resp.ID,
		"displayName": resp.Filename,
		"mimeType":    "application/octet-stream",
		"sizeBytes":   fmt.Sprintf("%d", resp.Bytes),
		"createTime":  formatGeminiTimestamp(resp.CreatedAt),
		"state":       toGeminiFileState(resp.Status),
		"uri":         resp.StorageURI,
	}
	if exp := formatGeminiTimestamp(safeDerefInt64(resp.ExpiresAt)); exp != "" {
		file["expirationTime"] = exp
	}
	return map[string]interface{}{
		"file": file,
	}
}
