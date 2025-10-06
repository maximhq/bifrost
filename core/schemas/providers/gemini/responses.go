package gemini

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func ToGeminiResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) (*GeminiGenerationRequest, error) {
	if bifrostReq == nil {
		return nil, nil
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
		contents, systemInstruction, err := convertResponsesMessagesToGeminiContents(bifrostReq.Input)
		if err != nil {
			return nil, fmt.Errorf("failed to convert messages: %w", err)
		}
		geminiReq.Contents = contents

		if systemInstruction != nil {
			geminiReq.SystemInstruction = systemInstruction
		}
	}

	return geminiReq, nil
}

func (response *GenerateContentResponse) ToResponsesBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	// Parse model string to get provider and model

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponse{
		ID:     response.ResponseID,
		Object: "response",
		Model:  response.ModelVersion,
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
			// Initialize ResponsesResponse if not already allocated
			if bifrostResp.ResponsesResponse == nil {
				bifrostResp.ResponsesResponse = &schemas.ResponsesResponse{}
			}
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
				// Convert Args to JSON string if it's not already a string
				argumentsStr := ""
				if part.FunctionCall.Args != nil {
					if argsBytes, err := json.Marshal(part.FunctionCall.Args); err == nil {
						argumentsStr = string(argsBytes)
					}
				}

				// Create copies of the values to avoid range loop variable capture
				functionCallID := part.FunctionCall.ID
				functionCallName := part.FunctionCall.Name

				msg := schemas.ResponsesMessage{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{},
					Type:    schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &functionCallID,
						Name:      &functionCallName,
						Arguments: &argumentsStr,
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
						CallID: schemas.Ptr(part.FunctionResponse.ID),
					},
				}

				// Also set the tool name if present (Gemini associates on name)
				if name := strings.TrimSpace(part.FunctionResponse.Name); name != "" {
					msg.ResponsesToolMessage.Name = schemas.Ptr(name)
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
									ImageURL: schemas.Ptr("data:" + part.InlineData.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(part.InlineData.Data)),
								}
							}
							return nil
						}(),
						Audio: func() *schemas.ResponsesInputMessageContentBlockAudio {
							if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
								// Extract format from MIME type (e.g., "audio/wav" -> "wav")
								format := strings.TrimPrefix(part.InlineData.MIMEType, "audio/")
								return &schemas.ResponsesInputMessageContentBlockAudio{
									Format: format,
									Data:   base64.StdEncoding.EncodeToString(part.InlineData.Data),
								}
							}
							return nil
						}(),
					},
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: contentBlocks,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.FileData != nil:
				// Handle file data
				block := schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						FileURL: schemas.Ptr(part.FileData.FileURI),
					},
				}
				if strings.HasPrefix(part.FileData.MIMEType, "image/") {
					block.Type = schemas.ResponsesInputMessageContentBlockTypeImage
					block.ResponsesInputMessageContentBlockImage = &schemas.ResponsesInputMessageContentBlockImage{
						ImageURL: schemas.Ptr(part.FileData.FileURI),
					}
				}
				contentBlocks := []schemas.ResponsesMessageContentBlock{block}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: contentBlocks,
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
		config.Temperature = schemas.Ptr(float64(*params.Temperature))
	}
	if params.TopP != nil {
		config.TopP = schemas.Ptr(float64(*params.TopP))
	}
	if params.MaxOutputTokens != nil {
		config.MaxOutputTokens = int32(*params.MaxOutputTokens)
	}

	if params.ExtraParams != nil {
		if topK, ok := params.ExtraParams["top_k"]; ok {
			if val, success := schemas.SafeExtractInt(topK); success {
				config.TopK = schemas.Ptr(val)
			}
		}
		if frequencyPenalty, ok := params.ExtraParams["frequency_penalty"]; ok {
			if val, success := schemas.SafeExtractFloat64(frequencyPenalty); success {
				config.FrequencyPenalty = schemas.Ptr(val)
			}
		}
		if presencePenalty, ok := params.ExtraParams["presence_penalty"]; ok {
			if val, success := schemas.SafeExtractFloat64(presencePenalty); success {
				config.PresencePenalty = schemas.Ptr(val)
			}
		}
		if stopSequences, ok := params.ExtraParams["stop_sequences"]; ok {
			if val, success := schemas.SafeExtractStringSlice(stopSequences); success {
				config.StopSequences = val
			}
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
						Parameters: func() *Schema {
							if tool.ResponsesToolFunction.Parameters != nil {
								return convertFunctionParametersToGeminiSchema(*tool.ResponsesToolFunction.Parameters)
							}
							return nil
						}(),
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
		return config
	}

	// Handle string-based tool choice modes
	if toolChoice.ResponsesToolChoiceStr != nil {
		funcConfig := &FunctionCallingConfig{}
		switch *toolChoice.ResponsesToolChoiceStr {
		case "none":
			funcConfig.Mode = FunctionCallingConfigModeNone
		case "required", "any":
			funcConfig.Mode = FunctionCallingConfigModeAny
		default: // "auto" or any other value
			funcConfig.Mode = FunctionCallingConfigModeAuto
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
func convertResponsesMessagesToGeminiContents(messages []schemas.ResponsesMessage) ([]CustomContent, *CustomContent, error) {
	var contents []CustomContent
	var systemInstruction *CustomContent

	for _, msg := range messages {
		// Handle system messages separately
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
			if systemInstruction == nil {
				systemInstruction = &CustomContent{}
			}

			// Convert system message content
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemInstruction.Parts = append(systemInstruction.Parts, &CustomPart{
						Text: *msg.Content.ContentStr,
					})
				}
				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						part, err := convertContentBlockToGeminiPart(block)
						if err != nil {
							return nil, nil, fmt.Errorf("failed to convert system message content block: %w", err)
						}
						if part != nil {
							systemInstruction.Parts = append(systemInstruction.Parts, part)
						}
					}
				}
			}

			continue
		}

		// Handle regular messages
		content := CustomContent{}

		if msg.Role != nil {
			content.Role = string(*msg.Role)
		} else {
			content.Role = "user" // Default role if msg.Role is nil
		}

		// Convert message content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				content.Parts = append(content.Parts, &CustomPart{
					Text: *msg.Content.ContentStr,
				})
			}

			if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					part, err := convertContentBlockToGeminiPart(block)
					if err != nil {
						return nil, nil, fmt.Errorf("failed to convert message content block: %w", err)
					}
					if part != nil {
						content.Parts = append(content.Parts, part)
					}
				}
			}
		}

		// Handle tool calls from assistant messages
		if msg.ResponsesToolMessage != nil && msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall:
				// Convert function call to Gemini FunctionCall
				if msg.ResponsesToolMessage.Name != nil {
					argsMap := map[string]any{}
					if msg.ResponsesToolMessage.Arguments != nil {
						if err := sonic.Unmarshal([]byte(*msg.ResponsesToolMessage.Arguments), &argsMap); err != nil {
							return nil, nil, fmt.Errorf("failed to decode function call arguments: %w", err)
						}
					}

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
			case schemas.ResponsesMessageTypeFunctionCallOutput:
				// Convert function response to Gemini FunctionResponse
				if msg.ResponsesToolMessage.CallID != nil {
					responseMap := make(map[string]any)
					if msg.Content != nil && msg.Content.ContentStr != nil {
						responseMap["output"] = *msg.Content.ContentStr
					}

					// Prefer the declared tool name; fallback to CallID if the name is absent
					funcName := ""
					if msg.ResponsesToolMessage.Name != nil && strings.TrimSpace(*msg.ResponsesToolMessage.Name) != "" {
						funcName = *msg.ResponsesToolMessage.Name
					} else {
						funcName = *msg.ResponsesToolMessage.CallID
					}

					part := &CustomPart{
						FunctionResponse: &FunctionResponse{
							Name:     funcName,
							Response: responseMap,
						},
					}
					// Keep ID = CallID
					part.FunctionResponse.ID = *msg.ResponsesToolMessage.CallID
					content.Parts = append(content.Parts, part)
				}
			}
		}

		if len(content.Parts) > 0 {
			contents = append(contents, content)
		}
	}

	return contents, systemInstruction, nil
}

