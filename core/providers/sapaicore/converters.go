package sapaicore

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// Response pools for Bedrock and Vertex response objects
var (
	bedrockResponsePool = sync.Pool{
		New: func() interface{} {
			return &BedrockResponse{}
		},
	}

	vertexResponsePool = sync.Pool{
		New: func() interface{} {
			return &VertexGenerateContentResponse{}
		},
	}
)

// acquireBedrockResponse gets a BedrockResponse from the pool and resets it.
func acquireBedrockResponse() *BedrockResponse {
	resp := bedrockResponsePool.Get().(*BedrockResponse)
	*resp = BedrockResponse{} // Reset the struct
	return resp
}

// releaseBedrockResponse returns a BedrockResponse to the pool.
func releaseBedrockResponse(resp *BedrockResponse) {
	if resp != nil {
		bedrockResponsePool.Put(resp)
	}
}

// acquireVertexResponse gets a VertexGenerateContentResponse from the pool and resets it.
func acquireVertexResponse() *VertexGenerateContentResponse {
	resp := vertexResponsePool.Get().(*VertexGenerateContentResponse)
	*resp = VertexGenerateContentResponse{} // Reset the struct
	return resp
}

// releaseVertexResponse returns a VertexGenerateContentResponse to the pool.
func releaseVertexResponse(resp *VertexGenerateContentResponse) {
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

// AnthropicStreamEvent represents a streaming event from SAP AI Core Bedrock (Anthropic Messages API format)
// Event types: message_start, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop
type AnthropicStreamEvent struct {
	Type         string                      `json:"type"`
	Index        *int                        `json:"index,omitempty"`
	Message      *AnthropicStreamMessage     `json:"message,omitempty"`                          // For message_start
	ContentBlock *AnthropicContentBlock      `json:"content_block,omitempty"`                    // For content_block_start
	Delta        *AnthropicStreamDelta       `json:"delta,omitempty"`                            // For content_block_delta and message_delta
	Usage        *AnthropicStreamUsage       `json:"usage,omitempty"`                            // For message_delta
	Metrics      *AnthropicInvocationMetrics `json:"amazon-bedrock-invocationMetrics,omitempty"` // For message_stop
}

// AnthropicContentBlock represents a content block in content_block_start events
type AnthropicContentBlock struct {
	Type  string  `json:"type"`            // "text", "tool_use"
	ID    *string `json:"id,omitempty"`    // For tool_use blocks
	Name  *string `json:"name,omitempty"`  // For tool_use blocks
	Input any     `json:"input,omitempty"` // For tool_use blocks (usually empty object initially)
}

// AnthropicStreamMessage represents the message in message_start event
type AnthropicStreamMessage struct {
	ID           string                `json:"id"`
	Type         string                `json:"type"`
	Role         string                `json:"role"`
	Model        string                `json:"model"`
	StopReason   *string               `json:"stop_reason"`
	StopSequence *string               `json:"stop_sequence"`
	Usage        *AnthropicStreamUsage `json:"usage,omitempty"`
}

// AnthropicStreamDelta represents delta content in streaming
type AnthropicStreamDelta struct {
	Type         string  `json:"type,omitempty"`          // "text_delta", "input_json_delta" for content_block_delta
	Text         *string `json:"text,omitempty"`          // Text content for text_delta
	PartialJSON  *string `json:"partial_json,omitempty"`  // Partial JSON for input_json_delta (tool arguments)
	StopReason   *string `json:"stop_reason,omitempty"`   // For message_delta
	StopSequence *string `json:"stop_sequence,omitempty"` // For message_delta
}

// AnthropicStreamUsage represents usage information
type AnthropicStreamUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// AnthropicInvocationMetrics represents invocation metrics from message_stop
type AnthropicInvocationMetrics struct {
	InputTokenCount   int `json:"inputTokenCount"`
	OutputTokenCount  int `json:"outputTokenCount"`
	InvocationLatency int `json:"invocationLatency"`
	FirstByteLatency  int `json:"firstByteLatency"`
}

// convertAnthropicStopReason converts Anthropic stop reasons to Bifrost format
func convertAnthropicStopReason(reason string) *string {
	var bifrostReason string
	switch reason {
	case "end_turn":
		bifrostReason = "stop"
	case "max_tokens":
		bifrostReason = "length"
	case "stop_sequence":
		bifrostReason = "stop"
	case "tool_use":
		bifrostReason = "tool_calls"
	default:
		bifrostReason = reason
	}
	return &bifrostReason
}

// BedrockRequest represents a request to Bedrock-compatible API
type BedrockRequest struct {
	AnthropicVersion string             `json:"anthropic_version,omitempty"`
	MaxTokens        int                `json:"max_tokens"`
	Messages         []BedrockMessage   `json:"messages"`
	System           string             `json:"system,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	TopK             *int               `json:"top_k,omitempty"`
	StopSequences    []string           `json:"stop_sequences,omitempty"`
	Tools            []BedrockTool      `json:"tools,omitempty"`
	ToolChoice       *BedrockToolChoice `json:"tool_choice,omitempty"`
}

// BedrockTool represents a tool definition for Bedrock/Anthropic
type BedrockTool struct {
	Name        string                          `json:"name"`
	Description *string                         `json:"description,omitempty"`
	InputSchema *schemas.ToolFunctionParameters `json:"input_schema,omitempty"`
}

// BedrockToolChoice represents tool choice configuration
type BedrockToolChoice struct {
	Type string  `json:"type"`           // "auto", "any", "tool"
	Name *string `json:"name,omitempty"` // Required when type is "tool"
}

// BedrockMessage represents a message in Bedrock format
type BedrockMessage struct {
	Role    string                `json:"role"`
	Content []BedrockContentBlock `json:"content"`
}

// BedrockContentBlock represents a content block in Bedrock format
type BedrockContentBlock struct {
	Type      string              `json:"type"`
	Text      string              `json:"text,omitempty"`
	Source    *BedrockImageSource `json:"source,omitempty"`
	ID        *string             `json:"id,omitempty"`          // For tool_use blocks
	Name      *string             `json:"name,omitempty"`        // For tool_use blocks
	Input     any                 `json:"input,omitempty"`       // For tool_use blocks
	ToolUseID *string             `json:"tool_use_id,omitempty"` // For tool_result blocks
	Content   any                 `json:"content,omitempty"`     // For tool_result blocks (can be string or array)
}

// BedrockImageSource represents an image source in Bedrock format
type BedrockImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// BedrockResponse represents a response from Bedrock-compatible API (uses Anthropic Messages API format)
type BedrockResponse struct {
	ID           string                `json:"id"`
	Type         string                `json:"type"`
	Role         string                `json:"role"`
	Content      []BedrockContentBlock `json:"content"`
	Model        string                `json:"model"`
	StopReason   string                `json:"stop_reason"`
	StopSequence *string               `json:"stop_sequence,omitempty"`
	Usage        *AnthropicStreamUsage `json:"usage,omitempty"`
}

// VertexGenerateContentRequest represents a request to Vertex AI
type VertexGenerateContentRequest struct {
	Contents          []VertexContent         `json:"contents"`
	SystemInstruction *VertexContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *VertexGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []VertexTool            `json:"tools,omitempty"`
	ToolConfig        *VertexToolConfig       `json:"toolConfig,omitempty"`
}

// VertexContent represents content in Vertex format
type VertexContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []VertexPart `json:"parts"`
}

// VertexPart represents a part in Vertex content
type VertexPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *VertexInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *VertexFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *VertexFunctionResponse `json:"functionResponse,omitempty"`
}

// VertexInlineData represents inline data (images) in Vertex format
type VertexInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// VertexFunctionCall represents a function call in Vertex format
type VertexFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// VertexFunctionResponse represents a function response in Vertex format
type VertexFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// VertexTool represents a tool in Vertex format
type VertexTool struct {
	FunctionDeclarations []VertexFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

// VertexFunctionDeclaration represents a function declaration in Vertex format
type VertexFunctionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// VertexToolConfig represents tool configuration in Vertex format
type VertexToolConfig struct {
	FunctionCallingConfig *VertexFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// VertexFunctionCallingConfig represents function calling configuration
type VertexFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // AUTO, ANY, NONE
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// VertexGenerationConfig represents generation config for Vertex
type VertexGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// VertexGenerateContentResponse represents a response from Vertex AI
type VertexGenerateContentResponse struct {
	Candidates    []VertexCandidate    `json:"candidates"`
	UsageMetadata *VertexUsageMetadata `json:"usageMetadata,omitempty"`
}

// VertexCandidate represents a candidate in Vertex response
type VertexCandidate struct {
	Content      VertexContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
	Index        int           `json:"index"`
}

// VertexUsageMetadata represents usage metadata from Vertex
type VertexUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
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

// convertToBedrock converts a Bifrost chat request to Bedrock format
func convertToBedrock(request *schemas.BifrostChatRequest) *BedrockRequest {
	bedrockReq := &BedrockRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4096, // Default
	}

	// Get model config for max tokens
	config := GetModelConfig(request.Model)
	bedrockReq.MaxTokens = config.MaxTokens

	// Convert messages from Input field
	var systemMessage string
	for _, msg := range request.Input {
		if msg.Role == schemas.ChatMessageRoleSystem {
			// Extract system message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				systemMessage = *msg.Content.ContentStr
			}
			continue
		}

		// Handle tool messages - convert to tool_result blocks
		if msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil {
			toolResultContent := ""
			if msg.Content != nil && msg.Content.ContentStr != nil {
				toolResultContent = *msg.Content.ContentStr
			}
			bedrockMsg := BedrockMessage{
				Role: "user", // Anthropic uses user role for tool results
				Content: []BedrockContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: msg.ChatToolMessage.ToolCallID,
						Content:   toolResultContent,
					},
				},
			}
			bedrockReq.Messages = append(bedrockReq.Messages, bedrockMsg)
			continue
		}

		bedrockMsg := BedrockMessage{
			Role: mapRoleForAnthropic(msg.Role),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				bedrockMsg.Content = []BedrockContentBlock{
					{Type: "text", Text: *msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
						bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
							Type: "text",
							Text: *block.Text,
						})
					} else if block.Type == schemas.ChatContentBlockTypeImage && block.ImageURLStruct != nil {
						// Handle image URL - extract base64 data
						bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
							Type: "image",
							Source: &BedrockImageSource{
								Type:      "base64",
								MediaType: extractMediaType(block.ImageURLStruct.URL),
								Data:      block.ImageURLStruct.URL,
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
				var inputArgs any
				if toolCall.Function.Arguments != "" {
					if err := sonic.UnmarshalString(toolCall.Function.Arguments, &inputArgs); err != nil {
						// If JSON parsing fails, use empty object
						inputArgs = map[string]any{}
					}
				} else {
					inputArgs = map[string]any{}
				}

				bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
					Type:  "tool_use",
					ID:    toolCall.ID,
					Name:  toolCall.Function.Name,
					Input: inputArgs,
				})
			}
		}

		bedrockReq.Messages = append(bedrockReq.Messages, bedrockMsg)
	}

	bedrockReq.System = systemMessage

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			bedrockReq.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			bedrockReq.TopP = request.Params.TopP
		}
		if request.Params.MaxCompletionTokens != nil {
			bedrockReq.MaxTokens = *request.Params.MaxCompletionTokens
		}
		if request.Params.Stop != nil {
			bedrockReq.StopSequences = request.Params.Stop
		}

		// Convert tools
		if request.Params.Tools != nil {
			tools := make([]BedrockTool, 0, len(request.Params.Tools))
			for _, tool := range request.Params.Tools {
				if tool.Function == nil {
					continue
				}
				bedrockTool := BedrockTool{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
				}
				if tool.Function.Parameters != nil {
					bedrockTool.InputSchema = tool.Function.Parameters
				}
				tools = append(tools, bedrockTool)
			}
			bedrockReq.Tools = tools
		}

		// Convert tool choice
		if request.Params.ToolChoice != nil {
			if request.Params.ToolChoice.ChatToolChoiceStr != nil {
				// "auto", "none", "required" -> Anthropic uses "auto", "none" is not supported, "required" -> "any"
				choice := *request.Params.ToolChoice.ChatToolChoiceStr
				switch choice {
				case "auto":
					bedrockReq.ToolChoice = &BedrockToolChoice{Type: "auto"}
				case "required":
					bedrockReq.ToolChoice = &BedrockToolChoice{Type: "any"}
					// "none" is not directly supported by Anthropic, omit it
				}
			} else if request.Params.ToolChoice.ChatToolChoiceStruct != nil {
				// Specific tool choice - use the Function field
				if request.Params.ToolChoice.ChatToolChoiceStruct.Function.Name != "" {
					bedrockReq.ToolChoice = &BedrockToolChoice{
						Type: "tool",
						Name: &request.Params.ToolChoice.ChatToolChoiceStruct.Function.Name,
					}
				}
			}
		}
	}

	return bedrockReq
}

// parseBedrockResponse parses a Bedrock response into Bifrost format.
// Uses object pooling for efficient memory reuse.
func parseBedrockResponse(body []byte, model string) (*schemas.BifrostChatResponse, error) {
	bedrockResp := acquireBedrockResponse()
	defer releaseBedrockResponse(bedrockResp)

	if err := sonic.Unmarshal(body, bedrockResp); err != nil {
		return nil, err
	}

	// Extract text content and tool calls
	var content string
	var toolCalls []schemas.ChatAssistantMessageToolCall
	toolCallIndex := uint16(0)

	for _, block := range bedrockResp.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			// Convert tool_use block to OpenAI tool call format
			var argsJSON string
			if block.Input != nil {
				if argsBytes, err := sonic.Marshal(block.Input); err == nil {
					argsJSON = string(argsBytes)
				}
			}
			if argsJSON == "" {
				argsJSON = "{}"
			}

			toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
				Index: toolCallIndex,
				Type:  schemas.Ptr("function"),
				ID:    block.ID,
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      block.Name,
					Arguments: argsJSON,
				},
			})
			toolCallIndex++
		}
	}

	// Map stop reason
	finishReason := mapBedrockStopReason(bedrockResp.StopReason)

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
		ID:      bedrockResp.ID,
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

	if bedrockResp.Usage != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     bedrockResp.Usage.InputTokens,
			CompletionTokens: bedrockResp.Usage.OutputTokens,
			TotalTokens:      bedrockResp.Usage.InputTokens + bedrockResp.Usage.OutputTokens,
		}
	}

	return response, nil
}

// mapBedrockStopReason maps Bedrock stop reasons to OpenAI format
func mapBedrockStopReason(reason string) string {
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

// resolveSchemaRefs resolves $ref references in a JSON schema by inlining the referenced definitions.
// This is needed because Vertex/Gemini doesn't support $ref alongside other fields.
// It also removes "ref" fields (without $) that may be left over from pre-processing.
func resolveSchemaRefs(params *schemas.ToolFunctionParameters) any {
	if params == nil {
		return nil
	}

	// Convert to map for easier manipulation
	paramsBytes, err := sonic.Marshal(params)
	if err != nil {
		return params
	}

	var schemaMap map[string]any
	if err := sonic.Unmarshal(paramsBytes, &schemaMap); err != nil {
		return params
	}

	// Extract definitions ($defs or definitions)
	defs := make(map[string]any)
	if d, ok := schemaMap["$defs"].(map[string]any); ok {
		defs = d
	}
	if d, ok := schemaMap["definitions"].(map[string]any); ok {
		for k, v := range d {
			defs[k] = v
		}
	}

	// Recursively resolve all $ref references and remove "ref" fields
	// Even if no $defs/definitions exist, there might be "ref" fields to remove
	resolved := resolveRefsInValue(schemaMap, defs)

	// Remove $defs and definitions from the result (they're now inlined)
	if resolvedMap, ok := resolved.(map[string]any); ok {
		delete(resolvedMap, "$defs")
		delete(resolvedMap, "definitions")
		return resolvedMap
	}

	return resolved
}

// resolveRefsInValue recursively resolves $ref references and removes "ref" fields in a value
func resolveRefsInValue(value any, defs map[string]any) any {
	switch v := value.(type) {
	case map[string]any:
		// Check if this object has a $ref (standard JSON Schema reference)
		if ref, ok := v["$ref"].(string); ok {
			// Extract the definition name from the ref (e.g., "#/$defs/QuestionOption" -> "QuestionOption")
			refName := extractRefName(ref)
			if refName != "" {
				if def, ok := defs[refName]; ok {
					// Clone the definition and resolve any nested refs in it
					defCopy := deepCopyValue(def)
					resolved := resolveRefsInValue(defCopy, defs)

					// If the original object had other fields besides $ref, merge them
					// But according to JSON Schema, if $ref is present, other fields should be ignored
					// However, we'll merge non-$ref fields for compatibility
					if resolvedMap, ok := resolved.(map[string]any); ok {
						for k, val := range v {
							if k != "$ref" {
								// Only add if not already in resolved (definition takes precedence)
								if _, exists := resolvedMap[k]; !exists {
									resolvedMap[k] = resolveRefsInValue(val, defs)
								}
							}
						}
						return resolvedMap
					}
					return resolved
				}
			}
		}

		// No $ref or couldn't resolve, process all fields
		// Also remove "ref" fields (non-standard, without $) as Vertex doesn't support them
		result := make(map[string]any)
		for k, val := range v {
			// Skip "ref" field - this is a non-standard field that Vertex rejects
			// when it appears alongside other schema fields like "type", "properties", etc.
			if k == "ref" {
				continue
			}
			result[k] = resolveRefsInValue(val, defs)
		}
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

// deepCopyValue creates a deep copy of a value
func deepCopyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any)
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
func convertToVertex(request *schemas.BifrostChatRequest) *VertexGenerateContentRequest {
	vertexReq := &VertexGenerateContentRequest{}

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
	var pendingToolResponseParts []VertexPart

	// Convert messages from Input field
	for i, msg := range request.Input {
		if msg.Role == schemas.ChatMessageRoleSystem {
			// Handle system message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				vertexReq.SystemInstruction = &VertexContent{
					Parts: []VertexPart{{Text: *msg.Content.ContentStr}},
				}
			}
			continue
		}

		// Check if this is a tool response message
		isToolResponse := msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil

		// If we have pending tool responses and current message is NOT a tool response,
		// flush the pending tool responses as a single Content
		if len(pendingToolResponseParts) > 0 && !isToolResponse {
			vertexReq.Contents = append(vertexReq.Contents, VertexContent{
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

			pendingToolResponseParts = append(pendingToolResponseParts, VertexPart{
				FunctionResponse: &VertexFunctionResponse{
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
				vertexReq.Contents = append(vertexReq.Contents, VertexContent{
					Role:  "user", // Tool responses use "user" role in Vertex
					Parts: pendingToolResponseParts,
				})
				pendingToolResponseParts = nil
			}
			continue
		}

		vertexContent := VertexContent{
			Role: mapToVertexRole(string(msg.Role)),
		}

		// Handle assistant messages with tool calls
		if msg.Role == schemas.ChatMessageRoleAssistant && msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
			// Add text content if present
			if msg.Content != nil {
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					vertexContent.Parts = append(vertexContent.Parts, VertexPart{
						Text: *msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil && *block.Text != "" {
							vertexContent.Parts = append(vertexContent.Parts, VertexPart{
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

				functionCall := &VertexFunctionCall{
					Args: args,
				}
				if tc.Function.Name != nil {
					functionCall.Name = *tc.Function.Name
				}

				vertexContent.Parts = append(vertexContent.Parts, VertexPart{
					FunctionCall: functionCall,
				})
			}

			vertexReq.Contents = append(vertexReq.Contents, vertexContent)
			continue
		}

		// Convert regular content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				vertexContent.Parts = []VertexPart{{Text: *msg.Content.ContentStr}}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
						vertexContent.Parts = append(vertexContent.Parts, VertexPart{
							Text: *block.Text,
						})
					} else if block.Type == schemas.ChatContentBlockTypeImage && block.ImageURLStruct != nil {
						vertexContent.Parts = append(vertexContent.Parts, VertexPart{
							InlineData: &VertexInlineData{
								MimeType: extractMediaType(block.ImageURLStruct.URL),
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
	config := GetModelConfig(request.Model)
	vertexReq.GenerationConfig = &VertexGenerationConfig{
		MaxOutputTokens: &config.MaxTokens,
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
			var functionDeclarations []VertexFunctionDeclaration
			for _, tool := range request.Params.Tools {
				if tool.Type == schemas.ChatToolTypeFunction && tool.Function != nil {
					fd := VertexFunctionDeclaration{
						Name: tool.Function.Name,
					}
					if tool.Function.Description != nil {
						fd.Description = *tool.Function.Description
					}
					if tool.Function.Parameters != nil {
						// Resolve $ref references in schema - Vertex doesn't support $ref alongside other fields
						fd.Parameters = resolveSchemaRefs(tool.Function.Parameters)
					}
					functionDeclarations = append(functionDeclarations, fd)
				}
			}
			if len(functionDeclarations) > 0 {
				vertexReq.Tools = []VertexTool{{
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
func convertToolChoiceToVertexConfig(toolChoice *schemas.ChatToolChoice) *VertexToolConfig {
	config := &VertexToolConfig{
		FunctionCallingConfig: &VertexFunctionCallingConfig{},
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
		for i, part := range candidate.Content.Parts {
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
		finishReason = mapVertexFinishReason(candidate.FinishReason)

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
			PromptTokens:     vertexResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: vertexResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      vertexResp.UsageMetadata.TotalTokenCount,
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

// convertResponsesToBedrock converts a Bifrost Responses request to Bedrock format
func convertResponsesToBedrock(request *schemas.BifrostResponsesRequest) *BedrockRequest {
	bedrockReq := &BedrockRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4096, // Default
	}

	// Get model config for max tokens
	config := GetModelConfig(request.Model)
	bedrockReq.MaxTokens = config.MaxTokens

	// Convert messages from Input field
	var systemMessage string
	for _, msg := range request.Input {
		// Handle system messages
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemMessage = *msg.Content.ContentStr
				} else if msg.Content.ContentBlocks != nil {
					// Extract text from content blocks
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil {
							systemMessage += *block.Text
						}
					}
				}
			}
			continue
		}

		// Skip messages without role
		if msg.Role == nil {
			continue
		}

		bedrockMsg := BedrockMessage{
			Role: mapResponsesRoleForAnthropic(*msg.Role),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				bedrockMsg.Content = []BedrockContentBlock{
					{Type: "text", Text: *msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					switch block.Type {
					case schemas.ResponsesInputMessageContentBlockTypeText,
						schemas.ResponsesOutputMessageContentTypeText:
						if block.Text != nil {
							bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
								Type: "text",
								Text: *block.Text,
							})
						}
					case schemas.ResponsesInputMessageContentBlockTypeImage:
						if block.ImageURL != nil {
							bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
								Type: "image",
								Source: &BedrockImageSource{
									Type:      "base64",
									MediaType: extractMediaType(*block.ImageURL),
									Data:      extractBase64Data(*block.ImageURL),
								},
							})
						}
					}
				}
			}
		}

		bedrockReq.Messages = append(bedrockReq.Messages, bedrockMsg)
	}

	bedrockReq.System = systemMessage

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			bedrockReq.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			bedrockReq.TopP = request.Params.TopP
		}
		if request.Params.MaxOutputTokens != nil {
			bedrockReq.MaxTokens = *request.Params.MaxOutputTokens
		}
	}

	return bedrockReq
}

// convertResponsesToBedrockConverse converts a Bifrost Responses request to Bedrock Converse API format
// This is required for SAP AI Core to support native tool calling in the Responses API
func convertResponsesToBedrockConverse(request *schemas.BifrostResponsesRequest) *BedrockConverseRequest {
	converseReq := &BedrockConverseRequest{}

	// Get model config for max tokens
	config := GetModelConfig(request.Model)
	maxTokens := config.MaxTokens

	// Convert messages from Input field
	var systemMessages []ConverseSystemMessage
	i := 0
	for i < len(request.Input) {
		msg := request.Input[i]

		// Handle system messages
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemMessages = append(systemMessages, ConverseSystemMessage{
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
						systemMessages = append(systemMessages, ConverseSystemMessage{
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
			var toolResultBlocks []ConverseContentBlock

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

				toolResultBlocks = append(toolResultBlocks, ConverseContentBlock{
					ToolResult: &ConverseToolResult{
						ToolUseId: toolUseId,
						Content: []ConverseContentBlock{
							{Text: &toolResultContent},
						},
					},
				})
				i++
			}

			// Create a single user message with all tool results
			if len(toolResultBlocks) > 0 {
				converseMsg := ConverseMessage{
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

				converseMsg := ConverseMessage{
					Role: "assistant",
					Content: []ConverseContentBlock{
						{
							ToolUse: &ConverseToolUse{
								ToolUseId: toolUseId,
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

		converseMsg := ConverseMessage{
			Role: mapResponsesRoleForAnthropic(*msg.Role),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
				converseMsg.Content = []ConverseContentBlock{
					{Text: msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					switch block.Type {
					case schemas.ResponsesInputMessageContentBlockTypeText,
						schemas.ResponsesOutputMessageContentTypeText:
						if block.Text != nil && *block.Text != "" {
							converseMsg.Content = append(converseMsg.Content, ConverseContentBlock{
								Text: block.Text,
							})
						}
					case schemas.ResponsesInputMessageContentBlockTypeImage:
						if block.ImageURL != nil {
							mediaType := extractMediaType(*block.ImageURL)
							format := "jpeg"
							if strings.Contains(mediaType, "png") {
								format = "png"
							} else if strings.Contains(mediaType, "gif") {
								format = "gif"
							} else if strings.Contains(mediaType, "webp") {
								format = "webp"
							}
							converseMsg.Content = append(converseMsg.Content, ConverseContentBlock{
								Image: &ConverseImageSource{
									Format: format,
									Source: &ConverseImageSourceData{
										Bytes: extractBase64Data(*block.ImageURL),
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
	converseReq.InferenceConfig = &ConverseInferenceConfig{
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
			tools := make([]ConverseTool, 0, len(request.Params.Tools))
			for _, tool := range request.Params.Tools {
				// Only handle function tools for now
				if tool.Type != schemas.ResponsesToolTypeFunction {
					continue
				}

				var inputSchema ConverseToolInputSchema
				if tool.ResponsesToolFunction != nil && tool.ResponsesToolFunction.Parameters != nil {
					inputSchema = ConverseToolInputSchema{
						JSON: resolveSchemaRefs(tool.ResponsesToolFunction.Parameters),
					}
				}

				converseTool := ConverseTool{
					ToolSpec: &ConverseToolSpec{
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
				converseReq.ToolConfig = &ConverseToolConfig{
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
					converseReq.ToolConfig.ToolChoice = &ConverseToolChoice{Auto: &struct{}{}}
				case "required":
					converseReq.ToolConfig.ToolChoice = &ConverseToolChoice{Any: &struct{}{}}
					// "none" is not directly supported by Converse API, omit it
				}
			} else if request.Params.ToolChoice.ResponsesToolChoiceStruct != nil {
				// Specific tool choice
				if request.Params.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
					converseReq.ToolConfig.ToolChoice = &ConverseToolChoice{
						Tool: &struct {
							Name string `json:"name"`
						}{
							Name: *request.Params.ToolChoice.ResponsesToolChoiceStruct.Name,
						},
					}
				}
			}
		}
	}

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

// parseBedrockToResponsesResponse parses a Bedrock response into Bifrost Responses format.
// Uses object pooling for efficient memory reuse.
func parseBedrockToResponsesResponse(body []byte, model string) (*schemas.BifrostResponsesResponse, error) {
	bedrockResp := acquireBedrockResponse()
	defer releaseBedrockResponse(bedrockResp)

	if err := sonic.Unmarshal(body, bedrockResp); err != nil {
		return nil, err
	}

	// Build output messages from Bedrock response
	var outputMessages []schemas.ResponsesMessage

	// Extract text content and build output message
	var textContent string
	for _, block := range bedrockResp.Content {
		if block.Type == "text" {
			textContent += block.Text
		}
	}

	if textContent != "" {
		outputType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		contentBlockType := schemas.ResponsesOutputMessageContentTypeText

		outputMessages = append(outputMessages, schemas.ResponsesMessage{
			Type:   &outputType,
			Role:   &role,
			Status: schemas.Ptr("completed"),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: contentBlockType,
						Text: &textContent,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						},
					},
				},
			},
		})
	}

	// Map stop reason
	stopReason := mapBedrockStopReasonToResponses(bedrockResp.StopReason)

	response := &schemas.BifrostResponsesResponse{
		ID:         &bedrockResp.ID,
		Object:     "response",
		CreatedAt:  int(time.Now().Unix()),
		Model:      model,
		Output:     outputMessages,
		Status:     schemas.Ptr("completed"),
		StopReason: &stopReason,
	}

	if bedrockResp.Usage != nil {
		response.Usage = &schemas.ResponsesResponseUsage{
			InputTokens:  bedrockResp.Usage.InputTokens,
			OutputTokens: bedrockResp.Usage.OutputTokens,
			TotalTokens:  bedrockResp.Usage.InputTokens + bedrockResp.Usage.OutputTokens,
		}
	}

	return response, nil
}

// mapBedrockStopReasonToResponses maps Bedrock stop reasons to Responses API format
func mapBedrockStopReasonToResponses(reason string) string {
	switch reason {
	case "end_turn":
		return "end_turn"
	case "max_tokens":
		return "max_output_tokens"
	case "stop_sequence":
		return "stop_sequence"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

// BedrockResponsesStreamState tracks state during streaming conversion for responses API
type BedrockResponsesStreamState struct {
	MessageID            *string
	Model                *string
	CreatedAt            int
	SequenceNumber       int
	HasEmittedCreated    bool
	HasEmittedInProgress bool
	TextItemAdded        bool
	ContentPartAdded     bool
	AccumulatedText      string
	ItemID               string
}

// newBedrockResponsesStreamState creates a new stream state for Bedrock responses streaming
func newBedrockResponsesStreamState() *BedrockResponsesStreamState {
	return &BedrockResponsesStreamState{
		CreatedAt:      int(time.Now().Unix()),
		SequenceNumber: 0,
	}
}

// processBedrockResponsesEventStream processes Bedrock event stream (AWS binary eventstream format) and sends chunks to the channel
// for Responses API format
func processBedrockResponsesEventStream(
	ctx *schemas.BifrostContext,
	bodyStream io.Reader,
	responseChan chan *schemas.BifrostStreamChunk,
	postHookRunner schemas.PostHookRunner,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	state := newBedrockResponsesStreamState()
	state.Model = &model
	state.MessageID = schemas.Ptr(fmt.Sprintf("resp_%d", time.Now().UnixNano()))
	state.ItemID = fmt.Sprintf("msg_%s_item_0", *state.MessageID)

	usage := &schemas.ResponsesResponseUsage{}
	var stopReason *string
	startTime := time.Now()
	chunkIndex := 0

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
			logger.Warn("Error decoding %s EventStream message: %v", providerName, err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, model, logger)
			return
		}

		// Process the decoded message payload (contains JSON for normal events)
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
					err := fmt.Errorf("%s stream %s: %s", providerName, excType, errMsg)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, model, logger)
					return
				}
			}

			// Parse the chunk payload - Bedrock wraps the actual JSON in a "bytes" field (base64 encoded)
			var chunkPayload struct {
				Bytes []byte `json:"bytes"`
			}
			if err := sonic.Unmarshal(message.Payload, &chunkPayload); err != nil {
				logger.Debug("Failed to parse chunk payload: %v, data: %s", err, string(message.Payload))
				continue
			}

			// Parse the Anthropic Messages API event from the bytes
			var event AnthropicStreamEvent
			if err := sonic.Unmarshal(chunkPayload.Bytes, &event); err != nil {
				logger.Debug("Failed to parse Anthropic stream event: %v, data: %s", err, string(chunkPayload.Bytes))
				continue
			}

			// Convert to Bifrost Responses stream responses
			responses := convertAnthropicEventToResponses(event, state, providerName, model)

			// Update usage from message_start
			if event.Type == "message_start" && event.Message != nil && event.Message.Usage != nil {
				usage.InputTokens = event.Message.Usage.InputTokens
			}

			// Update usage and stop reason from message_delta
			if event.Type == "message_delta" {
				if event.Delta != nil && event.Delta.StopReason != nil {
					stopReason = convertAnthropicStopReason(*event.Delta.StopReason)
				}
				if event.Usage != nil {
					usage.OutputTokens = event.Usage.OutputTokens
				}
			}

			// Update usage from message_stop metrics
			if event.Type == "message_stop" && event.Metrics != nil {
				if usage.InputTokens == 0 {
					usage.InputTokens = event.Metrics.InputTokenCount
				}
				if usage.OutputTokens == 0 {
					usage.OutputTokens = event.Metrics.OutputTokenCount
				}
			}

			// Send each response
			for _, response := range responses {
				if response != nil {
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: model,
						ChunkIndex:     chunkIndex,
					}
					chunkIndex++
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
				}
			}
		}
	}

	// Calculate total tokens
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens

	// Emit final events: content_part.done, output_item.done, response.completed
	finalResponses := emitFinalResponseEvents(state, stopReason, usage, providerName, model, startTime)
	for _, response := range finalResponses {
		if response != nil {
			response.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ResponsesStreamRequest,
				Provider:       providerName,
				ModelRequested: model,
				ChunkIndex:     chunkIndex,
				Latency:        time.Since(startTime).Milliseconds(),
			}
			chunkIndex++
			if response.Type == schemas.ResponsesStreamResponseTypeCompleted {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
		}
	}
}

// convertBedrockEventToResponses converts a Bedrock stream event to Bifrost Responses stream responses
func convertAnthropicEventToResponses(
	event AnthropicStreamEvent,
	state *BedrockResponsesStreamState,
	providerName schemas.ModelProvider,
	model string,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Emit lifecycle events if not already done (on message_start)
	if event.Type == "message_start" && !state.HasEmittedCreated {
		// Update message ID from the actual message
		if event.Message != nil && event.Message.ID != "" {
			state.MessageID = &event.Message.ID
			state.ItemID = fmt.Sprintf("msg_%s_item_0", event.Message.ID)
		}

		// Emit response.created
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCreated,
			SequenceNumber: state.SequenceNumber,
			Response: &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				Object:    "response",
				CreatedAt: state.CreatedAt,
				Model:     model,
				Status:    schemas.Ptr("in_progress"),
			},
		})
		state.SequenceNumber++
		state.HasEmittedCreated = true

		// Emit response.in_progress
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeInProgress,
			SequenceNumber: state.SequenceNumber,
			Response: &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				Object:    "response",
				CreatedAt: state.CreatedAt,
				Model:     model,
				Status:    schemas.Ptr("in_progress"),
			},
		})
		state.SequenceNumber++
		state.HasEmittedInProgress = true
	}

	// Handle text delta from content_block_delta events
	if event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Text != nil {
		text := *event.Delta.Text
		state.AccumulatedText += text

		// Add output item if not already added
		if !state.TextItemAdded {
			messageType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant

			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				Item: &schemas.ResponsesMessage{
					ID:   &state.ItemID,
					Type: &messageType,
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
		if !state.ContentPartAdded {
			emptyText := ""
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				ContentIndex:   schemas.Ptr(0),
				ItemID:         &state.ItemID,
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
			state.ContentPartAdded = true
		}

		// Emit text delta
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.ItemID,
			Delta:          &text,
		})
		state.SequenceNumber++
	}

	return responses
}

// ConverseResponsesStreamState tracks the state for streaming Responses API via Converse API
type ConverseResponsesStreamState struct {
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

// newConverseResponsesStreamState creates a new stream state for Responses API via Converse
func newConverseResponsesStreamState() *ConverseResponsesStreamState {
	return &ConverseResponsesStreamState{
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
	state := newConverseResponsesStreamState()
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
			var event ConverseStreamEvent
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
						ToolUseID: event.Start.ToolUse.ToolUseId,
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
	var converseResp BedrockConverseResponse
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
				toolUseID := block.ToolUse.ToolUseId
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

// These types are used for the SAP AI Core Converse API which supports native tool calling

// BedrockConverseRequest represents a request to the Bedrock Converse API
// This format is required for native tool calling support on SAP AI Core
type BedrockConverseRequest struct {
	Messages        []ConverseMessage        `json:"messages,omitempty"`
	System          []ConverseSystemMessage  `json:"system,omitempty"`
	InferenceConfig *ConverseInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *ConverseToolConfig      `json:"toolConfig,omitempty"`
}

// ConverseMessage represents a message in Converse API format
type ConverseMessage struct {
	Role    string                 `json:"role"`
	Content []ConverseContentBlock `json:"content"`
}

// ConverseSystemMessage represents a system message in Converse API format
type ConverseSystemMessage struct {
	Text       *string             `json:"text,omitempty"`
	CachePoint *ConverseCachePoint `json:"cachePoint,omitempty"`
}

// ConverseCachePoint represents a cache point for prompt caching
type ConverseCachePoint struct {
	Type string `json:"type"` // "default"
}

// ConverseContentBlock represents a content block in Converse API format
type ConverseContentBlock struct {
	Text       *string              `json:"text,omitempty"`
	Image      *ConverseImageSource `json:"image,omitempty"`
	ToolUse    *ConverseToolUse     `json:"toolUse,omitempty"`
	ToolResult *ConverseToolResult  `json:"toolResult,omitempty"`
	CachePoint *ConverseCachePoint  `json:"cachePoint,omitempty"`
}

// ConverseImageSource represents an image in Converse API format
type ConverseImageSource struct {
	Format string                   `json:"format"` // "png", "jpeg", "gif", "webp"
	Source *ConverseImageSourceData `json:"source"`
}

// ConverseImageSourceData represents image source data
type ConverseImageSourceData struct {
	Bytes string `json:"bytes"` // Base64-encoded image bytes
}

// ConverseToolUse represents a tool use block in Converse API format
type ConverseToolUse struct {
	ToolUseId string      `json:"toolUseId"`
	Name      string      `json:"name"`
	Input     interface{} `json:"input"`
}

// ConverseToolResult represents a tool result in Converse API format
type ConverseToolResult struct {
	ToolUseId string                 `json:"toolUseId"`
	Content   []ConverseContentBlock `json:"content"`
	Status    *string                `json:"status,omitempty"` // "success" or "error"
}

// ConverseInferenceConfig represents inference configuration for Converse API
type ConverseInferenceConfig struct {
	MaxTokens     *int     `json:"maxTokens,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
}

// ConverseToolConfig represents tool configuration for Converse API
type ConverseToolConfig struct {
	Tools      []ConverseTool      `json:"tools,omitempty"`
	ToolChoice *ConverseToolChoice `json:"toolChoice,omitempty"`
}

// ConverseTool represents a tool definition for Converse API
type ConverseTool struct {
	ToolSpec   *ConverseToolSpec   `json:"toolSpec,omitempty"`
	CachePoint *ConverseCachePoint `json:"cachePoint,omitempty"`
}

// ConverseToolSpec represents a tool specification
type ConverseToolSpec struct {
	Name        string                  `json:"name"`
	Description *string                 `json:"description,omitempty"`
	InputSchema ConverseToolInputSchema `json:"inputSchema"`
}

// ConverseToolInputSchema represents the input schema for a tool
type ConverseToolInputSchema struct {
	JSON interface{} `json:"json,omitempty"`
}

// ConverseToolChoice represents tool choice configuration
type ConverseToolChoice struct {
	Auto *struct{} `json:"auto,omitempty"`
	Any  *struct{} `json:"any,omitempty"`
	Tool *struct {
		Name string `json:"name"`
	} `json:"tool,omitempty"`
}

// BedrockConverseResponse represents a non-streaming response from Converse API
type BedrockConverseResponse struct {
	Output     *ConverseOutput  `json:"output"`
	StopReason string           `json:"stopReason"`
	Usage      *ConverseUsage   `json:"usage"`
	Metrics    *ConverseMetrics `json:"metrics,omitempty"`
}

// ConverseOutput represents the output from a Converse response
type ConverseOutput struct {
	Message *ConverseMessage `json:"message,omitempty"`
}

// ConverseUsage represents token usage from Converse API
type ConverseUsage struct {
	InputTokens           int `json:"inputTokens"`
	OutputTokens          int `json:"outputTokens"`
	TotalTokens           int `json:"totalTokens"`
	CacheReadInputTokens  int `json:"cacheReadInputTokens,omitempty"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens,omitempty"`
}

// ConverseMetrics represents response metrics from Converse API
type ConverseMetrics struct {
	LatencyMs int64 `json:"latencyMs"`
}

// ConverseStreamEvent represents a streaming event from Converse API
type ConverseStreamEvent struct {
	// For messageStart events
	Role *string `json:"role,omitempty"`

	// For contentBlockStart events
	ContentBlockIndex *int                       `json:"contentBlockIndex,omitempty"`
	Start             *ConverseContentBlockStart `json:"start,omitempty"`

	// For contentBlockDelta events
	Delta *ConverseContentBlockDelta `json:"delta,omitempty"`

	// For messageStop events
	StopReason *string `json:"stopReason,omitempty"`

	// For metadata events
	Usage   *ConverseUsage   `json:"usage,omitempty"`
	Metrics *ConverseMetrics `json:"metrics,omitempty"`
}

// ConverseContentBlockStart represents the start of a content block
type ConverseContentBlockStart struct {
	ToolUse *ConverseToolUseStart `json:"toolUse,omitempty"`
}

// ConverseToolUseStart represents the start of a tool use block
type ConverseToolUseStart struct {
	ToolUseId string `json:"toolUseId"`
	Name      string `json:"name"`
}

// ConverseContentBlockDelta represents incremental content
type ConverseContentBlockDelta struct {
	Text    *string               `json:"text,omitempty"`
	ToolUse *ConverseToolUseDelta `json:"toolUse,omitempty"`
}

// ConverseToolUseDelta represents incremental tool use content
type ConverseToolUseDelta struct {
	Input string `json:"input"` // Incremental JSON string
}

// convertToBedrockConverse converts a Bifrost chat request to Bedrock Converse API format
// This is required for SAP AI Core to support native tool calling
func convertToBedrockConverse(request *schemas.BifrostChatRequest) *BedrockConverseRequest {
	converseReq := &BedrockConverseRequest{}

	// Get model config for max tokens
	config := GetModelConfig(request.Model)
	maxTokens := config.MaxTokens

	// Convert messages from Input field
	// We need to handle consecutive tool messages specially - they must be merged into a single user message
	var systemMessages []ConverseSystemMessage
	i := 0
	for i < len(request.Input) {
		msg := request.Input[i]

		if msg.Role == schemas.ChatMessageRoleSystem {
			// Extract system message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				systemMessages = append(systemMessages, ConverseSystemMessage{
					Text: msg.Content.ContentStr,
				})
			}
			i++
			continue
		}

		// Handle tool messages - collect ALL consecutive tool messages and merge into a single user message
		if msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil {
			var toolResultBlocks []ConverseContentBlock

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

				toolResultBlocks = append(toolResultBlocks, ConverseContentBlock{
					ToolResult: &ConverseToolResult{
						ToolUseId: toolUseId,
						Content: []ConverseContentBlock{
							{Text: &toolResultContent},
						},
					},
				})
				i++
			}

			// Create a single user message with all tool results
			converseMsg := ConverseMessage{
				Role:    "user", // Bedrock Converse uses user role for tool results
				Content: toolResultBlocks,
			}
			converseReq.Messages = append(converseReq.Messages, converseMsg)
			continue
		}

		converseMsg := ConverseMessage{
			Role: mapRoleForAnthropic(msg.Role),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
				// Only add text block if content is non-empty (Bedrock rejects blank text)
				converseMsg.Content = []ConverseContentBlock{
					{Text: msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil && *block.Text != "" {
						// Only add text block if content is non-empty (Bedrock rejects blank text)
						converseMsg.Content = append(converseMsg.Content, ConverseContentBlock{
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
						converseMsg.Content = append(converseMsg.Content, ConverseContentBlock{
							Image: &ConverseImageSource{
								Format: format,
								Source: &ConverseImageSourceData{
									Bytes: extractBase64Data(block.ImageURLStruct.URL),
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

				converseMsg.Content = append(converseMsg.Content, ConverseContentBlock{
					ToolUse: &ConverseToolUse{
						ToolUseId: toolUseId,
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
	converseReq.InferenceConfig = &ConverseInferenceConfig{
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
			tools := make([]ConverseTool, 0, len(request.Params.Tools))
			for _, tool := range request.Params.Tools {
				if tool.Function == nil {
					continue
				}
				converseTool := ConverseTool{
					ToolSpec: &ConverseToolSpec{
						Name:        tool.Function.Name,
						Description: tool.Function.Description,
					},
				}
				if tool.Function.Parameters != nil {
					// Resolve $ref references in schema - Bedrock Converse doesn't support $ref alongside other fields
					converseTool.ToolSpec.InputSchema = ConverseToolInputSchema{
						JSON: resolveSchemaRefs(tool.Function.Parameters),
					}
				}
				tools = append(tools, converseTool)
			}
			if len(tools) > 0 {
				converseReq.ToolConfig = &ConverseToolConfig{
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
					converseReq.ToolConfig.ToolChoice = &ConverseToolChoice{Auto: &struct{}{}}
				case "required":
					converseReq.ToolConfig.ToolChoice = &ConverseToolChoice{Any: &struct{}{}}
					// "none" is not directly supported by Converse API, omit it
				}
			} else if request.Params.ToolChoice.ChatToolChoiceStruct != nil {
				// Specific tool choice
				if request.Params.ToolChoice.ChatToolChoiceStruct.Function.Name != "" {
					converseReq.ToolConfig.ToolChoice = &ConverseToolChoice{
						Tool: &struct {
							Name string `json:"name"`
						}{
							Name: request.Params.ToolChoice.ChatToolChoiceStruct.Function.Name,
						},
					}
				}
			}
		}
	}

	return converseReq
}

// parseBedrockConverseResponse parses a Bedrock Converse API response into Bifrost format
func parseBedrockConverseResponse(body []byte, model string) (*schemas.BifrostChatResponse, error) {
	var converseResp BedrockConverseResponse
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

				toolUseId := block.ToolUse.ToolUseId
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
				CachedTokens: converseResp.Usage.CacheReadInputTokens,
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

// emitFinalResponseEvents emits the final events to complete the stream
func emitFinalResponseEvents(
	state *BedrockResponsesStreamState,
	stopReason *string,
	usage *schemas.ResponsesResponseUsage,
	providerName schemas.ModelProvider,
	model string,
	startTime time.Time,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Map stop reason
	var mappedStopReason string
	if stopReason != nil {
		mappedStopReason = mapBedrockStopReasonToResponses(*stopReason)
	} else {
		mappedStopReason = "end_turn"
	}

	// Emit output_text.done with full accumulated text
	if state.TextItemAdded {
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.ItemID,
			Text:           &state.AccumulatedText,
		})
		state.SequenceNumber++
	}

	// Emit content_part.done
	if state.ContentPartAdded {
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.ItemID,
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
	}

	// Emit output_item.done
	if state.TextItemAdded {
		messageType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		contentBlockType := schemas.ResponsesOutputMessageContentTypeText

		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			Item: &schemas.ResponsesMessage{
				ID:     &state.ItemID,
				Type:   &messageType,
				Role:   &role,
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: contentBlockType,
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

	// Emit response.completed
	completedAt := int(time.Now().Unix())
	messageType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleAssistant
	contentBlockType := schemas.ResponsesOutputMessageContentTypeText

	var outputMessages []schemas.ResponsesMessage
	if state.TextItemAdded {
		outputMessages = []schemas.ResponsesMessage{
			{
				ID:     &state.ItemID,
				Type:   &messageType,
				Role:   &role,
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: contentBlockType,
							Text: &state.AccumulatedText,
							ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
								LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
								Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							},
						},
					},
				},
			},
		}
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCompleted,
		SequenceNumber: state.SequenceNumber,
		Response: &schemas.BifrostResponsesResponse{
			ID:          state.MessageID,
			Object:      "response",
			CreatedAt:   state.CreatedAt,
			CompletedAt: &completedAt,
			Model:       model,
			Status:      schemas.Ptr("completed"),
			StopReason:  &mappedStopReason,
			Output:      outputMessages,
			Usage:       usage,
		},
	})
	state.SequenceNumber++

	return responses
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
			var event ConverseStreamEvent
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
					toolUseId := event.Start.ToolUse.ToolUseId
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
							CachedTokens: event.Usage.CacheReadInputTokens,
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
