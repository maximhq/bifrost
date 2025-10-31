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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas/providers/bedrock"
	cohere "github.com/maximhq/bifrost/core/schemas/providers/cohere"
)

// BedrockProvider implements the Provider interface for AWS Bedrock.
type BedrockProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *http.Client                  // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
}

// bedrockChatResponsePool provides a pool for Bedrock response objects.
var bedrockChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &bedrock.BedrockConverseResponse{}
	},
}

// acquireBedrockChatResponse gets a Bedrock response from the pool and resets it.
func acquireBedrockChatResponse() *bedrock.BedrockConverseResponse {
	resp := bedrockChatResponsePool.Get().(*bedrock.BedrockConverseResponse)
	*resp = bedrock.BedrockConverseResponse{} // Reset the struct
	return resp
}

// releaseBedrockChatResponse returns a Bedrock response to the pool.
func releaseBedrockChatResponse(resp *bedrock.BedrockConverseResponse) {
	if resp != nil {
		bedrockChatResponsePool.Put(resp)
	}
}

// NewBedrockProvider creates a new Bedrock provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and AWS-specific settings.
func NewBedrockProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*BedrockProvider, error) {
	config.CheckAndSetDefaults()

	client := &http.Client{Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		bedrockChatResponsePool.Put(&bedrock.BedrockConverseResponse{})
	}

	return &BedrockProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Bedrock.
func (provider *BedrockProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.Bedrock, provider.customProviderConfig)
}

// completeRequest sends a request to Bedrock's API and handles the response.
// It constructs the API URL, sets up AWS authentication, and processes the response.
// Returns the response body, request latency, or an error if the request fails.
func (provider *BedrockProvider) completeRequest(ctx context.Context, requestBody interface{}, path string, key schemas.Key) ([]byte, time.Duration, *schemas.BifrostError) {
	config := key.BedrockKeyConfig

	region := bedrock.DefaultBedrockRegion
	if config.Region != nil {
		region = *config.Region
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
					Error:   err,
				},
			}
		}
		return nil, 0, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	// Create the request with the JSON body
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, 0, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value))
	} else {
		// Sign the request using either explicit credentials or IAM role authentication
		if err := signAWSRequest(ctx, req, config.AccessKey, config.SecretKey, config.SessionToken, region, "bedrock", provider.GetProviderKey()); err != nil {
			return nil, 0, err
		}
	}

	// Execute the request and measure latency
	startTime := time.Now()
	resp, err := provider.client.Do(req)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, latency, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, latency, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, provider.GetProviderKey())
		}
		return nil, latency, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latency, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp bedrock.BedrockError

		if err := sonic.Unmarshal(body, &errorResp); err != nil {
			return nil, latency, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &resp.StatusCode,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
			}
		}

		return nil, latency, &schemas.BifrostError{
			StatusCode: &resp.StatusCode,
			Error: &schemas.ErrorField{
				Message: errorResp.Message,
			},
		}
	}

	return body, latency, nil
}

// makeStreamingRequest creates a streaming request to Bedrock's API.
// It formats the request, sends it to Bedrock, and returns the response.
// Returns the response body and an error if the request fails.
func (provider *BedrockProvider) makeStreamingRequest(ctx context.Context, requestBody interface{}, key schemas.Key, model string) (*http.Response, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	// Format the path with proper model identifier for streaming
	path := provider.getModelPath("converse-stream", model, key)

	region := bedrock.DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Create the streaming request
	jsonBody, jsonErr := sonic.Marshal(requestBody)
	if jsonErr != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, jsonErr, providerName)
	}

	// Create HTTP request for streaming
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewReader(jsonBody))
	if reqErr != nil {
		return nil, newBifrostOperationError("error creating request", reqErr, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value != "" {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value))
	} else {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
		// Sign the request using either explicit credentials or IAM role authentication
		if err := signAWSRequest(ctx, req, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
			return nil, err
		}
	}

	// Make the request
	resp, respErr := provider.client.Do(req)
	if respErr != nil {
		if errors.Is(respErr, http.ErrHandlerTimeout) || errors.Is(respErr, context.Canceled) || errors.Is(respErr, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, respErr, provider.GetProviderKey())
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, respErr, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerName, nil, nil)
	}

	return resp, nil
}

