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

type ImageGenerationEventType string

const (
	ImageGenerationPartial   ImageGenerationEventType = "image_generation.partial_image"
	ImageGenerationCompleted ImageGenerationEventType = "image_generation.completed"
)

var StreamingEnabledImageModels = map[string]bool{
	"gpt-image-1": true,
	"dall-e-2":    false,
	"dall-e-3":    false,
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
func (provider *OpenAIProvider) doRequest(ctx context.Context, key schemas.Key, openaiRequest *OpenAIImageGenerationRequest) (*OpenAIImageGenerationResponse, time.Duration, *schemas.BifrostError) {

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
				continue
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
			if response.Type == ImageGenerationCompleted && response.Usage != nil {
				// Collect usage information and send at the end of the stream
				// Usage is contained within the completion message
			}
			// Handle image chunks
			bifrostChunk := &schemas.BifrostImageGenerationStreamResponse{
				ID:         uuid.NewString(),
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
			switch response.Type {
			case ImageGenerationCompleted:
				bifrostChunk.Type = string(ImageGenerationCompleted)
			case ImageGenerationPartial:
				bifrostChunk.Type = string(ImageGenerationPartial)
			}

			// Split data into 64KB chunks
			if response.B64JSON != nil {
				b64Data := *response.B64JSON
				chunkSize := 128 * 1024
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
			if response.Type == ImageGenerationCompleted {
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

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ImageGenerationStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// simulateImageStreaming makes a non-streaming request and chunks the response
func (provider *OpenAIProvider) simulateImageStreaming(
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

		// Keep chunkSize as const
		chunkSize := 128 * 1024

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

				chunk := &schemas.BifrostImageGenerationStreamResponse{
					ID:            resp.ID,
					Type:          string(ImageGenerationPartial),
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
					chunk.Type = string(ImageGenerationCompleted)
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
