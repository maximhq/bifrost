package cohere

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"net/http"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/valyala/fasthttp"
)

// cohereResponsePool provides a pool for Cohere v2 response objects.
var cohereResponsePool = sync.Pool{
	New: func() interface{} {
		return &CohereChatResponse{}
	},
}


// CohereProvider implements the Provider interface for Cohere.
type CohereProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests	
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewCohereProvider creates a new Cohere provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and connection limits.
func NewCohereProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*CohereProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     10000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		cohereResponsePool.Put(&CohereChatResponse{})
	}

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.cohere.ai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CohereProvider{
		logger:               logger,
		client:               client,		
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Cohere.
func (provider *CohereProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Cohere, provider.customProviderConfig)
}

// ListModels performs a list models request to Cohere's API.
func (provider *CohereProvider) ListModels(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL using centralized URL construction
	requestURL := ToCohereListModelsURL(request, provider.networkConfig.BaseURL+"/v1/models")
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))

		var errorResp CohereError
		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, bifrostErr
	}

	// Parse Cohere list models response
	var cohereResponse CohereListModelsResponse
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &cohereResponse, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert Cohere v2 response to Bifrost response
	response := cohereResponse.ToBifrostListModelsResponse(providerName)

	response.ExtraFields.Provider = providerName
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletion is not supported by the Cohere provider.
// Returns an error indicating that text completion is not supported.
func (provider *CohereProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("text completion", "cohere")
}

// TextCompletionStream performs a streaming text completion request to Cohere's API.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *CohereProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("text completion stream", "cohere")
}

// ChatCompletion performs a chat completion request to the Cohere API using v2 converter.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *CohereProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed
	if err := providerUtils.CheckOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Convert to Cohere v2 request
	reqBody := ToCohereChatCompletionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion input is not provided", nil, providerName)
	}

	cohereResponse, rawResponse, latency, err := provider.handleCohereChatCompletionRequest(ctx, reqBody, key)
	if err != nil {
		return nil, err
	}

	// Convert Cohere v2 response to Bifrost response
	response := cohereResponse.ToBifrostChatResponse()

	response.Model = request.Model
	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

