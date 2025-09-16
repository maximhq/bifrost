// Package providers implements various LLM providers and their utility functions.
// This file contains the AWS Bedrock provider implementation.
package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bufio"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// BedrockAnthropicTextResponse represents the response structure from Bedrock's Anthropic text completion API.
// It includes the completion text and stop reason information.
type BedrockAnthropicTextResponse struct {
	Completion string `json:"completion"`  // Generated completion text
	StopReason string `json:"stop_reason"` // Reason for completion termination
	Stop       string `json:"stop"`        // Stop sequence that caused completion to stop
}

// BedrockMistralTextResponse represents the response structure from Bedrock's Mistral text completion API.
// It includes multiple output choices with their text and stop reasons.
type BedrockMistralTextResponse struct {
	Outputs []struct {
		Text       string `json:"text"`        // Generated text
		StopReason string `json:"stop_reason"` // Reason for completion termination
	} `json:"outputs"` // Array of output choices
}

// BedrockChatResponse represents the response structure from Bedrock's chat completion API.
// It includes message content, metrics, and token usage statistics.
type BedrockChatResponse struct {
	Metrics struct {
		Latency int `json:"latencyMs"` // Response latency in milliseconds
	} `json:"metrics"` // Performance metrics
	Output struct {
		Message struct {
			Content []struct {
				Text *string `json:"text"` // Message content
				// Bedrock returns a union type where either Text or ToolUse is present (mutually exclusive)
				BedrockAnthropicToolUseMessage
			} `json:"content"` // Array of message content
			Role string `json:"role"` // Role of the message sender
		} `json:"message"` // Message structure
	} `json:"output"` // Output structure
	StopReason string `json:"stopReason"` // Reason for completion termination
	Usage      struct {
		InputTokens  int `json:"inputTokens"`  // Number of input tokens used
		OutputTokens int `json:"outputTokens"` // Number of output tokens generated
		TotalTokens  int `json:"totalTokens"`  // Total number of tokens used
	} `json:"usage"` // Token usage statistics
}

// BedrockAnthropicSystemMessage represents a system message for Anthropic models.
type BedrockAnthropicSystemMessage struct {
	Text string `json:"text"` // System message text
}

// BedrockAnthropicTextMessage represents a text message for Anthropic models.
type BedrockAnthropicTextMessage struct {
	Type string `json:"type"` // Type of message
	Text string `json:"text"` // Message text
}

// BedrockMistralContent represents content for Mistral models.
type BedrockMistralContent struct {
	Text string `json:"text"` // Content text
}

// BedrockMistralChatMessage represents a chat message for Mistral models.
type BedrockMistralChatMessage struct {
	Role       schemas.ModelChatMessageRole `json:"role"`                   // Role of the message sender
	Content    []BedrockMistralContent      `json:"content"`                // Array of message content
	ToolCalls  *[]BedrockAnthropicToolCall  `json:"tool_calls,omitempty"`   // Optional tool calls
	ToolCallID *string                      `json:"tool_call_id,omitempty"` // Optional tool call ID
}

// BedrockAnthropicImageMessage represents an image message for Anthropic models.
type BedrockAnthropicImageMessage struct {
	Type  string                `json:"type"`  // Type of message
	Image BedrockAnthropicImage `json:"image"` // Image data
}

// BedrockAnthropicImage represents image data for Anthropic models.
type BedrockAnthropicImage struct {
	Format string                      `json:"format,omitempty"` // Image format
	Source BedrockAnthropicImageSource `json:"source,omitempty"` // Image source
}

// BedrockAnthropicImageSource represents the source of an image for Anthropic models.
type BedrockAnthropicImageSource struct {
	Bytes string `json:"bytes"` // Base64 encoded image data
}

// BedrockAnthropicToolUseMessage represents a tool use message for Anthropic models.
type BedrockAnthropicToolUseMessage struct {
	ToolUse *BedrockAnthropicToolUse `json:"toolUse"`
}

// BedrockToolChoice represents the tool choice configuration for Bedrock models.
type BedrockToolChoice struct {
	Auto map[string]interface{} `json:"auto,omitempty"`
	Any  map[string]interface{} `json:"any,omitempty"`
	Tool *BedrockSpecificTool   `json:"tool,omitempty"`
}

// BedrockSpecificTool represents a specific tool choice configuration.
type BedrockSpecificTool struct {
	Type string `json:"type"` // "tool" always
	Name string `json:"name"`
}

type BedrockAnthropicToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

// BedrockAnthropicToolCall represents a tool call for Anthropic models.
type BedrockAnthropicToolCall struct {
	ToolSpec BedrockAnthropicToolSpec `json:"toolSpec"` // Tool specification
}

// BedrockAnthropicToolSpec represents a tool specification for Anthropic models.
type BedrockAnthropicToolSpec struct {
	Name        string `json:"name"`        // Tool name
	Description string `json:"description"` // Tool description
	InputSchema struct {
		Json interface{} `json:"json"` // Input schema in JSON format
	} `json:"inputSchema"` // Input schema structure
}

// BedrockError represents the error response structure from Bedrock's API.
type BedrockError struct {
	Message string `json:"message"` // Error message
}

// BedrockStreamMessageStartEvent is emitted when the assistant message starts.
type BedrockStreamMessageStartEvent struct {
	MessageStart struct {
		Role string `json:"role"` // e.g. "assistant"
	} `json:"messageStart"`
}

// BedrockStreamContentBlockDeltaEvent is sent for each content delta chunk (text, reasoning, tool use).
type BedrockStreamContentBlockDeltaEvent struct {
	ContentBlockDelta struct {
		Delta struct {
			Text             string          `json:"text,omitempty"`
			ReasoningContent json.RawMessage `json:"reasoningContent,omitempty"`
			ToolUse          json.RawMessage `json:"toolUse,omitempty"`
		} `json:"delta"`
		ContentBlockIndex int `json:"contentBlockIndex"`
	} `json:"contentBlockDelta"`
}

