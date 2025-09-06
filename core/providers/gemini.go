// Package providers implements various LLM providers and their utility functions.
// This file contains the Gemini provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Response message for PredictionService.GenerateContent.
type GenerateContentResponse struct {
	// Response variations returned by the model.
	Candidates []*Candidate `json:"candidates,omitempty"`
	// Usage metadata about the response(s).
	UsageMetadata *GenerateContentResponseUsageMetadata `json:"usageMetadata,omitempty"`
}

// A response candidate generated from the model.
type Candidate struct {
	// Optional. Contains the multi-part content of the response.
	Content *Content `json:"content,omitempty"`
	// Optional. The reason why the model stopped generating tokens.
	// If empty, the model has not stopped generating the tokens.
	FinishReason string `json:"finishReason,omitempty"`
	// Output only. Index of the candidate.
	Index int32 `json:"index,omitempty"`
}

// Contains the multi-part content of a message.
type Content struct {
	// Optional. List of parts that constitute a single message. Each part may have
	// a different IANA MIME type.
	Parts []*Part `json:"parts,omitempty"`
	// Optional. The producer of the content. Must be either 'user' or
	// 'model'. Useful to set for multi-turn conversations, otherwise can be
	// empty. If role is not specified, SDK will determine the role.
	Role string `json:"role,omitempty"`
}

// A datatype containing media content.
// Exactly one field within a Part should be set, representing the specific type
// of content being conveyed. Using multiple fields within the same `Part`
// instance is considered invalid.
type Part struct {
	// Optional. Inlined bytes data.
	InlineData *Blob `json:"inlineData,omitempty"`
	// Optional. Text part (can be code).
	Text string `json:"text,omitempty"`
}

// Content blob.
type Blob struct {
	// Required. Raw bytes.
	Data []byte `json:"data,omitempty"`
}

// Usage metadata about response(s).
type GenerateContentResponseUsageMetadata struct {
	// Number of tokens in the response(s). This includes all the generated response candidates.
	CandidatesTokenCount int32 `json:"candidatesTokenCount,omitempty"`
	// Number of tokens in the prompt. When cached_content is set, this is still the total
	// effective prompt size meaning this includes the number of tokens in the cached content.
	PromptTokenCount int32 `json:"promptTokenCount,omitempty"`
	// Total token count for prompt, response candidates, and tool-use prompts (if present).
	TotalTokenCount int32 `json:"totalTokenCount,omitempty"`
}

type GeminiProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	streamClient         *http.Client                  // HTTP client for streaming requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewGeminiProvider creates a new Gemini provider instance.
// It initializes the HTTP client with the provided configuration.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewGeminiProvider(config *schemas.ProviderConfig, logger schemas.Logger) *GeminiProvider {
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

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &GeminiProvider{
		logger:               logger,
		client:               client,
		streamClient:         streamClient,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Gemini.
func (provider *GeminiProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.Gemini, provider.customProviderConfig)
}

// TextCompletion is not supported by the Gemini provider.
func (provider *GeminiProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", string(provider.GetProviderKey()))
}

// ChatCompletion performs a chat completion request to the Gemini API.
func (provider *GeminiProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.OperationChatCompletion); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/openai/chat/completions")
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
		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("%s error: %v", providerName, errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	// response := acquireGeminiResponse()
	// defer releaseGeminiResponse(response)
	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	for _, choice := range response.Choices {
		if choice.Message.AssistantMessage == nil || choice.Message.AssistantMessage.ToolCalls == nil {
			continue
		}
		for i, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
			if (toolCall.ID == nil || *toolCall.ID == "") && toolCall.Function.Name != nil && *toolCall.Function.Name != "" {
				id := *toolCall.Function.Name
				(*choice.Message.AssistantMessage.ToolCalls)[i].ID = &id
			}
		}
	}

	response.ExtraFields.Provider = providerName

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		response.ExtraFields.Params = *params
	}

	return response, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Gemini API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Gemini's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *GeminiProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.OperationChatCompletionStream); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Prepare Gemini headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + key.Value,
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Use shared OpenAI-compatible streaming logic
	return handleOpenAIStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/openai/chat/completions",
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		providerName,
		params,
		postHookRunner,
		provider.logger,
	)
}

