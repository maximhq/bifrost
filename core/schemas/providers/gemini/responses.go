package gemini

import (
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func ToGeminiResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		geminiReq.GenerationConfig = convertParamsToGenerationConfigResponses(bifrostReq.Params)

		// Handle tool-related parameters
		if len(bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools = convertResponsesToolsToGemini(bifrostReq.Params.Tools)

			// Convert tool choice if present
			if bifrostReq.Params.ToolChoice != nil {
				geminiReq.ToolConfig = convertResponsesToolChoiceToGemini(bifrostReq.Params.ToolChoice)
			}
		}
	}

	// Convert ResponsesInput messages to Gemini contents
	if bifrostReq.Input != nil {
		contents, systemInstruction := convertResponsesMessagesToGeminiContents(bifrostReq.Input)
		geminiReq.Contents = contents

		if systemInstruction != nil {
			geminiReq.SystemInstruction = systemInstruction
		}
	}

	return geminiReq
}

func (response *GenerateContentResponse) ToResponsesBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	// Parse model string to get provider and model

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponse{
		ID:    response.ResponseID,
		Model: response.ModelVersion,
	}

	// Convert usage information
	if response.UsageMetadata != nil {
		bifrostResp.Usage = &schemas.LLMUsage{
			TotalTokens: int(response.UsageMetadata.TotalTokenCount),
			ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{
				InputTokens:  int(response.UsageMetadata.PromptTokenCount),
				OutputTokens: int(response.UsageMetadata.CandidatesTokenCount),
			},
		}

		// Handle cached tokens if present
		if response.UsageMetadata.CachedContentTokenCount > 0 {
			if bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails == nil {
				bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
			}
			bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails.CachedTokens = int(response.UsageMetadata.CachedContentTokenCount)
		}
	}

	// Convert candidates to Responses output messages
	if len(response.Candidates) > 0 {
		outputMessages := convertGeminiCandidatesToResponsesOutput(response.Candidates)
		if len(outputMessages) > 0 {
			bifrostResp.ResponsesResponse.Output = outputMessages
		}
	}

	return bifrostResp
}

// Helper functions for Responses conversion
// convertGeminiCandidatesToResponsesOutput converts Gemini candidates to Responses output messages
func convertGeminiCandidatesToResponsesOutput(candidates []*Candidate) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage

	for _, candidate := range candidates {
		if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
			continue
		}

		for _, part := range candidate.Content.Parts {
			// Handle different types of parts
			switch {
			case part.Text != "":
				// Regular text message
				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: &part.Text,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.Thought:
				// Thinking/reasoning message
				if part.Text != "" {
					msg := schemas.ResponsesMessage{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: &part.Text,
						},
						Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					}
					messages = append(messages, msg)
				}

			case part.FunctionCall != nil:
				// Function call message
				msg := schemas.ResponsesMessage{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{},
					Type:    schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: &part.FunctionCall.ID,
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

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: &output,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: &part.FunctionResponse.ID,
					},
				}
				messages = append(messages, msg)

			case part.InlineData != nil:
				// Handle inline data (images, audio, etc.)
				contentBlocks := []schemas.ResponsesMessageContentBlock{
					{
						Type: func() schemas.ResponsesMessageContentBlockType {
							if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
								return schemas.ResponsesInputMessageContentBlockTypeImage
							} else if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
								return schemas.ResponsesInputMessageContentBlockTypeAudio
							}
							return schemas.ResponsesInputMessageContentBlockTypeText
						}(),
						ResponsesInputMessageContentBlockImage: func() *schemas.ResponsesInputMessageContentBlockImage {
							if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
								return &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr("data:" + part.InlineData.MIMEType + ";base64," + string(part.InlineData.Data)),
								}
							}
							return nil
						}(),
					},
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: &contentBlocks,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.FileData != nil:
				// Handle file data
				contentBlocks := []schemas.ResponsesMessageContentBlock{
					{
						Type: func() schemas.ResponsesMessageContentBlockType {
							if strings.HasPrefix(part.FileData.MIMEType, "image/") {
								return schemas.ResponsesInputMessageContentBlockTypeImage
							} else if strings.HasPrefix(part.FileData.MIMEType, "audio/") {
								return schemas.ResponsesInputMessageContentBlockTypeAudio
							}
							return schemas.ResponsesInputMessageContentBlockTypeText
						}(),
						ResponsesInputMessageContentBlockImage: func() *schemas.ResponsesInputMessageContentBlockImage {
							if strings.HasPrefix(part.FileData.MIMEType, "image/") {
								return &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr(part.FileData.FileURI),
								}
							}
							return nil
						}(),
					},
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: &contentBlocks,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.CodeExecutionResult != nil:
				// Handle code execution results
				output := part.CodeExecutionResult.Output
				if part.CodeExecutionResult.Outcome != OutcomeOK {
					output = "Error: " + output
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: &output,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
				}
				messages = append(messages, msg)

			case part.ExecutableCode != nil:
				// Handle executable code
				codeContent := "```" + part.ExecutableCode.Language + "\n" + part.ExecutableCode.Code + "\n```"

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: &codeContent,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)
			}
		}
	}

	return messages
}

