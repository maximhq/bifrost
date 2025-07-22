// Package providers implements various LLM providers and their utility functions.
// This file contains the AWS Bedrock provider implementation.
package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"bufio"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
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
	ToolCalls  *[]BedrockMistralToolCall    `json:"tool_calls,omitempty"`   // Optional tool calls
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

// BedrockMistralToolCall represents a tool call for Mistral models.
type BedrockMistralToolCall struct {
	ID       string               `json:"id"`       // Tool call ID
	Function schemas.FunctionCall `json:"function"` // Function to call
}

type BedrockAnthropicToolUseMessage struct {
	ToolUse *BedrockAnthropicToolUse `json:"toolUse"`
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
	logger        schemas.Logger        // Logger for provider operations
	client        *http.Client          // HTTP client for API requests
	meta          schemas.MetaConfig    // Bedrock-specific configuration
	networkConfig schemas.NetworkConfig // Network configuration including extra headers
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

	if config.MetaConfig == nil {
		return nil, fmt.Errorf("meta config is not set")
	}

	client := &http.Client{Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		bedrockChatResponsePool.Put(&BedrockChatResponse{})

	}

	return &BedrockProvider{
		logger:        logger,
		client:        client,
		meta:          config.MetaConfig,
		networkConfig: config.NetworkConfig,
	}, nil
}

// GetProviderKey returns the provider identifier for Bedrock.
func (provider *BedrockProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Bedrock
}

// CompleteRequest sends a request to Bedrock's API and handles the response.
// It constructs the API URL, sets up AWS authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *BedrockProvider) completeRequest(ctx context.Context, requestBody map[string]interface{}, path string, accessKey string) ([]byte, *schemas.BifrostError) {
	if provider.meta == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "meta config for bedrock is not provided",
			},
		}
	}

	region := "us-east-1"
	if provider.meta.GetRegion() != nil {
		region = *provider.meta.GetRegion()
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: schemas.ErrorField{
					Type:    StrPtr(schemas.RequestCancelled),
					Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
					Error:   err,
				},
			}
		}
		return nil, &schemas.BifrostError{
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
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	if provider.meta.GetSecretAccessKey() != nil {
		if err := signAWSRequest(req, accessKey, *provider.meta.GetSecretAccessKey(), provider.meta.GetSessionToken(), region, "bedrock"); err != nil {
			return nil, err
		}
	} else {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "secret access key not set",
			},
		}
	}

	// Execute the request
	resp, err := provider.client.Do(req)
	if err != nil {
		return nil, &schemas.BifrostError{
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
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp BedrockError

		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &resp.StatusCode,
				Error: schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
			}
		}

		return nil, &schemas.BifrostError{
			StatusCode: &resp.StatusCode,
			Error: schemas.ErrorField{
				Message: errorResp.Message,
			},
		}
	}

	return body, nil
}

// GetTextCompletionResult processes the text completion response from Bedrock.
// It handles different model types (Anthropic and Mistral) and formats the response.
// Returns a BifrostResponse containing the completion results or an error if processing fails.
func (provider *BedrockProvider) getTextCompletionResult(result []byte, model string) (*schemas.BifrostResponse, *schemas.BifrostError) {
	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		var response BedrockAnthropicTextResponse
		if err := json.Unmarshal(result, &response); err != nil {
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
				Provider: schemas.Bedrock,
			},
		}, nil

	case "mistral.mixtral-8x7b-instruct-v0:1":
		fallthrough
	case "mistral.mistral-7b-instruct-v0:2":
		fallthrough
	case "mistral.mistral-large-2402-v1:0":
		fallthrough
	case "mistral.mistral-large-2407-v1:0":
		fallthrough
	case "mistral.mistral-small-2402-v1:0":
		var response BedrockMistralTextResponse
		if err := json.Unmarshal(result, &response); err != nil {
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
				Provider: schemas.Bedrock,
			},
		}, nil
	}

	return nil, newConfigurationError(fmt.Sprintf("invalid model choice: %s", model), schemas.Bedrock)
}

