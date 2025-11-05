package gemini

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func (request *GeminiGenerationRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.Gemini)

	if provider == schemas.Vertex && !request.IsEmbedding {
		// Add google/ prefix for Bifrost if not already present
		if !strings.HasPrefix(model, "google/") {
			model = "google/" + model
		}
	}

	// Handle chat completion requests
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    []schemas.ChatMessage{},
	}

	messages := []schemas.ChatMessage{}
	// Track all tool calls from previous messages for function response correlation
	previousToolCalls := []schemas.ChatAssistantMessageToolCall{}

	allGenAiMessages := []Content{}
	if request.SystemInstruction != nil {
		allGenAiMessages = append(allGenAiMessages, *request.SystemInstruction)
	}
	for _, content := range request.Contents {
		allGenAiMessages = append(allGenAiMessages, content)
	}

	for _, content := range allGenAiMessages {
		if len(content.Parts) == 0 {
			continue
		}

		// Handle multiple parts - collect all content and tool calls
		var toolCalls []schemas.ChatAssistantMessageToolCall
		var contentBlocks []schemas.ChatContentBlock
		var thoughtStr string // Track thought content for assistant/model

		for _, part := range content.Parts {
			switch {
			case part.Text != "":
				// Handle thought content specially for assistant messages
				if part.Thought &&
					(content.Role == string(schemas.ChatMessageRoleAssistant) || content.Role == string(RoleModel)) {
					thoughtStr = thoughtStr + part.Text + "\n"
				} else {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeText,
						Text: &part.Text,
					})
				}

			case part.FunctionCall != nil:
				// Only add function calls for assistant messages
				if content.Role == string(schemas.ChatMessageRoleAssistant) || content.Role == string(RoleModel) {
					jsonArgs, err := json.Marshal(part.FunctionCall.Args)
					if err != nil {
						jsonArgs = []byte(fmt.Sprintf("%v", part.FunctionCall.Args))
					}
					name := part.FunctionCall.Name // create local copy
					// Gemini primarily works with function names for correlation
					// Use ID if provided, otherwise fallback to name for stable correlation
					callID := name
					if strings.TrimSpace(part.FunctionCall.ID) != "" {
						callID = part.FunctionCall.ID
					}
					toolCall := schemas.ChatAssistantMessageToolCall{
						ID:   schemas.Ptr(callID),
						Type: schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      &name,
							Arguments: string(jsonArgs),
						},
					}
					toolCalls = append(toolCalls, toolCall)
				}

			case part.FunctionResponse != nil:
				// Create a separate tool response message
				responseContent, err := json.Marshal(part.FunctionResponse.Response)
				if err != nil {
					responseContent = []byte(fmt.Sprintf("%v", part.FunctionResponse.Response))
				}

				// Correlate with the function call: prefer ID if available, otherwise use name
				callID := part.FunctionResponse.Name
				if strings.TrimSpace(part.FunctionResponse.ID) != "" {
					callID = part.FunctionResponse.ID
				} else {
					// Fallback: search through all previous tool calls to find matching one by name
					for _, tc := range previousToolCalls {
						if tc.Function.Name != nil && *tc.Function.Name == part.FunctionResponse.Name &&
							tc.ID != nil && *tc.ID != "" {
							callID = *tc.ID
							break
						}
					}
				}

				toolResponseMsg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleTool,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr(string(responseContent)),
					},
					ChatToolMessage: &schemas.ChatToolMessage{
						ToolCallID: &callID,
					},
				}

				messages = append(messages, toolResponseMsg)

			case part.InlineData != nil:
				// Handle inline images/media - only append if it's actually an image
				if isImageMimeType(part.InlineData.MIMEType) {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeImage,
						ImageURLStruct: &schemas.ChatInputImage{
							URL: fmt.Sprintf("data:%s;base64,%s", part.InlineData.MIMEType, base64.StdEncoding.EncodeToString(part.InlineData.Data)),
						},
					})
				}

			case part.FileData != nil:
				// Handle file data - only append if it's actually an image
				if isImageMimeType(part.FileData.MIMEType) {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeImage,
						ImageURLStruct: &schemas.ChatInputImage{
							URL: part.FileData.FileURI,
						},
					})
				}

			case part.ExecutableCode != nil:
				// Handle executable code as text content
				codeText := fmt.Sprintf("```%s\n%s\n```", part.ExecutableCode.Language, part.ExecutableCode.Code)
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: &codeText,
				})

			case part.CodeExecutionResult != nil:
				// Handle code execution results as text content
				resultText := fmt.Sprintf("Code execution result (%s):\n%s", part.CodeExecutionResult.Outcome, part.CodeExecutionResult.Output)
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: &resultText,
				})
			}
		}

		// Only create message if there's actual content, tool calls, or thought content
		if len(contentBlocks) > 0 || len(toolCalls) > 0 || thoughtStr != "" {
			// Create main message with content blocks
			bifrostMsg := schemas.ChatMessage{
				Role: func(r string) schemas.ChatMessageRole {
					if r == string(RoleModel) { // GenAI's internal alias
						return schemas.ChatMessageRoleAssistant
					}
					return schemas.ChatMessageRole(r)
				}(content.Role),
			}

			// Set content only if there are content blocks
			if len(contentBlocks) > 0 {
				bifrostMsg.Content = &schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				}
			}

			// Set assistant-specific fields for assistant/model messages
			if content.Role == string(schemas.ChatMessageRoleAssistant) || content.Role == string(RoleModel) {
				if len(toolCalls) > 0 || thoughtStr != "" {
					bifrostMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
					if len(toolCalls) > 0 {
						bifrostMsg.ChatAssistantMessage.ToolCalls = toolCalls
						// Track these tool calls for future function response correlation
						previousToolCalls = append(previousToolCalls, toolCalls...)
					}
				}
			}

			messages = append(messages, bifrostMsg)
		}
	}

	bifrostReq.Input = messages

	// Convert generation config to parameters
	if params := request.convertGenerationConfigToChatParameters(); params != nil {
		bifrostReq.Params = params
	}

	// Convert safety settings
	if len(request.SafetySettings) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["safety_settings"] = request.SafetySettings
	}

	// Convert additional request fields
	if request.CachedContent != "" {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["cached_content"] = request.CachedContent
	}

	// Convert labels
	if len(request.Labels) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["labels"] = request.Labels
	}

	// Convert tools and tool config
	if len(request.Tools) > 0 {
		ensureExtraParams(bifrostReq)

		tools := make([]schemas.ChatTool, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if len(tool.FunctionDeclarations) > 0 {
				for _, fn := range tool.FunctionDeclarations {
					bifrostTool := schemas.ChatTool{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:        fn.Name,
							Description: schemas.Ptr(fn.Description),
						},
					}
					// Convert parameters schema if present
					if fn.Parameters != nil {
						params := request.convertSchemaToFunctionParameters(fn.Parameters)
						bifrostTool.Function.Parameters = &params
					}
					tools = append(tools, bifrostTool)
				}
			}
			// Handle other tool types (Retrieval, GoogleSearch, etc.) as ExtraParams
			if tool.Retrieval != nil {
				bifrostReq.Params.ExtraParams["retrieval"] = tool.Retrieval
			}
			if tool.GoogleSearch != nil {
				bifrostReq.Params.ExtraParams["google_search"] = tool.GoogleSearch
			}
			if tool.CodeExecution != nil {
				bifrostReq.Params.ExtraParams["code_execution"] = tool.CodeExecution
			}
		}

		if len(tools) > 0 {
			bifrostReq.Params.Tools = tools
		}
	}

	// Convert tool config
	if request.ToolConfig.FunctionCallingConfig != nil || request.ToolConfig.RetrievalConfig != nil {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["tool_config"] = request.ToolConfig
	}

	return bifrostReq
}

