package cohere

import (
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToCohereResponsesRequest converts a BifrostRequest (Responses structure) to CohereChatRequest
func ToCohereResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *CohereChatRequest {
	if bifrostReq == nil {
		return nil
	}

	cohereReq := &CohereChatRequest{
		Model: bifrostReq.Model,
	}

	// Map basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxOutputTokens != nil {
			cohereReq.MaxTokens = bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			cohereReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			cohereReq.P = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.ExtraParams != nil {
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				cohereReq.K = topK
			}
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				cohereReq.StopSequences = stop
			}
			if frequencyPenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["frequency_penalty"]); ok {
				cohereReq.FrequencyPenalty = frequencyPenalty
			}
			if presencePenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["presence_penalty"]); ok {
				cohereReq.PresencePenalty = presencePenalty
			}
			if thinkingParam, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "thinking"); ok {
				if thinkingMap, ok := thinkingParam.(map[string]interface{}); ok {
					thinking := &CohereThinking{}
					if typeStr, ok := schemas.SafeExtractString(thinkingMap["type"]); ok {
						thinking.Type = CohereThinkingType(typeStr)
					}
					if tokenBudget, ok := schemas.SafeExtractIntPointer(thinkingMap["token_budget"]); ok {
						thinking.TokenBudget = tokenBudget
					}
					cohereReq.Thinking = thinking
				}
			}
		}
	}

	// Convert tools
	if bifrostReq.Params != nil && bifrostReq.Params.Tools != nil {
		var cohereTools []CohereChatRequestTool
		for _, tool := range bifrostReq.Params.Tools {
			if tool.ResponsesToolFunction != nil && tool.Name != nil {
				cohereTool := CohereChatRequestTool{
					Type: "function",
					Function: CohereChatRequestFunction{
						Name:        *tool.Name,
						Description: tool.Description,
						Parameters:  tool.ResponsesToolFunction.Parameters,
					},
				}
				cohereTools = append(cohereTools, cohereTool)
			}
		}

		if len(cohereTools) > 0 {
			cohereReq.Tools = cohereTools
		}
	}

	// Convert tool choice
	if bifrostReq.Params != nil && bifrostReq.Params.ToolChoice != nil {
		cohereReq.ToolChoice = convertBifrostToolChoiceToCohereToolChoice(*bifrostReq.Params.ToolChoice)
	}

	// Process ResponsesInput (which contains the Responses items)
	if bifrostReq.Input != nil {
		cohereReq.Messages = convertResponsesMessagesToCohereMessages(bifrostReq.Input)
	}

	return cohereReq
}

// ToBifrostResponsesResponse converts CohereChatResponse to BifrostResponse (Responses structure)
func (response *CohereChatResponse) ToBifrostResponsesResponse() *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr(response.ID),
		CreatedAt: int(time.Now().Unix()), // Set current timestamp
	}

	// Convert usage information
	if response.Usage != nil {
		usage := &schemas.ResponsesResponseUsage{}

		if response.Usage.Tokens != nil {
			if response.Usage.Tokens.InputTokens != nil {
				usage.InputTokens = int(*response.Usage.Tokens.InputTokens)
			}
			if response.Usage.Tokens.OutputTokens != nil {
				usage.OutputTokens = int(*response.Usage.Tokens.OutputTokens)
			}
			usage.TotalTokens = usage.InputTokens + usage.OutputTokens
		}

		if response.Usage.CachedTokens != nil {
			usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{
				CachedTokens: int(*response.Usage.CachedTokens),
			}
		}

		bifrostResp.Usage = usage
	}

	// Convert output message to Responses format
	if response.Message != nil {
		outputMessages := convertCohereMessageToResponsesOutput(*response.Message)
		bifrostResp.Output = outputMessages
	}

	return bifrostResp
}

// Helper functions

