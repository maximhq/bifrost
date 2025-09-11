package gemini

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func (request *GeminiGenerationRequest) ToResponsesAPIBifrostRequest() *schemas.BifrostRequest {
	return nil
}

func ToGeminiResponsesAPIResponse(bifrostResp *schemas.BifrostResponse) *GenerateContentResponse {
	return nil
}

func ToGeminiResponsesAPIRequest(bifrostReq *schemas.BifrostRequest) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		geminiReq.GenerationConfig = convertParamsToGenerationConfigResponsesAPI(bifrostReq.Params)

		// Handle tool-related parameters
		if bifrostReq.Params.Tools != nil && len(*bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools = convertResponsesAPIToolsToGemini(*bifrostReq.Params.Tools)

			// Convert tool choice if present
			if bifrostReq.Params.ToolChoice != nil {
				geminiReq.ToolConfig = convertResponsesAPIToolChoiceToGemini(bifrostReq.Params.ToolChoice)
			}
		}
	}

	// Convert ResponsesInput messages to Gemini contents
	if bifrostReq.Input.ResponsesInput != nil {
		contents, systemInstruction := convertResponsesAPIMessagesToGeminiContents(*bifrostReq.Input.ResponsesInput)
		geminiReq.Contents = contents

		if systemInstruction != nil {
			geminiReq.SystemInstruction = systemInstruction
		}
	}

	return geminiReq
}

func (response *GenerateContentResponse) ToResponsesAPIBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	// Parse model string to get provider and model

	// Create the BifrostResponse with ResponsesAPI structure
	bifrostResp := &schemas.BifrostResponse{
		ID:    response.ResponseID,
		Model: response.ModelVersion,
		ResponseAPIExtendedResponse: &schemas.ResponseAPIExtendedResponse{
			ResponsesAPIExtendedRequestParams: &schemas.ResponsesAPIExtendedRequestParams{
				Model: response.ModelVersion,
			},
			CreatedAt: func() int {
				if !response.CreateTime.IsZero() {
					return int(response.CreateTime.Unix())
				}
				return 0
			}(),
		},
	}

	// Convert usage information
	if response.UsageMetadata != nil {
		bifrostResp.Usage = &schemas.LLMUsage{
			PromptTokens:     int(response.UsageMetadata.PromptTokenCount),
			CompletionTokens: int(response.UsageMetadata.CandidatesTokenCount),
			TotalTokens:      int(response.UsageMetadata.TotalTokenCount),
			ResponsesAPIExtendedResponseUsage: &schemas.ResponsesAPIExtendedResponseUsage{
				InputTokens:  int(response.UsageMetadata.PromptTokenCount),
				OutputTokens: int(response.UsageMetadata.CandidatesTokenCount),
			},
		}

		// Handle cached tokens if present
		if response.UsageMetadata.CachedContentTokenCount > 0 {
			if bifrostResp.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails == nil {
				bifrostResp.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails = &schemas.ResponsesAPIResponseInputTokens{}
			}
			bifrostResp.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails.CachedTokens = int(response.UsageMetadata.CachedContentTokenCount)
		}
	}

	// Convert candidates to ResponsesAPI output messages
	if len(response.Candidates) > 0 {
		outputMessages := convertGeminiCandidatesToResponsesAPIOutput(response.Candidates)
		if len(outputMessages) > 0 {
			bifrostResp.ResponseAPIExtendedResponse.Output = outputMessages
		}
	}

	return bifrostResp
}