// signAWSRequest signs an HTTP request using AWS Signature Version 4.
// It is used in providers like Bedrock.
// It sets required headers, calculates the request body hash, and signs the request
// using the provided AWS credentials.
// Returns a BifrostError if signing fails.
func signAWSRequest(ctx context.Context, req *http.Request, accessKey, secretKey string, sessionToken *string, region, service string, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Set required headers before signing
	req.Header.Set("Content-Type", "application/json")
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	// Calculate SHA256 hash of the request body
	var bodyHash string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return newBifrostOperationError("error reading request body", err, providerName)
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

	var cfg aws.Config
	var err error

	// If both accessKey and secretKey are empty, use the default credential provider chain
	// This will automatically use IAM roles, environment variables, shared credentials, etc.
	if accessKey == "" && secretKey == "" {
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
		)
	} else {
		// Use explicit credentials when provided
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
				creds := aws.Credentials{
					AccessKeyID:     accessKey,
					SecretAccessKey: secretKey,
				}
				if sessionToken != nil && *sessionToken != "" {
					creds.SessionToken = *sessionToken
				}
				return creds, nil
			})),
		)
	}
	if err != nil {
		return newBifrostOperationError("failed to load aws config", err, providerName)
	}

	// Create the AWS signer
	signer := v4.NewSigner()

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return newBifrostOperationError("failed to retrieve aws credentials", err, providerName)
	}

	// Sign the request with AWS Signature V4
	if err := signer.SignHTTP(ctx, creds, req, bodyHash, service, region, time.Now()); err != nil {
		return newBifrostOperationError("failed to sign request", err, providerName)
	}

	return nil
}

// listModelsByKey performs a list models request to Bedrock's API for a single key.
// It retrieves all foundation models available in Amazon Bedrock for a specific key.
func (provider *BedrockProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	config := key.BedrockKeyConfig

	region := bedrock.DefaultBedrockRegion
	if config.Region != nil {
		region = *config.Region
	}

	// Build query parameters
	params := url.Values{}
	if request.ExtraParams != nil {
		if byCustomizationType, ok := request.ExtraParams["byCustomizationType"].(string); ok && byCustomizationType != "" {
			params.Set("byCustomizationType", byCustomizationType)
		}
		if byInferenceType, ok := request.ExtraParams["byInferenceType"].(string); ok && byInferenceType != "" {
			params.Set("byInferenceType", byInferenceType)
		}
		if byOutputModality, ok := request.ExtraParams["byOutputModality"].(string); ok && byOutputModality != "" {
			params.Set("byOutputModality", byOutputModality)
		}
		if byProvider, ok := request.ExtraParams["byProvider"].(string); ok && byProvider != "" {
			params.Set("byProvider", byProvider)
		}
	}

	// List models endpoint uses the bedrock service (not bedrock-runtime)
	url := fmt.Sprintf("https://bedrock.%s.amazonaws.com/foundation-models?%s", region, params.Encode())

	// Create the GET request without a body
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value))
	} else {
		// Sign the request using either explicit credentials or IAM role authentication
		if err := signAWSRequest(ctx, req, config.AccessKey, config.SecretKey, config.SessionToken, region, "bedrock", providerName); err != nil {
			return nil, err
		}
	}

	startTime := time.Now()

	// Execute the request
	resp, err := provider.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		} else if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Read response body and close
	responseBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp bedrock.BedrockError

		if err := sonic.Unmarshal(responseBody, &errorResp); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &resp.StatusCode,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
			}
		}
		return nil, &schemas.BifrostError{
			StatusCode: &resp.StatusCode,
			Error: &schemas.ErrorField{
				Message: errorResp.Message,
			},
		}
	}

	// Parse Bedrock-specific response
	bedrockResponse := &bedrock.BedrockListModelsResponse{}
	rawResponse, bifrostErr := handleProviderResponse(responseBody, bedrockResponse, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	response := bedrockResponse.ToBifrostListModelsResponse(providerName)
	if response == nil {
		return nil, newBifrostOperationError("failed to convert Bedrock model list response", nil, providerName)
	}

	response.ExtraFields.Latency = time.Since(startTime).Milliseconds()

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Bedrock's API.
// It retrieves all foundation models available in Amazon Bedrock.
// Requests are made concurrently for improved performance.
func (provider *BedrockProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return handleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// TextCompletion performs a text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	requestBody := bedrock.ToBedrockTextCompletionRequest(request)
	if requestBody == nil {
		return nil, newBifrostOperationError("text completion input is not provided", nil, providerName)
	}

	path := provider.getModelPath("invoke", request.Model, key)
	body, latency, err := provider.completeRequest(ctx, requestBody, path, key)
	if err != nil {
		return nil, err
	}

	// Handle model-specific response conversion
	var bifrostResponse *schemas.BifrostTextCompletionResponse
	switch {
	case strings.Contains(request.Model, "anthropic.") || strings.Contains(request.Model, "claude"):
		var response bedrock.BedrockAnthropicTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, newBifrostOperationError("error parsing anthropic response", err, providerName)
		}
		bifrostResponse = response.ToBifrostTextCompletionResponse()

	case strings.Contains(request.Model, "mistral."):
		var response bedrock.BedrockMistralTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, newBifrostOperationError("error parsing mistral response", err, providerName)
		}
		bifrostResponse = response.ToBifrostTextCompletionResponse()

	default:
		return nil, newConfigurationError(fmt.Sprintf("unsupported model type for text completion: %s", request.Model), providerName)
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.TextCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Parse raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			return nil, newBifrostOperationError("error parsing raw response", err, providerName)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *BedrockProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion stream", "bedrock")
}

