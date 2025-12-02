package openai

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// OpenAIImageRequest is the struct for Image Generation requests by OpenAI.
type OpenAIImageRequest struct {
	Model          string  `json:"model"`
	Prompt         string  `json:"prompt"`
	N              *int    `json:"n,omitempty"`
	Size           *string `json:"size,omitempty"`
	Quality        *string `json:"quality,omitempty"`
	Style          *string `json:"style,omitempty"`
	ResponseFormat *string `json:"response_format,omitempty"`
	User           *string `json:"user,omitempty"`
}

// OpenAIImageResponse is the struct for Image Generation responses by OpenAI.
type OpenAIImageResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL           string `json:"url,omitempty"`
		B64JSON       string `json:"b64_json,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
	Usage *OpenAIImageGenerationUsage `json:"usage"`
}

type OpenAIImageGenerationUsage struct {
	TotalTokens  int `json:"total_tokens,omitempty"`
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`

	InputTokensDetails *struct {
		TextTokens  int `json:"text_tokens,omitempty"`
		ImageTokens int `json:"image_tokens,omitempty"`
	} `json:"input_tokens_details,omitempty"`
}

// ImageGeneration performs an Image Generation request to OpenAI's API.
// It formats the request, sends it to OpenAI, and processes the response.
// Returns a BifrostResponse containing the bifrost response or an error if the request fails.
func (provider *OpenAIProvider) ImageGeneration(ctx context.Context, key schemas.Key,
	req *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageGenerationRequest); err != nil {
		return nil, err // Handle error
	}
	openaiReq := ToOpenAIImageRequest(req)
	if openaiReq == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: input is required", nil, provider.GetProviderKey())
	}

	resp, latency, err := provider.DoRequest(ctx, key, openaiReq)
	if err != nil {
		return nil, err
	}

	bifrostResp := ToBifrostImageResponse(resp, openaiReq.Model, latency)

	bifrostResp.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResp.ExtraFields.ModelRequested = openaiReq.Model
	bifrostResp.ExtraFields.RequestType = schemas.ImageGenerationRequest

	return bifrostResp, nil
}

// ToOpenAIImageRequest converts a Bifrost Image Request to OpenAI format
func ToOpenAIImageRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *OpenAIImageRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &OpenAIImageRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt}

	mapImageParams(bifrostReq.Params, req)
	return req
}

// This function maps Image generation parameters from a Bifrost Request to OpenAI format
func mapImageParams(p *schemas.ImageGenerationParameters, req *OpenAIImageRequest) {
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

// ToBifrostImageResponse converts an OpenAI Image Response to Bifrost format
func ToBifrostImageResponse(openaiResponse *OpenAIImageResponse, requestModel string, latency time.Duration) *schemas.BifrostImageGenerationResponse {
	if openaiResponse == nil {
		return nil
	}

	data := make([]schemas.ImageData, len(openaiResponse.Data))
	for i, img := range openaiResponse.Data {
		data[i] = schemas.ImageData{
			URL:           img.URL,
			B64JSON:       img.B64JSON,
			RevisedPrompt: img.RevisedPrompt,
			Index:         i,
		}
	}

	var usage *schemas.ImageUsage
	if openaiResponse.Usage != nil {
		usage = &schemas.ImageUsage{
			PromptTokens: openaiResponse.Usage.InputTokens,
			TotalTokens:  openaiResponse.Usage.TotalTokens,
		}
	}

	return &schemas.BifrostImageGenerationResponse{
		ID:      uuid.NewString(),
		Created: openaiResponse.Created,
		Model:   requestModel,
		Data:    data,
		Usage:   usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.OpenAI,
			Latency:  latency.Milliseconds(),
		},
	}
}

// DoRequest sends a non-streaming image generation request to the openai images api
func (provider *OpenAIProvider) DoRequest(ctx context.Context, key schemas.Key, openaiRequest *OpenAIImageRequest) (*OpenAIImageResponse, time.Duration, *schemas.BifrostError) {

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/images/generations", schemas.ImageGenerationRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Serialize the request payload
	jsonData, err := sonic.Marshal(openaiRequest)
	if err != nil {
		return nil, 0, providerUtils.NewBifrostOperationError("Error marshalling json for openai image generation", err, schemas.OpenAI)
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, 0, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, 0, ParseOpenAIError(resp, schemas.ImageGenerationRequest, providerName, openaiRequest.Model)
	}

	// Create final response with the image data
	openaiResponse := &OpenAIImageResponse{}
	if err := sonic.Unmarshal(resp.Body(), openaiResponse); err != nil {
		return nil, 0, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	return openaiResponse, latency, nil

}

// ImageGenerationStream is not implemented at this time.
func (provider *OpenAIProvider) ImageGenerationStream(ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	key schemas.Key,
	request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}
