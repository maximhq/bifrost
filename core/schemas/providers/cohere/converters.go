package cohere

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ConvertChatRequestToCohere converts a Bifrost request to Cohere v2 format
func ConvertChatRequestToCohere(bifrostReq *schemas.BifrostRequest) *CohereChatRequest {
	if bifrostReq == nil || bifrostReq.Input.ChatCompletionInput == nil {
		return nil
	}

	messages := *bifrostReq.Input.ChatCompletionInput
	cohereReq := &CohereChatRequest{
		Model: bifrostReq.Model,
	}

	// Convert messages to Cohere v2 format
	var cohereMessages []CohereMessage
	for _, msg := range messages {
		cohereMsg := CohereMessage{
			Role: string(msg.Role),
		}

		// Convert content
		if msg.Content.ContentStr != nil {
			cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
		} else if msg.Content.ContentBlocks != nil {
			var contentBlocks []CohereContentBlock
			for _, block := range *msg.Content.ContentBlocks {
				if block.Text != nil {
					contentBlocks = append(contentBlocks, CohereContentBlock{
						Type: "text",
						Text: block.Text,
					})
				} else if block.ImageURL != nil {
					contentBlocks = append(contentBlocks, CohereContentBlock{
						Type: "image_url",
						ImageURL: &CohereImageURL{
							URL: block.ImageURL.URL,
						},
					})
				}
			}
			if len(contentBlocks) > 0 {
				cohereMsg.Content = NewBlocksContent(contentBlocks)
			}
		}

		// Convert tool calls for assistant messages
		if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
			var toolCalls []CohereToolCall
			for _, toolCall := range *msg.AssistantMessage.ToolCalls {
				cohereToolCall := CohereToolCall{
					ID:   toolCall.ID,
					Type: "function",
					Function: &CohereFunction{
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				toolCalls = append(toolCalls, cohereToolCall)
			}
			cohereMsg.ToolCalls = &toolCalls
		}

		// Convert tool messages
		if msg.ToolMessage != nil && msg.ToolMessage.ToolCallID != nil {
			cohereMsg.ToolCallID = msg.ToolMessage.ToolCallID
		}

		cohereMessages = append(cohereMessages, cohereMsg)
	}

	cohereReq.Messages = cohereMessages

	// Convert parameters
	if bifrostReq.Params != nil {
		cohereReq.MaxTokens = bifrostReq.Params.MaxTokens
		cohereReq.Temperature = bifrostReq.Params.Temperature
		cohereReq.P = bifrostReq.Params.TopP
		cohereReq.K = bifrostReq.Params.TopK
		cohereReq.StopSequences = bifrostReq.Params.StopSequences
		cohereReq.FrequencyPenalty = bifrostReq.Params.FrequencyPenalty
		cohereReq.PresencePenalty = bifrostReq.Params.PresencePenalty

		// Convert extra params
		if bifrostReq.Params.ExtraParams != nil {
			// Handle thinking parameter
			if thinkingParam, ok := bifrostReq.Params.ExtraParams["thinking"]; ok {
				if thinkingMap, ok := thinkingParam.(map[string]interface{}); ok {
					thinking := &CohereThinking{}

					if typeStr, ok := thinkingMap["type"].(string); ok {
						thinking.Type = CohereThinkingType(typeStr)
					}

					if tokenBudget, ok := thinkingMap["token_budget"].(int); ok {
						thinking.TokenBudget = &tokenBudget
					} else if tokenBudgetFloat, ok := thinkingMap["token_budget"].(float64); ok {
						tokenBudgetInt := int(tokenBudgetFloat)
						thinking.TokenBudget = &tokenBudgetInt
					}

					cohereReq.Thinking = thinking
				}
			}

			// Handle other Cohere-specific extra params
			if safetyMode, ok := bifrostReq.Params.ExtraParams["safety_mode"].(string); ok {
				cohereReq.SafetyMode = &safetyMode
			}

			if logProbs, ok := bifrostReq.Params.ExtraParams["log_probs"].(bool); ok {
				cohereReq.LogProbs = &logProbs
			}

			if strictToolChoice, ok := bifrostReq.Params.ExtraParams["strict_tool_choice"].(bool); ok {
				cohereReq.StrictToolChoice = &strictToolChoice
			}
		}

		// Convert tools - direct assignment since formats are identical
		if bifrostReq.Params.Tools != nil {
			cohereReq.Tools = bifrostReq.Params.Tools
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			if bifrostReq.Params.ToolChoice.ToolChoiceStr != nil {
				toolChoice := CohereToolChoice(*bifrostReq.Params.ToolChoice.ToolChoiceStr)
				cohereReq.ToolChoice = &toolChoice
			} else if bifrostReq.Params.ToolChoice.ToolChoiceStruct != nil {
				switch bifrostReq.Params.ToolChoice.ToolChoiceStruct.Type {
				case schemas.ToolChoiceTypeFunction:
					toolChoice := CohereToolChoice("REQUIRED")
					cohereReq.ToolChoice = &toolChoice
				case schemas.ToolChoiceTypeNone:
					toolChoice := CohereToolChoice("NONE")
					cohereReq.ToolChoice = &toolChoice
				default:
					toolChoiceStr := string(bifrostReq.Params.ToolChoice.ToolChoiceStruct.Type)
					toolChoice := CohereToolChoice(toolChoiceStr)
					cohereReq.ToolChoice = &toolChoice
				}
			}
		}
	}

	return cohereReq
}

// ConvertChatResponseToBifrost converts a Cohere v2 response to Bifrost format
func ConvertChatResponseToBifrost(cohereResp *CohereChatResponse) *schemas.BifrostResponse {
	if cohereResp == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostResponse{
		ID:     cohereResp.ID,
		Object: "chat.completion",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleAssistant,
					},
				},
			},
		},
	}

	// Convert message content
	if cohereResp.Message != nil {
		if cohereResp.Message.Content != nil {
			if cohereResp.Message.Content.IsString() {
				content := cohereResp.Message.Content.GetString()
				bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = schemas.MessageContent{
					ContentStr: content,
				}
			} else if cohereResp.Message.Content.IsBlocks() {
				blocks := cohereResp.Message.Content.GetBlocks()
				if blocks != nil {
					var contentBlocks []schemas.ContentBlock
					for _, block := range *blocks {
						if block.Type == "text" && block.Text != nil {
							contentBlocks = append(contentBlocks, schemas.ContentBlock{
								Type: "text",
								Text: block.Text,
							})
						} else if block.Type == "image_url" && block.ImageURL != nil {
							contentBlocks = append(contentBlocks, schemas.ContentBlock{
								Type: "image_url",
								ImageURL: &schemas.ImageURLStruct{
									URL: block.ImageURL.URL,
								},
							})
						}
					}
					if len(contentBlocks) > 0 {
						bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = schemas.MessageContent{
							ContentBlocks: &contentBlocks,
						}
					}
				}
			}
		}

		// Convert tool calls
		if cohereResp.Message.ToolCalls != nil {
			var toolCalls []schemas.ToolCall
			for _, toolCall := range *cohereResp.Message.ToolCalls {
				bifrostToolCall := schemas.ToolCall{
					ID: toolCall.ID,
					Function: schemas.FunctionCall{
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				toolCalls = append(toolCalls, bifrostToolCall)
			}
			bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.AssistantMessage = &schemas.AssistantMessage{
				ToolCalls: &toolCalls,
			}
		}
	}

	// Convert finish reason
	if cohereResp.FinishReason != nil {
		finishReason := string(*cohereResp.FinishReason)
		bifrostResponse.Choices[0].FinishReason = &finishReason
	}

	// Convert usage information
	if cohereResp.Usage != nil {
		usage := &schemas.LLMUsage{}

		if cohereResp.Usage.Tokens != nil {
			if cohereResp.Usage.Tokens.InputTokens != nil {
				usage.PromptTokens = int(*cohereResp.Usage.Tokens.InputTokens)
			}
			if cohereResp.Usage.Tokens.OutputTokens != nil {
				usage.CompletionTokens = int(*cohereResp.Usage.Tokens.OutputTokens)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}

		bifrostResponse.Usage = usage

		// Convert billed usage
		if cohereResp.Usage.BilledUnits != nil {
			bifrostResponse.ExtraFields.BilledUsage = &schemas.BilledLLMUsage{}
			if cohereResp.Usage.BilledUnits.InputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.PromptTokens = cohereResp.Usage.BilledUnits.InputTokens
			}
			if cohereResp.Usage.BilledUnits.OutputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.CompletionTokens = cohereResp.Usage.BilledUnits.OutputTokens
			}
		}
	}

	return bifrostResponse
}

