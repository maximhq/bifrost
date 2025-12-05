package huggingface

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToHuggingFaceChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) *HuggingFaceChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	// Convert messages from Bifrost format to HuggingFace format
	hfMessages := make([]HuggingFaceChatMessage, 0, len(bifrostReq.Input))
	for _, msg := range bifrostReq.Input {
		hfMsg := HuggingFaceChatMessage{}

		// Set role
		if msg.Role != "" {
			role := string(msg.Role)
			hfMsg.Role = &role
		}

		// Set name if present
		if msg.Name != nil {
			hfMsg.Name = msg.Name
		}

		// Convert content
		if msg.Content != nil {
			// Handle string content
			if msg.Content.ContentStr != nil {
				contentJSON, _ := sonic.Marshal(*msg.Content.ContentStr)
				hfMsg.Content = json.RawMessage(contentJSON)
			} else if msg.Content.ContentBlocks != nil {
				// Handle content blocks (array of text/image objects)
				contentItems := make([]HuggingFaceContentItem, 0, len(msg.Content.ContentBlocks))
				for _, block := range msg.Content.ContentBlocks {
					item := HuggingFaceContentItem{}
					blockType := string(block.Type)
					item.Type = &blockType

					switch block.Type {
					case schemas.ChatContentBlockTypeText:
						if block.Text != nil {
							item.Text = block.Text
						}
					case schemas.ChatContentBlockTypeImage:
						if block.ImageURLStruct != nil {
							item.ImageURL = &HuggingFaceImageRef{
								URL: block.ImageURLStruct.URL,
							}
						}
					}
					contentItems = append(contentItems, item)
				}
				contentJSON, _ := sonic.Marshal(contentItems)
				hfMsg.Content = json.RawMessage(contentJSON)
			}
		}

		// Handle tool calls for assistant messages
		if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
			hfToolCalls := make([]HuggingFaceToolCall, 0, len(msg.ChatAssistantMessage.ToolCalls))
			for _, tc := range msg.ChatAssistantMessage.ToolCalls {
				// Skip tool calls with nil Function to avoid panics
				if tc.Function.Name == nil {
					continue
				}

				fnName := *tc.Function.Name
				fnArgs := tc.Function.Arguments

				hfToolCall := HuggingFaceToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: HuggingFaceFunction{
						Name:      fnName,
						Arguments: fnArgs,
					},
				}
				hfToolCalls = append(hfToolCalls, hfToolCall)
			}
			hfMsg.ToolCalls = hfToolCalls
		}

		// Handle tool_call_id for tool messages
		if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
			hfMsg.ToolCallID = msg.ChatToolMessage.ToolCallID
			if debug {
				fmt.Printf("[huggingface debug] Added tool_call_id=%s to tool message\n", *msg.ChatToolMessage.ToolCallID)
			}
		}

		hfMessages = append(hfMessages, hfMsg)
	}

	// Note: The model should already be in the correct format (modelName:inferenceProvider)
	// from the splitIntoModelProvider function. We should NOT transform it again here.

	// Create the HuggingFace request
	hfReq := &HuggingFaceChatRequest{
		Messages: hfMessages,
		Model:    bifrostReq.Model,
	}

	// Map parameters if present
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.FrequencyPenalty != nil {
			hfReq.FrequencyPenalty = params.FrequencyPenalty
		}
		if params.LogProbs != nil {
			hfReq.Logprobs = params.LogProbs
		}
		if params.MaxCompletionTokens != nil {
			hfReq.MaxTokens = params.MaxCompletionTokens
		}
		if params.PresencePenalty != nil {
			hfReq.PresencePenalty = params.PresencePenalty
		}
		if params.Seed != nil {
			hfReq.Seed = params.Seed
		}
		if len(params.Stop) > 0 {
			hfReq.Stop = params.Stop
		}
		if params.Temperature != nil {
			hfReq.Temperature = params.Temperature
		}
		if params.TopLogProbs != nil {
			hfReq.TopLogprobs = params.TopLogProbs
		}
		if params.TopP != nil {
			hfReq.TopP = params.TopP
		}

		// Handle response format
		if params.ResponseFormat != nil {
			// Convert the response format to HuggingFace format
			responseFormatJSON, err := sonic.Marshal(params.ResponseFormat)
			if err == nil {
				var hfResponseFormat HuggingFaceResponseFormat
				if err := sonic.Unmarshal(responseFormatJSON, &hfResponseFormat); err == nil {
					hfReq.ResponseFormat = &hfResponseFormat
				}
			}
		}

		// Handle stream options
		if params.StreamOptions != nil {
			hfReq.StreamOptions = &HuggingFaceStreamOptions{
				IncludeUsage: params.StreamOptions.IncludeUsage,
			}
		}

		// Handle tools
		if len(params.Tools) > 0 {
			hfTools := make([]HuggingFaceTool, 0, len(params.Tools))
			for _, tool := range params.Tools {
				if tool.Function != nil {
					var paramsRaw json.RawMessage
					if tool.Function.Parameters != nil {
						paramsJSON, err := sonic.Marshal(tool.Function.Parameters)
						if err == nil {
							paramsRaw = json.RawMessage(paramsJSON)
						}
					}

					description := ""
					if tool.Function.Description != nil {
						description = *tool.Function.Description
					}

					hfTool := HuggingFaceTool{
						Type: string(tool.Type),
						Function: HuggingFaceToolFunction{
							Name:        tool.Function.Name,
							Description: description,
							Parameters:  paramsRaw,
						},
					}
					hfTools = append(hfTools, hfTool)
				}
			}
			hfReq.Tools = hfTools
		}

		// Handle tool choice
		if params.ToolChoice != nil {
			if params.ToolChoice.ChatToolChoiceStr != nil {
				toolChoiceJSON, _ := sonic.Marshal(*params.ToolChoice.ChatToolChoiceStr)
				hfReq.ToolChoice = json.RawMessage(toolChoiceJSON)
			} else if params.ToolChoice.ChatToolChoiceStruct != nil {
				toolChoiceJSON, _ := sonic.Marshal(params.ToolChoice.ChatToolChoiceStruct)
				hfReq.ToolChoice = json.RawMessage(toolChoiceJSON)
			}
		}
	}

	return hfReq
}