// Embedding performs an embedding request to the Gemini API.
func (provider *GeminiProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if embedding is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.OperationEmbedding); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if input == nil || len(input.Texts) == 0 {
		return nil, newBifrostOperationError("invalid embedding input: at least one text is required", nil, providerName)
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
		if params.ExtraParams != nil {
			requestBody = mergeConfig(requestBody, params.ExtraParams)
		}
	}

	// Use the shared embedding request handler
	return handleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/openai/embeddings",
		requestBody,
		key,
		params,
		provider.networkConfig.ExtraHeaders,
		providerName,
		provider.sendBackRawResponse,
		provider.logger,
	)
}

func (provider *GeminiProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if speech is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.OperationSpeech); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Validate input
	if input == nil || input.Input == "" {
		return nil, newBifrostOperationError("invalid speech input: no text provided", fmt.Errorf("empty text input"), providerName)
	}

	// Prepare request body using shared function
	requestBody := prepareGeminiGenerationRequest(input, params, []string{"AUDIO"})

	// Use common request function
	bifrostResponse, geminiResponse, bifrostErr := provider.completeRequest(ctx, model, key, requestBody, ":generateContent", params)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Extract audio data from response
	var audioData []byte
	if len(geminiResponse.Candidates) > 0 && geminiResponse.Candidates[0].Content != nil {
		for _, part := range geminiResponse.Candidates[0].Content.Parts {
			if part.InlineData != nil && part.InlineData.Data != nil {
				audioData = append(audioData, part.InlineData.Data...)
			}
		}
	}

	if len(audioData) == 0 {
		return nil, newBifrostOperationError("no audio data received from Gemini", fmt.Errorf("empty audio response"), providerName)
	}

	// Extract usage metadata using shared function
	inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(geminiResponse)

	// Update the response with speech-specific data
	bifrostResponse.Object = "audio.speech"
	bifrostResponse.Speech = &schemas.BifrostSpeech{
		Audio: audioData,
		Usage: &schemas.AudioLLMUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  totalTokens,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

func (provider *GeminiProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if speech stream is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.OperationSpeechStream); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Validate input
	if input == nil || input.Input == "" {
		return nil, newBifrostOperationError("invalid speech input: no text provided", fmt.Errorf("empty text input"), providerName)
	}

	// Prepare request body using shared function
	requestBody := prepareGeminiGenerationRequest(input, params, []string{"AUDIO"})

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/models/"+model+":streamGenerateContent?alt=sse", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Set headers for streaming
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseStreamGeminiError(providerName, resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		// Increase buffer size to handle large chunks (especially for audio data)
		buf := make([]byte, 0, 64*1024) // 64KB buffer
		scanner.Buffer(buf, 1024*1024)  // Allow up to 1MB tokens
		chunkIndex := -1
		usage := &schemas.AudioLLMUsage{}

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
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

			chunkIndex++

			// Process chunk using shared function
			geminiResponse, err := processGeminiStreamChunk(jsonData)
			if err != nil {
				if strings.Contains(err.Error(), "gemini api error") {
					// Handle API error
					bifrostErr := &schemas.BifrostError{
						Type:           Ptr("gemini_api_error"),
						IsBifrostError: false,
						Error: schemas.ErrorField{
							Message: err.Error(),
							Error:   err,
						},
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
				provider.logger.Warn(fmt.Sprintf("Failed to process chunk: %v", err))
				continue
			}

			// Extract audio data from Gemini response for regular chunks
			var audioChunk []byte
			if len(geminiResponse.Candidates) > 0 {
				candidate := geminiResponse.Candidates[0]
				if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
					var buf []byte
					for _, part := range candidate.Content.Parts {
						if part.InlineData != nil && part.InlineData.Data != nil {
							buf = append(buf, part.InlineData.Data...)
						}
					}
					if len(buf) > 0 {
						audioChunk = buf
					}
				}
			}

			// Check if this is the final chunk (has finishReason)
			if len(geminiResponse.Candidates) > 0 && (geminiResponse.Candidates[0].FinishReason != "" || geminiResponse.UsageMetadata != nil) {
				// Extract usage metadata using shared function
				inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(geminiResponse)
				usage.InputTokens = inputTokens
				usage.OutputTokens = outputTokens
				usage.TotalTokens = totalTokens
			}

			// Only send response if we have actual audio content
			if len(audioChunk) > 0 {
				// Create Bifrost speech response for streaming
				response := &schemas.BifrostResponse{
					Object: "audio.speech.chunk",
					Model:  model,
					Speech: &schemas.BifrostSpeech{
						Audio: audioChunk,
						BifrostSpeechStreamResponse: &schemas.BifrostSpeechStreamResponse{
							Type: "audio.speech.chunk",
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						Provider:   providerName,
						ChunkIndex: chunkIndex,
					},
				}

				// Process response through post-hooks and send to channel
				processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)
			}
		}

		chunkIndex++
		ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)

		response := &schemas.BifrostResponse{
			Object: "audio.speech.chunk",
			Speech: &schemas.BifrostSpeech{
				Usage: usage,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:   providerName,
				ChunkIndex: chunkIndex,
			},
		}

		if params != nil {
			response.ExtraFields.Params = *params
		}

		processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, provider.logger)
		}
	}()

	return responseChan, nil
}

