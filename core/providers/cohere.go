// Package providers implements various LLM providers and their utility functions.
// This file contains the Cohere provider implementation.
package providers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"net/http"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// cohereResponsePool provides a pool for Cohere response objects.
var cohereResponsePool = sync.Pool{
	New: func() interface{} {
		return &CohereChatResponse{}
	},
}

// acquireCohereResponse gets a Cohere response from the pool and resets it.
func acquireCohereResponse() *CohereChatResponse {
	resp := cohereResponsePool.Get().(*CohereChatResponse)
	*resp = CohereChatResponse{} // Reset the struct
	return resp
}

// releaseCohereResponse returns a Cohere response to the pool.
func releaseCohereResponse(resp *CohereChatResponse) {
	if resp != nil {
		cohereResponsePool.Put(resp)
	}
}

// CohereParameterDefinition represents a parameter definition for a Cohere tool.
// It defines the type, description, and whether the parameter is required.
type CohereParameterDefinition struct {
	Type        string  `json:"type"`                  // Type of the parameter
	Description *string `json:"description,omitempty"` // Optional description of the parameter
	Required    bool    `json:"required"`              // Whether the parameter is required
}

// CohereTool represents a tool definition for the Cohere API.
// It includes the tool's name, description, and parameter definitions.
type CohereTool struct {
	Name                 string                               `json:"name"`                  // Name of the tool
	Description          string                               `json:"description"`           // Description of the tool
	ParameterDefinitions map[string]CohereParameterDefinition `json:"parameter_definitions"` // Definitions of the tool's parameters
}

// CohereToolCall represents a tool call made by the Cohere API.
// It includes the name of the tool and its parameters.
type CohereToolCall struct {
	Name       string      `json:"name"`       // Name of the tool being called
	Parameters interface{} `json:"parameters"` // Parameters passed to the tool
}

// CohereChatResponse represents the response from Cohere's chat API.
// It includes the response ID, generated text, chat history, and usage statistics.
type CohereChatResponse struct {
	GenerationID string `json:"generation_id"` // ID of the generation
	Text         string `json:"text"`          // Generated text response
	ChatHistory  []struct {
		Role      schemas.ModelChatMessageRole `json:"role"`       // Role of the message sender
		Message   string                       `json:"message"`    // Content of the message
		ToolCalls []CohereToolCall             `json:"tool_calls"` // Tool calls made in the message
	} `json:"chat_history"` // History of the chat conversation
	FinishReason string `json:"finish_reason"` // Reason for completion termination
	Meta         struct {
		APIVersion struct {
			Version string `json:"version"` // Version of the API used
		} `json:"api_version"` // API version information
		BilledUnits struct {
			InputTokens  float64 `json:"input_tokens"`  // Number of input tokens billed
			OutputTokens float64 `json:"output_tokens"` // Number of output tokens billed
		} `json:"billed_units"` // Token usage billing information
		Tokens struct {
			InputTokens  float64 `json:"input_tokens"`  // Number of input tokens used
			OutputTokens float64 `json:"output_tokens"` // Number of output tokens generated
		} `json:"tokens"` // Token usage statistics
	} `json:"meta"` // Metadata about the response
	ToolCalls []CohereToolCall `json:"tool_calls"` // Tool calls made in the response
}

// CohereError represents an error response from the Cohere API.
type CohereError struct {
	Message string `json:"message"` // Error message
}

// CohereEmbeddingResponse represents the response from Cohere's embedding API.
type CohereEmbeddingResponse struct {
	ID         string `json:"id"` // Unique identifier for the embedding request
	Embeddings struct {
		Float [][]float32 `json:"float"` // Array of float embeddings, one for each input text
	} `json:"embeddings"` // Embeddings in the response
}

// CohereProvider implements the Provider interface for Cohere.
type CohereProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// CohereStreamStartEvent represents the start of a stream event.
type CohereStreamStartEvent struct {
	EventType    string `json:"event_type"`    // stream-start
	GenerationID string `json:"generation_id"` // ID of the generation
}

