package azure

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// setAzureAuth sets the Azure authentication header on the request for OpenAI models.
// It handles three authentication methods in order of priority:
// 1. Service Principal (client ID/secret/tenant ID) - uses Bearer token
// 2. Context token - uses Bearer token
// 3. API key - uses api-key header
func (provider *AzureProvider) setAzureAuth(ctx context.Context, req *fasthttp.Request, key schemas.Key) *schemas.BifrostError {
	// Service Principal authentication
	if key.AzureKeyConfig != nil && key.AzureKeyConfig.ClientID != nil &&
		key.AzureKeyConfig.ClientSecret != nil && key.AzureKeyConfig.TenantID != nil && key.AzureKeyConfig.ClientID.GetValue() != "" && key.AzureKeyConfig.ClientSecret.GetValue() != "" && key.AzureKeyConfig.TenantID.GetValue() != "" {
		cred, err := provider.getOrCreateAuth(key.AzureKeyConfig.TenantID.GetValue(), key.AzureKeyConfig.ClientID.GetValue(), key.AzureKeyConfig.ClientSecret.GetValue())
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to get or create Azure authentication", err, schemas.Azure)
		}

		scopes := getAzureScopes(key.AzureKeyConfig.Scopes)

		token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
			Scopes: scopes,
		})
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to get Azure access token", err, schemas.Azure)
		}

		if token.Token == "" {
			return providerUtils.NewBifrostOperationError("Azure access token is empty", fmt.Errorf("token is empty"), schemas.Azure)
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.Token))
		req.Header.Del("api-key")
		return nil
	}

	// Context token authentication
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok && authToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
		req.Header.Del("api-key")
		return nil
	}

	// API key authentication
	req.Header.Del("Authorization")
	req.Header.Set("api-key", key.Value.GetValue())
	return nil
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
	resp := schemas.AcquireBifrostFileUploadResponse()
	resp.ID = r.ID
	resp.Object = r.Object
	resp.Bytes = r.Bytes
	resp.CreatedAt = r.CreatedAt
	resp.Filename = r.Filename
	resp.Purpose = r.Purpose
	resp.Status = openai.ToBifrostFileStatus(r.Status)
	resp.StatusDetails = r.StatusDetails
	resp.StorageBackend = schemas.FileStorageAPI
	resp.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType: schemas.FileUploadRequest,
		Provider:    providerName,
		Latency:     latency.Milliseconds(),
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}
