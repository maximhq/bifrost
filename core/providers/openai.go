// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenAI provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
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

// openAIResponsePool provides a pool for OpenAI response objects.
var openAIResponsePool = sync.Pool{
	New: func() interface{} {
		openAIPoolGets.Add(1)
		return &schemas.BifrostResponse{}
	},
}

// acquireOpenAIResponse gets an OpenAI response from the pool and resets it.
func acquireOpenAIResponse() *schemas.BifrostResponse {
	openAIPoolGets.Add(1)
	resp := openAIResponsePool.Get().(*schemas.BifrostResponse)
	*resp = schemas.BifrostResponse{} // Reset the struct
	return resp
}

// releaseOpenAIResponse returns an OpenAI response to the pool.
func releaseOpenAIResponse(resp *schemas.BifrostResponse) {
	if resp != nil {
		openAIPoolPuts.Add(1)
		openAIResponsePool.Put(resp)
	}
}

// OpenAIProvider implements the Provider interface for OpenAI's GPT API.
type OpenAIProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	metrics             OpenAIMetrics         // Performance metrics for this provider
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
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
		openAIResponsePool.Put(&schemas.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.openai.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &OpenAIProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		metrics:             OpenAIMetrics{}, // Initialize metrics
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for OpenAI.
func (provider *OpenAIProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.OpenAI
}

// TextCompletion is not supported by the OpenAI provider.
// Returns an error indicating that text completion is not available.
func (provider *OpenAIProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "openai")
}

// ChatCompletion performs a chat completion request to the OpenAI API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *OpenAIProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	timings := make(map[string]time.Duration)

	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	// Track JSON marshaling time
	marshalStart := time.Now()
	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		timings["json_marshaling"] = time.Since(marshalStart)
		provider.recordOpenAIMetrics(timings, false) // Record metrics for failed request
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.OpenAI)
	}
	timings["json_marshaling"] = time.Since(marshalStart)

	// Track request setup time
	setupStart := time.Now()

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
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	timings["request_setup"] = time.Since(setupStart)

	// Track HTTP request time
	httpStart := time.Now()

	mockResponse := mockOpenAIChatCompletionResponse(req, model)
	// Copy the mock response body to the real response
	resp.SetBody(mockResponse)
	// Simulate network delay
	jitter := time.Duration(float64(1500*time.Millisecond) * (0.6 + 0.8*rand.Float64()))
	time.Sleep(jitter)

	// Make request
	// bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	timings["http_request"] = time.Since(httpStart)
	// if bifrostErr != nil {
	// 	provider.recordOpenAIMetrics(timings, false) // Record metrics for failed request
	// 	return nil, bifrostErr
	// }

	// Track error handling time
	errorStart := time.Now()

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		timings["error_handling"] = time.Since(errorStart)
		provider.recordOpenAIMetrics(timings, false) // Record metrics for failed request
		provider.logger.Debug(fmt.Sprintf("error from openai provider: %s", string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	timings["error_handling"] = time.Since(errorStart)

	// Track response parsing time (more granular)
	parseStart := time.Now()

	responseBody := resp.Body()

	// Track pool acquisition time
	poolStart := time.Now()
	response := acquireOpenAIResponse()
	timings["pool_acquisition"] = time.Since(poolStart)
	defer releaseOpenAIResponse(response)

	// Use enhanced response handler with pre-allocated response
	unmarshalStartTime := time.Now()
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	timings["json_unmarshaling"] = time.Since(unmarshalStartTime)
	if bifrostErr != nil {
		timings["total_response_parsing"] = time.Since(parseStart)
		provider.recordOpenAIMetrics(timings, false) // Record metrics for failed request
		return nil, bifrostErr
	}

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		response.ExtraFields.Params = *params
	}

	timings["total_response_parsing"] = time.Since(parseStart)

	response.ExtraFields.RawResponse = map[string]interface{}{
		"timings": timings,
	}

	provider.recordOpenAIMetrics(timings, true) // Record metrics for successful request
	return response, nil
}