// BedrockStreamContentBlockStopEvent indicates the end of a content block.
type BedrockStreamContentBlockStopEvent struct {
	ContentBlockStop struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
	} `json:"contentBlockStop"`
}

// BedrockStreamMessageStopEvent marks the end of the assistant message.
type BedrockStreamMessageStopEvent struct {
	MessageStop struct {
		StopReason string `json:"stopReason"` // e.g. "stop", "max_tokens", "tool_use"
	} `json:"messageStop"`
}

// BedrockStreamMetadataEvent contains metadata after streaming ends.
type BedrockStreamMetadataEvent struct {
	Metadata struct {
		Usage struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
			TotalTokens  int `json:"totalTokens"`
		} `json:"usage"`
		Metrics struct {
			LatencyMs float64 `json:"latencyMs"`
		} `json:"metrics"`
	} `json:"metadata"`
}

// BedrockProvider implements the Provider interface for AWS Bedrock.
type BedrockProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *http.Client                  // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
}

// bedrockChatResponsePool provides a pool for Bedrock response objects.
var bedrockChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &BedrockChatResponse{}
	},
}

// acquireBedrockChatResponse gets a Bedrock response from the pool and resets it.
func acquireBedrockChatResponse() *BedrockChatResponse {
	resp := bedrockChatResponsePool.Get().(*BedrockChatResponse)
	*resp = BedrockChatResponse{} // Reset the struct
	return resp
}

// releaseBedrockChatResponse returns a Bedrock response to the pool.
func releaseBedrockChatResponse(resp *BedrockChatResponse) {
	if resp != nil {
		bedrockChatResponsePool.Put(resp)
	}
}

// NewBedrockProvider creates a new Bedrock provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and AWS-specific settings.
func NewBedrockProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*BedrockProvider, error) {
	config.CheckAndSetDefaults()

	client := &http.Client{Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		bedrockChatResponsePool.Put(&BedrockChatResponse{})
	}

	return &BedrockProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Bedrock.
func (provider *BedrockProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.Bedrock, provider.customProviderConfig)
}

// CompleteRequest sends a request to Bedrock's API and handles the response.
// It constructs the API URL, sets up AWS authentication, and processes the response.
// Returns the response body, request latency, or an error if the request fails.
func (provider *BedrockProvider) completeRequest(ctx context.Context, requestBody map[string]interface{}, path string, config schemas.BedrockKeyConfig) ([]byte, time.Duration, *schemas.BifrostError) {
	region := "us-east-1"
	if config.Region != nil {
		region = *config.Region
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, &schemas.BifrostError{
				IsBifrostError: false,
				Error: schemas.ErrorField{
					Type:    Ptr(schemas.RequestCancelled),
					Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
					Error:   err,
				},
			}
		}
		return nil, 0, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	// Create the request with the JSON body
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, 0, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Sign the request using either explicit credentials or IAM role authentication
	if err := signAWSRequest(ctx, req, config.AccessKey, config.SecretKey, config.SessionToken, region, "bedrock", provider.GetProviderKey()); err != nil {
		return nil, 0, err
	}

	// Execute the request and measure latency
	startTime := time.Now()
	resp, err := provider.client.Do(req)
	latency := time.Since(startTime)
	if err != nil {
		return nil, latency, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latency, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp BedrockError

		if err := sonic.Unmarshal(body, &errorResp); err != nil {
			return nil, latency, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &resp.StatusCode,
				Error: schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
			}
		}

		return nil, latency, &schemas.BifrostError{
			StatusCode: &resp.StatusCode,
			Error: schemas.ErrorField{
				Message: errorResp.Message,
			},
		}
	}

	return body, latency, nil
}

// GetTextCompletionResult processes the text completion response from Bedrock.
// It handles different model types (Anthropic and Mistral) and formats the response.
// Returns a BifrostResponse containing the completion results or an error if processing fails.
func (provider *BedrockProvider) getTextCompletionResult(result []byte, model string, providerName schemas.ModelProvider) (*schemas.BifrostResponse, *schemas.BifrostError) {
	switch {
	case strings.Contains(model, "anthropic."):
		var response BedrockAnthropicTextResponse
		if err := sonic.Unmarshal(result, &response); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: schemas.ErrorField{
					Message: "error parsing response",
					Error:   err,
				},
			}
		}

		return &schemas.BifrostResponse{
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
						StopString: &response.Stop,
					},
					FinishReason: &response.StopReason,
				},
			},
			Model: model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider: providerName,
			},
		}, nil

	case strings.Contains(model, "mistral."):
		var response BedrockMistralTextResponse
		if err := sonic.Unmarshal(result, &response); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: schemas.ErrorField{
					Message: "error parsing response",
					Error:   err,
				},
			}
		}

		var choices []schemas.BifrostResponseChoice
		for i, output := range response.Outputs {
			choices = append(choices, schemas.BifrostResponseChoice{
				Index: i,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleAssistant,
						Content: schemas.MessageContent{
							ContentStr: &output.Text,
						},
					},
				},
				FinishReason: &output.StopReason,
			})
		}

		return &schemas.BifrostResponse{
			Choices: choices,
			Model:   model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider: providerName,
			},
		}, nil
	}

	return nil, newConfigurationError(fmt.Sprintf("invalid model choice: %s", model), providerName)
}

// parseBedrockAnthropicMessageToolCallContent parses the content of a tool call message.
// It handles both text and JSON content.
// Returns a map containing the parsed content.
func parseBedrockAnthropicMessageToolCallContent(content string) map[string]interface{} {
	toolResultContentBlock := map[string]interface{}{}
	var parsedJSON interface{}
	err := sonic.Unmarshal([]byte(content), &parsedJSON)
	if err == nil {
		if arr, ok := parsedJSON.([]interface{}); ok {
			toolResultContentBlock["json"] = map[string]interface{}{"content": arr}
		} else {
			toolResultContentBlock["json"] = map[string]interface{}{"output": parsedJSON}
		}
	} else {
		toolResultContentBlock["text"] = content
	}

	return toolResultContentBlock
}