func (provider *GeminiProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if transcription is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.OperationTranscription); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Check file size limit (Gemini has a 20MB limit for inline data)
	const maxFileSize = 20 * 1024 * 1024 // 20MB
	if len(input.File) > maxFileSize {
		return nil, newBifrostOperationError("audio file too large for inline transcription", fmt.Errorf("file size %d bytes exceeds 20MB limit", len(input.File)), providerName)
	}

	if input.Prompt == nil {
		input.Prompt = Ptr("Generate a transcript of the speech.")
	}

	// Prepare request body using shared function
	requestBody := prepareGeminiGenerationRequest(input, params, nil)

	// Use common request function
	bifrostResponse, geminiResponse, bifrostErr := provider.completeRequest(ctx, model, key, requestBody, ":generateContent", params)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Extract text from Gemini response
	var transcriptText string
	if len(geminiResponse.Candidates) > 0 && geminiResponse.Candidates[0].Content != nil {
		for _, p := range geminiResponse.Candidates[0].Content.Parts {
			if p.Text != "" {
				transcriptText += p.Text
			}
		}
	}

	// If no transcript text was extracted, return an error
	if transcriptText == "" {
		return nil, newBifrostOperationError("failed to extract transcript from Gemini response", fmt.Errorf("no transcript text found"), providerName)
	}

	// Extract usage metadata using shared function
	inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(geminiResponse)

	// Update the response with transcription-specific data
	bifrostResponse.Object = "audio.transcription"
	bifrostResponse.Transcribe = &schemas.BifrostTranscribe{
		Text: transcriptText,
		Usage: &schemas.TranscriptionUsage{
			Type:         "tokens",
			InputTokens:  &inputTokens,
			OutputTokens: &outputTokens,
			TotalTokens:  &totalTokens,
		},
		BifrostTranscribeNonStreamResponse: &schemas.BifrostTranscribeNonStreamResponse{
			Task:     Ptr("transcribe"),
			Language: input.Language,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

func (provider *GeminiProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if transcription stream is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.OperationTranscriptionStream); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Check file size limit (Gemini has a 20MB limit for inline data)
	if input.File != nil {
		const maxFileSize = 20 * 1024 * 1024 // 20MB
		if len(input.File) > maxFileSize {
			return nil, newBifrostOperationError("audio file too large for inline transcription", fmt.Errorf("file size %d bytes exceeds 20MB limit", len(input.File)), providerName)
		}
	}

	if input.Prompt == nil {
		input.Prompt = Ptr("Generate a transcript of the speech.")
	}

	// Prepare request body using shared function
	requestBody := prepareGeminiGenerationRequest(input, params, nil)

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/models/"+model+":streamGenerateContent?alt=sse", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Set headers for streaming
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseStreamGeminiError(providerName, resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := -1
		usage := &schemas.TranscriptionUsage{}

		var fullTranscriptionText string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
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

			chunkIndex++

			// First, check if this is an error response
			var errorCheck map[string]interface{}
			if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				bifrostErr := &schemas.BifrostError{
					Type:           Ptr("gemini_api_error"),
					IsBifrostError: false,
					Error: schemas.ErrorField{
						Message: fmt.Sprintf("Gemini API error: %v", errorCheck["error"]),
						Error:   fmt.Errorf("stream error: %v", errorCheck["error"]),
					},
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				return
			}

			// Parse Gemini streaming response
			var geminiResponse GenerateContentResponse
			if err := sonic.Unmarshal([]byte(jsonData), &geminiResponse); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse Gemini stream response: %v", err))
				continue
			}

			// Extract text from Gemini response for regular chunks
			var deltaText string
			if len(geminiResponse.Candidates) > 0 && geminiResponse.Candidates[0].Content != nil {
				if len(geminiResponse.Candidates[0].Content.Parts) > 0 {
					var sb strings.Builder
					for _, p := range geminiResponse.Candidates[0].Content.Parts {
						if p.Text != "" {
							sb.WriteString(p.Text)
						}
					}
					if sb.Len() > 0 {
						deltaText = sb.String()
						fullTranscriptionText += deltaText
					}
				}
			}

			// Check if this is the final chunk (has finishReason)
			if len(geminiResponse.Candidates) > 0 && (geminiResponse.Candidates[0].FinishReason != "" || geminiResponse.UsageMetadata != nil) {
				// Extract usage metadata from Gemini response
				inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(&geminiResponse)
				usage.InputTokens = Ptr(inputTokens)
				usage.OutputTokens = Ptr(outputTokens)
				usage.TotalTokens = Ptr(totalTokens)
			}

			// Only send response if we have actual text content
			if deltaText != "" {
				// Create Bifrost transcription response for streaming
				response := &schemas.BifrostResponse{
					Object: "audio.transcription.chunk",
					Transcribe: &schemas.BifrostTranscribe{
						BifrostTranscribeStreamResponse: &schemas.BifrostTranscribeStreamResponse{
							Type:  Ptr("transcript.text.delta"),
							Delta: &deltaText, // Delta text for this chunk
						},
					},
					Model: model,
					ExtraFields: schemas.BifrostResponseExtraFields{
						Provider:   providerName,
						ChunkIndex: chunkIndex,
					},
				}

				// Process response through post-hooks and send to channel
				processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)
			}
		}

		chunkIndex++
		ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)

		response := &schemas.BifrostResponse{
			Object: "audio.transcription.chunk",
			Transcribe: &schemas.BifrostTranscribe{
				Text: fullTranscriptionText,
				Usage: &schemas.TranscriptionUsage{
					Type:         "tokens",
					InputTokens:  usage.InputTokens,
					OutputTokens: usage.OutputTokens,
					TotalTokens:  usage.TotalTokens,
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:   providerName,
				ChunkIndex: chunkIndex,
			},
		}

		if params != nil {
			response.ExtraFields.Params = *params
		}

		processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, provider.logger)
		}
	}()

	return responseChan, nil
}

