// Package providers implements various LLM providers and their utility functions.
// This file contains the Anthropic provider implementation.
package providers

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// AnthropicToolChoice represents the tool choice configuration for Anthropic's API.
// It specifies how tools should be used in the completion request.
type AnthropicToolChoice struct {
	Type                   schemas.ToolChoiceType `json:"type"`                      // Type of tool choice
	Name                   *string                `json:"name"`                      // Name of the tool to use
	DisableParallelToolUse *bool                  `json:"disable_parallel_tool_use"` // Whether to disable parallel tool use
}

// AnthropicTextResponse represents the response structure from Anthropic's text completion API.
// It includes the completion text, model information, and token usage statistics.
type AnthropicTextResponse struct {
	ID         string `json:"id"`         // Unique identifier for the completion
	Type       string `json:"type"`       // Type of completion
	Completion string `json:"completion"` // Generated completion text
	Model      string `json:"model"`      // Model used for the completion
	Usage      struct {
		InputTokens  int `json:"input_tokens"`  // Number of input tokens used
		OutputTokens int `json:"output_tokens"` // Number of output tokens generated
	} `json:"usage"` // Token usage statistics
}

// AnthropicChatResponse represents the response structure from Anthropic's chat completion API.
// It includes message content, model information, and token usage statistics.
type AnthropicChatResponse struct {
	ID      string `json:"id"`   // Unique identifier for the completion
	Type    string `json:"type"` // Type of completion
	Role    string `json:"role"` // Role of the message sender
	Content []struct {
		Type     string                 `json:"type"`               // Type of content
		Text     string                 `json:"text,omitempty"`     // Text content
		Thinking string                 `json:"thinking,omitempty"` // Thinking process
		ID       string                 `json:"id"`                 // Content identifier
		Name     string                 `json:"name"`               // Name of the content
		Input    map[string]interface{} `json:"input"`              // Input parameters
	} `json:"content"` // Array of content items
	Model        string  `json:"model"`                   // Model used for the completion
	StopReason   string  `json:"stop_reason,omitempty"`   // Reason for completion termination
	StopSequence *string `json:"stop_sequence,omitempty"` // Sequence that caused completion to stop
	Usage        struct {
		InputTokens  int `json:"input_tokens"`  // Number of input tokens used
		OutputTokens int `json:"output_tokens"` // Number of output tokens generated
	} `json:"usage"` // Token usage statistics
}

// AnthropicStreamEvent represents a single event in the Anthropic streaming response.
// It corresponds to the various event types defined in Anthropic's Messages API streaming documentation.
type AnthropicStreamEvent struct {
	Type         string                  `json:"type"`
	Message      *AnthropicStreamMessage `json:"message,omitempty"`
	Index        *int                    `json:"index,omitempty"`
	ContentBlock *AnthropicContentBlock  `json:"content_block,omitempty"`
	Delta        *AnthropicDelta         `json:"delta,omitempty"`
	Usage        *schemas.LLMUsage       `json:"usage,omitempty"`
	Error        *AnthropicStreamError   `json:"error,omitempty"`
}

// AnthropicStreamMessage represents the message structure in streaming events.
// This appears in message_start events and contains the initial message structure.
type AnthropicStreamMessage struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        *schemas.LLMUsage       `json:"usage"`
}