// prepareOpenAIChatRequest formats messages for the OpenAI API.
// It handles both text and image content in messages.
// Returns a slice of formatted messages and any additional parameters.
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
func (provider *OpenAIProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Validate input texts are not empty
	if len(input.Texts) == 0 {
		return nil, newBifrostOperationError("input texts cannot be empty", nil, schemas.OpenAI)
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

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.OpenAI)
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
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from openai provider: %s", string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	// Parse response
	var response OpenAIResponse
	if err := sonic.Unmarshal(resp.Body(), &response); err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.OpenAI)
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
						return nil, newBifrostOperationError(fmt.Sprintf("unsupported number type in embedding array: %T", v[j]), nil, schemas.OpenAI)
					}
				}
				embeddings[i] = floatArray
			case string:
				// Decode base64 string into float32 array
				decodedData, err := base64.StdEncoding.DecodeString(v)
				if err != nil {
					return nil, newBifrostOperationError("failed to decode base64 embedding", err, schemas.OpenAI)
				}

				// Validate that decoded data length is divisible by 4 (size of float32)
				const sizeOfFloat32 = 4
				if len(decodedData)%sizeOfFloat32 != 0 {
					return nil, newBifrostOperationError("malformed base64 embedding data: length not divisible by 4", nil, schemas.OpenAI)
				}

				floats := make([]float32, len(decodedData)/sizeOfFloat32)
				for i := 0; i < len(floats); i++ {
					floats[i] = math.Float32frombits(binary.LittleEndian.Uint32(decodedData[i*4 : (i+1)*4]))
				}
				embeddings[i] = floats
			default:
				return nil, newBifrostOperationError(fmt.Sprintf("unsupported embedding type: %T", data.Embedding), nil, schemas.OpenAI)
			}
		}
		bifrostResponse.Embedding = embeddings
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// ChatCompletionStream handles streaming for OpenAI chat completions.
// It formats messages, prepares request body, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + key.Value,
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

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.OpenAI)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, newBifrostOperationError("failed to create HTTP request", err, schemas.OpenAI)
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
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, schemas.OpenAI)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, parseStreamOpenAIError(resp)
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

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData = strings.TrimPrefix(line, "data: ")
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			var errorCheck map[string]interface{}
			if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				errorStream, err := parseOpenAIErrorForStreamDataLine(jsonData)
				if err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}

				select {
				case responseChan <- errorStream:
				case <-ctx.Done():
				}
				return // Stop processing on error
			}

			// Parse into bifrost response
			var response schemas.BifrostResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
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

				processAndSendResponse(ctx, postHookRunner, &response, responseChan)
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

				processAndSendResponse(ctx, postHookRunner, &response, responseChan)

				// End stream processing after finish reason
				break
			}

			// Handle regular content chunks
			if choice.Delta.Content != nil || len(choice.Delta.ToolCalls) > 0 {
				if params != nil {
					response.ExtraFields.Params = *params
				}
				response.ExtraFields.Provider = providerType

				processAndSendResponse(ctx, postHookRunner, &response, responseChan)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan)
		}
	}()

	return responseChan, nil
}

