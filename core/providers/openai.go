// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenAI provider implementation.
package providers

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// OpenAIResponse represents the response structure from the OpenAI API.
// It includes completion choices, model information, and usage statistics.
type OpenAIResponse struct {
	ID      string                          `json:"id"`      // Unique identifier for the completion
	Object  string                          `json:"object"`  // Type of completion (text.completion, chat.completion, or embedding)
	Choices []schemas.BifrostResponseChoice `json:"choices"` // Array of completion choices
	Data    []struct {                      // Embedding data
		Object    string `json:"object"`
		Embedding any    `json:"embedding"`
		Index     int    `json:"index"`
	} `json:"data,omitempty"`
	Model             string           `json:"model"`              // Model used for the completion
	Created           int              `json:"created"`            // Unix timestamp of completion creation
	ServiceTier       *string          `json:"service_tier"`       // Service tier used for the request
	SystemFingerprint *string          `json:"system_fingerprint"` // System fingerprint for the request
	Usage             schemas.LLMUsage `json:"usage"`              // Token usage statistics
}

// OpenAIError represents the error response structure from the OpenAI API.
// It includes detailed error information and event tracking.
type OpenAIError struct {
	EventID string `json:"event_id"` // Unique identifier for the error event
	Type    string `json:"type"`     // Type of error
	Error   struct {
		Type    string      `json:"type"`     // Error type
		Code    string      `json:"code"`     // Error code
		Message string      `json:"message"`  // Error message
		Param   interface{} `json:"param"`    // Parameter that caused the error
		EventID string      `json:"event_id"` // Event ID for tracking
	} `json:"error"`
}

// openAIResponsePool provides a pool for OpenAI response objects.
var openAIResponsePool = sync.Pool{
	New: func() interface{} {
		return &OpenAIResponse{}
	},
}

// acquireOpenAIResponse gets an OpenAI response from the pool and resets it.
func acquireOpenAIResponse() *OpenAIResponse {
	resp := openAIResponsePool.Get().(*OpenAIResponse)
	*resp = OpenAIResponse{} // Reset the struct
	return resp
}

// releaseOpenAIResponse returns an OpenAI response to the pool.
func releaseOpenAIResponse(resp *OpenAIResponse) {
	if resp != nil {
		openAIResponsePool.Put(resp)
	}
}

// OpenAIProvider implements the Provider interface for OpenAI's GPT API.
type OpenAIProvider struct {
	logger        schemas.Logger        // Logger for provider operations
	client        *fasthttp.Client      // HTTP client for API requests
	streamClient  *http.Client          // HTTP client for streaming requests
	networkConfig schemas.NetworkConfig // Network configuration including extra headers
}

// NewOpenAIProvider creates a new OpenAI provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewOpenAIProvider(config *schemas.ProviderConfig, logger schemas.Logger) *OpenAIProvider {
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
		openAIResponsePool.Put(&OpenAIResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.openai.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &OpenAIProvider{
		logger:        logger,
		client:        client,
		streamClient:  streamClient,
		networkConfig: config.NetworkConfig,
	}
}

// GetProviderKey returns the provider identifier for OpenAI.
func (provider *OpenAIProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.OpenAI
}

// TextCompletion is not supported by the OpenAI provider.
// Returns an error indicating that text completion is not available.
func (provider *OpenAIProvider) TextCompletion(ctx context.Context, model, key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "openai")
}

// ChatCompletion performs a chat completion request to the OpenAI API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *OpenAIProvider) ChatCompletion(ctx context.Context, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

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

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from openai provider: %s", string(resp.Body())))

		var errorResp OpenAIError

		bifrostErr := handleProviderAPIError(resp, &errorResp)

		if errorResp.EventID != "" {
			bifrostErr.EventID = &errorResp.EventID
		}
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Code = &errorResp.Error.Code
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != "" {
			bifrostErr.Error.EventID = &errorResp.Error.EventID
		}

		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	response := acquireOpenAIResponse()
	defer releaseOpenAIResponse(response)

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		ID:                response.ID,
		Object:            response.Object,
		Choices:           response.Choices,
		Model:             response.Model,
		Created:           response.Created,
		ServiceTier:       response.ServiceTier,
		SystemFingerprint: response.SystemFingerprint,
		Usage:             &response.Usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.OpenAI,
			RawResponse: rawResponse,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