// Helper functions for ResponsesAPI conversion
// convertGeminiCandidatesToResponsesAPIOutput converts Gemini candidates to ResponsesAPI output messages
func convertGeminiCandidatesToResponsesAPIOutput(candidates []*Candidate) []schemas.ChatMessage {
	var messages []schemas.ChatMessage

	for _, candidate := range candidates {
		if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
			continue
		}

		for _, part := range candidate.Content.Parts {
			// Handle different types of parts
			switch {
			case part.Text != "":
				// Regular text message
				msg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{
						ContentStr: &part.Text,
					},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type: schemas.Ptr("message"),
					},
				}
				messages = append(messages, msg)

			case part.Thought:
				// Thinking/reasoning message
				if part.Text != "" {
					msg := schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: schemas.ChatMessageContent{
							ContentStr: &part.Text,
						},
						ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
							Type: schemas.Ptr("reasoning"),
						},
					}
					messages = append(messages, msg)
				}

			case part.FunctionCall != nil:
				// Function call message
				msg := schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type:   schemas.Ptr("function_call"),
						ID:     &part.FunctionCall.ID,
						Status: schemas.Ptr("completed"),
					},
					AssistantMessage: &schemas.AssistantMessage{
						ResponsesAPIExtendedAssistantMessage: &schemas.ResponsesAPIExtendedAssistantMessage{},
					},
				}
				messages = append(messages, msg)

			case part.FunctionResponse != nil:
				// Function response message
				output := ""
				if part.FunctionResponse.Response != nil {
					if outputVal, ok := part.FunctionResponse.Response["output"]; ok {
						if outputStr, ok := outputVal.(string); ok {
							output = outputStr
						}
					}
				}

				msg := schemas.ChatMessage{
					Role: schemas.ModelChatMessageRoleTool,
					Content: schemas.ChatMessageContent{
						ContentStr: &output,
					},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type: schemas.Ptr("function_call_output"),
					},
					ToolMessage: &schemas.ToolMessage{
						ResponsesAPIToolMessage: &schemas.ResponsesAPIToolMessage{
							CallID: &part.FunctionResponse.ID,
						},
					},
				}
				messages = append(messages, msg)

			case part.InlineData != nil:
				// Handle inline data (images, audio, etc.)
				contentBlocks := []schemas.ContentBlock{
					{
						Type: func() schemas.ContentBlockType {
							if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
								return schemas.ContentBlockTypeImage
							} else if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
								return schemas.ContentBlockTypeInputAudio
							}
							return schemas.ContentBlockTypeText
						}(),
						ResponsesAPIExtendedContentBlock: &schemas.ResponsesAPIExtendedContentBlock{
							InputMessageContentBlockImage: func() *schemas.InputMessageContentBlockImage {
								if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
									return &schemas.InputMessageContentBlockImage{
										ImageURL: schemas.Ptr("data:" + part.InlineData.MIMEType + ";base64," + string(part.InlineData.Data)),
									}
								}
								return nil
							}(),
						},
					},
				}

				msg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{
						ContentBlocks: &contentBlocks,
					},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type: schemas.Ptr("message"),
					},
				}
				messages = append(messages, msg)

			case part.FileData != nil:
				// Handle file data
				contentBlocks := []schemas.ContentBlock{
					{
						Type: func() schemas.ContentBlockType {
							if strings.HasPrefix(part.FileData.MIMEType, "image/") {
								return schemas.ContentBlockTypeImage
							} else if strings.HasPrefix(part.FileData.MIMEType, "audio/") {
								return schemas.ContentBlockTypeInputAudio
							}
							return schemas.ContentBlockTypeText
						}(),
						ResponsesAPIExtendedContentBlock: &schemas.ResponsesAPIExtendedContentBlock{
							InputMessageContentBlockImage: func() *schemas.InputMessageContentBlockImage {
								if strings.HasPrefix(part.FileData.MIMEType, "image/") {
									return &schemas.InputMessageContentBlockImage{
										ImageURL: schemas.Ptr(part.FileData.FileURI),
									}
								}
								return nil
							}(),
						},
					},
				}

				msg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{
						ContentBlocks: &contentBlocks,
					},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type: schemas.Ptr("message"),
					},
				}
				messages = append(messages, msg)

			case part.CodeExecutionResult != nil:
				// Handle code execution results
				output := part.CodeExecutionResult.Output
				if part.CodeExecutionResult.Outcome != OutcomeOK {
					output = "Error: " + output
				}

				msg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{
						ContentStr: &output,
					},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type: schemas.Ptr("code_interpreter_call"),
					},
				}
				messages = append(messages, msg)

			case part.ExecutableCode != nil:
				// Handle executable code
				codeContent := "```" + part.ExecutableCode.Language + "\n" + part.ExecutableCode.Code + "\n```"

				msg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{
						ContentStr: &codeContent,
					},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type: schemas.Ptr("message"),
					},
				}
				messages = append(messages, msg)
			}
		}
	}

	return messages
}