// Speech handles non-streaming speech synthesis requests.
// It formats the request body, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	responseFormat := input.ResponseFormat
	if responseFormat == "" {
		responseFormat = "mp3"
	}

	requestBody := map[string]interface{}{
		"input":           input.Input,
		"model":           model,
		"voice":           input.VoiceConfig.Voice,
		"instructions":    input.Instructions,
		"response_format": responseFormat,
	}

	if params != nil {
		requestBody = mergeConfig(requestBody, params.ExtraParams)
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.OpenAI)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/audio/speech")
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
		provider.logger.Debug(fmt.Sprintf("error from openai provider: %s", string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	// Get the binary audio data from the response body
	audioData := resp.Body()

	// Create final response with the audio data
	// Note: For speech synthesis, we return the binary audio data in the raw response
	// The audio data is typically in MP3, WAV, or other audio formats as specified by response_format
	bifrostResponse := &schemas.BifrostResponse{
		Object: "audio.speech",
		Model:  model,
		Speech: &schemas.BifrostSpeech{
			Audio: audioData,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.OpenAI,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// SpeechStream handles streaming for speech synthesis.
// It formats the request body, creates HTTP request, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	responseFormat := input.ResponseFormat
	if responseFormat == "" {
		responseFormat = "mp3"
	}

	requestBody := map[string]interface{}{
		"input":           input.Input,
		"model":           model,
		"voice":           input.VoiceConfig.Voice,
		"instructions":    input.Instructions,
		"response_format": responseFormat,
		"stream_format":   "sse",
	}

	if params != nil {
		requestBody = mergeConfig(requestBody, params.ExtraParams)
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.OpenAI)
	}

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + key.Value,
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/v1/audio/speech", strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, newBifrostOperationError("failed to create HTTP request", err, schemas.OpenAI)
	}

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, schemas.OpenAI)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, parseStreamOpenAIError(resp)
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

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData = strings.TrimPrefix(line, "data: ")
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			var errorCheck map[string]interface{}
			if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				errorStream, err := parseOpenAIErrorForStreamDataLine(jsonData)
				if err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}

				select {
				case responseChan <- errorStream:
				case <-ctx.Done():
				}
				return // Stop processing on error
			}

			// Parse into bifrost response
			var response schemas.BifrostResponse

			var speechResponse schemas.BifrostSpeech
			if err := sonic.Unmarshal([]byte(jsonData), &speechResponse); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			response.Speech = &speechResponse
			response.Object = "audio.speech.chunk"
			response.Model = model
			response.ExtraFields = schemas.BifrostResponseExtraFields{
				Provider: schemas.OpenAI,
			}

			if params != nil {
				response.ExtraFields.Params = *params
			}

			processAndSendResponse(ctx, postHookRunner, &response, responseChan)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan)
		}
	}()

	return responseChan, nil
}

// Transcription handles non-streaming transcription requests.
// It creates a multipart form, adds fields, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if bifrostErr := parseTranscriptionFormDataBody(writer, input, model, params); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/audio/transcriptions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType(writer.FormDataContentType()) // This sets multipart/form-data with boundary
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(body.Bytes())

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from openai provider: %s", string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	responseBody := resp.Body()

	// Parse OpenAI's transcription response directly into BifrostTranscribe
	transcribeResponse := &schemas.BifrostTranscribe{
		BifrostTranscribeNonStreamResponse: &schemas.BifrostTranscribeNonStreamResponse{},
	}

	if err := sonic.Unmarshal(responseBody, transcribeResponse); err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.OpenAI)
	}

	// Parse raw response for RawResponse field
	var rawResponse interface{}
	if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderDecodeRaw, err, schemas.OpenAI)
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		Object:     "audio.transcription",
		Model:      model,
		Transcribe: transcribeResponse,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.OpenAI,
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

func (provider *OpenAIProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("stream", "true"); err != nil {
		return nil, newBifrostOperationError("failed to write stream field", err, schemas.OpenAI)
	}

	if bifrostErr := parseTranscriptionFormDataBody(writer, input, model, params); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  writer.FormDataContentType(),
		"Authorization": "Bearer " + key.Value,
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/v1/audio/transcriptions", &body)
	if err != nil {
		return nil, newBifrostOperationError("failed to create HTTP request", err, schemas.OpenAI)
	}

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, schemas.OpenAI)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, parseStreamOpenAIError(resp)
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
			if line == "" {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
			}

			var jsonData string
			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData = strings.TrimPrefix(line, "data: ")
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			var errorCheck map[string]interface{}
			if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				errorStream, err := parseOpenAIErrorForStreamDataLine(jsonData)
				if err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}

				select {
				case responseChan <- errorStream:
				case <-ctx.Done():
				}
				return // Stop processing on error
			}

			var response schemas.BifrostResponse

			var transcriptionResponse schemas.BifrostTranscribe
			if err := sonic.Unmarshal([]byte(jsonData), &transcriptionResponse); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			response.Transcribe = &transcriptionResponse
			response.Object = "audio.transcription.chunk"
			response.Model = model
			response.ExtraFields = schemas.BifrostResponseExtraFields{
				Provider: schemas.OpenAI,
			}

			if params != nil {
				response.ExtraFields.Params = *params
			}

			processAndSendResponse(ctx, postHookRunner, &response, responseChan)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan)
		}
	}()

	return responseChan, nil
}