// ConvertEmbeddingRequestToCohere converts a Bifrost embedding request to Cohere format
func ConvertEmbeddingRequestToCohere(bifrostReq *schemas.BifrostRequest) *CohereEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input.EmbeddingInput == nil {
		return nil
	}

	embeddingInput := bifrostReq.Input.EmbeddingInput
	cohereReq := &CohereEmbeddingRequest{
		Model: bifrostReq.Model,
	}

	// Convert texts from Bifrost format
	if len(embeddingInput.Texts) > 0 {
		cohereReq.Texts = &embeddingInput.Texts
	}

	// Set default input type if not specified in extra params
	cohereReq.InputType = "search_document" // Default value

	if bifrostReq.Params != nil {
		cohereReq.OutputDimension = bifrostReq.Params.Dimensions
		cohereReq.MaxTokens = bifrostReq.Params.MaxTokens
	}

	// Handle extra params
	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		// Input type
		if inputType, ok := bifrostReq.Params.ExtraParams["input_type"].(string); ok {
			cohereReq.InputType = inputType
		}

		// Embedding types
		if embeddingTypes, ok := bifrostReq.Params.ExtraParams["embedding_types"].([]interface{}); ok {
			var types []string
			for _, t := range embeddingTypes {
				if typeStr, ok := t.(string); ok {
					types = append(types, typeStr)
				}
			}
			if len(types) > 0 {
				cohereReq.EmbeddingTypes = &types
			}
		}

		// Truncate
		if truncate, ok := bifrostReq.Params.ExtraParams["truncate"].(string); ok {
			cohereReq.Truncate = &truncate
		}

		// Images (if provided)
		if images, ok := bifrostReq.Params.ExtraParams["images"].([]interface{}); ok {
			var imageStrs []string
			for _, img := range images {
				if imgStr, ok := img.(string); ok {
					imageStrs = append(imageStrs, imgStr)
				}
			}
			if len(imageStrs) > 0 {
				cohereReq.Images = &imageStrs
			}
		}

		// Mixed inputs (if provided)
		if inputs, ok := bifrostReq.Params.ExtraParams["inputs"].([]interface{}); ok {
			var cohereInputs []CohereEmbeddingInput
			for _, input := range inputs {
				if inputMap, ok := input.(map[string]interface{}); ok {
					if content, ok := inputMap["content"].([]interface{}); ok {
						var contentBlocks []CohereContentBlock
						for _, block := range content {
							if blockMap, ok := block.(map[string]interface{}); ok {
								contentBlock := CohereContentBlock{}

								if blockType, ok := blockMap["type"].(string); ok {
									contentBlock.Type = blockType
								}

								if text, ok := blockMap["text"].(string); ok {
									contentBlock.Text = &text
								}

								if imageURL, ok := blockMap["image_url"].(map[string]interface{}); ok {
									if url, ok := imageURL["url"].(string); ok {
										contentBlock.ImageURL = &CohereImageURL{URL: url}
									}
								}

								contentBlocks = append(contentBlocks, contentBlock)
							}
						}
						if len(contentBlocks) > 0 {
							cohereInputs = append(cohereInputs, CohereEmbeddingInput{
								Content: contentBlocks,
							})
						}
					}
				}
			}
			if len(cohereInputs) > 0 {
				cohereReq.Inputs = &cohereInputs
			}
		}
	}

	return cohereReq
}

