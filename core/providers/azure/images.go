package azure

import (
	"context"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// AzureImagRequest is the struct for Image Generation requests.
type AzureImageRequest struct {
	Model          string  `json:"model"`
	Prompt         string  `json:"prompt"`
	N              *int    `json:"n,omitempty"`
	Size           *string `json:"size,omitempty"`
	Quality        *string `json:"quality,omitempty"`
	Style          *string `json:"style,omitempty"`
	ResponseFormat *string `json:"response_format,omitempty"`
	User           *string `json:"user,omitempty"`
}

type AzureImageResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL           string `json:"url,omitempty"`
		B64JSON       string `json:"b64_json,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
	Usage *AzureImageGenerationUsage `json:"usage"`
}

type AzureImageGenerationUsage struct {
	TotalTokens  int `json:"total_tokens,omitempty"`
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`

	InputTokensDetails *struct {
		TextTokens  int `json:"text_tokens,omitempty"`
		ImageTokens int `json:"image_tokens,omitempty"`
	} `json:"input_tokens_details,omitempty"`
}

func (provider *AzureProvider) ImageGeneration(ctx context.Context, key schemas.Key,
	request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	azureReq := ToAzureImageRequest(request)

	resp, deployment, latency, err := provider.DoRequest(ctx, key, azureReq)
	if err != nil {
		return nil, err
	}

	bifrostResp := ToBifrostImageResponse(resp, azureReq.Model, latency)

	bifrostResp.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResp.ExtraFields.ModelRequested = request.Model
	bifrostResp.ExtraFields.ModelDeployment = deployment
	bifrostResp.ExtraFields.RequestType = schemas.ImageGenerationRequest

	return bifrostResp, nil
}

// ToAzureImageRequest converts a Bifrost Image Request to Azure format
func ToAzureImageRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *AzureImageRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &AzureImageRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt}

	mapImageParams(bifrostReq.Params, req)
	return req
}

// This function maps Image generation parameters from a Bifrost Request to Azure format
func mapImageParams(p *schemas.ImageGenerationParameters, req *AzureImageRequest) {
	if p == nil {
		return
	}
	req.N = p.N
	req.Size = p.Size
	req.Quality = p.Quality
	req.Style = p.Style
	req.ResponseFormat = p.ResponseFormat
	req.User = p.User
}

// ToBifrostImageResponse converts an Azure Image Response to Bifrost format
func ToBifrostImageResponse(azureResponse *AzureImageResponse, requestModel string, latency time.Duration) *schemas.BifrostImageGenerationResponse {
	if azureResponse == nil {
		return nil
	}

	data := make([]schemas.ImageData, len(azureResponse.Data))
	for i, img := range azureResponse.Data {
		data[i] = schemas.ImageData{
			URL:           img.URL,
			B64JSON:       img.B64JSON,
			RevisedPrompt: img.RevisedPrompt,
			Index:         i,
		}
	}

	var usage *schemas.ImageUsage
	if azureResponse.Usage != nil {
		usage = &schemas.ImageUsage{
			PromptTokens: azureResponse.Usage.InputTokens,
			TotalTokens:  azureResponse.Usage.TotalTokens,
		}
	}

	return &schemas.BifrostImageGenerationResponse{
		ID:      uuid.NewString(),
		Created: azureResponse.Created,
		Model:   requestModel,
		Data:    data,
		Usage:   usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Azure,
			Latency:  latency.Milliseconds(),
		},
	}
}

// DoRequest sends a non-streaming image generation request to the azure images api
func (provider *AzureProvider) DoRequest(ctx context.Context, key schemas.Key, azureRequest *AzureImageRequest) (*AzureImageResponse, string, time.Duration, *schemas.BifrostError) {

	providerName := provider.GetProviderKey()

	jsonData, err := sonic.Marshal(azureRequest)
	if err != nil {
		return nil, "", 0, providerUtils.NewBifrostOperationError("could not serialize azure image generation request", err, providerName)
	}

	response, deployment, latency, bifrostErr := provider.completeRequest(ctx, jsonData, "images/generations", key, azureRequest.Model, schemas.ImageGenerationRequest)

	// Handle error response
	if bifrostErr != nil {
		return nil, "", 0, bifrostErr
	}
	azureResponse := &AzureImageResponse{}
	if err := sonic.Unmarshal(response, azureResponse); err != nil {
		return nil, "", 0, providerUtils.NewBifrostOperationError("Error unmarshalling image response from azure", err, schemas.Azure)
	}

	return azureResponse, deployment, latency, nil

}

// ImageGenerationStream is not implemented at this time.
func (provider *AzureProvider) ImageGenerationStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}
