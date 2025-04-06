package providers

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/interfaces"
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

// OpenAIResponsePool provides a pool for OpenAI response objects
var openAIResponsePool = sync.Pool{
	New: func() interface{} {
		return &OpenAIResponse{}
	},
}

// BifrostResponsePool provides a pool for Bifrost response objects
var bifrostResponsePool = sync.Pool{
	New: func() interface{} {
		return &interfaces.BifrostResponse{}
	},
}

// AcquireOpenAIResponse gets an OpenAI response from the pool
func AcquireOpenAIResponse() *OpenAIResponse {
	resp := openAIResponsePool.Get().(*OpenAIResponse)
	*resp = OpenAIResponse{} // Reset the struct
	return resp
}

// ReleaseOpenAIResponse returns an OpenAI response to the pool
func ReleaseOpenAIResponse(resp *OpenAIResponse) {
	if resp != nil {
		openAIResponsePool.Put(resp)
	}
}

// AcquireBifrostResponse gets a Bifrost response from the pool
func AcquireBifrostResponse() *interfaces.BifrostResponse {
	resp := bifrostResponsePool.Get().(*interfaces.BifrostResponse)
	*resp = interfaces.BifrostResponse{} // Reset the struct
	return resp
}

// ReleaseBifrostResponse returns a Bifrost response to the pool
func ReleaseBifrostResponse(resp *interfaces.BifrostResponse) {
	if resp != nil {
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
	logger interfaces.Logger
	client *fasthttp.Client
}

// NewOpenAIProvider creates a new OpenAI provider instance
func NewOpenAIProvider(config *interfaces.ProviderConfig, logger interfaces.Logger) *OpenAIProvider {
	// Create the client
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	for range config.ConcurrencyAndBufferSize.Concurrency {
		// Create and put new objects directly into pools
		openAIResponsePool.Put(&OpenAIResponse{})
		bifrostResponsePool.Put(&interfaces.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &OpenAIProvider{
		logger: logger,
		client: client,
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

	preparedParams := PrepareParams(params)

	requestBody := MergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

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

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("https://api.openai.com/v1/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.SetBody(jsonBody)

	// Make request
	if err := provider.client.Do(req, resp); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: ErrOpenAIRequest.Error(),
				Error:   err,
			},
		}
	}

	// Handle error response
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

	responseBody := resp.Body()

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
		Provider:    interfaces.OpenAI,
		RawResponse: rawResponse,
	}

	return result, nil
}
