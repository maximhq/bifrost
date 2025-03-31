package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maximhq/bifrost/interfaces"
)

type BedrockAnthropicTextResponse struct {
	Completion string `json:"completion"`
	StopReason string `json:"stop_reason"`
	Stop       string `json:"stop"`
}

type BedrockMistralTextResponse struct {
	Outputs []struct {
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"outputs"`
}

type BedrockChatResponse struct {
	Metrics struct {
		Latency int `json:"latencyMs"`
	} `json:"metrics"`
	Output struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Role string `json:"role"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

type BedrockAnthropicSystemMessage struct {
	Text string `json:"text"`
}

type BedrockAnthropicTextMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type BedrockMistralContent struct {
	Text string `json:"text"`
}

type BedrockMistralChatMessage struct {
	Role       interfaces.ModelChatMessageRole `json:"role"`
	Content    []BedrockMistralContent         `json:"content"`
	ToolCalls  *[]BedrockMistralToolCall       `json:"tool_calls,omitempty"`
	ToolCallID *string                         `json:"tool_call_id,omitempty"`
}

type BedrockAnthropicImageMessage struct {
	Type  string                `json:"type"`
	Image BedrockAnthropicImage `json:"image"`
}

type BedrockAnthropicImage struct {
	Format string                      `json:"string"`
	Source BedrockAnthropicImageSource `json:"source"`
}

type BedrockAnthropicImageSource struct {
	Bytes string `json:"bytes"`
}

type BedrockMistralToolCall struct {
	ID       string              `json:"id"`
	Function interfaces.Function `json:"function"`
}

type BedrockAnthropicToolCall struct {
	ToolSpec BedrockAnthropicToolSpec `json:"toolSpec"`
}

type BedrockAnthropicToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Json interface{} `json:"json"`
	} `json:"inputSchema"`
}

type BedrockProvider struct {
	client *http.Client
	meta   *interfaces.MetaConfig
}

func NewBedrockProvider(config *interfaces.ProviderConfig) *BedrockProvider {
	return &BedrockProvider{
		client: &http.Client{Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)},
		meta:   config.MetaConfig,
	}
}

func (provider *BedrockProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Bedrock
}

func (provider *BedrockProvider) PrepareReq(path string, jsonData []byte, accessKey string) (*http.Request, error) {
	if provider.meta == nil {
		return nil, errors.New("meta config for bedrock is not provided")
	}

	region := "us-east-1"
	if provider.meta.Region != nil {
		region = *provider.meta.Region
	}

	// Create the request with the JSON body
	req, err := http.NewRequest("POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	if err := SignAWSRequest(req, accessKey, *provider.meta.SecretAccessKey, provider.meta.SessionToken, region, "bedrock"); err != nil {
		return nil, err
	}

	return req, nil
}

func (provider *BedrockProvider) GetTextCompletionResult(result []byte, model string) (*interfaces.BifrostResponse, error) {
	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		var response BedrockAnthropicTextResponse
		if err := json.Unmarshal(result, &response); err != nil {
			return nil, fmt.Errorf("failed to parse Bedrock response: %v", err)
		}

		return &interfaces.BifrostResponse{
			Choices: []interfaces.BifrostResponseChoice{
				{
					Index: 0,
					Message: interfaces.BifrostResponseChoiceMessage{
						Role:    interfaces.RoleAssistant,
						Content: &response.Completion,
					},
					FinishReason: &response.StopReason,
					StopString:   &response.Stop,
				},
			},
			Model: model,
			ExtraFields: interfaces.BifrostResponseExtraFields{
				Provider: interfaces.Bedrock,
			},
		}, nil

	case "mistral.mixtral-8x7b-instruct-v0:1":
		fallthrough
	case "mistral.mistral-7b-instruct-v0:2":
		fallthrough
	case "mistral.mistral-large-2402-v1:0":
		fallthrough
	case "mistral.mistral-large-2407-v1:0":
		fallthrough
	case "mistral.mistral-small-2402-v1:0":
		var response BedrockMistralTextResponse
		if err := json.Unmarshal(result, &response); err != nil {
			return nil, fmt.Errorf("failed to parse Bedrock response: %v", err)
		}

		var choices []interfaces.BifrostResponseChoice
		for i, output := range response.Outputs {
			choices = append(choices, interfaces.BifrostResponseChoice{
				Index: i,
				Message: interfaces.BifrostResponseChoiceMessage{
					Role:    interfaces.RoleAssistant,
					Content: &output.Text,
				},
				FinishReason: &output.StopReason,
			})
		}

		return &interfaces.BifrostResponse{
			Choices: choices,
			Model:   model,
			ExtraFields: interfaces.BifrostResponseExtraFields{
				Provider: interfaces.Bedrock,
			},
		}, nil
	}

	return nil, fmt.Errorf("invalid model choice: %s", model)
}