// prepareGeminiGenerationRequest prepares the common request structure for Gemini API calls
func prepareGeminiGenerationRequest(input interface{}, params *schemas.ModelParameters, responseModalities []string) map[string]interface{} {
	requestBody := map[string]interface{}{
		"generationConfig": map[string]interface{}{},
	}

	// Add response modalities if specified
	if len(responseModalities) > 0 {
		requestBody["generationConfig"].(map[string]interface{})["responseModalities"] = responseModalities
	}

	// Map Bifrost parameters to Gemini generationConfig
	if params != nil {
		generationConfig := requestBody["generationConfig"].(map[string]interface{})

		// Map standard parameters to Gemini generationConfig
		if params.StopSequences != nil {
			generationConfig["stopSequences"] = *params.StopSequences
		}
		if params.MaxTokens != nil {
			generationConfig["maxOutputTokens"] = *params.MaxTokens
		}
		if params.Temperature != nil {
			generationConfig["temperature"] = *params.Temperature
		}
		if params.TopP != nil {
			generationConfig["topP"] = *params.TopP
		}
		if params.TopK != nil {
			generationConfig["topK"] = *params.TopK
		}
		if params.PresencePenalty != nil {
			generationConfig["presencePenalty"] = *params.PresencePenalty
		}
		if params.FrequencyPenalty != nil {
			generationConfig["frequencyPenalty"] = *params.FrequencyPenalty
		}

		// Handle tool-related parameters
		if params.Tools != nil && len(*params.Tools) > 0 {
			// Transform Bifrost tools to Gemini format
			var geminiTools []map[string]interface{}
			for _, tool := range *params.Tools {
				if tool.Type == "function" {
					geminiTool := map[string]interface{}{
						"functionDeclarations": []map[string]interface{}{
							{
								"name":        tool.Function.Name,
								"description": tool.Function.Description,
								"parameters":  tool.Function.Parameters,
							},
						},
					}
					geminiTools = append(geminiTools, geminiTool)
				}
			}

			if len(geminiTools) > 0 {
				requestBody["tools"] = geminiTools

				// Add toolConfig for Gemini
				toolConfig := map[string]interface{}{}

				// Handle tool choice
				if params.ToolChoice != nil {
					functionCallingConfig := map[string]interface{}{}

					if params.ToolChoice.ToolChoiceStr != nil {
						// Map string values to Gemini's enum values
						switch *params.ToolChoice.ToolChoiceStr {
						case "none":
							functionCallingConfig["mode"] = "NONE"
						case "auto":
							functionCallingConfig["mode"] = "AUTO"
						case "any":
							functionCallingConfig["mode"] = "ANY"
						case "required":
							functionCallingConfig["mode"] = "ANY"
						default:
							functionCallingConfig["mode"] = "AUTO"
						}
					} else if params.ToolChoice.ToolChoiceStruct != nil {
						switch params.ToolChoice.ToolChoiceStruct.Type {
						case schemas.ToolChoiceTypeNone:
							functionCallingConfig["mode"] = "NONE"
						case schemas.ToolChoiceTypeAuto:
							functionCallingConfig["mode"] = "AUTO"
						case schemas.ToolChoiceTypeRequired:
							functionCallingConfig["mode"] = "ANY"
						case schemas.ToolChoiceTypeFunction:
							functionCallingConfig["mode"] = "ANY"
						default:
							functionCallingConfig["mode"] = "AUTO"
						}

						// Handle specific function selection if provided
						if params.ToolChoice.ToolChoiceStruct.Function.Name != "" {
							functionCallingConfig["allowedFunctionNames"] = []string{params.ToolChoice.ToolChoiceStruct.Function.Name}
						}
					}

					// Only add functionCallingConfig if it has content
					if len(functionCallingConfig) > 0 {
						toolConfig["functionCallingConfig"] = functionCallingConfig
					}
				}

				// Only add toolConfig if it has content
				if len(toolConfig) > 0 {
					requestBody["toolConfig"] = toolConfig
				}
			}
		}

		// Add any extra parameters that might be Gemini-specific
		if params.ExtraParams != nil {
			requestBody = mergeConfig(requestBody, params.ExtraParams)
		}
	}

	// Add contents based on input type
	switch v := input.(type) {
	case *schemas.SpeechInput:
		// Speech/TTS request
		requestBody["contents"] = []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": v.Input},
				},
			},
		}
		addSpeechConfig(requestBody, v.VoiceConfig)
	case *schemas.TranscriptionInput:
		// Transcription request
		parts := []map[string]interface{}{
			{"text": v.Prompt},
		}

		if len(v.File) > 0 {
			if v.Format == nil {
				v.Format = Ptr(detectAudioMimeType(v.File))
			}
			parts = append(parts, map[string]interface{}{
				"inlineData": map[string]interface{}{
					"mimeType": *v.Format,
					"data":     v.File,
				},
			})
		}

		requestBody["contents"] = []map[string]interface{}{
			{"parts": parts},
		}
	case []schemas.BifrostMessage:
		// Chat completion request
		formattedMessages, _ := prepareOpenAIChatRequest(v, params)
		requestBody["contents"] = formattedMessages
	}

	return requestBody
}