// ChatCompletion performs a chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	// pool the request
	reqBody, err := bedrock.ToBedrockChatCompletionRequest(request)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert request", err, providerName)
	}

	// Format the path with proper model identifier
	path := provider.getModelPath("converse", request.Model, key)

	// Create the signed request
	responseBody, latency, bifrostErr := provider.completeRequest(ctx, reqBody, path, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// pool the response
	bedrockResponse := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(bedrockResponse)

	// Parse the response using the new Bedrock type
	if err := sonic.Unmarshal(responseBody, bedrockResponse); err != nil {
		return nil, newBifrostOperationError("failed to parse bedrock response", err, providerName)
	}

	// Convert using the new response converter
	bifrostResponse, err := bedrockResponse.ToBifrostChatResponse(request.Model)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert bedrock response", err, providerName)
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponse
		}
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the streaming response.
// Returns a channel for streaming BifrostResponse objects or an error if the request fails.
func (provider *BedrockProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	reqBody, err := bedrock.ToBedrockChatCompletionRequest(request)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert request", err, providerName)
	}

	resp, bifrostErr := provider.makeStreamingRequest(ctx, reqBody, key, request.Model)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		// Process AWS Event Stream format
		var messageID string
		var usage *schemas.BifrostLLMUsage
		var finishReason *string
		chunkIndex := 0

		// Process AWS Event Stream format using proper decoder
		startTime := time.Now()
		lastChunkTime := startTime
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

		for {
			// Decode a single EventStream message
			message, err := decoder.Decode(resp.Body, payloadBuf)
			if err != nil {
				if err == io.EOF {
					// End of stream - this is normal
					break
				}
				provider.logger.Warn(fmt.Sprintf("Error decoding %s EventStream message: %v", providerName, err))
				processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
				return
			}

			// Process the decoded message payload (contains JSON for normal events)
			if len(message.Payload) > 0 {
				if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
					if msgType := msgTypeHeader.String(); msgType != "event" {
						excType := msgType
						if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
							if v := excHeader.String(); v != "" {
								excType = v
							}
						}
						errMsg := string(message.Payload)
						err := fmt.Errorf("%s stream %s: %s", providerName, excType, errMsg)
						processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
						return
					}
				}

				// Parse the JSON event into our typed structure
				var streamEvent bedrock.BedrockStreamEvent
				if err := sonic.Unmarshal(message.Payload, &streamEvent); err != nil {
					provider.logger.Debug(fmt.Sprintf("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload)))
					return
				}

				if streamEvent.Usage != nil {
					usage = &schemas.BifrostLLMUsage{
						PromptTokens:     streamEvent.Usage.InputTokens,
						CompletionTokens: streamEvent.Usage.OutputTokens,
						TotalTokens:      streamEvent.Usage.TotalTokens,
					}
				}

				if streamEvent.StopReason != nil {
					finishReason = schemas.Ptr(anthropic.ConvertAnthropicFinishReasonToBifrost(anthropic.AnthropicStopReason(*streamEvent.StopReason)))
				}

				response, bifrostErr, _ := streamEvent.ToBifrostChatCompletionStream()
				if response != nil {
					response.ID = messageID
					response.Model = request.Model
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					}
					chunkIndex++
					lastChunkTime = time.Now()

					if provider.sendBackRawResponse {
						response.ExtraFields.RawResponse = string(message.Payload)
					}

					processAndSendResponse(ctx, postHookRunner, getBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan, provider.logger)
				}
				if bifrostErr != nil {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					}
					processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
			}
		}

		// Send final response
		response := createBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, request.Model)
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		handleStreamEndWithSuccess(ctx, getBifrostResponseForStreamResponse(nil, response, nil, nil, nil), postHookRunner, responseChan, provider.logger)
	}()

	return responseChan, nil
}

