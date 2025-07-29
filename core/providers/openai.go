// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenAI provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
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
		return &schemas.BifrostResponse{}
	},
}

// acquireOpenAIResponse gets an OpenAI response from the pool and resets it.
func acquireOpenAIResponse() *schemas.BifrostResponse {
	resp := openAIResponsePool.Get().(*schemas.BifrostResponse)
	*resp = schemas.BifrostResponse{} // Reset the struct
	return resp
}

// releaseOpenAIResponse returns an OpenAI response to the pool.
func releaseOpenAIResponse(resp *schemas.BifrostResponse) {
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
func (provider *OpenAIProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "openai")
}

// ChatCompletion performs a chat completion request to the OpenAI API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *OpenAIProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

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

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/chat/completions")
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

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	response := acquireOpenAIResponse()
	defer releaseOpenAIResponse(response)

	// Use enhanced response handler with pre-allocated response
	_, bifrostErr = handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if params != nil {
		response.ExtraFields.Params = *params
	}

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
			Provider:    schemas.OpenAI,
			RawResponse: rawResponse,
		},
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