// CohereStreamTextEvent represents the text generation event.
type CohereStreamTextEvent struct {
	EventType string `json:"event_type"` // text-generation
	Text      string `json:"text"`       // Text content being generated
}

// CohereStreamToolEvent represents the tool use event.
type CohereStreamToolCallEvent struct {
	EventType string `json:"event_type"` // tool-use
	ToolCall  struct {
		ID         string `json:"id"`         // ID of the tool call
		Parameters string `json:"parameters"` // Parameters of the tool being called
	} `json:"tool_call"` // Tool call information
	Text *string `json:"text"` // Text content being generated
}

// CohereStreamStopEvent represents the end of a stream event.
type CohereStreamStopEvent struct {
	EventType string             `json:"event_type"` // stream-end
	Response  CohereChatResponse `json:"response"`   // Response information
}

// NewCohereProvider creates a new Cohere provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and connection limits.
func NewCohereProvider(config *schemas.ProviderConfig, logger schemas.Logger) *CohereProvider {
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
		cohereResponsePool.Put(&CohereChatResponse{})

	}

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.cohere.ai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CohereProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Cohere.
func (provider *CohereProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Cohere
}

// TextCompletion is not supported by the Cohere provider.
// Returns an error indicating that text completion is not supported.
func (provider *CohereProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "cohere")
}

// ChatCompletion performs a chat completion request to the Cohere API.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *CohereProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Prepare request body using shared function
	requestBody, err := prepareCohereChatRequest(messages, params, model, false)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "failed to prepare Cohere chat request",
				Error:   err,
			},
		}
	}

	// Marshal request body
	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
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
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/chat")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from cohere provider: %s", string(resp.Body())))

		var errorResp CohereError

		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, bifrostErr
	}

	// Read response body
	responseBody := resp.Body()

	// Create response object from pool
	response := acquireCohereResponse()
	defer releaseCohereResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Transform tool calls if present
	var toolCalls []schemas.ToolCall
	if response.ToolCalls != nil {
		for _, tool := range response.ToolCalls {
			function := schemas.FunctionCall{
				Name: &tool.Name,
			}

			args, err := sonic.Marshal(tool.Parameters)
			if err != nil {
				function.Arguments = fmt.Sprintf("%v", tool.Parameters)
			} else {
				function.Arguments = string(args)
			}

			toolCalls = append(toolCalls, schemas.ToolCall{
				Function: function,
			})
		}
	}

	// Get role and content from the last message in chat history
	var role schemas.ModelChatMessageRole
	var content string
	if len(response.ChatHistory) > 0 {
		lastMsg := response.ChatHistory[len(response.ChatHistory)-1]
		role = lastMsg.Role
		content = lastMsg.Message
	} else {
		role = schemas.ModelChatMessageRoleChatbot
		content = response.Text
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		ID: response.GenerationID,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: role,
						Content: schemas.MessageContent{
							ContentStr: &content,
						},
						AssistantMessage: &schemas.AssistantMessage{
							ToolCalls: &toolCalls,
						},
					},
				},
				FinishReason: &response.FinishReason,
			},
		},
		Usage: &schemas.LLMUsage{
			PromptTokens:     int(response.Meta.Tokens.InputTokens),
			CompletionTokens: int(response.Meta.Tokens.OutputTokens),
			TotalTokens:      int(response.Meta.Tokens.InputTokens + response.Meta.Tokens.OutputTokens),
		},
		Model: model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Cohere,
			BilledUsage: &schemas.BilledLLMUsage{
				PromptTokens:     float64Ptr(response.Meta.BilledUnits.InputTokens),
				CompletionTokens: float64Ptr(response.Meta.BilledUnits.OutputTokens),
			},
			ChatHistory: convertChatHistory(response.ChatHistory),
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