func prepareOpenAIChatRequest(messages []schemas.BifrostMessage, params *schemas.ModelParameters) ([]map[string]interface{}, map[string]interface{}) {
	// Format messages for OpenAI API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		if msg.Role == schemas.ModelChatMessageRoleAssistant {
			assistantMessage := map[string]interface{}{
				"role":    msg.Role,
				"content": msg.Content,
			}
			if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
				assistantMessage["tool_calls"] = *msg.AssistantMessage.ToolCalls
			}
			formattedMessages = append(formattedMessages, assistantMessage)
		} else {
			message := map[string]interface{}{
				"role": msg.Role,
			}

			if msg.Content.ContentStr != nil {
				message["content"] = *msg.Content.ContentStr
			} else if msg.Content.ContentBlocks != nil {
				contentBlocks := *msg.Content.ContentBlocks
				for i := range contentBlocks {
					if contentBlocks[i].Type == schemas.ContentBlockTypeImage && contentBlocks[i].ImageURL != nil {
						sanitizedURL, _ := SanitizeImageURL(contentBlocks[i].ImageURL.URL)
						contentBlocks[i].ImageURL.URL = sanitizedURL
					}
				}

				message["content"] = contentBlocks
			}

			if msg.ToolMessage != nil && msg.ToolMessage.ToolCallID != nil {
				message["tool_call_id"] = *msg.ToolMessage.ToolCallID
			}

			formattedMessages = append(formattedMessages, message)
		}
	}

	preparedParams := prepareParams(params)

	return formattedMessages, preparedParams
}

// Embedding generates embeddings for the given input text(s).
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *OpenAIProvider) Embedding(ctx context.Context, model string, key string, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Validate input texts are not empty
	if len(input.Texts) == 0 {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "input texts cannot be empty",
			},
		}
	}

	// Prepare request body with base parameters
	requestBody := map[string]interface{}{
		"model": model,
		"input": input.Texts,
	}

	// Merge any additional parameters
	if params != nil {
		// Map standard parameters
		if params.EncodingFormat != nil {
			requestBody["encoding_format"] = *params.EncodingFormat
		}
		if params.Dimensions != nil {
			requestBody["dimensions"] = *params.Dimensions
		}
		if params.User != nil {
			requestBody["user"] = *params.User
		}

		// Merge any extra parameters
		requestBody = mergeConfig(requestBody, params.ExtraParams)
	}

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

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/embeddings")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from openai provider: %s", string(resp.Body())))

		var errorResp OpenAIError

		bifrostErr := handleProviderAPIError(resp, &errorResp)

		if errorResp.EventID != "" {
			bifrostErr.EventID = &errorResp.EventID
		}
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Code = &errorResp.Error.Code
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != "" {
			bifrostErr.Error.EventID = &errorResp.Error.EventID
		}

		return nil, bifrostErr
	}

	// Parse response
	var response OpenAIResponse
	if err := json.Unmarshal(resp.Body(), &response); err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   err,
			},
		}
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		ID:                response.ID,
		Object:            response.Object,
		Model:             response.Model,
		Created:           response.Created,
		Usage:             &response.Usage,
		ServiceTier:       response.ServiceTier,
		SystemFingerprint: response.SystemFingerprint,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.OpenAI,
		},
	}

	// Extract embeddings from response data
	if len(response.Data) > 0 {
		embeddings := make([][]float32, len(response.Data))
		for i, data := range response.Data {
			switch v := data.Embedding.(type) {
			case []float32:
				embeddings[i] = v
			case []interface{}:
				// Convert []interface{} to []float32
				floatArray := make([]float32, len(v))
				for j := range v {
					if num, ok := v[j].(float64); ok {
						floatArray[j] = float32(num)
					} else {
						return nil, &schemas.BifrostError{
							IsBifrostError: true,
							Error: schemas.ErrorField{
								Message: fmt.Sprintf("unsupported number type in embedding array: %T", v[j]),
							},
						}
					}
				}
				embeddings[i] = floatArray
			case string:
				// Decode base64 string into float32 array
				decodedData, err := base64.StdEncoding.DecodeString(v)
				if err != nil {
					return nil, &schemas.BifrostError{
						IsBifrostError: true,
						Error: schemas.ErrorField{
							Message: "failed to decode base64 embedding",
							Error:   err,
						},
					}
				}

				// Validate that decoded data length is divisible by 4 (size of float32)
				const sizeOfFloat32 = 4
				if len(decodedData)%sizeOfFloat32 != 0 {
					return nil, &schemas.BifrostError{
						IsBifrostError: true,
						Error: schemas.ErrorField{
							Message: "malformed base64 embedding data: length not divisible by 4",
						},
					}
				}

				floats := make([]float32, len(decodedData)/sizeOfFloat32)
				for i := 0; i < len(floats); i++ {
					floats[i] = math.Float32frombits(binary.LittleEndian.Uint32(decodedData[i*4 : (i+1)*4]))
				}
				embeddings[i] = floats
			default:
				return nil, &schemas.BifrostError{
					IsBifrostError: true,
					Error: schemas.ErrorField{
						Message: fmt.Sprintf("unsupported embedding type: %T", data.Embedding),
					},
				}
			}
		}
		bifrostResponse.Embedding = embeddings
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

func (provider *OpenAIProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + key,
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Use shared streaming logic
	return handleOpenAIStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.OpenAI,
		params,
		postHookRunner,
		provider.logger,
	)
}

