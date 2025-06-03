// Package providers implements various LLM providers and their utility functions.
// This file contains the Vertex provider implementation.
package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"
	"golang.org/x/oauth2/google"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type VertexError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// VertexProvider implements the Provider interface for Vertex's API.
type VertexProvider struct {
	logger schemas.Logger     // Logger for provider operations
	client *http.Client       // HTTP client for API requests
	meta   schemas.MetaConfig // Vertex-specific configuration
}

// NewVertexProvider creates a new Vertex provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewVertexProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*VertexProvider, error) {
	config.CheckAndSetDefaults()

	if config.MetaConfig == nil {
		return nil, fmt.Errorf("meta config is not set")
	}

	authCredentials := config.MetaConfig.GetAuthCredentials()
	if authCredentials == nil {
		return nil, fmt.Errorf("auth credentials are not set")
	}

	// Get a Google JWT Config for the correct scope
	conf, err := google.JWTConfigFromJSON([]byte(*authCredentials), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT config: %w", err)
	}

	// Get an access token
	client := conf.Client(context.Background())

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		openAIResponsePool.Put(&OpenAIResponse{})
		anthropicChatResponsePool.Put(&AnthropicChatResponse{})
		bifrostResponsePool.Put(&schemas.BifrostResponse{})
	}

	return &VertexProvider{
		logger: logger,
		client: client,
		meta:   config.MetaConfig,
	}, nil
}

// GetProviderKey returns the provider identifier for Vertex.
func (provider *VertexProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Vertex
}

// TextCompletion is not supported by the Vertex provider.
// Returns an error indicating that text completion is not available.
func (provider *VertexProvider) TextCompletion(ctx context.Context, model, key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: "text completion is not supported by vertex provider",
		},
	}
}

// ChatCompletion performs a chat completion request to the Vertex API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *VertexProvider) ChatCompletion(ctx context.Context, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Format messages for Vertex API
	var formattedMessages []map[string]interface{}
	var preparedParams map[string]interface{}

	if strings.Contains(model, "claude") {
		formattedMessages, preparedParams = prepareAnthropicChatRequest(model, messages, params)
	} else {
		formattedMessages, preparedParams = prepareOpenAIChatRequest(model, messages, params)
	}

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	if strings.Contains(model, "claude") {
		if _, exists := requestBody["anthropic_version"]; !exists {
			requestBody["anthropic_version"] = "vertex-2023-10-16"
		}

		delete(requestBody, "model")
	}

	delete(requestBody, "region")

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

	projectID := provider.meta.GetProjectID()
	if projectID == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "project ID is not set",
			},
		}
	}

	region := provider.meta.GetRegion()
	if region == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "region is not set in meta config",
			},
		}
	}

	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions", *region, *projectID, *region)

	if strings.Contains(model, "claude") {
		url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:streamRawPredict", *region, *projectID, *region, model)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}
	req.Header.Set("Content-Type", "application/json")

	// Make request
	resp, err := provider.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: schemas.ErrorField{
					Type:    StrPtr(schemas.RequestCancelled),
					Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
					Error:   err,
				},
			}
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}
	defer resp.Body.Close()

	// Handle error response
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
		var openAIErr OpenAIError
		var vertexErr []VertexError

		provider.logger.Debug(fmt.Sprintf("error from vertex provider: %s", string(body)))

		if err := json.Unmarshal(body, &openAIErr); err != nil {
			// Try Vertex error format if OpenAI format fails
			if err := json.Unmarshal(body, &vertexErr); err != nil {
				return nil, &schemas.BifrostError{
					IsBifrostError: true,
					StatusCode:     &resp.StatusCode,
					Error: schemas.ErrorField{
						Message: schemas.ErrProviderResponseUnmarshal,
						Error:   err,
					},
				}
			}

			if len(vertexErr) > 0 {
				return nil, &schemas.BifrostError{
					StatusCode: &resp.StatusCode,
					Type:       &vertexErr[0].Error.Status,
					Error: schemas.ErrorField{
						Message: vertexErr[0].Error.Message,
					},
				}
			}
		}

		return nil, &schemas.BifrostError{
			StatusCode: &resp.StatusCode,
			Error: schemas.ErrorField{
				Message: openAIErr.Error.Message,
			},
		}
	}

	if strings.Contains(model, "claude") {
		// Create response object from pool
		response := acquireAnthropicChatResponse()
		defer releaseAnthropicChatResponse(response)

		// Create Bifrost response from pool
		bifrostResponse := acquireBifrostResponse()
		defer releaseBifrostResponse(bifrostResponse)

		rawResponse, bifrostErr := handleProviderResponse(body, response)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		var err *schemas.BifrostError
		bifrostResponse, err = parseAnthropicResponse(response, bifrostResponse)
		if err != nil {
			return nil, err
		}

		bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			Provider:    schemas.Vertex,
			RawResponse: rawResponse,
		}

		return bifrostResponse, nil
	} else {
		// Pre-allocate response structs from pools
		response := acquireOpenAIResponse()
		defer releaseOpenAIResponse(response)

		result := acquireBifrostResponse()
		defer releaseBifrostResponse(result)

		// Use enhanced response handler with pre-allocated response
		rawResponse, bifrostErr := handleProviderResponse(body, response)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

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
			Provider:    schemas.Vertex,
			RawResponse: rawResponse,
		}

		return result, nil
	}
}