// convertBifrostToolChoiceToCohere converts schemas.ToolChoice to CohereToolChoice
func convertBifrostToolChoiceToCohereToolChoice(toolChoice schemas.ResponsesToolChoice) *CohereToolChoice {
	toolChoiceString := toolChoice.ResponsesToolChoiceStr

	if toolChoiceString != nil {
		switch *toolChoiceString {
		case "none":
			choice := ToolChoiceNone
			return &choice
		case "required", "auto", "function":
			choice := ToolChoiceRequired
			return &choice
		default:
			choice := ToolChoiceRequired
			return &choice
		}
	}

	return nil
}

// convertResponsesMessagesToCohereMessages converts Responses items to Cohere messages
func convertResponsesMessagesToCohereMessages(messages []schemas.ResponsesMessage) []CohereMessage {
	var cohereMessages []CohereMessage
	var systemContent []string

	for _, msg := range messages {
		// Handle nil Type with default
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}

		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Handle nil Role with default
			role := "user"
			if msg.Role != nil {
				role = string(*msg.Role)
			}

			if role == "system" {
				// Collect system messages separately for Cohere
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						systemContent = append(systemContent, *msg.Content.ContentStr)
					} else if msg.Content.ContentBlocks != nil {
						for _, block := range msg.Content.ContentBlocks {
							if block.Text != nil {
								systemContent = append(systemContent, *block.Text)
							}
						}
					}
				}
			} else {
				cohereMsg := CohereMessage{
					Role: role,
				}

				// Convert content - only if Content is not nil
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
					} else if msg.Content.ContentBlocks != nil {
						contentBlocks := convertResponsesMessageContentBlocksToCohere(msg.Content.ContentBlocks)
						cohereMsg.Content = NewBlocksContent(contentBlocks)
					}
				}

				cohereMessages = append(cohereMessages, cohereMsg)
			}

		case schemas.ResponsesMessageTypeFunctionCall:
			// Handle function calls from Responses
			assistantMsg := CohereMessage{
				Role: "assistant",
			}

			// Extract function call details
			var cohereToolCalls []CohereToolCall
			toolCall := CohereToolCall{
				Type:     "function",
				Function: &CohereFunction{},
			}

			if msg.CallID != nil {
				toolCall.ID = msg.CallID
			}

			// Get function details from AssistantMessage
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Arguments != nil {
				toolCall.Function.Arguments = *msg.ResponsesToolMessage.Arguments
			}

			// Get name from ToolMessage if available
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
				toolCall.Function.Name = msg.ResponsesToolMessage.Name
			}

			cohereToolCalls = append(cohereToolCalls, toolCall)

			if len(cohereToolCalls) > 0 {
				assistantMsg.ToolCalls = cohereToolCalls
			}

			cohereMessages = append(cohereMessages, assistantMsg)

		case "function_call_output":
			// Handle function call outputs
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
				toolMsg := CohereMessage{
					Role: "tool",
				}

				// Extract content from ResponsesFunctionToolCallOutput if Content is not set
				// This is needed for OpenAI Responses API which uses an "output" field
				content := msg.Content
				if content == nil && msg.ResponsesToolMessage.Output != nil {
					content = &schemas.ResponsesMessageContent{}
					if msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
						content.ContentStr = msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
					} else if msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
						content.ContentBlocks = msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
					}
				}

				// Convert content - only if Content is not nil
				if content != nil {
					if content.ContentStr != nil {
						toolMsg.Content = NewStringContent(*content.ContentStr)
					} else if content.ContentBlocks != nil {
						contentBlocks := convertResponsesMessageContentBlocksToCohere(content.ContentBlocks)
						toolMsg.Content = NewBlocksContent(contentBlocks)
					}
				}

				toolMsg.ToolCallID = msg.ResponsesToolMessage.CallID

				cohereMessages = append(cohereMessages, toolMsg)
			}
		}
	}

	// Prepend system messages if any
	if len(systemContent) > 0 {
		systemMsg := CohereMessage{
			Role:    "system",
			Content: NewStringContent(strings.Join(systemContent, "\n")),
		}
		cohereMessages = append([]CohereMessage{systemMsg}, cohereMessages...)
	}

	return cohereMessages
}

