// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenAI provider implementation.
package providers

import (
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
	Object            string                          `json:"object"`             // Type of completion (text.completion or chat.completion)
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
	setConfigDefaults(config)

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

	// Make request
	if err := provider.client.Do(req, resp); err != nil {
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
