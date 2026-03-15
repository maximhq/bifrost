package sapaicore

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// Response pool for Vertex (Gemini) response objects.
// We reuse the canonical response type from the gemini package.
var vertexResponsePool = sync.Pool{
	New: func() interface{} {
		return &gemini.GenerateContentResponse{}
	},
}

// acquireVertexResponse gets a GenerateContentResponse from the pool and resets it.
func acquireVertexResponse() *gemini.GenerateContentResponse {
	resp := vertexResponsePool.Get().(*gemini.GenerateContentResponse)
	*resp = gemini.GenerateContentResponse{} // Reset the struct
	return resp
}

// releaseVertexResponse returns a GenerateContentResponse to the pool.
func releaseVertexResponse(resp *gemini.GenerateContentResponse) {
	if resp != nil {
		vertexResponsePool.Put(resp)
	}
}

// extractMediaType extracts the media type from a base64 data URL or returns a default.
// Handles formats like "data:image/png;base64,..." or plain base64 data.
func extractMediaType(url string) string {
	if strings.HasPrefix(url, "data:") {
		// Extract media type from data URL: data:image/png;base64,...
		if idx := strings.Index(url, ";"); idx > 5 {
			return url[5:idx] // Skip "data:" prefix
		}
		if idx := strings.Index(url, ","); idx > 5 {
			return url[5:idx]
		}
	}
	// Default to JPEG for unknown formats
	return "image/jpeg"
}

// vertexGenerationConfig is a simplified GenerationConfig for SAP AI Core's Vertex path.
// We keep this local because the upstream gemini.GenerationConfig has many more fields
// (30+ fields) that are not needed for the SAP AI Core case, and its MaxOutputTokens is
// int32 while SAP AI Core uses *int.
type vertexGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// vertexRequest is a simplified request type for SAP AI Core's Vertex path.
// We keep this local because gemini.GeminiGenerationRequest has many extra fields
// (embedding, transcription, speech, image flags) that produce different serialization.
type vertexRequest struct {
	Contents          []gemini.Content        `json:"contents"`
	SystemInstruction *gemini.Content         `json:"systemInstruction,omitempty"`
	GenerationConfig  *vertexGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []gemini.Tool           `json:"tools,omitempty"`
	ToolConfig        *gemini.ToolConfig      `json:"toolConfig,omitempty"`
}

// mapRoleForAnthropic maps message roles to Anthropic-compatible roles.
// Anthropic only supports "user" and "assistant" roles. The "developer" role
// (used by some clients like Codex) is mapped to "user" since developer
// instructions are similar to user instructions.
func mapRoleForAnthropic(role schemas.ChatMessageRole) string {
	switch role {
	case "developer":
		return "user"
	default:
		return string(role)
	}
}

// mapResponsesRoleForAnthropic maps Responses API message roles to Anthropic-compatible roles.
// Anthropic only supports "user" and "assistant" roles. The "developer" role
// (used by some clients like Codex) is mapped to "user" since developer
// instructions are similar to user instructions.
func mapResponsesRoleForAnthropic(role schemas.ResponsesMessageRoleType) string {
	switch role {
	case schemas.ResponsesInputMessageRoleDeveloper:
		return "user"
	default:
		return string(role)
	}
}

// resolveSchemaRefs resolves $ref references in a JSON schema by inlining the referenced definitions.
// This is needed because Vertex/Gemini doesn't support $ref alongside other fields.
// It also removes "ref" fields (without $) that may be left over from pre-processing.
// The function preserves key ordering by using OrderedMap throughout, which is critical
// for provider-side prompt caching (different key ordering = different cache keys).
func resolveSchemaRefs(params *schemas.ToolFunctionParameters) any {
	if params == nil {
		return nil
	}

	// Convert to OrderedMap to preserve key ordering from the original JSON.
	// ToolFunctionParameters.MarshalJSON preserves order, and OrderedMap.UnmarshalJSON
	// captures it, so the round-trip maintains the original field order.
	paramsBytes, err := sonic.Marshal(params)
	if err != nil {
		return params
	}

	schemaMap := schemas.NewOrderedMap()
	if err := schemaMap.UnmarshalJSON(paramsBytes); err != nil {
		return params
	}

	// Extract definitions ($defs or definitions).
	// The defs lookup map uses plain map[string]any for key-based lookups;
	// the values inside are *OrderedMap (created by OrderedMap.UnmarshalJSON).
	defs := make(map[string]any)
	if d, ok := extractOrderedMapField(schemaMap, "$defs"); ok {
		d.Range(func(key string, value interface{}) bool {
			defs[key] = value
			return true
		})
	}
	if d, ok := extractOrderedMapField(schemaMap, "definitions"); ok {
		d.Range(func(key string, value interface{}) bool {
			defs[key] = value
			return true
		})
	}

	// Recursively resolve all $ref references and remove "ref" fields.
	// Even if no $defs/definitions exist, there might be "ref" fields to remove.
	resolved := resolveRefsInValue(schemaMap, defs)

	// Remove $defs and definitions from the result (they're now inlined)
	if resolvedMap, ok := resolved.(*schemas.OrderedMap); ok {
		resolvedMap.Delete("$defs")
		resolvedMap.Delete("definitions")
		return resolvedMap
	}

	return resolved
}

// extractOrderedMapField gets a field from an OrderedMap and asserts it's an *OrderedMap.
func extractOrderedMapField(om *schemas.OrderedMap, key string) (*schemas.OrderedMap, bool) {
	val, exists := om.Get(key)
	if !exists {
		return nil, false
	}
	nested, ok := val.(*schemas.OrderedMap)
	return nested, ok
}

// resolveRefsInValue recursively resolves $ref references and removes "ref" fields in a value.
// It handles *OrderedMap (from OrderedMap.UnmarshalJSON) to preserve key ordering.
func resolveRefsInValue(value any, defs map[string]any) any {
	switch v := value.(type) {
	case *schemas.OrderedMap:
		if v == nil {
			return nil
		}
		// Check if this object has a $ref (standard JSON Schema reference)
		if refVal, ok := v.Get("$ref"); ok {
			if ref, ok := refVal.(string); ok {
				refName := extractRefName(ref)
				if refName != "" {
					if def, ok := defs[refName]; ok {
						// Clone the definition and resolve any nested refs in it
						defCopy := deepCopyValue(def)
						resolved := resolveRefsInValue(defCopy, defs)

						// If the original object had other fields besides $ref, merge them.
						// According to JSON Schema, if $ref is present, other fields should be ignored,
						// but we merge non-$ref fields for compatibility.
						if resolvedMap, ok := resolved.(*schemas.OrderedMap); ok {
							v.Range(func(k string, val interface{}) bool {
								if k != "$ref" {
									if _, exists := resolvedMap.Get(k); !exists {
										resolvedMap.Set(k, resolveRefsInValue(val, defs))
									}
								}
								return true
							})
							return resolvedMap
						}
						return resolved
					}
				}
			}
		}

		// No $ref or couldn't resolve, process all fields.
		// Also remove "ref" fields (non-standard, without $) as Vertex doesn't support them.
		result := schemas.NewOrderedMapWithCapacity(v.Len())
		v.Range(func(k string, val interface{}) bool {
			// Skip "ref" field - this is a non-standard field that Vertex rejects
			// when it appears alongside other schema fields like "type", "properties", etc.
			if k == "ref" {
				return true
			}
			result.Set(k, resolveRefsInValue(val, defs))
			return true
		})
		return result

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = resolveRefsInValue(item, defs)
		}
		return result

	default:
		return v
	}
}