// convertParamsToGenerationConfigResponsesAPI converts ModelParameters to GenerationConfig for ResponsesAPI
func convertParamsToGenerationConfigResponsesAPI(params *schemas.ModelParameters) GenerationConfig {
	config := GenerationConfig{}

	if params.Temperature != nil {
		config.Temperature = schemas.Ptr(float32(*params.Temperature))
	}
	if params.TopP != nil {
		config.TopP = schemas.Ptr(float32(*params.TopP))
	}
	if params.TopK != nil {
		topK := float32(*params.TopK)
		config.TopK = &topK
	}
	if params.MaxTokens != nil {
		config.MaxOutputTokens = int32(*params.MaxTokens)
	}
	if params.StopSequences != nil {
		config.StopSequences = *params.StopSequences
	}
	if params.FrequencyPenalty != nil {
		config.FrequencyPenalty = schemas.Ptr(float32(*params.FrequencyPenalty))
	}
	if params.PresencePenalty != nil {
		config.PresencePenalty = schemas.Ptr(float32(*params.PresencePenalty))
	}

	return config
}

// convertResponsesAPIToolsToGemini converts ResponsesAPI tools to Gemini tools
func convertResponsesAPIToolsToGemini(tools []schemas.Tool) []Tool {
	var geminiTools []Tool

	for _, tool := range tools {
		if tool.Type != nil && *tool.Type == "function" {
			geminiTool := Tool{}

			// Extract function information from ResponsesAPIExtendedTool
			if tool.ResponsesAPIExtendedTool != nil {
				if tool.ResponsesAPIExtendedTool.Name != nil && tool.ResponsesAPIExtendedTool.ToolFunction != nil {
					funcDecl := &FunctionDeclaration{
						Name: *tool.ResponsesAPIExtendedTool.Name,
						Description: func() string {
							if tool.ResponsesAPIExtendedTool.Description != nil {
								return *tool.ResponsesAPIExtendedTool.Description
							}
							return ""
						}(),
						Parameters: convertFunctionParametersToGeminiSchema(tool.ResponsesAPIExtendedTool.ToolFunction.Parameters),
					}
					geminiTool.FunctionDeclarations = []*FunctionDeclaration{funcDecl}
				}
			}

			if len(geminiTool.FunctionDeclarations) > 0 {
				geminiTools = append(geminiTools, geminiTool)
			}
		}
	}

	return geminiTools
}

// convertResponsesAPIToolChoiceToGemini converts ResponsesAPI tool choice to Gemini tool config
func convertResponsesAPIToolChoiceToGemini(toolChoice *schemas.ToolChoice) ToolConfig {
	config := ToolConfig{}

	if toolChoice.ToolChoiceStruct != nil && toolChoice.ToolChoiceStruct.ResponsesAPIExtendedToolChoice != nil {
		funcConfig := &FunctionCallingConfig{}
		ext := toolChoice.ToolChoiceStruct.ResponsesAPIExtendedToolChoice

		if ext.Mode != nil {
			switch *ext.Mode {
			case "auto":
				funcConfig.Mode = FunctionCallingConfigModeAuto
			case "required":
				funcConfig.Mode = FunctionCallingConfigModeAny
			case "none":
				funcConfig.Mode = FunctionCallingConfigModeNone
			}
		}

		if ext.Name != nil {
			funcConfig.Mode = FunctionCallingConfigModeAny
			funcConfig.AllowedFunctionNames = []string{*ext.Name}
		}

		config.FunctionCallingConfig = funcConfig
	}

	return config
}

// convertFunctionParametersToGeminiSchema converts function parameters to Gemini Schema
func convertFunctionParametersToGeminiSchema(params schemas.FunctionParameters) *Schema {
	schema := &Schema{
		Type: Type(params.Type),
	}

	if params.Description != nil {
		schema.Description = *params.Description
	}

	if params.Properties != nil {
		schema.Properties = make(map[string]*Schema)
		for key, prop := range params.Properties {
			propSchema := convertPropertyToGeminiSchema(prop)
			schema.Properties[key] = propSchema
		}
	}

	if params.Required != nil {
		schema.Required = params.Required
	}

	return schema
}

