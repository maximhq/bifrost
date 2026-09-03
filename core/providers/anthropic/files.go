package anthropic

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToAnthropicFileUploadResponse converts a Bifrost file upload response to Anthropic format.
func ToAnthropicFileUploadResponse(resp *schemas.BifrostFileUploadResponse) *AnthropicFileResponse {
	return &AnthropicFileResponse{
		ID:           resp.ID,
		Type:         resp.Object,
		Filename:     resp.Filename,
		MimeType:     resp.ContentType,
		SizeBytes:    resp.Bytes,
		CreatedAt:    formatAnthropicFileTimestamp(resp.CreatedAt),
		Downloadable: resp.Downloadable,
	}
}

// ToAnthropicFileListResponse converts a Bifrost file list response to Anthropic format.
func ToAnthropicFileListResponse(resp *schemas.BifrostFileListResponse) *AnthropicFileListResponse {
	data := make([]AnthropicFileResponse, len(resp.Data))
	for i, file := range resp.Data {
		data[i] = AnthropicFileResponse{
			ID:           file.ID,
			Type:         file.Object,
			Filename:     file.Filename,
			MimeType:     file.ContentType,
			SizeBytes:    file.Bytes,
			CreatedAt:    formatAnthropicFileTimestamp(file.CreatedAt),
			Downloadable: file.Downloadable,
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
		ID:           resp.ID,
		Type:         resp.Object,
		Filename:     resp.Filename,
		MimeType:     resp.ContentType,
		SizeBytes:    resp.Bytes,
		CreatedAt:    formatAnthropicFileTimestamp(resp.CreatedAt),
		Downloadable: resp.Downloadable,
	}
}

// ToAnthropicFileDeleteResponse converts a Bifrost file delete response to Anthropic format.
func ToAnthropicFileDeleteResponse(resp *schemas.BifrostFileDeleteResponse) *AnthropicFileDeleteResponse {
	respType := "file"
	if resp.Deleted {
		respType = "file_deleted"
	}
	return &AnthropicFileDeleteResponse{
		ID:   resp.ID,
		Type: respType,
	}
}

// formatAnthropicFileTimestamp converts Unix timestamp to Anthropic ISO timestamp format.
func formatAnthropicFileTimestamp(unixTime int64) string {
	if unixTime == 0 {
		return ""
	}
	return time.Unix(unixTime, 0).UTC().Format(time.RFC3339)
}