// PrepareChatCompletionMessages formats chat messages for Bedrock's API.
// It handles different model types (Anthropic and Mistral) and formats messages accordingly.
// Returns a map containing the formatted messages and any system messages, or an error if formatting fails.
func (provider *BedrockProvider) prepareChatCompletionMessages(messages []schemas.BifrostMessage, model string) (map[string]interface{}, *schemas.BifrostError) {
	switch {
	case strings.Contains(model, "anthropic."):
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

		// Format messages for Bedrock API
		var bedrockMessages []map[string]interface{}
		for _, msg := range messages {
			var content []interface{}
			if msg.Role != schemas.ModelChatMessageRoleSystem {
				if msg.Role == schemas.ModelChatMessageRoleTool && msg.ToolCallID != nil {
					toolCallResult := map[string]interface{}{
						"toolUseId": *msg.ToolCallID,
					}
					var toolResultContentBlocks []map[string]interface{}
					if msg.Content.ContentStr != nil {
						toolResultContentBlocks = append(toolResultContentBlocks, parseBedrockAnthropicMessageToolCallContent(*msg.Content.ContentStr))
					} else if msg.Content.ContentBlocks != nil {
						for _, block := range *msg.Content.ContentBlocks {
							if block.Text != nil {
								toolResultContentBlocks = append(toolResultContentBlocks, parseBedrockAnthropicMessageToolCallContent(*block.Text))
							}
						}
					}
					toolCallResult["content"] = toolResultContentBlocks
					content = append(content, map[string]interface{}{
						"toolResult": toolCallResult,
					})
				} else {
					// Bedrock wants only toolUse block on content, text blocks are not allowed when tools are called.
					if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
						for _, toolCall := range *msg.AssistantMessage.ToolCalls {
							var input map[string]interface{}
							if toolCall.Function.Arguments != "" {
								if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
									input = map[string]interface{}{"arguments": toolCall.Function.Arguments}
								}
							}

							content = append(content, BedrockAnthropicToolUseMessage{
								ToolUse: &BedrockAnthropicToolUse{
									ToolUseID: *toolCall.ID,
									Name:      *toolCall.Function.Name,
									Input:     input,
								},
							})
						}
					} else {
						if msg.Content.ContentStr != nil {
							content = append(content, BedrockAnthropicTextMessage{
								Type: "text",
								Text: *msg.Content.ContentStr,
							})
						} else if msg.Content.ContentBlocks != nil {
							for _, block := range *msg.Content.ContentBlocks {
								if block.Text != nil {
									content = append(content, BedrockAnthropicTextMessage{
										Type: "text",
										Text: *block.Text,
									})
								}
								if block.ImageURL != nil {
									sanitizedURL, _ := SanitizeImageURL(block.ImageURL.URL)
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

									content = append(content, BedrockAnthropicImageMessage{
										Type: "image",
										Image: BedrockAnthropicImage{
											Format: func() string {
												if formattedImgContent.MediaType != "" {
													mediaType := formattedImgContent.MediaType
													// Remove "image/" prefix if present, since normalizeMediaType ensures full format
													mediaType = strings.TrimPrefix(mediaType, "image/")
													return mediaType
												}
												return ""
											}(),
											Source: BedrockAnthropicImageSource{
												Bytes: formattedImgContent.URL,
											},
										},
									})
								}
							}
						}
					}

				}

				if len(content) > 0 {
					bedrockMessages = append(bedrockMessages, map[string]interface{}{
						"role":    msg.Role,
						"content": content,
					})
				}
			}
		}

		// Post-process bedrockMessages for tool call results
		processedBedrockMessages := []map[string]interface{}{}
		i := 0
		for i < len(bedrockMessages) {
			currentMsg := bedrockMessages[i]
			currentRole, roleOk := getRoleFromMessage(currentMsg)

			if !roleOk {
				// If role is of an unexpected type or missing, treat as non-tool message
				processedBedrockMessages = append(processedBedrockMessages, currentMsg)
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
					processedBedrockMessages = append(processedBedrockMessages, currentMsg)
					i++
					continue
				}

				// Look ahead for more sequential tool messages
				j := i + 1
				for j < len(bedrockMessages) {
					nextMsg := bedrockMessages[j]
					nextRole, nextRoleOk := getRoleFromMessage(nextMsg)

					if !nextRoleOk || nextRole != schemas.ModelChatMessageRoleTool {
						break // Not a sequential tool message or role is invalid/missing
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
				processedBedrockMessages = append(processedBedrockMessages, mergedMsg)
				i = j // Advance main loop index past all merged messages
			} else {
				// Not a tool message, add it as is
				processedBedrockMessages = append(processedBedrockMessages, currentMsg)
				i++
			}
		}
		bedrockMessages = processedBedrockMessages // Update with processed messages

		body := map[string]interface{}{
			"messages": bedrockMessages,
		}

		if len(systemMessages) > 0 {
			var messages []string
			for _, message := range systemMessages {
				messages = append(messages, message.Text)
			}

			body["system"] = strings.Join(messages, " \n")
		}

		return body, nil

	case strings.Contains(model, "mistral."):
		var bedrockMessages []BedrockMistralChatMessage
		for _, msg := range messages {
			// Check if this is a tool message before changing the role
			isToolMessage := msg.Role == schemas.ModelChatMessageRoleTool

			// Convert tool messages to user messages (Mistral doesn't support tool role)
			role := msg.Role
			switch role {
			case schemas.ModelChatMessageRoleTool, schemas.ModelChatMessageRoleSystem:
				role = schemas.ModelChatMessageRoleUser
			}

			// Only process user and assistant messages
			if role != schemas.ModelChatMessageRoleUser && role != schemas.ModelChatMessageRoleAssistant {
				continue
			}

			var filteredToolCalls []BedrockAnthropicToolCall
			if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
				for _, toolCall := range *msg.AssistantMessage.ToolCalls {
					if toolCall.ID != nil && toolCall.Function.Name != nil {
						// Parse the arguments to get parameters
						var params interface{}
						if toolCall.Function.Arguments != "" {
							if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &params); err != nil {
								// If parsing fails, use empty object
								params = map[string]interface{}{}
							}
						}

						filteredToolCalls = append(filteredToolCalls, BedrockAnthropicToolCall{
							ToolSpec: BedrockAnthropicToolSpec{
								Name:        *toolCall.Function.Name,
								Description: "Tool function", // Default description since FunctionCall doesn't have one
								InputSchema: struct {
									Json interface{} `json:"json"`
								}{
									Json: params,
								},
							},
						})
					}
				}
			}

			message := BedrockMistralChatMessage{
				Role: role,
			}

			// Ensure message has valid content
			var hasValidContent bool
			switch {
			case msg.Content.ContentStr != nil && *msg.Content.ContentStr != "":
				message.Content = []BedrockMistralContent{{Text: *msg.Content.ContentStr}}
				hasValidContent = true
			case msg.Content.ContentBlocks != nil && len(*msg.Content.ContentBlocks) > 0:
				for _, b := range *msg.Content.ContentBlocks {
					if b.Text != nil && *b.Text != "" {
						message.Content = append(message.Content, BedrockMistralContent{Text: *b.Text})
						hasValidContent = true
					}
				}
			}

			// For tool messages that were converted to user messages, ensure they have content
			if isToolMessage && !hasValidContent {
				// If tool message has no content, create a default content
				defaultText := "Tool result received"
				if msg.ToolCallID != nil {
					defaultText = fmt.Sprintf("Tool result for call ID: %s", *msg.ToolCallID)
				}
				message.Content = []BedrockMistralContent{{Text: defaultText}}
				hasValidContent = true
			}

			// Final safety check: ensure message always has content
			if !hasValidContent {
				message.Content = []BedrockMistralContent{{Text: "Message content"}}
				hasValidContent = true
			}

			// Only add messages that have valid content or tool calls
			if hasValidContent || len(filteredToolCalls) > 0 {
				if len(filteredToolCalls) > 0 {
					message.ToolCalls = &filteredToolCalls
				}
				bedrockMessages = append(bedrockMessages, message)
			}
		}

		body := map[string]interface{}{
			"messages": bedrockMessages,
		}

		return body, nil
	}

	return nil, newConfigurationError(fmt.Sprintf("invalid model choice: %s", model), provider.GetProviderKey())
}

