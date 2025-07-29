// Package providers implements various LLM providers and their utility functions.
// This file contains the Anthropic provider implementation.
package providers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/api"
	"github.com/valyala/fasthttp"
)

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	apiVersion          string                // API version for the provider
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// anthropicChatResponsePool provides a pool for Anthropic chat response objects.
var anthropicChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &api.AnthropicChatResponse{}
	},
}

// anthropicTextResponsePool provides a pool for Anthropic text response objects.
var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &api.AnthropicTextResponse{}
	},
}

// acquireAnthropicChatResponse gets an Anthropic chat response from the pool and resets it.
func acquireAnthropicChatResponse() *api.AnthropicChatResponse {
	resp := anthropicChatResponsePool.Get().(*api.AnthropicChatResponse)
	*resp = api.AnthropicChatResponse{} // Reset the struct
	return resp
}

// releaseAnthropicChatResponse returns an Anthropic chat response to the pool.
func releaseAnthropicChatResponse(resp *api.AnthropicChatResponse) {
	if resp != nil {
		anthropicChatResponsePool.Put(resp)
	}
}

// acquireAnthropicTextResponse gets an Anthropic text response from the pool and resets it.
func acquireAnthropicTextResponse() *api.AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*api.AnthropicTextResponse)
	*resp = api.AnthropicTextResponse{} // Reset the struct
	return resp
}

