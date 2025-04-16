// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenAI provider implementation.
package providers

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/maxim-go"
	"github.com/valyala/fasthttp"
)

// Counters for pool usage
var (
	openAIPoolGets      atomic.Int64
	openAIPoolPuts      atomic.Int64
	openAIPoolCreations atomic.Int64

	bifrostPoolGets      atomic.Int64
	bifrostPoolPuts      atomic.Int64
	bifrostPoolCreations atomic.Int64
)

// GetPoolStats returns the current pool usage statistics
func GetPoolStats() map[string]int64 {
	return map[string]int64{
		"openai_pool_gets":       openAIPoolGets.Load(),
		"openai_pool_puts":       openAIPoolPuts.Load(),
		"openai_pool_creations":  openAIPoolCreations.Load(),
		"bifrost_pool_gets":      bifrostPoolGets.Load(),
		"bifrost_pool_puts":      bifrostPoolPuts.Load(),
		"bifrost_pool_creations": bifrostPoolCreations.Load(),
	}
}

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

// RequestMetrics contains timing and size metrics for API requests
type RequestMetrics struct {
	// Timing metrics
	MessageFormatting      time.Duration `json:"message_formatting"`
	ParamsPreparation      time.Duration `json:"params_preparation"`
	RequestBodyPreparation time.Duration `json:"request_body_preparation"`
	JSONMarshaling         time.Duration `json:"json_marshaling"`
	RequestSetup           time.Duration `json:"request_setup"`
	HTTPRequest            time.Duration `json:"http_request"`
	ErrorHandling          time.Duration `json:"error_handling"`
	ResponseParsing        time.Duration `json:"response_parsing"`

	// Size metrics
	RequestSizeInBytes  int `json:"request_size_in_bytes"`
	ResponseSizeInBytes int `json:"response_size_in_bytes"`
}

// openAIResponsePool provides a pool for OpenAI response objects.
var openAIResponsePool = sync.Pool{
	New: func() interface{} {
		openAIPoolCreations.Add(1)
		return &OpenAIResponse{}
	},
}

// acquireOpenAIResponse gets an OpenAI response from the pool and resets it.
func acquireOpenAIResponse() *OpenAIResponse {
	openAIPoolGets.Add(1)
	resp := openAIResponsePool.Get().(*OpenAIResponse)
	*resp = OpenAIResponse{} // Reset the struct
	return resp
}

// releaseOpenAIResponse returns an OpenAI response to the pool.
func releaseOpenAIResponse(resp *OpenAIResponse) {
	if resp != nil {
		openAIPoolPuts.Add(1)
		openAIResponsePool.Put(resp)
	}
}

// OpenAIProvider implements the Provider interface for OpenAI's API.
type OpenAIProvider struct {
	MockResponse bool
	logger       schemas.Logger   // Logger for provider operations
	client       *fasthttp.Client // HTTP client for API requests
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
		MockResponse: true,
		logger:       logger,
		client:       client,
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
	metrics := RequestMetrics{}

	// Track message formatting time
	formatStart := time.Now()

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

	metrics.MessageFormatting = time.Since(formatStart)
	paramsStart := time.Now()
	preparedParams := prepareParams(params)
	metrics.ParamsPreparation = time.Since(paramsStart)

	bodyStart := time.Now()
	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)
	metrics.RequestBodyPreparation = time.Since(bodyStart)

	// Track JSON marshaling time
	marshalStart := time.Now()
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
	metrics.JSONMarshaling = time.Since(marshalStart)

	// Track request size in bytes
	metrics.RequestSizeInBytes = len(jsonBody)

	// Create request
	setupStart := time.Now()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("https://api.openai.com/v1/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.SetBody(jsonBody)

	metrics.RequestSetup = time.Since(setupStart)

	// Track HTTP request time
	httpStart := time.Now()

	var shouldMakeRealCall bool = true
	if provider.MockResponse {
		// Try mock response first
		if mockResponse := mockOpenAIChatCompletionResponse(req, model); mockResponse != nil {
			// Copy the mock response body to the real response
			resp.SetBody(mockResponse)
			// Simulate network delay
			jitter := time.Duration(float64(1500*time.Millisecond) * (0.6 + 0.8*rand.Float64()))
			time.Sleep(jitter)
			shouldMakeRealCall = false
		} else {
			// Log that we're falling back to real API call due to mock failure
			provider.logger.Debug("Mock response generation failed, falling back to real API call")
		}
	}

	if shouldMakeRealCall {
		// Make the real API call
		if err := provider.client.Do(req, resp); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: schemas.ErrorField{
					Message: schemas.ErrProviderRequest,
					Error:   err,
				},
			}
		}
	}

	metrics.HTTPRequest = time.Since(httpStart)

	// Track response size in bytes
	responseBody := resp.Body()
	metrics.ResponseSizeInBytes = len(responseBody)

	// Track error handling time
	errorStart := time.Now()

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp OpenAIError

		bifrostErr := handleProviderAPIError(resp, &errorResp)

		bifrostErr.EventID = &errorResp.EventID
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Code = &errorResp.Error.Code
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Param = errorResp.Error.Param
		bifrostErr.Error.EventID = &errorResp.Error.EventID

		return nil, bifrostErr
	}
	metrics.ErrorHandling = time.Since(errorStart)

	parseStart := time.Now()

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

	metrics.ResponseParsing = time.Since(parseStart)

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
		Provider: schemas.OpenAI,
		RawResponse: map[string]interface{}{
			"response":         rawResponse,
			"provider_metrics": metrics,
		},
	}

	return result, nil
}

// mockOpenAIResponse creates a mock response for OpenAI API calls
func mockOpenAIChatCompletionResponse(req *fasthttp.Request, model string) []byte {
	baseMessage := "This is a mock response from the Bifrost API gateway. The actual API was not called."
	// Calculate how many times to repeat to reach ~10kb
	repeatCount := (10*1024)/len(baseMessage) + 1
	repeatedMessage := strings.Repeat(baseMessage, repeatCount)

	// Create a mock response that mimics OpenAI's format
	mockResp := &OpenAIResponse{
		ID:      "mock-" + model + "-" + fmt.Sprintf("%d", time.Now().Unix()),
		Object:  "chat.completion",
		Model:   model,
		Created: int(time.Now().Unix()),
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				Message: schemas.BifrostResponseChoiceMessage{
					Role:    schemas.RoleAssistant,
					Content: maxim.StrPtr(repeatedMessage),
				},
				FinishReason: maxim.StrPtr("stop"),
			},
		},
		Usage: schemas.LLMUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	// Convert to JSON
	mockJSON, err := json.Marshal(mockResp)
	if err != nil {
		return nil
	}

	return mockJSON
}