// GetChatCompletionTools prepares tool specifications for Bedrock's API.
// It formats tool definitions for different model types (Anthropic and Mistral).
// Returns tool specifications appropriate for the given model type.
func (provider *BedrockProvider) getChatCompletionTools(params *schemas.ModelParameters, model string) (interface{}, *schemas.BifrostError) {
	switch {
	case strings.Contains(model, "anthropic."), strings.Contains(model, "mistral."):
		// Both Anthropic and Mistral models on Bedrock use toolConfig.tools with toolSpec structure
		var tools []BedrockAnthropicToolCall
		for _, tool := range *params.Tools {
			tools = append(tools, BedrockAnthropicToolCall{
				ToolSpec: BedrockAnthropicToolSpec{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					InputSchema: struct {
						Json interface{} `json:"json"`
					}{
						Json: tool.Function.Parameters,
					},
				},
			})
		}
		return tools, nil

	default:
		return nil, newConfigurationError(fmt.Sprintf("unsupported model for tool calling: %s", model), provider.GetProviderKey())
	}
}

// prepareTextCompletionParams prepares text completion parameters for Bedrock's API.
// It handles parameter mapping and conversion for different model types.
// Returns the modified parameters map with model-specific adjustments.
func (provider *BedrockProvider) prepareTextCompletionParams(params map[string]interface{}, model string) map[string]interface{} {
	switch {
	case strings.Contains(model, "anthropic."):
		maxTokens, maxTokensExists := params["max_tokens"]
		if _, exists := params["max_tokens_to_sample"]; !exists {
			// If max_tokens_to_sample is not present, rename max_tokens to max_tokens_to_sample
			if maxTokensExists {
				params["max_tokens_to_sample"] = maxTokens
			} else {
				params["max_tokens_to_sample"] = AnthropicDefaultMaxTokens
			}
		}

		delete(params, "max_tokens")
	}
	return params
}

// TextCompletion performs a text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationTextCompletion); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	preparedParams := provider.prepareTextCompletionParams(prepareParams(params), model)

	requestBody := mergeConfig(map[string]interface{}{
		"prompt": text,
	}, preparedParams)

	body, latency, err := provider.completeRequest(ctx, requestBody, fmt.Sprintf("%s/invoke", model), *key.BedrockKeyConfig)
	if err != nil {
		return nil, err
	}

	bifrostResponse, err := provider.getTextCompletionResult(body, model, providerName)
	if err != nil {
		return nil, err
	}

	// Add latency to response
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Parse raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			return nil, newBifrostOperationError("error parsing raw response", err, providerName)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// extractToolsFromHistory extracts minimal tool definitions from conversation history.
// It analyzes the messages to find tool-related content and returns whether tool content
// was found and a list of unique minimal tool definitions extracted from the conversation.
// This is needed when Bedrock requires toolConfig but no tools are provided in the current request.
func (provider *BedrockProvider) extractToolsFromHistory(messages []schemas.BifrostMessage) (bool, []BedrockAnthropicToolCall) {
	hasToolContent := false
	var toolsFromHistory []BedrockAnthropicToolCall
	seenTools := make(map[string]BedrockAnthropicToolCall)

	for _, msg := range messages {
		// Check for tool result messages
		if msg.Role == schemas.ModelChatMessageRoleTool {
			hasToolContent = true
		}
		// Check for assistant messages with tool calls
		if msg.Role == schemas.ModelChatMessageRoleAssistant && msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
			hasToolContent = true
			// Extract tool definitions from tool calls for toolConfig
			for _, toolCall := range *msg.AssistantMessage.ToolCalls {
				if toolCall.Function.Name != nil {
					toolName := *toolCall.Function.Name
					if _, exists := seenTools[toolName]; !exists {
						// Create a basic tool definition from the tool call
						// Note: We can't fully reconstruct the original tool definition,
						// but we can provide a minimal one that satisfies Bedrock's requirement
						tool := BedrockAnthropicToolCall{
							ToolSpec: BedrockAnthropicToolSpec{
								Name:        toolName,
								Description: fmt.Sprintf("Tool: %s", toolName),
								InputSchema: struct {
									Json interface{} `json:"json"`
								}{
									Json: map[string]interface{}{
										"type":       "object",
										"properties": map[string]interface{}{},
									},
								},
							},
						}
						seenTools[toolName] = tool
						toolsFromHistory = append(toolsFromHistory, tool)
					}
				}
			}
		}
	}

	return hasToolContent, toolsFromHistory
}