// ToGeminiChatCompletionRequest converts a BifrostChatRequest to Gemini's generation request format for chat completion
func ToGeminiChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest, responseModalities []string) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		geminiReq.GenerationConfig = convertParamsToGenerationConfig(bifrostReq.Params, responseModalities)

		// Handle tool-related parameters
		if len(bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools = convertBifrostToolsToGemini(bifrostReq.Params.Tools)

			// Convert tool choice to tool config
			if bifrostReq.Params.ToolChoice != nil {
				geminiReq.ToolConfig = convertToolChoiceToToolConfig(bifrostReq.Params.ToolChoice)
			}
		}

		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			// Safety settings
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				if settings, ok := safetySettings.([]SafetySetting); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				if labelMap, ok := labels.(map[string]string); ok {
					geminiReq.Labels = labelMap
				}
			}
		}
	}

	// Convert chat completion messages to Gemini format
	geminiReq.Contents = convertBifrostMessagesToGemini(bifrostReq.Input)

	return geminiReq
}

// ToBifrostChatResponse converts a GenerateContentResponse to a BifrostChatResponse
func (response *GenerateContentResponse) ToBifrostChatResponse() *schemas.BifrostChatResponse {
	bifrostResp := &schemas.BifrostChatResponse{
		ID:     response.ResponseID,
		Model:  response.ModelVersion,
		Object: "chat.completion",
	}

	// Set creation timestamp if available
	if !response.CreateTime.IsZero() {
		bifrostResp.Created = int(response.CreateTime.Unix())
	}

	// Extract usage metadata
	inputTokens, outputTokens, totalTokens, cachedTokens, reasoningTokens := response.extractUsageMetadata()

	// Process candidates to extract text content
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			var textContent string

			// Extract text content from all parts
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					textContent += part.Text
				}
			}

			if textContent != "" {
				// Create choice from the candidate
				choice := schemas.BifrostResponseChoice{
					Index: 0,
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{
								ContentStr: &textContent,
							},
						},
					},
				}

				// Set finish reason if available
				if candidate.FinishReason != "" {
					finishReason := string(candidate.FinishReason)
					choice.FinishReason = &finishReason
				}

				bifrostResp.Choices = []schemas.BifrostResponseChoice{choice}
			}
		}
	}

	// Set usage information
	bifrostResp.Usage = &schemas.BifrostLLMUsage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      totalTokens,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedTokens: cachedTokens,
		},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: reasoningTokens,
		},
	}

	return bifrostResp
}