func parseTranscriptionFormDataBody(writer *multipart.Writer, input *schemas.TranscriptionInput, model string, params *schemas.ModelParameters) *schemas.BifrostError {
	// Add file field
	fileWriter, err := writer.CreateFormFile("file", "audio.mp3") // OpenAI requires a filename
	if err != nil {
		return newBifrostOperationError("failed to create form file", err, schemas.OpenAI)
	}
	if _, err := fileWriter.Write(input.File); err != nil {
		return newBifrostOperationError("failed to write file data", err, schemas.OpenAI)
	}

	// Add model field
	if err := writer.WriteField("model", model); err != nil {
		return newBifrostOperationError("failed to write model field", err, schemas.OpenAI)
	}

	// Add optional fields
	if input.Language != nil {
		if err := writer.WriteField("language", *input.Language); err != nil {
			return newBifrostOperationError("failed to write language field", err, schemas.OpenAI)
		}
	}

	if input.Prompt != nil {
		if err := writer.WriteField("prompt", *input.Prompt); err != nil {
			return newBifrostOperationError("failed to write prompt field", err, schemas.OpenAI)
		}
	}

	if input.ResponseFormat != nil {
		if err := writer.WriteField("response_format", *input.ResponseFormat); err != nil {
			return newBifrostOperationError("failed to write response_format field", err, schemas.OpenAI)
		}
	}

	// Note: Temperature and TimestampGranularities can be added via params.ExtraParams if needed

	// Add extra params if provided
	if params != nil && params.ExtraParams != nil {
		for key, value := range params.ExtraParams {
			// Handle array parameters specially for OpenAI's form data format
			switch v := value.(type) {
			case []string:
				// For arrays like timestamp_granularities[] or include[]
				for _, item := range v {
					if err := writer.WriteField(key+"[]", item); err != nil {
						return newBifrostOperationError(fmt.Sprintf("failed to write array param %s", key), err, schemas.OpenAI)
					}
				}
			case []interface{}:
				// Handle generic interface arrays
				for _, item := range v {
					if err := writer.WriteField(key+"[]", fmt.Sprintf("%v", item)); err != nil {
						return newBifrostOperationError(fmt.Sprintf("failed to write array param %s", key), err, schemas.OpenAI)
					}
				}
			default:
				// Handle non-array parameters normally
				if err := writer.WriteField(key, fmt.Sprintf("%v", value)); err != nil {
					return newBifrostOperationError(fmt.Sprintf("failed to write extra param %s", key), err, schemas.OpenAI)
				}
			}
		}
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return newBifrostOperationError("failed to close multipart writer", err, schemas.OpenAI)
	}

	return nil
}

func parseOpenAIError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp schemas.BifrostError

	bifrostErr := handleProviderAPIError(resp, &errorResp)

	if errorResp.EventID != nil {
		bifrostErr.EventID = errorResp.EventID
	}
	bifrostErr.Error.Type = errorResp.Error.Type
	bifrostErr.Error.Code = errorResp.Error.Code
	bifrostErr.Error.Message = errorResp.Error.Message
	bifrostErr.Error.Param = errorResp.Error.Param
	if errorResp.Error.EventID != nil {
		bifrostErr.Error.EventID = errorResp.Error.EventID
	}

	return bifrostErr
}

func parseStreamOpenAIError(resp *http.Response) *schemas.BifrostError {
	var errorResp schemas.BifrostError

	statusCode := resp.StatusCode
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if err := sonic.Unmarshal(body, &errorResp); err != nil {
		return &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &statusCode,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   err,
			},
		}
	}

	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error:          schemas.ErrorField{},
	}

	if errorResp.EventID != nil {
		bifrostErr.EventID = errorResp.EventID
	}
	bifrostErr.Error.Type = errorResp.Error.Type
	bifrostErr.Error.Code = errorResp.Error.Code
	bifrostErr.Error.Message = errorResp.Error.Message
	bifrostErr.Error.Param = errorResp.Error.Param
	if errorResp.Error.EventID != nil {
		bifrostErr.Error.EventID = errorResp.Error.EventID
	}

	return bifrostErr
}

