package azure

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// setAzureAuth sets the Azure authentication header on the request.
func (provider *AzureProvider) setAzureAuth(ctx context.Context, req *fasthttp.Request, key schemas.Key) {
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok && authToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
		req.Header.Del("api-key")
	} else {
		req.Header.Del("Authorization")
		req.Header.Set("api-key", key.Value)
	}
}

// AzureFileResponse represents an Azure file response (same as OpenAI).
type AzureFileResponse struct {
	ID            string              `json:"id"`
	Object        string              `json:"object"`
	Bytes         int64               `json:"bytes"`
	CreatedAt     int64               `json:"created_at"`
	Filename      string              `json:"filename"`
	Purpose       schemas.FilePurpose `json:"purpose"`
	Status        string              `json:"status,omitempty"`
	StatusDetails *string             `json:"status_details,omitempty"`
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