// extractRefName extracts the definition name from a $ref string
// e.g., "#/$defs/QuestionOption" -> "QuestionOption"
// e.g., "#/definitions/QuestionOption" -> "QuestionOption"
func extractRefName(ref string) string {
	// Handle #/$defs/Name format
	if strings.HasPrefix(ref, "#/$defs/") {
		return strings.TrimPrefix(ref, "#/$defs/")
	}
	// Handle #/definitions/Name format
	if strings.HasPrefix(ref, "#/definitions/") {
		return strings.TrimPrefix(ref, "#/definitions/")
	}
	return ""
}

// deepCopyValue creates a deep copy of a value, preserving OrderedMap key ordering.
func deepCopyValue(value any) any {
	switch v := value.(type) {
	case *schemas.OrderedMap:
		if v == nil {
			return nil
		}
		result := schemas.NewOrderedMapWithCapacity(v.Len())
		v.Range(func(k string, val interface{}) bool {
			result.Set(k, deepCopyValue(val))
			return true
		})
		return result
	case map[string]any:
		result := make(map[string]any, len(v))
		for k, val := range v {
			result[k] = deepCopyValue(val)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = deepCopyValue(item)
		}
		return result
	default:
		return v
	}
}

// convertToVertex converts a Bifrost chat request to Vertex AI format
func convertToVertex(request *schemas.BifrostChatRequest) *vertexRequest {
	vertexReq := &vertexRequest{}

	// Build a map from tool call ID to function name for correlating tool responses
	callIDToFunctionName := make(map[string]string)
	for _, msg := range request.Input {
		if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
			for _, tc := range msg.ChatAssistantMessage.ToolCalls {
				if tc.ID != nil && tc.Function.Name != nil {
					callIDToFunctionName[*tc.ID] = *tc.Function.Name
				}
			}
		}
	}

	// Track pending tool response parts for grouping consecutive tool messages
	var pendingToolResponseParts []*gemini.Part

	// Convert messages from Input field
	for i, msg := range request.Input {
		if msg.Role == schemas.ChatMessageRoleSystem {
			// Handle system message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				vertexReq.SystemInstruction = &gemini.Content{
					Parts: []*gemini.Part{{Text: *msg.Content.ContentStr}},
				}
			}
			continue
		}

		// Check if this is a tool response message
		isToolResponse := msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil

		// If we have pending tool responses and current message is NOT a tool response,
		// flush the pending tool responses as a single Content
		if len(pendingToolResponseParts) > 0 && !isToolResponse {
			vertexReq.Contents = append(vertexReq.Contents, gemini.Content{
				Role:  "user", // Tool responses use "user" role in Vertex
				Parts: pendingToolResponseParts,
			})
			pendingToolResponseParts = nil
		}

		// Handle tool response messages - collect them for grouping
		if isToolResponse {
			var functionName string
			if msg.ChatToolMessage.ToolCallID != nil {
				if name, ok := callIDToFunctionName[*msg.ChatToolMessage.ToolCallID]; ok {
					functionName = name
				}
			}

			// Parse the response content
			var responseData map[string]any
			var contentStr string

			if msg.Content != nil {
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					contentStr = *msg.Content.ContentStr
				} else if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							contentStr += *block.Text
						}
					}
				}
			}

			// Try to parse as JSON, otherwise wrap in output key
			if contentStr != "" {
				if err := sonic.Unmarshal([]byte(contentStr), &responseData); err != nil {
					responseData = map[string]any{"output": contentStr}
				}
			} else {
				responseData = map[string]any{"output": ""}
			}

			pendingToolResponseParts = append(pendingToolResponseParts, &gemini.Part{
				FunctionResponse: &gemini.FunctionResponse{
					Name:     functionName,
					Response: responseData,
				},
			})

			// Check if this is the last message or next message is not a tool response
			isLastMessage := i == len(request.Input)-1
			var nextIsToolResponse bool
			if !isLastMessage {
				nextMsg := request.Input[i+1]
				nextIsToolResponse = nextMsg.Role == schemas.ChatMessageRoleTool && nextMsg.ChatToolMessage != nil
			}

			// Flush if this is the last message or next is not a tool response
			if isLastMessage || !nextIsToolResponse {
				vertexReq.Contents = append(vertexReq.Contents, gemini.Content{
					Role:  "user", // Tool responses use "user" role in Vertex
					Parts: pendingToolResponseParts,
				})
				pendingToolResponseParts = nil
			}
			continue
		}

		vertexContent := gemini.Content{
			Role: mapToVertexRole(string(msg.Role)),
		}

		// Handle assistant messages with tool calls
		if msg.Role == schemas.ChatMessageRoleAssistant && msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
			// Add text content if present
			if msg.Content != nil {
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					vertexContent.Parts = append(vertexContent.Parts, &gemini.Part{
						Text: *msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil && *block.Text != "" {
							vertexContent.Parts = append(vertexContent.Parts, &gemini.Part{
								Text: *block.Text,
							})
						}
					}
				}
			}

			// Add function calls
			for _, tc := range msg.ChatAssistantMessage.ToolCalls {
				var args map[string]any
				if tc.Function.Arguments != "" {
					_ = sonic.Unmarshal([]byte(tc.Function.Arguments), &args)
				}

				functionCall := &gemini.FunctionCall{
					Args: args,
				}
				if tc.Function.Name != nil {
					functionCall.Name = *tc.Function.Name
				}

				vertexContent.Parts = append(vertexContent.Parts, &gemini.Part{
					FunctionCall: functionCall,
				})
			}

			vertexReq.Contents = append(vertexReq.Contents, vertexContent)
			continue
		}

		// Convert regular content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				vertexContent.Parts = []*gemini.Part{{Text: *msg.Content.ContentStr}}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
						vertexContent.Parts = append(vertexContent.Parts, &gemini.Part{
							Text: *block.Text,
						})
					} else if block.Type == schemas.ChatContentBlockTypeImage && block.ImageURLStruct != nil {
						vertexContent.Parts = append(vertexContent.Parts, &gemini.Part{
							InlineData: &gemini.Blob{
								MIMEType: extractMediaType(block.ImageURLStruct.URL),
								Data:     block.ImageURLStruct.URL,
							},
						})
					}
				}
			}
		}

		vertexReq.Contents = append(vertexReq.Contents, vertexContent)
	}

	// Set generation config
	defaultMaxTokens := DefaultMaxTokens
	vertexReq.GenerationConfig = &vertexGenerationConfig{
		MaxOutputTokens: &defaultMaxTokens,
	}

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			vertexReq.GenerationConfig.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			vertexReq.GenerationConfig.TopP = request.Params.TopP
		}
		if request.Params.MaxCompletionTokens != nil {
			vertexReq.GenerationConfig.MaxOutputTokens = request.Params.MaxCompletionTokens
		}
		if request.Params.Stop != nil {
			vertexReq.GenerationConfig.StopSequences = request.Params.Stop
		}

		// Convert tools to Vertex format
		if len(request.Params.Tools) > 0 {
			var functionDeclarations []*gemini.FunctionDeclaration
			for _, tool := range request.Params.Tools {
				if tool.Type == schemas.ChatToolTypeFunction && tool.Function != nil {
					fd := &gemini.FunctionDeclaration{
						Name: tool.Function.Name,
					}
					if tool.Function.Description != nil {
						fd.Description = *tool.Function.Description
					}
					if tool.Function.Parameters != nil {
						// Resolve $ref references in schema - Vertex doesn't support $ref alongside other fields
						// Use ParametersJSONSchema since we pass a raw any value (not *Schema)
						fd.ParametersJSONSchema = resolveSchemaRefs(tool.Function.Parameters)
					}
					functionDeclarations = append(functionDeclarations, fd)
				}
			}
			if len(functionDeclarations) > 0 {
				vertexReq.Tools = []gemini.Tool{{
					FunctionDeclarations: functionDeclarations,
				}}
			}
		}

		// Convert tool choice to Vertex format
		if request.Params.ToolChoice != nil {
			vertexReq.ToolConfig = convertToolChoiceToVertexConfig(request.Params.ToolChoice)
		}
	}

	return vertexReq
}

