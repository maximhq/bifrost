package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
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
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/cohere"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// BedrockProvider implements the Provider interface for AWS Bedrock.
type BedrockProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *http.Client                  // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
}

// bedrockChatResponsePool provides a pool for Bedrock response objects.
var bedrockChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &BedrockConverseResponse{}
	},
}

// acquireBedrockChatResponse gets a Bedrock response from the pool and resets it.
func acquireBedrockChatResponse() *BedrockConverseResponse {
	resp := bedrockChatResponsePool.Get().(*BedrockConverseResponse)
	*resp = BedrockConverseResponse{} // Reset the struct
	return resp
}

// releaseBedrockChatResponse returns a Bedrock response to the pool.
func releaseBedrockChatResponse(resp *BedrockConverseResponse) {
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
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		bedrockChatResponsePool.Put(&BedrockConverseResponse{})
	}

	return &BedrockProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Bedrock.
func (provider *BedrockProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Bedrock, provider.customProviderConfig)
}

// completeRequest sends a request to Bedrock's API and handles the response.
// It constructs the API URL, sets up AWS authentication, and processes the response.
// Returns the response body, request latency, or an error if the request fails.
func (provider *BedrockProvider) completeRequest(ctx *schemas.BifrostContext, jsonData []byte, path string, key schemas.Key) ([]byte, time.Duration, *schemas.BifrostError) {
	config := key.BedrockKeyConfig

	region := DefaultBedrockRegion
	if config.Region != nil && config.Region.GetValue() != "" {
		region = config.Region.GetValue()
	}

	// Create the request with the JSON body
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewBuffer(jsonData))
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
	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
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
		// Check for timeout first using net.Error before checking net.OpError
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, provider.GetProviderKey())
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, provider.GetProviderKey())
		}
		// Check for DNS lookup and network errors after timeout checks
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
			return nil, latency, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderNetworkError,
					Error:   err,
				},
			}
		}
		return nil, latency, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderDoRequest,
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
		var errorResp BedrockError

		var rawErrorResponse interface{}
		if err := sonic.Unmarshal(body, &rawErrorResponse); err != nil {
			rawErrorResponse = string(body)
		}

		if err := sonic.Unmarshal(body, &errorResp); err != nil {
			return nil, latency, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &resp.StatusCode,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
				ExtraFields: schemas.BifrostErrorExtraFields{
					RawResponse: rawErrorResponse,
				},
			}
		}

		return nil, latency, &schemas.BifrostError{
			StatusCode: &resp.StatusCode,
			Error: &schemas.ErrorField{
				Message: errorResp.Message,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawResponse: rawErrorResponse,
			},
		}
	}

	return body, latency, nil
}

// makeStreamingRequest creates a streaming request to Bedrock's API.
// It formats the request, sends it to Bedrock, and returns the response.
// Returns the response body and an error if the request fails.
func (provider *BedrockProvider) makeStreamingRequest(ctx *schemas.BifrostContext, jsonData []byte, key schemas.Key, model string, action string) (*http.Response, string, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, "", providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Format the path with proper model identifier for streaming
	path, deployment := provider.getModelPath(action, model, key)

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		region = key.BedrockKeyConfig.Region.GetValue()
	}

	// Create HTTP request for streaming
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewReader(jsonData))
	if reqErr != nil {
		return nil, deployment, providerUtils.NewBifrostOperationError("error creating request", reqErr, providerName)
	}

	// Set any extra headers from network config
	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
	} else {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
		// Sign the request using either explicit credentials or IAM role authentication
		if err := signAWSRequest(ctx, req, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
			return nil, deployment, err
		}
	}

	// Make the request
	resp, respErr := provider.client.Do(req)
	if respErr != nil {
		if errors.Is(respErr, context.Canceled) {
			return nil, deployment, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   respErr,
				},
			}
		}
		// Check for timeout first using net.Error before checking net.OpError
		var netErr net.Error
		if errors.As(respErr, &netErr) && netErr.Timeout() {
			return nil, deployment, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, respErr, providerName)
		}
		if errors.Is(respErr, http.ErrHandlerTimeout) || errors.Is(respErr, context.DeadlineExceeded) {
			return nil, deployment, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, respErr, providerName)
		}
		// Check for DNS lookup and network errors after timeout checks
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if errors.As(respErr, &opErr) || errors.As(respErr, &dnsErr) {
			return nil, deployment, providerUtils.NewBifrostOperationError(schemas.ErrProviderNetworkError, respErr, providerName)
		}
		return nil, deployment, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, respErr, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, deployment, providerUtils.NewProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerName, nil, nil)
	}

	return resp, deployment, nil
}

// signAWSRequest signs an HTTP request using AWS Signature Version 4.
// It is used in providers like Bedrock.
// It sets required headers, calculates the request body hash, and signs the request
// using the provided AWS credentials.
// Returns a BifrostError if signing fails.
func signAWSRequest(ctx *schemas.BifrostContext, req *http.Request, accessKey, secretKey schemas.EnvVar, sessionToken *schemas.EnvVar, region, service string, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Set required headers before signing (only if not already set)
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	// Calculate SHA256 hash of the request body
	var bodyHash string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return providerUtils.NewBifrostOperationError("error reading request body", err, providerName)
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

	// Set x-amz-content-sha256 header (required for S3, harmless for other AWS services)
	req.Header.Set("x-amz-content-sha256", bodyHash)

	var cfg aws.Config
	var err error

	// If both accessKey and secretKey are empty, use the default credential provider chain
	// This will automatically use IAM roles, environment variables, shared credentials, etc.
	if accessKey.GetValue() == "" && secretKey.GetValue() == "" {
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
		)
	} else {
		// Use explicit credentials when provided
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
				creds := aws.Credentials{
					AccessKeyID:     accessKey.GetValue(),
					SecretAccessKey: secretKey.GetValue(),
				}
				if sessionToken != nil && sessionToken.GetValue() != "" {
					creds.SessionToken = sessionToken.GetValue()
				}
				return creds, nil
			})),
		)
	}
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to load aws config", err, providerName)
	}

	// Create the AWS signer
	signer := v4.NewSigner()

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to retrieve aws credentials", err, providerName)
	}

	// Sign the request with AWS Signature V4
	if err := signer.SignHTTP(ctx, creds, req, bodyHash, service, region, time.Now()); err != nil {
		return providerUtils.NewBifrostOperationError("failed to sign request", err, providerName)
	}

	return nil
}

// listModelsByKey performs a list models request to Bedrock's API for a single key.
// It retrieves all foundation models available in Amazon Bedrock for a specific key.
func (provider *BedrockProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	config := key.BedrockKeyConfig

	region := DefaultBedrockRegion
	if config.Region != nil && config.Region.GetValue() != "" {
		region = config.Region.GetValue()
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
	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
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
		}
		// Check for timeout first using net.Error before checking net.OpError
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		// Check for DNS lookup and network errors after timeout checks
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderNetworkError,
					Error:   err,
				},
			}
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderDoRequest,
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
		var errorResp BedrockError

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
	bedrockResponse := &BedrockListModelsResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, bedrockResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	response := bedrockResponse.ToBifrostListModelsResponse(providerName, key.Models, config.Deployments)
	if response == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert Bedrock model list response", nil, providerName)
	}

	response.ExtraFields.Latency = time.Since(startTime).Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Bedrock's API.
