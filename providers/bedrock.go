package providers

import (
	"bifrost/interfaces"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
	Type   string                      `json:"type"`
	Source BedrockAnthropicImageSource `json:"source"`
}

type BedrockAnthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type BedrockMistralToolCall struct {
	ID       string                  `json:"id"`
	Function interfaces.FunctionCall `json:"function"`
}

type BedrockProvider struct {
	client *http.Client
	meta   *interfaces.BedrockMetaConfig
}

func NewBedrockProvider(config *interfaces.ProviderConfig) *BedrockProvider {
	return &BedrockProvider{
		client: &http.Client{Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)},
		meta:   config.MetaConfig.BedrockMetaConfig,
	}
}

func (p *BedrockProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Bedrock
}

func (p *BedrockProvider) PrepareReq(path string, jsonData []byte, accessKey string) (*http.Request, error) {
	if p.meta == nil {
		return nil, errors.New("meta config for bedrock is not provided")
	}

	region := "us-east-1"
	if p.meta.Region != nil {
		region = *p.meta.Region
	}

	// Create the request with the JSON body
	req, err := http.NewRequest("POST", fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path), bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	if err := SignAWSRequest(req, accessKey, p.meta.SecretAccessKey, p.meta.SessionToken, region, "bedrock"); err != nil {
		return nil, err
	}

	return req, nil
}

func (p *BedrockProvider) GetTextCompletionResult(result []byte, model string) (*interfaces.CompletionResult, error) {
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

		return &interfaces.CompletionResult{
			Choices: []interfaces.CompletionResultChoice{
				{
					Index: 0,
					Message: interfaces.CompletionResponseChoice{
						Role:    interfaces.RoleAssistant,
						Content: response.Completion,
					},
					StopReason: &response.StopReason,
					Stop:       &response.Stop,
				},
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

		var choices []interfaces.CompletionResultChoice
		for i, output := range response.Outputs {
			choices = append(choices, interfaces.CompletionResultChoice{
				Index: i,
				Message: interfaces.CompletionResponseChoice{
					Role:    interfaces.RoleAssistant,
					Content: output.Text,
				},
				StopReason: &output.StopReason,
			})
		}

		return &interfaces.CompletionResult{
			Choices: choices,
		}, nil
	}

	return nil, fmt.Errorf("invalid model choice: %s", model)
}

func (p *BedrockProvider) PrepareChatCompletionMessages(messages []interfaces.Message, model string) (map[string]interface{}, error) {
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
						Source: BedrockAnthropicImageSource{
							Type:      msg.ImageContent.Type,
							MediaType: msg.ImageContent.MediaType,
							Data:      msg.ImageContent.URL,
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
						ID:       toolCall.ID,
						Function: *toolCall.Function,
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

func (p *BedrockProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	startTime := time.Now()

	preparedParams := PrepareParams(params)

	requestBody := MergeConfig(map[string]interface{}{
		"prompt": text,
	}, preparedParams)

	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Create the signed request with correct operation name
	req, err := p.PrepareReq(fmt.Sprintf("%s/invoke", model), jsonData, key)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Execute the request
	resp, err := p.client.Do(req)
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

	result, err := p.GetTextCompletionResult(body, model)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response body: %v", err)
	}
	// Calculate latency
	latency := time.Since(startTime).Seconds()
	result.Usage.Latency = &latency

	return result, nil
}

func (p *BedrockProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	messageBody, err := p.PrepareChatCompletionMessages(messages, model)
	if err != nil {
		return nil, fmt.Errorf("error preparing messages: %v", err)
	}

	preparedParams := PrepareParams(params)
	requestBody := MergeConfig(messageBody, preparedParams)

	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Format the path with proper model identifier
	path := fmt.Sprintf("%s/converse", model)

	if p.meta != nil && p.meta.InferenceProfiles != nil {
		if inferenceProfileId, ok := p.meta.InferenceProfiles[model]; ok {
			if p.meta.ARN != nil {
				encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", *p.meta.ARN, inferenceProfileId))
				path = fmt.Sprintf("%s/converse", encodedModelIdentifier)
			}
		}
	}

	// Create the signed request
	req, err := p.PrepareReq(path, jsonData, key)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Execute the request
	resp, err := p.client.Do(req)
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

	var choices []interfaces.CompletionResultChoice
	for i, choice := range response.Output.Message.Content {
		choices = append(choices, interfaces.CompletionResultChoice{
			Index: i,
			Message: interfaces.CompletionResponseChoice{
				Role:    interfaces.RoleAssistant,
				Content: choice.Text,
			},
			StopReason: &response.StopReason,
		})
	}

	latency := float64(response.Metrics.Latency)

	result := &interfaces.CompletionResult{
		Choices: choices,
		Usage: interfaces.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.TotalTokens,
			Latency:          &latency,
		},
		Model:    model,
		Provider: interfaces.Bedrock,
	}

	return result, nil
}
