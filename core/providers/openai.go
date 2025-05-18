// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenAI provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"fmt"
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
	ID                string                          `json:"id"`                 // Unique identifier for the completion
	Object            string                          `json:"object"`             // Type of completion (text.completion or chat.completion) or text.completion.chunk or chat.completion.chunk
	Choices           []schemas.BifrostResponseChoice `json:"choices"`            // Array of completion choices
	Model             string                          `json:"model"`              // Model used for the completion
	Created           int                             `json:"created"`            // Unix timestamp of completion creation
	ServiceTier       *string                         `json:"service_tier"`       // Service tier used for the request
	SystemFingerprint *string                         `json:"system_fingerprint"` // System fingerprint for the request
	Usage             schemas.LLMUsage                `json:"usage"`              // Token usage statistics
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

// OpenAIProvider implements the Provider interface for OpenAI's API.
type OpenAIProvider struct {
	logger schemas.Logger   // Logger for provider operations
	client *fasthttp.Client // HTTP client for API requests
}

// NewOpenAIProvider creates a new OpenAI provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewOpenAIProvider(config *schemas.ProviderConfig, logger schemas.Logger) *OpenAIProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		openAIResponsePool.Put(&OpenAIResponse{})
		bifrostResponsePool.Put(&schemas.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &OpenAIProvider{
		logger: logger,
		client: client,
	}
}

// GetProviderKey returns the provider identifier for OpenAI.
func (provider *OpenAIProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.OpenAI
}

// TextCompletion is not supported by the OpenAI provider.
// Returns an error indicating that text completion is not available.
func (provider *OpenAIProvider) TextCompletion(model, key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: "text completion is not supported by openai provider",
		},
	}
}

// ChatCompletion performs a chat completion request to the OpenAI API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *OpenAIProvider) ChatCompletion(model, key string, messages []schemas.Message, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(model, messages, params)

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

	req.SetRequestURI("https://api.openai.com/v1/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.SetBody(jsonBody)

	// Use the existing client configuration
	if err := provider.client.Do(req, resp); err != nil {
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
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

	result := acquireBifrostResponse()
	defer releaseBifrostResponse(result)

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Populate result from response
	result.ID = response.ID
	result.Choices = response.Choices
	result.Object = response.Object
	result.Usage = response.Usage
	result.ServiceTier = response.ServiceTier
	result.SystemFingerprint = response.SystemFingerprint
	result.Model = response.Model
	result.Created = response.Created
	result.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider:    schemas.OpenAI,
		RawResponse: rawResponse,
	}

	return result, nil
}

// StreamChatCompletion performs a streaming chat completion request to the OpenAI API.
func (provider *OpenAIProvider) StreamChatCompletion(model, key string, messages []schemas.Message, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(model, messages, params)

	// Ensure 'stream: true' is set for streaming requests
	if preparedParams == nil {
		preparedParams = make(map[string]interface{})
	}
	preparedParams["stream"] = true

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

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	// Do not defer ReleaseResponse for streaming, it's handled in the goroutine

	req.SetRequestURI("https://api.openai.com/v1/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "text/event-stream") // Important for SSE
	req.SetBody(jsonBody)

	// Use the existing client configuration
	if err := provider.client.Do(req, resp); err != nil {
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		bodyBytes := resp.Body()
		provider.logger.Debug(fmt.Sprintf("error from openai provider on stream: %s", string(bodyBytes)))
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
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return nil, bifrostErr
	}

	// Create a larger buffered channel for stream chunks
	streamChannel := make(chan schemas.BifrostResponse, 100) //TODO make this configurable
	initialBifrostResponse := acquireBifrostResponse()
	initialBifrostResponse.StreamChannel = streamChannel
	initialBifrostResponse.Object = "chat.completion.chunk"
	initialBifrostResponse.Model = model

	go func() {
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		defer close(streamChannel)

		// Get the response body as a reader
		reader := bufio.NewReader(bytes.NewReader(resp.Body()))

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				provider.logger.Warn(fmt.Sprintf("error reading stream: %v", err))
				continue
			}

			line = strings.TrimSpace(line)
			if line == "" {
				continue // Skip empty lines
			}

			if !strings.HasPrefix(line, "data: ") {
				continue // Skip non-data lines
			}

			dataContent := strings.TrimPrefix(line, "data: ")
			if dataContent == "[DONE]" {
				provider.logger.Debug("Stream finished with [DONE]")
				return // End of stream
			}

			var streamResp OpenAIResponse
			if err := json.Unmarshal([]byte(dataContent), &streamResp); err != nil {
				provider.logger.Error(fmt.Errorf("error unmarshalling stream data chunk: %w. Data: '%s'", err, dataContent))
				continue
			}

			if len(streamResp.Choices) > 0 {
				choice := streamResp.Choices[0]

				bifrostChunk := acquireBifrostResponse()
				bifrostChunk.ID = streamResp.ID
				bifrostChunk.Object = streamResp.Object
				bifrostChunk.Model = streamResp.Model
				bifrostChunk.Created = streamResp.Created
				bifrostChunk.ServiceTier = streamResp.ServiceTier
				bifrostChunk.SystemFingerprint = streamResp.SystemFingerprint

				bifrostChunk.Choices = []schemas.BifrostResponseChoice{
					{
						Index:        choice.Index,
						Delta:        choice.Delta,
						FinishReason: choice.FinishReason,
					},
				}

				bifrostChunk.Usage = streamResp.Usage

				// Add timeout to channel send
				select {
				case streamChannel <- *bifrostChunk:
					// Chunk sent successfully
				case <-time.After(100 * time.Millisecond): //TODO have a better way of handling this
					provider.logger.Warn("Consumer too slow, forcing chunk delivery")
					// Try once more with a blocking send
					streamChannel <- *bifrostChunk
				}
			}
		}
	}()

	return initialBifrostResponse, nil
}

func prepareOpenAIChatRequest(model string, messages []schemas.Message, params *schemas.ModelParameters) ([]map[string]interface{}, map[string]interface{}) {
	// Format messages for OpenAI API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		if msg.ImageContent != nil {
			var content []map[string]interface{}

			// Add text content if present
			if msg.Content != nil {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": msg.Content,
				})
			}

			imageContent := map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]interface{}{
					"url": msg.ImageContent.URL,
				},
			}

			if msg.ImageContent.Detail != nil {
				imageContent["image_url"].(map[string]interface{})["detail"] = msg.ImageContent.Detail
			}

			content = append(content, imageContent)

			formattedMessages = append(formattedMessages, map[string]interface{}{
				"role":    msg.Role,
				"content": content,
			})
		} else {
			formattedMessages = append(formattedMessages, map[string]interface{}{
				"role":    msg.Role,
				"content": msg.Content,
			})
		}
	}

	preparedParams := prepareParams(params)

	return formattedMessages, preparedParams
}