// parseBedrockAnthropicMessageToolCallContent parses the content of a tool call message.
// It handles both text and JSON content.
// Returns a map containing the parsed content.
func parseBedrockAnthropicMessageToolCallContent(content string) map[string]interface{} {
	toolResultContentBlock := map[string]interface{}{}
	var parsedJSON interface{}
	err := json.Unmarshal([]byte(content), &parsedJSON)
	if err == nil {
		if arr, ok := parsedJSON.([]interface{}); ok {
			toolResultContentBlock["json"] = map[string]interface{}{"content": arr}
		} else {
			toolResultContentBlock["json"] = parsedJSON
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
	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		fallthrough
	case "anthropic.claude-3-sonnet-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20240620-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20241022-v2:0":
		fallthrough
	case "anthropic.claude-3-5-haiku-20241022-v1:0":
		fallthrough
	case "anthropic.claude-3-opus-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-7-sonnet-20250219-v1:0":
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
					if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
						for _, toolCall := range *msg.AssistantMessage.ToolCalls {
							var input map[string]interface{}
							if toolCall.Function.Arguments != "" {
								if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
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
					}

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

	case "mistral.mistral-large-2402-v1:0":
		fallthrough
	case "mistral.mistral-large-2407-v1:0":
		var bedrockMessages []BedrockMistralChatMessage
		for _, msg := range messages {
			var filteredToolCalls []BedrockMistralToolCall
			if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
				for _, toolCall := range *msg.AssistantMessage.ToolCalls {
					if toolCall.ID != nil {
						filteredToolCalls = append(filteredToolCalls, BedrockMistralToolCall{
							ID:       *toolCall.ID,
							Function: toolCall.Function,
						})
					}
				}
			}

			message := BedrockMistralChatMessage{
				Role: msg.Role,
			}

			switch {
			case msg.Content.ContentStr != nil:
				message.Content = []BedrockMistralContent{{Text: *msg.Content.ContentStr}}
			case msg.Content.ContentBlocks != nil:
				for _, b := range *msg.Content.ContentBlocks {
					if b.Text != nil {
						message.Content = append(message.Content, BedrockMistralContent{Text: *b.Text})
					}
				}
			}

			if len(filteredToolCalls) > 0 {
				message.ToolCalls = &filteredToolCalls
			}

			bedrockMessages = append(bedrockMessages, message)
		}

		body := map[string]interface{}{
			"messages": bedrockMessages,
		}

		return body, nil
	}

	return nil, newConfigurationError(fmt.Sprintf("invalid model choice: %s", model), schemas.Bedrock)
}

// GetChatCompletionTools prepares tool specifications for Bedrock's API.
// It formats tool definitions for different model types (Anthropic and Mistral).
// Returns an array of tool specifications for the given model.
func (provider *BedrockProvider) getChatCompletionTools(params *schemas.ModelParameters, model string) []BedrockAnthropicToolCall {
	var tools []BedrockAnthropicToolCall

	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		fallthrough
	case "anthropic.claude-3-sonnet-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20240620-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20241022-v2:0":
		fallthrough
	case "anthropic.claude-3-5-haiku-20241022-v1:0":
		fallthrough
	case "anthropic.claude-3-opus-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-7-sonnet-20250219-v1:0":
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
	}

	return tools
}

// prepareTextCompletionParams prepares text completion parameters for Bedrock's API.
// It handles parameter mapping and conversion for different model types.
// Returns the modified parameters map with model-specific adjustments.
func (provider *BedrockProvider) prepareTextCompletionParams(params map[string]interface{}, model string) map[string]interface{} {
	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		// Check if there is a key entry for max_tokens
		if maxTokens, exists := params["max_tokens"]; exists {
			// Check if max_tokens_to_sample is already present
			if _, exists := params["max_tokens_to_sample"]; !exists {
				// If max_tokens_to_sample is not present, rename max_tokens to max_tokens_to_sample
				params["max_tokens_to_sample"] = maxTokens
			}
			delete(params, "max_tokens")
		}
	}
	return params
}

// TextCompletion performs a text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	preparedParams := provider.prepareTextCompletionParams(prepareParams(params), model)

	requestBody := mergeConfig(map[string]interface{}{
		"prompt": text,
	}, preparedParams)

	body, err := provider.completeRequest(ctx, requestBody, fmt.Sprintf("%s/invoke", model), key.Value)
	if err != nil {
		return nil, err
	}

	bifrostResponse, err := provider.getTextCompletionResult(body, model)
	if err != nil {
		return nil, err
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, newBifrostOperationError("error parsing raw response", err, schemas.Bedrock)
	}

	bifrostResponse.ExtraFields.RawResponse = rawResponse

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