// prepareCohereChatRequest prepares the request body for Cohere chat completion requests.
// It transforms the messages into Cohere format and handles tools, parameters, and content formatting.
func prepareCohereChatRequest(messages []schemas.BifrostMessage, params *schemas.ModelParameters, model string, stream bool) (map[string]interface{}, error) {
	// Get the last message and chat history
	lastMessage := messages[len(messages)-1]
	chatHistory := messages[:len(messages)-1]

	// Transform chat history
	var cohereHistory []map[string]interface{}
	for _, msg := range chatHistory {
		historyMsg := map[string]interface{}{
			"role": msg.Role,
		}

		if msg.Role == schemas.ModelChatMessageRoleAssistant {
			if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
				var toolCalls []map[string]interface{}
				for _, toolCall := range *msg.AssistantMessage.ToolCalls {
					var arguments map[string]interface{}
					var parsedJSON interface{}
					err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &parsedJSON)
					if err == nil {
						if arr, ok := parsedJSON.(map[string]interface{}); ok {
							arguments = arr
						} else {
							arguments = map[string]interface{}{"content": parsedJSON}
						}
					} else {
						arguments = map[string]interface{}{"content": toolCall.Function.Arguments}
					}

					toolCalls = append(toolCalls, map[string]interface{}{
						"name":       toolCall.Function.Name,
						"parameters": arguments,
					})
				}
				historyMsg["tool_calls"] = toolCalls
			}
		} else if msg.Role == schemas.ModelChatMessageRoleTool {
			// Find the original tool call parameters from conversation history
			var toolCallParameters map[string]interface{}

			// Look back through the chat history to find the assistant message with the matching tool call
			for i := len(chatHistory) - 1; i >= 0; i-- {
				prevMsg := chatHistory[i]
				if prevMsg.Role == schemas.ModelChatMessageRoleAssistant &&
					prevMsg.AssistantMessage != nil &&
					prevMsg.AssistantMessage.ToolCalls != nil {

					// Search through tool calls in this assistant message
					for _, toolCall := range *prevMsg.AssistantMessage.ToolCalls {
						if toolCall.ID != nil && msg.ToolMessage != nil && msg.ToolMessage.ToolCallID != nil &&
							*toolCall.ID == *msg.ToolMessage.ToolCallID {

							// Found the matching tool call, extract its parameters
							var parsedJSON interface{}
							err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &parsedJSON)
							if err == nil {
								if arr, ok := parsedJSON.(map[string]interface{}); ok {
									toolCallParameters = arr
								} else {
									toolCallParameters = map[string]interface{}{"content": parsedJSON}
								}
							} else {
								toolCallParameters = map[string]interface{}{"content": toolCall.Function.Arguments}
							}
							break
						}
					}

					// If we found the parameters, stop searching
					if toolCallParameters != nil {
						break
					}
				}
			}

			// If no parameters found, use empty map as fallback
			if toolCallParameters == nil {
				toolCallParameters = map[string]interface{}{}
			}

			toolResults := []map[string]interface{}{
				{
					"call": map[string]interface{}{
						"name":       *msg.ToolMessage.ToolCallID,
						"parameters": toolCallParameters,
					},
					"outputs": *msg.Content.ContentStr,
				},
			}

			historyMsg["tool_results"] = toolResults
		}

		if msg.Content.ContentStr != nil {
			historyMsg["message"] = *msg.Content.ContentStr
		} else if msg.Content.ContentBlocks != nil {
			// Create content array with text and image
			contentArray := []map[string]interface{}{}

			// Iterate over ContentBlocks to build the content array
			for _, block := range *msg.Content.ContentBlocks {
				if block.Text != nil {
					contentArray = append(contentArray, map[string]interface{}{
						"type": "text",
						"text": *block.Text,
					})
				}
				// Add image content using our helper function
				// NOTE: Cohere v1 does not support image content
				// if processedImageContent := processImageContent(block.ImageContent); processedImageContent != nil {
				// 	contentArray = append(contentArray, processedImageContent)
				// }
			}

			historyMsg["content"] = contentArray
		}

		cohereHistory = append(cohereHistory, historyMsg)
	}

	preparedParams := prepareParams(params)

	// Prepare request body
	requestBody := mergeConfig(map[string]interface{}{
		"chat_history": cohereHistory,
		"model":        model,
	}, preparedParams)

	// Add stream parameter if streaming
	if stream {
		requestBody["stream"] = true
	}

	if lastMessage.Content.ContentStr != nil {
		requestBody["message"] = *lastMessage.Content.ContentStr
	} else if lastMessage.Content.ContentBlocks != nil {
		message := ""
		for _, block := range *lastMessage.Content.ContentBlocks {
			if block.Text != nil {
				message += *block.Text + "\n"
			}
		}
		requestBody["message"] = strings.TrimSuffix(message, "\n")
	}

	// Add tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		var tools []CohereTool
		for _, tool := range *params.Tools {
			parameterDefinitions := make(map[string]CohereParameterDefinition)
			params := tool.Function.Parameters
			for name, prop := range tool.Function.Parameters.Properties {
				propMap, ok := prop.(map[string]interface{})
				if ok {
					paramDef := CohereParameterDefinition{
						Required: slices.Contains(params.Required, name),
					}

					if typeStr, ok := propMap["type"].(string); ok {
						paramDef.Type = typeStr
					}

					if desc, ok := propMap["description"].(string); ok {
						paramDef.Description = &desc
					}

					parameterDefinitions[name] = paramDef
				}
			}

			tools = append(tools, CohereTool{
				Name:                 tool.Function.Name,
				Description:          tool.Function.Description,
				ParameterDefinitions: parameterDefinitions,
			})
		}
		requestBody["tools"] = tools
	}

	// Add tool choice if present
	if params != nil && params.ToolChoice != nil {
		if params.ToolChoice.ToolChoiceStr != nil {
			requestBody["tool_choice"] = *params.ToolChoice.ToolChoiceStr
		} else if params.ToolChoice.ToolChoiceStruct != nil {
			requestBody["tool_choice"] = map[string]interface{}{
				"type": strings.ToUpper(string(params.ToolChoice.ToolChoiceStruct.Type)),
			}
		}
	}

	return requestBody, nil
}