// releaseAnthropicTextResponse returns an Anthropic text response to the pool.
func releaseAnthropicTextResponse(resp *api.AnthropicTextResponse) {
	if resp != nil {
		anthropicTextResponsePool.Put(resp)
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
	for range config.ConcurrencyAndBufferSize.Concurrency {
		anthropicTextResponsePool.Put(&api.AnthropicTextResponse{})
		anthropicChatResponsePool.Put(&api.AnthropicChatResponse{})

	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.anthropic.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AnthropicProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		apiVersion:          "2023-06-01",
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Anthropic.
func (provider *AnthropicProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Anthropic
}

// buildAnthropicTextRequest creates a type-safe Anthropic text completion request
// from Bifrost text input and parameters.
func buildAnthropicTextRequest(model string, text string, params *schemas.ModelParameters) *api.AnthropicTextRequest {
	// Format the prompt with Anthropic's expected format
	prompt := fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", text)

	// Build the request
	request := &api.AnthropicTextRequest{
		Model: model,
		Prompt: prompt,
	}

	// Add parameters if provided
	if params != nil {
		request.MaxTokensToSample = params.MaxTokens
		request.Temperature = params.Temperature
		request.TopP = params.TopP
		request.TopK = params.TopK
		if params.StopSequences != nil {
			request.StopSequences = *params.StopSequences
		}

		if params.ExtraParams != nil {
			request.ExtraParams = params.ExtraParams
		}
	}

	return request
}

// buildAnthropicChatRequest creates a type-safe Anthropic chat completion request
// from Bifrost messages and parameters.
func buildAnthropicChatRequest(model string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) *api.AnthropicMessageRequest {
	// Convert Bifrost messages to Anthropic format
	var anthropicMessages []api.AnthropicMessage
	var systemContent *api.AnthropicContent

	for _, msg := range messages {
		if msg.Role == schemas.ModelChatMessageRoleSystem {
			// Handle system messages separately
			if msg.Content.ContentStr != nil {
				systemContent = &api.AnthropicContent{
					ContentStr: msg.Content.ContentStr,
				}
			} else if msg.Content.ContentBlocks != nil {
				// Convert content blocks to Anthropic format
				contentBlocks := make([]api.AnthropicContentBlock, 0, len(*msg.Content.ContentBlocks))
				for _, block := range *msg.Content.ContentBlocks {
					if block.Text != nil {
						contentBlocks = append(contentBlocks, api.AnthropicContentBlock{
							Type: "text",
							Text: block.Text,
						})
					}
				}
				if len(contentBlocks) > 0 {
					systemContent = &api.AnthropicContent{
						ContentBlocks: &contentBlocks,
					}
				}
			}
		} else {
			// Convert regular messages
			anthropicMsg := api.AnthropicMessage{
				Role: string(msg.Role),
			}

			if msg.Content.ContentStr != nil {
				anthropicMsg.Content = api.AnthropicContent{
					ContentStr: msg.Content.ContentStr,
				}
			} else if msg.Content.ContentBlocks != nil {
				// Convert content blocks to Anthropic format
				contentBlocks := make([]api.AnthropicContentBlock, 0, len(*msg.Content.ContentBlocks))
				for _, block := range *msg.Content.ContentBlocks {
					if block.Text != nil {
						contentBlocks = append(contentBlocks, api.AnthropicContentBlock{
							Type: "text",
							Text: block.Text,
						})
					}
					if block.ImageURL != nil {
						// Handle image content
						imageSource := buildAnthropicImageSourceMap(block.ImageURL)
						contentBlocks = append(contentBlocks, api.AnthropicContentBlock{
							Type:   "image",
							Source: imageSource,
						})
					}
				}
				if len(contentBlocks) > 0 {
					anthropicMsg.Content = api.AnthropicContent{
						ContentBlocks: &contentBlocks,
					}
				}
			}

			anthropicMessages = append(anthropicMessages, anthropicMsg)
		}
	}

	// Build the request
	request := &api.AnthropicMessageRequest{
		Model:    model,
		Messages: anthropicMessages,
	}

	// Add system content if present
	if systemContent != nil {
		request.System = systemContent
	}

	// Add parameters if provided
	if params != nil {
		request.MaxTokens = 0 // Default value, will be set below if provided
		if params.MaxTokens != nil {
			request.MaxTokens = *params.MaxTokens
		}
		request.Temperature = params.Temperature
		request.TopP = params.TopP
		request.TopK = params.TopK
		if params.StopSequences != nil {
			request.StopSequences = params.StopSequences
		}

		// Handle tools if present
		if params.Tools != nil {
			tools := make([]api.AnthropicTool, 0, len(*params.Tools))
			for _, tool := range *params.Tools {
				anthropicTool := api.AnthropicTool{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
				}

				// Convert function parameters to input schema
				if tool.Function.Parameters.Type != "" {
					anthropicTool.InputSchema = &struct {
						Type       string                 `json:"type"`
						Properties map[string]interface{} `json:"properties"`
						Required   []string               `json:"required"`
					}{
						Type:       tool.Function.Parameters.Type,
						Properties: tool.Function.Parameters.Properties,
						Required:   tool.Function.Parameters.Required,
					}
				}

				tools = append(tools, anthropicTool)
			}
			request.Tools = &tools
		}

		// Handle tool choice if present
		if params.ToolChoice != nil {
			if params.ToolChoice.ToolChoiceStr != nil {
				request.ToolChoice = &api.AnthropicToolChoice{
					Type: schemas.ToolChoiceType(*params.ToolChoice.ToolChoiceStr),
				}
			} else if params.ToolChoice.ToolChoiceStruct != nil {
				toolChoice := &api.AnthropicToolChoice{
					Type: params.ToolChoice.ToolChoiceStruct.Type,
				}
				if params.ToolChoice.ToolChoiceStruct.Function.Name != "" {
					toolChoice.Name = &params.ToolChoice.ToolChoiceStruct.Function.Name
				}
				request.ToolChoice = toolChoice
			}
		}

		// Handle extra parameters by mapping them to specific fields
		if params.ExtraParams != nil {
			// Create a copy of ExtraParams to avoid modifying the original
			remainingExtraParams := make(map[string]interface{})
			maps.Copy(remainingExtraParams, params.ExtraParams)

			// Track fields to be deleted
			fieldsToDelete := []string{}

			// Extract known fields
			if stream, ok := remainingExtraParams["stream"].(bool); ok {
				request.Stream = &stream
				fieldsToDelete = append(fieldsToDelete, "stream")
			}
			if anthropicVersion, ok := remainingExtraParams["anthropic_version"].(string); ok {
				request.AnthropicVersion = &anthropicVersion
				fieldsToDelete = append(fieldsToDelete, "anthropic_version")
			}
			if region, ok := remainingExtraParams["region"].(string); ok {
				request.Region = &region
				fieldsToDelete = append(fieldsToDelete, "region")
			}

			// Delete all extracted fields at once
			deleteFields(remainingExtraParams, fieldsToDelete)

			// Add any remaining extra params to the request's ExtraParams field
			if len(remainingExtraParams) > 0 {
				request.ExtraParams = remainingExtraParams
			}
		}
	}

	return request
}

// completeRequest sends a request to Anthropic's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AnthropicProvider) completeRequest(ctx context.Context, requestBody api.AnthropicRequestConfig, key string) ([]byte, *schemas.BifrostError) {

	var jsonData []byte
	var err error
	if requestBody.AnthropicTextRequest != nil {
		jsonData, err = sonic.Marshal(requestBody.AnthropicTextRequest)
	} else if requestBody.AnthropicMessageRequest != nil {
		jsonData, err = sonic.Marshal(requestBody.AnthropicMessageRequest)
	}

	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Anthropic)
	}

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(requestBody.URL)
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
		provider.logger.Debug(fmt.Sprintf("error from anthropic provider: %s", string(resp.Body())))

		var errorResp api.AnthropicError

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
	request := buildAnthropicTextRequest(model, text, params)

	requestBody := api.AnthropicRequestConfig{
		URL: provider.networkConfig.BaseURL + "/v1/complete",
		AnthropicTextRequest: request,
	}

	responseBody, err := provider.completeRequest(ctx, requestBody, key.Value)
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
			Provider: schemas.Anthropic,
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
	// Build type-safe request
	request := buildAnthropicChatRequest(model, messages, params)

	requestBody := api.AnthropicRequestConfig{
		URL:                     provider.networkConfig.BaseURL + "/v1/messages",
		AnthropicMessageRequest: request,
	}

	responseBody, err := provider.completeRequest(ctx, requestBody, key.Value)
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
		Provider: schemas.Anthropic,
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

// buildAnthropicImageSourceMap creates the "source" map for an Anthropic image content part.
func buildAnthropicImageSourceMap(imgContent *schemas.ImageURLStruct) *api.AnthropicImageSource {
	if imgContent == nil {
		return nil
	}

	sanitizedURL, _ := SanitizeImageURL(imgContent.URL)
	urlTypeInfo := ExtractURLTypeInfo(sanitizedURL)

	imageSource := &api.AnthropicImageSource{
		Type: string(urlTypeInfo.Type),
	}

	if urlTypeInfo.MediaType != nil {
		imageSource.MediaType = urlTypeInfo.MediaType
	}

	if urlTypeInfo.DataURLWithoutPrefix != nil {
		imageSource.Data = urlTypeInfo.DataURLWithoutPrefix
	} else {
		imageSource.URL = &sanitizedURL
	}

	return imageSource
}

func parseAnthropicResponse(response *api.AnthropicChatResponse, bifrostResponse *schemas.BifrostResponse) (*schemas.BifrostResponse, *schemas.BifrostError) {
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
				Type:     StrPtr("function"),
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
			FinishReason: &response.StopReason,
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
	// Build type-safe request
	requestBody := buildAnthropicChatRequest(model, messages, params)

	// Set streaming flag
	stream := true
	requestBody.Stream = &stream

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
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.Anthropic,
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
	requestBody *api.AnthropicMessageRequest,
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
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, newBifrostOperationError("failed to create HTTP request", err, providerType)
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

		// Track minimal state needed for response format
		var messageID string
		var modelName string

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

			// Handle different event types
			switch eventType {
			case "message_start":
				var event api.AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse message_start event: %v", err))
					continue
				}
				if event.Message != nil {
					messageID = event.Message.ID
					modelName = event.Message.Model
				}

			case "content_block_start":
				var event api.AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse content_block_start event: %v", err))
					continue
				}

				if event.Index != nil && event.ContentBlock != nil {
					// Handle different content block types
					switch event.ContentBlock.Type {
					case "tool_use":
						// Tool use content block initialization
						if event.ContentBlock.Name != nil && event.ContentBlock.ID != nil {
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
									Provider: providerType,
								},
							}

							if params != nil {
								streamResponse.ExtraFields.Params = *params
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
						}
					default:
						thought := ""
						if event.ContentBlock.Text != nil {
							thought = *event.ContentBlock.Text
						}
						content := ""
						if event.ContentBlock.Text != nil {
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
								Provider: providerType,
							},
						}

						if params != nil {
							streamResponse.ExtraFields.Params = *params
						}

						// Use utility function to process and send response
						processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
					}
				}

			case "content_block_delta":
				var event api.AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse content_block_delta event: %v", err))
					continue
				}

				if event.Index != nil && event.Delta != nil {
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
									Provider: providerType,
								},
							}

							if params != nil {
								streamResponse.ExtraFields.Params = *params
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
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
									Provider: providerType,
								},
							}

							if params != nil {
								streamResponse.ExtraFields.Params = *params
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
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
									Provider: providerType,
								},
							}

							if params != nil {
								streamResponse.ExtraFields.Params = *params
							}

							// Use utility function to process and send response
							processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
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
				var event api.AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse message_delta event: %v", err))
					continue
				}

				// Handle delta changes to the top-level message

				// Send usage information immediately if present
				if event.Usage != nil {
					streamResponse := &schemas.BifrostResponse{
						ID:     messageID,
						Object: "chat.completion.chunk",
						Model:  modelName,
						Usage:  event.Usage,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{}, // Empty delta for usage update
								},
								FinishReason: event.Delta.StopReason,
							},
						},
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider: providerType,
						},
					}

					if params != nil {
						streamResponse.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
				}

			case "message_stop":
				var event api.AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse message_stop event: %v", err))
					continue
				}

				stopReason := ""
				if event.Delta != nil {
					stopReason = *event.Delta.StopReason
				}

				// Send final message with stop reason
				streamResponse := &schemas.BifrostResponse{
					ID:     messageID,
					Object: "chat.completion.chunk",
					Model:  modelName,
					Choices: []schemas.BifrostResponseChoice{
						{
							Index:        0,
							FinishReason: &stopReason,
							BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
								Delta: schemas.BifrostStreamDelta{}, // Empty delta for final message
							},
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						Provider: providerType,
					},
				}

				if params != nil {
					streamResponse.ExtraFields.Params = *params
				}

				// Use utility function to process and send response
				processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
				return

			case "ping":
				// Ping events are just keepalive, no action needed
				continue

			case "error":
				var event api.AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse error event: %v", err))
					continue
				}
				if event.Error != nil {
					// Send error through channel before closing
					bifrostError := &schemas.BifrostError{
						IsBifrostError: false,
						Error: schemas.ErrorField{
							Type:    &event.Error.Type,
							Message: event.Error.Message,
						},
					}

					processedResponse, processedError := postHookRunner(&ctx, nil, bifrostError)
					bifrostError = processedError

					select {
					case responseChan <- &schemas.BifrostStream{
						BifrostResponse: processedResponse,
						BifrostError:    bifrostError,
					}:
					case <-ctx.Done():
					}
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
			processAndSendError(ctx, postHookRunner, err, responseChan)
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
