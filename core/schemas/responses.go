package schemas

import (
	"fmt"
	"maps"

	"github.com/bytedance/sonic"
)

// =============================================================================
// OPENAI RESPONSES API SCHEMAS
// =============================================================================
//
// This file contains all the schema definitions for the OpenAI Responses API.
//
// Structure:
// 1. Core API Request/Response Structures
// 2. Input Message Structures
// 3. Output Message Structures
// 4. Tool Call Structures (organized by tool type)
// 5. Tool Configuration Structures
// 6. Tool Choice Configuration
//
// Union Types:
// - Many structs use "union types" where only one field should be set
// - These are implemented with pointer fields and custom JSON marshaling
// =============================================================================

// =============================================================================
// 1. CORE API REQUEST/RESPONSE STRUCTURES
// =============================================================================

type ResponsesParameters struct {
	Background         *bool                         `json:"background,omitempty"`
	Conversation       *string                       `json:"conversation,omitempty"`
	Include            []string                      `json:"include,omitempty"` // Supported values: "web_search_call.action.sources", "code_interpreter_call.outputs", "computer_call_output.output.image_url", "file_search_call.results", "message.input_image.image_url", "message.output_text.logprobs", "reasoning.encrypted_content"
	Instructions       *string                       `json:"instructions,omitempty"`
	MaxOutputTokens    *int                          `json:"max_output_tokens,omitempty"`
	MaxToolCalls       *int                          `json:"max_tool_calls,omitempty"`
	Metadata           *map[string]any               `json:"metadata,omitempty"`
	ParallelToolCalls  *bool                         `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID *string                       `json:"previous_response_id,omitempty"`
	PromptCacheKey     *string                       `json:"prompt_cache_key,omitempty"`  // Prompt cache key
	Reasoning          *ResponsesParametersReasoning `json:"reasoning,omitempty"`         // Configuration options for reasoning models
	SafetyIdentifier   *string                       `json:"safety_identifier,omitempty"` // Safety identifier
	ServiceTier        *string                       `json:"service_tier,omitempty"`
	StreamOptions      *ResponsesStreamOptions       `json:"stream_options,omitempty"`
	Store              *bool                         `json:"store,omitempty"`
	Temperature        *float64                      `json:"temperature,omitempty"`
	Text               *ResponsesTextConfig          `json:"text,omitempty"`
	TopLogProbs        *int                          `json:"top_logprobs,omitempty"`
	TopP               *float64                      `json:"top_p,omitempty"`       // Controls diversity via nucleus sampling
	ToolChoice         *ResponsesToolChoice          `json:"tool_choice,omitempty"` // Whether to call a tool
	Tools              []ResponsesTool               `json:"tools,omitempty"`       // Tools to use
	Truncation         *string                       `json:"truncation,omitempty"`

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

type ResponsesStreamOptions struct {
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
}

type ResponsesTextConfig struct {
	Format    *ResponsesTextConfigFormat `json:"format,omitempty"`    // An object specifying the format that the model must output
	Verbosity *string                    `json:"verbosity,omitempty"` // "low" | "medium" | "high" or null
}

type ResponsesTextConfigFormat struct {
	Type       string                               `json:"type"`                  // "text" | "json_schema" | "json_object"
	JSONSchema *ResponsesTextConfigFormatJSONSchema `json:"json_schema,omitempty"` // when type == "json_schema"
}

// ResponsesTextConfigFormatJSONSchema represents a JSON schema specification
type ResponsesTextConfigFormatJSONSchema struct {
	Name        string         `json:"name"`
	Schema      map[string]any `json:"schema"` // JSON Schema (subset)
	Type        string         `json:"type"`   // always "json_schema"
	Description *string        `json:"description,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

type ResponsesResponse struct {
	Background         *bool                          `json:"background,omitempty"`
	Conversation       *ResponsesResponseConversation `json:"conversation,omitempty"`
	Include            []string                       `json:"include,omitempty"` // Supported values: "web_search_call.action.sources", "code_interpreter_call.outputs", "computer_call_output.output.image_url", "file_search_call.results", "message.input_image.image_url", "message.output_text.logprobs", "reasoning.encrypted_content"
	Instructions       *ResponsesResponseInstructions `json:"instructions,omitempty"`
	MaxOutputTokens    *int                           `json:"max_output_tokens,omitempty"`
	MaxToolCalls       *int                           `json:"max_tool_calls,omitempty"`
	Metadata           *map[string]any                `json:"metadata,omitempty"`
	ParallelToolCalls  *bool                          `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID *string                        `json:"previous_response_id,omitempty"`
	PromptCacheKey     *string                        `json:"prompt_cache_key,omitempty"`  // Prompt cache key
	Reasoning          *ResponsesParametersReasoning  `json:"reasoning,omitempty"`         // Configuration options for reasoning models
	SafetyIdentifier   *string                        `json:"safety_identifier,omitempty"` // Safety identifier
	ServiceTier        *string                        `json:"service_tier,omitempty"`
	StreamOptions      *ResponsesStreamOptions        `json:"stream_options,omitempty"`
	Store              *bool                          `json:"store,omitempty"`
	Temperature        *float64                       `json:"temperature,omitempty"`
	Text               *ResponsesTextConfig           `json:"text,omitempty"`
	TopLogProbs        *int                           `json:"top_logprobs,omitempty"`
	TopP               *float64                       `json:"top_p,omitempty"`       // Controls diversity via nucleus sampling
	ToolChoice         *ResponsesToolChoice           `json:"tool_choice,omitempty"` // Whether to call a tool
	Tools              []ResponsesTool                `json:"tools,omitempty"`       // Tools to use
	Truncation         *string                        `json:"truncation,omitempty"`

	CreatedAt         int                                 `json:"created_at"`                   // Unix timestamp when Response was created
	IncompleteDetails *ResponsesResponseIncompleteDetails `json:"incomplete_details,omitempty"` // Details about why the response is incomplete
	Output            []ResponsesMessage                  `json:"output,omitempty"`
	Prompt            *ResponsesPrompt                    `json:"prompt,omitempty"` // Reference to a prompt template and variables
}

type ResponsesResponseConversation struct {
	ResponsesResponseConversationStr    *string
	ResponsesResponseConversationStruct *ResponsesResponseConversationStruct
}

// MarshalJSON implements custom JSON marshalling for ResponsesMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rc ResponsesResponseConversation) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rc.ResponsesResponseConversationStr != nil && rc.ResponsesResponseConversationStruct != nil {
		return nil, fmt.Errorf("both ResponsesResponseConversationStr and ResponsesResponseConversationStruct are set; only one should be non-nil")
	}

	if rc.ResponsesResponseConversationStr != nil {
		return sonic.Marshal(*rc.ResponsesResponseConversationStr)
	}
	if rc.ResponsesResponseConversationStruct != nil {
		return sonic.Marshal(rc.ResponsesResponseConversationStruct)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesResponseConversation) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rc.ResponsesResponseConversationStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var structContent ResponsesResponseConversationStruct
	if err := sonic.Unmarshal(data, &structContent); err == nil {
		rc.ResponsesResponseConversationStruct = &structContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor a struct")
}

type ResponsesResponseInstructions struct {
	ResponsesResponseInstructionsStr   *string
	ResponsesResponseInstructionsArray []ResponsesMessage
}

// MarshalJSON implements custom JSON marshalling for ResponsesMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rc ResponsesResponseInstructions) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rc.ResponsesResponseInstructionsStr != nil && rc.ResponsesResponseInstructionsArray != nil {
		return nil, fmt.Errorf("both ResponsesMessageContentStr and ResponsesMessageContentBlocks are set; only one should be non-nil")
	}

	if rc.ResponsesResponseInstructionsStr != nil {
		return sonic.Marshal(*rc.ResponsesResponseInstructionsStr)
	}
	if rc.ResponsesResponseInstructionsArray != nil {
		return sonic.Marshal(rc.ResponsesResponseInstructionsArray)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesResponseInstructions) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rc.ResponsesResponseInstructionsStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessage
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		rc.ResponsesResponseInstructionsArray = arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Messages")
}

type ResponsesPrompt struct {
	ID        string         `json:"id"`
	Variables map[string]any `json:"variables"`
	Version   *string        `json:"version,omitempty"`
}

type ResponsesParametersReasoning struct {
	Effort          *string `json:"effort,omitempty"`           // "minimal" | "low" | "medium" | "high"
	GenerateSummary *string `json:"generate_summary,omitempty"` // Deprecated: use summary instead
	Summary         *string `json:"summary,omitempty"`          // "auto" | "concise" | "detailed"
}

type ResponsesResponseConversationStruct struct {
	ID string `json:"id"` // The unique ID of the conversation
}

type ResponsesResponseError struct {
	Code    string `json:"code"`    // The error code for the response
	Message string `json:"message"` // A human-readable description of the error
}

type ResponsesResponseIncompleteDetails struct {
	Reason string `json:"reason"` // The reason why the response is incomplete
}

type ResponsesExtendedResponseUsage struct {
	InputTokens         int                            `json:"input_tokens"`          // Number of input tokens
	InputTokensDetails  *ResponsesResponseInputTokens  `json:"input_tokens_details"`  // Detailed breakdown of input tokens
	OutputTokens        int                            `json:"output_tokens"`         // Number of output tokens
	OutputTokensDetails *ResponsesResponseOutputTokens `json:"output_tokens_details"` // Detailed breakdown of output tokens
}

type ResponsesResponseUsage struct {
	*ResponsesExtendedResponseUsage
	TotalTokens int `json:"total_tokens"` // Total number of tokens used
}

type ResponsesResponseInputTokens struct {
	CachedTokens int `json:"cached_tokens"` // Tokens retrieved from cache
}

type ResponsesResponseOutputTokens struct {
	ReasoningTokens int `json:"reasoning_tokens"` // Number of reasoning tokens
}

// =============================================================================
// 2. INPUT MESSAGE STRUCTURES
// =============================================================================

type ResponsesMessageType string

const (
	ResponsesMessageTypeMessage              ResponsesMessageType = "message"
	ResponsesMessageTypeFileSearchCall       ResponsesMessageType = "file_search_call"
	ResponsesMessageTypeComputerCall         ResponsesMessageType = "computer_call"
	ResponsesMessageTypeComputerCallOutput   ResponsesMessageType = "computer_call_output"
	ResponsesMessageTypeWebSearchCall        ResponsesMessageType = "web_search_call"
	ResponsesMessageTypeFunctionCall         ResponsesMessageType = "function_call"
	ResponsesMessageTypeFunctionCallOutput   ResponsesMessageType = "function_call_output"
	ResponsesMessageTypeCodeInterpreterCall  ResponsesMessageType = "code_interpreter_call"
	ResponsesMessageTypeLocalShellCall       ResponsesMessageType = "local_shell_call"
	ResponsesMessageTypeLocalShellCallOutput ResponsesMessageType = "local_shell_call_output"
	ResponsesMessageTypeMCPCall              ResponsesMessageType = "mcp_call"
	ResponsesMessageTypeCustomToolCall       ResponsesMessageType = "custom_tool_call"
	ResponsesMessageTypeCustomToolCallOutput ResponsesMessageType = "custom_tool_call_output"
	ResponsesMessageTypeImageGenerationCall  ResponsesMessageType = "image_generation_call"
	ResponsesMessageTypeMCPListTools         ResponsesMessageType = "mcp_list_tools"
	ResponsesMessageTypeMCPApprovalRequest   ResponsesMessageType = "mcp_approval_request"
	ResponsesMessageTypeMCPApprovalResponses ResponsesMessageType = "mcp_approval_responses"
	ResponsesMessageTypeReasoning            ResponsesMessageType = "reasoning"
	ResponsesMessageTypeItemReference        ResponsesMessageType = "item_reference"
	ResponsesMessageTypeRefusal              ResponsesMessageType = "refusal"
)

// ResponsesMessage is a union type that can contain different types of input items
// Only one of the fields should be set at a time
type ResponsesMessage struct {
	ID     *string               `json:"id,omitempty"` // Common ID field for most item types
	Type   *ResponsesMessageType `json:"type,omitempty"`
	Status *string               `json:"status,omitempty"` // "in_progress" | "completed" | "incomplete" | "interpreting" | "failed"

	Role    *ResponsesMessageRoleType `json:"role,omitempty"`
	Content *ResponsesMessageContent  `json:"content,omitempty"`

	*ResponsesToolMessage // For Tool calls and outputs

	// Reasoning
	*ResponsesReasoning
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessage.
// It handles the embedded pointer fields by initializing them based on the message type.
func (rm *ResponsesMessage) UnmarshalJSON(data []byte) error {
	// First unmarshal into a temporary struct to avoid recursion and get the type
	type tempResponsesMessage struct {
		ID      *string                   `json:"id,omitempty"`
		Type    *ResponsesMessageType     `json:"type,omitempty"`
		Status  *string                   `json:"status,omitempty"`
		Role    *ResponsesMessageRoleType `json:"role,omitempty"`
		Content *ResponsesMessageContent  `json:"content,omitempty"`
	}

	var temp tempResponsesMessage
	if err := sonic.Unmarshal(data, &temp); err != nil {
		return err
	}

	// Assign the basic fields
	rm.ID = temp.ID
	rm.Type = temp.Type
	rm.Status = temp.Status
	rm.Role = temp.Role
	rm.Content = temp.Content

	// Based on the message type, initialize the appropriate embedded struct
	if temp.Type != nil {
		switch *temp.Type {
		case ResponsesMessageTypeFileSearchCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesFileSearchToolCall: &ResponsesFileSearchToolCall{},
			}
		case ResponsesMessageTypeComputerCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesComputerToolCall: &ResponsesComputerToolCall{},
			}
		case ResponsesMessageTypeComputerCallOutput:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesComputerToolCallOutput: &ResponsesComputerToolCallOutput{},
			}
		case ResponsesMessageTypeWebSearchCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesWebSearchToolCall: &ResponsesWebSearchToolCall{},
			}
		case ResponsesMessageTypeFunctionCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{}
		case ResponsesMessageTypeFunctionCallOutput:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesFunctionToolCallOutput: &ResponsesFunctionToolCallOutput{},
			}
		case ResponsesMessageTypeCodeInterpreterCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesCodeInterpreterToolCall: &ResponsesCodeInterpreterToolCall{},
			}
		case ResponsesMessageTypeLocalShellCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesLocalShellCall: &ResponsesLocalShellCall{},
			}
		case ResponsesMessageTypeLocalShellCallOutput:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesLocalShellCallOutput: &ResponsesLocalShellCallOutput{},
			}
		case ResponsesMessageTypeMCPCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesMCPToolCall: &ResponsesMCPToolCall{},
			}
		case ResponsesMessageTypeCustomToolCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesCustomToolCall: &ResponsesCustomToolCall{},
			}
		case ResponsesMessageTypeCustomToolCallOutput:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesCustomToolCallOutput: &ResponsesCustomToolCallOutput{},
			}
		case ResponsesMessageTypeImageGenerationCall:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesImageGenerationCall: &ResponsesImageGenerationCall{},
			}
		case ResponsesMessageTypeMCPListTools:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesMCPListTools: &ResponsesMCPListTools{},
			}
		case ResponsesMessageTypeMCPApprovalRequest:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesMCPApprovalRequest: &ResponsesMCPApprovalRequest{},
			}
		case ResponsesMessageTypeMCPApprovalResponses:
			rm.ResponsesToolMessage = &ResponsesToolMessage{
				ResponsesMCPApprovalResponse: &ResponsesMCPApprovalResponse{},
			}
		case ResponsesMessageTypeReasoning:
			rm.ResponsesReasoning = &ResponsesReasoning{}
		case ResponsesMessageTypeMessage, ResponsesMessageTypeItemReference, ResponsesMessageTypeRefusal:
			// Regular message types, no embedded structs needed
			return nil
		default:
			// Unknown type, try to unmarshal basic tool message fields if present
			rm.ResponsesToolMessage = &ResponsesToolMessage{}
		}

		// Now unmarshal the tool message fields
		if rm.ResponsesToolMessage != nil {
			// First unmarshal basic tool message fields (call_id, name, arguments)
			if err := sonic.Unmarshal(data, rm.ResponsesToolMessage); err != nil {
				return fmt.Errorf("failed to unmarshal tool message: %v", err)
			}

			// Then unmarshal into specific embedded structs based on message type
			switch *temp.Type {
			case ResponsesMessageTypeFileSearchCall:
				if rm.ResponsesToolMessage.ResponsesFileSearchToolCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesFileSearchToolCall); err != nil {
						return fmt.Errorf("failed to unmarshal file search tool call: %v", err)
					}
				}
			case ResponsesMessageTypeComputerCall:
				if rm.ResponsesToolMessage.ResponsesComputerToolCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesComputerToolCall); err != nil {
						return fmt.Errorf("failed to unmarshal computer tool call: %v", err)
					}
				}
			case ResponsesMessageTypeComputerCallOutput:
				if rm.ResponsesToolMessage.ResponsesComputerToolCallOutput != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesComputerToolCallOutput); err != nil {
						return fmt.Errorf("failed to unmarshal computer tool call output: %v", err)
					}
				}
			case ResponsesMessageTypeWebSearchCall:
				if rm.ResponsesToolMessage.ResponsesWebSearchToolCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesWebSearchToolCall); err != nil {
						return fmt.Errorf("failed to unmarshal web search tool call: %v", err)
					}
				}
			case ResponsesMessageTypeFunctionCallOutput:
				if rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput); err != nil {
						return fmt.Errorf("failed to unmarshal function tool call output: %v", err)
					}
				}
			case ResponsesMessageTypeCodeInterpreterCall:
				if rm.ResponsesToolMessage.ResponsesCodeInterpreterToolCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesCodeInterpreterToolCall); err != nil {
						return fmt.Errorf("failed to unmarshal code interpreter tool call: %v", err)
					}
				}
			case ResponsesMessageTypeLocalShellCall:
				if rm.ResponsesToolMessage.ResponsesLocalShellCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesLocalShellCall); err != nil {
						return fmt.Errorf("failed to unmarshal local shell call: %v", err)
					}
				}
			case ResponsesMessageTypeLocalShellCallOutput:
				if rm.ResponsesToolMessage.ResponsesLocalShellCallOutput != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesLocalShellCallOutput); err != nil {
						return fmt.Errorf("failed to unmarshal local shell call output: %v", err)
					}
				}
			case ResponsesMessageTypeMCPCall:
				if rm.ResponsesToolMessage.ResponsesMCPToolCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesMCPToolCall); err != nil {
						return fmt.Errorf("failed to unmarshal MCP tool call: %v", err)
					}
				}
			case ResponsesMessageTypeCustomToolCall:
				if rm.ResponsesToolMessage.ResponsesCustomToolCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesCustomToolCall); err != nil {
						return fmt.Errorf("failed to unmarshal custom tool call: %v", err)
					}
				}
			case ResponsesMessageTypeCustomToolCallOutput:
				if rm.ResponsesToolMessage.ResponsesCustomToolCallOutput != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesCustomToolCallOutput); err != nil {
						return fmt.Errorf("failed to unmarshal custom tool call output: %v", err)
					}
				}
			case ResponsesMessageTypeImageGenerationCall:
				if rm.ResponsesToolMessage.ResponsesImageGenerationCall != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesImageGenerationCall); err != nil {
						return fmt.Errorf("failed to unmarshal image generation call: %v", err)
					}
				}
			case ResponsesMessageTypeMCPListTools:
				if rm.ResponsesToolMessage.ResponsesMCPListTools != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesMCPListTools); err != nil {
						return fmt.Errorf("failed to unmarshal MCP list tools: %v", err)
					}
				}
			case ResponsesMessageTypeMCPApprovalRequest:
				if rm.ResponsesToolMessage.ResponsesMCPApprovalRequest != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesMCPApprovalRequest); err != nil {
						return fmt.Errorf("failed to unmarshal MCP approval request: %v", err)
					}
				}
			case ResponsesMessageTypeMCPApprovalResponses:
				if rm.ResponsesToolMessage.ResponsesMCPApprovalResponse != nil {
					if err := sonic.Unmarshal(data, rm.ResponsesToolMessage.ResponsesMCPApprovalResponse); err != nil {
						return fmt.Errorf("failed to unmarshal MCP approval response: %v", err)
					}
				}
				// Note: ResponsesMessageTypeFunctionCall only needs basic fields (handled above)
			}
		}

		if rm.ResponsesReasoning != nil {
			if err := sonic.Unmarshal(data, rm.ResponsesReasoning); err != nil {
				return fmt.Errorf("failed to unmarshal reasoning: %v", err)
			}
		}
	}

	return nil
}

// MarshalJSON implements custom JSON marshalling for ResponsesMessage.
// It handles the embedded pointer fields by only marshaling non-nil fields.
func (rm ResponsesMessage) MarshalJSON() ([]byte, error) {
	// Start with the base fields
	result := make(map[string]interface{})

	if rm.ID != nil {
		result["id"] = *rm.ID
	}
	if rm.Type != nil {
		result["type"] = *rm.Type
	}
	if rm.Status != nil {
		result["status"] = *rm.Status
	}
	if rm.Role != nil {
		result["role"] = *rm.Role
	}
	if rm.Content != nil {
		result["content"] = rm.Content
	}

	// Add tool message fields if present
	if rm.ResponsesToolMessage != nil {
		toolData, err := sonic.Marshal(rm.ResponsesToolMessage)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tool message: %v", err)
		}

		var toolFields map[string]interface{}
		if err := sonic.Unmarshal(toolData, &toolFields); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool data: %v", err)
		}

		// Merge tool fields into result
		maps.Copy(result, toolFields)
	}

	// Add reasoning fields if present
	if rm.ResponsesReasoning != nil {
		reasoningData, err := sonic.Marshal(rm.ResponsesReasoning)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal reasoning: %v", err)
		}

		var reasoningFields map[string]interface{}
		if err := sonic.Unmarshal(reasoningData, &reasoningFields); err != nil {
			return nil, fmt.Errorf("failed to unmarshal reasoning data: %v", err)
		}

		// Merge reasoning fields into result
		maps.Copy(result, reasoningFields)
	}

	return sonic.Marshal(result)
}

type ResponsesMessageRoleType string

const (
	ResponsesInputMessageRoleAssistant ResponsesMessageRoleType = "assistant"
	ResponsesInputMessageRoleUser      ResponsesMessageRoleType = "user"
	ResponsesInputMessageRoleSystem    ResponsesMessageRoleType = "system"
	ResponsesInputMessageRoleDeveloper ResponsesMessageRoleType = "developer"
)

// ResponsesInputMessageContent is a union type that can be either a string or array of content blocks
type ResponsesMessageContent struct {
	ContentStr    *string                        // Simple text content
	ContentBlocks []ResponsesMessageContentBlock // Rich content with multiple media types
}

// MarshalJSON implements custom JSON marshalling for ResponsesMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rc ResponsesMessageContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rc.ContentStr != nil && rc.ContentBlocks != nil {
		return nil, fmt.Errorf("both ResponsesMessageContentStr and ResponsesMessageContentBlocks are set; only one should be non-nil")
	}

	if rc.ContentStr != nil {
		return sonic.Marshal(*rc.ContentStr)
	}
	if rc.ContentBlocks != nil {
		return sonic.Marshal(rc.ContentBlocks)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesMessageContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessageContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		rc.ContentBlocks = arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Content blocks")
}

type ResponsesMessageContentBlockType string

const (
	ResponsesInputMessageContentBlockTypeText  ResponsesMessageContentBlockType = "input_text"
	ResponsesInputMessageContentBlockTypeImage ResponsesMessageContentBlockType = "input_image"
	ResponsesInputMessageContentBlockTypeFile  ResponsesMessageContentBlockType = "input_file"
	ResponsesInputMessageContentBlockTypeAudio ResponsesMessageContentBlockType = "input_audio"
	ResponsesOutputMessageContentTypeText      ResponsesMessageContentBlockType = "output_text"
	ResponsesOutputMessageContentTypeRefusal   ResponsesMessageContentBlockType = "refusal"
	ResponsesOutputMessageContentTypeReasoning ResponsesMessageContentBlockType = "reasoning_text"
)

// ResponsesMessageContentBlock represents different types of content (text, image, file, audio)
// Only one of the content type fields should be set
type ResponsesMessageContentBlock struct {
	Type   ResponsesMessageContentBlockType `json:"type"`
	FileID *string                          `json:"file_id,omitempty"` // Reference to uploaded file
	Text   *string                          `json:"text,omitempty"`

	*ResponsesInputMessageContentBlockImage
	*ResponsesInputMessageContentBlockFile
	Audio *ResponsesInputMessageContentBlockAudio `json:"input_audio,omitempty"`

	*ResponsesOutputMessageContentText    // Normal text output from the model
	*ResponsesOutputMessageContentRefusal // Model refusal to answer
}

type ResponsesInputMessageContentBlockImage struct {
	ImageURL *string `json:"image_url,omitempty"`
	Detail   *string `json:"detail,omitempty"` // "low" | "high" | "auto"
}

type ResponsesInputMessageContentBlockFile struct {
	FileData *string `json:"file_data,omitempty"` // Base64 encoded file data
	FileURL  *string `json:"file_url,omitempty"`  // Direct URL to file
	Filename *string `json:"filename,omitempty"`  // Name of the file
}

type ResponsesInputMessageContentBlockAudio struct {
	Format string `json:"format"` // "mp3" or "wav"
	Data   string `json:"data"`   // base64 encoded audio data
}

// =============================================================================
// 3. OUTPUT MESSAGE STRUCTURES
// =============================================================================

type ResponsesOutputMessageContentText struct {
	Annotations []ResponsesOutputMessageContentTextAnnotation `json:"annotations,omitempty"` // Citations and references
	LogProbs    []ResponsesOutputMessageContentTextLogProb    `json:"logprobs,omitempty"`    // Token log probabilities
}

type ResponsesOutputMessageContentTextAnnotation struct {
	Type        string  `json:"type"`                  // "file_citation" | "url_citation" | "container_file_citation" | "file_path"
	Index       *int    `json:"index,omitempty"`       // Common index field (FileCitation, FilePath)
	FileID      *string `json:"file_id,omitempty"`     // Common file ID field (FileCitation, ContainerFileCitation, FilePath)
	StartIndex  *int    `json:"start_index,omitempty"` // Common start index field (URLCitation, ContainerFileCitation)
	EndIndex    *int    `json:"end_index,omitempty"`   // Common end index field (URLCitation, ContainerFileCitation)
	Filename    *string `json:"filename,omitempty"`
	Title       *string `json:"title,omitempty"`
	URL         *string `json:"url,omitempty"`
	ContainerID *string `json:"container_id,omitempty"`
}

// ResponsesOutputMessageContentTextLogProb represents log probability information for content.
type ResponsesOutputMessageContentTextLogProb struct {
	Bytes       []int     `json:"bytes"`
	LogProb     float64   `json:"logprob"`
	Token       string    `json:"token"`
	TopLogProbs []LogProb `json:"top_logprobs"`
}
type ResponsesOutputMessageContentRefusal struct {
	Refusal string `json:"refusal"`
}

type ResponsesToolMessage struct {
	CallID    *string `json:"call_id,omitempty"` // Common call ID for tool calls and outputs
	Name      *string `json:"name,omitempty"`    // Common name field for tool calls
	Arguments *string `json:"arguments,omitempty"`

	// Tool calls and outputs
	*ResponsesFileSearchToolCall
	*ResponsesComputerToolCall
	*ResponsesComputerToolCallOutput
	*ResponsesWebSearchToolCall
	*ResponsesFunctionToolCallOutput
	*ResponsesCodeInterpreterToolCall
	*ResponsesLocalShellCall
	*ResponsesLocalShellCallOutput
	*ResponsesMCPToolCall
	*ResponsesCustomToolCall
	*ResponsesCustomToolCallOutput
	*ResponsesImageGenerationCall

	// MCP-specific
	*ResponsesMCPListTools
	*ResponsesMCPApprovalRequest
	*ResponsesMCPApprovalResponse
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesToolMessage.
// This prevents embedded pointer fields from interfering with basic field unmarshaling.
func (rtm *ResponsesToolMessage) UnmarshalJSON(data []byte) error {
	// Use a simple struct to unmarshal basic fields without embedded interference
	type basicToolMessage struct {
		CallID    *string `json:"call_id,omitempty"`
		Name      *string `json:"name,omitempty"`
		Arguments *string `json:"arguments,omitempty"`
	}

	var basic basicToolMessage
	if err := sonic.Unmarshal(data, &basic); err != nil {
		return err
	}

	// Assign the basic fields
	rtm.CallID = basic.CallID
	rtm.Name = basic.Name
	rtm.Arguments = basic.Arguments

	// Embedded field unmarshaling is handled by the parent ResponsesMessage.UnmarshalJSON
	// based on the message type - no need to duplicate logic here

	return nil
}

// MarshalJSON implements custom JSON marshalling for ResponsesToolMessage.
// It only marshals the basic fields and skips nil embedded pointers to prevent auto-generated
// marshalling from dereferencing them. The parent ResponsesMessage.MarshalJSON already handles
// merging embedded struct fields using the same pattern.
func (rtm ResponsesToolMessage) MarshalJSON() ([]byte, error) {
	result := make(map[string]interface{})

	// Only marshal basic fields
	if rtm.CallID != nil {
		result["call_id"] = *rtm.CallID
	}
	if rtm.Name != nil {
		result["name"] = *rtm.Name
	}
	if rtm.Arguments != nil {
		result["arguments"] = *rtm.Arguments
	}

	// Helper to marshal and merge embedded struct
	mergeEmbedded := func(v interface{}) error {
		data, err := sonic.Marshal(v)
		if err != nil {
			return err
		}
		var fields map[string]interface{}
		if err := sonic.Unmarshal(data, &fields); err != nil {
			return err
		}
		maps.Copy(result, fields)
		return nil
	}

	// Marshal each embedded pointer field only if non-nil
	// Note: We check each field explicitly because nil pointers in interface{} don't compare to nil
	if rtm.ResponsesFileSearchToolCall != nil {
		if err := mergeEmbedded(rtm.ResponsesFileSearchToolCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesComputerToolCall != nil {
		if err := mergeEmbedded(rtm.ResponsesComputerToolCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesComputerToolCallOutput != nil {
		if err := mergeEmbedded(rtm.ResponsesComputerToolCallOutput); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesWebSearchToolCall != nil {
		if err := mergeEmbedded(rtm.ResponsesWebSearchToolCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesFunctionToolCallOutput != nil {
		// Special case: ResponsesFunctionToolCallOutput marshals to a raw value (string or array),
		// not an object, so we need to add it as an "output" field directly
		outputData, err := sonic.Marshal(rtm.ResponsesFunctionToolCallOutput)
		if err != nil {
			return nil, err
		}
		var output interface{}
		if err := sonic.Unmarshal(outputData, &output); err != nil {
			return nil, err
		}
		result["output"] = output
	}
	if rtm.ResponsesCodeInterpreterToolCall != nil {
		if err := mergeEmbedded(rtm.ResponsesCodeInterpreterToolCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesLocalShellCall != nil {
		if err := mergeEmbedded(rtm.ResponsesLocalShellCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesLocalShellCallOutput != nil {
		if err := mergeEmbedded(rtm.ResponsesLocalShellCallOutput); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesMCPToolCall != nil {
		if err := mergeEmbedded(rtm.ResponsesMCPToolCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesCustomToolCall != nil {
		if err := mergeEmbedded(rtm.ResponsesCustomToolCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesCustomToolCallOutput != nil {
		if err := mergeEmbedded(rtm.ResponsesCustomToolCallOutput); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesImageGenerationCall != nil {
		if err := mergeEmbedded(rtm.ResponsesImageGenerationCall); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesMCPListTools != nil {
		if err := mergeEmbedded(rtm.ResponsesMCPListTools); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesMCPApprovalRequest != nil {
		if err := mergeEmbedded(rtm.ResponsesMCPApprovalRequest); err != nil {
			return nil, err
		}
	}
	if rtm.ResponsesMCPApprovalResponse != nil {
		if err := mergeEmbedded(rtm.ResponsesMCPApprovalResponse); err != nil {
			return nil, err
		}
	}

	return sonic.Marshal(result)
}

// =============================================================================
// 4. TOOL CALL STRUCTURES (organized by tool type)
// =============================================================================

// -----------------------------------------------------------------------------
// File Search Tool
// -----------------------------------------------------------------------------

type ResponsesFileSearchToolCall struct {
	Queries []string                            `json:"queries"`
	Results []ResponsesFileSearchToolCallResult `json:"results,omitempty"`
}

type ResponsesFileSearchToolCallResult struct {
	Attributes *map[string]any `json:"attributes,omitempty"`
	FileID     *string         `json:"file_id,omitempty"`
	Filename   *string         `json:"filename,omitempty"`
	Score      *float64        `json:"score,omitempty"`
	Text       *string         `json:"text,omitempty"`
}

// -----------------------------------------------------------------------------
// Computer Tool
// -----------------------------------------------------------------------------
type ResponsesComputerToolCall struct {
	Action              ResponsesComputerToolCallAction               `json:"action"`
	PendingSafetyChecks []ResponsesComputerToolCallPendingSafetyCheck `json:"pending_safety_checks"`
}

type ResponsesComputerToolCallPendingSafetyCheck struct {
	ID      string `json:"id"`
	Context string `json:"context"`
	Message string `json:"message"`
}

// ComputerAction represents the different types of computer actions
type ResponsesComputerToolCallAction struct {
	Type    string                                `json:"type"`             // "click" | "double_click" | "drag" | "keypress" | "move" | "screenshot" | "scroll" | "type" | "wait"
	X       *int                                  `json:"x,omitempty"`      // Common X coordinate field (Click, DoubleClick, Move, Scroll)
	Y       *int                                  `json:"y,omitempty"`      // Common Y coordinate field (Click, DoubleClick, Move, Scroll)
	Button  *string                               `json:"button,omitempty"` // "left" | "right" | "wheel" | "back" | "forward"
	Path    []ResponsesComputerToolCallActionPath `json:"path,omitempty"`
	Keys    []string                              `json:"keys,omitempty"`
	ScrollX *int                                  `json:"scroll_x,omitempty"`
	ScrollY *int                                  `json:"scroll_y,omitempty"`
	Text    *string                               `json:"text,omitempty"`
}

type ResponsesComputerToolCallActionPath struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Computer Tool Call Output - contains the results from executing a computer tool call
type ResponsesComputerToolCallOutput struct {
	Output                   ResponsesComputerToolCallOutputData                `json:"output"`
	AcknowledgedSafetyChecks []ResponsesComputerToolCallAcknowledgedSafetyCheck `json:"acknowledged_safety_checks,omitempty"`
}

// ComputerToolCallOutputData - A computer screenshot image used with the computer use tool
type ResponsesComputerToolCallOutputData struct {
	Type     string  `json:"type"` // always "computer_screenshot"
	FileID   *string `json:"file_id,omitempty"`
	ImageURL *string `json:"image_url,omitempty"`
}

// AcknowledgedSafetyCheck - The safety checks reported by the API that have been acknowledged by the developer
type ResponsesComputerToolCallAcknowledgedSafetyCheck struct {
	ID      string  `json:"id"`
	Code    *string `json:"code,omitempty"`
	Message *string `json:"message,omitempty"`
}

// -----------------------------------------------------------------------------
// Web Search Tool
// -----------------------------------------------------------------------------
type ResponsesWebSearchToolCall struct {
	Action ResponsesWebSearchAction `json:"action"`
}

// WebSearchAction represents the different types of web search actions
type ResponsesWebSearchAction struct {
	Type    string                                 `json:"type"`          // "search" | "open_page" | "find"
	URL     *string                                `json:"url,omitempty"` // Common URL field (OpenPage, Find)
	Query   *string                                `json:"query,omitempty"`
	Sources []ResponsesWebSearchActionSearchSource `json:"sources,omitempty"`
	Pattern *string                                `json:"pattern,omitempty"`
}

// WebSearchSource - The sources used in the search
type ResponsesWebSearchActionSearchSource struct {
	Type string `json:"type"` // always "url"
	URL  string `json:"url"`
}

// -----------------------------------------------------------------------------
// Function Tool
// -----------------------------------------------------------------------------

// Function Tool Call Output - contains the results from executing a function tool call
type ResponsesFunctionToolCallOutput struct {
	ResponsesFunctionToolCallOutputStr    *string //A JSON string of the output of the function tool call.
	ResponsesFunctionToolCallOutputBlocks []ResponsesMessageContentBlock
}

// MarshalJSON implements custom JSON marshalling for ResponsesFunctionToolCallOutput.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rf ResponsesFunctionToolCallOutput) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rf.ResponsesFunctionToolCallOutputStr != nil && rf.ResponsesFunctionToolCallOutputBlocks != nil {
		return nil, fmt.Errorf("both ResponsesFunctionToolCallOutputStr and ResponsesFunctionToolCallOutputBlocks are set; only one should be non-nil")
	}

	if rf.ResponsesFunctionToolCallOutputStr != nil {
		return sonic.Marshal(*rf.ResponsesFunctionToolCallOutputStr)
	}
	if rf.ResponsesFunctionToolCallOutputBlocks != nil {
		return sonic.Marshal(rf.ResponsesFunctionToolCallOutputBlocks)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesFunctionToolCallOutput.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rf *ResponsesFunctionToolCallOutput) UnmarshalJSON(data []byte) error {
	// Parse as generic object to check if it contains content-like fields
	var genericObj map[string]interface{}
	if err := sonic.Unmarshal(data, &genericObj); err != nil {
		return err
	}

	// If the object doesn't contain typical content fields, it's probably not meant for this struct
	// (e.g., it's a tool call, not a tool call output)
	hasContentFields := false
	for key := range genericObj {
		if key == "content" || key == "output" || key == "result" {
			hasContentFields = true
			break
		}
	}

	if !hasContentFields {
		return nil // Skip unmarshaling if no relevant content fields
	}

	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rf.ResponsesFunctionToolCallOutputStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessageContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		rf.ResponsesFunctionToolCallOutputBlocks = arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Content blocks")
}

// -----------------------------------------------------------------------------
// Reasoning
// -----------------------------------------------------------------------------

type ResponsesReasoning struct {
	Summary          []ResponsesReasoningContent `json:"summary"`
	EncryptedContent *string                     `json:"encrypted_content,omitempty"`
}

type ResponsesReasoningContentBlockType string

const (
	ResponsesReasoningContentBlockTypeSummaryText ResponsesReasoningContentBlockType = "summary_text"
)

type ResponsesReasoningContent struct {
	Type ResponsesReasoningContentBlockType `json:"type"`
	Text string                             `json:"text"`
}

// -----------------------------------------------------------------------------
// Image Generation Tool
// -----------------------------------------------------------------------------
type ResponsesImageGenerationCall struct {
	Result string `json:"result"`
}

// -----------------------------------------------------------------------------
// Code Interpreter Tool
// -----------------------------------------------------------------------------
type ResponsesCodeInterpreterToolCall struct {
	Code        *string                          `json:"code"`         // The code to run, or null if not available
	ContainerID string                           `json:"container_id"` // The ID of the container used to run the code
	Outputs     []ResponsesCodeInterpreterOutput `json:"outputs"`      // The outputs generated by the code interpreter, can be null
}

// CodeInterpreterOutput represents the different types of code interpreter outputs
type ResponsesCodeInterpreterOutput struct {
	*ResponsesCodeInterpreterOutputLogs
	*ResponsesCodeInterpreterOutputImage
}

// MarshalJSON implements custom JSON marshaling for ResponsesCodeInterpreterOutput
func (o ResponsesCodeInterpreterOutput) MarshalJSON() ([]byte, error) {
	// Error if both variants are set
	if o.ResponsesCodeInterpreterOutputLogs != nil && o.ResponsesCodeInterpreterOutputImage != nil {
		return nil, fmt.Errorf("ResponsesCodeInterpreterOutput cannot have both Logs and Image set")
	}

	// Marshal whichever one is present
	if o.ResponsesCodeInterpreterOutputLogs != nil {
		return sonic.Marshal(o.ResponsesCodeInterpreterOutputLogs)
	}
	if o.ResponsesCodeInterpreterOutputImage != nil {
		return sonic.Marshal(o.ResponsesCodeInterpreterOutputImage)
	}

	// Return null if neither is set
	return []byte("null"), nil
}

// UnmarshalJSON implements custom JSON unmarshaling for ResponsesCodeInterpreterOutput
func (o *ResponsesCodeInterpreterOutput) UnmarshalJSON(data []byte) error {
	// Handle null case
	if string(data) == "null" {
		return nil
	}

	// First, peek at the type field to determine which variant to unmarshal
	var typeStruct struct {
		Type string `json:"type"`
	}
	if err := sonic.Unmarshal(data, &typeStruct); err != nil {
		return fmt.Errorf("failed to read type field: %w", err)
	}

	// Unmarshal into the appropriate concrete type based on the type field
	switch typeStruct.Type {
	case "logs":
		var logs ResponsesCodeInterpreterOutputLogs
		if err := sonic.Unmarshal(data, &logs); err != nil {
			return fmt.Errorf("failed to unmarshal logs output: %w", err)
		}
		o.ResponsesCodeInterpreterOutputLogs = &logs
		o.ResponsesCodeInterpreterOutputImage = nil
		return nil

	case "image":
		var image ResponsesCodeInterpreterOutputImage
		if err := sonic.Unmarshal(data, &image); err != nil {
			return fmt.Errorf("failed to unmarshal image output: %w", err)
		}
		o.ResponsesCodeInterpreterOutputImage = &image
		o.ResponsesCodeInterpreterOutputLogs = nil
		return nil

	default:
		return fmt.Errorf("unknown ResponsesCodeInterpreterOutput type: %s", typeStruct.Type)
	}
}

// CodeInterpreterOutputLogs - The logs output from the code interpreter
type ResponsesCodeInterpreterOutputLogs struct {
	Logs string `json:"logs"`
	Type string `json:"type"` // always "logs"
}

// CodeInterpreterOutputImage - The image output from the code interpreter
type ResponsesCodeInterpreterOutputImage struct {
	Type string `json:"type"` // always "image"
	URL  string `json:"url"`
}

// -----------------------------------------------------------------------------
// Local Shell Tool
// -----------------------------------------------------------------------------
type ResponsesLocalShellCall struct {
	Action ResponsesLocalShellCallAction `json:"action"`
}

type ResponsesLocalShellCallAction struct {
	Command          []string `json:"command"`
	Env              []string `json:"env"`
	Type             string   `json:"type"` // always "exec"
	TimeoutMS        *int     `json:"timeout_ms,omitempty"`
	User             *string  `json:"user,omitempty"`
	WorkingDirectory *string  `json:"working_directory,omitempty"`
}

type ResponsesLocalShellCallOutput struct {
	Output string `json:"output"`
}

// -----------------------------------------------------------------------------
// MCP (Model Context Protocol) Tools
// -----------------------------------------------------------------------------
type ResponsesMCPListTools struct {
	ServerLabel string             `json:"server_label"`
	Tools       []ResponsesMCPTool `json:"tools"`
	Error       *string            `json:"error,omitempty"`
}

type ResponsesMCPTool struct {
	Name        string          `json:"name"`
	InputSchema map[string]any  `json:"input_schema"`
	Description *string         `json:"description,omitempty"`
	Annotations *map[string]any `json:"annotations,omitempty"`
}

// MCP Approval Request - requests approval for a specific action within MCP
type ResponsesMCPApprovalRequest struct {
	Action ResponsesMCPApprovalRequestAction `json:"action"`
}

type ResponsesMCPApprovalRequestAction struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // always "mcp_approval_request"
	Name        string `json:"name"`
	ServerLabel string `json:"server_label"`
	Arguments   string `json:"arguments"`
}

// MCP Approval Response - contains the response to an approval request within MCP
type ResponsesMCPApprovalResponse struct {
	ApprovalResponseID string  `json:"approval_response_id"`
	Approve            bool    `json:"approve"`
	Reason             *string `json:"reason,omitempty"`
}

// MCP Tool Call - an invocation of a tool on an MCP server
type ResponsesMCPToolCall struct {
	ServerLabel string  `json:"server_label"`     // The label of the MCP server running the tool
	Error       *string `json:"error,omitempty"`  // The error from the tool call, if any
	Output      *string `json:"output,omitempty"` // The output from the tool call
}

// -----------------------------------------------------------------------------
// Custom Tools
// -----------------------------------------------------------------------------
type ResponsesCustomToolCallOutput struct {
	Output string `json:"output"` // The output from the custom tool call generated by your code
}

// Custom Tool Call - a call to a custom tool created by the model
type ResponsesCustomToolCall struct {
	Input string `json:"input"` // The input for the custom tool call generated by the model
}

// =============================================================================
// 5. TOOL CHOICE CONFIGURATION
// =============================================================================

// Combined tool choices for all providers, make sure to check the provider's
// documentation to see which tool choices are supported.
type ResponsesToolChoiceType string

const (
	// ResponsesToolChoiceTypeNone means no tool should be called
	ResponsesToolChoiceTypeNone ResponsesToolChoiceType = "none"
	// ResponsesToolChoiceTypeAuto means an automatic tool should be called
	ResponsesToolChoiceTypeAuto ResponsesToolChoiceType = "auto"
	// ResponsesToolChoiceTypeAny means any tool can be called
	ResponsesToolChoiceTypeAny ResponsesToolChoiceType = "any"
	// ResponsesToolChoiceTypeRequired means a specific tool must be called
	ResponsesToolChoiceTypeRequired ResponsesToolChoiceType = "required"
	// ResponsesToolChoiceTypeFunction means a specific tool must be called
	ResponsesToolChoiceTypeFunction ResponsesToolChoiceType = "function"
	// ResponsesToolChoiceTypeAllowedTools means a specific tool must be called
	ResponsesToolChoiceTypeAllowedTools ResponsesToolChoiceType = "allowed_tools"
	// ResponsesToolChoiceTypeFileSearch means a file search tool must be called
	ResponsesToolChoiceTypeFileSearch ResponsesToolChoiceType = "file_search"
	// ResponsesToolChoiceTypeWebSearchPreview means a web search preview tool must be called
	ResponsesToolChoiceTypeWebSearchPreview ResponsesToolChoiceType = "web_search_preview"
	// ResponsesToolChoiceTypeComputerUsePreview means a computer use preview tool must be called
	ResponsesToolChoiceTypeComputerUsePreview ResponsesToolChoiceType = "computer_use_preview"
	// ResponsesToolChoiceTypeCodeInterpreter means a code interpreter tool must be called
	ResponsesToolChoiceTypeCodeInterpreter ResponsesToolChoiceType = "code_interpreter"
	// ResponsesToolChoiceTypeImageGeneration means an image generation tool must be called
	ResponsesToolChoiceTypeImageGeneration ResponsesToolChoiceType = "image_generation"
	// ResponsesToolChoiceTypeMCP means an MCP tool must be called
	ResponsesToolChoiceTypeMCP ResponsesToolChoiceType = "mcp"
	// ResponsesToolChoiceTypeCustom means a custom tool must be called
	ResponsesToolChoiceTypeCustom ResponsesToolChoiceType = "custom"
)

// ResponsesToolChoice represents how the model should select tools - can be string or object
type ResponsesToolChoiceStruct struct {
	Type        ResponsesToolChoiceType             `json:"type"`                   // Type of tool choice
	Mode        *string                             `json:"mode,omitempty"`         //"none" | "auto" | "required"
	Name        *string                             `json:"name,omitempty"`         // Common name field for function/MCP/custom tools
	ServerLabel *string                             `json:"server_label,omitempty"` // Common server label field for MCP tools
	Tools       []ResponsesToolChoiceAllowedToolDef `json:"tools,omitempty"`
}

type ResponsesToolChoice struct {
	ResponsesToolChoiceStr    *string
	ResponsesToolChoiceStruct *ResponsesToolChoiceStruct
}

// MarshalJSON implements custom JSON marshalling for ChatMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (bc ResponsesToolChoice) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if bc.ResponsesToolChoiceStr != nil && bc.ResponsesToolChoiceStruct != nil {
		return nil, fmt.Errorf("both ResponsesToolChoiceStr, ResponsesToolChoiceStruct are set; only one should be non-nil")
	}

	if bc.ResponsesToolChoiceStr != nil {
		return sonic.Marshal(bc.ResponsesToolChoiceStr)
	}
	if bc.ResponsesToolChoiceStruct != nil {
		return sonic.Marshal(bc.ResponsesToolChoiceStruct)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (bc *ResponsesToolChoice) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var toolChoiceStr string
	if err := sonic.Unmarshal(data, &toolChoiceStr); err == nil {
		bc.ResponsesToolChoiceStr = &toolChoiceStr
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var responsesToolChoiceStruct ResponsesToolChoiceStruct
	if err := sonic.Unmarshal(data, &responsesToolChoiceStruct); err == nil {
		bc.ResponsesToolChoiceStruct = &responsesToolChoiceStruct
		return nil
	}

	return fmt.Errorf("tool_choice field is neither a string nor a ResponsesToolChoiceStruct object")
}

// ToolChoiceAllowedToolDef - Definition of an allowed tool
type ResponsesToolChoiceAllowedToolDef struct {
	Type        string  `json:"type"`                   // "function" | "mcp" | "image_generation"
	Name        *string `json:"name,omitempty"`         // for function tools
	ServerLabel *string `json:"server_label,omitempty"` // for MCP tools
}

// =============================================================================
// 7. TOOL CONFIGURATION STRUCTURES
// =============================================================================

// Tool represents different types of tools the model can use
type ResponsesTool struct {
	Type        string  `json:"type"`                  // "function" | "file_search" | "computer_use_preview" | "web_search" | "web_search_2025_08_26" | "mcp" | "code_interpreter" | "image_generation" | "local_shell" | "custom" | "web_search_preview" | "web_search_preview_2025_03_11"
	Name        *string `json:"name,omitempty"`        // Common name field (Function, Custom tools)
	Description *string `json:"description,omitempty"` // Common description field (Function, Custom tools)

	*ResponsesToolFunction
	*ResponsesToolFileSearch
	*ResponsesToolComputerUsePreview
	*ResponsesToolWebSearch
	*ResponsesToolMCP
	*ResponsesToolCodeInterpreter
	*ResponsesToolImageGeneration
	*ResponsesToolLocalShell
	*ResponsesToolCustom
	*ResponsesToolWebSearchPreview
}

type ResponsesToolFunction struct {
	Parameters *ToolFunctionParameters `json:"parameters,omitempty"` // A JSON schema object describing the parameters
	Strict     *bool                   `json:"strict,omitempty"`     // Whether to enforce strict parameter validation
}

// ToolFileSearch - A tool that searches for relevant content from uploaded files
type ResponsesToolFileSearch struct {
	VectorStoreIDs []string                               `json:"vector_store_ids"`          // The IDs of the vector stores to search
	Filters        *ResponsesToolFileSearchFilter         `json:"filters,omitempty"`         // A filter to apply
	MaxNumResults  *int                                   `json:"max_num_results,omitempty"` // Maximum results (1-50)
	RankingOptions *ResponsesToolFileSearchRankingOptions `json:"ranking_options,omitempty"` // Ranking options for search
}

// FileSearchFilter - A filter to apply to file search
type ResponsesToolFileSearchFilter struct {
	Type string `json:"type"` // "eq" | "ne" | "gt" | "gte" | "lt" | "lte" | "and" | "or"

	// Filter types - only one should be set
	*ResponsesToolFileSearchComparisonFilter
	*ResponsesToolFileSearchCompoundFilter
}

// MarshalJSON implements custom JSON marshaling for ResponsesToolFileSearchFilter
func (f *ResponsesToolFileSearchFilter) MarshalJSON() ([]byte, error) {
	// Validate that exactly one filter type is set
	if f.ResponsesToolFileSearchComparisonFilter != nil && f.ResponsesToolFileSearchCompoundFilter != nil {
		return nil, fmt.Errorf("both comparison and compound filters are set; only one should be non-nil")
	}
	if f.ResponsesToolFileSearchComparisonFilter == nil && f.ResponsesToolFileSearchCompoundFilter == nil {
		return nil, fmt.Errorf("neither comparison nor compound filter is set; exactly one must be non-nil")
	}

	// Create a map to hold the JSON data
	result := make(map[string]interface{})
	result["type"] = f.Type

	// Marshal the appropriate embedded struct based on type
	switch f.Type {
	case "eq", "ne", "gt", "gte", "lt", "lte":
		if f.ResponsesToolFileSearchComparisonFilter == nil {
			return nil, fmt.Errorf("comparison filter is nil but type is %s", f.Type)
		}
		// Copy fields from the embedded struct
		result["key"] = f.ResponsesToolFileSearchComparisonFilter.Key
		result["value"] = f.ResponsesToolFileSearchComparisonFilter.Value
	case "and", "or":
		if f.ResponsesToolFileSearchCompoundFilter == nil {
			return nil, fmt.Errorf("compound filter is nil but type is %s", f.Type)
		}
		// Copy fields from the embedded struct
		result["filters"] = f.ResponsesToolFileSearchCompoundFilter.Filters
	default:
		return nil, fmt.Errorf("unknown filter type: %s", f.Type)
	}

	return sonic.Marshal(result)
}

// UnmarshalJSON implements custom JSON unmarshaling for ResponsesToolFileSearchFilter
func (f *ResponsesToolFileSearchFilter) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a map to inspect the type field
	var raw map[string]interface{}
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to unmarshal filter JSON: %w", err)
	}

	// Extract the type field
	typeValue, ok := raw["type"]
	if !ok {
		return fmt.Errorf("missing required 'type' field in filter")
	}

	typeStr, ok := typeValue.(string)
	if !ok {
		return fmt.Errorf("'type' field must be a string, got %T", typeValue)
	}

	f.Type = typeStr

	// Initialize the appropriate embedded struct based on type
	switch typeStr {
	case "eq", "ne", "gt", "gte", "lt", "lte":
		// This is a comparison filter
		f.ResponsesToolFileSearchComparisonFilter = &ResponsesToolFileSearchComparisonFilter{}
		f.ResponsesToolFileSearchCompoundFilter = nil

		// Unmarshal into the comparison filter
		if err := sonic.Unmarshal(data, f.ResponsesToolFileSearchComparisonFilter); err != nil {
			return fmt.Errorf("failed to unmarshal comparison filter: %w", err)
		}

		// Validate required fields
		if f.ResponsesToolFileSearchComparisonFilter.Key == "" {
			return fmt.Errorf("comparison filter missing required 'key' field")
		}
		if f.ResponsesToolFileSearchComparisonFilter.Value == nil {
			return fmt.Errorf("comparison filter missing required 'value' field")
		}

	case "and", "or":
		// This is a compound filter
		f.ResponsesToolFileSearchCompoundFilter = &ResponsesToolFileSearchCompoundFilter{}
		f.ResponsesToolFileSearchComparisonFilter = nil

		// Unmarshal into the compound filter
		if err := sonic.Unmarshal(data, f.ResponsesToolFileSearchCompoundFilter); err != nil {
			return fmt.Errorf("failed to unmarshal compound filter: %w", err)
		}

		// Validate required fields
		if f.ResponsesToolFileSearchCompoundFilter.Filters == nil {
			return fmt.Errorf("compound filter missing required 'filters' field")
		}
		if len(f.ResponsesToolFileSearchCompoundFilter.Filters) == 0 {
			return fmt.Errorf("compound filter 'filters' array cannot be empty")
		}

	default:
		return fmt.Errorf("unknown filter type: %s (supported types: eq, ne, gt, gte, lt, lte, and, or)", typeStr)
	}

	return nil
}

// FileSearchComparisonFilter - Compare a specified attribute key to a value
type ResponsesToolFileSearchComparisonFilter struct {
	Key   string      `json:"key"`   // The key to compare against the value
	Type  string      `json:"type"`  //
	Value interface{} `json:"value"` // The value to compare (string, number, or boolean)
}

// FileSearchCompoundFilter - Combine multiple filters using and or or
type ResponsesToolFileSearchCompoundFilter struct {
	Filters []ResponsesToolFileSearchFilter `json:"filters"` // Array of filters to combine
}

// FileSearchRankingOptions - Ranking options for search
type ResponsesToolFileSearchRankingOptions struct {
	Ranker         *string  `json:"ranker,omitempty"`          // The ranker to use
	ScoreThreshold *float64 `json:"score_threshold,omitempty"` // Score threshold (0-1)
}

// ToolComputerUsePreview - A tool that controls a virtual computer
type ResponsesToolComputerUsePreview struct {
	DisplayHeight int    `json:"display_height"` // The height of the computer display
	DisplayWidth  int    `json:"display_width"`  // The width of the computer display
	Environment   string `json:"environment"`    // The type of computer environment to control
}

// ToolWebSearch - Search the Internet for sources related to the prompt
type ResponsesToolWebSearch struct {
	Filters           *ResponsesToolWebSearchFilters      `json:"filters,omitempty"`             // Filters for the search
	SearchContextSize *string                             `json:"search_context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *ResponsesToolWebSearchUserLocation `json:"user_location,omitempty"`       // The approximate location of the user
}

// ResponsesToolWebSearchFilters - Filters for web search
type ResponsesToolWebSearchFilters struct {
	AllowedDomains []string `json:"allowed_domains"` // Allowed domains for the search
}

// ResponsesToolWebSearchUserLocation - The approximate location of the user
type ResponsesToolWebSearchUserLocation struct {
	City     *string `json:"city,omitempty"`     // Free text input for the city
	Country  *string `json:"country,omitempty"`  // Two-letter ISO country code
	Region   *string `json:"region,omitempty"`   // Free text input for the region
	Timezone *string `json:"timezone,omitempty"` // IANA timezone
	Type     *string `json:"type,omitempty"`     // always "approximate"
}

// ResponsesToolMCP - Give the model access to additional tools via remote MCP servers
type ResponsesToolMCP struct {
	ServerLabel       string                                       `json:"server_label"`                 // A label for this MCP server
	AllowedTools      *ResponsesToolMCPAllowedTools                `json:"allowed_tools,omitempty"`      // List of allowed tool names or filter
	Authorization     *string                                      `json:"authorization,omitempty"`      // OAuth access token
	ConnectorID       *string                                      `json:"connector_id,omitempty"`       // Service connector ID
	Headers           *map[string]string                           `json:"headers,omitempty"`            // Optional HTTP headers
	RequireApproval   *ResponsesToolMCPAllowedToolsApprovalSetting `json:"require_approval,omitempty"`   // Tool approval settings
	ServerDescription *string                                      `json:"server_description,omitempty"` // Optional server description
	ServerURL         *string                                      `json:"server_url,omitempty"`         // The URL for the MCP server
}

// ResponsesToolMCPAllowedTools - List of allowed tool names or a filter object
type ResponsesToolMCPAllowedTools struct {
	// Either a simple array of tool names or a filter object
	ToolNames []string                            `json:",omitempty"`
	Filter    *ResponsesToolMCPAllowedToolsFilter `json:",omitempty"`
}

// ResponsesToolMCPAllowedToolsFilter - A filter object to specify which tools are allowed
type ResponsesToolMCPAllowedToolsFilter struct {
	ReadOnly  *bool    `json:"read_only,omitempty"`  // Whether tool is read-only
	ToolNames []string `json:"tool_names,omitempty"` // List of allowed tool names
}

// ResponsesToolMCPAllowedToolsApprovalSetting - Specify which tools require approval
type ResponsesToolMCPAllowedToolsApprovalSetting struct {
	// Either a string setting or filter objects
	Setting *string                                     `json:",omitempty"` // "always" | "never"
	Always  *ResponsesToolMCPAllowedToolsApprovalFilter `json:"always,omitempty"`
	Never   *ResponsesToolMCPAllowedToolsApprovalFilter `json:"never,omitempty"`
}

// ResponsesToolMCPAllowedToolsApprovalFilter - Filter for approval settings
type ResponsesToolMCPAllowedToolsApprovalFilter struct {
	ReadOnly  *bool    `json:"read_only,omitempty"`  // Whether tool is read-only
	ToolNames []string `json:"tool_names,omitempty"` // List of tool names
}

// ToolCodeInterpreter - A tool that runs Python code
type ResponsesToolCodeInterpreter struct {
	Container interface{} `json:"container"` // Container ID or object with file IDs
}

// ToolImageGeneration - A tool that generates images
type ResponsesToolImageGeneration struct {
	Background        *string                                     `json:"background,omitempty"`         // "transparent" | "opaque" | "auto"
	InputFidelity     *string                                     `json:"input_fidelity,omitempty"`     // "high" | "low"
	InputImageMask    *ResponsesToolImageGenerationInputImageMask `json:"input_image_mask,omitempty"`   // Optional mask for inpainting
	Model             *string                                     `json:"model,omitempty"`              // Image generation model
	Moderation        *string                                     `json:"moderation,omitempty"`         // Moderation level
	OutputCompression *int                                        `json:"output_compression,omitempty"` // Compression level (0-100)
	OutputFormat      *string                                     `json:"output_format,omitempty"`      // "png" | "webp" | "jpeg"
	PartialImages     *int                                        `json:"partial_images,omitempty"`     // Number of partial images (0-3)
	Quality           *string                                     `json:"quality,omitempty"`            // "low" | "medium" | "high" | "auto"
	Size              *string                                     `json:"size,omitempty"`               // Image size
}

// ImageGenerationInputMask - Optional mask for inpainting
type ResponsesToolImageGenerationInputImageMask struct {
	FileID   *string `json:"file_id,omitempty"`   // File ID for the mask image
	ImageURL *string `json:"image_url,omitempty"` // Base64-encoded mask image
}

// ToolLocalShell - A tool that allows executing shell commands locally
type ResponsesToolLocalShell struct {
	// No unique fields needed since Type is now in the top-level struct
}

// ToolCustom - A custom tool that processes input using a specified format
type ResponsesToolCustom struct {
	Format *ResponsesToolCustomFormat `json:"format,omitempty"` // The input format
}

// CustomToolFormat - The input format for the custom tool
type ResponsesToolCustomFormat struct {
	Type string `json:"type"` // always "text"

	// For Grammar
	Definition *string `json:"definition,omitempty"` // The grammar definition
	Syntax     *string `json:"syntax,omitempty"`     // "lark" | "regex"
}

// ToolWebSearchPreview - Web search tool preview variant
type ResponsesToolWebSearchPreview struct {
	SearchContextSize *string                             `json:"search_context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *ResponsesToolWebSearchUserLocation `json:"user_location,omitempty"`       // The user's location
}