// processImageContent processes image content for Cohere API format.
// NOTE: Cohere v1 does not support image content, so this function is a placeholder.
// It returns nil since image processing is not available.
func processImageContent(imageContent *schemas.ImageURLStruct) map[string]interface{} {
	if imageContent == nil {
		return nil
	}

	// Cohere v1 does not support image content
	// Return nil to skip image processing
	return nil
}

// convertChatHistory converts Cohere's chat history format to Bifrost's format for standardization.
// It transforms the chat history messages and their tool calls.
func convertChatHistory(history []struct {
	Role      schemas.ModelChatMessageRole `json:"role"`
	Message   string                       `json:"message"`
	ToolCalls []CohereToolCall             `json:"tool_calls"`
}) *[]schemas.BifrostMessage {
	converted := make([]schemas.BifrostMessage, len(history))
	for i, msg := range history {
		var toolCalls []schemas.ToolCall
		if msg.ToolCalls != nil {
			for _, tool := range msg.ToolCalls {
				function := schemas.FunctionCall{
					Name: &tool.Name,
				}

				args, err := sonic.Marshal(tool.Parameters)
				if err != nil {
					function.Arguments = fmt.Sprintf("%v", tool.Parameters)
				} else {
					function.Arguments = string(args)
				}

				toolCalls = append(toolCalls, schemas.ToolCall{
					Function: function,
				})
			}
		}

		converted[i] = schemas.BifrostMessage{
			Role: msg.Role,
			Content: schemas.MessageContent{
				ContentStr: &msg.Message,
			},
			AssistantMessage: &schemas.AssistantMessage{
				ToolCalls: &toolCalls,
			},
		}
	}
	return &converted
}