// convertParamsToGenerationConfigResponses converts ChatParameters to GenerationConfig for Responses
func convertParamsToGenerationConfigResponses(params *schemas.ResponsesParameters) GenerationConfig {
	config := GenerationConfig{}

	if params.Temperature != nil {
		config.Temperature = schemas.Ptr(float32(*params.Temperature))
	}
	if params.TopP != nil {
		config.TopP = schemas.Ptr(float32(*params.TopP))
	}
	if params.MaxOutputTokens != nil {
		config.MaxOutputTokens = int32(*params.MaxOutputTokens)
	}

	if params.ExtraParams != nil {
		if topK, ok := params.ExtraParams["top_k"]; ok {
			config.TopK = schemas.Ptr(float32(topK.(float64)))
		}
		if frequencyPenalty, ok := params.ExtraParams["frequency_penalty"]; ok {
			config.FrequencyPenalty = schemas.Ptr(float32(frequencyPenalty.(float64)))
		}
		if presencePenalty, ok := params.ExtraParams["presence_penalty"]; ok {
			config.PresencePenalty = schemas.Ptr(float32(presencePenalty.(float64)))
		}
		if stopSequences, ok := params.ExtraParams["stop_sequences"]; ok {
			config.StopSequences = stopSequences.([]string)
		}
	}

	return config
}