func (response *HuggingFaceChatResponse) ToBifrostChatResponse(model string) (*schemas.BifrostChatResponse, error) {
	if response == nil {
		return nil, nil
	}

	if model == "" {
		return nil, fmt.Errorf("model name cannot be empty")
	}

	// Create the base Bifrost response
	bifrostResponse := &schemas.BifrostChatResponse{
		ID:                response.ID,
		Model:             model,
		Created:           int(response.Created),
		Object:            "chat.completion",
		SystemFingerprint: response.SystemFingerprint,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.HuggingFace,
		},
	}

	// Convert choices
	if len(response.Choices) > 0 {
		bifrostResponse.Choices = make([]schemas.BifrostResponseChoice, len(response.Choices))
		for i, choice := range response.Choices {
			bifrostChoice := schemas.BifrostResponseChoice{
				Index:        choice.Index,
				FinishReason: &choice.FinishReason,
			}

			// Convert the message
			if choice.Message.Role != nil || choice.Message.Content != nil || len(choice.Message.ToolCalls) > 0 {
				message := &schemas.ChatMessage{}

				// Set role
				if choice.Message.Role != nil {
					message.Role = schemas.ChatMessageRole(*choice.Message.Role)
				}

				// Set content
				if choice.Message.Content != nil {
					message.Content = &schemas.ChatMessageContent{
						ContentStr: choice.Message.Content,
					}
				}

				// Handle tool calls
				if len(choice.Message.ToolCalls) > 0 {
					if debug {
						fmt.Printf("[huggingface debug] Converting %d tool calls from response\n", len(choice.Message.ToolCalls))
						for idx, tc := range choice.Message.ToolCalls {
							fmt.Printf("[huggingface debug]   Tool call %d: ID=%s, Type=%s, Function=%s\n",
								idx, tc.ID, tc.Type, tc.Function.Name)
						}
					}
					message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
						ToolCalls: make([]schemas.ChatAssistantMessageToolCall, len(choice.Message.ToolCalls)),
					}
					for j, toolCall := range choice.Message.ToolCalls {
						message.ChatAssistantMessage.ToolCalls[j] = schemas.ChatAssistantMessageToolCall{
							Index: uint16(j),
							Type:  &toolCall.Type,
							ID:    &toolCall.ID,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &toolCall.Function.Name,
								Arguments: toolCall.Function.Arguments,
							},
						}
					}
				}

				bifrostChoice.ChatNonStreamResponseChoice = &schemas.ChatNonStreamResponseChoice{
					Message: message,
				}
			}

			// Convert logprobs if present
			if choice.Logprobs != nil {
				bifrostChoice.LogProbs = &schemas.BifrostLogProbs{
					Content: make([]schemas.ContentLogProb, len(choice.Logprobs.Content)),
				}
				for j, logprob := range choice.Logprobs.Content {
					bifrostChoice.LogProbs.Content[j] = schemas.ContentLogProb{
						Token:   logprob.Token,
						LogProb: float64(logprob.Logprob),
					}
					// Convert top logprobs
					if len(logprob.TopLogprobs) > 0 {
						bifrostChoice.LogProbs.Content[j].TopLogProbs = make([]schemas.LogProb, len(logprob.TopLogprobs))
						for k, topLogprob := range logprob.TopLogprobs {
							bifrostChoice.LogProbs.Content[j].TopLogProbs[k] = schemas.LogProb{
								Token:   topLogprob.Token,
								LogProb: float64(topLogprob.Logprob),
							}
						}
					}
				}
			}

			bifrostResponse.Choices[i] = bifrostChoice
		}
	}

	// Convert usage information
	if response.Usage.TotalTokens > 0 || response.Usage.PromptTokens > 0 || response.Usage.CompletionTokens > 0 {
		bifrostResponse.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
		}
	}

	return bifrostResponse, nil
}

