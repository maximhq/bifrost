// Package providers implements various LLM providers and their utility functions.
// This file contains the Mistral provider implementation.
package providers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// MistralResponse represents the response structure from the Mistral API.
type MistralResponse struct {
	ID      string                          `json:"id"`
	Object  string                          `json:"object"`
	Choices []schemas.BifrostResponseChoice `json:"choices"`
	Model   string                          `json:"model"`
	Created int                             `json:"created"`
	Usage   schemas.LLMUsage                `json:"usage"`
}

// mistralResponsePool provides a pool for Mistral response objects.
var mistralResponsePool = sync.Pool{
	New: func() interface{} {
		return &MistralResponse{}
	},
}

// acquireMistralResponse gets a Mistral response from the pool and resets it.
func acquireMistralResponse() *MistralResponse {
	resp := mistralResponsePool.Get().(*MistralResponse)
	*resp = MistralResponse{} // Reset the struct
	return resp
}

// releaseMistralResponse returns a Mistral response to the pool.
func releaseMistralResponse(resp *MistralResponse) {
	if resp != nil {
		mistralResponsePool.Put(resp)
	}
}

// MistralProvider implements the Provider interface for Mistral's API.
type MistralProvider struct {
	logger        schemas.Logger        // Logger for provider operations
	client        *fasthttp.Client      // HTTP client for API requests
	networkConfig schemas.NetworkConfig // Network configuration including extra headers
}

// NewMistralProvider creates a new Mistral provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewMistralProvider(config *schemas.ProviderConfig, logger schemas.Logger) *MistralProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.Concurrency,
	}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		mistralResponsePool.Put(&MistralResponse{})
		bifrostResponsePool.Put(&schemas.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.mistral.ai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &MistralProvider{
		logger:        logger,
		client:        client,
		networkConfig: config.NetworkConfig,
	}
}

// GetProviderKey returns the provider identifier for Mistral.
func (provider *MistralProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Mistral
}

// TextCompletion is not supported by the Mistral provider.
func (provider *MistralProvider) TextCompletion(ctx context.Context, model, key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: "text completion is not supported by mistral provider",
		},
	}
}

// ChatCompletion performs a chat completion request to the Mistral API.
func (provider *MistralProvider) ChatCompletion(ctx context.Context, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

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

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from mistral provider: %s", string(resp.Body())))

		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("Mistral error: %v", errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	response := acquireMistralResponse()
	defer releaseMistralResponse(response)

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
	result.Model = response.Model
	result.Created = response.Created
	result.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider:    schemas.Mistral,
		RawResponse: rawResponse,
	}

	return result, nil
}
