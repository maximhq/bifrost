package providers

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/maximhq/maxim-go"
	"github.com/valyala/fasthttp"
)

// Pre-defined errors to reduce allocations in error paths
var (
	ErrOpenAIRequest          = fmt.Errorf("error making OpenAI request")
	ErrOpenAIResponse         = fmt.Errorf("OpenAI error response")
	ErrOpenAIJSONMarshaling   = fmt.Errorf("error marshaling OpenAI request")
	ErrOpenAIDecodeStructured = fmt.Errorf("error decoding OpenAI structured response")
	ErrOpenAIDecodeRaw        = fmt.Errorf("error decoding OpenAI raw response")
	ErrOpenAIDecompress       = fmt.Errorf("error decompressing OpenAI response")
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

// OpenAIResponsePool provides a pool for OpenAI response objects
var openAIResponsePool = sync.Pool{
	New: func() interface{} {
		openAIPoolCreations.Add(1)
		return &OpenAIResponse{}
	},
}

// BifrostResponsePool provides a pool for Bifrost response objects
var bifrostResponsePool = sync.Pool{
	New: func() interface{} {
		bifrostPoolCreations.Add(1)
		return &interfaces.BifrostResponse{}
	},
}

// AcquireOpenAIResponse gets an OpenAI response from the pool
func AcquireOpenAIResponse() *OpenAIResponse {
	openAIPoolGets.Add(1)
	resp := openAIResponsePool.Get().(*OpenAIResponse)
	*resp = OpenAIResponse{} // Reset the struct
	return resp
}

// ReleaseOpenAIResponse returns an OpenAI response to the pool
func ReleaseOpenAIResponse(resp *OpenAIResponse) {
	if resp != nil {
		openAIPoolPuts.Add(1)
		openAIResponsePool.Put(resp)
	}
}

// AcquireBifrostResponse gets a Bifrost response from the pool
func AcquireBifrostResponse() *interfaces.BifrostResponse {
	bifrostPoolGets.Add(1)
	resp := bifrostResponsePool.Get().(*interfaces.BifrostResponse)
	*resp = interfaces.BifrostResponse{} // Reset the struct
	return resp
}

// ReleaseBifrostResponse returns a Bifrost response to the pool
func ReleaseBifrostResponse(resp *interfaces.BifrostResponse) {
	if resp != nil {
		bifrostPoolPuts.Add(1)
		bifrostResponsePool.Put(resp)
	}
}

type OpenAIResponse struct {
	ID                string                             `json:"id"`
	Object            string                             `json:"object"` // text.completion or chat.completion
	Choices           []interfaces.BifrostResponseChoice `json:"choices"`
	Model             string                             `json:"model"`
	Created           int                                `json:"created"` // The Unix timestamp (in seconds).
	ServiceTier       *string                            `json:"service_tier"`
	SystemFingerprint *string                            `json:"system_fingerprint"`
	Usage             interfaces.LLMUsage                `json:"usage"`
}

type OpenAIError struct {
	EventID string `json:"event_id"`
	Type    string `json:"type"`
	Error   struct {
		Type    string      `json:"type"`
		Code    string      `json:"code"`
		Message string      `json:"message"`
		Param   interface{} `json:"param"`
		EventID string      `json:"event_id"`
	} `json:"error"`
}

// OpenAIProvider implements the Provider interface for OpenAI
type OpenAIProvider struct {
	MockResponse bool
	logger       interfaces.Logger
	client       *fasthttp.Client
}

// NewOpenAIProvider creates a new OpenAI provider instance
func NewOpenAIProvider(config *interfaces.ProviderConfig, logger interfaces.Logger) *OpenAIProvider {
	// Create the client
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Prewarm pools directly without affecting counters
	for range config.ConcurrencyAndBufferSize.Concurrency {
		openAIResponsePool.Put(&OpenAIResponse{})
		bifrostResponsePool.Put(&interfaces.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &OpenAIProvider{
		MockResponse: false,
		logger:       logger,
		client:       client,
	}
}

func (provider *OpenAIProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.OpenAI
}

// TextCompletion performs text completion
func (provider *OpenAIProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	return nil, &interfaces.BifrostError{
		IsBifrostError: false,
		Error: interfaces.ErrorField{
			Message: "text completion is not supported by openai provider",
		},
	}
}

func (provider *OpenAIProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	timings := make(map[string]time.Duration)

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
	timings["message_formatting"] = time.Since(formatStart)

	// Track params preparation time
	paramsStart := time.Now()
	preparedParams := PrepareParams(params)
	timings["params_preparation"] = time.Since(paramsStart)

	// Track request body preparation time
	bodyStart := time.Now()
	requestBody := MergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)
	timings["request_body_preparation"] = time.Since(bodyStart)

	// Track JSON marshaling time
	marshalStart := time.Now()
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: ErrOpenAIJSONMarshaling.Error(),
				Error:   err,
			},
		}
	}
	timings["json_marshaling"] = time.Since(marshalStart)

	// Track request setup time
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
	timings["request_setup"] = time.Since(setupStart)

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
			return nil, &interfaces.BifrostError{
				IsBifrostError: false,
				Error: interfaces.ErrorField{
					Message: ErrOpenAIRequest.Error(),
					Error:   err,
				},
			}
		}
	}

	timings["http_request"] = time.Since(httpStart)

	// Track error handling time
	errorStart := time.Now()
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp OpenAIError
		if err := json.Unmarshal(resp.Body(), &errorResp); err != nil {
			return nil, &interfaces.BifrostError{
				IsBifrostError: true,
				Error: interfaces.ErrorField{
					Message: ErrOpenAIResponse.Error(),
					Error:   err,
				},
			}
		}

		statusCode := resp.StatusCode()

		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			EventID:        &errorResp.EventID,
			StatusCode:     &statusCode,
			Error: interfaces.ErrorField{
				Type:    &errorResp.Error.Type,
				Code:    &errorResp.Error.Code,
				Message: errorResp.Error.Message,
				Param:   errorResp.Error.Param,
				EventID: &errorResp.Error.EventID,
			},
		}
	}
	timings["error_handling"] = time.Since(errorStart)

	responseBody := resp.Body()

	// Track response parsing time
	parseStart := time.Now()
	// Pre-allocate response structs from pools
	openAIResponse := AcquireOpenAIResponse()
	defer ReleaseOpenAIResponse(openAIResponse)

	result := AcquireBifrostResponse()

	// Parallel Unmarshaling of response
	var wg sync.WaitGroup
	var structuredErr, rawErr error
	var rawResponse interface{}

	wg.Add(2)
	go func() {
		defer wg.Done()
		structuredErr = json.Unmarshal(responseBody, openAIResponse)
	}()
	go func() {
		defer wg.Done()
		rawErr = json.Unmarshal(responseBody, &rawResponse)
	}()
	wg.Wait()

	// Check for unmarshaling errors
	if structuredErr != nil {
		ReleaseBifrostResponse(result)
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: ErrOpenAIDecodeStructured.Error(),
				Error:   structuredErr,
			},
		}
	}
	if rawErr != nil {
		ReleaseBifrostResponse(result)
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: ErrOpenAIDecodeRaw.Error(),
				Error:   rawErr,
			},
		}
	}
	timings["response_parsing"] = time.Since(parseStart)

	// Populate result from response
	result.ID = openAIResponse.ID
	result.Choices = openAIResponse.Choices
	result.Object = openAIResponse.Object
	result.Usage = openAIResponse.Usage
	result.ServiceTier = openAIResponse.ServiceTier
	result.SystemFingerprint = openAIResponse.SystemFingerprint
	result.Created = openAIResponse.Created
	result.Model = openAIResponse.Model
	result.ExtraFields = interfaces.BifrostResponseExtraFields{
		Provider: interfaces.OpenAI,
		RawResponse: map[string]interface{}{
			"response": rawResponse,
			"timings":  timings,
		},
	}

	ReleaseBifrostResponse(result)

	return result, nil
}

// mockOpenAIResponse creates a mock response for OpenAI API calls
func mockOpenAIChatCompletionResponse(req *fasthttp.Request, model string) []byte {
	// Create a mock response that mimics OpenAI's format
	mockResp := &OpenAIResponse{
		ID:      "mock-" + model + "-" + fmt.Sprintf("%d", time.Now().Unix()),
		Object:  "chat.completion",
		Model:   model,
		Created: int(time.Now().Unix()),
		Choices: []interfaces.BifrostResponseChoice{
			{
				Index: 0,
				Message: interfaces.BifrostResponseChoiceMessage{
					Role:    interfaces.RoleAssistant,
					Content: maxim.StrPtr("This is a mock response from the Bifrost API gateway. The actual API was not called."),
				},
				FinishReason: maxim.StrPtr("stop"),
			},
		},
		Usage: interfaces.LLMUsage{
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