// convertToolChoiceToVertexConfig converts OpenAI tool choice to Vertex tool config
func convertToolChoiceToVertexConfig(toolChoice *schemas.ChatToolChoice) *gemini.ToolConfig {
	config := &gemini.ToolConfig{
		FunctionCallingConfig: &gemini.FunctionCallingConfig{},
	}

	if toolChoice.ChatToolChoiceStr != nil {
		switch *toolChoice.ChatToolChoiceStr {
		case "none":
			config.FunctionCallingConfig.Mode = "NONE"
		case "auto":
			config.FunctionCallingConfig.Mode = "AUTO"
		case "any", "required":
			config.FunctionCallingConfig.Mode = "ANY"
		default:
			config.FunctionCallingConfig.Mode = "AUTO"
		}
	} else if toolChoice.ChatToolChoiceStruct != nil {
		switch toolChoice.ChatToolChoiceStruct.Type {
		case schemas.ChatToolChoiceTypeNone:
			config.FunctionCallingConfig.Mode = "NONE"
		case schemas.ChatToolChoiceTypeFunction:
			config.FunctionCallingConfig.Mode = "ANY"
		case schemas.ChatToolChoiceTypeRequired:
			config.FunctionCallingConfig.Mode = "ANY"
		default:
			config.FunctionCallingConfig.Mode = "AUTO"
		}

		// Handle specific function selection
		if toolChoice.ChatToolChoiceStruct.Function.Name != "" {
			config.FunctionCallingConfig.AllowedFunctionNames = []string{toolChoice.ChatToolChoiceStruct.Function.Name}
		}
	}

	return config
}

// mapToVertexRole maps OpenAI roles to Vertex AI roles
func mapToVertexRole(role string) string {
	switch role {
	case "assistant":
		return "model"
	case "user":
		return "user"
	default:
		return role
	}
}

// parseVertexResponse parses a Vertex AI response into Bifrost format.
// Uses object pooling for efficient memory reuse.
func parseVertexResponse(body []byte, model string) (*schemas.BifrostChatResponse, error) {
	vertexResp := acquireVertexResponse()
	defer releaseVertexResponse(vertexResp)

	if err := sonic.Unmarshal(body, vertexResp); err != nil {
		return nil, err
	}

	// Extract content and tool calls from first candidate
	var content string
	var finishReason string
	var toolCalls []schemas.ChatAssistantMessageToolCall

	if len(vertexResp.Candidates) > 0 {
		candidate := vertexResp.Candidates[0]
		if candidate.Content != nil {
			for i, part := range candidate.Content.Parts {
				if part == nil {
					continue
				}
				// Handle text content
				if part.Text != "" {
					content += part.Text
				}
				// Handle function calls
				if part.FunctionCall != nil {
					// Serialize args to JSON string
					argsJSON := "{}"
					if part.FunctionCall.Args != nil {
						if argsBytes, err := sonic.Marshal(part.FunctionCall.Args); err == nil {
							argsJSON = string(argsBytes)
						}
					}

					// Generate a tool call ID
					toolCallID := fmt.Sprintf("call_%s_%d", model, i)
					toolCallType := "function"
					funcName := part.FunctionCall.Name

					toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
						Index: uint16(len(toolCalls)),
						Type:  &toolCallType,
						ID:    &toolCallID,
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      &funcName,
							Arguments: argsJSON,
						},
					})
				}
			}
		}
		finishReason = mapVertexFinishReason(string(candidate.FinishReason))

		// If there are tool calls, set finish reason to tool_calls
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		}
	}

	// Create ChatMessage for the response
	assistantRole := schemas.ChatMessageRoleAssistant
	responseMessage := &schemas.ChatMessage{
		Role: assistantRole,
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
	}

	// Add tool calls to message if present
	if len(toolCalls) > 0 {
		responseMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls: toolCalls,
		}
	}

	response := &schemas.BifrostChatResponse{
		ID:      "",
		Object:  "chat.completion",
		Created: int(time.Now().Unix()),
		Model:   model,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: responseMessage,
				},
				FinishReason: &finishReason,
			},
		},
	}

	if vertexResp.UsageMetadata != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     int(vertexResp.UsageMetadata.PromptTokenCount),
			CompletionTokens: int(vertexResp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:      int(vertexResp.UsageMetadata.TotalTokenCount),
		}
	}

	return response, nil
}

// mapVertexFinishReason maps Vertex AI finish reasons to OpenAI format
func mapVertexFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	default:
		return "stop"
	}
}