// PERFORMANCE TRACKING

// OpenAIMetrics stores provider-specific metrics using atomic operations for high-throughput scenarios
type OpenAIMetrics struct {
	// Counters
	RequestCount int64
	ErrorCount   int64

	// Time accumulators (in nanoseconds for atomic operations)
	TotalTimeNs                  int64
	MessagePreparationTimeNs     int64
	RequestBodyPreparationTimeNs int64
	JSONMarshalingTimeNs         int64
	RequestSetupTimeNs           int64
	HTTPRequestTimeNs            int64
	ErrorHandlingTimeNs          int64
	PoolAcquisitionTimeNs        int64
	JSONUnmarshalingTimeNs       int64
	ResponseParsingTimeNs        int64
}

// Counters for pool usage
var (
	openAIPoolGets      atomic.Int64
	openAIPoolPuts      atomic.Int64
	openAIPoolCreations atomic.Int64
)

// GetPoolStats returns the current pool usage statistics
func GetPoolStats() map[string]int64 {
	return map[string]int64{
		"openai_pool_gets":      openAIPoolGets.Load(),
		"openai_pool_puts":      openAIPoolPuts.Load(),
		"openai_pool_creations": openAIPoolCreations.Load(),
	}
}

// recordOpenAIMetrics records provider-specific metrics atomically in a goroutine
func (provider *OpenAIProvider) recordOpenAIMetrics(timings map[string]time.Duration, success bool) {
	// Spawn goroutine to avoid blocking the request path
	go func() {
		atomic.AddInt64(&provider.metrics.RequestCount, 1)
		if !success {
			atomic.AddInt64(&provider.metrics.ErrorCount, 1)
		}

		// Calculate total time from all timings
		var totalTime time.Duration
		for _, duration := range timings {
			totalTime += duration
		}

		// Add to accumulators
		atomic.AddInt64(&provider.metrics.TotalTimeNs, totalTime.Nanoseconds())

		if msgPrepTime, exists := timings["total_message_preparation"]; exists {
			atomic.AddInt64(&provider.metrics.MessagePreparationTimeNs, msgPrepTime.Nanoseconds())
		}
		if bodyPrepTime, exists := timings["request_body_preparation"]; exists {
			atomic.AddInt64(&provider.metrics.RequestBodyPreparationTimeNs, bodyPrepTime.Nanoseconds())
		}
		if marshalTime, exists := timings["json_marshaling"]; exists {
			atomic.AddInt64(&provider.metrics.JSONMarshalingTimeNs, marshalTime.Nanoseconds())
		}
		if setupTime, exists := timings["request_setup"]; exists {
			atomic.AddInt64(&provider.metrics.RequestSetupTimeNs, setupTime.Nanoseconds())
		}
		if httpTime, exists := timings["http_request"]; exists {
			atomic.AddInt64(&provider.metrics.HTTPRequestTimeNs, httpTime.Nanoseconds())
		}
		if errorTime, exists := timings["error_handling"]; exists {
			atomic.AddInt64(&provider.metrics.ErrorHandlingTimeNs, errorTime.Nanoseconds())
		}
		if poolTime, exists := timings["pool_acquisition"]; exists {
			atomic.AddInt64(&provider.metrics.PoolAcquisitionTimeNs, poolTime.Nanoseconds())
		}
		if unmarshalTime, exists := timings["json_unmarshaling"]; exists {
			atomic.AddInt64(&provider.metrics.JSONUnmarshalingTimeNs, unmarshalTime.Nanoseconds())
		}
		if parseTime, exists := timings["total_response_parsing"]; exists {
			atomic.AddInt64(&provider.metrics.ResponseParsingTimeNs, parseTime.Nanoseconds())
		}
	}()
}

