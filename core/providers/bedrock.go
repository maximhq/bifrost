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

	"bufio"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/bedrock"
	cohere "github.com/maximhq/bifrost/core/schemas/providers/cohere"
	"github.com/valyala/fasthttp"
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

// CompleteRequest sends a request to Bedrock's API and handles the response.
// It constructs the API URL, sets up AWS authentication, and processes the response.
// Returns the response body, request latency, or an error if the request fails.
func (provider *BedrockProvider) completeRequest(ctx context.Context, requestBody interface{}, path string, key schemas.Key) ([]byte, time.Duration, *schemas.BifrostError) {
	config := key.BedrockKeyConfig

	region := "us-east-1"
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
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
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

// TextCompletion performs a text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
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
	var bifrostResponse *schemas.BifrostResponse
	switch {
	case strings.Contains(request.Model, "anthropic.") || strings.Contains(request.Model, "claude"):
		var response bedrock.BedrockAnthropicTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, newBifrostOperationError("error parsing anthropic response", err, providerName)
		}
		bifrostResponse = response.ToBifrostResponse()

	case strings.Contains(request.Model, "mistral."):
		var response bedrock.BedrockMistralTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, newBifrostOperationError("error parsing mistral response", err, providerName)
		}
		bifrostResponse = response.ToBifrostResponse()

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
func (provider *BedrockProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
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
	bifrostResponse, err := bedrockResponse.ToBifrostResponse()
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

// Responses performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
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
	bifrostResponse, err := bedrockResponse.ToResponsesBifrostResponse()
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

// signAWSRequest signs an HTTP request using AWS Signature Version 4.
// It is used in providers like Bedrock.
// It sets required headers, calculates the request body hash, and signs the request
// using the provided AWS credentials.
// Returns a BifrostError if signing fails.
func signAWSRequest(ctx context.Context, req *http.Request, accessKey, secretKey string, sessionToken *string, region, service string, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Set required headers before signing
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

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

// Embedding generates embeddings for the given input text(s) using Amazon Bedrock.
// Supports Titan and Cohere embedding models. Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *BedrockProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
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
	var bifrostResponse *schemas.BifrostResponse
	switch modelType {
	case "titan":
		var titanResp bedrock.BedrockTitanEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &titanResp); err != nil {
			return nil, newBifrostOperationError("error parsing Titan embedding response", err, providerName)
		}
		bifrostResponse = titanResp.ToBifrostResponse(request.Model)

	case "cohere":
		var cohereResp cohere.CohereEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &cohereResp); err != nil {
			return nil, newBifrostOperationError("error parsing Cohere embedding response", err, providerName)
		}
		bifrostResponse = cohereResp.ToBifrostResponse()
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

// ChatCompletionStream performs a streaming chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the streaming response.
// Returns a channel for streaming BifrostResponse objects or an error if the request fails.
func (provider *BedrockProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	reqBody, err := bedrock.ToBedrockChatCompletionRequest(request)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert request", err, providerName)
	}

	// Format the path with proper model identifier for streaming
	path := provider.getModelPath("converse-stream", request.Model, key)

	region := "us-east-1"
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Create the streaming request
	jsonBody, jsonErr := sonic.Marshal(reqBody)
	if jsonErr != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, jsonErr, providerName)
	}

	// Create HTTP request for streaming
	req, reqErr := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewReader(jsonBody))
	if reqErr != nil {
		return nil, newBifrostOperationError("error creating request", reqErr, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value))
	} else {
		// Sign the request using either explicit credentials or IAM role authentication
		if err := signAWSRequest(ctx, req, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
			return nil, err
		}
	}

	// Make the request
	resp, respErr := provider.client.Do(req)
	if respErr != nil {
		if errors.Is(respErr, fasthttp.ErrTimeout) || errors.Is(respErr, context.Canceled) || errors.Is(respErr, context.DeadlineExceeded) {
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

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		// Process AWS Event Stream format
		var messageID string
		var usage *schemas.LLMUsage
		var finishReason *string
		chunkIndex := -1

		// Read the response body as a continuous stream
		reader := bufio.NewReader(resp.Body)
		buffer := make([]byte, 1024*1024) // 1MB buffer
		var accumulator []byte            // Accumulate data across reads
		startTime := time.Now()
		lastChunkTime := startTime

		for {
			n, err := reader.Read(buffer)
			if err != nil {
				if err == io.EOF {
					// Process any remaining data in the accumulator
					if len(accumulator) > 0 {
						_ = provider.processAWSEventStreamData(ctx, postHookRunner, accumulator, &messageID, &chunkIndex, &usage, &finishReason, request.Model, providerName, responseChan, &lastChunkTime)
					}
					break
				}
				provider.logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerName, err))
				processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
				return
			}

			if n == 0 {
				continue
			}

			// Append new data to accumulator
			accumulator = append(accumulator, buffer[:n]...)

			// Process the accumulated data and get the remaining unprocessed part
			remaining := provider.processAWSEventStreamData(ctx, postHookRunner, accumulator, &messageID, &chunkIndex, &usage, &finishReason, request.Model, providerName, responseChan, &lastChunkTime)

			// Reset accumulator with remaining data
			accumulator = remaining
		}

		// Send final response
		response := createBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, request.Model)
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, provider.logger)
	}()

	return responseChan, nil
}