// addSpeechConfig adds speech configuration to the request body
func addSpeechConfig(requestBody map[string]interface{}, voiceConfig schemas.SpeechVoiceInput) {
	speechConfig := map[string]interface{}{}

	// Handle single voice configuration
	if voiceConfig.Voice != nil {
		speechConfig["voiceConfig"] = map[string]interface{}{
			"prebuiltVoiceConfig": map[string]interface{}{
				"voiceName": *voiceConfig.Voice,
			},
		}
	}

	// Handle multi-speaker voice configuration
	if len(voiceConfig.MultiVoiceConfig) > 0 {
		var speakerVoiceConfigs []map[string]interface{}
		for _, vc := range voiceConfig.MultiVoiceConfig {
			speakerVoiceConfigs = append(speakerVoiceConfigs, map[string]interface{}{
				"speaker": vc.Speaker,
				"voiceConfig": map[string]interface{}{
					"prebuiltVoiceConfig": map[string]interface{}{
						"voiceName": vc.Voice,
					},
				},
			})
		}

		speechConfig["multiSpeakerVoiceConfig"] = map[string]interface{}{
			"speakerVoiceConfigs": speakerVoiceConfigs,
		}
	}

	// Add speech config to generation config if not empty
	if len(speechConfig) > 0 {
		requestBody["generationConfig"].(map[string]interface{})["speechConfig"] = speechConfig
	}
}