// ChatCompletion performs a chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	messageBody, err := provider.prepareChatCompletionMessages(messages, model)
	if err != nil {
		return nil, err
	}

	preparedParams := prepareParams(params)

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		preparedParams["toolConfig"] = map[string]interface{}{
			"tools": provider.getChatCompletionTools(params, model),
		}
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

	if provider.meta != nil && provider.meta.GetInferenceProfiles() != nil {
		if inferenceProfileId, ok := provider.meta.GetInferenceProfiles()[model]; ok {
			if provider.meta.GetARN() != nil {
				encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", *provider.meta.GetARN(), inferenceProfileId))
				path = fmt.Sprintf("%s/converse", encodedModelIdentifier)
			}
		}
	}

	// Create the signed request
	responseBody, err := provider.completeRequest(ctx, requestBody, path, key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
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
			arguments, err := json.Marshal(input)
			if err != nil {
				arguments = []byte("{}")
			}

			toolCalls = append(toolCalls, schemas.ToolCall{
				Type: StrPtr("function"),
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

	latency := float64(response.Metrics.Latency)

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
			Latency:     &latency,
			Provider:    schemas.Bedrock,
			RawResponse: rawResponse,
		},
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
func signAWSRequest(req *http.Request, accessKey, secretKey string, sessionToken *string, region, service string) *schemas.BifrostError {
	// Set required headers before signing
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Calculate SHA256 hash of the request body
	var bodyHash string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return newBifrostOperationError("error reading request body", err, schemas.Bedrock)
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

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			creds := aws.Credentials{
				AccessKeyID:     accessKey,
				SecretAccessKey: secretKey,
			}
			if sessionToken != nil {
				creds.SessionToken = *sessionToken
			}
			return creds, nil
		})),
	)
	if err != nil {
		return newBifrostOperationError("failed to load aws config", err, schemas.Bedrock)
	}

	// Create the AWS signer
	signer := v4.NewSigner()

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(context.TODO())
	if err != nil {
		return newBifrostOperationError("failed to retrieve aws credentials", err, schemas.Bedrock)
	}

	// Sign the request with AWS Signature V4
	if err := signer.SignHTTP(context.TODO(), creds, req, bodyHash, service, region, time.Now()); err != nil {
		return newBifrostOperationError("failed to sign request", err, schemas.Bedrock)
	}

	return nil
}

// Embedding generates embeddings for the given input text(s) using Amazon Bedrock.
// Supports Titan and Cohere embedding models. Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *BedrockProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	switch {
	case strings.HasPrefix(model, "amazon.titan-embed-text"):
		return provider.handleTitanEmbedding(ctx, model, key.Value, input, params)
	case strings.HasPrefix(model, "cohere.embed"):
		return provider.handleCohereEmbedding(ctx, model, key.Value, input, params)
	default:
		return nil, newConfigurationError("embedding is not supported for this Bedrock model", schemas.Bedrock)
	}
}