// It retrieves all foundation models available in Amazon Bedrock.
// Requests are made concurrently for improved performance.
func (provider *BedrockProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return providerUtils.HandleMultipleListModelsRequests(
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
func (provider *BedrockProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToBedrockTextCompletionRequest(request), nil },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	path, deployment := provider.getModelPath("invoke", request.Model, key)
	body, latency, err := provider.completeRequest(ctx, jsonData, path, key)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Handle model-specific response conversion
	var bifrostResponse *schemas.BifrostTextCompletionResponse
	switch {
	case schemas.IsAnthropicModel(deployment):
		var response BedrockAnthropicTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, providerUtils.NewBifrostOperationError("error parsing anthropic response", err, providerName)
		}
		bifrostResponse = response.ToBifrostTextCompletionResponse()

	case schemas.IsMistralModel(deployment):
		var response BedrockMistralTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, providerUtils.NewBifrostOperationError("error parsing mistral response", err, providerName)
		}
		bifrostResponse = response.ToBifrostTextCompletionResponse()

	default:
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("unsupported model type for text completion: %s", request.Model), providerName)
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ModelDeployment = deployment
	bifrostResponse.ExtraFields.RequestType = schemas.TextCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	// Parse raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError("error parsing raw response", err, providerName)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *BedrockProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.TextCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToBedrockTextCompletionRequest(request), nil },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	resp, deployment, bifrostErr := provider.makeStreamingRequest(ctx, jsonData, key, request.Model, "invoke-with-response-stream")
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.TextCompletionStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.TextCompletionStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer resp.Body.Close()
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.Body, provider.logger)
		defer stopCancellation()

		// Process AWS Event Stream format
		startTime := time.Now()
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// Decode a single EventStream message
			message, err := decoder.Decode(resp.Body, payloadBuf)
			if err != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				if err == io.EOF {
					// End of stream - this is normal
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("error decoding %s EventStream message: %v", providerName, err)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TextCompletionStreamRequest, providerName, request.Model, provider.logger)
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
						var bedrockErr BedrockError
						if err := sonic.Unmarshal(message.Payload, &bedrockErr); err == nil && bedrockErr.Message != "" {
							errMsg = bedrockErr.Message
						}
						err := fmt.Errorf("%s stream %s: %s", providerName, excType, errMsg)
						providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TextCompletionStreamRequest, providerName, request.Model, provider.logger)
						return
					}
				}

				// Parse the chunk payload
				var chunkPayload struct {
					Bytes []byte `json:"bytes"`
				}
				if err := sonic.Unmarshal(message.Payload, &chunkPayload); err != nil {
					provider.logger.Debug("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload))
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TextCompletionStreamRequest, providerName, request.Model, provider.logger)
					return
				}

				// Create BifrostStreamChunk response containing the raw model-specific JSON chunk
				textResponse := &schemas.BifrostTextCompletionResponse{
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:     schemas.TextCompletionStreamRequest,
						Provider:        providerName,
						ModelRequested:  request.Model,
						ModelDeployment: deployment,
						Latency:         time.Since(startTime).Milliseconds(),
						// Pass the raw JSON string from the chunk bytes
						RawResponse: string(chunkPayload.Bytes),
					},
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(textResponse, nil, nil, nil, nil, nil), responseChan)
			}
		}
	}()

	return responseChan, nil
}

// ChatCompletion performs a chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Use centralized Bedrock converter
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToBedrockChatCompletionRequest(ctx, request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Format the path with proper model identifier
	path, deployment := provider.getModelPath("converse", request.Model, key)

	// Create the signed request
	responseBody, latency, bifrostErr := provider.completeRequest(ctx, jsonData, path, key)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// pool the response
	bedrockResponse := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(bedrockResponse)

	// Parse the response using the new Bedrock type
	if err := sonic.Unmarshal(responseBody, bedrockResponse); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to parse bedrock response", err, providerName), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Convert using the new response converter
	bifrostResponse, err := bedrockResponse.ToBifrostChatResponse(ctx, request.Model)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to convert bedrock response", err, providerName), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Override finish reason for structured output
	// When structured output is used, tool_use is expected but should appear as "stop" to the client
	if _, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
		if len(bifrostResponse.Choices) > 0 && bifrostResponse.Choices[0].FinishReason != nil {
			if *bifrostResponse.Choices[0].FinishReason == string(schemas.BifrostFinishReasonToolCalls) {
				bifrostResponse.Choices[0].FinishReason = schemas.Ptr(string(schemas.BifrostFinishReasonStop))
			}
		}
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ModelDeployment = deployment
	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponse
		}
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the streaming response.
// Returns a channel for streaming BifrostStreamChunk objects or an error if the request fails.
func (provider *BedrockProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToBedrockChatCompletionRequest(ctx, request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	resp, deployment, bifrostErr := provider.makeStreamingRequest(ctx, jsonData, key, request.Model, "converse-stream")
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer resp.Body.Close()
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.Body, provider.logger)
		defer stopCancellation()

		// Process AWS Event Stream format
		usage := &schemas.BifrostLLMUsage{}
		var finishReason *string
		chunkIndex := 0

		// Process AWS Event Stream format using proper decoder
		startTime := time.Now()
		lastChunkTime := startTime
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

		// Bedrock does not provide a unique identifier for the stream, so we generate one ourselves
		id := uuid.New().String()

		// Check for structured output mode - if set, we need to intercept tool calls
		// and convert them to content instead of forwarding as tool calls
		var structuredOutputToolName string
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}
		var structuredOutputBuilder strings.Builder
		var isAccumulatingStructuredOutput bool

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// Decode a single EventStream message
			message, err := decoder.Decode(resp.Body, payloadBuf)
			if err != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				// End of stream - this is normal
				if err == io.EOF {
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error decoding %s EventStream message: %v", providerName, err)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
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
						providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
						return
					}
				}

				// Parse the JSON event into our typed structure
				var streamEvent BedrockStreamEvent
				if err := sonic.Unmarshal(message.Payload, &streamEvent); err != nil {
					provider.logger.Debug("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload))
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
					return
				}

				if streamEvent.Usage != nil {
					// Accumulate usage information instead of overwriting
					// In some cases usage comes in multiple events, so we need to take the maximum values
					if streamEvent.Usage.InputTokens > usage.PromptTokens {
						usage.PromptTokens = streamEvent.Usage.InputTokens
					}
					if streamEvent.Usage.OutputTokens > usage.CompletionTokens {
						usage.CompletionTokens = streamEvent.Usage.OutputTokens
					}
					if streamEvent.Usage.TotalTokens > usage.TotalTokens {
						usage.TotalTokens = streamEvent.Usage.TotalTokens
					}
					// Handle cached tokens if present
					if streamEvent.Usage.CacheReadInputTokens > 0 {
						if usage.PromptTokensDetails == nil {
							usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
						}
						if streamEvent.Usage.CacheReadInputTokens > usage.PromptTokensDetails.CachedTokens {
							usage.PromptTokensDetails.CachedTokens = streamEvent.Usage.CacheReadInputTokens
						}
					}
					if streamEvent.Usage.CacheWriteInputTokens > 0 {
						if usage.CompletionTokensDetails == nil {
							usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
						}
						if streamEvent.Usage.CacheWriteInputTokens > usage.CompletionTokensDetails.CachedTokens {
							usage.CompletionTokensDetails.CachedTokens = streamEvent.Usage.CacheWriteInputTokens
						}
					}
				}

				if streamEvent.StopReason != nil {
					finishReason = schemas.Ptr(anthropic.ConvertAnthropicFinishReasonToBifrost(anthropic.AnthropicStopReason(*streamEvent.StopReason)))

					// Override finish reason for structured output
					// When structured output is used, tool_use stop reason should appear as "stop" to the client
					if structuredOutputToolName != "" && *finishReason == string(schemas.BifrostFinishReasonToolCalls) {
						finishReason = schemas.Ptr(string(schemas.BifrostFinishReasonStop))
					}
				}

				// Handle structured output: intercept tool calls for the structured output tool
				// and convert them to content instead of forwarding as tool calls
				if structuredOutputToolName != "" {
					// Check for tool use start event
					if streamEvent.Start != nil && streamEvent.Start.ToolUse != nil {
						if streamEvent.Start.ToolUse.Name == structuredOutputToolName {
							// This is the structured output tool - start accumulating, don't forward
							isAccumulatingStructuredOutput = true
							continue
						}
					}

					// Check for tool use delta event
					if streamEvent.Delta != nil && streamEvent.Delta.ToolUse != nil && isAccumulatingStructuredOutput {
						// Accumulate the input for tracking purposes
						structuredOutputBuilder.WriteString(streamEvent.Delta.ToolUse.Input)

						// Convert tool use delta to content delta
						content := streamEvent.Delta.ToolUse.Input
						response := &schemas.BifrostChatResponse{
							ID:     id,
							Model:  request.Model,
							Object: "chat.completion.chunk",
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: &schemas.ChatStreamResponseChoiceDelta{
											Content: &content,
										},
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								RequestType:     schemas.ChatCompletionStreamRequest,
								Provider:        providerName,
								ModelRequested:  request.Model,
								ModelDeployment: deployment,
								ChunkIndex:      chunkIndex,
								Latency:         time.Since(lastChunkTime).Milliseconds(),
							},
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							response.ExtraFields.RawResponse = string(message.Payload)
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
						continue
					}
				}

				response, bifrostErr, _ := streamEvent.ToBifrostChatCompletionStream()
				if bifrostErr != nil {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
				if response != nil {
					response.ID = id
					response.Model = request.Model
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:     schemas.ChatCompletionStreamRequest,
						Provider:        providerName,
						ModelRequested:  request.Model,
						ModelDeployment: deployment,
						ChunkIndex:      chunkIndex,
						Latency:         time.Since(lastChunkTime).Milliseconds(),
					}
					chunkIndex++
					lastChunkTime = time.Now()

					if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
						response.ExtraFields.RawResponse = string(message.Payload)
					}

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
				}
			}
		}

		// Send final response
		response := providerUtils.CreateBifrostChatCompletionChunkResponse(id, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, request.Model)
		response.ExtraFields.ModelDeployment = deployment
		// Set raw request if enabled
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonData)
		}
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
	}()

	return responseChan, nil
}

