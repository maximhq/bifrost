package azure

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// AzureImageRequest is the struct for Image Generation requests by Azure.
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

// AzureImageResponse is the struct for Image Generation responses by Azure.
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

// ImageGeneration performs an Image Generation request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the bifrost response or an error if the request fails.
func (provider *AzureProvider) ImageGeneration(ctx context.Context, key schemas.Key,
	request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	// Convert bifrost request to Azure format.
	azureReq := ToAzureImageRequest(request)
	if azureReq == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: input is required", nil, provider.GetProviderKey())
	}

	// Make request
	resp, deployment, latency, err := provider.DoRequest(ctx, key, azureReq)
	if err != nil {
		return nil, err
	}

	// Convert Azure response to Bifrost format.
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
		Prompt: bifrostReq.Input.Prompt,
	}

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
		return nil, "", 0, providerUtils.NewBifrostOperationError("failed to unmarshal Azure image response", err, schemas.Azure)
	}

	return azureResponse, deployment, latency, nil

}

// ImageGenerationStream performs a streaming image generation request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *AzureProvider) ImageGenerationStream(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	key schemas.Key,
	request *schemas.BifrostImageGenerationRequest,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {

	// Validate api key configs
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	//
	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/images/generations?api-version=%s", key.AzureKeyConfig.Endpoint, deployment, *apiVersion)

	// Prepare Azure-specific headers
	authHeader := make(map[string]string)

	// Set Azure authentication - either Bearer token or api-key
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		authHeader["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
	} else {
		authHeader["api-key"] = key.Value
	}

	if !openai.StreamingEnabledImageModels[request.Model] {
		return provider.simulateImageStreaming(ctx, postHookRunner, key, request)
	}

	// Azure is OpenAI-compatible
	return openai.HandleOpenAIImageGenerationStreaming(
		ctx,
		provider.client,
		url,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil, nil,
		provider.logger,
	)

}

func (provider *AzureProvider) simulateImageStreaming(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	key schemas.Key,
	request *schemas.BifrostImageGenerationRequest,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {

	// Make non-streaming request
	resp, bifrostErr := provider.ImageGeneration(ctx, key, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	go func() {
		defer close(responseChan)

		chunkSize := 64 * 1024

		for imgIdx, img := range resp.Data {
			if img.B64JSON == "" {
				continue
			}

			b64Data := img.B64JSON
			chunkIndex := 0

			// Send partial chunks
			for offset := 0; offset < len(b64Data); offset += chunkSize {
				end := offset + chunkSize
				if end > len(b64Data) {
					end = len(b64Data)
				}

				isLastChunk := end >= len(b64Data) && imgIdx == len(resp.Data)-1

				chunkType := "image_generation.partial_image"
				if isLastChunk {
					chunkType = "image_generation.completed"
				}

				chunk := &schemas.BifrostImageGenerationStreamResponse{
					ID:            resp.ID,
					Type:          chunkType,
					Index:         imgIdx,
					ChunkIndex:    chunkIndex,
					PartialB64:    b64Data[offset:end],
					RevisedPrompt: img.RevisedPrompt,
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ImageGenerationStreamRequest,
						Provider:       provider.GetProviderKey(),
						ModelRequested: request.Model,
					},
				}

				if isLastChunk {
					chunk.Usage = resp.Usage
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
					providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, chunk),
					responseChan)

				chunkIndex++
			}
		}
	}()

	return responseChan, nil
}