// convertBifrostContentBlocksToCohere converts Bifrost content blocks to Cohere format
func convertResponsesMessageContentBlocksToCohere(blocks []schemas.ResponsesMessageContentBlock) []CohereContentBlock {
	var cohereBlocks []CohereContentBlock

	for _, block := range blocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText:
			if block.Text != nil {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: CohereContentBlockTypeText,
					Text: block.Text,
				})
			}
		case schemas.ResponsesInputMessageContentBlockTypeImage:
			if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil && *block.ResponsesInputMessageContentBlockImage.ImageURL != "" {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: CohereContentBlockTypeImage,
					ImageURL: &CohereImageURL{
						URL: *block.ResponsesInputMessageContentBlockImage.ImageURL,
					},
				})
			}
		case schemas.ResponsesOutputMessageContentTypeReasoning:
			if block.Text != nil {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type:     CohereContentBlockTypeThinking,
					Thinking: block.Text,
				})
			}
		}
	}

	return cohereBlocks
}

// convertCohereMessageToResponsesOutput converts Cohere message to Responses output format
func convertCohereMessageToResponsesOutput(cohereMsg CohereMessage) []schemas.ResponsesMessage {
	var outputMessages []schemas.ResponsesMessage

	// Handle text content first
	if cohereMsg.Content != nil {
		var content schemas.ResponsesMessageContent

		var contentBlocks []schemas.ResponsesMessageContentBlock

		if cohereMsg.Content.StringContent != nil {
			contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: cohereMsg.Content.StringContent,
			})
		} else if cohereMsg.Content.BlocksContent != nil {
			// Convert content blocks
			for _, block := range cohereMsg.Content.BlocksContent {
				contentBlocks = append(contentBlocks, convertCohereContentBlockToBifrost(block))
			}
		}
		content.ContentBlocks = contentBlocks

		// Create message output
		if content.ContentBlocks != nil {
			outputMsg := schemas.ResponsesMessage{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &content,
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			}

			outputMessages = append(outputMessages, outputMsg)
		}
	}

	// Handle tool calls
	if cohereMsg.ToolCalls != nil {
		for _, toolCall := range cohereMsg.ToolCalls {
			// Check if Function is nil to avoid nil pointer dereference
			if toolCall.Function == nil {
				// Skip this tool call if Function is nil
				continue
			}

			// Safely extract function name and arguments
			var functionName *string
			var functionArguments *string

			if toolCall.Function.Name != nil {
				functionName = toolCall.Function.Name
			} else {
				// Use empty string if Name is nil
				functionName = schemas.Ptr("")
			}

			// Arguments is a string, not a pointer, so it's safe to access directly
			functionArguments = schemas.Ptr(toolCall.Function.Arguments)

			toolCallMsg := schemas.ResponsesMessage{
				ID:     toolCall.ID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      functionName,
					CallID:    toolCall.ID,
					Arguments: functionArguments,
				},
			}

			outputMessages = append(outputMessages, toolCallMsg)
		}
	}

	return outputMessages
}

// convertCohereContentBlockToBifrost converts CohereContentBlock to schemas.ContentBlock for Responses
func convertCohereContentBlockToBifrost(cohereBlock CohereContentBlock) schemas.ResponsesMessageContentBlock {
	switch cohereBlock.Type {
	case CohereContentBlockTypeText:
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeText,
			Text: cohereBlock.Text,
		}
	case CohereContentBlockTypeImage:
		// For images, create a text block describing the image
		if cohereBlock.ImageURL == nil {
			// Skip invalid image blocks without ImageURL
			return schemas.ResponsesMessageContentBlock{}
		}
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &cohereBlock.ImageURL.URL,
			},
		}
	case CohereContentBlockTypeThinking:
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesOutputMessageContentTypeReasoning,
			Text: cohereBlock.Thinking,
		}
	default:
		// Fallback to text block
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeText,
			Text: schemas.Ptr(string(cohereBlock.Type)),
		}
	}
}

