// Package providers implements various LLM providers and their utility functions.
// This file contains the Anthropic provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/anthropic"
	"github.com/valyala/fasthttp"
)

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	streamClient         *http.Client                  // HTTP client for streaming requests
	apiVersion           string                        // API version for the provider
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// anthropicChatResponsePool provides a pool for Anthropic chat response objects.
var anthropicChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &anthropic.AnthropicChatResponse{}
	},
}

// anthropicTextResponsePool provides a pool for Anthropic text response objects.
var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &anthropic.AnthropicTextResponse{}
	},
}

// acquireAnthropicChatResponse gets an Anthropic chat response from the pool and resets it.
func acquireAnthropicChatResponse() *anthropic.AnthropicChatResponse {
	resp := anthropicChatResponsePool.Get().(*anthropic.AnthropicChatResponse)
	*resp = anthropic.AnthropicChatResponse{} // Reset the struct
	return resp
}

// releaseAnthropicChatResponse returns an Anthropic chat response to the pool.
func releaseAnthropicChatResponse(resp *anthropic.AnthropicChatResponse) {
	if resp != nil {
		anthropicChatResponsePool.Put(resp)
	}
}

// acquireAnthropicTextResponse gets an Anthropic text response from the pool and resets it.
func acquireAnthropicTextResponse() *anthropic.AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*anthropic.AnthropicTextResponse)
	*resp = anthropic.AnthropicTextResponse{} // Reset the struct
	return resp
}

// releaseAnthropicTextResponse returns an Anthropic text response to the pool.
func releaseAnthropicTextResponse(resp *anthropic.AnthropicTextResponse) {
	if resp != nil {
		anthropicTextResponsePool.Put(resp)
	}
}

// Since Anthropic always needs to have a max_tokens parameter, we set a default value if not provided.
const (
	AnthropicDefaultMaxTokens = 4096
)

// mapAnthropicFinishReasonToOpenAI maps Anthropic finish reasons to OpenAI-compatible ones
func MapAnthropicFinishReason(anthropicReason string) string {
	switch anthropicReason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		// Pass through Anthropic-specific reasons like "pause_turn", "refusal", etc.
		return anthropicReason
	}
}

// NewAnthropicProvider creates a new Anthropic provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAnthropicProvider(config *schemas.ProviderConfig, logger schemas.Logger) *AnthropicProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.Concurrency,
	}

	// Initialize streaming HTTP client
	streamClient := &http.Client{
		Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
	}

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		anthropicTextResponsePool.Put(&anthropic.AnthropicTextResponse{})
		anthropicChatResponsePool.Put(&anthropic.AnthropicChatResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.anthropic.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AnthropicProvider{
		logger:               logger,
		client:               client,
		streamClient:         streamClient,
		apiVersion:           "2023-06-01",
		networkConfig:        config.NetworkConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for Anthropic.
func (provider *AnthropicProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.Anthropic, provider.customProviderConfig)
}

// completeRequest sends a request to Anthropic's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AnthropicProvider) completeRequest(ctx context.Context, requestBody interface{}, url string, key string) ([]byte, *schemas.BifrostError) {
	// Marshal the request body
	jsonData, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, provider.GetProviderKey())
	}

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", provider.apiVersion)

	req.SetBody(jsonData)

	// Send the request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body())))

		var errorResp anthropic.AnthropicError

		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message

		return nil, bifrostErr
	}

	// Read the response body
	body := resp.Body()

	return body, nil
}