// Responses performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	// pool the request
	reqBody, err := bedrock.ToBedrockResponsesRequest(request)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert request", err, providerName)
	}

	// Format the path with proper model identifier
	path := provider.getModelPath("converse", request.Model, key)

	// Create the signed request
	responseBody, latency, bifrostErr := provider.completeRequest(ctx, reqBody, path, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// pool the response
	bedrockResponse := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(bedrockResponse)

	// Parse the response using the new Bedrock type
	if err := sonic.Unmarshal(responseBody, bedrockResponse); err != nil {
		return nil, newBifrostOperationError("failed to parse bedrock response", err, providerName)
	}

	// Convert using the new response converter
	bifrostResponse, err := bedrockResponse.ToBifrostResponsesResponse()
	if err != nil {
		return nil, newBifrostOperationError("failed to convert bedrock response", err, providerName)
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponse
		}
	}

	return bifrostResponse, nil
}

// ResponsesStream performs a streaming chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the streaming response.
// Returns a channel for streaming BifrostResponse objects or an error if the request fails.
func (provider *BedrockProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	reqBody, err := bedrock.ToBedrockResponsesRequest(request)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert request", err, providerName)
	}

	resp, bifrostErr := provider.makeStreamingRequest(ctx, reqBody, key, request.Model)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		// Process AWS Event Stream format
		var usage *schemas.ResponsesResponseUsage
		chunkIndex := 0

		// Process AWS Event Stream format using proper decoder
		startTime := time.Now()
		lastChunkTime := startTime
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

		for {
			// Decode a single EventStream message
			message, err := decoder.Decode(resp.Body, payloadBuf)
			if err != nil {
				if err == io.EOF {
					// End of stream - this is normal
					break
				}
				provider.logger.Warn(fmt.Sprintf("Error decoding %s EventStream message: %v", providerName, err))
				processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
				return
			}

			// Process the decoded message payload (contains JSON for normal events)
			if len(message.Payload) > 0 {
				if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
					if msgType := msgTypeHeader.String(); msgType != "event" {
						excType := msgType
						if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
							if v := excHeader.String(); v != "" {
								excType = v
							}
						}
						errMsg := string(message.Payload)
						err := fmt.Errorf("%s stream %s: %s", providerName, excType, errMsg)
						processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
						return
					}
				}

				// Parse the JSON event into our typed structure
				var streamEvent bedrock.BedrockStreamEvent
				if err := sonic.Unmarshal(message.Payload, &streamEvent); err != nil {
					provider.logger.Debug(fmt.Sprintf("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload)))
					return
				}

				if chunkIndex == 0 {
					sendCreatedEventResponsesChunk(ctx, postHookRunner, provider.GetProviderKey(), request.Model, startTime, responseChan, provider.logger)
					sendInProgressEventResponsesChunk(ctx, postHookRunner, provider.GetProviderKey(), request.Model, startTime, responseChan, provider.logger)
					chunkIndex = 2
				}

				if streamEvent.Usage != nil {
					usage = &schemas.ResponsesResponseUsage{
						InputTokens:  streamEvent.Usage.InputTokens,
						OutputTokens: streamEvent.Usage.OutputTokens,
						TotalTokens:  streamEvent.Usage.TotalTokens,
					}
				}

				response, bifrostErr, _ := streamEvent.ToBifrostResponsesStream(chunkIndex)
				if response != nil {

					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					}
					chunkIndex++
					lastChunkTime = time.Now()

					if provider.sendBackRawResponse {
						response.ExtraFields.RawResponse = string(message.Payload)
					}

					processAndSendResponse(ctx, postHookRunner, getBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan, provider.logger)
				}
				if bifrostErr != nil {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					}
					processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
			}
		}

		// Send final response
		response := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: chunkIndex + 1,
			Response: &schemas.BifrostResponsesResponse{
				Usage: usage,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ResponsesStreamRequest,
				Provider:       providerName,
				ModelRequested: request.Model,
				ChunkIndex:     chunkIndex + 1,
				Latency:        time.Since(startTime).Milliseconds(),
			},
		}
		handleStreamEndWithSuccess(ctx, getBifrostResponseForStreamResponse(nil, nil, response, nil, nil), postHookRunner, responseChan, provider.logger)
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s) using Amazon Bedrock.
// Supports Titan and Cohere embedding models. Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *BedrockProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	// Determine model type
	modelType, err := bedrock.DetermineEmbeddingModelType(request.Model)
	if err != nil {
		return nil, newConfigurationError(err.Error(), providerName)
	}

	// Convert request and execute based on model type
	var rawResponse []byte
	var bifrostError *schemas.BifrostError
	var latency time.Duration

	switch modelType {
	case "titan":
		titanReq, err := bedrock.ToBedrockTitanEmbeddingRequest(request)
		if err != nil {
			return nil, newBifrostOperationError("failed to convert Titan request", err, providerName)
		}
		path := provider.getModelPath("invoke", request.Model, key)
		rawResponse, latency, bifrostError = provider.completeRequest(ctx, titanReq, path, key)

	case "cohere":
		cohereReq, err := bedrock.ToBedrockCohereEmbeddingRequest(request)
		if err != nil {
			return nil, newBifrostOperationError("failed to convert Cohere request", err, providerName)
		}
		path := provider.getModelPath("invoke", request.Model, key)
		rawResponse, latency, bifrostError = provider.completeRequest(ctx, cohereReq, path, key)

	default:
		return nil, newConfigurationError("unsupported embedding model type", providerName)
	}

	if bifrostError != nil {
		return nil, bifrostError
	}

	// Parse response based on model type
	var bifrostResponse *schemas.BifrostEmbeddingResponse
	switch modelType {
	case "titan":
		var titanResp bedrock.BedrockTitanEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &titanResp); err != nil {
			return nil, newBifrostOperationError("error parsing Titan embedding response", err, providerName)
		}
		bifrostResponse = titanResp.ToBifrostEmbeddingResponse()
		bifrostResponse.Model = request.Model

	case "cohere":
		var cohereResp cohere.CohereEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &cohereResp); err != nil {
			return nil, newBifrostOperationError("error parsing Cohere embedding response", err, providerName)
		}
		bifrostResponse = cohereResp.ToBifrostEmbeddingResponse()
		bifrostResponse.Model = request.Model
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponseData interface{}
		if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponseData
		}
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Bedrock provider.
func (provider *BedrockProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "bedrock")
}

// SpeechStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "bedrock")
}

// Transcription is not supported by the Bedrock provider.
func (provider *BedrockProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "bedrock")
}

// TranscriptionStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "bedrock")
}

func (provider *BedrockProvider) getModelPath(basePath string, model string, key schemas.Key) string {
	// Format the path with proper model identifier for streaming
	path := fmt.Sprintf("%s/%s", model, basePath)

	if key.BedrockKeyConfig.Deployments != nil {
		if inferenceProfileID, ok := key.BedrockKeyConfig.Deployments[model]; ok {
			if key.BedrockKeyConfig.ARN != nil {
				encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", *key.BedrockKeyConfig.ARN, inferenceProfileID))
				path = fmt.Sprintf("%s/%s", encodedModelIdentifier, basePath)
			}
		}
	}

	return path
}