// processAWSEventStreamData processes raw AWS Event Stream data and extracts JSON events.
// Returns any remaining unprocessed bytes that should be kept for the next read.
func (provider *BedrockProvider) processAWSEventStreamData(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	data []byte,
	messageID *string,
	chunkIndex *int,
	usage **schemas.LLMUsage,
	finishReason **string,
	model string,
	providerName schemas.ModelProvider,
	responseChan chan *schemas.BifrostStream,
	lastChunkTime *time.Time,
) []byte {
	lastProcessed := 0
	depth := 0
	inString := false
	escaped := false
	objStart := -1

	for i := 0; i < len(data); i++ {
		b := data[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch b {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				objStart = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && objStart >= 0 {
					jsonBytes := data[objStart : i+1]
					// Quick filter to match original behavior - check for JSON content and relevant fields
					hasQuotes := bytes.Contains(jsonBytes, []byte(`"`))
					hasRelevantContent := bytes.Contains(jsonBytes, []byte(`role`)) ||
						bytes.Contains(jsonBytes, []byte(`delta`)) ||
						bytes.Contains(jsonBytes, []byte(`usage`)) ||
						bytes.Contains(jsonBytes, []byte(`stopReason`)) ||
						bytes.Contains(jsonBytes, []byte(`contentBlockIndex`)) ||
						bytes.Contains(jsonBytes, []byte(`metadata`))

					if hasQuotes && hasRelevantContent {
						provider.processEventBuffer(ctx, postHookRunner, jsonBytes, messageID, chunkIndex, usage, finishReason, model, providerName, responseChan, lastChunkTime)
						lastProcessed = i + 1
					}
					objStart = -1
				}
			}
		default:
			// skip
		}
	}

	if lastProcessed < len(data) {
		return data[lastProcessed:]
	}
	return nil
}