// convertResponsesToBedrockConverse converts a Bifrost Responses request to Bedrock Converse API format
// This is required for SAP AI Core to support native tool calling in the Responses API
func convertResponsesToBedrockConverse(request *schemas.BifrostResponsesRequest) *bedrock.BedrockConverseRequest {
	converseReq := &bedrock.BedrockConverseRequest{}

	maxTokens := DefaultMaxTokens

	// Convert messages from Input field
	var systemMessages []bedrock.BedrockSystemMessage
	i := 0
	for i < len(request.Input) {
		msg := request.Input[i]

		// Handle system messages
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemMessages = append(systemMessages, bedrock.BedrockSystemMessage{
						Text: msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					// Extract text from content blocks
					var systemText string
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil {
							systemText += *block.Text
						}
					}
					if systemText != "" {
						systemMessages = append(systemMessages, bedrock.BedrockSystemMessage{
							Text: &systemText,
						})
					}
				}
			}
			i++
			continue
		}

		// Handle function_call_output messages - these are tool results
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			var toolResultBlocks []bedrock.BedrockContentBlock

			// Collect all consecutive function_call_output messages
			for i < len(request.Input) {
				currMsg := request.Input[i]
				if currMsg.Type == nil || *currMsg.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
					break
				}

				// Get tool result content
				toolResultContent := ""
				if currMsg.ResponsesToolMessage != nil && currMsg.ResponsesToolMessage.Output != nil {
					if currMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
						toolResultContent = *currMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
					} else if currMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
						// Extract text from output blocks
						for _, block := range currMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
							if block.Text != nil {
								toolResultContent += *block.Text
							}
						}
					}
				}

				toolUseId := ""
				if currMsg.ResponsesToolMessage != nil && currMsg.ResponsesToolMessage.CallID != nil {
					toolUseId = *currMsg.ResponsesToolMessage.CallID
				}

				toolResultBlocks = append(toolResultBlocks, bedrock.BedrockContentBlock{
					ToolResult: &bedrock.BedrockToolResult{
						ToolUseID: toolUseId,
						Content: []bedrock.BedrockContentBlock{
							{Text: &toolResultContent},
						},
					},
				})
				i++
			}

			// Create a single user message with all tool results
			if len(toolResultBlocks) > 0 {
				converseMsg := bedrock.BedrockMessage{
					Role:    "user", // Bedrock Converse uses user role for tool results
					Content: toolResultBlocks,
				}
				converseReq.Messages = append(converseReq.Messages, converseMsg)
			}
			continue
		}

		// Handle function_call messages (assistant tool calls in history)
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall {
			if msg.ResponsesToolMessage != nil {
				// Convert arguments string to any (parse JSON)
				var inputArgs interface{}
				if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
					if err := sonic.UnmarshalString(*msg.ResponsesToolMessage.Arguments, &inputArgs); err != nil {
						inputArgs = map[string]interface{}{}
					}
				} else {
					inputArgs = map[string]interface{}{}
				}

				toolUseId := ""
				if msg.ResponsesToolMessage.CallID != nil {
					toolUseId = *msg.ResponsesToolMessage.CallID
				}
				toolName := ""
				if msg.ResponsesToolMessage.Name != nil {
					toolName = *msg.ResponsesToolMessage.Name
				}

				converseMsg := bedrock.BedrockMessage{
					Role: "assistant",
					Content: []bedrock.BedrockContentBlock{
						{
							ToolUse: &bedrock.BedrockToolUse{
								ToolUseID: toolUseId,
								Name:      toolName,
								Input:     inputArgs,
							},
						},
					},
				}
				converseReq.Messages = append(converseReq.Messages, converseMsg)
			}
			i++
			continue
		}

		// Skip messages without role
		if msg.Role == nil {
			i++
			continue
		}

		converseMsg := bedrock.BedrockMessage{
			Role: bedrock.BedrockMessageRole(mapResponsesRoleForAnthropic(*msg.Role)),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
				converseMsg.Content = []bedrock.BedrockContentBlock{
					{Text: msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					switch block.Type {
					case schemas.ResponsesInputMessageContentBlockTypeText,
						schemas.ResponsesOutputMessageContentTypeText:
						if block.Text != nil && *block.Text != "" {
							converseMsg.Content = append(converseMsg.Content, bedrock.BedrockContentBlock{
								Text: block.Text,
							})
						}
					case schemas.ResponsesInputMessageContentBlockTypeImage:
						if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
							mediaType := extractMediaType(*block.ResponsesInputMessageContentBlockImage.ImageURL)
							format := "jpeg"
							if strings.Contains(mediaType, "png") {
								format = "png"
							} else if strings.Contains(mediaType, "gif") {
								format = "gif"
							} else if strings.Contains(mediaType, "webp") {
								format = "webp"
							}
							converseMsg.Content = append(converseMsg.Content, bedrock.BedrockContentBlock{
								Image: &bedrock.BedrockImageSource{
									Format: format,
									Source: bedrock.BedrockImageSourceData{
										Bytes: schemas.Ptr(extractBase64Data(*block.ResponsesInputMessageContentBlockImage.ImageURL)),
									},
								},
							})
						}
					}
				}
			}
		}

		if len(converseMsg.Content) > 0 {
			converseReq.Messages = append(converseReq.Messages, converseMsg)
		}
		i++
	}

	converseReq.System = systemMessages

	// Set inference config
	converseReq.InferenceConfig = &bedrock.BedrockInferenceConfig{
		MaxTokens: &maxTokens,
	}

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			converseReq.InferenceConfig.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			converseReq.InferenceConfig.TopP = request.Params.TopP
		}
		if request.Params.MaxOutputTokens != nil {
			converseReq.InferenceConfig.MaxTokens = request.Params.MaxOutputTokens
		}

		// Convert tools
		if request.Params.Tools != nil {
			tools := make([]bedrock.BedrockTool, 0, len(request.Params.Tools))
			for _, tool := range request.Params.Tools {
				// Only handle function tools for now
				if tool.Type != schemas.ResponsesToolTypeFunction {
					continue
				}

				var inputSchema bedrock.BedrockToolInputSchema
				if tool.ResponsesToolFunction != nil && tool.ResponsesToolFunction.Parameters != nil {
					inputSchema = bedrock.BedrockToolInputSchema{
						JSON: resolveSchemaRefs(tool.ResponsesToolFunction.Parameters),
					}
				}

				converseTool := bedrock.BedrockTool{
					ToolSpec: &bedrock.BedrockToolSpec{
						Name:        "",
						Description: tool.Description,
						InputSchema: inputSchema,
					},
				}
				if tool.Name != nil {
					converseTool.ToolSpec.Name = *tool.Name
				}
				tools = append(tools, converseTool)
			}
			if len(tools) > 0 {
				converseReq.ToolConfig = &bedrock.BedrockToolConfig{
					Tools: tools,
				}
			}
		}

		// Convert tool choice
		if request.Params.ToolChoice != nil && converseReq.ToolConfig != nil {
			if request.Params.ToolChoice.ResponsesToolChoiceStr != nil {
				choice := *request.Params.ToolChoice.ResponsesToolChoiceStr
				switch choice {
				case "auto":
					converseReq.ToolConfig.ToolChoice = &bedrock.BedrockToolChoice{Auto: &bedrock.BedrockToolChoiceAuto{}}
				case "required":
					converseReq.ToolConfig.ToolChoice = &bedrock.BedrockToolChoice{Any: &bedrock.BedrockToolChoiceAny{}}
					// "none" is not directly supported by Converse API, omit it
				}
			} else if request.Params.ToolChoice.ResponsesToolChoiceStruct != nil {
				// Specific tool choice
				if request.Params.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
					converseReq.ToolConfig.ToolChoice = &bedrock.BedrockToolChoice{
						Tool: &bedrock.BedrockToolChoiceTool{
							Name: *request.Params.ToolChoice.ResponsesToolChoiceStruct.Name,
						},
					}
				}
			}
		}
	}

	// Ensure toolConfig is present when conversation history contains tool blocks
	ensureToolConfigForHistory(converseReq)

	return converseReq
}

// extractBase64Data extracts the base64 data from a data URL or returns the URL as-is if it's already base64
func extractBase64Data(url string) string {
	if strings.HasPrefix(url, "data:") {
		// Extract base64 data from data URL: data:image/png;base64,...
		if idx := strings.Index(url, ","); idx > 0 {
			return url[idx+1:]
		}
	}
	// Return as-is (assume it's already base64 encoded)
	return url
}

// converseResponsesStreamState tracks the state for streaming Responses API via Converse API
type converseResponsesStreamState struct {
	MessageID      string
	CreatedAt      int
	SequenceNumber int

	// Text output state
	TextItemID       string
	TextItemAdded    bool
	TextContentAdded bool
	AccumulatedText  string

	// Tool call state
	CurrentToolCallIndex int
	ToolCalls            []converseToolCallState
}

// converseToolCallState tracks state for a single tool call being streamed
type converseToolCallState struct {
	ItemID           string
	ToolUseID        string
	ToolName         string
	AccumulatedArgs  string
	ItemAdded        bool
	ContentPartAdded bool
}

// newconverseResponsesStreamState creates a new stream state for Responses API via Converse
func newconverseResponsesStreamState() *converseResponsesStreamState {
	return &converseResponsesStreamState{
		MessageID:            fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		CreatedAt:            int(time.Now().Unix()),
		CurrentToolCallIndex: -1,
	}
}