// Responses performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Use centralized Bedrock converter
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToBedrockResponsesRequest(ctx, request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Format the path with proper model identifier
	path, deployment := provider.getModelPath("converse", request.Model, key)

	// Create the signed request
	responseBody, latency, bifrostErr := provider.completeRequest(ctx, jsonData, path, key)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// pool the response
	bedrockResponse := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(bedrockResponse)

	// Parse the response using the new Bedrock type
	if err := sonic.Unmarshal(responseBody, bedrockResponse); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to parse bedrock response", err, providerName), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Convert using the new response converter
	bifrostResponse, err := bedrockResponse.ToBifrostResponsesResponse(ctx)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to convert bedrock response", err, providerName), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	bifrostResponse.Model = deployment

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ModelDeployment = deployment
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
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
func (provider *BedrockProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToBedrockResponsesRequest(ctx, request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	resp, deployment, bifrostErr := provider.makeStreamingRequest(ctx, jsonData, key, request.Model, "converse-stream")
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ResponsesStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ResponsesStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		// Always release response on exit; bodyStream close should prevent indefinite blocking.
		defer resp.Body.Close()
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.Body, provider.logger)
		defer stopCancellation()

		// Process AWS Event Stream format
		usage := &schemas.ResponsesResponseUsage{}
		chunkIndex := 0

		// Create stream state for stateful conversions
		streamState := acquireBedrockResponsesStreamState()
		streamState.Model = &deployment
		defer releaseBedrockResponsesStreamState(streamState)

		// Check for structured output mode - if set, we need to intercept tool calls
		// and convert them to content instead of forwarding as tool calls
		var structuredOutputToolName string
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}
		var isAccumulatingStructuredOutput bool

		// Process AWS Event Stream format using proper decoder
		startTime := time.Now()
		lastChunkTime := startTime
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// Decode a single EventStream message
			message, err := decoder.Decode(resp.Body, payloadBuf)
			if err != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				if err == io.EOF {
					// End of stream - finalize any open items
					finalResponses := FinalizeBedrockStream(streamState, chunkIndex, usage)
					for i, finalResponse := range finalResponses {
						finalResponse.ExtraFields = schemas.BifrostResponseExtraFields{
							RequestType:     schemas.ResponsesStreamRequest,
							Provider:        providerName,
							ModelRequested:  request.Model,
							ModelDeployment: deployment,
							ChunkIndex:      chunkIndex,
							Latency:         time.Since(lastChunkTime).Milliseconds(),
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							finalResponse.ExtraFields.RawResponse = "{}" // Final event has no payload
						}

						if i == len(finalResponses)-1 {
							// Set raw request if enabled
							ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
							if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
								providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonData)
							}
							finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, finalResponse, nil, nil, nil), responseChan)
					}
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error decoding %s EventStream message: %v", providerName, err)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
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
						providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
						return
					}
				}

				// Parse the JSON event into our typed structure
				var streamEvent BedrockStreamEvent
				if err := sonic.Unmarshal(message.Payload, &streamEvent); err != nil {
					provider.logger.Debug("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload))
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
					return
				}

				if streamEvent.Usage != nil {
					// Accumulate usage information instead of overwriting
					// In some cases usage comes in multiple events, so we need to take the maximum values
					if streamEvent.Usage.InputTokens > usage.InputTokens {
						usage.InputTokens = streamEvent.Usage.InputTokens
					}
					if streamEvent.Usage.OutputTokens > usage.OutputTokens {
						usage.OutputTokens = streamEvent.Usage.OutputTokens
					}
					if streamEvent.Usage.TotalTokens > usage.TotalTokens {
						usage.TotalTokens = streamEvent.Usage.TotalTokens
					}
					// Handle cached tokens if present
					if streamEvent.Usage.CacheReadInputTokens > 0 {
						if usage.InputTokensDetails == nil {
							usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
						}
						if streamEvent.Usage.CacheReadInputTokens > usage.InputTokensDetails.CachedTokens {
							usage.InputTokensDetails.CachedTokens = streamEvent.Usage.CacheReadInputTokens
						}
					}
					if streamEvent.Usage.CacheWriteInputTokens > 0 {
						if usage.OutputTokensDetails == nil {
							usage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{}
						}
						if streamEvent.Usage.CacheWriteInputTokens > usage.OutputTokensDetails.CachedTokens {
							usage.OutputTokensDetails.CachedTokens = streamEvent.Usage.CacheWriteInputTokens
						}
					}
				}

				// Handle structured output: intercept tool calls for the structured output tool
				// and convert them to content instead of forwarding as tool calls
				if structuredOutputToolName != "" {
					// Check for tool use start event
					if streamEvent.Start != nil && streamEvent.Start.ToolUse != nil {
						if streamEvent.Start.ToolUse.Name == structuredOutputToolName {
							// This is the structured output tool - start accumulating, don't forward
							isAccumulatingStructuredOutput = true
							continue
						}
					}

					// Check for tool use delta event
					if streamEvent.Delta != nil && streamEvent.Delta.ToolUse != nil && isAccumulatingStructuredOutput {
						// Convert tool use delta to text delta
						content := streamEvent.Delta.ToolUse.Input
						response := &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
							SequenceNumber: chunkIndex,
							Delta:          &content,
							ExtraFields: schemas.BifrostResponseExtraFields{
								RequestType:     schemas.ResponsesStreamRequest,
								Provider:        providerName,
								ModelRequested:  request.Model,
								ModelDeployment: deployment,
								ChunkIndex:      chunkIndex,
								Latency:         time.Since(lastChunkTime).Milliseconds(),
							},
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							response.ExtraFields.RawResponse = string(message.Payload)
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
						continue
					}
				}

				responses, bifrostErr, _ := streamEvent.ToBifrostResponsesStream(chunkIndex, streamState)
				if bifrostErr != nil {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
				for _, response := range responses {
					if response != nil {
						response.ExtraFields = schemas.BifrostResponseExtraFields{
							RequestType:     schemas.ResponsesStreamRequest,
							Provider:        providerName,
							ModelRequested:  request.Model,
							ModelDeployment: deployment,
							ChunkIndex:      chunkIndex,
							Latency:         time.Since(lastChunkTime).Milliseconds(),
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							response.ExtraFields.RawResponse = string(message.Payload)
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
					}
				}
			}
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s) using Amazon Bedrock.
// Supports Titan and Cohere embedding models. Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *BedrockProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Determine model type
	modelType, err := DetermineEmbeddingModelType(request.Model)
	if err != nil {
		return nil, providerUtils.NewConfigurationError(err.Error(), providerName)
	}

	// Convert request and execute based on model type
	var rawResponse []byte
	var bifrostError *schemas.BifrostError
	var latency time.Duration
	var path string
	var deployment string
	var jsonData []byte

	switch modelType {
	case "titan":
		jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) { return ToBedrockTitanEmbeddingRequest(request) },
			provider.GetProviderKey())
		if bifrostError != nil {
			return nil, bifrostError
		}
		path, deployment = provider.getModelPath("invoke", request.Model, key)
		rawResponse, latency, bifrostError = provider.completeRequest(ctx, jsonData, path, key)

	case "cohere":
		jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) { return ToBedrockCohereEmbeddingRequest(request) },
			provider.GetProviderKey())
		if bifrostError != nil {
			return nil, bifrostError
		}
		path, deployment = provider.getModelPath("invoke", request.Model, key)
		rawResponse, latency, bifrostError = provider.completeRequest(ctx, jsonData, path, key)

	default:
		return nil, providerUtils.NewConfigurationError("unsupported embedding model type", providerName)
	}

	if bifrostError != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostError, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Parse response based on model type
	var bifrostResponse *schemas.BifrostEmbeddingResponse
	switch modelType {
	case "titan":
		var titanResp BedrockTitanEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &titanResp); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing Titan embedding response", err, providerName), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		bifrostResponse = titanResp.ToBifrostEmbeddingResponse()
		bifrostResponse.Model = request.Model

	case "cohere":
		var cohereResp cohere.CohereEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &cohereResp); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing Cohere embedding response", err, providerName), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		bifrostResponse = cohereResp.ToBifrostEmbeddingResponse()
		bifrostResponse.Model = request.Model
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ModelDeployment = deployment
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseData interface{}
		if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponseData
		}
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Bedrock provider.
func (provider *BedrockProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, schemas.Bedrock)
}

// SpeechStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, schemas.Bedrock)
}

// Transcription is not supported by the Bedrock provider.
func (provider *BedrockProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, schemas.Bedrock)
}

// TranscriptionStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, schemas.Bedrock)
}

// ImageGeneration generates images using Amazon Bedrock.
// Supports Titan Image Generator v1, Nova Canvas v1, and Titan Image Generator v2.
// Returns a BifrostImageGenerationResponse containing the generated images and any error that occurred.
func (provider *BedrockProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ImageGenerationRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	modelType := DetermineImageGenModelType(request.Model)
	var rawResponse []byte
	var jsonData []byte
	var bifrostError *schemas.BifrostError
	var latency time.Duration
	var path string
	var deployment string
	switch modelType {

	case "titan-image-generator-v1", "nova-canvas-v1:0", "titan-image-generator-v2:0":
		jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) { return ToBedrockImageGenerationRequest(request) },
			provider.GetProviderKey())
		if bifrostError != nil {
			return nil, bifrostError
		}
		path, deployment = provider.getModelPath("invoke", request.Model, key)
		rawResponse, latency, bifrostError = provider.completeRequest(ctx, jsonData, path, key)
	default:
		return nil, providerUtils.NewConfigurationError("unsupported image generation model type", providerName)
	}
	if bifrostError != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostError, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Parse response based on model type
	var bifrostResponse *schemas.BifrostImageGenerationResponse
	switch modelType {
	case "titan-image-generator-v1", "nova-canvas-v1:0", "titan-image-generator-v2:0":
		var imageResp BedrockImageGenerationResponse
		if err := sonic.Unmarshal(rawResponse, &imageResp); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing image generation response", err, providerName), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		if imageResp.Error != "" {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(imageResp.Error, nil, providerName), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		bifrostResponse = ToBifrostImageGenerationResponse(&imageResp)
		bifrostResponse.Model = request.Model
		bifrostResponse.ExtraFields.RequestType = schemas.ImageGenerationRequest
		bifrostResponse.ExtraFields.Provider = providerName
		bifrostResponse.ExtraFields.ModelRequested = request.Model
		bifrostResponse.ExtraFields.ModelDeployment = deployment
		bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

		// Set raw request if enabled
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
		}

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			var rawResponseData interface{}
			if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
				bifrostResponse.ExtraFields.RawResponse = rawResponseData
			}
		}

		return bifrostResponse, nil
	}
	return nil, providerUtils.NewConfigurationError("unsupported image generation model type", providerName)
}

// ImageGenerationStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, schemas.Bedrock)
}

// FileUpload uploads a file to S3 for Bedrock batch processing.
func (provider *BedrockProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {

	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		if err.Error != nil {
			provider.logger.Error("file upload operation not allowed: %s", err.Error.Message)
		}
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		provider.logger.Error("bedrock key config is is missing in file upload request")
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Get S3 bucket from storage config or extra params
	s3Bucket := ""
	s3Prefix := ""
	if request.StorageConfig != nil && request.StorageConfig.S3 != nil {
		if request.StorageConfig.S3.Bucket != "" {
			s3Bucket = request.StorageConfig.S3.Bucket
		}
		if request.StorageConfig.S3.Prefix != "" {
			s3Prefix = request.StorageConfig.S3.Prefix
		}
	} else if request.ExtraParams != nil {
		if bucket, ok := request.ExtraParams["s3_bucket"].(string); ok && bucket != "" {
			s3Bucket = bucket
		}
		if prefix, ok := request.ExtraParams["s3_prefix"].(string); ok && prefix != "" {
			s3Prefix = prefix
		}
	}

	if s3Bucket == "" {
		provider.logger.Error("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)")
		return nil, providerUtils.NewBifrostOperationError("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)", nil, providerName)
	}

	// Parse bucket name and optional prefix from s3Bucket (could be "bucket-name" or "s3://bucket-name/prefix/")
	bucketName, bucketPrefix := parseS3URI(s3Bucket)
	if bucketPrefix != "" {
		s3Prefix = bucketPrefix + s3Prefix
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		region = key.BedrockKeyConfig.Region.GetValue()
	}

	// Generate S3 key for the file
	filename := request.Filename
	if filename == "" {
		filename = fmt.Sprintf("file-%d.jsonl", time.Now().UnixNano())
	}

	cleanedPrefix := strings.Trim(s3Prefix, "/")
	s3Key := filename
	if cleanedPrefix != "" {
		s3Key = cleanedPrefix + "/" + filename
	}

	provider.logger.Debug("uploading file to s3: %s", s3Key)

	// Build S3 PUT request URL
	// Escape each path segment individually to handle special characters while preserving "/"
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(request.File))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	httpReq.Header.Set("Content-Type", "application/octet-stream")
	httpReq.ContentLength = int64(len(request.File))

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
		provider.logger.Error("error signing request: %s", err.Error.Message)
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
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
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		provider.logger.Error("s3 upload failed: %d", resp.StatusCode)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 upload failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
	}

	// Return S3 URI as the file ID
	s3URI := fmt.Sprintf("s3://%s/%s", bucketName, s3Key)

	return &schemas.BifrostFileUploadResponse{
		ID:             s3URI,
		Object:         "file",
		Bytes:          int64(len(request.File)),
		CreatedAt:      time.Now().Unix(),
		Filename:       filename,
		Purpose:        request.Purpose,
		Status:         schemas.FileStatusProcessed,
		StorageBackend: schemas.FileStorageS3,
		StorageURI:     s3URI,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileUploadRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileList lists files in the S3 bucket used for Bedrock batch processing from all provided keys.
// FileList lists S3 files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *BedrockProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Get S3 bucket from storage config or extra params
	s3Bucket := ""
	s3Prefix := ""
	if request.StorageConfig != nil && request.StorageConfig.S3 != nil {
		if request.StorageConfig.S3.Bucket != "" {
			s3Bucket = request.StorageConfig.S3.Bucket
		}
		if request.StorageConfig.S3.Prefix != "" {
			s3Prefix = request.StorageConfig.S3.Prefix
		}
	}
	if request.ExtraParams != nil {
		if bucket, ok := request.ExtraParams["s3_bucket"].(string); ok && bucket != "" {
			s3Bucket = bucket
		}
		if prefix, ok := request.ExtraParams["s3_prefix"].(string); ok && prefix != "" {
			s3Prefix = prefix
		}
	}

	if s3Bucket == "" {
		return nil, providerUtils.NewBifrostOperationError("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)", nil, providerName)
	}

	bucketName, bucketPrefix := parseS3URI(s3Bucket)
	if bucketPrefix != "" {
		s3Prefix = bucketPrefix + s3Prefix
	}

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostFileListResponse{
			Object:  "list",
			Data:    []schemas.FileObject{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileListRequest,
				Provider:    providerName,
			},
		}, nil
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig != nil {
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}
	}

	// Build S3 ListObjectsV2 request
	params := url.Values{}
	params.Set("list-type", "2")
	params.Set("prefix", s3Prefix)
	if request.Limit > 0 {
		params.Set("max-keys", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper
	if nativeCursor != "" {
		params.Set("continuation-token", nativeCursor)
	}

	requestURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/?%s", bucketName, region, params.Encode())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request for S3
	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}
	if bifrostErr := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
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
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, providerName)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 list failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
	}

	// Parse S3 ListObjectsV2 XML response
	var listResp S3ListObjectsResponse
	if err := parseS3ListResponse(body, &listResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError("error parsing S3 response", err, providerName)
	}

	// Convert files to Bifrost format
	files := make([]schemas.FileObject, 0, len(listResp.Contents))
	for _, obj := range listResp.Contents {
		s3URI := fmt.Sprintf("s3://%s/%s", bucketName, obj.Key)
		filename := obj.Key
		if idx := strings.LastIndex(obj.Key, "/"); idx >= 0 {
			filename = obj.Key[idx+1:]
		}
		files = append(files, schemas.FileObject{
			ID:        s3URI,
			Object:    "file",
			Bytes:     obj.Size,
			CreatedAt: obj.LastModified.Unix(),
			Filename:  filename,
			Purpose:   schemas.FilePurposeBatch,
			Status:    schemas.FileStatusProcessed,
		})
	}

	// Build cursor for next request
	// S3 uses NextContinuationToken for pagination
	nextCursor, hasMore := helper.BuildNextCursor(listResp.IsTruncated, listResp.NextContinuationToken)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    files,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.After = &nextCursor
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves S3 object metadata for Bedrock batch processing by trying each key until found.
func (provider *BedrockProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil, providerName)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if key.BedrockKeyConfig == nil {
			lastErr = providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
			continue
		}

		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// Build S3 HEAD request
		// Escape each path segment individually to handle special characters while preserving "/"
		reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodHead, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err, providerName)
			continue
		}

		// Sign request for S3
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := provider.client.Do(httpReq)
		latency := time.Since(startTime)
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
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = providerUtils.NewProviderAPIError(fmt.Sprintf("S3 HEAD failed with status %d", resp.StatusCode), nil, resp.StatusCode, providerName, nil, nil)
			continue
		}

		resp.Body.Close()

		// Extract metadata from headers
		filename := s3Key
		if idx := strings.LastIndex(s3Key, "/"); idx >= 0 {
			filename = s3Key[idx+1:]
		}

		var createdAt int64
		if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
			if t, err := time.Parse(time.RFC1123, lastMod); err == nil {
				createdAt = t.Unix()
			}
		}

		return &schemas.BifrostFileRetrieveResponse{
			ID:             request.FileID,
			Object:         "file",
			Bytes:          resp.ContentLength,
			CreatedAt:      createdAt,
			Filename:       filename,
			Purpose:        schemas.FilePurposeBatch,
			Status:         schemas.FileStatusProcessed,
			StorageBackend: schemas.FileStorageS3,
			StorageURI:     request.FileID,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileRetrieveRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// FileDelete deletes an S3 object used for Bedrock batch processing by trying each key until successful.
func (provider *BedrockProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil, providerName)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if key.BedrockKeyConfig == nil {
			lastErr = providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
			continue
		}

		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// Build S3 DELETE request
		// Escape each path segment individually to handle special characters while preserving "/"
		reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err, providerName)
			continue
		}

		// Sign request for S3
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := provider.client.Do(httpReq)
		latency := time.Since(startTime)
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
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
			continue
		}

		// S3 DELETE returns 204 No Content on success
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = providerUtils.NewProviderAPIError(fmt.Sprintf("S3 DELETE failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
			continue
		}

		resp.Body.Close()

		return &schemas.BifrostFileDeleteResponse{
			ID:      request.FileID,
			Object:  "file",
			Deleted: true,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileDeleteRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// FileContent downloads S3 object content for Bedrock batch processing by trying each key until found.
func (provider *BedrockProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil, providerName)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if key.BedrockKeyConfig == nil {
			lastErr = providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
			continue
		}

		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// Build S3 GET request
		// Escape each path segment individually to handle special characters while preserving "/"
		reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err, providerName)
			continue
		}

		// Sign request for S3
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", providerName); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := provider.client.Do(httpReq)
		latency := time.Since(startTime)
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
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = providerUtils.NewProviderAPIError(fmt.Sprintf("S3 GET failed: %s", string(body)), nil, resp.StatusCode, providerName, nil, nil)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error reading S3 object content", err, providerName)
			continue
		}

		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		return &schemas.BifrostFileContentResponse{
			FileID:      request.FileID,
			Content:     body,
			ContentType: contentType,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileContentRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// BatchCreate creates a new batch inference job on AWS Bedrock.
func (provider *BedrockProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		provider.logger.Error("batch create is not allowed for Bedrock provider", "error", err)
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		provider.logger.Error("bedrock key config is not provided")
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Require RoleArn in extra params
	roleArn := ""
	// First we will honor the role_arn coming from the client side if present
	if request.ExtraParams != nil {
		if r, ok := request.ExtraParams["role_arn"].(string); ok {
			roleArn = r
		}
	}
	// If its empty then we will honor the role_arn from the key config
	if roleArn == "" {
		if key.BedrockKeyConfig.ARN != nil {
			roleArn = key.BedrockKeyConfig.ARN.GetValue()
		}
	}
	// And if still we don't get role ARN
	if roleArn == "" {
		provider.logger.Error("role_arn is required for Bedrock batch API (provide in extra_params)")
		return nil, providerUtils.NewBifrostOperationError("role_arn is required for Bedrock batch API (provide in extra_params)", nil, providerName)
	}
	// Get output S3 URI from extra params
	outputS3Uri := ""
	if request.ExtraParams != nil {
		if o, ok := request.ExtraParams["output_s3_uri"].(string); ok {
			outputS3Uri = o
		}
	}
	if outputS3Uri == "" {
		provider.logger.Error("output_s3_uri is required for Bedrock batch API (provide in extra_params)")
		return nil, providerUtils.NewBifrostOperationError("output_s3_uri is required for Bedrock batch API (provide in extra_params)", nil, providerName)
	}

	if request.Model == nil {
		provider.logger.Error("model is required for Bedrock batch API")
		return nil, providerUtils.NewBifrostOperationError("model is required for Bedrock batch API", nil, providerName)
	}

	// Get model ID

	var modelID *string
	if key.BedrockKeyConfig.Deployments != nil && request.Model != nil {
		if deployment, ok := key.BedrockKeyConfig.Deployments[*request.Model]; ok {
			modelID = schemas.Ptr(deployment)
		}
	}
	if modelID == nil {
		modelID = request.Model
	}

	// Generate job name
	jobName := fmt.Sprintf("bifrost-batch-%d", time.Now().Unix())
	if request.Metadata != nil {
		if name, ok := request.Metadata["job_name"]; ok {
			jobName = name
		}
	}

	// Determine input file ID (S3 URI)
	inputFileID := request.InputFileID

	// If no S3 URI provided but inline requests are available, upload them to S3 first
	if inputFileID == "" && len(request.Requests) > 0 {
		// Get region for S3 upload
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		var sessionKey *string
		if key.BedrockKeyConfig.SessionToken != nil && key.BedrockKeyConfig.SessionToken.GetValue() != "" {
			sessionKey = schemas.Ptr(key.BedrockKeyConfig.SessionToken.GetValue())
		}

		// Convert inline requests to Bedrock JSONL format
		jsonlData, err := ConvertBedrockRequestsToJSONL(request.Requests, modelID)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to convert requests to JSONL", err, providerName)
		}

		// Generate S3 key for the input file
		inputKey := generateBatchInputS3Key(jobName)

		// Derive bucket from output S3 URI
		inputS3URI := deriveInputS3URIFromOutput(outputS3Uri, inputKey)
		bucket, s3Key := parseS3URI(inputS3URI)

		// Upload to S3 using Bedrock credentials
		if bifrostErr := uploadToS3(
			ctx,
			key.BedrockKeyConfig.AccessKey.GetValue(),
			key.BedrockKeyConfig.SecretKey.GetValue(),
			sessionKey,
			region,
			bucket,
			s3Key,
			jsonlData,
			providerName,
		); bifrostErr != nil {
			return nil, bifrostErr
		}

		inputFileID = inputS3URI
	}

	// Validate that we have an input file ID (either provided or uploaded)
	if inputFileID == "" {
		provider.logger.Error("either input_file_id (S3 URI) or requests array is required for Bedrock batch API")
		return nil, providerUtils.NewBifrostOperationError("either input_file_id (S3 URI) or requests array is required for Bedrock batch API", nil, providerName)
	}

	// Build request
	bedrockReq := &BedrockBatchJobRequest{
		JobName: jobName,
		ModelID: modelID,
		RoleArn: roleArn,
		InputDataConfig: BedrockInputDataConfig{
			S3InputDataConfig: BedrockS3InputDataConfig{
				S3Uri:         inputFileID,
				S3InputFormat: "JSONL",
			},
		},
		OutputDataConfig: BedrockOutputDataConfig{
			S3OutputDataConfig: BedrockS3OutputDataConfig{
				S3Uri: outputS3Uri,
			},
		},
	}

	// Set timeout if provided
	if request.CompletionWindow != "" {
		// Parse completion window (e.g., "24h" -> 24)
		if d, err := time.ParseDuration(request.CompletionWindow); err == nil {
			bedrockReq.TimeoutDurationInHours = int(d.Hours())
		}
	}

	jsonData, err := sonic.Marshal(bedrockReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	sendBackRawRequest := provider.sendBackRawRequest
	sendBackRawResponse := provider.sendBackRawResponse

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		region = key.BedrockKeyConfig.Region.GetValue()
	}

	// Create HTTP request
	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job", region)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error creating request", err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Sign request
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error reading response", err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errorResp BedrockError
		if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil), jsonData, body, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil), jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	var bedrockResp BedrockBatchJobResponse
	if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName), jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	// AWS CreateModelInvocationJob only returns jobArn, not status or other details.
	// Retrieve the job to get full status details.
	retrieveResp, bifrostErr := provider.BatchRetrieve(ctx, []schemas.Key{key}, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  bedrockResp.JobArn,
	})
	if bifrostErr != nil {
		// Return basic response if retrieve fails
		return &schemas.BifrostBatchCreateResponse{
			ID:          bedrockResp.JobArn,
			Object:      "batch",
			InputFileID: inputFileID,
			Status:      schemas.BatchStatusValidating,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchCreateRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	// Use retrieved response for complete data
	result := &schemas.BifrostBatchCreateResponse{
		ID:          retrieveResp.ID,
		Object:      "batch",
		InputFileID: inputFileID,
		Status:      retrieveResp.Status,
		CreatedAt:   retrieveResp.CreatedAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCreateRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if retrieveResp.ExpiresAt != nil {
		result.ExpiresAt = retrieveResp.ExpiresAt
	}

	return result, nil
}

// BatchList lists batch inference jobs using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *BedrockProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Initialize serial pagination helper (Bedrock uses PageToken for pagination)
	helper, err := providerUtils.NewSerialListHelper(keys, request.PageToken, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostBatchListResponse{
			Object:  "list",
			Data:    []schemas.BifrostBatchRetrieveResponse{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchListRequest,
				Provider:    providerName,
			},
		}, nil
	}

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		region = key.BedrockKeyConfig.Region.GetValue()
	}

	// Build URL with query params
	params := url.Values{}
	if request.Limit > 0 {
		params.Set("maxResults", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper
	if nativeCursor != "" {
		params.Set("nextToken", nativeCursor)
	}

	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-jobs", region)
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request
	if bifrostErr := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
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
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, providerName)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp BedrockError
		if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
			return nil, providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil)
		}
		return nil, providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil)
	}

	var bedrockResp BedrockBatchJobListResponse
	if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Convert batches to Bifrost format
	batches := make([]schemas.BifrostBatchRetrieveResponse, 0, len(bedrockResp.InvocationJobSummaries))
	for _, job := range bedrockResp.InvocationJobSummaries {
		var createdAt int64
		if job.SubmitTime != nil {
			createdAt = job.SubmitTime.Unix()
		}

		// Store Bedrock-specific fields in Metadata for later conversion back to Bedrock format
		metadata := make(map[string]string)
		if job.JobName != "" {
			metadata["job_name"] = job.JobName
		}
		if job.ModelID != "" {
			metadata["model_id"] = job.ModelID
		}

		batches = append(batches, schemas.BifrostBatchRetrieveResponse{
			ID:        job.JobArn,
			Object:    "batch",
			Status:    ToBifrostBatchStatus(job.Status),
			CreatedAt: createdAt,
			Metadata:  metadata,
		})
	}

	// Build cursor for next request
	// Bedrock uses NextToken for pagination
	nativeNextToken := ""
	apiHasMore := false
	if bedrockResp.NextToken != nil && *bedrockResp.NextToken != "" {
		nativeNextToken = *bedrockResp.NextToken
		apiHasMore = true
	}
	nextCursor, hasMore := helper.BuildNextCursor(apiHasMore, nativeNextToken)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  "list",
		Data:    batches,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.NextCursor = &nextCursor
	}

	return bifrostResp, nil
}