// TextCompletion performs a text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.OperationTextCompletion); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	anthropicReq := anthropic.ConvertTextRequestToAnthropic(model, text, params)
	if anthropicReq == nil {
		return nil, newBifrostOperationError("text completion input is not provided", nil, provider.GetProviderKey())
	}

	// Use struct directly for JSON marshaling
	responseBody, err := provider.completeRequest(ctx, anthropicReq, provider.networkConfig.BaseURL+"/v1/complete", key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAnthropicTextResponse()
	defer releaseAnthropicTextResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		ID: response.ID,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleAssistant,
						Content: schemas.MessageContent{
							ContentStr: &response.Completion,
						},
					},
				},
			},
		},
		Usage: &schemas.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
		Model: response.Model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: provider.GetProviderKey(),
		},
	}

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.OperationChatCompletion); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	anthropicReq := anthropic.ConvertChatRequestToAnthropic(&schemas.BifrostRequest{
		Model: model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &messages,
		},
		Params: params,
	})
	if anthropicReq == nil {
		return nil, newBifrostOperationError("failed to convert request", fmt.Errorf("conversion returned nil"), provider.GetProviderKey())
	}

	// Use struct directly for JSON marshaling
	responseBody, err := provider.completeRequest(ctx, anthropicReq, provider.networkConfig.BaseURL+"/v1/messages", key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAnthropicChatResponse()
	defer releaseAnthropicChatResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{}
	bifrostResponse, err = parseAnthropicResponse(response, bifrostResponse)
	if err != nil {
		return nil, err
	}

	bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider: provider.GetProviderKey(),
	}

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

func parseAnthropicResponse(response *anthropic.AnthropicChatResponse, bifrostResponse *schemas.BifrostResponse) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Collect all content and tool calls into a single message
	var toolCalls []schemas.ToolCall
	var thinking string

	var contentBlocks []schemas.ContentBlock
	// Process content and tool calls
	for _, c := range response.Content {
		switch c.Type {
		case "thinking":
			thinking = c.Thinking
		case "text":
			contentBlocks = append(contentBlocks, schemas.ContentBlock{
				Type: "text",
				Text: &c.Text,
			})
		case "tool_use":
			function := schemas.FunctionCall{
				Name: &c.Name,
			}

			args, err := sonic.Marshal(c.Input)
			if err != nil {
				function.Arguments = fmt.Sprintf("%v", c.Input)
			} else {
				function.Arguments = string(args)
			}

			toolCalls = append(toolCalls, schemas.ToolCall{
				Type:     Ptr("function"),
				ID:       &c.ID,
				Function: function,
			})
		}
	}

	// Create the assistant message
	var assistantMessage *schemas.AssistantMessage

	// Create AssistantMessage if we have tool calls or thinking
	if len(toolCalls) > 0 || thinking != "" {
		assistantMessage = &schemas.AssistantMessage{}
		if len(toolCalls) > 0 {
			assistantMessage.ToolCalls = &toolCalls
		}
		if thinking != "" {
			assistantMessage.Thought = &thinking
		}
	}

	// Create a single choice with the collected content
	bifrostResponse.ID = response.ID
	bifrostResponse.Choices = []schemas.BifrostResponseChoice{
		{
			Index: 0,
			BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
				Message: schemas.BifrostMessage{
					Role: schemas.ModelChatMessageRoleAssistant,
					Content: schemas.MessageContent{
						ContentBlocks: &contentBlocks,
					},
					AssistantMessage: assistantMessage,
				},
				StopString: response.StopSequence,
			},
			FinishReason: func() *string {
				if response.StopReason != "" {
					mapped := MapAnthropicFinishReason(response.StopReason)
					return &mapped
				}
				return nil
			}(),
		},
	}
	bifrostResponse.Usage = &schemas.LLMUsage{
		PromptTokens:     response.Usage.InputTokens,
		CompletionTokens: response.Usage.OutputTokens,
		TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
	}
	bifrostResponse.Model = response.Model

	return bifrostResponse, nil
}

// Embedding is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "anthropic")
}

// ChatCompletionStream performs a streaming chat completion request to the Anthropic API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.OperationChatCompletionStream); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	anthropicReq := anthropic.ConvertChatRequestToAnthropic(&schemas.BifrostRequest{
		Model: model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &messages,
		},
		Params: params,
	})
	if anthropicReq == nil {
		return nil, newBifrostOperationError("failed to convert request", fmt.Errorf("conversion returned nil"), provider.GetProviderKey())
	}

	// Enable streaming
	anthropicReq.Stream = schemas.Ptr(true)

	// Use struct directly for JSON marshaling

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         key.Value,
		"anthropic-version": provider.apiVersion,
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
	}

	// Use shared Anthropic streaming logic
	return handleAnthropicStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/messages",
		anthropicReq,
		headers,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		params,
		postHookRunner,
		provider.logger,
	)
}