// Embedding generates embeddings for the given input text(s) using the Cohere API.
// Supports Cohere's embedding models and returns a BifrostResponse containing the embedding(s).
func (provider *CohereProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Prepare request body with default values
	requestBody := map[string]interface{}{
		"texts":           input.Texts,
		"model":           model,
		"input_type":      "search_document", // Default input type - can be overridden via ExtraParams
		"embedding_types": []string{"float"}, // Default to float embeddings
	}

	// Apply additional parameters if provided
	if params != nil {
		// Validate encoding format - Cohere API supports float, int8, uint8, binary, ubinary, but our provider only implements float
		if params.EncodingFormat != nil {
			if *params.EncodingFormat != "float" {
				return nil, newConfigurationError(fmt.Sprintf("Cohere provider currently only supports 'float' encoding format, received: %s", *params.EncodingFormat), schemas.Cohere)
			}
			// Override default with the specified format
			requestBody["embedding_types"] = []string{*params.EncodingFormat}
		}

		// Merge extra parameters - this allows overriding input_type and other parameters
		if params.ExtraParams != nil {
			for k, v := range params.ExtraParams {
				requestBody[k] = v
			}
		}
	}

	// Marshal request body
	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Cohere)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v2/embed")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from cohere embedding provider: %s", string(resp.Body())))

		var errorResp CohereError
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, bifrostErr
	}

	// Parse response
	var cohereResp CohereEmbeddingResponse
	if err := sonic.Unmarshal(resp.Body(), &cohereResp); err != nil {
		return nil, newBifrostOperationError("error parsing Cohere embedding response", err, schemas.Cohere)
	}

	// Parse raw response for consistent format
	var rawResponse interface{}
	if err := sonic.Unmarshal(resp.Body(), &rawResponse); err != nil {
		return nil, newBifrostOperationError("error parsing raw response for Cohere embedding", err, schemas.Cohere)
	}

	// Calculate token usage approximation (since Cohere doesn't provide this for embeddings)
	totalInputTokens := approximateTokenCount(input.Texts)

	// Create BifrostResponse
	bifrostResponse := &schemas.BifrostResponse{
		ID:     cohereResp.ID,
		Object: "list",
		Data: []schemas.BifrostEmbedding{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.BifrostEmbeddingResponse{
					Embedding2DArray: &cohereResp.Embeddings.Float,
				},
			},
		},
		Model: model,
		Usage: &schemas.LLMUsage{
			PromptTokens: totalInputTokens,
			TotalTokens:  totalInputTokens,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Cohere,
			RawResponse: rawResponse,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Cohere API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *CohereProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Prepare request body using shared function
	requestBody, err := prepareCohereChatRequest(messages, params, model, true)
	if err != nil {
		return nil, newBifrostOperationError("failed to prepare Cohere chat request", err, schemas.Cohere)
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Cohere)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/v1/chat", strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, newBifrostOperationError("failed to create HTTP request", err, schemas.Cohere)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Make the request
	resp, err := provider.streamClient.Do(req)
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
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from Cohere: %d", resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, schemas.Cohere, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		var responseID string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData := strings.TrimPrefix(line, "data: ")

				// Parse the streaming event
				var streamEvent map[string]interface{}
				if err := sonic.Unmarshal([]byte(jsonData), &streamEvent); err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse Cohere stream event: %v", err))
					continue
				}

				eventType, exists := streamEvent["event_type"].(string)
				if !exists {
					continue
				}

				switch eventType {
				case "stream-start":
					var startEvent CohereStreamStartEvent
					if err := sonic.Unmarshal([]byte(jsonData), &startEvent); err != nil {
						provider.logger.Warn(fmt.Sprintf("Failed to parse Cohere stream-start event: %v", err))
						continue
					}

					responseID = startEvent.GenerationID

					// Send empty message to signal stream start
					streamResponse := &schemas.BifrostResponse{
						ID:     responseID,
						Object: "chat.completion.chunk",
						Model:  model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,

								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{
										Role: StrPtr(string(schemas.ModelChatMessageRoleAssistant)),
									},
								},
							},
						},
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider: schemas.Cohere,
						},
					}

					if params != nil {
						streamResponse.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan)

				case "text-generation":
					var textEvent CohereStreamTextEvent
					if err := sonic.Unmarshal([]byte(jsonData), &textEvent); err != nil {
						provider.logger.Warn(fmt.Sprintf("Failed to parse Cohere text-generation event: %v", err))
						continue
					}

					// Create response for this text chunk
					response := &schemas.BifrostResponse{
						ID:     responseID,
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{
										Content: &textEvent.Text,
									},
								},
								FinishReason: nil, // Not finished yet
							},
						},
						Model: model,
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider: schemas.Cohere,
						},
					}

					if params != nil {
						response.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, response, responseChan)

				case "tool-calls-chunk":
					var toolEvent CohereStreamToolCallEvent
					if err := sonic.Unmarshal([]byte(jsonData), &toolEvent); err != nil {
						provider.logger.Warn(fmt.Sprintf("Failed to parse Cohere tool-use event: %v", err))
						continue
					}

					toolCall := schemas.ToolCall{
						ID: &toolEvent.ToolCall.ID,
						Function: schemas.FunctionCall{
							Name:      &toolEvent.ToolCall.ID,
							Arguments: toolEvent.ToolCall.Parameters,
						},
					}

					// Create response for tool calls
					response := &schemas.BifrostResponse{
						ID:     responseID,
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{
										ToolCalls: []schemas.ToolCall{toolCall},
										Content:   toolEvent.Text,
									},
								},
								FinishReason: nil,
							},
						},
						Model: model,
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider: schemas.Cohere,
						},
					}

					if params != nil {
						response.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, response, responseChan)

				case "stream-end":
					var stopEvent CohereStreamStopEvent
					if err := sonic.Unmarshal([]byte(jsonData), &stopEvent); err != nil {
						provider.logger.Warn(fmt.Sprintf("Failed to parse Cohere stream-end event: %v", err))
						continue
					}

					// Convert tool calls from the final response
					var toolCalls []schemas.ToolCall
					for _, toolCall := range stopEvent.Response.ToolCalls {
						function := schemas.FunctionCall{
							Name: &toolCall.Name,
						}

						args, err := sonic.Marshal(toolCall.Parameters)
						if err != nil {
							function.Arguments = fmt.Sprintf("%v", toolCall.Parameters)
						} else {
							function.Arguments = string(args)
						}

						toolCalls = append(toolCalls, schemas.ToolCall{
							Function: function,
						})
					}

					// Send final response with complete content from the stopEvent
					response := &schemas.BifrostResponse{
						ID:     responseID,
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
									Delta: schemas.BifrostStreamDelta{
										Role:      StrPtr(string(schemas.ModelChatMessageRoleAssistant)),
										Content:   &stopEvent.Response.Text,
										ToolCalls: toolCalls,
									},
								},
								FinishReason: &stopEvent.Response.FinishReason,
							},
						},
						Model: model,
						ExtraFields: schemas.BifrostResponseExtraFields{
							Provider: schemas.Cohere,
						},
					}

					if params != nil {
						response.ExtraFields.Params = *params
					}

					// Use utility function to process and send response
					processAndSendResponse(ctx, postHookRunner, response, responseChan)

					return // End of stream

				default:
					// Unknown event type, log and continue
					provider.logger.Debug(fmt.Sprintf("Unknown Cohere stream event type: %s", eventType))
				}
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading Cohere stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan)
		}
	}()

	return responseChan, nil
}

func (provider *CohereProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "cohere")
}

func (provider *CohereProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "cohere")
}

func (provider *CohereProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "cohere")
}

func (provider *CohereProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "cohere")
}