// fetchBatchManifest fetches the manifest.json.out from S3 to get record counts.
// Returns nil if manifest doesn't exist (job still in progress) or on error.
func (provider *BedrockProvider) fetchBatchManifest(ctx *schemas.BifrostContext, key schemas.Key, region, outputS3Uri string) *BedrockBatchManifest {
	if outputS3Uri == "" {
		return nil
	}

	// Parse the output S3 URI and construct manifest path
	bucketName, prefix := parseS3URI(outputS3Uri)
	if bucketName == "" {
		return nil
	}

	// Manifest is at: {output_s3_uri}/manifest.json.out
	base := strings.Trim(prefix, "/")
	manifestKey := "manifest.json.out"
	if base != "" {
		manifestKey = base + "/manifest.json.out"
	}

	// Build S3 GET request
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(manifestKey))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		provider.logger.Error("failed to create manifest request: %v", err)
		return nil
	}

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "s3", provider.GetProviderKey()); err != nil {
		provider.logger.Error("failed to sign manifest request: %v", err)
		return nil
	}

	resp, err := provider.client.Do(httpReq)
	if err != nil {
		provider.logger.Error("failed to fetch manifest: %v", err)
		return nil
	}
	defer resp.Body.Close()

	// 404 is expected if job is still in progress
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		provider.logger.Debug("failed to read manifest body: %v", err)
		return nil
	}

	var manifest BedrockBatchManifest
	if err := sonic.Unmarshal(body, &manifest); err != nil {
		provider.logger.Error("failed to parse manifest: %v", err)
		return nil
	}

	return &manifest
}