// convertPropertyToGeminiSchema converts a property to Gemini Schema
func convertPropertyToGeminiSchema(prop interface{}) *Schema {
	schema := &Schema{}

	// Handle property as map[string]interface{}
	if propMap, ok := prop.(map[string]interface{}); ok {
		if propType, exists := propMap["type"]; exists {
			if typeStr, ok := propType.(string); ok {
				schema.Type = Type(typeStr)
			}
		}

		if desc, exists := propMap["description"]; exists {
			if descStr, ok := desc.(string); ok {
				schema.Description = descStr
			}
		}

		if enum, exists := propMap["enum"]; exists {
			if enumSlice, ok := enum.([]interface{}); ok {
				var enumStrs []string
				for _, item := range enumSlice {
					if str, ok := item.(string); ok {
						enumStrs = append(enumStrs, str)
					}
				}
				schema.Enum = enumStrs
			}
		}

		// Handle nested properties for object types
		if props, exists := propMap["properties"]; exists {
			if propsMap, ok := props.(map[string]interface{}); ok {
				schema.Properties = make(map[string]*Schema)
				for key, nestedProp := range propsMap {
					schema.Properties[key] = convertPropertyToGeminiSchema(nestedProp)
				}
			}
		}

		// Handle array items
		if items, exists := propMap["items"]; exists {
			schema.Items = convertPropertyToGeminiSchema(items)
		}
	}

	return schema
}

// convertResponsesAPIMessagesToGeminiContents converts ResponsesAPI messages to Gemini contents
func convertResponsesAPIMessagesToGeminiContents(messages []schemas.ChatMessage) ([]CustomContent, *CustomContent) {
	var contents []CustomContent
	var systemInstruction *CustomContent

	for _, msg := range messages {
		// Handle system messages separately
		if msg.Role == schemas.ModelChatMessageRoleSystem {
			if systemInstruction == nil {
				systemInstruction = &CustomContent{}
			}

			// Convert system message content
			if msg.Content.ContentStr != nil {
				systemInstruction.Parts = append(systemInstruction.Parts, &CustomPart{
					Text: *msg.Content.ContentStr,
				})
			}

			if msg.Content.ContentBlocks != nil {
				for _, block := range *msg.Content.ContentBlocks {
					part := convertContentBlockToGeminiPart(block)
					if part != nil {
						systemInstruction.Parts = append(systemInstruction.Parts, part)
					}
				}
			}
			continue
		}

		// Handle regular messages
		content := CustomContent{
			Role: string(msg.Role),
		}

		// Convert message content
		if msg.Content.ContentStr != nil {
			content.Parts = append(content.Parts, &CustomPart{
				Text: *msg.Content.ContentStr,
			})
		}

		if msg.Content.ContentBlocks != nil {
			for _, block := range *msg.Content.ContentBlocks {
				part := convertContentBlockToGeminiPart(block)
				if part != nil {
					content.Parts = append(content.Parts, part)
				}
			}
		}

		// Handle tool calls from assistant messages
		if msg.AssistantMessage != nil && msg.AssistantMessage.ResponsesAPIExtendedAssistantMessage != nil {

		}

		// Handle tool results from tool messages
		if msg.ToolMessage != nil && msg.ToolMessage.ResponsesAPIToolMessage != nil {

		}

		if len(content.Parts) > 0 {
			contents = append(contents, content)
		}
	}

	return contents, systemInstruction
}

// convertContentBlockToGeminiPart converts a content block to Gemini part
func convertContentBlockToGeminiPart(block schemas.ContentBlock) *CustomPart {
	switch block.Type {
	case schemas.ContentBlockTypeText:
		if block.Text != nil {
			return &CustomPart{
				Text: *block.Text,
			}
		}

	case schemas.ContentBlockTypeImage:
		if block.ResponsesAPIExtendedContentBlock != nil &&
			block.ResponsesAPIExtendedContentBlock.InputMessageContentBlockImage != nil {

		}
	}

	return nil
}