// processBedrockConverseResponsesEventStream processes Bedrock Converse API event stream and sends chunks to the channel
// This handles the Converse stream format which has native tool calling support and converts to Responses API format
func processBedrockConverseResponsesEventStream(
	ctx *schemas.BifrostContext,
	bodyStream io.Reader,
	responseChan chan *schemas.BifrostStreamChunk,
	postHookRunner schemas.PostHookRunner,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	state := newconverseResponsesStreamState()
	state.TextItemID = fmt.Sprintf("msg_%s_text_0", state.MessageID)

	usage := &schemas.ResponsesResponseUsage{}
	var stopReason *string
	startTime := time.Now()
	chunkIndex := 0
	hasEmittedCreated := false

	// Helper to send a response
	sendResponse := func(resp *schemas.BifrostResponsesStreamResponse) {
		if resp != nil {
			resp.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ResponsesStreamRequest,
				Provider:       providerName,
				ModelRequested: model,
				ChunkIndex:     chunkIndex,
			}
			chunkIndex++
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, resp, nil, nil, nil), responseChan)
		}
	}

	// Process AWS Event Stream format using proper decoder
	decoder := eventstream.NewDecoder()
	payloadBuf := make([]byte, 0, 1024*1024)

	for {
		if ctx.Err() != nil {
			return
		}

		message, err := decoder.Decode(bodyStream, payloadBuf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == io.EOF {
				break
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error decoding %s Converse EventStream message for Responses API: %v", providerName, err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, model, logger)
			return
		}

		if len(message.Payload) > 0 {
			// Check message type header for errors
			if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
				if msgType := msgTypeHeader.String(); msgType != "event" {
					excType := msgType
					if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
						if v := excHeader.String(); v != "" {
							excType = v
						}
					}
					errMsg := string(message.Payload)
					err := fmt.Errorf("%s Converse stream %s: %s", providerName, excType, errMsg)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, model, logger)
					return
				}
			}

			// Get the event type from headers
			eventType := ""
			if eventTypeHeader := message.Headers.Get(":event-type"); eventTypeHeader != nil {
				eventType = eventTypeHeader.String()
			}

			// Parse the Converse stream event from the payload
			var event bedrock.BedrockStreamEvent
			if err := sonic.Unmarshal(message.Payload, &event); err != nil {
				logger.Debug("Failed to parse Converse stream event for Responses API: %v, data: %s", err, string(message.Payload))
				continue
			}

			// Emit lifecycle events on first real event
			if !hasEmittedCreated {
				sendResponse(&schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeCreated,
					SequenceNumber: state.SequenceNumber,
					Response: &schemas.BifrostResponsesResponse{
						ID:        &state.MessageID,
						Object:    "response",
						CreatedAt: state.CreatedAt,
						Model:     model,
						Status:    schemas.Ptr("in_progress"),
					},
				})
				state.SequenceNumber++
				hasEmittedCreated = true

				sendResponse(&schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeInProgress,
					SequenceNumber: state.SequenceNumber,
					Response: &schemas.BifrostResponsesResponse{
						ID:        &state.MessageID,
						Object:    "response",
						CreatedAt: state.CreatedAt,
						Model:     model,
						Status:    schemas.Ptr("in_progress"),
					},
				})
				state.SequenceNumber++
			}

			// Process based on event type
			switch eventType {
			case "contentBlockStart":
				// Handle tool_use content blocks
				if event.Start != nil && event.Start.ToolUse != nil {
					state.CurrentToolCallIndex++
					toolState := converseToolCallState{
						ItemID:    fmt.Sprintf("fc_%s_%d", state.MessageID, state.CurrentToolCallIndex),
						ToolUseID: event.Start.ToolUse.ToolUseID,
						ToolName:  event.Start.ToolUse.Name,
					}
					state.ToolCalls = append(state.ToolCalls, toolState)

					// Emit output_item.added for function_call
					msgType := schemas.ResponsesMessageTypeFunctionCall
					sendResponse(&schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
						SequenceNumber: state.SequenceNumber,
						OutputIndex:    schemas.Ptr(state.CurrentToolCallIndex + 1), // +1 because text item is at index 0
						Item: &schemas.ResponsesMessage{
							ID:   &toolState.ItemID,
							Type: &msgType,
							ResponsesToolMessage: &schemas.ResponsesToolMessage{
								CallID: &toolState.ToolUseID,
								Name:   &toolState.ToolName,
							},
						},
					})
					state.SequenceNumber++
					state.ToolCalls[state.CurrentToolCallIndex].ItemAdded = true
				}

			case "contentBlockDelta":
				if event.Delta != nil {
					// Handle text delta
					if event.Delta.Text != nil && *event.Delta.Text != "" {
						text := *event.Delta.Text
						state.AccumulatedText += text

						// Add text output item if not already added
						if !state.TextItemAdded {
							msgType := schemas.ResponsesMessageTypeMessage
							role := schemas.ResponsesInputMessageRoleAssistant
							sendResponse(&schemas.BifrostResponsesStreamResponse{
								Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
								SequenceNumber: state.SequenceNumber,
								OutputIndex:    schemas.Ptr(0),
								Item: &schemas.ResponsesMessage{
									ID:   &state.TextItemID,
									Type: &msgType,
									Role: &role,
									Content: &schemas.ResponsesMessageContent{
										ContentBlocks: []schemas.ResponsesMessageContentBlock{},
									},
								},
							})
							state.SequenceNumber++
							state.TextItemAdded = true
						}

						// Add content part if not already added
						if !state.TextContentAdded {
							emptyText := ""
							sendResponse(&schemas.BifrostResponsesStreamResponse{
								Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
								SequenceNumber: state.SequenceNumber,
								OutputIndex:    schemas.Ptr(0),
								ContentIndex:   schemas.Ptr(0),
								ItemID:         &state.TextItemID,
								Part: &schemas.ResponsesMessageContentBlock{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: &emptyText,
									ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
										LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
										Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
									},
								},
							})
							state.SequenceNumber++
							state.TextContentAdded = true
						}

						// Emit text delta
						sendResponse(&schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
							SequenceNumber: state.SequenceNumber,
							OutputIndex:    schemas.Ptr(0),
							ContentIndex:   schemas.Ptr(0),
							ItemID:         &state.TextItemID,
							Delta:          &text,
						})
						state.SequenceNumber++
					}

					// Handle tool use delta (streaming arguments)
					if event.Delta.ToolUse != nil && event.Delta.ToolUse.Input != "" && state.CurrentToolCallIndex >= 0 {
						toolIdx := state.CurrentToolCallIndex
						args := event.Delta.ToolUse.Input
						state.ToolCalls[toolIdx].AccumulatedArgs += args

						// Emit function_call_arguments.delta
						sendResponse(&schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
							SequenceNumber: state.SequenceNumber,
							OutputIndex:    schemas.Ptr(toolIdx + 1),
							ItemID:         &state.ToolCalls[toolIdx].ItemID,
							Delta:          &args,
						})
						state.SequenceNumber++
					}
				}

			case "contentBlockStop":
				// If we just finished a tool use block, emit function_call_arguments.done
				if state.CurrentToolCallIndex >= 0 && len(state.ToolCalls) > 0 {
					toolIdx := state.CurrentToolCallIndex
					if state.ToolCalls[toolIdx].ItemAdded && state.ToolCalls[toolIdx].AccumulatedArgs != "" {
						sendResponse(&schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
							SequenceNumber: state.SequenceNumber,
							OutputIndex:    schemas.Ptr(toolIdx + 1),
							ItemID:         &state.ToolCalls[toolIdx].ItemID,
							Arguments:      &state.ToolCalls[toolIdx].AccumulatedArgs,
						})
						state.SequenceNumber++
					}
				}

			case "messageStop":
				if event.StopReason != nil {
					mappedReason := mapConverseStopReason(*event.StopReason)
					stopReason = &mappedReason
				}

			case "metadata":
				if event.Usage != nil {
					usage.InputTokens = event.Usage.InputTokens
					usage.OutputTokens = event.Usage.OutputTokens
					usage.TotalTokens = event.Usage.TotalTokens
				}
			}
		}
	}

	// Calculate total tokens if not set
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}

	// Emit final events
	// 1. Emit output_text.done if we had text
	if state.TextItemAdded {
		sendResponse(&schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.TextItemID,
			Text:           &state.AccumulatedText,
		})
		state.SequenceNumber++

		// Emit content_part.done
		sendResponse(&schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.TextItemID,
			Part: &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: &state.AccumulatedText,
				ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
					LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
				},
			},
		})
		state.SequenceNumber++

		// Emit output_item.done for text
		msgType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		sendResponse(&schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			Item: &schemas.ResponsesMessage{
				ID:     &state.TextItemID,
				Type:   &msgType,
				Role:   &role,
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: &state.AccumulatedText,
							ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
								LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
								Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							},
						},
					},
				},
			},
		})
		state.SequenceNumber++
	}

	// 2. Emit output_item.done for each tool call
	for i, toolCall := range state.ToolCalls {
		if toolCall.ItemAdded {
			msgType := schemas.ResponsesMessageTypeFunctionCall
			sendResponse(&schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    schemas.Ptr(i + 1),
				Item: &schemas.ResponsesMessage{
					ID:     &toolCall.ItemID,
					Type:   &msgType,
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &toolCall.ToolUseID,
						Name:      &toolCall.ToolName,
						Arguments: &toolCall.AccumulatedArgs,
					},
				},
			})
			state.SequenceNumber++
		}
	}

	// 3. Build output for response.completed
	var outputMessages []schemas.ResponsesMessage
	if state.TextItemAdded {
		msgType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		outputMessages = append(outputMessages, schemas.ResponsesMessage{
			ID:     &state.TextItemID,
			Type:   &msgType,
			Role:   &role,
			Status: schemas.Ptr("completed"),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: &state.AccumulatedText,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						},
					},
				},
			},
		})
	}
	for _, toolCall := range state.ToolCalls {
		if toolCall.ItemAdded {
			msgType := schemas.ResponsesMessageTypeFunctionCall
			outputMessages = append(outputMessages, schemas.ResponsesMessage{
				ID:     &toolCall.ItemID,
				Type:   &msgType,
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolCall.ToolUseID,
					Name:      &toolCall.ToolName,
					Arguments: &toolCall.AccumulatedArgs,
				},
			})
		}
	}

	// 4. Emit response.completed
	completedAt := int(time.Now().Unix())
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	finalResp := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCompleted,
		SequenceNumber: state.SequenceNumber,
		Response: &schemas.BifrostResponsesResponse{
			ID:          &state.MessageID,
			Object:      "response",
			CreatedAt:   state.CreatedAt,
			CompletedAt: &completedAt,
			Model:       model,
			Status:      schemas.Ptr("completed"),
			StopReason:  stopReason,
			Output:      outputMessages,
			Usage:       usage,
		},
	}
	finalResp.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType:    schemas.ResponsesStreamRequest,
		Provider:       providerName,
		ModelRequested: model,
		ChunkIndex:     chunkIndex,
		Latency:        time.Since(startTime).Milliseconds(),
	}
	providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, finalResp, nil, nil, nil), responseChan)
}