// prepareToolChoice prepares tool choice configuration for different model types.
// Both Anthropic and Mistral models on Bedrock support toolChoice in toolConfig.
func (provider *BedrockProvider) prepareToolChoice(params *schemas.ModelParameters, model string) interface{} {
	if params == nil || params.ToolChoice == nil {
		return nil
	}

	switch {
	case strings.Contains(model, "anthropic."), strings.Contains(model, "mistral."):
		// Both Anthropic and Mistral models use toolChoice in toolConfig
		// AWS Bedrock supports: "auto", "any", "tool" as union types
		if params.ToolChoice.ToolChoiceStr != nil {
			choice := *params.ToolChoice.ToolChoiceStr
			switch choice {
			case string(schemas.ToolChoiceTypeAuto), string(schemas.ToolChoiceTypeAny):
				return nil
			case string(schemas.ToolChoiceTypeFunction), "tool":
				if params.ToolChoice.ToolChoiceStruct == nil {
					return nil
				}
				return &BedrockToolChoice{
					Tool: &BedrockSpecificTool{
						Type: "tool",
						Name: params.ToolChoice.ToolChoiceStruct.Function.Name,
					},
				}
			}
			// Note: "none" is not supported by AWS Bedrock for these models
		} else if params.ToolChoice.ToolChoiceStruct != nil {
			if (params.ToolChoice.ToolChoiceStruct.Type == schemas.ToolChoiceTypeFunction || params.ToolChoice.ToolChoiceStruct.Type == "tool") &&
				params.ToolChoice.ToolChoiceStruct.Function.Name != "" {

				return &BedrockToolChoice{
					Tool: &BedrockSpecificTool{
						Type: "tool",
						Name: params.ToolChoice.ToolChoiceStruct.Function.Name,
					},
				}
			}
		}
	}

	return nil
}

// ChatCompletion performs a chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationChatCompletion); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	messageBody, err := provider.prepareChatCompletionMessages(messages, model)
	if err != nil {
		return nil, err
	}

	preparedParams := prepareParams(params)

	if strings.Contains(model, "anthropic.") {
		if _, exists := preparedParams["max_tokens"]; !exists {
			preparedParams["max_tokens"] = AnthropicDefaultMaxTokens
		}
	}

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		tools, err := provider.getChatCompletionTools(params, model)
		if err != nil {
			return nil, err
		}
		toolConfig := map[string]interface{}{
			"tools": tools,
		}

		// Add tool choice if specified
		if toolChoice := provider.prepareToolChoice(params, model); toolChoice != nil {
			toolConfig["toolChoice"] = toolChoice
		}

		preparedParams["toolConfig"] = toolConfig

		delete(preparedParams, "tools")
		delete(preparedParams, "tool_choice")
	} else {
		// Check if conversation history contains tool use/result blocks
		// Bedrock requires toolConfig when such blocks are present
		hasToolContent, toolsFromHistory := provider.extractToolsFromHistory(messages)

		// If conversation contains tool content but no tools provided in current request,
		// include the extracted tools to satisfy Bedrock's toolConfig requirement
		if hasToolContent && len(toolsFromHistory) > 0 {
			preparedParams["toolConfig"] = map[string]interface{}{
				"tools": toolsFromHistory,
			}
		}
	}

	requestBody := mergeConfig(messageBody, preparedParams)

	// Format the path with proper model identifier
	path := fmt.Sprintf("%s/converse", model)

	if key.BedrockKeyConfig.Deployments != nil {
		if inferenceProfileId, ok := key.BedrockKeyConfig.Deployments[model]; ok {
			if key.BedrockKeyConfig.ARN != nil {
				encodedModelIdentifier := url.QueryEscape(fmt.Sprintf("%s/%s", *key.BedrockKeyConfig.ARN, inferenceProfileId))
				path = fmt.Sprintf("%s/converse", encodedModelIdentifier)
			}
		}
	}

	// Create the signed request
	responseBody, latency, err := provider.completeRequest(ctx, requestBody, path, *key.BedrockKeyConfig)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Collect all content and tool calls into a single message (similar to Anthropic aggregation)
	var toolCalls []schemas.ToolCall

	var contentBlocks []schemas.ContentBlock
	// Process content and tool calls
	for _, choice := range response.Output.Message.Content {
		if choice.Text != nil && *choice.Text != "" {
			contentBlocks = append(contentBlocks, schemas.ContentBlock{
				Type: "text",
				Text: choice.Text,
			})
		}

		if choice.ToolUse != nil {
			input := choice.ToolUse.Input
			if input == nil {
				input = map[string]any{}
			}
			arguments, err := sonic.Marshal(input)
			if err != nil {
				arguments = []byte("{}")
			}

			toolCalls = append(toolCalls, schemas.ToolCall{
				Type: Ptr("function"),
				ID:   &choice.ToolUse.ToolUseID,
				Function: schemas.FunctionCall{
					Name:      &choice.ToolUse.Name,
					Arguments: string(arguments),
				},
			})
		}
	}

	// Create the assistant message
	var assistantMessage *schemas.AssistantMessage

	// Create AssistantMessage if we have tool calls
	if len(toolCalls) > 0 {
		assistantMessage = &schemas.AssistantMessage{
			ToolCalls: &toolCalls,
		}
	}

	// Create a single choice with the aggregated content
	choices := []schemas.BifrostResponseChoice{
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
			},
			FinishReason: &response.StopReason,
		},
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		Choices: choices,
		Usage: &schemas.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.TotalTokens,
		},
		Model: model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:  latency.Milliseconds(),
			Provider: providerName,
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