// GetOpenAIMetrics returns averaged provider metrics
func (provider *OpenAIProvider) GetOpenAIMetrics() map[string]interface{} {
	// Read atomic values and calculate averages
	requestCount := atomic.LoadInt64(&provider.metrics.RequestCount)
	if requestCount == 0 {
		return map[string]interface{}{
			"request_count": 0,
			"error_count":   0,
		}
	}

	return map[string]interface{}{
		"request_count":                requestCount,
		"error_count":                  atomic.LoadInt64(&provider.metrics.ErrorCount),
		"error_rate":                   fmt.Sprintf("%.2f%%", float64(atomic.LoadInt64(&provider.metrics.ErrorCount))/float64(requestCount)*100),
		"avg_total_time":               time.Duration(atomic.LoadInt64(&provider.metrics.TotalTimeNs) / requestCount).String(),
		"avg_message_preparation_time": time.Duration(atomic.LoadInt64(&provider.metrics.MessagePreparationTimeNs) / requestCount).String(),
		"avg_request_body_prep_time":   time.Duration(atomic.LoadInt64(&provider.metrics.RequestBodyPreparationTimeNs) / requestCount).String(),
		"avg_json_marshaling_time":     time.Duration(atomic.LoadInt64(&provider.metrics.JSONMarshalingTimeNs) / requestCount).String(),
		"avg_request_setup_time":       time.Duration(atomic.LoadInt64(&provider.metrics.RequestSetupTimeNs) / requestCount).String(),
		"avg_http_request_time":        time.Duration(atomic.LoadInt64(&provider.metrics.HTTPRequestTimeNs) / requestCount).String(),
		"avg_error_handling_time":      time.Duration(atomic.LoadInt64(&provider.metrics.ErrorHandlingTimeNs) / requestCount).String(),
		"avg_pool_acquisition_time":    time.Duration(atomic.LoadInt64(&provider.metrics.PoolAcquisitionTimeNs) / requestCount).String(),
		"avg_json_unmarshaling_time":   time.Duration(atomic.LoadInt64(&provider.metrics.JSONUnmarshalingTimeNs) / requestCount).String(),
		"avg_response_parsing_time":    time.Duration(atomic.LoadInt64(&provider.metrics.ResponseParsingTimeNs) / requestCount).String(),
	}
}

func parseOpenAIErrorForStreamDataLine(jsonData string) (*schemas.BifrostStream, error) {
	var openAIError schemas.BifrostError
	if err := sonic.Unmarshal([]byte(jsonData), &openAIError); err != nil {
		return nil, err
	}

	// Send error through channel
	errorStream := &schemas.BifrostStream{
		BifrostError: &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Type:    openAIError.Error.Type,
				Code:    openAIError.Error.Code,
				Message: openAIError.Error.Message,
				Param:   openAIError.Error.Param,
			},
		},
	}

	if openAIError.EventID != nil {
		errorStream.BifrostError.EventID = openAIError.EventID
	}
	if openAIError.Error.EventID != nil {
		errorStream.BifrostError.Error.EventID = openAIError.Error.EventID
	}

	return errorStream, nil
}

