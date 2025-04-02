package providers

import (
	"encoding/json"

	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"
)

type OpenAIChatResponse struct {
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
	return &OpenAIProvider{
		logger: logger,
		client: &fasthttp.Client{
			ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
			WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
			MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
		},
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
				Message: "error marshaling request",
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
				Message: "error sending request",
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
					Message: "error parsing error response",
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

	body := resp.Body()

	// Decode structured response
	var response OpenAIChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error parsing response",
				Error:   err,
			},
		}
	}

	// Decode raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error parsing raw response",
				Error:   err,
			},
		}
	}

	result := &interfaces.BifrostResponse{
		ID:                response.ID,
		Choices:           response.Choices,
		Object:            response.Object,
		Usage:             response.Usage,
		ServiceTier:       response.ServiceTier,
		SystemFingerprint: response.SystemFingerprint,
		Created:           response.Created,
		Model:             response.Model,
		ExtraFields: interfaces.BifrostResponseExtraFields{
			Provider:    interfaces.OpenAI,
			RawResponse: rawResponse,
		},
	}

	return result, nil
}