// signAWSRequest signs an HTTP request using AWS Signature Version 4.
// It is used in providers like Bedrock.
// It sets required headers, calculates the request body hash, and signs the request
// using the provided AWS credentials.
// Returns a BifrostError if signing fails.
func signAWSRequest(ctx context.Context, req *http.Request, accessKey, secretKey string, sessionToken *string, region, service string, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Set required headers before signing
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Calculate SHA256 hash of the request body
	var bodyHash string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return newBifrostOperationError("error reading request body", err, providerName)
		}
		// Restore the body for subsequent reads
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		hash := sha256.Sum256(bodyBytes)
		bodyHash = hex.EncodeToString(hash[:])
	} else {
		// For empty body, use the hash of an empty string
		hash := sha256.Sum256([]byte{})
		bodyHash = hex.EncodeToString(hash[:])
	}

	var cfg aws.Config
	var err error

	// If both accessKey and secretKey are empty, use the default credential provider chain
	// This will automatically use IAM roles, environment variables, shared credentials, etc.
	if accessKey == "" && secretKey == "" {
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
		)
	} else {
		// Use explicit credentials when provided
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
				creds := aws.Credentials{
					AccessKeyID:     accessKey,
					SecretAccessKey: secretKey,
				}
				if sessionToken != nil && *sessionToken != "" {
					creds.SessionToken = *sessionToken
				}
				return creds, nil
			})),
		)
	}
	if err != nil {
		return newBifrostOperationError("failed to load aws config", err, providerName)
	}

	// Create the AWS signer
	signer := v4.NewSigner()

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return newBifrostOperationError("failed to retrieve aws credentials", err, providerName)
	}

	// Sign the request with AWS Signature V4
	if err := signer.SignHTTP(ctx, creds, req, bodyHash, service, region, time.Now()); err != nil {
		return newBifrostOperationError("failed to sign request", err, providerName)
	}

	return nil
}

// Embedding generates embeddings for the given input text(s) using Amazon Bedrock.
// Supports Titan and Cohere embedding models. Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *BedrockProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationEmbedding); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	switch {
	case strings.Contains(model, "amazon.titan-embed-text"):
		return provider.handleTitanEmbedding(ctx, model, *key.BedrockKeyConfig, input, params, providerName)
	case strings.Contains(model, "cohere.embed"):
		return provider.handleCohereEmbedding(ctx, model, *key.BedrockKeyConfig, input, params, providerName)
	default:
		return nil, newConfigurationError("embedding is not supported for this Bedrock model", providerName)
	}
}