func (provider *BedrockProvider) PrepareChatCompletionMessages(messages []interfaces.Message, model string) (map[string]interface{}, error) {
	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		fallthrough
	case "anthropic.claude-3-sonnet-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20240620-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20241022-v2:0":
		fallthrough
	case "anthropic.claude-3-5-haiku-20241022-v1:0":
		fallthrough
	case "anthropic.claude-3-opus-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-7-sonnet-20250219-v1:0":
		// Add system messages if present
		var systemMessages []BedrockAnthropicSystemMessage
		for _, msg := range messages {
			if msg.Role == interfaces.RoleSystem {
				//TODO handling image inputs here
				systemMessages = append(systemMessages, BedrockAnthropicSystemMessage{
					Text: *msg.Content,
				})
			}
		}

		// Format messages for Bedrock API
		var bedrockMessages []map[string]interface{}
		for _, msg := range messages {
			if msg.Role != interfaces.RoleSystem {
				var content any
				if msg.Content != nil {
					content = BedrockAnthropicTextMessage{
						Type: "text",
						Text: *msg.Content,
					}
				} else if msg.ImageContent != nil {
					content = BedrockAnthropicImageMessage{
						Type: "image",
						Image: BedrockAnthropicImage{
							Format: *msg.ImageContent.Type,
							Source: BedrockAnthropicImageSource{
								Bytes: msg.ImageContent.URL,
							},
						},
					}
				}

				bedrockMessages = append(bedrockMessages, map[string]interface{}{
					"role":    msg.Role,
					"content": []interface{}{content},
				})
			}
		}

		body := map[string]interface{}{
			"messages": bedrockMessages,
		}

		if len(systemMessages) > 0 {
			var messages []string
			for _, message := range systemMessages {
				messages = append(messages, message.Text)
			}

			body["system"] = strings.Join(messages, " ")
		}

		return body, nil

	case "mistral.mistral-large-2402-v1:0":
		fallthrough
	case "mistral.mistral-large-2407-v1:0":
		var bedrockMessages []BedrockMistralChatMessage
		for _, msg := range messages {
			var filteredToolCalls []BedrockMistralToolCall
			if msg.ToolCalls != nil {
				for _, toolCall := range *msg.ToolCalls {
					filteredToolCalls = append(filteredToolCalls, BedrockMistralToolCall{
						ID:       *toolCall.ID,
						Function: toolCall.Function,
					})
				}
			}

			message := BedrockMistralChatMessage{
				Role: msg.Role,
				Content: []BedrockMistralContent{
					{Text: *msg.Content},
				},
			}

			if len(filteredToolCalls) > 0 {
				message.ToolCalls = &filteredToolCalls
			}

			bedrockMessages = append(bedrockMessages, message)
		}

		body := map[string]interface{}{
			"messages": bedrockMessages,
		}

		return body, nil
	}

	return nil, fmt.Errorf("invalid model choice: %s", model)
}