func (chunk *CohereStreamEvent) ToBifrostResponsesStream(sequenceNumber int) (*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError, bool) {
	switch chunk.Type {
	case StreamEventMessageStart:
		messageType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant

		item := &schemas.ResponsesMessage{
			ID:   chunk.ID,
			Type: &messageType,
			Role: &role,
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr(""), // Empty content initially
			},
		}

		return &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			Item:           item,
		}, nil, false
	case StreamEventContentStart:
		// Content block start - create content part added event
		if chunk.Delta != nil && chunk.Index != nil && chunk.Delta.Message != nil && chunk.Delta.Message.Content != nil && chunk.Delta.Message.Content.CohereStreamContentObject != nil {
			var contentType schemas.ResponsesMessageContentBlockType
			var part *schemas.ResponsesMessageContentBlock

			switch chunk.Delta.Message.Content.CohereStreamContentObject.Type {
			case CohereContentBlockTypeText:
				contentType = schemas.ResponsesOutputMessageContentTypeText
				part = &schemas.ResponsesMessageContentBlock{
					Type: contentType,
					Text: schemas.Ptr(""), // Empty text initially
				}
			case CohereContentBlockTypeThinking:
				// This is a function call starting
				contentType = schemas.ResponsesOutputMessageContentTypeReasoning // Will be updated to function call
				part = &schemas.ResponsesMessageContentBlock{
					Type: contentType,
					Text: schemas.Ptr(""), // Will contain function call info
				}
			}

			if part != nil {
				return &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					ContentIndex:   chunk.Index,
					Part:           part,
				}, nil, false
			}
		}
	case StreamEventContentDelta:
		if chunk.Index != nil && chunk.Delta != nil {
			// Handle text content delta
			if chunk.Delta.Message != nil && chunk.Delta.Message.Content != nil && chunk.Delta.Message.Content.CohereStreamContentObject != nil && chunk.Delta.Message.Content.CohereStreamContentObject.Text != nil && *chunk.Delta.Message.Content.CohereStreamContentObject.Text != "" {
				return &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					ContentIndex:   chunk.Index,
					Delta:          chunk.Delta.Message.Content.CohereStreamContentObject.Text,
				}, nil, false
			}
		}
		return nil, nil, false
	case StreamEventContentEnd:
		// Content block is complete
		if chunk.Index != nil {
			return &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				ContentIndex:   chunk.Index,
			}, nil, false
		}
	case StreamEventToolPlanDelta:
		if chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolPlan != nil && *chunk.Delta.Message.ToolPlan != "" {
			// Tool plan delta - map to reasoning summary text delta
			return &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				ContentIndex:   schemas.Ptr(0), // Tool plan is typically at index 0
				Delta:          chunk.Delta.Message.ToolPlan,
			}, nil, false
		}
		return nil, nil, false
	case StreamEventToolCallStart:
		if chunk.Index != nil && chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolCalls != nil && chunk.Delta.Message.ToolCalls.CohereToolCallObject != nil {
			// Tool call start - create function call message
			toolCall := chunk.Delta.Message.ToolCalls.CohereToolCallObject
			if toolCall.Function != nil && toolCall.Function.Name != nil {
				messageType := schemas.ResponsesMessageTypeFunctionCall

				item := &schemas.ResponsesMessage{
					ID:   toolCall.ID,
					Type: &messageType,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    toolCall.ID,
						Name:      toolCall.Function.Name,
						Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
					},
				}

				return &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					Item:           item,
				}, nil, false
			}
		}
		return nil, nil, false
	case StreamEventToolCallDelta:
		if chunk.Index != nil && chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolCalls != nil && chunk.Delta.Message.ToolCalls.CohereToolCallObject != nil {
			// Tool call delta - handle function arguments streaming
			toolCall := chunk.Delta.Message.ToolCalls.CohereToolCallObject
			if toolCall.Function != nil {
				return &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
					SequenceNumber: sequenceNumber,
					ContentIndex:   chunk.Index,
					OutputIndex:    schemas.Ptr(0),
					Delta:          schemas.Ptr(toolCall.Function.Arguments),
				}, nil, false
			}
		}
		return nil, nil, false
	case StreamEventToolCallEnd:
		if chunk.Index != nil {
			// Tool call end - indicate function call arguments are complete
			return &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				ContentIndex:   chunk.Index,
			}, nil, false
		}
		return nil, nil, false
	case StreamEventCitationStart:
		if chunk.Index != nil && chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.Citations != nil {
			// Citation start - create annotation for the citation
			citation := chunk.Delta.Message.Citations.CohereStreamCitationObject

			// Map Cohere citation to ResponsesOutputMessageContentTextAnnotation
			annotation := &schemas.ResponsesOutputMessageContentTextAnnotation{
				Type:       "file_citation", // Default to file_citation
				StartIndex: schemas.Ptr(citation.Start),
				EndIndex:   schemas.Ptr(citation.End),
			}

			// Set annotation type and metadata
			if len(citation.Sources) > 0 {
				source := citation.Sources[0]

				if source.ID != nil {
					annotation.FileID = source.ID
				}

				if source.Document != nil {
					if title, ok := (*source.Document)["title"].(string); ok {
						annotation.Title = &title
					}
					if id, ok := (*source.Document)["id"].(string); ok && annotation.FileID == nil {
						annotation.FileID = &id
					}
					if snippet, ok := (*source.Document)["snippet"].(string); ok {
						annotation.Text = &snippet
					}
					if url, ok := (*source.Document)["url"].(string); ok {
						annotation.URL = &url
					}
				}
			}

			return &schemas.BifrostResponsesStreamResponse{
				Type:            schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
				SequenceNumber:  sequenceNumber,
				ContentIndex:    schemas.Ptr(citation.ContentIndex),
				Annotation:      annotation,
				OutputIndex:     schemas.Ptr(0),
				AnnotationIndex: chunk.Index,
			}, nil, false
		}
		return nil, nil, false
	case StreamEventCitationEnd:
		if chunk.Index != nil {
			// Citation end - indicate annotation is complete
			return &schemas.BifrostResponsesStreamResponse{
				Type:            schemas.ResponsesStreamResponseTypeOutputTextAnnotationDone,
				SequenceNumber:  sequenceNumber,
				ContentIndex:    chunk.Index,
				OutputIndex:     schemas.Ptr(0),
				AnnotationIndex: chunk.Index,
			}, nil, false
		}
		return nil, nil, false
	case StreamEventMessageEnd:
		response := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: sequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			Response:       &schemas.BifrostResponsesResponse{}, // Initialize Response field
		}

		if chunk.Delta != nil {
			if chunk.Delta.Usage != nil {
				usage := &schemas.ResponsesResponseUsage{}

				if chunk.Delta.Usage.Tokens != nil {
					if chunk.Delta.Usage.Tokens.InputTokens != nil {
						usage.InputTokens = int(*chunk.Delta.Usage.Tokens.InputTokens)
					}
					if chunk.Delta.Usage.Tokens.OutputTokens != nil {
						usage.OutputTokens = int(*chunk.Delta.Usage.Tokens.OutputTokens)
					}
					usage.TotalTokens = usage.InputTokens + usage.OutputTokens
				}

				if chunk.Delta.Usage.CachedTokens != nil {
					usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{
						CachedTokens: int(*chunk.Delta.Usage.CachedTokens),
					}
				}
				response.Response.Usage = usage
			}
		}

		return response, nil, true
	case StreamEventDebug:
		return nil, nil, false
	}
	return nil, nil, false
}