// processGeminiStreamChunk processes a single chunk from Gemini streaming response
func processGeminiStreamChunk(jsonData string) (*GenerateContentResponse, error) {
	// First, check if this is an error response
	var errorCheck map[string]interface{}
	if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
		return nil, fmt.Errorf("failed to parse stream data as JSON: %v", err)
	}

	// Handle error responses
	if _, hasError := errorCheck["error"]; hasError {
		return nil, fmt.Errorf("gemini api error: %v", errorCheck["error"])
	}

	// Parse Gemini streaming response
	var geminiResponse GenerateContentResponse
	if err := sonic.Unmarshal([]byte(jsonData), &geminiResponse); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini stream response: %v", err)
	}

	return &geminiResponse, nil
}

// extractGeminiUsageMetadata extracts usage metadata (as ints) from Gemini response
func extractGeminiUsageMetadata(geminiResponse *GenerateContentResponse) (int, int, int) {
	var inputTokens, outputTokens, totalTokens int
	if geminiResponse.UsageMetadata != nil {
		usageMetadata := geminiResponse.UsageMetadata
		inputTokens = int(usageMetadata.PromptTokenCount)
		outputTokens = int(usageMetadata.CandidatesTokenCount)
		totalTokens = int(usageMetadata.TotalTokenCount)
	}
	return inputTokens, outputTokens, totalTokens
}

// completeRequest handles the common HTTP request pattern for Gemini API calls
func (provider *GeminiProvider) completeRequest(ctx context.Context, model string, key schemas.Key, requestBody map[string]interface{}, endpoint string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *GenerateContentResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Use Gemini's generateContent endpoint
	req.SetRequestURI(provider.networkConfig.BaseURL + "/models/" + model + endpoint)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("x-goog-api-key", key.Value)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, nil, parseGeminiError(providerName, resp)
	}

	responseBody := resp.Body()

	// Parse Gemini's response
	var geminiResponse GenerateContentResponse
	if err := sonic.Unmarshal(responseBody, &geminiResponse); err != nil {
		return nil, nil, newBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Create base response
	bifrostResponse := &schemas.BifrostResponse{
		Model: model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponse
		}
	}

	return bifrostResponse, &geminiResponse, nil
}

// parseStreamGeminiError parses Gemini streaming error responses
func parseStreamGeminiError(providerName schemas.ModelProvider, resp *http.Response) *schemas.BifrostError {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return newBifrostOperationError("failed to read error response body", err, providerName)
	}

	// Try to parse as JSON first
	var errorResp map[string]interface{}
	if err := sonic.Unmarshal(body, &errorResp); err == nil {
		// Successfully parsed as JSON
		return newBifrostOperationError(fmt.Sprintf("Gemini streaming error: %v", errorResp), fmt.Errorf("HTTP %d", resp.StatusCode), providerName)
	}

	// If JSON parsing fails, treat as plain text
	bodyStr := string(body)
	if bodyStr == "" {
		bodyStr = "empty response body"
	}

	return newBifrostOperationError(fmt.Sprintf("Gemini streaming error (HTTP %d): %s", resp.StatusCode, bodyStr), fmt.Errorf("HTTP %d", resp.StatusCode), providerName)
}

// parseGeminiError parses Gemini error responses
func parseGeminiError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp map[string]interface{}
	body := resp.Body()

	if err := sonic.Unmarshal(body, &errorResp); err != nil {
		return newBifrostOperationError("failed to parse error response", err, providerName)
	}

	return newBifrostOperationError(fmt.Sprintf("Gemini error: %v", errorResp), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}