func (provider *BedrockProvider) GetChatCompletionTools(params *interfaces.ModelParameters, model string) []BedrockAnthropicToolCall {
	var tools []BedrockAnthropicToolCall

	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		fallthrough
	case "anthropic.claude-3-sonnet-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20240620-v1:0":
		fallthrough
	case "anthropic.claude-3-5-sonnet-20241022-v2:0":
		fallthrough
	case "anthropic.claude-3-5-haiku-20241022-v1:0":
		fallthrough
	case "anthropic.claude-3-opus-20240229-v1:0":
		fallthrough
	case "anthropic.claude-3-7-sonnet-20250219-v1:0":
		for _, tool := range *params.Tools {
			tools = append(tools, BedrockAnthropicToolCall{
				ToolSpec: BedrockAnthropicToolSpec{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					InputSchema: struct {
						Json interface{} `json:"json"`
					}{
						Json: tool.Function.Parameters,
					},
				},
			})
		}
	}

	return tools
}

func (provider *BedrockProvider) PrepareTextCompletionParams(params map[string]interface{}, model string) map[string]interface{} {
	switch model {
	case "anthropic.claude-instant-v1:2":
		fallthrough
	case "anthropic.claude-v2":
		fallthrough
	case "anthropic.claude-v2:1":
		// Check if there is a key entry for max_tokens
		if maxTokens, exists := params["max_tokens"]; exists {
			// Check if max_tokens_to_sample is already present
			if _, exists := params["max_tokens_to_sample"]; !exists {
				// If max_tokens_to_sample is not present, rename max_tokens to max_tokens_to_sample
				params["max_tokens_to_sample"] = maxTokens
			}
			delete(params, "max_tokens")
		}
	}
	return params
}

func (provider *BedrockProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
	preparedParams := provider.PrepareTextCompletionParams(PrepareParams(params), model)

	requestBody := MergeConfig(map[string]interface{}{
		"prompt": text,
	}, preparedParams)

	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Create the signed request with correct operation name
	req, err := provider.PrepareReq(fmt.Sprintf("%s/invoke", model), jsonData, key)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Execute the request
	resp, err := provider.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock API error: %s", string(body))
	}

	result, err := provider.GetTextCompletionResult(body, model)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response body: %v", err)
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, fmt.Errorf("failed to parse raw response: %v", err)
	}

	result.ExtraFields.RawResponse = rawResponse

	return result, nil
}

func (provider *BedrockProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
	messageBody, err := provider.PrepareChatCompletionMessages(messages, model)
	if err != nil {
		return nil, fmt.Errorf("error preparing messages: %v", err)
	}

	preparedParams := PrepareParams(params)

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		preparedParams["tools"] = provider.GetChatCompletionTools(params, model)
	}

	requestBody := MergeConfig(messageBody, preparedParams)

	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Format the path with proper model identifier
	path := fmt.Sprintf("%s/converse", model)

	if provider.meta != nil && provider.meta.InferenceProfiles != nil {
		if inferenceProfileId, ok := provider.meta.InferenceProfiles[model]; ok {
			if provider.meta.ARN != nil {
				encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", *provider.meta.ARN, inferenceProfileId))
				path = fmt.Sprintf("%s/converse", encodedModelIdentifier)
			}
		}
	}

	// Create the signed request
	req, err := provider.PrepareReq(path, jsonData, key)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Execute the request
	resp, err := provider.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock API error: %s", string(body))
	}

	var response BedrockChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse Bedrock response: %v", err)
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, fmt.Errorf("failed to parse raw response: %v", err)
	}

	var choices []interfaces.BifrostResponseChoice
	for i, choice := range response.Output.Message.Content {
		choices = append(choices, interfaces.BifrostResponseChoice{
			Index: i,
			Message: interfaces.BifrostResponseChoiceMessage{
				Role:    interfaces.RoleAssistant,
				Content: &choice.Text,
			},
			FinishReason: &response.StopReason,
		})
	}

	latency := float64(response.Metrics.Latency)

	result := &interfaces.BifrostResponse{
		Choices: choices,
		Usage: interfaces.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.TotalTokens,
		},
		Model: model,

		ExtraFields: interfaces.BifrostResponseExtraFields{
			Latency:     &latency,
			Provider:    interfaces.Bedrock,
			RawResponse: rawResponse,
		},
	}

	return result, nil
}