// parseBedrockConverseToResponsesResponse parses a Bedrock Converse API response into Bifrost Responses format
func parseBedrockConverseToResponsesResponse(body []byte, model string) (*schemas.BifrostResponsesResponse, error) {
	var converseResp bedrock.BedrockConverseResponse
	if err := sonic.Unmarshal(body, &converseResp); err != nil {
		return nil, err
	}

	messageID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := int(time.Now().Unix())

	// Build output messages
	var outputMessages []schemas.ResponsesMessage

	if converseResp.Output != nil && converseResp.Output.Message != nil {
		// Extract text content
		var textContent string
		for _, block := range converseResp.Output.Message.Content {
			if block.Text != nil {
				textContent += *block.Text
			}
		}

		// Add text message if we have text content
		if textContent != "" {
			msgType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant
			outputMessages = append(outputMessages, schemas.ResponsesMessage{
				ID:     schemas.Ptr(fmt.Sprintf("msg_%s_text_0", messageID)),
				Type:   &msgType,
				Role:   &role,
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: &textContent,
							ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
								LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
								Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							},
						},
					},
				},
			})
		}

		// Extract tool calls
		toolCallIndex := 0
		for _, block := range converseResp.Output.Message.Content {
			if block.ToolUse != nil {
				// Serialize arguments to JSON string
				argsJSON := "{}"
				if block.ToolUse.Input != nil {
					if argsBytes, err := sonic.Marshal(block.ToolUse.Input); err == nil {
						argsJSON = string(argsBytes)
					}
				}

				msgType := schemas.ResponsesMessageTypeFunctionCall
				toolUseID := block.ToolUse.ToolUseID
				toolName := block.ToolUse.Name
				outputMessages = append(outputMessages, schemas.ResponsesMessage{
					ID:     schemas.Ptr(fmt.Sprintf("fc_%s_%d", messageID, toolCallIndex)),
					Type:   &msgType,
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &toolUseID,
						Name:      &toolName,
						Arguments: &argsJSON,
					},
				})
				toolCallIndex++
			}
		}
	}

	// Map stop reason
	mappedStopReason := mapConverseStopReason(converseResp.StopReason)

	response := &schemas.BifrostResponsesResponse{
		ID:         &messageID,
		Object:     "response",
		CreatedAt:  createdAt,
		Model:      model,
		Status:     schemas.Ptr("completed"),
		StopReason: &mappedStopReason,
		Output:     outputMessages,
	}

	if converseResp.Usage != nil {
		response.Usage = &schemas.ResponsesResponseUsage{
			InputTokens:  converseResp.Usage.InputTokens,
			OutputTokens: converseResp.Usage.OutputTokens,
			TotalTokens:  converseResp.Usage.TotalTokens,
		}
	}

	return response, nil
}

// Type aliases for Bedrock Converse API types, reused from the bedrock package.
// These are used for SAP AI Core's Converse API which supports native tool calling.
// See core/providers/bedrock/types.go for full type definitions.

// ensureToolConfigForHistory checks if the Converse request messages contain
// toolUse or toolResult blocks but no toolConfig is defined. If so, it creates
// stub tool definitions from the tool names found in history. This is required
// because Bedrock Converse API mandates toolConfig when tool blocks are present.
func ensureToolConfigForHistory(converseReq *bedrock.BedrockConverseRequest) {
	if converseReq.ToolConfig != nil {
		return
	}

	// Detect any tool-related blocks and collect tool names from toolUse blocks
	hasAnyToolBlocks := false
	seen := make(map[string]bool)
	var toolNames []string
	for _, msg := range converseReq.Messages {
		for _, block := range msg.Content {
			if block.ToolUse != nil {
				hasAnyToolBlocks = true
				if block.ToolUse.Name != "" && !seen[block.ToolUse.Name] {
					seen[block.ToolUse.Name] = true
					toolNames = append(toolNames, block.ToolUse.Name)
				}
			}
			if block.ToolResult != nil {
				hasAnyToolBlocks = true
			}
		}
	}

	if !hasAnyToolBlocks {
		return
	}

	// If we have names, build stub tool specs for those names; otherwise provide an empty tools list
	if len(toolNames) > 0 {
		tools := make([]bedrock.BedrockTool, 0, len(toolNames))
		for _, name := range toolNames {
			tools = append(tools, bedrock.BedrockTool{
				ToolSpec: &bedrock.BedrockToolSpec{
					Name: name,
					InputSchema: bedrock.BedrockToolInputSchema{
						JSON: map[string]interface{}{
							"type":       "object",
							"properties": map[string]interface{}{},
						},
					},
				},
			})
		}
		converseReq.ToolConfig = &bedrock.BedrockToolConfig{Tools: tools}
		return
	}

	// No tool names found (e.g., only toolResult blocks) — still attach an empty toolConfig to satisfy Bedrock
	converseReq.ToolConfig = &bedrock.BedrockToolConfig{Tools: []bedrock.BedrockTool{}}
}