// handleTitanEmbedding handles embedding requests for Amazon Titan models.
func (provider *BedrockProvider) handleTitanEmbedding(ctx context.Context, model string, config schemas.BedrockKeyConfig, input *schemas.EmbeddingInput, params *schemas.ModelParameters, providerName schemas.ModelProvider) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Titan Text Embeddings V1/V2 - only supports single text input
	if len(input.Texts) == 0 {
		return nil, newConfigurationError("no input text provided for embedding", providerName)
	}
	if len(input.Texts) > 1 {
		return nil, newConfigurationError("Amazon Titan embedding models support only single text input, received multiple texts", providerName)
	}

	requestBody := map[string]interface{}{
		"inputText": input.Texts[0],
	}

	if params != nil {
		// Titan models do not support the dimensions parameter - they have fixed dimensions
		if params.Dimensions != nil {
			return nil, newConfigurationError("Amazon Titan embedding models do not support custom dimensions parameter", providerName)
		}
		if params.ExtraParams != nil {
			for k, v := range params.ExtraParams {
				requestBody[k] = v
			}
		}
	}

	// Properly escape model name for URL path to ensure AWS SIGv4 signing works correctly
	path := url.PathEscape(model) + "/invoke"
	rawResponse, latency, err := provider.completeRequest(ctx, requestBody, path, config)
	if err != nil {
		return nil, err
	}

	// Parse Titan response from raw message
	var titanResp struct {
		Embedding           []float32 `json:"embedding"`
		InputTextTokenCount int       `json:"inputTextTokenCount"`
	}
	if err := sonic.Unmarshal(rawResponse, &titanResp); err != nil {
		return nil, newBifrostOperationError("error parsing Titan embedding response", err, providerName)
	}

	bifrostResponse := &schemas.BifrostResponse{
		Object: "list",
		Data: []schemas.BifrostEmbedding{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.BifrostEmbeddingResponse{
					Embedding2DArray: &[][]float32{titanResp.Embedding},
				},
			},
		},
		Model: model,
		Usage: &schemas.LLMUsage{
			PromptTokens: titanResp.InputTextTokenCount,
			TotalTokens:  titanResp.InputTextTokenCount,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
			Latency:  latency.Milliseconds(),
		},
	}

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// handleCohereEmbedding handles embedding requests for Cohere models on Bedrock.
func (provider *BedrockProvider) handleCohereEmbedding(ctx context.Context, model string, config schemas.BedrockKeyConfig, input *schemas.EmbeddingInput, params *schemas.ModelParameters, providerName schemas.ModelProvider) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if len(input.Texts) == 0 {
		return nil, newConfigurationError("no input text provided for embedding", providerName)
	}

	requestBody := map[string]interface{}{
		"texts":      input.Texts,
		"input_type": "search_document",
	}
	if params != nil && params.ExtraParams != nil {
		maps.Copy(requestBody, params.ExtraParams)
	}

	// Properly escape model name for URL path to ensure AWS SIGv4 signing works correctly
	path := url.PathEscape(model) + "/invoke"
	rawResponse, latency, err := provider.completeRequest(ctx, requestBody, path, config)
	if err != nil {
		return nil, err
	}

	// Parse Cohere response
	var cohereResp struct {
		Embeddings [][]float32 `json:"embeddings"`
		ID         string      `json:"id"`
		Texts      []string    `json:"texts"`
	}
	if err := sonic.Unmarshal(rawResponse, &cohereResp); err != nil {
		return nil, newBifrostOperationError("error parsing embedding response", err, providerName)
	}

	// Calculate token usage based on input texts (approximation since Cohere doesn't provide this)
	totalInputTokens := approximateTokenCount(input.Texts)

	bifrostResponse := &schemas.BifrostResponse{
		Object: "list",
		Data: []schemas.BifrostEmbedding{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.BifrostEmbeddingResponse{
					Embedding2DArray: &cohereResp.Embeddings,
				},
			},
		},
		ID:    cohereResp.ID,
		Model: model,
		Usage: &schemas.LLMUsage{
			PromptTokens: totalInputTokens,
			TotalTokens:  totalInputTokens,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
			Latency:  latency.Milliseconds(),
		},
	}

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the streaming response.
// Returns a channel for streaming BifrostResponse objects or an error if the request fails.
func (provider *BedrockProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationChatCompletionStream); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	messageBody, err := provider.prepareChatCompletionMessages(messages, model)
	if err != nil {
		return nil, err
	}

	preparedParams := prepareParams(params)

	if strings.Contains(model, "anthropic.") {
		if _, exists := preparedParams["max_tokens"]; !exists {
			preparedParams["max_tokens"] = AnthropicDefaultMaxTokens
		}
	}

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		tools, err := provider.getChatCompletionTools(params, model)
		if err != nil {
			return nil, err
		}

		toolConfig := map[string]interface{}{
			"tools": tools,
		}

		// Add tool choice if specified
		if toolChoice := provider.prepareToolChoice(params, model); toolChoice != nil {
			toolConfig["toolChoice"] = toolChoice
		}

		preparedParams["toolConfig"] = toolConfig
	} else {
		// Check if conversation history contains tool use/result blocks
		// Bedrock requires toolConfig when such blocks are present
		hasToolContent, toolsFromHistory := provider.extractToolsFromHistory(messages)

		// If conversation contains tool content but no tools provided in current request,
		// include the extracted tools to satisfy Bedrock's toolConfig requirement
		if hasToolContent && len(toolsFromHistory) > 0 {
			preparedParams["toolConfig"] = map[string]interface{}{
				"tools": toolsFromHistory,
			}
		}
	}

	requestBody := mergeConfig(messageBody, preparedParams)

	// Format the path with proper model identifier for streaming
	path := fmt.Sprintf("%s/converse-stream", model)

	if key.BedrockKeyConfig.Deployments != nil {
		if inferenceProfileId, ok := key.BedrockKeyConfig.Deployments[model]; ok {
			if key.BedrockKeyConfig.ARN != nil {
				encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", *key.BedrockKeyConfig.ARN, inferenceProfileId))
				path = fmt.Sprintf("%s/converse-stream", encodedModelIdentifier)
			}
		}
	}

	region := "us-east-1"
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Create the streaming request
	jsonBody, jsonErr := sonic.Marshal(requestBody)
	if jsonErr != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, jsonErr, providerName)
	}

	// Create HTTP request for streaming
	req, reqErr := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewReader(jsonBody))
	if reqErr != nil {
		return nil, newBifrostOperationError("error creating request", reqErr, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Sign the request using either explicit credentials or IAM role authentication
	if signErr := signAWSRequest(ctx, req, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); signErr != nil {
		return nil, signErr
	}

	// Make the request
	resp, respErr := provider.client.Do(req)
	if respErr != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, respErr, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		// Process AWS Event Stream format
		var messageID string
		var usage *schemas.LLMUsage
		var finishReason *string
		chunkIndex := -1

		// Read the response body as a continuous stream
		reader := bufio.NewReader(resp.Body)
		buffer := make([]byte, 1024*1024) // 1MB buffer
		var accumulator []byte            // Accumulate data across reads

		for {
			n, err := reader.Read(buffer)
			if err != nil {
				if err == io.EOF {
					// Process any remaining data in the accumulator
					if len(accumulator) > 0 {
						_ = provider.processAWSEventStreamData(ctx, postHookRunner, accumulator, &messageID, &chunkIndex, &usage, &finishReason, model, providerName, responseChan)
					}
					break
				}
				provider.logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerName, err))
				processAndSendError(ctx, postHookRunner, err, responseChan, provider.logger)
				return
			}

			if n == 0 {
				continue
			}

			// Append new data to accumulator
			accumulator = append(accumulator, buffer[:n]...)

			// Process the accumulated data and get the remaining unprocessed part
			remaining := provider.processAWSEventStreamData(ctx, postHookRunner, accumulator, &messageID, &chunkIndex, &usage, &finishReason, model, providerName, responseChan)

			// Reset accumulator with remaining data
			accumulator = remaining
		}

		// Send final response
		response := createBifrostChatCompletionChunkResponse(usage, finishReason, chunkIndex, params, providerName)
		handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, provider.logger)
	}()

	return responseChan, nil
}

// processAWSEventStreamData processes raw AWS Event Stream data and extracts JSON events.
// Returns any remaining unprocessed bytes that should be kept for the next read.
func (provider *BedrockProvider) processAWSEventStreamData(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	data []byte,
	messageID *string,
	chunkIndex *int,
	usage **schemas.LLMUsage,
	finishReason **string,
	model string,
	providerName schemas.ModelProvider,
	responseChan chan *schemas.BifrostStream,
) []byte {
	lastProcessed := 0
	depth := 0
	inString := false
	escaped := false
	objStart := -1

	for i := 0; i < len(data); i++ {
		b := data[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch b {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				objStart = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && objStart >= 0 {
					jsonBytes := data[objStart : i+1]
					// Quick filter to match original behavior - check for JSON content and relevant fields
					hasQuotes := bytes.Contains(jsonBytes, []byte(`"`))
					hasRelevantContent := bytes.Contains(jsonBytes, []byte(`role`)) ||
						bytes.Contains(jsonBytes, []byte(`delta`)) ||
						bytes.Contains(jsonBytes, []byte(`usage`)) ||
						bytes.Contains(jsonBytes, []byte(`stopReason`)) ||
						bytes.Contains(jsonBytes, []byte(`contentBlockIndex`)) ||
						bytes.Contains(jsonBytes, []byte(`metadata`))

					if hasQuotes && hasRelevantContent {
						provider.processEventBuffer(ctx, postHookRunner, jsonBytes, messageID, chunkIndex, usage, finishReason, model, providerName, responseChan)
						lastProcessed = i + 1
					}
					objStart = -1
				}
			}
		default:
			// skip
		}
	}

	if lastProcessed < len(data) {
		return data[lastProcessed:]
	}
	return nil
}