// ConvertEmbeddingResponseToBifrost converts a Cohere embedding response to Bifrost format
func ConvertEmbeddingResponseToBifrost(cohereResp *CohereEmbeddingResponse) *schemas.BifrostResponse {
	if cohereResp == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostResponse{
		ID:     cohereResp.ID,
		Object: "list",
	}

	// Convert embeddings data
	if cohereResp.Embeddings != nil {
		var bifrostEmbeddings []schemas.BifrostEmbedding

		// Handle different embedding types - prioritize float embeddings
		if cohereResp.Embeddings.Float != nil {
			for i, embedding := range *cohereResp.Embeddings.Float {
				bifrostEmbedding := schemas.BifrostEmbedding{
					Object: "embedding",
					Index:  i,
					Embedding: schemas.BifrostEmbeddingResponse{
						EmbeddingArray: &embedding,
					},
				}
				bifrostEmbeddings = append(bifrostEmbeddings, bifrostEmbedding)
			}
		} else if cohereResp.Embeddings.Base64 != nil {
			// Handle base64 embeddings as strings
			for i, embedding := range *cohereResp.Embeddings.Base64 {
				bifrostEmbedding := schemas.BifrostEmbedding{
					Object: "embedding",
					Index:  i,
					Embedding: schemas.BifrostEmbeddingResponse{
						EmbeddingStr: &embedding,
					},
				}
				bifrostEmbeddings = append(bifrostEmbeddings, bifrostEmbedding)
			}
		}
		// Note: Int8, Uint8, Binary, Ubinary types would need special handling
		// depending on how Bifrost wants to represent them

		bifrostResponse.Data = bifrostEmbeddings
	}

	// Convert usage information
	if cohereResp.Meta != nil {
		if cohereResp.Meta.Tokens != nil {
			usage := &schemas.LLMUsage{}
			if cohereResp.Meta.Tokens.InputTokens != nil {
				usage.PromptTokens = int(*cohereResp.Meta.Tokens.InputTokens)
			}
			if cohereResp.Meta.Tokens.OutputTokens != nil {
				usage.CompletionTokens = int(*cohereResp.Meta.Tokens.OutputTokens)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			bifrostResponse.Usage = usage
		}

		// Convert billed usage
		if cohereResp.Meta.BilledUnits != nil {
			bifrostResponse.ExtraFields.BilledUsage = &schemas.BilledLLMUsage{}
			if cohereResp.Meta.BilledUnits.InputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.PromptTokens = cohereResp.Meta.BilledUnits.InputTokens
			}
			if cohereResp.Meta.BilledUnits.OutputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.CompletionTokens = cohereResp.Meta.BilledUnits.OutputTokens
			}
		}
	}

	return bifrostResponse
}