// AnthropicContentBlock represents a content block in Anthropic responses.
// This includes text, tool_use, thinking, and web_search_tool_result blocks.
type AnthropicContentBlock struct {
	Type     string                 `json:"type"`
	Text     string                 `json:"text,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Input    map[string]interface{} `json:"input,omitempty"`
	Thinking string                 `json:"thinking,omitempty"`
	// Web search tool result specific fields
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   []AnthropicToolContent `json:"content,omitempty"`
}

// AnthropicToolContent represents content within tool result blocks
type AnthropicToolContent struct {
	Type             string  `json:"type"`
	Title            string  `json:"title,omitempty"`
	URL              string  `json:"url,omitempty"`
	EncryptedContent string  `json:"encrypted_content,omitempty"`
	PageAge          *string `json:"page_age,omitempty"`
}

// AnthropicDelta represents incremental updates to content blocks during streaming.
// This includes all delta types: text_delta, input_json_delta, thinking_delta, and signature_delta.
type AnthropicDelta struct {
	Type         string  `json:"type"`
	Text         string  `json:"text,omitempty"`
	PartialJSON  string  `json:"partial_json,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	Signature    string  `json:"signature,omitempty"`
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// AnthropicStreamError represents error events in the streaming response.
type AnthropicStreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// AnthropicError represents the error response structure from Anthropic's API.
// It includes error type and message information.
type AnthropicError struct {
	Type  string `json:"type"` // always "error"
	Error struct {
		Type    string `json:"type"`    // Error type
		Message string `json:"message"` // Error message
	} `json:"error"` // Error details
}

type AnthropicImageContent struct {
	Type      ImageContentType `json:"type"`
	URL       string           `json:"url"`
	MediaType string           `json:"media_type,omitempty"`
}

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	logger        schemas.Logger        // Logger for provider operations
	client        *fasthttp.Client      // HTTP client for API requests
	streamClient  *http.Client          // HTTP client for streaming requests
	apiVersion    string                // API version for the provider
	networkConfig schemas.NetworkConfig // Network configuration including extra headers
}

// anthropicChatResponsePool provides a pool for Anthropic chat response objects.
var anthropicChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicChatResponse{}
	},
}

// anthropicTextResponsePool provides a pool for Anthropic text response objects.
var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicTextResponse{}
	},
}

// acquireAnthropicChatResponse gets an Anthropic chat response from the pool and resets it.
func acquireAnthropicChatResponse() *AnthropicChatResponse {
	resp := anthropicChatResponsePool.Get().(*AnthropicChatResponse)
	*resp = AnthropicChatResponse{} // Reset the struct
	return resp
}

// releaseAnthropicChatResponse returns an Anthropic chat response to the pool.
func releaseAnthropicChatResponse(resp *AnthropicChatResponse) {
	if resp != nil {
		anthropicChatResponsePool.Put(resp)
	}
}

// acquireAnthropicTextResponse gets an Anthropic text response from the pool and resets it.
func acquireAnthropicTextResponse() *AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*AnthropicTextResponse)
	*resp = AnthropicTextResponse{} // Reset the struct
	return resp
}

// releaseAnthropicTextResponse returns an Anthropic text response to the pool.
func releaseAnthropicTextResponse(resp *AnthropicTextResponse) {
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
		anthropicTextResponsePool.Put(&AnthropicTextResponse{})
		anthropicChatResponsePool.Put(&AnthropicChatResponse{})

	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.anthropic.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AnthropicProvider{
		logger:        logger,
		client:        client,
		streamClient:  streamClient,
		apiVersion:    "2023-06-01",
		networkConfig: config.NetworkConfig,
	}
}

// GetProviderKey returns the provider identifier for Anthropic.
func (provider *AnthropicProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Anthropic
}

// prepareTextCompletionParams prepares text completion parameters for Anthropic's API.
// It handles parameter mapping and conversion to the format expected by Anthropic.
// Returns the modified parameters map.
func (provider *AnthropicProvider) prepareTextCompletionParams(params map[string]interface{}) map[string]interface{} {
	// Check if there is a key entry for max_tokens
	if maxTokens, exists := params["max_tokens"]; exists {
		// Check if max_tokens_to_sample is already present
		if _, exists := params["max_tokens_to_sample"]; !exists {
			// If max_tokens_to_sample is not present, rename max_tokens to max_tokens_to_sample
			params["max_tokens_to_sample"] = maxTokens
		}
		delete(params, "max_tokens")
	}
	return params
}