// processEventBuffer processes AWS Event Stream JSON payloads and determines event type from content
func (provider *BedrockProvider) processEventBuffer(ctx context.Context, postHookRunner schemas.PostHookRunner, eventBuffer []byte, messageID *string, chunkIndex *int, usage **schemas.LLMUsage, finishReason **string, model string, providerName schemas.ModelProvider, responseChan chan *schemas.BifrostStream) {
	// Parse the JSON event
	var event map[string]interface{}
	if err := sonic.Unmarshal(eventBuffer, &event); err != nil {
		provider.logger.Debug(fmt.Sprintf("Failed to parse JSON from event buffer: %v, data: %s", err, string(eventBuffer)))
		return
	}

	// Determine event type based on JSON content structure
	switch {
	case event["role"] != nil:
		// This is a messageStart event
		*chunkIndex++
		if role, ok := event["role"].(string); ok {
			*messageID = fmt.Sprintf("bedrock-%d", time.Now().UnixNano())

			// Send empty response to signal start
			streamResponse := &schemas.BifrostResponse{
				ID:     *messageID,
				Object: "chat.completion.chunk",
				Model:  model,
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
					Provider:   providerName,
					ChunkIndex: *chunkIndex,
				},
			}

			// Use utility function to process and send response
			processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)
		}

	case event["contentBlockIndex"] != nil && event["delta"] != nil:
		// This is a contentBlockDelta event
		*chunkIndex++
		contentBlockIndex := 0
		if idx, ok := event["contentBlockIndex"].(float64); ok {
			contentBlockIndex = int(idx)
		}

		if delta, ok := event["delta"].(map[string]interface{}); ok {
			switch {
			case delta["text"] != nil:
				// Handle text delta
				if text, ok := delta["text"].(string); ok && text != "" {
					// Create streaming response for this delta
					streamResponse := &schemas.BifrostResponse{
						ID:     *messageID,
						Object: "chat.completion.chunk",
						Model:  model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: contentBlockIndex,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{
										Content: &text,
									},
								},
							},
						},
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider:   providerName,
							ChunkIndex: *chunkIndex,
						},
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)
				}

			case delta["toolUse"] != nil:
				// Handle tool use delta
				if toolUse, ok := delta["toolUse"].(map[string]interface{}); ok {
					// Parse the tool use structure properly
					var toolCall schemas.ToolCall
					toolCall.Type = func() *string { s := "function"; return &s }()

					// Extract toolUseId
					if toolUseID, hasID := toolUse["toolUseId"].(string); hasID {
						toolCall.ID = &toolUseID
					}

					// Extract name
					if name, hasName := toolUse["name"].(string); hasName {
						toolCall.Function.Name = &name
					}

					// Extract and marshal input as arguments
					if input, hasInput := toolUse["input"].(map[string]interface{}); hasInput {
						inputBytes, err := sonic.Marshal(input)
						if err != nil {
							toolCall.Function.Arguments = "{}"
						} else {
							toolCall.Function.Arguments = string(inputBytes)
						}
					} else {
						toolCall.Function.Arguments = "{}"
					}

					// Create streaming response for tool delta
					streamResponse := &schemas.BifrostResponse{
						ID:     *messageID,
						Object: "chat.completion.chunk",
						Model:  model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: contentBlockIndex,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{
										ToolCalls: []schemas.ToolCall{toolCall},
									},
								},
							},
						},
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider:   providerName,
							ChunkIndex: *chunkIndex,
						},
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)
				}
			}
		}

	case event["stopReason"] != nil:
		// This is a messageStop event
		if stopReason, ok := event["stopReason"].(string); ok {
			*finishReason = &stopReason
		}

	case event["usage"] != nil:
		// This is a metadata event with usage information at top level
		if usageData, ok := event["usage"].(map[string]interface{}); ok {
			inputTokens := 0
			outputTokens := 0
			totalTokens := 0

			if val, exists := usageData["inputTokens"].(float64); exists {
				inputTokens = int(val)
			}
			if val, exists := usageData["outputTokens"].(float64); exists {
				outputTokens = int(val)
			}
			if val, exists := usageData["totalTokens"].(float64); exists {
				totalTokens = int(val)
			}

			*usage = &schemas.LLMUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				TotalTokens:      totalTokens,
			}
		}

	case event["metadata"] != nil:
		// This is a metadata event - check if it contains nested usage information
		if metadata, ok := event["metadata"].(map[string]interface{}); ok {
			if usageData, ok := metadata["usage"].(map[string]interface{}); ok {
				inputTokens := 0
				outputTokens := 0
				totalTokens := 0

				if val, exists := usageData["inputTokens"].(float64); exists {
					inputTokens = int(val)
				}
				if val, exists := usageData["outputTokens"].(float64); exists {
					outputTokens = int(val)
				}
				if val, exists := usageData["totalTokens"].(float64); exists {
					totalTokens = int(val)
				}

				*usage = &schemas.LLMUsage{
					PromptTokens:     inputTokens,
					CompletionTokens: outputTokens,
					TotalTokens:      totalTokens,
				}
			}
		}

	default:
		// Log unknown event types for debugging
		provider.logger.Debug(fmt.Sprintf("Unknown event type received: %v", event))
	}
}

func (provider *BedrockProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "bedrock")
}

func (provider *BedrockProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "bedrock")
}

func (provider *BedrockProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "bedrock")
}

func (provider *BedrockProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "bedrock")
}