// convertResponsesToolsToGemini converts Responses tools to Gemini tools
func convertResponsesToolsToGemini(tools []schemas.ResponsesTool) []Tool {
	var geminiTools []Tool

	for _, tool := range tools {
		if tool.Type == "function" {
			geminiTool := Tool{}

			// Extract function information from ResponsesExtendedTool
			if tool.ResponsesToolFunction != nil {
				if tool.Name != nil && tool.ResponsesToolFunction != nil {
					funcDecl := &FunctionDeclaration{
						Name: *tool.Name,
						Description: func() string {
							if tool.Description != nil {
								return *tool.Description
							}
							return ""
						}(),
						Parameters: convertFunctionParametersToGeminiSchema(*tool.ResponsesToolFunction.Parameters),
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

// convertResponsesToolChoiceToGemini converts Responses tool choice to Gemini tool config
func convertResponsesToolChoiceToGemini(toolChoice *schemas.ResponsesToolChoice) ToolConfig {
	config := ToolConfig{}

	if toolChoice.ResponsesToolChoiceStruct != nil {
		funcConfig := &FunctionCallingConfig{}
		ext := toolChoice.ResponsesToolChoiceStruct

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
func convertFunctionParametersToGeminiSchema(params schemas.ToolFunctionParameters) *Schema {
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

// convertResponsesMessagesToGeminiContents converts Responses messages to Gemini contents
func convertResponsesMessagesToGeminiContents(messages []schemas.ResponsesMessage) ([]CustomContent, *CustomContent) {
	var contents []CustomContent
	var systemInstruction *CustomContent

	for _, msg := range messages {
		// Handle system messages separately
		if *msg.Role == schemas.ResponsesInputMessageRoleSystem {
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
			Role: string(*msg.Role),
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
		if msg.ResponsesToolMessage != nil && msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall:
				// Convert function call to Gemini FunctionCall
				if msg.ResponsesToolMessage.Name != nil {
					argsMap := make(map[string]any)
					if msg.ResponsesToolMessage.Arguments != nil {
						// Parse JSON arguments
						if err := sonic.Unmarshal([]byte(*msg.ResponsesToolMessage.Arguments), &argsMap); err == nil {
							part := &CustomPart{
								FunctionCall: &FunctionCall{
									Name: *msg.ResponsesToolMessage.Name,
									Args: argsMap,
								},
							}
							if msg.ResponsesToolMessage.CallID != nil {
								part.FunctionCall.ID = *msg.ResponsesToolMessage.CallID
							}
							content.Parts = append(content.Parts, part)
						}
					}
				}

			case schemas.ResponsesMessageTypeFunctionCallOutput:
				// Convert function response to Gemini FunctionResponse
				if msg.ResponsesToolMessage.CallID != nil {
					responseMap := make(map[string]any)
					if msg.Content != nil && msg.Content.ContentStr != nil {
						responseMap["output"] = *msg.Content.ContentStr
					}

					part := &CustomPart{
						FunctionResponse: &FunctionResponse{
							Name:     *msg.ResponsesToolMessage.CallID, // Use CallID as name
							Response: responseMap,
						},
					}
					if msg.ResponsesToolMessage.CallID != nil {
						part.FunctionResponse.ID = *msg.ResponsesToolMessage.CallID
					}
					content.Parts = append(content.Parts, part)
				}
			}
		}

		if len(content.Parts) > 0 {
			contents = append(contents, content)
		}
	}

	return contents, systemInstruction
}

// convertContentBlockToGeminiPart converts a content block to Gemini part
func convertContentBlockToGeminiPart(block schemas.ResponsesMessageContentBlock) *CustomPart {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeText:
		if block.Text != nil {
			return &CustomPart{
				Text: *block.Text,
			}
		}

	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
			imageURL := *block.ResponsesInputMessageContentBlockImage.ImageURL

			// Use existing utility functions to handle URL parsing
			sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
			if err != nil {
				return nil
			}

			urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)
			mimeType := "image/jpeg" // default
			if urlInfo.MediaType != nil {
				mimeType = *urlInfo.MediaType
			}

			if urlInfo.Type == schemas.ImageContentTypeBase64 {
				data := ""
				if urlInfo.DataURLWithoutPrefix != nil {
					data = *urlInfo.DataURLWithoutPrefix
				}

				return &CustomPart{
					InlineData: &CustomBlob{
						MIMEType: mimeType,
						Data:     []byte(data),
					},
				}
			} else {
				return &CustomPart{
					FileData: &FileData{
						MIMEType: mimeType,
						FileURI:  sanitizedURL,
					},
				}
			}
		}

	case schemas.ResponsesInputMessageContentBlockTypeAudio:
		if block.Audio != nil {
			return &CustomPart{
				InlineData: &CustomBlob{
					MIMEType: block.Audio.Format,       // "mp3" or "wav"
					Data:     []byte(block.Audio.Data), // base64 encoded audio data
				},
			}
		}

	case schemas.ResponsesInputMessageContentBlockTypeFile:
		if block.ResponsesInputMessageContentBlockFile != nil {
			if block.ResponsesInputMessageContentBlockFile.FileURL != nil {
				return &CustomPart{
					FileData: &FileData{
						MIMEType: "application/octet-stream", // default
						FileURI:  *block.ResponsesInputMessageContentBlockFile.FileURL,
					},
				}
			} else if block.ResponsesInputMessageContentBlockFile.FileData != nil {
				return &CustomPart{
					InlineData: &CustomBlob{
						MIMEType: "application/octet-stream", // default
						Data:     []byte(*block.ResponsesInputMessageContentBlockFile.FileData),
					},
				}
			}
		}
	}

	return nil
}

// convertGeminiContentsToResponsesMessages converts Gemini contents back to Responses messages
func convertGeminiContentsToResponsesMessages(contents []CustomContent) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage

	for _, content := range contents {
		msg := schemas.ResponsesMessage{
			Role:    schemas.Ptr(schemas.ResponsesMessageRoleType(content.Role)),
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Content: &schemas.ResponsesMessageContent{},
		}

		var textParts []string
		for _, part := range content.Parts {
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			// Handle function calls and other parts as needed
		}

		if len(textParts) > 0 {
			combinedText := strings.Join(textParts, "\n")
			msg.Content.ContentStr = &combinedText
		}

		messages = append(messages, msg)
	}

	return messages
}

// convertResponsesMessagesToGeminiCandidates converts Responses messages to Gemini candidates
func convertResponsesMessagesToGeminiCandidates(messages []schemas.ResponsesMessage) []*Candidate {
	var candidates []*Candidate

	for _, msg := range messages {
		candidate := &Candidate{
			Content: &Content{
				Role: string(*msg.Role),
			},
		}

		var parts []*Part
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				parts = append(parts, &Part{
					Text: *msg.Content.ContentStr,
				})
			}
		}

		candidate.Content.Parts = parts
		candidates = append(candidates, candidate)
	}

	return candidates
}
