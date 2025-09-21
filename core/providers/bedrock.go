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
	"maps"
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
)

// BedrockAnthropicTextResponse represents the response structure from Bedrock's Anthropic text completion API.
// It includes the completion text and stop reason information.
type BedrockAnthropicTextResponse struct {
	Completion string `json:"completion"`  // Generated completion text
	StopReason string `json:"stop_reason"` // Reason for completion termination
	Stop       string `json:"stop"`        // Stop sequence that caused completion to stop
}

// BedrockMistralTextResponse represents the response structure from Bedrock's Mistral text completion API.
// It includes multiple output choices with their text and stop reasons.
type BedrockMistralTextResponse struct {
	Outputs []struct {
		Text       string `json:"text"`        // Generated text
		StopReason string `json:"stop_reason"` // Reason for completion termination
	} `json:"outputs"` // Array of output choices
}

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
// Returns the response body or an error if the request fails.
func (provider *BedrockProvider) completeRequest(ctx context.Context, requestBody interface{}, path string, key schemas.Key) ([]byte, *schemas.BifrostError) {
	config := key.BedrockKeyConfig

	region := "us-east-1"
	if config.Region != nil {
		region = *config.Region
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: schemas.ErrorField{
					Type:    Ptr(schemas.RequestCancelled),
					Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
					Error:   err,
				},
			}
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	// Create the request with the JSON body
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
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
			return nil, err
		}
	}

	// Execute the request
	resp, err := provider.client.Do(req)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp bedrock.BedrockError

		if err := sonic.Unmarshal(body, &errorResp); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &resp.StatusCode,
				Error: schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
			}
		}

		return nil, &schemas.BifrostError{
			StatusCode: &resp.StatusCode,
			Error: schemas.ErrorField{
				Message: errorResp.Message,
			},
		}
	}

	return body, nil
}

// GetTextCompletionResult processes the text completion response from Bedrock.
// It handles different model types (Anthropic and Mistral) and formats the response.
// Returns a BifrostResponse containing the completion results or an error if processing fails.
func (provider *BedrockProvider) getTextCompletionResult(result []byte, model string, providerName schemas.ModelProvider) (*schemas.BifrostResponse, *schemas.BifrostError) {
	switch {
	case strings.Contains(model, "anthropic."):
		var response BedrockAnthropicTextResponse
		if err := sonic.Unmarshal(result, &response); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: schemas.ErrorField{
					Message: "error parsing response",
					Error:   err,
				},
			}
		}

		return &schemas.BifrostResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
						Message: schemas.BifrostMessage{
							Role: schemas.ModelChatMessageRoleAssistant,
							Content: schemas.MessageContent{
								ContentStr: &response.Completion,
							},
						},
						StopString: &response.Stop,
					},
					FinishReason: &response.StopReason,
				},
			},
			Model: model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider: providerName,
			},
		}, nil

	case strings.Contains(model, "mistral."):
		var response BedrockMistralTextResponse
		if err := sonic.Unmarshal(result, &response); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: schemas.ErrorField{
					Message: "error parsing response",
					Error:   err,
				},
			}
		}

		var choices []schemas.BifrostResponseChoice
		for i, output := range response.Outputs {
			choices = append(choices, schemas.BifrostResponseChoice{
				Index: i,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleAssistant,
						Content: schemas.MessageContent{
							ContentStr: &output.Text,
						},
					},
				},
				FinishReason: &output.StopReason,
			})
		}

		return &schemas.BifrostResponse{
			Choices: choices,
			Model:   model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider: providerName,
			},
		}, nil
	}

	return nil, newConfigurationError(fmt.Sprintf("invalid model choice: %s", model), providerName)
}

