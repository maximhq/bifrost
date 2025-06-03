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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

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
				Text string `json:"text"` // Message content
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
	Format string                      `json:"string"` // Image format
	Source BedrockAnthropicImageSource `json:"source"` // Image source
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

// BedrockProvider implements the Provider interface for AWS Bedrock.
type BedrockProvider struct {
	logger schemas.Logger     // Logger for provider operations
	client *http.Client       // HTTP client for API requests
	meta   schemas.MetaConfig // AWS-specific configuration
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
		bifrostResponsePool.Put(&schemas.BifrostResponse{})
	}

	return &BedrockProvider{
		logger: logger,
		client: client,
		meta:   config.MetaConfig,
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
					Message: schemas.BifrostMessage{
						Role:    schemas.ModelChatMessageRoleAssistant,
						Content: &response.Completion,
					},
					FinishReason: &response.StopReason,
					StopString:   &response.Stop,
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
				Message: schemas.BifrostMessage{
					Role:    schemas.ModelChatMessageRoleAssistant,
					Content: &output.Text,
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

	return nil, &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: fmt.Sprintf("invalid model choice: %s", model),
		},
	}
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
				//TODO handling image inputs here
				if msg.Content != nil {
					systemMessages = append(systemMessages, BedrockAnthropicSystemMessage{
						Text: *msg.Content,
					})
				}
			}
		}

		// Format messages for Bedrock API
		var bedrockMessages []map[string]interface{}
		for _, msg := range messages {
			if msg.Role != schemas.ModelChatMessageRoleSystem {
				var content any
				if msg.Content != nil {
					content = BedrockAnthropicTextMessage{
						Type: "text",
						Text: *msg.Content,
					}
				} else if (msg.UserMessage != nil && msg.UserMessage.ImageContent != nil) || (msg.ToolMessage != nil && msg.ToolMessage.ImageContent != nil) {
					var messageImageContent schemas.ImageContent
					if msg.UserMessage != nil && msg.UserMessage.ImageContent != nil {
						messageImageContent = *msg.UserMessage.ImageContent
					} else if msg.ToolMessage != nil && msg.ToolMessage.ImageContent != nil {
						messageImageContent = *msg.ToolMessage.ImageContent
					}

					content = BedrockAnthropicImageMessage{
						Type: "image",
						Image: BedrockAnthropicImage{
							Format: func() string {
								if messageImageContent.Type != nil {
									return *messageImageContent.Type
								}
								return ""
							}(),
							Source: BedrockAnthropicImageSource{
								Bytes: messageImageContent.URL,
							},
						},
					}
				}

				bedrockMessages = append(bedrockMessages, map[string]interface{}{
					"role":    msg.Role,
					"content": []interface{}{content},
				})
			}
		}

		body := map[string]interface{}{
			"messages": bedrockMessages,
		}

		if len(systemMessages) > 0 {
			var messages []string
			for _, message := range systemMessages {
				messages = append(messages, message.Text)
			}

			body["system"] = strings.Join(messages, " ")
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

			if msg.Content != nil {
				message.Content = []BedrockMistralContent{
					{Text: *msg.Content},
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

	return nil, &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: fmt.Sprintf("invalid model choice: %s", model),
		},
	}
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
func (provider *BedrockProvider) TextCompletion(ctx context.Context, model, key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	preparedParams := provider.prepareTextCompletionParams(prepareParams(params), model)

	requestBody := mergeConfig(map[string]interface{}{
		"prompt": text,
	}, preparedParams)

	body, err := provider.completeRequest(ctx, requestBody, fmt.Sprintf("%s/invoke", model), key)
	if err != nil {
		return nil, err
	}

	result, err := provider.getTextCompletionResult(body, model)
	if err != nil {
		return nil, err
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "error parsing raw response",
				Error:   err,
			},
		}
	}

	result.ExtraFields.RawResponse = rawResponse

	return result, nil
}

// ChatCompletion performs a chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) ChatCompletion(ctx context.Context, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	messageBody, err := provider.prepareChatCompletionMessages(messages, model)
	if err != nil {
		return nil, err
	}

	preparedParams := prepareParams(params)

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		preparedParams["tools"] = provider.getChatCompletionTools(params, model)
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
	responseBody, err := provider.completeRequest(ctx, requestBody, path, key)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(response)

	// Create Bifrost response from pool
	bifrostResponse := acquireBifrostResponse()
	defer releaseBifrostResponse(bifrostResponse)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var choices []schemas.BifrostResponseChoice
	for i, choice := range response.Output.Message.Content {
		choices = append(choices, schemas.BifrostResponseChoice{
			Index: i,
			Message: schemas.BifrostMessage{
				Role:    schemas.ModelChatMessageRoleAssistant,
				Content: &choice.Text,
			},
			FinishReason: &response.StopReason,
		})
	}

	latency := float64(response.Metrics.Latency)

	bifrostResponse.Choices = choices
	bifrostResponse.Usage = schemas.LLMUsage{
		PromptTokens:     response.Usage.InputTokens,
		CompletionTokens: response.Usage.OutputTokens,
		TotalTokens:      response.Usage.TotalTokens,
	}
	bifrostResponse.Model = model
	bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
		Latency:     &latency,
		Provider:    schemas.Bedrock,
		RawResponse: rawResponse,
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
			return &schemas.BifrostError{
				IsBifrostError: true,
				Error: schemas.ErrorField{
					Message: "error reading request body",
					Error:   err,
				},
			}
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
		return &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "failed to load aws config",
				Error:   err,
			},
		}
	}

	// Create the AWS signer
	signer := v4.NewSigner()

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(context.TODO())
	if err != nil {
		return &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "failed to retrieve aws credentials",
				Error:   err,
			},
		}
	}

	// Sign the request with AWS Signature V4
	if err := signer.SignHTTP(context.TODO(), creds, req, bodyHash, service, region, time.Now()); err != nil {
		return &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "failed to sign request",
				Error:   err,
			},
		}
	}

	return nil
}