// ToBifrostChatResponse converts a HuggingFace streaming response to a Bifrost chat response
func (response *HuggingFaceChatStreamResponse) ToBifrostChatStreamResponse() *schemas.BifrostChatResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostChatResponse{
		ID:                response.ID,
		Model:             response.Model,
		SystemFingerprint: response.SystemFingerprint,
		Object:            response.Object,
		Choices:           make([]schemas.BifrostResponseChoice, len(response.Choices)),
	}

	// Convert usage if present
	if response.Usage != nil {
		bifrostResponse.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
		}
	}

	// Convert choices
	for i, choice := range response.Choices {
		bifrostChoice := schemas.BifrostResponseChoice{
			Index: choice.Index,
		}

		// Set finish reason if present
		if choice.FinishReason != nil {
			bifrostChoice.FinishReason = choice.FinishReason
		}

		// Convert delta to streaming choice
		streamChoice := &schemas.ChatStreamResponseChoice{
			Delta: &schemas.ChatStreamResponseChoiceDelta{},
		}

		// Set role if present
		if choice.Delta.Role != nil {
			streamChoice.Delta.Role = choice.Delta.Role
		}

		// Set content if present
		if choice.Delta.Content != nil {
			streamChoice.Delta.Content = choice.Delta.Content
		}

		// Set reasoning as thought if present
		if choice.Delta.Reasoning != nil {
			streamChoice.Delta.Thought = choice.Delta.Reasoning
		}

		// Convert tool calls if present
		if len(choice.Delta.ToolCalls) > 0 {
			streamChoice.Delta.ToolCalls = make([]schemas.ChatAssistantMessageToolCall, len(choice.Delta.ToolCalls))
			for j, tc := range choice.Delta.ToolCalls {
				streamChoice.Delta.ToolCalls[j] = schemas.ChatAssistantMessageToolCall{
					Index: uint16(tc.Index),
					ID:    &tc.ID,
					Type:  &tc.Type,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}

		// Convert logprobs if present
		if choice.Logprobs != nil {
			bifrostChoice.LogProbs = &schemas.BifrostLogProbs{
				Content: make([]schemas.ContentLogProb, len(choice.Logprobs.Content)),
			}
			for j, lp := range choice.Logprobs.Content {
				topLogprobs := make([]schemas.LogProb, len(lp.TopLogprobs))
				for k, tlp := range lp.TopLogprobs {
					topLogprobs[k] = schemas.LogProb{
						Token:   tlp.Token,
						LogProb: float64(tlp.Logprob),
					}
				}
				bifrostChoice.LogProbs.Content[j] = schemas.ContentLogProb{
					Token:       lp.Token,
					LogProb:     float64(lp.Logprob),
					TopLogProbs: topLogprobs,
				}
			}
		}

		bifrostChoice.ChatStreamResponseChoice = streamChoice
		bifrostResponse.Choices[i] = bifrostChoice
	}

	return bifrostResponse
}