// convertToBedrockConverse converts a Bifrost chat request to Bedrock Converse API format
// This is required for SAP AI Core to support native tool calling
func convertToBedrockConverse(request *schemas.BifrostChatRequest) *bedrock.BedrockConverseRequest {
	converseReq := &bedrock.BedrockConverseRequest{}

	maxTokens := DefaultMaxTokens

	// Convert messages from Input field
	// We need to handle consecutive tool messages specially - they must be merged into a single user message
	var systemMessages []bedrock.BedrockSystemMessage
	i := 0
	for i < len(request.Input) {
		msg := request.Input[i]

		if msg.Role == schemas.ChatMessageRoleSystem {
			// Extract system message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				systemMessages = append(systemMessages, bedrock.BedrockSystemMessage{
					Text: msg.Content.ContentStr,
				})
			}
			i++
			continue
		}

		// Handle tool messages - collect ALL consecutive tool messages and merge into a single user message
		if msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil {
			var toolResultBlocks []bedrock.BedrockContentBlock

			// Collect all consecutive tool messages
			for i < len(request.Input) && request.Input[i].Role == schemas.ChatMessageRoleTool && request.Input[i].ChatToolMessage != nil {
				toolMsg := request.Input[i]
				toolResultContent := ""
				if toolMsg.Content != nil && toolMsg.Content.ContentStr != nil {
					toolResultContent = *toolMsg.Content.ContentStr
				}

				toolUseId := ""
				if toolMsg.ChatToolMessage.ToolCallID != nil {
					toolUseId = *toolMsg.ChatToolMessage.ToolCallID
				}

				toolResultBlocks = append(toolResultBlocks, bedrock.BedrockContentBlock{
					ToolResult: &bedrock.BedrockToolResult{
						ToolUseID: toolUseId,
						Content: []bedrock.BedrockContentBlock{
							{Text: &toolResultContent},
						},
					},
				})
				i++
			}

			// Create a single user message with all tool results
			converseMsg := bedrock.BedrockMessage{
				Role:    "user", // Bedrock Converse uses user role for tool results
				Content: toolResultBlocks,
			}
			converseReq.Messages = append(converseReq.Messages, converseMsg)
			continue
		}

		converseMsg := bedrock.BedrockMessage{
			Role: bedrock.BedrockMessageRole(mapRoleForAnthropic(msg.Role)),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
				// Only add text block if content is non-empty (Bedrock rejects blank text)
				converseMsg.Content = []bedrock.BedrockContentBlock{
					{Text: msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil && *block.Text != "" {
						// Only add text block if content is non-empty (Bedrock rejects blank text)
						converseMsg.Content = append(converseMsg.Content, bedrock.BedrockContentBlock{
							Text: block.Text,
						})
					} else if block.Type == schemas.ChatContentBlockTypeImage && block.ImageURLStruct != nil {
						// Handle image URL - extract base64 data
						mediaType := extractMediaType(block.ImageURLStruct.URL)
						format := "jpeg" // default
						if strings.Contains(mediaType, "png") {
							format = "png"
						} else if strings.Contains(mediaType, "gif") {
							format = "gif"
						} else if strings.Contains(mediaType, "webp") {
							format = "webp"
						}
						converseMsg.Content = append(converseMsg.Content, bedrock.BedrockContentBlock{
							Image: &bedrock.BedrockImageSource{
								Format: format,
								Source: bedrock.BedrockImageSourceData{
									Bytes: schemas.Ptr(extractBase64Data(block.ImageURLStruct.URL)),
								},
							},
						})
					}
				}
			}
		}

		// Handle assistant messages with tool calls
		if msg.Role == schemas.ChatMessageRoleAssistant && msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
			for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
				// Convert arguments string to any (parse JSON)
				var inputArgs interface{}
				if toolCall.Function.Arguments != "" {
					if err := sonic.UnmarshalString(toolCall.Function.Arguments, &inputArgs); err != nil {
						// If JSON parsing fails, use empty object
						inputArgs = map[string]interface{}{}
					}
				} else {
					inputArgs = map[string]interface{}{}
				}

				toolUseId := ""
				if toolCall.ID != nil {
					toolUseId = *toolCall.ID
				}
				toolName := ""
				if toolCall.Function.Name != nil {
					toolName = *toolCall.Function.Name
				}

				converseMsg.Content = append(converseMsg.Content, bedrock.BedrockContentBlock{
					ToolUse: &bedrock.BedrockToolUse{
						ToolUseID: toolUseId,
						Name:      toolName,
						Input:     inputArgs,
					},
				})
			}
		}

		converseReq.Messages = append(converseReq.Messages, converseMsg)
		i++
	}

	converseReq.System = systemMessages

	// Set inference config
	converseReq.InferenceConfig = &bedrock.BedrockInferenceConfig{
		MaxTokens: &maxTokens,
	}

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			converseReq.InferenceConfig.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			converseReq.InferenceConfig.TopP = request.Params.TopP
		}
		if request.Params.MaxCompletionTokens != nil {
			converseReq.InferenceConfig.MaxTokens = request.Params.MaxCompletionTokens
		}
		if request.Params.Stop != nil {
			converseReq.InferenceConfig.StopSequences = request.Params.Stop
		}

		// Convert tools
		if request.Params.Tools != nil {
			tools := make([]bedrock.BedrockTool, 0, len(request.Params.Tools))
			for _, tool := range request.Params.Tools {
				if tool.Function == nil {
					continue
				}
				converseTool := bedrock.BedrockTool{
					ToolSpec: &bedrock.BedrockToolSpec{
						Name:        tool.Function.Name,
						Description: tool.Function.Description,
					},
				}
				if tool.Function.Parameters != nil {
					// Resolve $ref references in schema - Bedrock Converse doesn't support $ref alongside other fields
					converseTool.ToolSpec.InputSchema = bedrock.BedrockToolInputSchema{
						JSON: resolveSchemaRefs(tool.Function.Parameters),
					}
				}
				tools = append(tools, converseTool)
			}
			if len(tools) > 0 {
				converseReq.ToolConfig = &bedrock.BedrockToolConfig{
					Tools: tools,
				}
			}
		}

		// Convert tool choice
		if request.Params.ToolChoice != nil && converseReq.ToolConfig != nil {
			if request.Params.ToolChoice.ChatToolChoiceStr != nil {
				choice := *request.Params.ToolChoice.ChatToolChoiceStr
				switch choice {
				case "auto":
					converseReq.ToolConfig.ToolChoice = &bedrock.BedrockToolChoice{Auto: &bedrock.BedrockToolChoiceAuto{}}
				case "required":
					converseReq.ToolConfig.ToolChoice = &bedrock.BedrockToolChoice{Any: &bedrock.BedrockToolChoiceAny{}}
					// "none" is not directly supported by Converse API, omit it
				}
			} else if request.Params.ToolChoice.ChatToolChoiceStruct != nil {
				// Specific tool choice
				if request.Params.ToolChoice.ChatToolChoiceStruct.Function.Name != "" {
					converseReq.ToolConfig.ToolChoice = &bedrock.BedrockToolChoice{
						Tool: &bedrock.BedrockToolChoiceTool{
							Name: request.Params.ToolChoice.ChatToolChoiceStruct.Function.Name,
						},
					}
				}
			}
		}
	}

	// Ensure toolConfig is present when conversation history contains tool blocks
	ensureToolConfigForHistory(converseReq)

	return converseReq
}