// handleAnthropicStreaming handles streaming for Anthropic-compatible APIs (Anthropic, Vertex Claude models).
// This shared function reduces code duplication between providers that use the same SSE event format.
func handleAnthropicStreaming(
	ctx context.Context,
	httpClient *http.Client,
	url string,
	requestBody interface{},
	headers map[string]string,
	extraHeaders map[string]string,
	providerType schemas.ModelProvider,
	params *schemas.ModelParameters,
	postHookRunner schemas.PostHookRunner,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerType)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerType)
	}

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, extraHeaders, nil)

	// Make the request
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerType)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerType, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerType, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := -1

		// Track minimal state needed for response format
		var messageID string
		var modelName string
		var usage *schemas.LLMUsage
		var finishReason *string

		// Track SSE event parsing state
		var eventType string
		var eventData string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE event - track event type and data separately
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
				continue
			} else if strings.HasPrefix(line, "data: ") {
				eventData = strings.TrimPrefix(line, "data: ")
			} else {
				continue
			}

			// Skip if we don't have both event type and data
			if eventType == "" || eventData == "" {
				continue
			}

			var event anthropic.AnthropicStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse message_start event: %v", err))
				continue
			}

			if event.Usage != nil {
				usage = &schemas.LLMUsage{
					PromptTokens:     event.Usage.InputTokens,
					CompletionTokens: event.Usage.OutputTokens,
					TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}
			if event.Delta != nil && event.Delta.StopReason != nil {
				mappedReason := MapAnthropicFinishReason(*event.Delta.StopReason)
				finishReason = &mappedReason
			}

			// Handle different event types
			switch eventType {
			case "message_start":
				if event.Message != nil {
					messageID = event.Message.ID
					modelName = event.Message.Model

					// Send first chunk with role
					if event.Message.Role != "" {
						chunkIndex++
						role := event.Message.Role

						// Create streaming response for message start with role
						streamResponse := &schemas.BifrostResponse{
							ID:     messageID,
							Object: "chat.completion.chunk",
							Model:  modelName,
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
										Delta: schemas.BifrostStreamDelta{
											Role: &role,
										},
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								Provider:   providerType,
								ChunkIndex: chunkIndex,
							},
						}

						// Use utility function to process and send response
						processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, logger)
					}
				}

			case "content_block_start":
				if event.Index != nil && event.ContentBlock != nil {
					chunkIndex++

					// Handle different content block types
					switch event.ContentBlock.Type {
					case "tool_use":
						// Tool use content block initialization
						if event.ContentBlock.Name != nil && *event.ContentBlock.Name != "" &&
							event.ContentBlock.ID != nil && *event.ContentBlock.ID != "" {
							// Create streaming response for tool start
							streamResponse := &schemas.BifrostResponse{
								ID:     messageID,
								Object: "chat.completion.chunk",
								Model:  modelName,
								Choices: []schemas.BifrostResponseChoice{
									{
										Index: *event.Index,
										BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
											Delta: schemas.BifrostStreamDelta{
												ToolCalls: []schemas.ToolCall{
													{
														Type: func() *string { s := "function"; return &s }(),
														ID:   event.ContentBlock.ID,
														Function: schemas.FunctionCall{
															Name: event.ContentBlock.Name,
														},
													},
												},
											},
										},
									},
								},
								ExtraFields: schemas.BifrostResponseExtraFields{
									Provider:   providerType,
									ChunkIndex: chunkIndex,
								},
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, logger)
						}
					default:
						thought := ""
						if event.ContentBlock.Thinking != nil && *event.ContentBlock.Thinking != "" {
							thought = *event.ContentBlock.Thinking
						}
						content := ""
						if event.ContentBlock.Text != nil && *event.ContentBlock.Text != "" {
							content = *event.ContentBlock.Text
						}

						// Send empty message for other content block types
						streamResponse := &schemas.BifrostResponse{
							ID:     messageID,
							Object: "chat.completion.chunk",
							Model:  modelName,
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: *event.Index,
									BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
										Delta: schemas.BifrostStreamDelta{
											Thought: &thought,
											Content: &content,
										},
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								Provider:   providerType,
								ChunkIndex: chunkIndex,
							},
						}

						// Use utility function to process and send response
						processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, logger)
					}
				}

			case "content_block_delta":
				if event.Index != nil && event.Delta != nil {
					chunkIndex++

					// Handle different delta types
					switch event.Delta.Type {
					case "text_delta":
						if event.Delta.Text != "" {
							// Create streaming response for this delta
							streamResponse := &schemas.BifrostResponse{
								ID:     messageID,
								Object: "chat.completion.chunk",
								Model:  modelName,
								Choices: []schemas.BifrostResponseChoice{
									{
										Index: *event.Index,
										BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
											Delta: schemas.BifrostStreamDelta{
												Content: &event.Delta.Text,
											},
										},
									},
								},
								ExtraFields: schemas.BifrostResponseExtraFields{
									Provider:   providerType,
									ChunkIndex: chunkIndex,
								},
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, logger)
						}

					case "input_json_delta":
						// Handle tool use streaming - accumulate partial JSON
						if event.Delta.PartialJSON != "" {
							// Create streaming response for tool input delta
							streamResponse := &schemas.BifrostResponse{
								ID:     messageID,
								Object: "chat.completion.chunk",
								Model:  modelName,
								Choices: []schemas.BifrostResponseChoice{
									{
										Index: *event.Index,
										BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
											Delta: schemas.BifrostStreamDelta{
												ToolCalls: []schemas.ToolCall{
													{
														Type: func() *string { s := "function"; return &s }(),
														Function: schemas.FunctionCall{
															Arguments: event.Delta.PartialJSON,
														},
													},
												},
											},
										},
									},
								},
								ExtraFields: schemas.BifrostResponseExtraFields{
									Provider:   providerType,
									ChunkIndex: chunkIndex,
								},
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, logger)
						}

					case "thinking_delta":
						// Handle thinking content streaming
						if event.Delta.Thinking != "" {
							// Create streaming response for thinking delta
							streamResponse := &schemas.BifrostResponse{
								ID:     messageID,
								Object: "chat.completion.chunk",
								Model:  modelName,
								Choices: []schemas.BifrostResponseChoice{
									{
										Index: *event.Index,
										BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
											Delta: schemas.BifrostStreamDelta{
												Thought: &event.Delta.Thinking,
											},
										},
									},
								},
								ExtraFields: schemas.BifrostResponseExtraFields{
									Provider:   providerType,
									ChunkIndex: chunkIndex,
								},
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, logger)
						}

					case "signature_delta":
						// Handle signature verification for thinking content
						// This is used to verify the integrity of thinking content

					}
				}

			case "content_block_stop":
				// Content block is complete, no specific action needed for streaming
				continue

			case "message_delta":
				continue

			case "message_stop":
				continue

			case "ping":
				// Ping events are just keepalive, no action needed
				continue

			case "error":
				if event.Error != nil {
					// Send error through channel before closing
					bifrostErr := &schemas.BifrostError{
						IsBifrostError: false,
						Error: schemas.ErrorField{
							Type:    &event.Error.Type,
							Message: event.Error.Message,
						},
					}

					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				}
				return

			default:
				// Unknown event type - handle gracefully as per Anthropic's versioning policy
				// New event types may be added, so we should not error but log and continue
				logger.Debug(fmt.Sprintf("Unknown %s stream event type: %s, data: %s", providerType, eventType, eventData))
				continue
			}

			// Reset for next event
			eventType = ""
			eventData = ""
		}

		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerType, err))
			processAndSendError(ctx, postHookRunner, err, responseChan, logger)
		} else {
			response := createBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, params, providerType)
			handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, logger)
		}
	}()

	return responseChan, nil
}

func (provider *AnthropicProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "anthropic")
}

func (provider *AnthropicProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "anthropic")
}

func (provider *AnthropicProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "anthropic")
}

func (provider *AnthropicProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "anthropic")
}