// completeRequest sends a request to Anthropic's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AnthropicProvider) completeRequest(ctx context.Context, requestBody map[string]interface{}, url string, key string) ([]byte, *schemas.BifrostError) {
	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
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
		provider.logger.Debug(fmt.Sprintf("error from anthropic provider: %s", string(resp.Body())))

		var errorResp AnthropicError

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
func (provider *AnthropicProvider) TextCompletion(ctx context.Context, model, key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	preparedParams := provider.prepareTextCompletionParams(prepareParams(params))

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":  model,
		"prompt": fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", text),
	}, preparedParams)

	responseBody, err := provider.completeRequest(ctx, requestBody, provider.networkConfig.BaseURL+"/v1/complete", key)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAnthropicTextResponse()
	defer releaseAnthropicTextResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
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
			Provider:    schemas.Anthropic,
			RawResponse: rawResponse,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletion(ctx context.Context, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareAnthropicChatRequest(messages, params)

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	responseBody, err := provider.completeRequest(ctx, requestBody, provider.networkConfig.BaseURL+"/v1/messages", key)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAnthropicChatResponse()
	defer releaseAnthropicChatResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
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
		Provider:    schemas.Anthropic,
		RawResponse: rawResponse,
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// buildAnthropicImageSourceMap creates the "source" map for an Anthropic image content part.
func buildAnthropicImageSourceMap(imgContent *schemas.ImageURLStruct) map[string]interface{} {
	if imgContent == nil {
		return nil
	}

	sanitizedURL, _ := SanitizeImageURL(imgContent.URL)
	urlTypeInfo := ExtractURLTypeInfo(sanitizedURL)

	formattedImgContent := AnthropicImageContent{
		Type: urlTypeInfo.Type,
	}

	if urlTypeInfo.MediaType != nil {
		formattedImgContent.MediaType = *urlTypeInfo.MediaType
	}

	if urlTypeInfo.DataURLWithoutPrefix != nil {
		formattedImgContent.URL = *urlTypeInfo.DataURLWithoutPrefix
	} else {
		formattedImgContent.URL = sanitizedURL
	}

	sourceMap := map[string]interface{}{
		"type": string(formattedImgContent.Type), // "base64" or "url"
	}

	if formattedImgContent.Type == ImageContentTypeURL {
		sourceMap["url"] = formattedImgContent.URL
	} else {
		if formattedImgContent.MediaType != "" {
			sourceMap["media_type"] = formattedImgContent.MediaType
		}
		sourceMap["data"] = formattedImgContent.URL // URL field contains base64 data string
	}
	return sourceMap
}

func prepareAnthropicChatRequest(messages []schemas.BifrostMessage, params *schemas.ModelParameters) ([]map[string]interface{}, map[string]interface{}) {
	// Add system messages if present
	var systemMessages []BedrockAnthropicSystemMessage
	for _, msg := range messages {
		if msg.Role == schemas.ModelChatMessageRoleSystem {
			if msg.Content.ContentStr != nil {
				systemMessages = append(systemMessages, BedrockAnthropicSystemMessage{
					Text: *msg.Content.ContentStr,
				})
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range *msg.Content.ContentBlocks {
					if block.Text != nil {
						systemMessages = append(systemMessages, BedrockAnthropicSystemMessage{
							Text: *block.Text,
						})
					}
				}
			}
		}
	}

	// Format messages for Anthropic API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		var content []interface{}

		if msg.Role != schemas.ModelChatMessageRoleSystem {
			if msg.Role == schemas.ModelChatMessageRoleTool && msg.ToolMessage != nil && msg.ToolMessage.ToolCallID != nil {
				toolCallResult := map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": *msg.ToolMessage.ToolCallID,
				}

				var toolCallResultContent []map[string]interface{}

				if msg.Content.ContentStr != nil {
					toolCallResultContent = append(toolCallResultContent, map[string]interface{}{
						"type": "text",
						"text": *msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					for _, block := range *msg.Content.ContentBlocks {
						if block.Text != nil {
							toolCallResultContent = append(toolCallResultContent, map[string]interface{}{
								"type": "text",
								"text": *block.Text,
							})
						}
					}
				}

				toolCallResult["content"] = toolCallResultContent
				content = append(content, toolCallResult)
			} else {
				// Add text content if present
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": *msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					for _, block := range *msg.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							content = append(content, map[string]interface{}{
								"type": "text",
								"text": *block.Text,
							})
						}
						if block.ImageURL != nil {
							imageSource := buildAnthropicImageSourceMap(block.ImageURL)
							if imageSource != nil {
								content = append(content, map[string]interface{}{
									"type":   "image",
									"source": imageSource,
								})
							}
						}
					}
				}

				// Add thinking content if present in AssistantMessage
				if msg.AssistantMessage != nil && msg.AssistantMessage.Thought != nil {
					content = append(content, map[string]interface{}{
						"type":     "thinking",
						"thinking": *msg.AssistantMessage.Thought,
					})
				}

				// Add tool calls as content if present
				if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
					for _, toolCall := range *msg.AssistantMessage.ToolCalls {
						if toolCall.Function.Name != nil {
							var input map[string]interface{}
							if toolCall.Function.Arguments != "" {
								if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
									// If unmarshaling fails, use a simple string representation
									input = map[string]interface{}{"arguments": toolCall.Function.Arguments}
								}
							}

							toolUseContent := map[string]interface{}{
								"type":  "tool_use",
								"name":  *toolCall.Function.Name,
								"input": input,
							}

							if toolCall.ID != nil {
								toolUseContent["id"] = *toolCall.ID
							}

							content = append(content, toolUseContent)
						}
					}
				}
			}

			if len(content) > 0 {
				formattedMessages = append(formattedMessages, map[string]interface{}{
					"role":    msg.Role,
					"content": content,
				})
			}
		}
	}

	preparedParams := prepareParams(params)

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		var tools []map[string]interface{}
		for _, tool := range *params.Tools {
			tools = append(tools, map[string]interface{}{
				"name":         tool.Function.Name,
				"description":  tool.Function.Description,
				"input_schema": tool.Function.Parameters,
			})
		}

		preparedParams["tools"] = tools
	}

	// Transform tool choice if present
	if params != nil && params.ToolChoice != nil {
		if params.ToolChoice.ToolChoiceStr != nil {
			preparedParams["tool_choice"] = map[string]interface{}{
				"type": *params.ToolChoice.ToolChoiceStr,
			}
		} else if params.ToolChoice.ToolChoiceStruct != nil {
			switch toolChoice := params.ToolChoice.ToolChoiceStruct.Type; toolChoice {
			case schemas.ToolChoiceTypeFunction:
				fallthrough
			case "tool":
				preparedParams["tool_choice"] = map[string]interface{}{
					"type": "tool",
					"name": params.ToolChoice.ToolChoiceStruct.Function.Name,
				}
			default:
				preparedParams["tool_choice"] = map[string]interface{}{
					"type": toolChoice,
				}
			}
		}
	}

	if len(systemMessages) > 0 {
		var messages []string
		for _, message := range systemMessages {
			messages = append(messages, message.Text)
		}

		preparedParams["system"] = strings.Join(messages, " ")
	}

	// Post-process formattedMessages for tool call results
	processedFormattedMessages := []map[string]interface{}{} // Use a new slice
	i := 0
	for i < len(formattedMessages) {
		currentMsg := formattedMessages[i]
		currentRole, roleOk := getRoleFromMessage(currentMsg)

		if !roleOk || currentRole == "" {
			// If role is of an unexpected type, missing, or empty, treat as non-tool message
			processedFormattedMessages = append(processedFormattedMessages, currentMsg)
			i++
			continue
		}

		if currentRole == schemas.ModelChatMessageRoleTool {
			// Content of a tool message is the toolCallResult map
			// Initialize accumulatedToolResults with the content of the current tool message.
			var accumulatedToolResults []interface{}

			// Safely extract content from current message
			if content, ok := currentMsg["content"].([]interface{}); ok {
				accumulatedToolResults = content
			} else {
				// If content is not the expected type, skip this message
				processedFormattedMessages = append(processedFormattedMessages, currentMsg)
				i++
				continue
			}

			// Look ahead for more sequential tool messages
			j := i + 1
			for j < len(formattedMessages) {
				nextMsg := formattedMessages[j]
				nextRole, nextRoleOk := getRoleFromMessage(nextMsg)

				if !nextRoleOk || nextRole == "" || nextRole != schemas.ModelChatMessageRoleTool {
					break // Not a sequential tool message or role is invalid/missing/empty
				}

				// Safely extract content from next message
				if nextContent, ok := nextMsg["content"].([]interface{}); ok {
					accumulatedToolResults = append(accumulatedToolResults, nextContent...)
				}
				j++
			}

			// Create a new message with role User and accumulated content
			mergedMsg := map[string]interface{}{
				"role":    schemas.ModelChatMessageRoleUser, // Final role is User
				"content": accumulatedToolResults,
			}
			processedFormattedMessages = append(processedFormattedMessages, mergedMsg)
			i = j // Advance main loop index past all merged messages
		} else {
			// Not a tool message, add it as is
			processedFormattedMessages = append(processedFormattedMessages, currentMsg)
			i++
		}
	}
	formattedMessages = processedFormattedMessages // Update with processed messages

	return formattedMessages, preparedParams
}