// BatchRetrieve retrieves a specific batch inference job from AWS Bedrock by trying each key until found.
func (provider *BedrockProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id (job ARN) is required", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if key.BedrockKeyConfig == nil {
			lastErr = providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
			continue
		}

		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// URL encode the job ARN
		encodedJobArn := url.PathEscape(request.BatchID)
		reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job/%s", region, encodedJobArn)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err, providerName)
			continue
		}

		// Sign request
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := provider.client.Do(httpReq)
		latency := time.Since(startTime)
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
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error reading response", err, providerName)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			var errorResp BedrockError
			if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
				lastErr = providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil)
			} else {
				lastErr = providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil)
			}
			continue
		}

		var bedrockResp BedrockBatchJobResponse
		if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
			continue
		}

		// Store Bedrock-specific fields in Metadata for later conversion back to Bedrock format
		metadata := make(map[string]string)
		if bedrockResp.JobName != "" {
			metadata["job_name"] = bedrockResp.JobName
		}
		if bedrockResp.ModelID != "" {
			metadata["model_id"] = bedrockResp.ModelID
		}

		result := &schemas.BifrostBatchRetrieveResponse{
			ID:       bedrockResp.JobArn,
			Object:   "batch",
			Status:   ToBifrostBatchStatus(bedrockResp.Status),
			Metadata: metadata,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchRetrieveRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}

		if bedrockResp.InputDataConfig != nil {
			result.InputFileID = bedrockResp.InputDataConfig.S3InputDataConfig.S3Uri
		}

		if bedrockResp.OutputDataConfig != nil {
			outputURI := bedrockResp.OutputDataConfig.S3OutputDataConfig.S3Uri
			result.OutputFileID = &outputURI
			// Fetch manifest to get record counts (only available after job starts processing)
			manifest := provider.fetchBatchManifest(ctx, key, region, outputURI)
			if manifest != nil {
				result.RequestCounts = schemas.BatchRequestCounts{
					Total:     manifest.TotalRecordCount,
					Completed: manifest.ProcessedRecordCount - manifest.ErrorRecordCount,
					Failed:    manifest.ErrorRecordCount,
				}
			}
		}

		// Capture VPC config in metadata if present
		if bedrockResp.VpcConfig != nil {
			if len(bedrockResp.VpcConfig.SecurityGroupIds) > 0 {
				metadata["vpc_security_group_ids"] = strings.Join(bedrockResp.VpcConfig.SecurityGroupIds, ",")
			}
			if len(bedrockResp.VpcConfig.SubnetIds) > 0 {
				metadata["vpc_subnet_ids"] = strings.Join(bedrockResp.VpcConfig.SubnetIds, ",")
			}
		}

		if bedrockResp.SubmitTime != nil {
			result.CreatedAt = bedrockResp.SubmitTime.Unix()
		}
		if bedrockResp.EndTime != nil {
			completedAt := bedrockResp.EndTime.Unix()
			result.CompletedAt = &completedAt
		}
		if bedrockResp.JobExpirationTime != nil {
			expiresAt := bedrockResp.JobExpirationTime.Unix()
			result.ExpiresAt = &expiresAt
		}

		return result, nil
	}

	return nil, lastErr
}