func (provider *CohereProvider) handleCohereChatCompletionRequest(ctx context.Context, reqBody *CohereChatRequest, key schemas.Key) (*CohereChatResponse, interface{}, time.Duration, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Marshal request body
	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, nil, time.Duration(0), &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v2/chat")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))

		var errorResp CohereError
		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, nil, latency, bifrostErr
	}

	// Parse Cohere v2 response
	var cohereResponse CohereChatResponse
	if err := sonic.Unmarshal(resp.Body(), &cohereResponse); err != nil {
		return nil, nil, latency, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error parsing Cohere v2 response",
				Error:   err,
			},
		}
	}

	// Parse raw response for sendBackRawResponse
	var rawResponse interface{}
	if provider.sendBackRawResponse {
		if err := sonic.Unmarshal(resp.Body(), &rawResponse); err != nil {
			return nil, nil, latency, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "error parsing raw response",
					Error:   err,
				},
			}
		}
	}

	return &cohereResponse, rawResponse, latency, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Cohere API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *CohereProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed
	if err := providerUtils.CheckOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	// Convert to Cohere v2 request and add streaming
	reqBody := ToCohereChatCompletionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	url := fmt.Sprintf("%s/v2/chat", provider.networkConfig.BaseURL)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)
	
	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	
	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	req.SetBody(jsonBody)
	
	// Make the request
	err = provider.client.Do(req, resp)
	if err != nil {
		defer fasthttp.ReleaseResponse(resp)
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
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer fasthttp.ReleaseResponse(resp)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode()), fmt.Errorf("%s", string(resp.Body())), resp.StatusCode(), providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	chunkIndex := -1

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer fasthttp.ReleaseResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		var responseID string
		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData := strings.TrimPrefix(line, "data: ")

				// Handle [DONE] marker
				if strings.TrimSpace(jsonData) == "[DONE]" {
					provider.logger.Debug("Received [DONE] marker, ending stream")
					return
				}

				// Parse the unified streaming event
				var event CohereStreamEvent
				if err := sonic.Unmarshal([]byte(jsonData), &event); err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse stream event: %v", err))
					continue
				}

				chunkIndex++

				// Extract response ID from message-start events
				if event.Type == StreamEventMessageStart && event.ID != nil {
					responseID = *event.ID
				}

				// Create base response with current responseID
				response := &schemas.BifrostChatResponse{
					ID:     responseID,
					Object: "chat.completion.chunk",
					Model:  request.Model,
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
								Delta: &schemas.ChatStreamResponseChoiceDelta{},
							},
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()

				switch event.Type {
				case StreamEventMessageStart:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.Role != nil {
						response.Choices[0].ChatStreamResponseChoice.Delta.Role = event.Delta.Message.Role
					}

				case StreamEventContentDelta:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.Content != nil && event.Delta.Message.Content.CohereStreamContentObject != nil && event.Delta.Message.Content.CohereStreamContentObject.Text != nil {
						// Try to cast content to CohereStreamContent
						response.Choices[0].ChatStreamResponseChoice.Delta.Content = event.Delta.Message.Content.CohereStreamContentObject.Text
					}

				case StreamEventToolPlanDelta:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.ToolPlan != nil {
						response.Choices[0].ChatStreamResponseChoice.Delta.Thought = event.Delta.Message.ToolPlan
					}

				case StreamEventContentStart:
					// Content start event - just continue, actual content comes in content-delta

				case StreamEventToolCallStart, StreamEventToolCallDelta:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.ToolCalls != nil && event.Delta.Message.ToolCalls.CohereToolCallObject != nil {
						// Handle single tool call object (tool-call-start/delta events)
						cohereToolCall := event.Delta.Message.ToolCalls.CohereToolCallObject
						toolCall := schemas.ChatAssistantMessageToolCall{}

						if cohereToolCall.ID != nil {
							toolCall.ID = cohereToolCall.ID
						}

						if cohereToolCall.Function != nil {
							if cohereToolCall.Function.Name != nil {
								toolCall.Function.Name = cohereToolCall.Function.Name
							}
							toolCall.Function.Arguments = cohereToolCall.Function.Arguments
						}

						response.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls = []schemas.ChatAssistantMessageToolCall{toolCall}
					}

				case StreamEventMessageEnd:
					if event.Delta != nil {
						// Set finish reason
						if event.Delta.FinishReason != nil {
							finishReason := string(*event.Delta.FinishReason)
							response.Choices[0].FinishReason = &finishReason
						}

						// Set usage information
						if event.Delta.Usage != nil {
							usage := &schemas.BifrostLLMUsage{}
							if event.Delta.Usage.Tokens != nil {
								if event.Delta.Usage.Tokens.InputTokens != nil {
									usage.PromptTokens = int(*event.Delta.Usage.Tokens.InputTokens)
								}
								if event.Delta.Usage.Tokens.OutputTokens != nil {
									usage.CompletionTokens = int(*event.Delta.Usage.Tokens.OutputTokens)
								}
								usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
							}
							response.Usage = usage
						}

						ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					}

				case StreamEventToolCallEnd, StreamEventContentEnd:
					// These events just signal completion, no additional data needed

				default:
					provider.logger.Debug(fmt.Sprintf("Unknown v2 stream event type: %s", event.Type))
					continue
				}

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan)

				// End stream after message-end
				if event.Type == StreamEventMessageEnd {
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Responses performs a responses request to the Cohere API using v2 converter.
func (provider *CohereProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed
	if err := providerUtils.CheckOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Convert to Cohere v2 request
	reqBody := ToCohereResponsesRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("responses input is not provided", nil, providerName)
	}

	cohereResponse, rawResponse, latency, err := provider.handleCohereChatCompletionRequest(ctx, reqBody, key)
	if err != nil {
		return nil, err
	}

	// Convert Cohere v2 response to Bifrost response
	response := cohereResponse.ToResponsesBifrostResponsesResponse()

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ResponsesStream performs a streaming responses request to the Cohere API.
func (provider *CohereProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if responses stream is allowed
	if err := providerUtils.CheckOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	// Convert to Cohere v2 request and add streaming
	reqBody := ToCohereResponsesRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("responses input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	url := fmt.Sprintf("%s/v2/chat", provider.networkConfig.BaseURL)
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)
	
	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)
		
	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	req.SetBody(jsonBody)


	// Make the request
	err = provider.client.Do(req, resp)
	if err != nil {
		defer fasthttp.ReleaseResponse(resp)
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
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer fasthttp.ReleaseResponse(resp)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode()), fmt.Errorf("%s", string(resp.Body())), resp.StatusCode(), providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer fasthttp.ReleaseResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track SSE event parsing state
		var eventData string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE event - track event data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				eventData = after
			} else {
				continue
			}

			// Skip if we don't have event data
			if eventData == "" {
				continue
			}

			// Handle [DONE] marker
			if strings.TrimSpace(eventData) == "[DONE]" {
				provider.logger.Debug("Received [DONE] marker, ending stream")
				return
			}

			// Parse the unified streaming event
			var event CohereStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream event: %v", err))
				continue
			}

			if chunkIndex == 0 {
				providerUtils.SendCreatedEventResponsesChunk(ctx, postHookRunner, providerName, request.Model, startTime, responseChan)
				providerUtils.SendInProgressEventResponsesChunk(ctx, postHookRunner, providerName, request.Model, startTime, responseChan)
				chunkIndex = 2
			}

			response, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(chunkIndex)
			if response != nil {
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex,
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				}

				lastChunkTime = time.Now()
				chunkIndex++

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				if isLastChunk {
					if response.Response == nil {
						response.Response = &schemas.BifrostResponsesResponse{}
					}
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					providerUtils.HandleStreamEndWithSuccess(ctx, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), postHookRunner, responseChan)
					break
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
			}
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
				}

				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				break
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerName, err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s) using the Cohere API.
// Supports Cohere's embedding models and returns a BifrostResponse containing the embedding(s).
func (provider *CohereProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Check if embedding is allowed
	if err := providerUtils.CheckOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create Bifrost request for conversion
	reqBody := ToCohereEmbeddingRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("embedding input is not provided", nil, providerName)
	}

	// Marshal request body
	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v2/embed")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))

		var errorResp CohereError
		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, bifrostErr
	}

	// Parse response
	var cohereResp CohereEmbeddingResponse
	if err := sonic.Unmarshal(resp.Body(), &cohereResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError("error parsing embedding response", err, providerName)
	}

	// Parse raw response for consistent format
	var rawResponse interface{}
	if err := sonic.Unmarshal(resp.Body(), &rawResponse); err != nil {
		return nil, providerUtils.NewBifrostOperationError("error parsing raw response for embedding", err, providerName)
	}

	// Create BifrostResponse
	response := cohereResp.ToBifrostEmbeddingResponse()

	response.Model = request.Model
	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.EmbeddingRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Only include RawResponse if sendBackRawResponse is enabled
	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Speech is not supported by the Cohere provider.
func (provider *CohereProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("speech", "cohere")
}

// SpeechStream is not supported by the Cohere provider.
func (provider *CohereProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("speech stream", "cohere")
}

// Transcription is not supported by the Cohere provider.
func (provider *CohereProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("transcription", "cohere")
}

// TranscriptionStream is not supported by the Cohere provider.
func (provider *CohereProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("transcription stream", "cohere")
}
