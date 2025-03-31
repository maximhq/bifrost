package providers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"
)

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
func (provider *OpenAIProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
	return nil, fmt.Errorf("text completion is not supported by OpenAI")
}

func (provider *OpenAIProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
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
		return nil, fmt.Errorf("error marshaling request: %v", err)
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
		return nil, fmt.Errorf("error making request: %v", err)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, fmt.Errorf("OpenAI error: %s", resp.Body())
	}

	body := resp.Body()

	// Decode structured response
	var response OpenAIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("error decoding structured response: %v", err)
	}

	// Decode raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, fmt.Errorf("error decoding raw response: %v", err)
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