// performOpenAICompatibleStreaming handles streaming for OpenAI-compatible APIs (OpenAI, Azure).
// This shared function reduces code duplication between providers that use the same SSE format.
func handleOpenAIStreaming(
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

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
			}

			var jsonData string
			var isDataLine bool

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData = strings.TrimPrefix(line, "data: ")
				isDataLine = true
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
				isDataLine = false
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			var errorCheck map[string]interface{}
			if err := json.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				var openAIError OpenAIError
				if err := json.Unmarshal([]byte(jsonData), &openAIError); err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}

				// Send error through channel
				errorResponse := &schemas.BifrostStream{
					BifrostError: &schemas.BifrostError{
						IsBifrostError: false,
						Error: schemas.ErrorField{
							Type:    &openAIError.Error.Type,
							Code:    &openAIError.Error.Code,
							Message: openAIError.Error.Message,
							Param:   openAIError.Error.Param,
						},
					},
				}

				if openAIError.EventID != "" {
					errorResponse.BifrostError.EventID = &openAIError.EventID
				}
				if openAIError.Error.EventID != "" {
					errorResponse.BifrostError.Error.EventID = &openAIError.Error.EventID
				}

				select {
				case responseChan <- errorResponse:
				case <-ctx.Done():
				}
				return // Stop processing on error
			}

			// Only process as regular response if it's a proper data line
			if !isDataLine {
				logger.Warn(fmt.Sprintf("Received non-data line that's not an error: %s", line))
				continue
			}

			// Parse into bifrost response
			var response schemas.BifrostResponse
			if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			// Handle usage-only chunks (when stream_options include_usage is true)
			if len(response.Choices) == 0 && response.Usage != nil {
				// This is a usage information chunk at the end of stream
				if params != nil {
					response.ExtraFields.Params = *params
				}
				response.ExtraFields.Provider = providerType

				ProcessAndSendResponse(ctx, postHookRunner, &response, responseChan)
				continue
			}

			// Skip empty responses or responses without choices
			if len(response.Choices) == 0 {
				continue
			}

			// Handle finish reason in the final chunk
			choice := response.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				// This is the final chunk with finish reason
				if params != nil {
					response.ExtraFields.Params = *params
				}
				response.ExtraFields.Provider = providerType

				ProcessAndSendResponse(ctx, postHookRunner, &response, responseChan)

				// End stream processing after finish reason
				break
			}

			// Handle regular content chunks
			if choice.Delta.Content != nil || len(choice.Delta.ToolCalls) > 0 {
				if params != nil {
					response.ExtraFields.Params = *params
				}
				response.ExtraFields.Provider = providerType

				ProcessAndSendResponse(ctx, postHookRunner, &response, responseChan)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))

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