// prepareTextCompletionParams prepares text completion parameters for Bedrock's API.
// It handles parameter mapping and conversion for different model types.
// Returns the modified parameters map with model-specific adjustments.
func (provider *BedrockProvider) prepareTextCompletionParams(params map[string]interface{}, model string) map[string]interface{} {
	switch {
	case strings.Contains(model, "anthropic."):
		maxTokens, maxTokensExists := params["max_tokens"]
		if _, exists := params["max_tokens_to_sample"]; !exists {
			// If max_tokens_to_sample is not present, rename max_tokens to max_tokens_to_sample
			if maxTokensExists {
				params["max_tokens_to_sample"] = maxTokens
			} else {
				params["max_tokens_to_sample"] = AnthropicDefaultMaxTokens
			}
		}

		delete(params, "max_tokens")
	}
	return params
}

// TextCompletion performs a text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) TextCompletion(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationTextCompletion); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}
	
	preparedParams := provider.prepareTextCompletionParams(prepareParams(input.Params), input.Model)

	requestBody := mergeConfig(map[string]interface{}{
		"prompt": *input.Input.TextCompletionInput,
	}, preparedParams)

	path := provider.getModelPath("invoke", input.Model, key)
	body, err := provider.completeRequest(ctx, requestBody, path, key)
	if err != nil {
		return nil, err
	}

	bifrostResponse, err := provider.getTextCompletionResult(body, input.Model, providerName)
	if err != nil {
		return nil, err
	}

	// Parse raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			return nil, newBifrostOperationError("error parsing raw response", err, providerName)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if input.Params != nil {
		bifrostResponse.ExtraFields.Params = *input.Params
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) ChatCompletion(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationChatCompletion); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	// pool the request
	bedrockReq, err := bedrock.ConvertBifrostRequestToBedrock(input)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert request", err, providerName)
	}

	if bedrockReq == nil {
		return nil, newBifrostOperationError("failed to convert request", fmt.Errorf("conversion returned nil"), providerName)
	}

	// Format the path with proper model identifier
	path := provider.getModelPath("converse", input.Model, key)

	// Create the signed request
	responseBody, bifrostErr := provider.completeRequest(ctx, bedrockReq, path, key)
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
	bifrostResponse, err := bedrock.ConvertBedrockResponseToBifrost(bedrockResponse, input.Model, providerName)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert bedrock response", err, providerName)
	}

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponse
		}
	}

	if input.Params != nil {
		bifrostResponse.ExtraFields.Params = *input.Params
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
func (provider *BedrockProvider) Embedding(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationEmbedding); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	embeddingInput := input.Input.EmbeddingInput

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	switch {
	case strings.Contains(input.Model, "amazon.titan-embed-text"):
		return provider.handleTitanEmbedding(ctx, input.Model, key, embeddingInput, input.Params, providerName)
	case strings.Contains(input.Model, "cohere.embed"):
		return provider.handleCohereEmbedding(ctx, input.Model, key, embeddingInput, input.Params, providerName)
	default:
		return nil, newConfigurationError("embedding is not supported for this Bedrock model", providerName)
	}
}