// convertContentBlockToGeminiPart converts a content block to Gemini part
func convertContentBlockToGeminiPart(block schemas.ResponsesMessageContentBlock) (*CustomPart, error) {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeText:
		if block.Text != nil {
			return &CustomPart{
				Text: *block.Text,
			}, nil
		}

	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
			imageURL := *block.ResponsesInputMessageContentBlockImage.ImageURL

			// Use existing utility functions to handle URL parsing
			sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
			if err != nil {
				return nil, fmt.Errorf("failed to sanitize image URL: %w", err)
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

				// Decode base64 data
				decodedData, err := base64.StdEncoding.DecodeString(data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode base64 image data: %w", err)
				}

				return &CustomPart{
					InlineData: &CustomBlob{
						MIMEType: mimeType,
						Data:     decodedData,
					},
				}, nil
			} else {
				return &CustomPart{
					FileData: &FileData{
						MIMEType: mimeType,
						FileURI:  sanitizedURL,
					},
				}, nil
			}
		}

	case schemas.ResponsesInputMessageContentBlockTypeAudio:
		if block.Audio != nil {
			// Decode base64 audio data
			decodedData, err := base64.StdEncoding.DecodeString(block.Audio.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to decode base64 audio data: %w", err)
			}

			return &CustomPart{
				InlineData: &CustomBlob{
					MIMEType: func() string {
						f := strings.ToLower(strings.TrimSpace(block.Audio.Format))
						if f == "" {
							return "audio/mpeg"
						}
						if strings.HasPrefix(f, "audio/") {
							return f
						}
						return "audio/" + f
					}(),
					Data: decodedData,
				},
			}, nil
		}

	case schemas.ResponsesInputMessageContentBlockTypeFile:
		if block.ResponsesInputMessageContentBlockFile != nil {
			if block.ResponsesInputMessageContentBlockFile.FileURL != nil {
				return &CustomPart{
					FileData: &FileData{
						MIMEType: "application/octet-stream", // default
						FileURI:  *block.ResponsesInputMessageContentBlockFile.FileURL,
					},
				}, nil
			} else if block.ResponsesInputMessageContentBlockFile.FileData != nil {
				return &CustomPart{
					InlineData: &CustomBlob{
						MIMEType: "application/octet-stream", // default
						Data:     []byte(*block.ResponsesInputMessageContentBlockFile.FileData),
					},
				}, nil
			}
		}
	}

	return nil, nil
}