func parseAnthropicResponse(response *AnthropicChatResponse, bifrostResponse *schemas.BifrostResponse) (*schemas.BifrostResponse, *schemas.BifrostError) {
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

			args, err := json.Marshal(c.Input)
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
func (provider *AnthropicProvider) Embedding(ctx context.Context, model, key string, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "anthropic")
}

// ChatCompletionStream performs a streaming chat completion request to the Anthropic API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareAnthropicChatRequest(messages, params)

	// Merge additional parameters and set stream to true
	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         key,
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
	requestBody map[string]interface{},
	headers map[string]string,
	extraHeaders map[string]string,
	providerType schemas.ModelProvider,
	params *schemas.ModelParameters,
	postHookRunner schemas.PostHookRunner,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "failed to create HTTP request",
				Error:   err,
			},
		}
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
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &resp.StatusCode,
			Error: schemas.ErrorField{
				Message: fmt.Sprintf("HTTP error from %s: %d", providerType, resp.StatusCode),
			},
		}
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
				var event AnthropicStreamEvent
				if err := json.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse message_start event: %v", err))
					continue
				}
				if event.Message != nil {
					messageID = event.Message.ID
					modelName = event.Message.Model
				}

			case "content_block_start":
				var event AnthropicStreamEvent
				if err := json.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse content_block_start event: %v", err))
					continue
				}

				if event.Index != nil && event.ContentBlock != nil {
					// Handle different content block types
					switch event.ContentBlock.Type {
					case "tool_use":
						// Tool use content block initialization
						if event.ContentBlock.Name != "" && event.ContentBlock.ID != "" {
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
														ID:   &event.ContentBlock.ID,
														Function: schemas.FunctionCall{
															Name: &event.ContentBlock.Name,
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
							ProcessAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
						}
					default:
						thought := ""
						if event.ContentBlock.Thinking != "" {
							thought = event.ContentBlock.Thinking
						}
						content := ""
						if event.ContentBlock.Text != "" {
							content = event.ContentBlock.Text
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
						ProcessAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
					}
				}

			case "content_block_delta":
				var event AnthropicStreamEvent
				if err := json.Unmarshal([]byte(eventData), &event); err != nil {
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
							ProcessAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
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
							ProcessAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
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
							ProcessAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
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
				var event AnthropicStreamEvent
				if err := json.Unmarshal([]byte(eventData), &event); err != nil {
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
					ProcessAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
				}

			case "message_stop":
				var event AnthropicStreamEvent
				if err := json.Unmarshal([]byte(eventData), &event); err != nil {
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
				ProcessAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
				return

			case "ping":
				// Ping events are just keepalive, no action needed
				continue

			case "error":
				var event AnthropicStreamEvent
				if err := json.Unmarshal([]byte(eventData), &event); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse error event: %v", err))
					continue
				}
				if event.Error != nil {

					// Send error through channel before closing
					errorResponse := &schemas.BifrostStream{
						BifrostError: &schemas.BifrostError{
							IsBifrostError: false,
							Error: schemas.ErrorField{
								Type:    &event.Error.Type,
								Message: event.Error.Message,
							},
						},
					}

					select {
					case responseChan <- errorResponse:
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

			// Send scanner error through channel
			errorResponse := &schemas.BifrostStream{
				BifrostError: &schemas.BifrostError{
					IsBifrostError: true,
					Error: schemas.ErrorField{
						Message: "Error reading stream",
						Error:   err,
					},
				},
			}

			select {
			case responseChan <- errorResponse:
			case <-ctx.Done():
			}
		}
	}()

	return responseChan, nil
}