// processEventBuffer processes AWS Event Stream JSON payloads using typed Bedrock stream events
func (provider *BedrockProvider) processEventBuffer(ctx context.Context, postHookRunner schemas.PostHookRunner, eventBuffer []byte, messageID *string, chunkIndex *int, usage **schemas.LLMUsage, finishReason **string, model string, providerName schemas.ModelProvider, responseChan chan *schemas.BifrostStream, lastChunkTime *time.Time) {
	// Parse the JSON event into our typed structure
	var streamEvent bedrock.BedrockStreamEvent
	if err := sonic.Unmarshal(eventBuffer, &streamEvent); err != nil {
		provider.logger.Debug(fmt.Sprintf("Failed to parse JSON from event buffer: %v, data: %s", err, string(eventBuffer)))
		return
	}

	// Ensure we have a message ID for all events
	if *messageID == "" {
		*messageID = fmt.Sprintf("bedrock-%d", time.Now().UnixNano())
	}

	// Process typed stream events based on flat structure
	switch {
	case streamEvent.Role != nil:
		// Handle messageStart event
		*chunkIndex++

		// Send empty response to signal start
		streamResponse := &schemas.BifrostResponse{
			ID:     *messageID,
			Object: "chat.completion.chunk",
			Model:  model,
			Choices: []schemas.BifrostChatResponseChoice{
				{
					Index: 0,
					BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
						Delta: &schemas.BifrostStreamDelta{
							Role: streamEvent.Role,
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionStreamRequest,
				Provider:       providerName,
				ModelRequested: model,
				ChunkIndex:     *chunkIndex,
				Latency:        time.Since(*lastChunkTime).Milliseconds(),
			},
		}

		*lastChunkTime = time.Now()
		processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)

	case streamEvent.Start != nil && streamEvent.Start.ToolUse != nil:
		// Handle tool use start event
		*chunkIndex++
		contentBlockIndex := 0
		if streamEvent.ContentBlockIndex != nil {
			contentBlockIndex = *streamEvent.ContentBlockIndex
		}

		toolUseStart := streamEvent.Start.ToolUse

		// Create tool call structure for start event
		var toolCall schemas.ChatAssistantMessageToolCall
		toolCall.Type = schemas.Ptr("function")
		toolCall.Function.Name = schemas.Ptr(toolUseStart.Name)
		toolCall.Function.Arguments = "{}" // Start with empty arguments

		streamResponse := &schemas.BifrostResponse{
			ID:     *messageID,
			Object: "chat.completion.chunk",
			Model:  model,
			Choices: []schemas.BifrostChatResponseChoice{
				{
					Index: contentBlockIndex,
					BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
						Delta: &schemas.BifrostStreamDelta{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{toolCall},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionStreamRequest,
				Provider:       providerName,
				ModelRequested: model,
				ChunkIndex:     *chunkIndex,
				Latency:        time.Since(*lastChunkTime).Milliseconds(),
			},
		}

		*lastChunkTime = time.Now()
		processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)

	case streamEvent.ContentBlockIndex != nil && streamEvent.Delta != nil:
		// Handle contentBlockDelta event
		*chunkIndex++
		contentBlockIndex := *streamEvent.ContentBlockIndex

		switch {
		case streamEvent.Delta.Text != nil:
			// Handle text delta
			text := *streamEvent.Delta.Text
			if text != "" {
				streamResponse := &schemas.BifrostResponse{
					ID:     *messageID,
					Object: "chat.completion.chunk",
					Model:  model,
					Choices: []schemas.BifrostChatResponseChoice{
						{
							Index: contentBlockIndex,
							BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
								Delta: &schemas.BifrostStreamDelta{
									Content: &text,
								},
							},
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: model,
						ChunkIndex:     *chunkIndex,
						Latency:        time.Since(*lastChunkTime).Milliseconds(),
					},
				}

				*lastChunkTime = time.Now()
				processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)
			}

		case streamEvent.Delta.ToolUse != nil:
			// Handle tool use delta
			toolUseDelta := streamEvent.Delta.ToolUse

			// Create tool call structure
			var toolCall schemas.ChatAssistantMessageToolCall
			toolCall.Type = schemas.Ptr("function")

			// For streaming, we need to accumulate tool use data
			// This is a simplified approach - in practice, you'd need to track tool calls across chunks
			toolCall.Function.Arguments = toolUseDelta.Input

			streamResponse := &schemas.BifrostResponse{
				ID:     *messageID,
				Object: "chat.completion.chunk",
				Model:  model,
				Choices: []schemas.BifrostChatResponseChoice{
					{
						Index: contentBlockIndex,
						BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
							Delta: &schemas.BifrostStreamDelta{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{toolCall},
							},
						},
					},
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerName,
					ModelRequested: model,
					ChunkIndex:     *chunkIndex,
					Latency:        time.Since(*lastChunkTime).Milliseconds(),
				},
			}

			*lastChunkTime = time.Now()
			processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)
		}

	case streamEvent.StopReason != nil:
		// Handle messageStop event
		*finishReason = streamEvent.StopReason

	case streamEvent.Usage != nil:
		// Handle usage information
		bedrockUsage := streamEvent.Usage
		*usage = &schemas.LLMUsage{
			PromptTokens:     bedrockUsage.InputTokens,
			CompletionTokens: bedrockUsage.OutputTokens,
			TotalTokens:      bedrockUsage.TotalTokens,
		}

	default:
		// Log unknown event types for debugging
		provider.logger.Debug("Unknown or empty stream event received")
	}
}

func (provider *BedrockProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "bedrock")
}

func (provider *BedrockProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "bedrock")
}

func (provider *BedrockProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "bedrock")
}

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

func (provider *BedrockProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("responses stream", "bedrock")
}