// handleTitanEmbedding handles embedding requests for Amazon Titan models.
func (provider *BedrockProvider) handleTitanEmbedding(ctx context.Context, model string, key string, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Titan Text Embeddings V1/V2 - only supports single text input
	if len(input.Texts) == 0 {
		return nil, newConfigurationError("no input text provided for embedding", schemas.Bedrock)
	}
	if len(input.Texts) > 1 {
		return nil, newConfigurationError("Amazon Titan embedding models support only single text input, received multiple texts", schemas.Bedrock)
	}

	requestBody := map[string]interface{}{
		"inputText": input.Texts[0],
	}

	if params != nil {
		// Titan models do not support the dimensions parameter - they have fixed dimensions
		if params.Dimensions != nil {
			return nil, newConfigurationError("Amazon Titan embedding models do not support custom dimensions parameter", schemas.Bedrock)
		}
		if params.ExtraParams != nil {
			for k, v := range params.ExtraParams {
				requestBody[k] = v
			}
		}
	}

	// Properly escape model name for URL path to ensure AWS SIGv4 signing works correctly
	path := url.PathEscape(model) + "/invoke"
	rawResponse, err := provider.completeRequest(ctx, requestBody, path, key)
	if err != nil {
		return nil, err
	}

	// Parse Titan response from raw message
	var titanResp struct {
		Embedding           []float32 `json:"embedding"`
		InputTextTokenCount int       `json:"inputTextTokenCount"`
	}
	if err := json.Unmarshal(rawResponse, &titanResp); err != nil {
		return nil, newBifrostOperationError("error parsing Titan embedding response", err, schemas.Bedrock)
	}

	bifrostResponse := &schemas.BifrostResponse{
		Embedding: [][]float32{titanResp.Embedding},
		Model:     model,
		Usage: &schemas.LLMUsage{
			PromptTokens: titanResp.InputTextTokenCount,
			TotalTokens:  titanResp.InputTextTokenCount,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Bedrock,
			RawResponse: rawResponse,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// handleCohereEmbedding handles embedding requests for Cohere models on Bedrock.
func (provider *BedrockProvider) handleCohereEmbedding(ctx context.Context, model string, key string, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if len(input.Texts) == 0 {
		return nil, newConfigurationError("no input text provided for embedding", schemas.Bedrock)
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
	rawResponse, err := provider.completeRequest(ctx, requestBody, path, key)
	if err != nil {
		return nil, err
	}

	// Parse Cohere response
	var cohereResp struct {
		Embeddings [][]float32 `json:"embeddings"`
		ID         string      `json:"id"`
		Texts      []string    `json:"texts"`
	}
	if err := json.Unmarshal(rawResponse, &cohereResp); err != nil {
		return nil, newBifrostOperationError("error parsing Cohere embedding response", err, schemas.Bedrock)
	}

	// Calculate token usage based on input texts (approximation since Cohere doesn't provide this)
	totalInputTokens := approximateTokenCount(input.Texts)

	bifrostResponse := &schemas.BifrostResponse{
		Embedding: cohereResp.Embeddings,
		ID:        cohereResp.ID,
		Model:     model,
		Usage: &schemas.LLMUsage{
			PromptTokens: totalInputTokens,
			TotalTokens:  totalInputTokens,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Bedrock,
			RawResponse: rawResponse,
		},
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
	messageBody, err := provider.prepareChatCompletionMessages(messages, model)
	if err != nil {
		return nil, err
	}

	preparedParams := prepareParams(params)

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		preparedParams["toolConfig"] = map[string]interface{}{
			"tools": provider.getChatCompletionTools(params, model),
		}
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

	if provider.meta != nil && provider.meta.GetInferenceProfiles() != nil {
		if inferenceProfileId, ok := provider.meta.GetInferenceProfiles()[model]; ok {
			if provider.meta.GetARN() != nil {
				encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", *provider.meta.GetARN(), inferenceProfileId))
				path = fmt.Sprintf("%s/converse-stream", encodedModelIdentifier)
			}
		}
	}

	if provider.meta == nil {
		return nil, newConfigurationError("meta config for bedrock is not provided", schemas.Bedrock)
	}

	region := "us-east-1"
	if provider.meta.GetRegion() != nil {
		region = *provider.meta.GetRegion()
	}

	// Create the streaming request
	jsonBody, jsonErr := json.Marshal(requestBody)
	if jsonErr != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, jsonErr, schemas.Bedrock)
	}

	// Create HTTP request for streaming
	req, reqErr := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), strings.NewReader(string(jsonBody)))
	if reqErr != nil {
		return nil, newBifrostOperationError("error creating request", reqErr, schemas.Bedrock)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Sign the request for AWS
	if provider.meta.GetSecretAccessKey() != nil {
		if signErr := signAWSRequest(req, key.Value, *provider.meta.GetSecretAccessKey(), provider.meta.GetSessionToken(), region, "bedrock"); signErr != nil {
			return nil, signErr
		}
	} else {
		return nil, newConfigurationError("secret access key not set", schemas.Bedrock)
	}

	// Make the request
	resp, respErr := provider.client.Do(req)
	if respErr != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, respErr, schemas.Bedrock)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from Bedrock: %d", resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, schemas.Bedrock, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		// Create a buffer scanner to process the AWS Event Stream format
		scanner := bufio.NewScanner(resp.Body)
		var messageID string

		// AWS Event Streaming can have large buffers
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}

			// AWS Event Stream format embeds JSON within binary data
			// Look for JSON objects in the stream data
			jsonStart := strings.Index(line, "{")
			if jsonStart == -1 {
				continue
			}

			// Extract the JSON part from the line
			jsonData := line[jsonStart:]

			// Find the end of the JSON object by counting braces
			braceCount := 0
			jsonEnd := -1
			for i, char := range jsonData {
				if char == '{' {
					braceCount++
				} else if char == '}' {
					braceCount--
					if braceCount == 0 {
						jsonEnd = i + 1
						break
					}
				}
			}

			if jsonEnd == -1 {
				continue
			}

			// Extract the complete JSON object
			jsonStr := jsonData[:jsonEnd]

			// Parse the JSON event
			var event map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &event); err != nil {
				provider.logger.Debug(fmt.Sprintf("Failed to parse JSON from stream: %v, data: %s", err, jsonStr))
				continue
			}

			// Determine event type and handle accordingly
			switch {
			case event["contentBlockIndex"] != nil && event["delta"] != nil:
				// This is a contentBlockDelta event
				contentBlockIndex := 0
				if idx, ok := event["contentBlockIndex"].(float64); ok {
					contentBlockIndex = int(idx)
				}

				delta, ok := event["delta"].(map[string]interface{})
				if !ok {
					continue
				}

				switch {
				case delta["text"] != nil:
					// Handle text delta
					if text, ok := delta["text"].(string); ok && text != "" {
						// Create streaming response for this delta
						streamResponse := &schemas.BifrostResponse{
							ID:     messageID,
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
								Provider: schemas.Bedrock,
							},
						}

						if params != nil {
							streamResponse.ExtraFields.Params = *params
						}

						// Use utility function to process and send response
						processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
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
							inputBytes, err := json.Marshal(input)
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
							ID:     messageID,
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
								Provider: schemas.Bedrock,
							},
						}

						if params != nil {
							streamResponse.ExtraFields.Params = *params
						}

						// Use utility function to process and send response
						processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
					}
				}

			case event["role"] != nil:
				// This is a messageStart event
				if role, ok := event["role"].(string); ok {
					messageID = fmt.Sprintf("bedrock-%d", time.Now().UnixNano())

					// Send empty response to signal start
					streamResponse := &schemas.BifrostResponse{
						ID:     messageID,
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
							Provider: schemas.Bedrock,
						},
					}

					if params != nil {
						streamResponse.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)
				}

			case event["stopReason"] != nil:
				// This is a messageStop event
				if stopReason, ok := event["stopReason"].(string); ok {
					// Send a final streaming response with finish reason
					finalResponse := &schemas.BifrostResponse{
						ID:     messageID,
						Object: "chat.completion.chunk",
						Model:  model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index:        0,
								FinishReason: &stopReason,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{}, // Empty delta for final chunk
								},
							},
						},
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider: schemas.Bedrock,
						},
					}

					if params != nil {
						finalResponse.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, finalResponse, responseChan)
					return
				}

			case event["usage"] != nil:
				// This is a metadata event with usage information
				if usage, ok := event["usage"].(map[string]interface{}); ok {
					inputTokens := 0
					outputTokens := 0
					totalTokens := 0

					if val, exists := usage["inputTokens"].(float64); exists {
						inputTokens = int(val)
					}
					if val, exists := usage["outputTokens"].(float64); exists {
						outputTokens = int(val)
					}
					if val, exists := usage["totalTokens"].(float64); exists {
						totalTokens = int(val)
					}

					// Send usage information
					usageResponse := &schemas.BifrostResponse{
						ID:     messageID,
						Object: "chat.completion.chunk",
						Model:  model,
						Usage: &schemas.LLMUsage{
							PromptTokens:     inputTokens,
							CompletionTokens: outputTokens,
							TotalTokens:      totalTokens,
						},
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{}, // Empty delta for usage update
								},
							},
						},
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider: schemas.Bedrock,
						},
					}

					if params != nil {
						usageResponse.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, usageResponse, responseChan)
				}
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading Bedrock stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan)
		}
	}()

	return responseChan, nil
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
