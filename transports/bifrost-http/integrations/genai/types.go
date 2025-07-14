package genai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	genai_sdk "google.golang.org/genai"
)

var fnTypePtr = bifrost.Ptr(string(schemas.ToolChoiceTypeFunction))

// CustomBlob handles URL-safe base64 decoding for Google GenAI requests
type CustomBlob struct {
	Data     []byte `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// UnmarshalJSON custom unmarshalling to handle URL-safe base64 encoding
func (b *CustomBlob) UnmarshalJSON(data []byte) error {
	// First unmarshal into a temporary struct with string data
	var temp struct {
		Data     string `json:"data,omitempty"`
		MIMEType string `json:"mimeType,omitempty"`
	}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	b.MIMEType = temp.MIMEType

	if temp.Data != "" {
		// Convert URL-safe base64 to standard base64
		standardBase64 := strings.ReplaceAll(strings.ReplaceAll(temp.Data, "_", "/"), "-", "+")

		// Add padding if necessary
		switch len(standardBase64) % 4 {
		case 2:
			standardBase64 += "=="
		case 3:
			standardBase64 += "="
		}

		decoded, err := base64.StdEncoding.DecodeString(standardBase64)
		if err != nil {
			return fmt.Errorf("failed to decode base64 data: %v", err)
		}
		b.Data = decoded
	}

	return nil
}

// CustomPart handles Google GenAI Part with custom Blob unmarshalling
type CustomPart struct {
	VideoMetadata       *genai_sdk.VideoMetadata       `json:"videoMetadata,omitempty"`
	Thought             bool                           `json:"thought,omitempty"`
	CodeExecutionResult *genai_sdk.CodeExecutionResult `json:"codeExecutionResult,omitempty"`
	ExecutableCode      *genai_sdk.ExecutableCode      `json:"executableCode,omitempty"`
	FileData            *genai_sdk.FileData            `json:"fileData,omitempty"`
	FunctionCall        *genai_sdk.FunctionCall        `json:"functionCall,omitempty"`
	FunctionResponse    *genai_sdk.FunctionResponse    `json:"functionResponse,omitempty"`
	InlineData          *CustomBlob                    `json:"inlineData,omitempty"`
	Text                string                         `json:"text,omitempty"`
}

// ToGenAIPart converts CustomPart to genai_sdk.Part
func (p *CustomPart) ToGenAIPart() *genai_sdk.Part {
	part := &genai_sdk.Part{
		VideoMetadata:       p.VideoMetadata,
		Thought:             p.Thought,
		CodeExecutionResult: p.CodeExecutionResult,
		ExecutableCode:      p.ExecutableCode,
		FileData:            p.FileData,
		FunctionCall:        p.FunctionCall,
		FunctionResponse:    p.FunctionResponse,
		Text:                p.Text,
	}

	if p.InlineData != nil {
		part.InlineData = &genai_sdk.Blob{
			Data:     p.InlineData.Data,
			MIMEType: p.InlineData.MIMEType,
		}
	}

	return part
}

// CustomContent handles Google GenAI Content with custom Part unmarshalling
type CustomContent struct {
	Parts []*CustomPart `json:"parts,omitempty"`
	Role  string        `json:"role,omitempty"`
}

// ToGenAIContent converts CustomContent to genai_sdk.Content
func (c *CustomContent) ToGenAIContent() genai_sdk.Content {
	parts := make([]*genai_sdk.Part, len(c.Parts))
	for i, part := range c.Parts {
		parts[i] = part.ToGenAIPart()
	}

	return genai_sdk.Content{
		Parts: parts,
		Role:  c.Role,
	}
}

// ensureExtraParams ensures that bifrostReq.Params and bifrostReq.Params.ExtraParams are initialized
func ensureExtraParams(bifrostReq *schemas.BifrostRequest) {
	if bifrostReq.Params == nil {
		bifrostReq.Params = &schemas.ModelParameters{
			ExtraParams: make(map[string]interface{}),
		}
	}
	if bifrostReq.Params.ExtraParams == nil {
		bifrostReq.Params.ExtraParams = make(map[string]interface{})
	}
}

type GeminiChatRequest struct {
	Model              string                     `json:"model,omitempty"` // Model field for explicit model specification
	Contents           []CustomContent            `json:"contents"`
	SystemInstruction  *CustomContent             `json:"systemInstruction,omitempty"`
	GenerationConfig   genai_sdk.GenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings     []genai_sdk.SafetySetting  `json:"safetySettings,omitempty"`
	Tools              []genai_sdk.Tool           `json:"tools,omitempty"`
	ToolConfig         genai_sdk.ToolConfig       `json:"toolConfig,omitempty"`
	Labels             map[string]string          `json:"labels,omitempty"`
	CachedContent      string                     `json:"cachedContent,omitempty"`
	ResponseModalities []string                   `json:"responseModalities,omitempty"`
	Stream             bool                       `json:"-"` // Internal field to track streaming requests
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *GeminiChatRequest) IsStreamingRequested() bool {
	return r.Stream
}

// GeminiChatRequestError represents a Gemini chat completion error response
type GeminiChatRequestError struct {
	Error GeminiChatRequestErrorStruct `json:"error"` // Error details following Google API format
}

// GeminiChatRequestErrorStruct represents the error structure of a Gemini chat completion error response
type GeminiChatRequestErrorStruct struct {
	Code    int    `json:"code"`    // HTTP status code
	Message string `json:"message"` // Error message
	Status  string `json:"status"`  // Error status string (e.g., "INVALID_REQUEST")
}

func (r *GeminiChatRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	provider, model := integrations.ParseModelString(r.Model, schemas.Vertex)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{},
		},
	}

	messages := []schemas.BifrostMessage{}

	allGenAiMessages := []genai_sdk.Content{}
	if r.SystemInstruction != nil {
		allGenAiMessages = append(allGenAiMessages, r.SystemInstruction.ToGenAIContent())
	}
	for _, content := range r.Contents {
		allGenAiMessages = append(allGenAiMessages, content.ToGenAIContent())
	}

	for _, content := range allGenAiMessages {
		if len(content.Parts) == 0 {
			continue
		}

		// Handle multiple parts - collect all content and tool calls
		var toolCalls []schemas.ToolCall
		var contentBlocks []schemas.ContentBlock
		var thoughtStr string // Track thought content for assistant/model

		for _, part := range content.Parts {
			switch {
			case part.Text != "":
				// Handle thought content specially for assistant messages
				if part.Thought &&
					(content.Role == string(schemas.ModelChatMessageRoleAssistant) || content.Role == string(genai_sdk.RoleModel)) {
					thoughtStr = thoughtStr + part.Text + "\n"
				} else {
					contentBlocks = append(contentBlocks, schemas.ContentBlock{
						Type: schemas.ContentBlockTypeText,
						Text: &part.Text,
					})
				}

			case part.FunctionCall != nil:
				// Only add function calls for assistant messages
				if content.Role == string(schemas.ModelChatMessageRoleAssistant) || content.Role == string(genai_sdk.RoleModel) {
					jsonArgs, err := json.Marshal(part.FunctionCall.Args)
					if err != nil {
						jsonArgs = []byte(fmt.Sprintf("%v", part.FunctionCall.Args))
					}
					id := part.FunctionCall.ID     // create local copy
					name := part.FunctionCall.Name // create local copy
					toolCall := schemas.ToolCall{
						ID:   bifrost.Ptr(id),
						Type: fnTypePtr,
						Function: schemas.FunctionCall{
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

				toolResponseMsg := schemas.BifrostMessage{
					Role: schemas.ModelChatMessageRoleTool,
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr(string(responseContent)),
					},
					ToolMessage: &schemas.ToolMessage{
						ToolCallID: &part.FunctionResponse.Name,
					},
				}

				messages = append(messages, toolResponseMsg)

			case part.InlineData != nil:
				// Handle inline images/media - only append if it's actually an image
				if isImageMimeType(part.InlineData.MIMEType) {
					contentBlocks = append(contentBlocks, schemas.ContentBlock{
						Type: schemas.ContentBlockTypeImage,
						ImageURL: &schemas.ImageURLStruct{
							URL: fmt.Sprintf("data:%s;base64,%s", part.InlineData.MIMEType, base64.StdEncoding.EncodeToString(part.InlineData.Data)),
						},
					})
				}

			case part.FileData != nil:
				// Handle file data - only append if it's actually an image
				if isImageMimeType(part.FileData.MIMEType) {
					contentBlocks = append(contentBlocks, schemas.ContentBlock{
						Type: schemas.ContentBlockTypeImage,
						ImageURL: &schemas.ImageURLStruct{
							URL: part.FileData.FileURI,
						},
					})
				}

			case part.ExecutableCode != nil:
				// Handle executable code as text content
				codeText := fmt.Sprintf("```%s\n%s\n```", part.ExecutableCode.Language, part.ExecutableCode.Code)
				contentBlocks = append(contentBlocks, schemas.ContentBlock{
					Type: schemas.ContentBlockTypeText,
					Text: &codeText,
				})

			case part.CodeExecutionResult != nil:
				// Handle code execution results as text content
				resultText := fmt.Sprintf("Code execution result (%s):\n%s", part.CodeExecutionResult.Outcome, part.CodeExecutionResult.Output)
				contentBlocks = append(contentBlocks, schemas.ContentBlock{
					Type: schemas.ContentBlockTypeText,
					Text: &resultText,
				})
			}
		}

		// Only create message if there's actual content, tool calls, or thought content
		if len(contentBlocks) > 0 || len(toolCalls) > 0 || thoughtStr != "" {
			// Create main message with content blocks
			bifrostMsg := schemas.BifrostMessage{
				Role: func(r string) schemas.ModelChatMessageRole {
					if r == string(genai_sdk.RoleModel) { // GenAI's internal alias
						return schemas.ModelChatMessageRoleAssistant
					}
					return schemas.ModelChatMessageRole(r)
				}(content.Role),
			}

			// Set content only if there are content blocks
			if len(contentBlocks) > 0 {
				bifrostMsg.Content = schemas.MessageContent{
					ContentBlocks: &contentBlocks,
				}
			}

			// Set assistant-specific fields for assistant/model messages
			if content.Role == string(schemas.ModelChatMessageRoleAssistant) || content.Role == string(genai_sdk.RoleModel) {
				if len(toolCalls) > 0 || thoughtStr != "" {
					bifrostMsg.AssistantMessage = &schemas.AssistantMessage{}
					if len(toolCalls) > 0 {
						bifrostMsg.AssistantMessage.ToolCalls = &toolCalls
					}
					if thoughtStr != "" {
						bifrostMsg.AssistantMessage.Thought = &thoughtStr
					}
				}
			}

			messages = append(messages, bifrostMsg)
		}
	}

	bifrostReq.Input.ChatCompletionInput = &messages

	// Convert generation config to parameters
	if params := r.convertGenerationConfigToParams(); params != nil {
		bifrostReq.Params = params
	}

	// Convert safety settings
	if len(r.SafetySettings) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["safety_settings"] = r.SafetySettings
	}

	// Convert additional request fields
	if r.CachedContent != "" {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["cached_content"] = r.CachedContent
	}

	// Convert response modalities
	if len(r.ResponseModalities) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["response_modalities"] = r.ResponseModalities
	}

	// Convert labels
	if len(r.Labels) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["labels"] = r.Labels
	}

	// Convert tools and tool config
	if len(r.Tools) > 0 {
		ensureExtraParams(bifrostReq)

		tools := make([]schemas.Tool, 0, len(r.Tools))
		for _, tool := range r.Tools {
			if len(tool.FunctionDeclarations) > 0 {
				for _, fn := range tool.FunctionDeclarations {
					bifrostTool := schemas.Tool{
						Type: "function",
						Function: schemas.Function{
							Name:        fn.Name,
							Description: fn.Description,
						},
					}
					// Convert parameters schema if present
					if fn.Parameters != nil {
						bifrostTool.Function.Parameters = r.convertSchemaToFunctionParameters(fn.Parameters)
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
			bifrostReq.Params.Tools = &tools
		}
	}

	// Convert tool config
	if r.ToolConfig.FunctionCallingConfig != nil || r.ToolConfig.RetrievalConfig != nil {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["tool_config"] = r.ToolConfig
	}

	return bifrostReq
}

// convertGenerationConfigToParams converts Gemini GenerationConfig to ModelParameters
func (r *GeminiChatRequest) convertGenerationConfigToParams() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	config := r.GenerationConfig

	// Map generation config fields to parameters
	if config.Temperature != nil {
		temp := float64(*config.Temperature)
		params.Temperature = &temp
	}
	if config.TopP != nil {
		params.TopP = bifrost.Ptr(float64(*config.TopP))
	}
	if config.TopK != nil {
		params.TopK = bifrost.Ptr(int(*config.TopK))
	}
	if config.MaxOutputTokens > 0 {
		maxTokens := int(config.MaxOutputTokens)
		params.MaxTokens = &maxTokens
	}
	if config.CandidateCount > 0 {
		params.ExtraParams["candidate_count"] = config.CandidateCount
	}
	if len(config.StopSequences) > 0 {
		params.StopSequences = &config.StopSequences
	}
	if config.PresencePenalty != nil {
		params.PresencePenalty = bifrost.Ptr(float64(*config.PresencePenalty))
	}
	if config.FrequencyPenalty != nil {
		params.FrequencyPenalty = bifrost.Ptr(float64(*config.FrequencyPenalty))
	}
	if config.Seed != nil {
		params.ExtraParams["seed"] = *config.Seed
	}
	if config.ResponseMIMEType != "" {
		params.ExtraParams["response_mime_type"] = config.ResponseMIMEType
	}
	if config.ResponseLogprobs {
		params.ExtraParams["response_logprobs"] = config.ResponseLogprobs
	}
	if config.Logprobs != nil {
		params.ExtraParams["logprobs"] = *config.Logprobs
	}

	return params
}

// convertSchemaToFunctionParameters converts genai.Schema to schemas.FunctionParameters
func (r *GeminiChatRequest) convertSchemaToFunctionParameters(schema *genai_sdk.Schema) schemas.FunctionParameters {
	params := schemas.FunctionParameters{
		Type: string(schema.Type),
	}

	if schema.Description != "" {
		params.Description = &schema.Description
	}

	if len(schema.Required) > 0 {
		params.Required = schema.Required
	}

	if len(schema.Properties) > 0 {
		params.Properties = make(map[string]interface{})
		for k, v := range schema.Properties {
			params.Properties[k] = v
		}
	}

	if len(schema.Enum) > 0 {
		params.Enum = &schema.Enum
	}

	return params
}

func DeriveGenAIFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *genai_sdk.GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	genaiResp := &genai_sdk.GenerateContentResponse{
		Candidates: make([]*genai_sdk.Candidate, len(bifrostResp.Choices)),
	}

	if bifrostResp.Usage != nil {
		genaiResp.UsageMetadata = &genai_sdk.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(bifrostResp.Usage.PromptTokens),
			CandidatesTokenCount: int32(bifrostResp.Usage.CompletionTokens),
			TotalTokenCount:      int32(bifrostResp.Usage.TotalTokens),
		}
	}

	for i, choice := range bifrostResp.Choices {
		candidate := &genai_sdk.Candidate{
			Index: int32(choice.Index),
		}
		if choice.FinishReason != nil {
			candidate.FinishReason = genai_sdk.FinishReason(*choice.FinishReason)
		}

		if bifrostResp.Usage != nil {
			candidate.TokenCount = int32(bifrostResp.Usage.CompletionTokens)
		}

		parts := []*genai_sdk.Part{}
		if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
			parts = append(parts, &genai_sdk.Part{Text: *choice.Message.Content.ContentStr})
		} else if choice.Message.Content.ContentBlocks != nil {
			for _, block := range *choice.Message.Content.ContentBlocks {
				if block.Text != nil {
					parts = append(parts, &genai_sdk.Part{Text: *block.Text})
				}
			}
		}

		// Handle tool calls
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			for _, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				argsMap := make(map[string]interface{})
				if toolCall.Function.Arguments != "" {
					// Attempt to unmarshal arguments, but don't fail if it's not valid JSON,
					// as BifrostResponse.FunctionCall.Arguments is a string.
					// genai.FunctionCall.Args expects map[string]any.
					json.Unmarshal([]byte(toolCall.Function.Arguments), &argsMap)
				}
				if toolCall.Function.Name != nil {
					fc := &genai_sdk.FunctionCall{
						Name: *toolCall.Function.Name,
						Args: argsMap,
					}
					if toolCall.ID != nil {
						fc.ID = *toolCall.ID
					}
					parts = append(parts, &genai_sdk.Part{FunctionCall: fc})
				}
			}
		}

		// Handle thinking content if present
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.Thought != nil && *choice.Message.AssistantMessage.Thought != "" {
			parts = append(parts, &genai_sdk.Part{
				Text:    *choice.Message.AssistantMessage.Thought,
				Thought: true,
			})
		}

		if len(parts) > 0 {
			candidate.Content = &genai_sdk.Content{
				Parts: parts,
				Role:  string(choice.Message.Role),
			}
		}

		// Handle safety ratings if available (from ExtraFields)
		if bifrostResp.ExtraFields.RawResponse != nil {
			if rawMap, ok := bifrostResp.ExtraFields.RawResponse.(map[string]interface{}); ok {
				if candidates, ok := rawMap["candidates"].([]interface{}); ok && len(candidates) > i {
					if candidateMap, ok := candidates[i].(map[string]interface{}); ok {
						if safetyRatings, ok := candidateMap["safetyRatings"].([]interface{}); ok {
							var ratings []*genai_sdk.SafetyRating
							for _, rating := range safetyRatings {
								if ratingMap, ok := rating.(map[string]interface{}); ok {
									sr := &genai_sdk.SafetyRating{}
									if category, ok := ratingMap["category"].(string); ok {
										sr.Category = genai_sdk.HarmCategory(category)
									}
									if probability, ok := ratingMap["probability"].(string); ok {
										sr.Probability = genai_sdk.HarmProbability(probability)
									}
									if blocked, ok := ratingMap["blocked"].(bool); ok {
										sr.Blocked = blocked
									}
									ratings = append(ratings, sr)
								}
							}
							candidate.SafetyRatings = ratings
						}
					}
				}
			}
		}

		genaiResp.Candidates[i] = candidate
	}

	return genaiResp
}

// DeriveGeminiStreamFromBifrostResponse converts a Bifrost streaming response to Google GenAI streaming format
func DeriveGeminiStreamFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *genai_sdk.GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	genaiResp := &genai_sdk.GenerateContentResponse{
		Candidates: make([]*genai_sdk.Candidate, len(bifrostResp.Choices)),
	}

	// Set usage metadata if available
	if bifrostResp.Usage != nil {
		genaiResp.UsageMetadata = &genai_sdk.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(bifrostResp.Usage.PromptTokens),
			CandidatesTokenCount: int32(bifrostResp.Usage.CompletionTokens),
			TotalTokenCount:      int32(bifrostResp.Usage.TotalTokens),
		}
	}

	// Convert choices to streaming format
	for i, choice := range bifrostResp.Choices {
		candidate := &genai_sdk.Candidate{
			Index: int32(choice.Index),
		}

		// Set finish reason if present
		if choice.FinishReason != nil {
			candidate.FinishReason = genai_sdk.FinishReason(*choice.FinishReason)
		}

		// Set token count if available
		if bifrostResp.Usage != nil {
			candidate.TokenCount = int32(bifrostResp.Usage.CompletionTokens)
		}

		// Handle streaming response delta
		var parts []*genai_sdk.Part

		if choice.BifrostStreamResponseChoice != nil {
			// Convert streaming delta to parts
			delta := choice.BifrostStreamResponseChoice.Delta

			// Handle text content delta
			if delta.Content != nil && *delta.Content != "" {
				parts = append(parts, &genai_sdk.Part{
					Text: *delta.Content,
				})
			}

			// Handle thinking content delta
			if delta.Thought != nil && *delta.Thought != "" {
				parts = append(parts, &genai_sdk.Part{
					Text:    *delta.Thought,
					Thought: true,
				})
			}

			// Handle tool call deltas
			if len(delta.ToolCalls) > 0 {
				for _, toolCall := range delta.ToolCalls {
					if toolCall.Function.Name != nil && *toolCall.Function.Name != "" {
						// Convert tool call arguments from JSON string to map
						argsMap := make(map[string]interface{})
						if toolCall.Function.Arguments != "" {
							json.Unmarshal([]byte(toolCall.Function.Arguments), &argsMap)
						}

						fc := &genai_sdk.FunctionCall{
							Name: *toolCall.Function.Name,
							Args: argsMap,
						}
						if toolCall.ID != nil {
							fc.ID = *toolCall.ID
						}

						parts = append(parts, &genai_sdk.Part{
							FunctionCall: fc,
						})
					}
				}
			}

		}

		// Set content if we have parts
		if len(parts) > 0 {
			candidate.Content = &genai_sdk.Content{
				Parts: parts,
				Role:  string(schemas.ModelChatMessageRoleAssistant), // Streaming responses are typically from assistant
			}
		}

		genaiResp.Candidates[i] = candidate
	}

	// Set response metadata
	if bifrostResp.ID != "" {
		genaiResp.ResponseID = bifrostResp.ID
	}
	if bifrostResp.Model != "" {
		genaiResp.ModelVersion = bifrostResp.Model
	}

	return genaiResp
}

// DeriveGeminiErrorFromBifrostError derives a GeminiChatRequestError from a BifrostError
func DeriveGeminiErrorFromBifrostError(bifrostErr *schemas.BifrostError) *GeminiChatRequestError {
	if bifrostErr == nil {
		return nil
	}

	code := 500
	status := ""

	if bifrostErr.Error.Type != nil {
		status = *bifrostErr.Error.Type
	}

	if bifrostErr.StatusCode != nil {
		code = *bifrostErr.StatusCode
	}

	return &GeminiChatRequestError{
		Error: GeminiChatRequestErrorStruct{
			Code:    code,
			Message: bifrostErr.Error.Message,
			Status:  status,
		},
	}
}

// DeriveGeminiStreamFromBifrostError derives a Gemini streaming error from a BifrostError
func DeriveGeminiStreamFromBifrostError(bifrostErr *schemas.BifrostError) *GeminiChatRequestError {
	// For streaming, we use the same error format as regular Gemini errors
	return DeriveGeminiErrorFromBifrostError(bifrostErr)
}

// isImageMimeType checks if a MIME type represents an image format
func isImageMimeType(mimeType string) bool {
	if mimeType == "" {
		return false
	}

	// Convert to lowercase for case-insensitive comparison
	mimeType = strings.ToLower(mimeType)

	// Remove any parameters (e.g., "image/jpeg; charset=utf-8" -> "image/jpeg")
	if idx := strings.Index(mimeType, ";"); idx != -1 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}

	// If it starts with "image/", it's an image
	if strings.HasPrefix(mimeType, "image/") {
		return true
	}

	// Check for common image formats that might not have the "image/" prefix
	commonImageTypes := []string{
		"jpeg",
		"jpg",
		"png",
		"gif",
		"webp",
		"bmp",
		"svg",
		"tiff",
		"ico",
		"avif",
	}

	// Check if the mimeType contains any of the common image type strings
	for _, imageType := range commonImageTypes {
		if strings.Contains(mimeType, imageType) {
			return true
		}
	}

	return false
}