// BatchCancel stops a batch inference job on AWS Bedrock by trying each key until successful.
func (provider *BedrockProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id (job ARN) is required", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if key.BedrockKeyConfig == nil {
			lastErr = providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
			continue
		}

		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// URL encode the job ARN
		encodedJobArn := url.PathEscape(request.BatchID)
		reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job/%s/stop", region, encodedJobArn)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err, providerName)
			continue
		}

		// Sign request
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := provider.client.Do(httpReq)
		latency := time.Since(startTime)
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
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error reading response", err, providerName)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			var errorResp BedrockError
			if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
				lastErr = providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil)
			} else {
				lastErr = providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil)
			}
			continue
		}

		// After stopping, retrieve the job to get updated status
		retrieveResp, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
			Provider: request.Provider,
			BatchID:  request.BatchID,
		})
		if bifrostErr != nil {
			// Return basic response if retrieve fails
			// Compute total latency including stop + failed retrieve
			totalLatency := time.Since(startTime)
			return &schemas.BifrostBatchCancelResponse{
				ID:     request.BatchID,
				Object: "batch",
				Status: schemas.BatchStatusCancelling,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: schemas.BatchCancelRequest,
					Provider:    providerName,
					Latency:     totalLatency.Milliseconds(),
				},
			}, nil
		}

		return &schemas.BifrostBatchCancelResponse{
			ID:     retrieveResp.ID,
			Object: "batch",
			Status: retrieveResp.Status,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchCancelRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// BatchResults retrieves batch results from AWS Bedrock by trying each key until successful.
// For Bedrock, results are stored in S3 at the output S3 URI prefix.
// The output includes JSONL files with results (*.jsonl.out) and a manifest file.
func (provider *BedrockProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// First, retrieve the batch to get the output S3 URI prefix (using all keys)
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if batchResp.OutputFileID == nil || *batchResp.OutputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: output S3 URI is empty (batch may not be completed)", nil, providerName)
	}

	outputS3URI := *batchResp.OutputFileID
	var allResults []schemas.BatchResultItem
	var totalLatency int64
	// The output S3 URI is a prefix/folder. List files in that folder to find output JSONL files.
	var (
		listResp  *schemas.BifrostFileListResponse
		pageToken *string
		allFiles  []schemas.FileObject
	)
	for {
		listResp, bifrostErr = provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{
			Provider: request.Provider,
			StorageConfig: &schemas.FileStorageConfig{
				S3: &schemas.S3StorageConfig{
					Bucket: outputS3URI,
				},
			},
			Limit: 100,
			After: pageToken,
		})
		if bifrostErr != nil {
			break
		}
		totalLatency += listResp.ExtraFields.Latency
		allFiles = append(allFiles, listResp.Data...)
		if !listResp.HasMore || listResp.After == nil {
			break
		}
		pageToken = listResp.After
	}
	if bifrostErr != nil {
		// If listing fails, try direct download (in case outputS3URI is already a file path)
		fileContentResp, directErr := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{
			Provider: request.Provider,
			FileID:   outputS3URI,
		})
		if directErr != nil {
			return nil, providerUtils.NewBifrostOperationError(
				fmt.Sprintf("failed to access batch results at %s: listing failed and direct access failed", outputS3URI),
				nil, providerName)
		}

		// Direct download succeeded, parse the content
		results, parseErrors := parseBatchResultsJSONL(fileContentResp.Content, provider)
		batchResultsResp := &schemas.BifrostBatchResultsResponse{
			BatchID: request.BatchID,
			Results: results,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchResultsRequest,
				Provider:    providerName,
				Latency:     fileContentResp.ExtraFields.Latency,
			},
		}
		if len(parseErrors) > 0 {
			batchResultsResp.ExtraFields.ParseErrors = parseErrors
		}
		return batchResultsResp, nil
	}
	// Find and download JSONL output files (files ending with .jsonl.out or containing results)
	var allParseErrors []schemas.BatchError
	for _, file := range allFiles {
		// Skip manifest files, only process JSONL output files
		if strings.HasSuffix(file.ID, ".jsonl.out") || strings.HasSuffix(file.ID, ".jsonl") {
			fileContentResp, fileErr := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{
				Provider: request.Provider,
				FileID:   file.ID,
			})
			if fileErr != nil {
				provider.logger.Warn("failed to download batch result file %s: %v", file.ID, fileErr)
				continue
			}

			totalLatency += fileContentResp.ExtraFields.Latency
			results, parseErrors := parseBatchResultsJSONL(fileContentResp.Content, provider)
			allResults = append(allResults, results...)
			allParseErrors = append(allParseErrors, parseErrors...)
		}
	}

	batchResultsResp := &schemas.BifrostBatchResultsResponse{
		BatchID: request.BatchID,
		Results: allResults,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchResultsRequest,
			Provider:    providerName,
			Latency:     totalLatency,
		},
	}

	if len(allParseErrors) > 0 {
		batchResultsResp.ExtraFields.ParseErrors = allParseErrors
	}

	return batchResultsResp, nil
}

func (provider *BedrockProvider) getModelPath(basePath string, model string, key schemas.Key) (string, string) {
	deployment := model
	if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.Deployments != nil {
		if mapped, ok := key.BedrockKeyConfig.Deployments[model]; ok && mapped != "" {
			deployment = mapped
		}
	}
	// Default: use model/deployment directly
	path := fmt.Sprintf("%s/%s", deployment, basePath)
	// If ARN is present, Bedrock expects the ARN-scoped identifier
	if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.ARN != nil && key.BedrockKeyConfig.ARN.GetValue() != "" {
		encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", key.BedrockKeyConfig.ARN.GetValue(), deployment))
		path = fmt.Sprintf("%s/%s", encodedModelIdentifier, basePath)
	}
	return path, deployment
}

func (provider *BedrockProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}