// parseBedrockConverseResponse parses a Bedrock Converse API response into Bifrost format
func parseBedrockConverseResponse(body []byte, model string) (*schemas.BifrostChatResponse, error) {
	var converseResp bedrock.BedrockConverseResponse
	if err := sonic.Unmarshal(body, &converseResp); err != nil {
		return nil, err
	}

	// Extract text content and tool calls
	var content string
	var toolCalls []schemas.ChatAssistantMessageToolCall
	toolCallIndex := uint16(0)

	if converseResp.Output != nil && converseResp.Output.Message != nil {
		for _, block := range converseResp.Output.Message.Content {
			if block.Text != nil {
				content += *block.Text
			}
			if block.ToolUse != nil {
				// Convert tool_use block to OpenAI tool call format
				var argsJSON string
				if block.ToolUse.Input != nil {
					if argsBytes, err := sonic.Marshal(block.ToolUse.Input); err == nil {
						argsJSON = string(argsBytes)
					}
				}
				if argsJSON == "" {
					argsJSON = "{}"
				}

				toolUseId := block.ToolUse.ToolUseID
				toolName := block.ToolUse.Name

				toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
					Index: toolCallIndex,
					Type:  schemas.Ptr("function"),
					ID:    &toolUseId,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &toolName,
						Arguments: argsJSON,
					},
				})
				toolCallIndex++
			}
		}
	}

	// Map stop reason
	finishReason := mapConverseStopReason(converseResp.StopReason)

	// Create ChatMessage for the response
	assistantRole := schemas.ChatMessageRoleAssistant
	responseMessage := &schemas.ChatMessage{
		Role: assistantRole,
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
	}

	// Add tool calls to assistant message if present
	if len(toolCalls) > 0 {
		responseMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls: toolCalls,
		}
	}

	response := &schemas.BifrostChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: int(time.Now().Unix()),
		Model:   model,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: responseMessage,
				},
				FinishReason: &finishReason,
			},
		},
	}

	if converseResp.Usage != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     converseResp.Usage.InputTokens,
			CompletionTokens: converseResp.Usage.OutputTokens,
			TotalTokens:      converseResp.Usage.TotalTokens,
		}
		// Handle cached tokens if present
		if converseResp.Usage.CacheReadInputTokens > 0 {
			response.Usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
				CachedReadTokens: converseResp.Usage.CacheReadInputTokens,
			}
		}
	}

	return response, nil
}

// mapConverseStopReason maps Bedrock Converse stop reasons to OpenAI format
func mapConverseStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

// processBedrockConverseEventStream processes Bedrock Converse API event stream and sends chunks to the channel
// This handles the Converse stream format which has native tool calling support
func processBedrockConverseEventStream(
	ctx *schemas.BifrostContext,
	bodyStream io.Reader,
	responseChan chan *schemas.BifrostStreamChunk,
	postHookRunner schemas.PostHookRunner,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	chunkIndex := -1
	usage := &schemas.BifrostLLMUsage{}
	var finishReason *string
	messageID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	startTime := time.Now()

	// Track current tool call index for streaming tool arguments
	currentToolCallIndex := -1

	// Process AWS Event Stream format using proper decoder
	decoder := eventstream.NewDecoder()
	payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

	for {
		// If context was cancelled/timed out, let defer handle it
		if ctx.Err() != nil {
			return
		}

		// Decode a single EventStream message
		message, err := decoder.Decode(bodyStream, payloadBuf)
		if err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// End of stream - this is normal
			if err == io.EOF {
				break
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error decoding %s Converse EventStream message: %v", providerName, err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, model, logger)
			return
		}

		// Process the decoded message payload
		if len(message.Payload) > 0 {
			// Check message type header for errors
			if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
				if msgType := msgTypeHeader.String(); msgType != "event" {
					excType := msgType
					if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
						if v := excHeader.String(); v != "" {
							excType = v
						}
					}
					errMsg := string(message.Payload)
					err := fmt.Errorf("%s Converse stream %s: %s", providerName, excType, errMsg)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, model, logger)
					return
				}
			}

			// Get the event type from headers
			eventType := ""
			if eventTypeHeader := message.Headers.Get(":event-type"); eventTypeHeader != nil {
				eventType = eventTypeHeader.String()
			}

			// Parse the Converse stream event from the payload
			var event bedrock.BedrockStreamEvent
			if err := sonic.Unmarshal(message.Payload, &event); err != nil {
				logger.Debug("Failed to parse Converse stream event: %v, data: %s", err, string(message.Payload))
				continue
			}

			// Process based on event type
			switch eventType {
			case "messageStart":
				// Extract role from message start
				if event.Role != nil {
					chunkIndex++
					response := &schemas.BifrostChatResponse{
						ID:      messageID,
						Object:  "chat.completion.chunk",
						Created: int(time.Now().Unix()),
						Model:   model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										Role: event.Role,
									},
								},
							},
						},
					}
					response.ExtraFields.Provider = providerName
					response.ExtraFields.ModelRequested = model
					response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
					response.ExtraFields.ChunkIndex = chunkIndex
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
				}

			case "contentBlockStart":
				// Handle tool_use content blocks - emit tool call metadata
				if event.Start != nil && event.Start.ToolUse != nil {
					currentToolCallIndex++
					chunkIndex++

					// Create streaming response with tool call metadata (ID and name)
					toolUseId := event.Start.ToolUse.ToolUseID
					toolName := event.Start.ToolUse.Name
					response := &schemas.BifrostChatResponse{
						ID:      messageID,
						Object:  "chat.completion.chunk",
						Created: int(time.Now().Unix()),
						Model:   model,
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										ToolCalls: []schemas.ChatAssistantMessageToolCall{
											{
												Index: uint16(currentToolCallIndex),
												Type:  schemas.Ptr("function"),
												ID:    &toolUseId,
												Function: schemas.ChatAssistantMessageToolCallFunction{
													Name:      &toolName,
													Arguments: "", // Empty initially, filled by contentBlockDelta
												},
											},
										},
									},
								},
							},
						},
					}
					response.ExtraFields.Provider = providerName
					response.ExtraFields.ModelRequested = model
					response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
					response.ExtraFields.ChunkIndex = chunkIndex
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
				}

			case "contentBlockDelta":
				// Handle text delta and tool use delta
				if event.Delta != nil {
					// Handle text delta
					if event.Delta.Text != nil && *event.Delta.Text != "" {
						chunkIndex++
						response := &schemas.BifrostChatResponse{
							ID:      messageID,
							Object:  "chat.completion.chunk",
							Created: int(time.Now().Unix()),
							Model:   model,
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: &schemas.ChatStreamResponseChoiceDelta{
											Content: event.Delta.Text,
										},
									},
								},
							},
						}
						response.ExtraFields.Provider = providerName
						response.ExtraFields.ModelRequested = model
						response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
						response.ExtraFields.ChunkIndex = chunkIndex
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
					}

					// Handle tool use delta (streaming arguments)
					if event.Delta.ToolUse != nil && event.Delta.ToolUse.Input != "" {
						chunkIndex++
						response := &schemas.BifrostChatResponse{
							ID:      messageID,
							Object:  "chat.completion.chunk",
							Created: int(time.Now().Unix()),
							Model:   model,
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: &schemas.ChatStreamResponseChoiceDelta{
											ToolCalls: []schemas.ChatAssistantMessageToolCall{
												{
													Index: uint16(currentToolCallIndex),
													Type:  schemas.Ptr("function"),
													Function: schemas.ChatAssistantMessageToolCallFunction{
														Arguments: event.Delta.ToolUse.Input,
													},
												},
											},
										},
									},
								},
							},
						}
						response.ExtraFields.Provider = providerName
						response.ExtraFields.ModelRequested = model
						response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
						response.ExtraFields.ChunkIndex = chunkIndex
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
					}
				}

			case "contentBlockStop":
				// Content block completed - no action needed

			case "messageStop":
				// Extract stop reason
				if event.StopReason != nil {
					reason := mapConverseStopReason(*event.StopReason)
					finishReason = &reason
				}

			case "metadata":
				// Extract usage information
				if event.Usage != nil {
					usage.PromptTokens = event.Usage.InputTokens
					usage.CompletionTokens = event.Usage.OutputTokens
					usage.TotalTokens = event.Usage.TotalTokens
					// Handle cached tokens if present
					if event.Usage.CacheReadInputTokens > 0 {
						usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
							CachedReadTokens: event.Usage.CacheReadInputTokens,
						}
					}
				}
			}
		}
	}

	// Calculate total tokens if not set
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	// Send final chunk with usage
	if finishReason != nil || usage.TotalTokens > 0 {
		finalResponse := providerUtils.CreateBifrostChatCompletionChunkResponse("", usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, model)
		finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, finalResponse, nil, nil, nil, nil), responseChan)
	}
}
