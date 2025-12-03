package openai

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// OpenAIImageGenerationRequest is the struct for Image Generation requests by OpenAI.
type OpenAIImageGenerationRequest struct {
	Model          string  `json:"model"`
	Prompt         string  `json:"prompt"`
	N              *int    `json:"n,omitempty"`
	Size           *string `json:"size,omitempty"`
	Quality        *string `json:"quality,omitempty"`
	Style          *string `json:"style,omitempty"`
	Stream         *bool   `json:"stream,omitempty"`
	ResponseFormat *string `json:"response_format,omitempty"`
	User           *string `json:"user,omitempty"`
}

// OpenAIImageGenerationResponse is the struct for Image Generation responses by OpenAI.
type OpenAIImageGenerationResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL           string `json:"url,omitempty"`
		B64JSON       string `json:"b64_json,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
	Background   *string                     `json:"background,omitempty"`
	OutputFormat *string                     `json:"output_format,omitempty"`
	Size         *string                     `json:"size,omitempty"`
	Quality      *string                     `json:"quality,omitempty"`
	Usage        *OpenAIImageGenerationUsage `json:"usage"`
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

// OpenAIImageStreamResponse is the struct for Image Generation streaming responses by OpenAI.
type OpenAIImageStreamResponse struct {
	Type              string                      `json:"type,omitempty"`
	B64JSON           *string                     `json:"b64_json,omitempty"`
	PartialImageIndex int                         `json:"partial_image_index,omitempty"`
	Usage             *OpenAIImageGenerationUsage `json:"usage,omitempty"`
}

// ImageGeneration performs an Image Generation request to OpenAI's API.
// It formats the request, sends it to OpenAI, and processes the response.
// Returns a BifrostResponse containing the bifrost response or an error if the request fails.
func (provider *OpenAIProvider) ImageGeneration(ctx context.Context, key schemas.Key,
	req *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageGenerationRequest); err != nil {
		return nil, err // Handle error
	}
	openaiReq := ToOpenAIImageGenerationRequest(req)
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

// ToOpenAIImageGenerationRequest converts a Bifrost Image Request to OpenAI format
func ToOpenAIImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *OpenAIImageGenerationRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &OpenAIImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	mapImageParams(bifrostReq.Params, req)
	return req
}

// This function maps Image generation parameters from a Bifrost Request to OpenAI format
func mapImageParams(p *schemas.ImageGenerationParameters, req *OpenAIImageGenerationRequest) {
	if p == nil {
		return
	}
	req.Stream = p.Stream
	req.N = p.N
	req.Size = p.Size
	req.Quality = p.Quality
	req.Style = p.Style
	req.ResponseFormat = p.ResponseFormat
	req.User = p.User
}

// ToBifrostImageResponse converts an OpenAI Image Response to Bifrost format
func ToBifrostImageResponse(openaiResponse *OpenAIImageGenerationResponse, requestModel string, latency time.Duration) *schemas.BifrostImageGenerationResponse {
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
func (provider *OpenAIProvider) DoRequest(ctx context.Context, key schemas.Key, openaiRequest *OpenAIImageGenerationRequest) (*OpenAIImageGenerationResponse, time.Duration, *schemas.BifrostError) {

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
	openaiResponse := &OpenAIImageGenerationResponse{}
	if err := sonic.Unmarshal(resp.Body(), openaiResponse); err != nil {
		return nil, 0, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	return openaiResponse, latency, nil

}

// ImageGenerationStream handles streaming for image generation.
// It formats the request body, creates HTTP request, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) ImageGenerationStream(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	key schemas.Key,
	request *schemas.BifrostImageGenerationRequest,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if image generation stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageGenerationStreamRequest); err != nil {
		return nil, err
	}

	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}
	// Use shared streaming logic
	return HandleOpenAIImageGenerationStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/images/generations", schemas.ImageGenerationRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

func HandleOpenAIImageGenerationStreaming(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostImageGenerationRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	customRequestConverter func(*schemas.BifrostImageGenerationRequest) (any, error),
	postRequestConverter func(*OpenAIImageGenerationRequest) *OpenAIImageGenerationRequest,
	postResponseConverter func(*schemas.BifrostImageGenerationResponse) *schemas.BifrostImageGenerationResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {

	// Set headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		// Copy auth header to headers
		maps.Copy(headers, authHeader)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			if customRequestConverter != nil {
				return customRequestConverter(request)
			}
			reqBody := ToOpenAIImageGenerationRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
				if postRequestConverter != nil {
					reqBody = postRequestConverter(reqBody)
				}
			}
			return reqBody, nil
		},
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Updating request
	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(jsonBody)

	// Make the request
	err := client.Do(req, resp)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, parseStreamOpenAIError(resp, schemas.ImageGenerationStreamRequest, providerName, request.Model)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// Check if context is done before processing
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			// Check for end of stream
			if line == "" {
				break
			}

			var jsonData string

			// Parse SSE data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				jsonData = after
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			var bifrostErr schemas.BifrostError
			if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
				if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.ImageGenerationStreamRequest,
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, logger)
					return
				}
			}

			// Parse into bifrost response
			var response *OpenAIImageStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			// TODO: Track Usage Correctly
			// Handle final chunks (when stream_options include_usage is true)
			if response.Type == "image_generation.completed" && response.Usage != nil {
				// Collect usage information and send at the end of the stream
				// Usage is contained within the completion message
			}

			// TODO: Handle regular image chunks
			// Handle image chunks
			if response.Type == "image_generation.partial_image" || response.Type == "image_generation.completed" {

				bifrostChunk := &schemas.BifrostImageGenerationStreamResponse{
					ID:         uuid.NewString(),
					Type:       response.Type,
					Index:      response.PartialImageIndex,
					ChunkIndex: chunkIndex,
					PartialB64: "",
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ImageGenerationStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}

				// Split data into 64KB chunks
				if response.B64JSON != nil {
					b64Data := *response.B64JSON
					chunkSize := 64 * 1024
					for offset := 0; offset < len(b64Data); offset += chunkSize {
						end := offset + chunkSize
						if end > len(b64Data) {
							end = len(b64Data)
						}

						chunkIndex++
						chunk := b64Data[offset:end]
						bifrostChunk.PartialB64 = chunk
						bifrostChunk.ChunkIndex = chunkIndex

						lastChunkTime = time.Now()
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, bifrostChunk), responseChan)
					}
				} else {
					chunkIndex++
				}

				// Handle completion chunk
				if response.Type == "image_generation.completed" {
					if response.Usage != nil {
						bifrostChunk.Usage = &schemas.ImageUsage{
							PromptTokens: response.Usage.InputTokens,
							TotalTokens:  response.Usage.TotalTokens,
						}
					}
					bifrostChunk.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
						providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, bifrostChunk),
						responseChan)
				}
			}
		}
		// }

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ImageGenerationStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}