// mockOpenAIResponse creates a mock response for OpenAI API calls
func mockOpenAIChatCompletionResponse(req *fasthttp.Request, model string) []byte {
	// Create a mock response that mimics OpenAI's format
	mockResp := &OpenAIResponse{
		ID:      "mock-" + model + "-" + fmt.Sprintf("%d", time.Now().Unix()),
		Object:  "chat.completion",
		Model:   model,
		Created: int(time.Now().Unix()),
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleAssistant,
						Content: schemas.MessageContent{
							ContentStr: StrPtr("This is a mock response from the Bifrost API gateway. The actual API was not called. " +
								"This response has been expanded to demonstrate the system's ability to handle larger payloads. " +
								"In a real-world scenario, this could be a comprehensive analysis, detailed explanation, or extensive documentation. " +
								"The Bifrost API gateway serves as a unified interface for multiple AI providers, offering seamless integration " +
								"and consistent response formats across different language models and services. " +
								"When operating in mock mode, the system generates realistic responses that mirror the expected output " +
								"from actual AI providers while maintaining the same structure and formatting conventions. " +
								"This capability is particularly useful for testing, development, and demonstration purposes " +
								"where you need predictable responses without consuming actual API credits or making real network calls. " +
								"The mock responses can be configured to simulate various scenarios including success cases, " +
								"error conditions, streaming responses, and different content types such as text, code, and structured data. " +
								"Additionally, the system supports comprehensive logging and monitoring of all interactions, " +
								"providing detailed insights into request patterns, response times, token usage, and system performance metrics. " +
								"This mock response continues with additional content to reach the desired payload size of 10KB. " +
								"Performance optimization is a key consideration in the design of this system, ensuring that " +
								"large responses can be handled efficiently without compromising system stability or user experience. " +
								"The architecture supports horizontal scaling, load balancing, and fault tolerance mechanisms " +
								"to maintain high availability and reliability even under heavy load conditions. " +
								"Security features include authentication, authorization, rate limiting, and comprehensive audit logging " +
								"to ensure that all API interactions are properly tracked and controlled. " +
								"The system also provides extensive configuration options allowing administrators to customize " +
								"behavior based on specific requirements and use cases. " +
								"Documentation and examples are provided to help developers integrate with the API quickly and effectively. " +
								"The mock mode serves as an excellent starting point for understanding the API structure and response formats " +
								"before moving to production deployments with actual AI provider integrations. " +
								"This extended mock response demonstrates the system's capability to handle substantial content volumes " +
								"while maintaining proper JSON formatting and response structure compliance. " +
								"The implementation includes proper error handling, timeout management, and resource cleanup " +
								"to ensure robust operation in production environments. " +
								"Monitoring and alerting capabilities provide real-time visibility into system health and performance, " +
								"enabling proactive identification and resolution of potential issues. " +
								"The API supports various content types including plain text, markdown, HTML, JSON, XML, and binary data, " +
								"making it suitable for diverse application requirements and integration scenarios. " +
								"Advanced features include request transformation, response filtering, content validation, " +
								"and custom middleware support for implementing specialized business logic. " +
								"The system is designed to be provider-agnostic, allowing seamless switching between different AI services " +
								"without requiring changes to client applications or integration code. " +
								"This flexibility enables organizations to optimize costs, performance, and capabilities " +
								"by leveraging the best features of multiple AI providers through a single, unified interface. " +
								"Comprehensive testing suites ensure reliability and compatibility across all supported providers and features. " +
								"The mock response system includes sophisticated simulation capabilities that can replicate " +
								"real-world usage patterns and edge cases to support thorough testing and validation processes. " +
								"Performance benchmarking tools are integrated to measure and optimize system throughput, latency, " +
								"and resource utilization under various load conditions and configuration settings. " +
								"The architecture supports both synchronous and asynchronous processing models, " +
								"enabling efficient handling of both real-time interactions and batch processing scenarios. " +
								"Data persistence and caching mechanisms are implemented to improve response times " +
								"and reduce external API calls when appropriate, while maintaining data freshness and accuracy. " +
								"The system includes comprehensive logging and analytics capabilities that provide insights " +
								"into usage patterns, performance trends, and potential optimization opportunities. " +
								"This mock response continues to provide additional content to demonstrate the system's ability " +
								"to handle large payloads efficiently and effectively while maintaining proper formatting and structure. " +
								"The implementation follows industry best practices for API design, security, and performance, " +
								"ensuring compatibility with existing development workflows and deployment processes. " +
								"Support for multiple programming languages and frameworks makes integration straightforward " +
								"regardless of the technology stack used by client applications. " +
								"The system provides detailed documentation, code examples, and interactive tutorials " +
								"to help developers get started quickly and implement advanced features effectively. " +
								"This comprehensive mock response serves as an example of the type of detailed, " +
								"informative content that can be processed and delivered through the Bifrost API gateway, " +
								"demonstrating its capability to handle substantial payloads while maintaining high performance " +
								"and reliability standards expected in production environments."),
						},
					},
				},
				FinishReason: StrPtr("stop"),
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