// ToGeminiChatResponse converts a BifrostChatResponse to Gemini's GenerateContentResponse
func ToGeminiChatResponse(bifrostResp *schemas.BifrostChatResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	genaiResp := &GenerateContentResponse{
		ResponseID:   bifrostResp.ID,
		ModelVersion: bifrostResp.Model,
	}

	// Set creation time if available
	if bifrostResp.Created > 0 {
		genaiResp.CreateTime = time.Unix(int64(bifrostResp.Created), 0)
	}

	if len(bifrostResp.Choices) > 0 {
		candidates := make([]*Candidate, len(bifrostResp.Choices))

		for i, choice := range bifrostResp.Choices {
			candidate := &Candidate{
				Index: int32(choice.Index),
			}

			if choice.FinishReason != nil {
				candidate.FinishReason = FinishReason(*choice.FinishReason)
			}

			// Convert message content to Gemini parts
			var parts []*Part
			var role string

			// Handle streaming responses
			if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
				delta := choice.ChatStreamResponseChoice.Delta

				// Set role from delta if available
				if delta.Role != nil {
					role = *delta.Role
				} else {
					role = "model" // Default role for streaming responses
				}

				// Handle content text
				if delta.Content != nil && *delta.Content != "" {
					parts = append(parts, &Part{Text: *delta.Content})
				}

				// Handle tool calls in streaming
				if delta.ToolCalls != nil {
					for _, toolCall := range delta.ToolCalls {
						argsMap := make(map[string]interface{})
						if toolCall.Function.Arguments != "" {
							json.Unmarshal([]byte(toolCall.Function.Arguments), &argsMap)
						}
						if toolCall.Function.Name != nil {
							fc := &FunctionCall{
								Name: *toolCall.Function.Name,
								Args: argsMap,
							}
							if toolCall.ID != nil {
								fc.ID = *toolCall.ID
							}
							parts = append(parts, &Part{FunctionCall: fc})
						}
					}
				}

				if len(parts) > 0 {
					candidate.Content = &Content{
						Parts: parts,
						Role:  role,
					}
				}
			} else if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
				// Handle non-streaming responses
				if choice.ChatNonStreamResponseChoice.Message.Content != nil {
					if choice.ChatNonStreamResponseChoice.Message.Content.ContentStr != nil && *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr != "" {
						parts = append(parts, &Part{Text: *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr})
					} else if choice.ChatNonStreamResponseChoice.Message.Content.ContentBlocks != nil {
						for _, block := range choice.ChatNonStreamResponseChoice.Message.Content.ContentBlocks {
							if block.Text != nil {
								parts = append(parts, &Part{Text: *block.Text})
							}
						}
					}
				}

				// Handle tool calls
				if choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage != nil && choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls != nil {
					for _, toolCall := range choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls {
						argsMap := make(map[string]interface{})
						if toolCall.Function.Arguments != "" {
							if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &argsMap); err != nil {
								argsMap = map[string]interface{}{}
							}
						}
						if toolCall.Function.Name != nil {
							fc := &FunctionCall{
								Name: *toolCall.Function.Name,
								Args: argsMap,
							}
							if toolCall.ID != nil {
								fc.ID = *toolCall.ID
							}
							parts = append(parts, &Part{FunctionCall: fc})
						}
					}
				}

				if len(parts) > 0 {
					candidate.Content = &Content{
						Parts: parts,
						Role:  string(choice.ChatNonStreamResponseChoice.Message.Role),
					}
				}
			}

			candidates[i] = candidate
		}

		genaiResp.Candidates = candidates
	}

	// Set usage metadata from LLM usage
	if bifrostResp.Usage != nil {
		genaiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(bifrostResp.Usage.PromptTokens),
			CandidatesTokenCount: int32(bifrostResp.Usage.CompletionTokens),
			TotalTokenCount:      int32(bifrostResp.Usage.TotalTokens),
		}
		if bifrostResp.Usage.PromptTokensDetails != nil {
			genaiResp.UsageMetadata.CachedContentTokenCount = int32(bifrostResp.Usage.PromptTokensDetails.CachedTokens)
		}
		if bifrostResp.Usage.CompletionTokensDetails != nil {
			genaiResp.UsageMetadata.ThoughtsTokenCount = int32(bifrostResp.Usage.CompletionTokensDetails.ReasoningTokens)
		}
	}

	return genaiResp
}