// handleTitanEmbedding handles embedding requests for Amazon Titan models.
func (provider *BedrockProvider) handleTitanEmbedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters, providerName schemas.ModelProvider) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Titan Text Embeddings V1/V2 - only supports single text input
	if len(input.Texts) == 0 {
		return nil, newConfigurationError("no input text provided for embedding", providerName)
	}
	if len(input.Texts) > 1 {
		return nil, newConfigurationError("Amazon Titan embedding models support only single text input, received multiple texts", providerName)
	}

	requestBody := map[string]interface{}{
		"inputText": input.Texts[0],
	}

	if params != nil {
		// Titan models do not support the dimensions parameter - they have fixed dimensions
		if params.Dimensions != nil {
			return nil, newConfigurationError("Amazon Titan embedding models do not support custom dimensions parameter", providerName)
		}
		if params.ExtraParams != nil {
			for k, v := range params.ExtraParams {
				requestBody[k] = v
			}
		}
	}

	// Properly escape model name for URL path to ensure AWS SIGv4 signing works correctly
	path := provider.getModelPath("invoke", model, key)
	rawResponse, err := provider.completeRequest(ctx, requestBody, path, key)
	if err != nil {
		return nil, err
	}

	// Parse Titan response from raw message
	var titanResp struct {
		Embedding           []float32 `json:"embedding"`
		InputTextTokenCount int       `json:"inputTextTokenCount"`
	}
	if err := sonic.Unmarshal(rawResponse, &titanResp); err != nil {
		return nil, newBifrostOperationError("error parsing Titan embedding response", err, providerName)
	}

	bifrostResponse := &schemas.BifrostResponse{
		Object: "list",
		Data: []schemas.BifrostEmbedding{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.BifrostEmbeddingResponse{
					Embedding2DArray: &[][]float32{titanResp.Embedding},
				},
			},
		},
		Model: model,
		Usage: &schemas.LLMUsage{
			PromptTokens: titanResp.InputTextTokenCount,
			TotalTokens:  titanResp.InputTextTokenCount,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
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

// handleCohereEmbedding handles embedding requests for Cohere models on Bedrock.
func (provider *BedrockProvider) handleCohereEmbedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters, providerName schemas.ModelProvider) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if len(input.Texts) == 0 {
		return nil, newConfigurationError("no input text provided for embedding", providerName)
	}

	requestBody := map[string]interface{}{
		"texts":      input.Texts,
		"input_type": "search_document",
	}
	if params != nil && params.ExtraParams != nil {
		maps.Copy(requestBody, params.ExtraParams)
	}

	// Properly escape model name for URL path to ensure AWS SIGv4 signing works correctly
	path := provider.getModelPath("invoke", model, key)
	rawResponse, err := provider.completeRequest(ctx, requestBody, path, key)
	if err != nil {
		return nil, err
	}

	// Parse Cohere response
	var cohereResp struct {
		Embeddings [][]float32 `json:"embeddings"`
		ID         string      `json:"id"`
		Texts      []string    `json:"texts"`
	}
	if err := sonic.Unmarshal(rawResponse, &cohereResp); err != nil {
		return nil, newBifrostOperationError("error parsing embedding response", err, providerName)
	}

	// Calculate token usage based on input texts (approximation since Cohere doesn't provide this)
	totalInputTokens := approximateTokenCount(input.Texts)

	bifrostResponse := &schemas.BifrostResponse{
		Object: "list",
		Data: []schemas.BifrostEmbedding{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.BifrostEmbeddingResponse{
					Embedding2DArray: &cohereResp.Embeddings,
				},
			},
		},
		ID:    cohereResp.ID,
		Model: model,
		Usage: &schemas.LLMUsage{
			PromptTokens: totalInputTokens,
			TotalTokens:  totalInputTokens,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
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

// ChatCompletionStream performs a streaming chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the streaming response.
// Returns a channel for streaming BifrostResponse objects or an error if the request fails.
func (provider *BedrockProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.OperationChatCompletionStream); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, newConfigurationError("bedrock key config is not provided", providerName)
	}

	bedrockReq, err := bedrock.ConvertBifrostRequestToBedrock(input)
	if err != nil {
		return nil, newBifrostOperationError("failed to convert request", err, providerName)
	}

	// Format the path with proper model identifier for streaming
	path := provider.getModelPath("converse-stream", input.Model, key)

	region := "us-east-1"
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Create the streaming request
	jsonBody, jsonErr := sonic.Marshal(bedrockReq)
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

		for {
			n, err := reader.Read(buffer)
			if err != nil {
				if err == io.EOF {
					// Process any remaining data in the accumulator
					if len(accumulator) > 0 {
						_ = provider.processAWSEventStreamData(ctx, postHookRunner, accumulator, &messageID, &chunkIndex, &usage, &finishReason, input.Model, providerName, responseChan)
					}
					break
				}
				provider.logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerName, err))
				processAndSendError(ctx, postHookRunner, err, responseChan, provider.logger)
				return
			}

			if n == 0 {
				continue
			}

			// Append new data to accumulator
			accumulator = append(accumulator, buffer[:n]...)

			// Process the accumulated data and get the remaining unprocessed part
			remaining := provider.processAWSEventStreamData(ctx, postHookRunner, accumulator, &messageID, &chunkIndex, &usage, &finishReason, input.Model, providerName, responseChan)

			// Reset accumulator with remaining data
			accumulator = remaining
		}

		// Send final response
		response := createBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, input.Params, providerName)
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
						provider.processEventBuffer(ctx, postHookRunner, jsonBytes, messageID, chunkIndex, usage, finishReason, model, providerName, responseChan)
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
func (provider *BedrockProvider) processEventBuffer(ctx context.Context, postHookRunner schemas.PostHookRunner, eventBuffer []byte, messageID *string, chunkIndex *int, usage **schemas.LLMUsage, finishReason **string, model string, providerName schemas.ModelProvider, responseChan chan *schemas.BifrostStream) {
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
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
						Delta: schemas.BifrostStreamDelta{
							Role: streamEvent.Role,
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:   providerName,
				ChunkIndex: *chunkIndex,
			},
		}

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
		var toolCall schemas.ToolCall
		toolCall.Type = schemas.Ptr("function")
		toolCall.Function.Name = schemas.Ptr(toolUseStart.Name)
		toolCall.Function.Arguments = "{}" // Start with empty arguments

		streamResponse := &schemas.BifrostResponse{
			ID:     *messageID,
			Object: "chat.completion.chunk",
			Model:  model,
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: contentBlockIndex,
					BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
						Delta: schemas.BifrostStreamDelta{
							ToolCalls: []schemas.ToolCall{toolCall},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:   providerName,
				ChunkIndex: *chunkIndex,
			},
		}

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
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: contentBlockIndex,
							BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
								Delta: schemas.BifrostStreamDelta{
									Content: &text,
								},
							},
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						Provider:   providerName,
						ChunkIndex: *chunkIndex,
					},
				}

				processAndSendResponse(ctx, postHookRunner, streamResponse, responseChan, provider.logger)
			}

		case streamEvent.Delta.ToolUse != nil:
			// Handle tool use delta
			toolUseDelta := streamEvent.Delta.ToolUse

			// Parse the incremental input JSON
			var inputData interface{}
			if err := sonic.Unmarshal([]byte(toolUseDelta.Input), &inputData); err != nil {
				inputData = map[string]interface{}{}
			}

			// Create tool call structure
			var toolCall schemas.ToolCall
			toolCall.Type = schemas.Ptr("function")

			// For streaming, we need to accumulate tool use data
			// This is a simplified approach - in practice, you'd need to track tool calls across chunks
			toolCall.Function.Arguments = toolUseDelta.Input

			streamResponse := &schemas.BifrostResponse{
				ID:     *messageID,
				Object: "chat.completion.chunk",
				Model:  model,
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: contentBlockIndex,
						BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
							Delta: schemas.BifrostStreamDelta{
								ToolCalls: []schemas.ToolCall{toolCall},
							},
						},
					},
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					Provider:   providerName,
					ChunkIndex: *chunkIndex,
				},
			}

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

func (provider *BedrockProvider) Speech(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "bedrock")
}

func (provider *BedrockProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "bedrock")
}

func (provider *BedrockProvider) Transcription(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "bedrock")
}

func (provider *BedrockProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "bedrock")
}

func (provider *BedrockProvider) getModelPath(basePath string, model string, key schemas.Key) string {
	// Format the path with proper model identifier for streaming
	path := fmt.Sprintf("%s/%s", model, basePath)

	if key.BedrockKeyConfig.Deployments != nil {
		if inferenceProfileId, ok := key.BedrockKeyConfig.Deployments[model]; ok {
			if key.BedrockKeyConfig.ARN != nil {
				encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", *key.BedrockKeyConfig.ARN, inferenceProfileId))
				path = fmt.Sprintf("%s/%s", encodedModelIdentifier, basePath)
			}
		}
	}

	return path
}
