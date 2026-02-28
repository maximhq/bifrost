package schemas

import (
	"bytes"
	"fmt"
)

// BifrostChatRequest is the request struct for chat completion requests
type BifrostChatRequest struct {
	Provider       ModelProvider   `json:"provider"`
	Model          string          `json:"model"`
	Input          []ChatMessage   `json:"input,omitempty"`
	Params         *ChatParameters `json:"params,omitempty"`
	Fallbacks      []Fallback      `json:"fallbacks,omitempty"`
	RawRequestBody []byte          `json:"-"` // set bifrost-use-raw-request-body to true in ctx to use the raw request body. Bifrost will directly send this to the downstream provider.
}

// GetRawRequestBody returns the raw request body
func (cr *BifrostChatRequest) GetRawRequestBody() []byte {
	return cr.RawRequestBody
}

func (cr *BifrostChatRequest) GetExtraParams() map[string]interface{} {
	if cr.Params == nil {
		return make(map[string]interface{}, 0)
	}
	return cr.Params.ExtraParams
}

// BifrostChatResponse represents the complete result from a chat completion request.
type BifrostChatResponse struct {
	ID                string                     `json:"id"`
	Choices           []BifrostResponseChoice    `json:"choices"`
	Created           int                        `json:"created"` // The Unix timestamp (in seconds).
	Model             string                     `json:"model"`
	Object            string                     `json:"object"` // "chat.completion" or "chat.completion.chunk"
	ServiceTier       *string                    `json:"service_tier,omitempty"`
	SystemFingerprint string                     `json:"system_fingerprint"`
	Usage             *BifrostLLMUsage           `json:"usage"`
	ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
	ExtraParams       map[string]interface{}     `json:"-"`

	// Perplexity-specific fields
	SearchResults []SearchResult `json:"search_results,omitempty"`
	Videos        []VideoResult  `json:"videos,omitempty"`
	Citations     []string       `json:"citations,omitempty"`
}

// ToTextCompletionResponse converts a BifrostChatResponse to a BifrostTextCompletionResponse
func (cr *BifrostChatResponse) ToTextCompletionResponse() *BifrostTextCompletionResponse {
	if cr == nil {
		return nil
	}

	if len(cr.Choices) == 0 {
		return &BifrostTextCompletionResponse{
			ID:                cr.ID,
			Model:             cr.Model,
			Object:            "text_completion",
			SystemFingerprint: cr.SystemFingerprint,
			Usage:             cr.Usage,
			ExtraFields: BifrostResponseExtraFields{
				RequestType:    TextCompletionRequest,
				ChunkIndex:     cr.ExtraFields.ChunkIndex,
				Provider:       cr.ExtraFields.Provider,
				ModelRequested: cr.ExtraFields.ModelRequested,
				Latency:        cr.ExtraFields.Latency,
				RawResponse:    cr.ExtraFields.RawResponse,
				CacheDebug:     cr.ExtraFields.CacheDebug,
			},
		}
	}

	choice := cr.Choices[0]

	// Handle streaming response choice
	if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
		return &BifrostTextCompletionResponse{
			ID:                cr.ID,
			Model:             cr.Model,
			Object:            "text_completion",
			SystemFingerprint: cr.SystemFingerprint,
			Choices: []BifrostResponseChoice{
				{
					Index: 0,
					TextCompletionResponseChoice: &TextCompletionResponseChoice{
						Text: choice.ChatStreamResponseChoice.Delta.Content,
					},
					FinishReason: choice.FinishReason,
					LogProbs:     choice.LogProbs,
				},
			},
			Usage: cr.Usage,
			ExtraFields: BifrostResponseExtraFields{
				RequestType:    TextCompletionRequest,
				ChunkIndex:     cr.ExtraFields.ChunkIndex,
				Provider:       cr.ExtraFields.Provider,
				ModelRequested: cr.ExtraFields.ModelRequested,
				Latency:        cr.ExtraFields.Latency,
				RawResponse:    cr.ExtraFields.RawResponse,
				CacheDebug:     cr.ExtraFields.CacheDebug,
			},
		}
	}

	// Handle non-streaming response choice
	if choice.ChatNonStreamResponseChoice != nil {
		msg := choice.ChatNonStreamResponseChoice.Message
		var textContent *string
		if msg != nil && msg.Content != nil && msg.Content.ContentStr != nil {
			textContent = msg.Content.ContentStr
		}
		return &BifrostTextCompletionResponse{
			ID:                cr.ID,
			Model:             cr.Model,
			Object:            "text_completion",
			SystemFingerprint: cr.SystemFingerprint,
			Choices: []BifrostResponseChoice{
				{
					Index: 0,
					TextCompletionResponseChoice: &TextCompletionResponseChoice{
						Text: textContent,
					},
					FinishReason: choice.FinishReason,
					LogProbs:     choice.LogProbs,
				},
			},
			Usage: cr.Usage,
			ExtraFields: BifrostResponseExtraFields{
				RequestType:    TextCompletionRequest,
				ChunkIndex:     cr.ExtraFields.ChunkIndex,
				Provider:       cr.ExtraFields.Provider,
				ModelRequested: cr.ExtraFields.ModelRequested,
				Latency:        cr.ExtraFields.Latency,
				RawResponse:    cr.ExtraFields.RawResponse,
				CacheDebug:     cr.ExtraFields.CacheDebug,
			},
		}
	}

	// Fallback case - return basic response structure
	return &BifrostTextCompletionResponse{
		ID:                cr.ID,
		Model:             cr.Model,
		Object:            "text_completion",
		SystemFingerprint: cr.SystemFingerprint,
		Usage:             cr.Usage,
		ExtraFields: BifrostResponseExtraFields{
			RequestType:    TextCompletionRequest,
			ChunkIndex:     cr.ExtraFields.ChunkIndex,
			Provider:       cr.ExtraFields.Provider,
			ModelRequested: cr.ExtraFields.ModelRequested,
			Latency:        cr.ExtraFields.Latency,
			RawResponse:    cr.ExtraFields.RawResponse,
			CacheDebug:     cr.ExtraFields.CacheDebug,
		},
	}
}

// ChatParameters represents the parameters for a chat completion.
type ChatParameters struct {
	Audio                *ChatAudioParameters  `json:"audio,omitempty"`                 // Audio parameters
	FrequencyPenalty     *float64              `json:"frequency_penalty,omitempty"`     // Penalizes frequent tokens
	LogitBias            *map[string]float64   `json:"logit_bias,omitempty"`            // Bias for logit values
	LogProbs             *bool                 `json:"logprobs,omitempty"`              // Number of logprobs to return
	MaxCompletionTokens  *int                  `json:"max_completion_tokens,omitempty"` // Maximum number of tokens to generate
	Metadata             *map[string]any       `json:"metadata,omitempty"`              // Metadata to be returned with the response
	Modalities           []string              `json:"modalities,omitempty"`            // Modalities to be returned with the response
	ParallelToolCalls    *bool                 `json:"parallel_tool_calls,omitempty"`
	Prediction           *ChatPrediction       `json:"prediction,omitempty"`             // Predicted output content (OpenAI only)
	PresencePenalty      *float64              `json:"presence_penalty,omitempty"`       // Penalizes repeated tokens
	PromptCacheKey       *string               `json:"prompt_cache_key,omitempty"`       // Prompt cache key
	PromptCacheRetention *string               `json:"prompt_cache_retention,omitempty"` // Prompt cache retention ("in-memory" or "24h")
	Reasoning            *ChatReasoning        `json:"reasoning,omitempty"`              // Reasoning parameters
	ResponseFormat       *interface{}          `json:"response_format,omitempty"`        // Format for the response
	SafetyIdentifier     *string               `json:"safety_identifier,omitempty"`      // Safety identifier
	Seed                 *int                  `json:"seed,omitempty"`
	ServiceTier          *string               `json:"service_tier,omitempty"`
	StreamOptions        *ChatStreamOptions    `json:"stream_options,omitempty"`
	Stop                 []string              `json:"stop,omitempty"`
	Store                *bool                 `json:"store,omitempty"`
	Temperature          *float64              `json:"temperature,omitempty"`
	TopLogProbs          *int                  `json:"top_logprobs,omitempty"`
	TopP                 *float64              `json:"top_p,omitempty"`              // Controls diversity via nucleus sampling
	ToolChoice           *ChatToolChoice       `json:"tool_choice,omitempty"`        // Whether to call a tool
	Tools                []ChatTool            `json:"tools,omitempty"`              // Tools to use
	User                 *string               `json:"user,omitempty"`               // User identifier for tracking
	Verbosity            *string               `json:"verbosity,omitempty"`          // "low" | "medium" | "high"
	WebSearchOptions     *ChatWebSearchOptions `json:"web_search_options,omitempty"` // Web search options (OpenAI only)

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatParameters.
func (cp *ChatParameters) UnmarshalJSON(data []byte) error {
	// Alias to avoid recursion
	type Alias ChatParameters

	// Aux struct adds reasoning_effort for decoding
	var aux struct {
		*Alias
		ReasoningEffort    *string `json:"reasoning_effort"` // only for input
		ReasoningMaxTokens *int    `json:"reasoning_max_tokens"`
	}

	aux.Alias = (*Alias)(cp)

	// Single unmarshal
	if err := Unmarshal(data, &aux); err != nil {
		return err
	}

	// Now aux.Reasoning (from Alias) and aux.ReasoningEffort are filled

	// Validate that specific fields don't conflict
	if aux.ReasoningEffort != nil && aux.Reasoning != nil && aux.Reasoning.Effort != nil {
		return fmt.Errorf("both reasoning_effort and reasoning.effort cannot be present at the same time")
	}
	if aux.ReasoningMaxTokens != nil && aux.Reasoning != nil && aux.Reasoning.MaxTokens != nil {
		return fmt.Errorf("both reasoning_max_tokens and reasoning.max_tokens cannot be present at the same time")
	}

	if aux.ReasoningEffort != nil || aux.ReasoningMaxTokens != nil {
		if cp.Reasoning == nil {
			cp.Reasoning = &ChatReasoning{}
		}
		// Merge top-level fields into the reasoning object
		if aux.ReasoningEffort != nil {
			cp.Reasoning.Effort = aux.ReasoningEffort
		}
		if aux.ReasoningMaxTokens != nil {
			cp.Reasoning.MaxTokens = aux.ReasoningMaxTokens
		}
	}
	// ExtraParams etc. are already handled by the alias
	return nil
}

// ChatAudioParameters represents the parameters for a chat audio completion. (Only supported by OpenAI Models that support audio input)
type ChatAudioParameters struct {
	Format string `json:"format,omitempty"` // Format for the audio completion
	Voice  string `json:"voice,omitempty"`  // Voice to use for the audio completion
}

// Not in OpenAI's spec, but needed to support extra parameters for reasoning.
type ChatReasoning struct {
	Effort    *string `json:"effort,omitempty"`     // "none" |  "minimal" | "low" | "medium" | "high" (any value other than "none" will enable reasoning)
	MaxTokens *int    `json:"max_tokens,omitempty"` // Maximum number of tokens to generate for the reasoning output (required for anthropic)
}

// ChatPrediction represents predicted output content for the model to reference (OpenAI only).
// Providing prediction content can significantly reduce latency for certain models.
type ChatPrediction struct {
	Type    string      `json:"type"`    // Always "content"
	Content interface{} `json:"content"` // String or array of content parts
}

// ChatWebSearchOptions represents web search options for chat completions (OpenAI only).
type ChatWebSearchOptions struct {
	SearchContextSize *string                           `json:"search_context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *ChatWebSearchOptionsUserLocation `json:"user_location,omitempty"`
}

// ChatWebSearchOptionsUserLocation represents user location for web search.
type ChatWebSearchOptionsUserLocation struct {
	Type        string                                       `json:"type"` // "approximate"
	Approximate *ChatWebSearchOptionsUserLocationApproximate `json:"approximate,omitempty"`
}

// ChatWebSearchOptionsUserLocationApproximate represents approximate user location details.
type ChatWebSearchOptionsUserLocationApproximate struct {
	City     *string `json:"city,omitempty"`
	Country  *string `json:"country,omitempty"`  // Two-letter ISO country code (e.g., "US")
	Region   *string `json:"region,omitempty"`   // e.g., "California"
	Timezone *string `json:"timezone,omitempty"` // IANA timezone (e.g., "America/Los_Angeles")
}

// ChatStreamOptions represents the stream options for a chat completion.
type ChatStreamOptions struct {
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
	IncludeUsage       *bool `json:"include_usage,omitempty"` // Bifrost marks this as true by default
}

// ChatToolType represents the type of tool.
type ChatToolType string

// ChatToolType values
const (
	ChatToolTypeFunction ChatToolType = "function"
	ChatToolTypeCustom   ChatToolType = "custom"
)

// ChatTool represents a tool definition.
type ChatTool struct {
	Type         ChatToolType      `json:"type"`
	Function     *ChatToolFunction `json:"function,omitempty"`      // Function definition
	Custom       *ChatToolCustom   `json:"custom,omitempty"`        // Custom tool definition
	CacheControl *CacheControl     `json:"cache_control,omitempty"` // Cache control for the tool
}

// ChatToolFunction represents a function definition.
type ChatToolFunction struct {
	Name        string                  `json:"name"`                  // Name of the function
	Description *string                 `json:"description,omitempty"` // Description of the parameters
	Parameters  *ToolFunctionParameters `json:"parameters,omitempty"`  // A JSON schema object describing the parameters
	Strict      *bool                   `json:"strict,omitempty"`      // Whether to enforce strict parameter validation
}

// ToolFunctionParameters represents the parameters for a function definition.
// It supports JSON Schema fields used by various providers (OpenAI, Anthropic, Gemini, etc.).
// Field order follows JSON Schema / OpenAI conventions for consistent serialization.
//
// IMPORTANT: When marshalling to JSON, key order is preserved from the original input
// (captured during UnmarshalJSON). When constructing programmatically, the default
// struct field declaration order is used. This is critical because LLMs are
// sensitive to JSON key ordering in tool schemas.
type ToolFunctionParameters struct {
	Type                 string                      `json:"type"`                           // Type of the parameters
	Description          *string                     `json:"description,omitempty"`          // Description of the parameters
	Properties           *OrderedMap                 `json:"properties"`                     // Parameter properties - always include even if empty (required by JSON Schema and some providers like OpenAI)
	Required             []string                    `json:"required,omitempty"`             // Required parameter names
	AdditionalProperties *AdditionalPropertiesStruct `json:"additionalProperties,omitempty"` // Whether to allow additional properties
	Enum                 []string                    `json:"enum,omitempty"`                 // Enum values for the parameters

	// JSON Schema definition fields
	Defs        *OrderedMap `json:"$defs,omitempty"`       // JSON Schema draft 2019-09+ definitions
	Definitions *OrderedMap `json:"definitions,omitempty"` // Legacy JSON Schema draft-07 definitions
	Ref         *string     `json:"$ref,omitempty"`        // Reference to definition

	// Array schema fields
	Items    *OrderedMap `json:"items,omitempty"`    // Array element schema
	MinItems *int64      `json:"minItems,omitempty"` // Minimum array length
	MaxItems *int64      `json:"maxItems,omitempty"` // Maximum array length

	// Composition fields (union types)
	AnyOf []OrderedMap `json:"anyOf,omitempty"` // Union types (any of these schemas)
	OneOf []OrderedMap `json:"oneOf,omitempty"` // Exclusive union types (exactly one of these)
	AllOf []OrderedMap `json:"allOf,omitempty"` // Schema intersection (all of these)

	// String validation fields
	Format    *string `json:"format,omitempty"`    // String format (email, date, uri, etc.)
	Pattern   *string `json:"pattern,omitempty"`   // Regex pattern for strings
	MinLength *int64  `json:"minLength,omitempty"` // Minimum string length
	MaxLength *int64  `json:"maxLength,omitempty"` // Maximum string length

	// Number validation fields
	Minimum *float64 `json:"minimum,omitempty"` // Minimum number value
	Maximum *float64 `json:"maximum,omitempty"` // Maximum number value

	// Misc fields
	Title    *string     `json:"title,omitempty"`    // Schema title
	Default  interface{} `json:"default,omitempty"`  // Default value
	Nullable *bool       `json:"nullable,omitempty"` // Nullable indicator (OpenAPI 3.0 style)

	// keyOrder preserves the JSON key order from the original input so that
	// MarshalJSON can emit keys in the same order the client sent them.
	keyOrder JSONKeyOrder `json:"-"`
}

// MarshalJSON serializes ToolFunctionParameters to JSON, preserving the original key
// order from the input JSON. If no original order was captured (programmatic construction),
// it falls back to the default struct field declaration order.
// Properties is always emitted as an object, never null.
func (t ToolFunctionParameters) MarshalJSON() ([]byte, error) {
	if t.Properties == nil {
		t.Properties = &OrderedMap{}
	}
	type Alias ToolFunctionParameters
	data, err := Marshal(Alias(t))
	if err != nil {
		return nil, err
	}
	return t.keyOrder.Apply(data)
}

// UnmarshalJSON implements custom JSON unmarshalling for ToolFunctionParameters.
// It handles both JSON object format (standard) and JSON string format (used by some providers like xAI).
// It captures the original key order for order-preserving re-serialization.
func (t *ToolFunctionParameters) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as a JSON string first (xAI sends parameters as a string)
	var jsonStr string
	if err := Unmarshal(data, &jsonStr); err == nil {
		data = []byte(jsonStr)
	}

	type Alias ToolFunctionParameters
	var temp Alias
	if err := Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal ToolFunctionParameters: %w", err)
	}
	*t = ToolFunctionParameters(temp)

	// Normalize additionalProperties: null to omitted field
	if t.AdditionalProperties != nil &&
		t.AdditionalProperties.AdditionalPropertiesBool == nil &&
		t.AdditionalProperties.AdditionalPropertiesMap == nil {
		t.AdditionalProperties = nil
	}

	t.keyOrder.Capture(data)
	return nil
}

type AdditionalPropertiesStruct struct {
	AdditionalPropertiesBool *bool
	AdditionalPropertiesMap  *OrderedMap
}

// MarshalJSON implements custom JSON marshalling for AdditionalPropertiesStruct.
// It marshals either AdditionalPropertiesBool or AdditionalPropertiesMap based on which is set.
func (a AdditionalPropertiesStruct) MarshalJSON() ([]byte, error) {

	// if both are set, return an error
	if a.AdditionalPropertiesBool != nil && a.AdditionalPropertiesMap != nil {
		return nil, fmt.Errorf("both AdditionalPropertiesBool and AdditionalPropertiesMap are set; only one should be non-nil")
	}

	// If bool is set, marshal as boolean
	if a.AdditionalPropertiesBool != nil {
		return Marshal(*a.AdditionalPropertiesBool)
	}

	// If map is set, marshal as object
	if a.AdditionalPropertiesMap != nil {
		return Marshal(a.AdditionalPropertiesMap)
	}

	// If both are nil, return null
	return nil, fmt.Errorf("additionalProperties cannot be null; omit the field instead")
}

// UnmarshalJSON implements custom JSON unmarshalling for AdditionalPropertiesStruct.
// It handles both boolean and object types for additionalProperties.
func (a *AdditionalPropertiesStruct) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		a.AdditionalPropertiesBool = nil
		a.AdditionalPropertiesMap = nil
		return nil
	}

	// First, try to unmarshal as a boolean
	var boolValue bool
	if err := Unmarshal(data, &boolValue); err == nil {
		a.AdditionalPropertiesMap = nil
		a.AdditionalPropertiesBool = &boolValue
		return nil
	}

	// If that fails, try to unmarshal as a map
	var mapValue OrderedMap
	if err := Unmarshal(data, &mapValue); err == nil {
		a.AdditionalPropertiesBool = nil
		a.AdditionalPropertiesMap = &mapValue
		return nil
	}

	// If both fail, return an error
	return fmt.Errorf("additionalProperties must be either a boolean or an object")
}

type ChatToolCustom struct {
	Format *ChatToolCustomFormat `json:"format,omitempty"` // The input format
}

type ChatToolCustomFormat struct {
	Type    string                       `json:"type"` // always "text"
	Grammar *ChatToolCustomGrammarFormat `json:"grammar,omitempty"`
}

// ChatToolCustomGrammarFormat - A grammar defined by the user
type ChatToolCustomGrammarFormat struct {
	Definition string `json:"definition"` // The grammar definition
	Syntax     string `json:"syntax"`     // "lark" | "regex"
}

// ChatToolChoiceType  for all providers, make sure to check the provider's
// documentation to see which tool choices are supported.
type ChatToolChoiceType string

// ChatToolChoiceType values
const (
	ChatToolChoiceTypeNone     ChatToolChoiceType = "none"
	ChatToolChoiceTypeAuto     ChatToolChoiceType = "auto"
	ChatToolChoiceTypeAny      ChatToolChoiceType = "any"
	ChatToolChoiceTypeRequired ChatToolChoiceType = "required"
	// ChatToolChoiceTypeFunction means a specific tool must be called
	ChatToolChoiceTypeFunction ChatToolChoiceType = "function"
	// ChatToolChoiceTypeAllowedTools means a specific tool must be called
	ChatToolChoiceTypeAllowedTools ChatToolChoiceType = "allowed_tools"
	// ChatToolChoiceTypeCustom means a custom tool must be called
	ChatToolChoiceTypeCustom ChatToolChoiceType = "custom"
)

// ChatToolChoiceStruct represents a tool choice.
type ChatToolChoiceStruct struct {
	Type         ChatToolChoiceType          `json:"type"`                    // Type of tool choice
	Function     *ChatToolChoiceFunction     `json:"function,omitempty"`      // Function to call if type is ToolChoiceTypeFunction
	Custom       *ChatToolChoiceCustom       `json:"custom,omitempty"`        // Custom tool to call if type is ToolChoiceTypeCustom
	AllowedTools *ChatToolChoiceAllowedTools `json:"allowed_tools,omitempty"` // Allowed tools to call if type is ToolChoiceTypeAllowedTools
}

type ChatToolChoice struct {
	ChatToolChoiceStr    *string
	ChatToolChoiceStruct *ChatToolChoiceStruct
}

// MarshalJSON implements custom JSON marshalling for ChatMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (ctc ChatToolChoice) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if ctc.ChatToolChoiceStr != nil && ctc.ChatToolChoiceStruct != nil {
		return nil, fmt.Errorf("both ChatToolChoiceStr, ChatToolChoiceStruct are set; only one should be non-nil")
	}

	if ctc.ChatToolChoiceStr != nil {
		return Marshal(ctc.ChatToolChoiceStr)
	}
	if ctc.ChatToolChoiceStruct != nil {
		return Marshal(ctc.ChatToolChoiceStruct)
	}
	// If both are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (ctc *ChatToolChoice) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var toolChoiceStr string
	if err := Unmarshal(data, &toolChoiceStr); err == nil {
		ctc.ChatToolChoiceStr = &toolChoiceStr
		ctc.ChatToolChoiceStruct = nil
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var chatToolChoice ChatToolChoiceStruct
	if err := Unmarshal(data, &chatToolChoice); err == nil {
		ctc.ChatToolChoiceStr = nil
		ctc.ChatToolChoiceStruct = &chatToolChoice
		return nil
	}

	return fmt.Errorf("tool_choice field is neither a string nor a ChatToolChoiceStruct object")
}

// ChatToolChoiceFunction represents a function choice.
type ChatToolChoiceFunction struct {
	Name string `json:"name"`
}

// ChatToolChoiceCustom represents a custom choice.
type ChatToolChoiceCustom struct {
	Name string `json:"name"`
}

// ChatToolChoiceAllowedTools represents a allowed tools choice.
type ChatToolChoiceAllowedTools struct {
	Mode  string                           `json:"mode"` // "auto" | "required"
	Tools []ChatToolChoiceAllowedToolsTool `json:"tools"`
}

// ChatToolChoiceAllowedToolsTool represents a allowed tools tool.
type ChatToolChoiceAllowedToolsTool struct {
	Type     string                 `json:"type"` // "function"
	Function ChatToolChoiceFunction `json:"function,omitempty"`
}

// ChatMessageRole represents the role of a chat message
type ChatMessageRole string

// ChatMessageRole values
const (
	ChatMessageRoleAssistant ChatMessageRole = "assistant"
	ChatMessageRoleUser      ChatMessageRole = "user"
	ChatMessageRoleSystem    ChatMessageRole = "system"
	ChatMessageRoleTool      ChatMessageRole = "tool"
	ChatMessageRoleDeveloper ChatMessageRole = "developer"
)

// ChatMessage represents a message in a chat conversation.
type ChatMessage struct {
	Name    *string             `json:"name,omitempty"` // for chat completions
	Role    ChatMessageRole     `json:"role,omitempty"`
	Content *ChatMessageContent `json:"content,omitempty"`

	// Embedded pointer structs - when non-nil, their exported fields are flattened into the top-level JSON object
	// IMPORTANT: Only one of the following can be non-nil at a time, otherwise the JSON marshalling will override the common fields
	*ChatToolMessage
	*ChatAssistantMessage
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessage.
// This is needed because ChatAssistantMessage has a custom UnmarshalJSON method,
// which interferes with the JSON library's handling of other fields in ChatMessage.
func (cm *ChatMessage) UnmarshalJSON(data []byte) error {
	// Unmarshal the base fields directly
	type baseFields struct {
		Name    *string             `json:"name,omitempty"`
		Role    ChatMessageRole     `json:"role,omitempty"`
		Content *ChatMessageContent `json:"content,omitempty"`
	}
	var base baseFields
	if err := Unmarshal(data, &base); err != nil {
		return err
	}
	cm.Name = base.Name
	cm.Role = base.Role
	cm.Content = base.Content

	// Unmarshal ChatToolMessage fields
	type toolMsgAlias ChatToolMessage
	var toolMsg toolMsgAlias
	if err := Unmarshal(data, &toolMsg); err != nil {
		return err
	}
	if toolMsg.ToolCallID != nil {
		cm.ChatToolMessage = (*ChatToolMessage)(&toolMsg)
	}

	// Unmarshal ChatAssistantMessage (which has its own custom unmarshaller)
	var assistantMsg ChatAssistantMessage
	if err := Unmarshal(data, &assistantMsg); err != nil {
		return err
	}
	// Only set if any field is populated
	if assistantMsg.Refusal != nil || assistantMsg.Reasoning != nil || assistantMsg.ReasoningContent != nil ||
		len(assistantMsg.ReasoningDetails) > 0 || len(assistantMsg.Annotations) > 0 ||
		len(assistantMsg.ToolCalls) > 0 || assistantMsg.Audio != nil {
		cm.ChatAssistantMessage = &assistantMsg
	}

	return nil
}

// ChatMessageContent represents a content in a message.
type ChatMessageContent struct {
	ContentStr    *string
	ContentBlocks []ChatContentBlock
}

// MarshalJSON implements custom JSON marshalling for ChatMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (mc ChatMessageContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if mc.ContentStr != nil && mc.ContentBlocks != nil {
		return nil, fmt.Errorf("both Content string and Content blocks are set; only one should be non-nil")
	}

	if mc.ContentStr != nil {
		return Marshal(*mc.ContentStr)
	}
	if mc.ContentBlocks != nil {
		return Marshal(mc.ContentBlocks)
	}
	// If both are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (mc *ChatMessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		mc.ContentStr = nil
		mc.ContentBlocks = nil
		return nil
	}

	// First, try to unmarshal as a direct string
	var stringContent string
	if err := Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		mc.ContentBlocks = nil
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ChatContentBlock
	if err := Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = arrayContent
		mc.ContentStr = nil
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Content blocks")
}

// ChatContentBlockType represents the type of content block in a message.
type ChatContentBlockType string

// ChatContentBlockType values
const (
	ChatContentBlockTypeText       ChatContentBlockType = "text"
	ChatContentBlockTypeImage      ChatContentBlockType = "image_url"
	ChatContentBlockTypeInputAudio ChatContentBlockType = "input_audio"
	ChatContentBlockTypeFile       ChatContentBlockType = "file"
	ChatContentBlockTypeRefusal    ChatContentBlockType = "refusal"
)

// ChatContentBlock represents a content block in a message.
type ChatContentBlock struct {
	Type           ChatContentBlockType `json:"type"`
	Text           *string              `json:"text,omitempty"`
	Refusal        *string              `json:"refusal,omitempty"`
	ImageURLStruct *ChatInputImage      `json:"image_url,omitempty"`
	InputAudio     *ChatInputAudio      `json:"input_audio,omitempty"`
	File           *ChatInputFile       `json:"file,omitempty"`

	// Not in OpenAI's schemas, but sent by a few providers (Anthropic, Bedrock are some of them)
	CacheControl *CacheControl `json:"cache_control,omitempty"`
	Citations    *Citations    `json:"citations,omitempty"`

	// CachePoint is a Bedrock-specific field for standalone cache point blocks
	// When present without other content, this indicates a cache point marker
	CachePoint *CachePoint `json:"cachePoint,omitempty"`
}

// CachePoint represents a cache point marker (Bedrock-specific)
type CachePoint struct {
	Type string `json:"type"` // "default"
}

type CacheControlType string

const (
	CacheControlTypeEphemeral CacheControlType = "ephemeral"
)

type CacheControl struct {
	Type  CacheControlType `json:"type"`
	TTL   *string          `json:"ttl,omitempty"`   // "1m" | "1h"
	Scope *string          `json:"scope,omitempty"` // "user" | "global"
}

// ChatInputImage represents image data in a message.
type ChatInputImage struct {
	URL    string  `json:"url"`
	Detail *string `json:"detail,omitempty"`
}

// ChatInputAudio represents audio data in a message.
// Data carries the audio payload as a string (e.g., data URL or provider-accepted encoded content).
// Format is optional (e.g., "wav", "mp3"); when nil, providers may attempt auto-detection.
type ChatInputAudio struct {
	Data   string  `json:"data"`
	Format *string `json:"format,omitempty"`
}

// ChatInputFile represents a file in a message.
type ChatInputFile struct {
	FileData *string `json:"file_data,omitempty"` // Base64 encoded file data
	FileURL  *string `json:"file_url,omitempty"`  // Direct URL to file
	FileID   *string `json:"file_id,omitempty"`   // Reference to uploaded file
	Filename *string `json:"filename,omitempty"`  // Name of the file
	FileType *string `json:"file_type,omitempty"` // Type of the file
}

// ChatToolMessage represents a tool message in a chat conversation.
type ChatToolMessage struct {
	ToolCallID *string `json:"tool_call_id,omitempty"`
}

// ChatAssistantMessage represents a message in a chat conversation.
type ChatAssistantMessage struct {
	Refusal          *string                          `json:"refusal,omitempty"`
	Audio            *ChatAudioMessageAudio           `json:"audio,omitempty"`
	Reasoning        *string                          `json:"reasoning,omitempty"`
	ReasoningContent *string                          `json:"reasoning_content,omitempty"` // OpenAI-compatible alias used by providers like xAI/Fireworks
	ReasoningDetails []ChatReasoningDetails           `json:"reasoning_details,omitempty"`
	Annotations      []ChatAssistantMessageAnnotation `json:"annotations,omitempty"`
	ToolCalls        []ChatAssistantMessageToolCall   `json:"tool_calls,omitempty"`
}

// UnmarshalJSON implements custom unmarshalling for ChatAssistantMessage.
// If Reasoning is non-nil and ReasoningDetails is nil/empty, it adds a single
// ChatReasoningDetails entry of type "reasoning.text" with the text set to Reasoning.
func (cm *ChatAssistantMessage) UnmarshalJSON(data []byte) error {
	if cm == nil {
		return nil
	}

	// Alias to avoid infinite recursion
	type Alias ChatAssistantMessage

	var aux Alias
	if err := Unmarshal(data, &aux); err != nil {
		return err
	}

	// Copy decoded data back into the original type
	*cm = ChatAssistantMessage(aux)

	// Normalize reasoning_content into Reasoning for internal compatibility.
	// Keep ReasoningContent as-is so request forwarding can preserve the original field name.
	if cm.Reasoning == nil && cm.ReasoningContent != nil {
		cm.Reasoning = cm.ReasoningContent
	}

	// If Reasoning is present and there are no reasoning_details,
	// synthesize a text reasoning_details entry.
	if cm.Reasoning != nil && len(cm.ReasoningDetails) == 0 {
		text := *cm.Reasoning
		cm.ReasoningDetails = []ChatReasoningDetails{
			{
				Index: 0,
				Type:  BifrostReasoningDetailsTypeText,
				Text:  &text,
			},
		}
	}

	return nil
}

// ChatAssistantMessageAnnotation represents an annotation in a response.
type ChatAssistantMessageAnnotation struct {
	Type        string                                 `json:"type"`
	URLCitation ChatAssistantMessageAnnotationCitation `json:"url_citation"`
}

// ChatAssistantMessageAnnotationCitation represents a citation in a response.
type ChatAssistantMessageAnnotationCitation struct {
	StartIndex int          `json:"start_index"`
	EndIndex   int          `json:"end_index"`
	Title      string       `json:"title"`
	URL        *string      `json:"url,omitempty"`
	Sources    *interface{} `json:"sources,omitempty"`
	Type       *string      `json:"type,omitempty"`
}

// ChatAssistantMessageToolCall represents a tool call in a message
type ChatAssistantMessageToolCall struct {
	Index    uint16                               `json:"index"`
	Type     *string                              `json:"type,omitempty"`
	ID       *string                              `json:"id,omitempty"`
	Function ChatAssistantMessageToolCallFunction `json:"function"`
}

// ChatAssistantMessageToolCallFunction represents a call to a function.
type ChatAssistantMessageToolCallFunction struct {
	Name      *string `json:"name"`
	Arguments string  `json:"arguments"` // stringified json as retured by OpenAI, might not be a valid JSON always
}

// ChatAudioMessageAudio represents audio data in a message.
type ChatAudioMessageAudio struct {
	ID         string `json:"id"`
	Data       string `json:"data"`
	ExpiresAt  int    `json:"expires_at"`
	Transcript string `json:"transcript"`
}

// BifrostResponseChoice represents a choice in the completion result.
// This struct can represent either a streaming or non-streaming response choice.
// IMPORTANT: Only one of TextCompletionResponseChoice, NonStreamResponseChoice or StreamResponseChoice
// should be non-nil at a time.
type BifrostResponseChoice struct {
	Index        int              `json:"index"`
	FinishReason *string          `json:"finish_reason,omitempty"`
	LogProbs     *BifrostLogProbs `json:"log_probs,omitempty"`

	*TextCompletionResponseChoice
	*ChatNonStreamResponseChoice
	*ChatStreamResponseChoice
}

// BifrostFinishReason represents the reason why the model stopped generating.
type BifrostFinishReason string

// BifrostFinishReason values
const (
	BifrostFinishReasonStop      BifrostFinishReason = "stop"
	BifrostFinishReasonLength    BifrostFinishReason = "length"
	BifrostFinishReasonToolCalls BifrostFinishReason = "tool_calls"
)

type BifrostReasoningDetailsType string

const (
	BifrostReasoningDetailsTypeSummary   BifrostReasoningDetailsType = "reasoning.summary"
	BifrostReasoningDetailsTypeEncrypted BifrostReasoningDetailsType = "reasoning.encrypted"
	BifrostReasoningDetailsTypeText      BifrostReasoningDetailsType = "reasoning.text"
)

// Not in OpenAI's spec, but needed to support inter provider reasoning capabilities.
type ChatReasoningDetails struct {
	ID        *string                     `json:"id,omitempty"`
	Index     int                         `json:"index"`
	Type      BifrostReasoningDetailsType `json:"type"`
	Summary   *string                     `json:"summary,omitempty"`
	Text      *string                     `json:"text,omitempty"`
	Signature *string                     `json:"signature,omitempty"`
	Data      *string                     `json:"data,omitempty"` // for encrypted data
}

// BifrostLogProbs represents the log probabilities for different aspects of a response.
type BifrostLogProbs struct {
	Content []ContentLogProb `json:"content,omitempty"`
	Refusal []LogProb        `json:"refusal,omitempty"`

	*TextCompletionLogProb
}

type TextCompletionResponseChoice struct {
	Text *string `json:"text,omitempty"`
}

// ChatNonStreamResponseChoice represents a choice in the non-stream response
type ChatNonStreamResponseChoice struct {
	Message    *ChatMessage `json:"message"`
	StopString *string      `json:"stop,omitempty"`
}

// ChatStreamResponseChoice represents a choice in the stream response
type ChatStreamResponseChoice struct {
	Delta *ChatStreamResponseChoiceDelta `json:"delta,omitempty"` // Partial message info
}

// ChatStreamResponseChoiceDelta represents a delta in the stream response
type ChatStreamResponseChoiceDelta struct {
	Role             *string                        `json:"role,omitempty"`      // Only in the first chunk
	Content          *string                        `json:"content,omitempty"`   // May be empty string or null
	Refusal          *string                        `json:"refusal,omitempty"`   // Refusal content if any
	Audio            *ChatAudioMessageAudio         `json:"audio,omitempty"`     // Audio data if any
	Reasoning        *string                        `json:"reasoning,omitempty"` // May be empty string or null
	ReasoningDetails []ChatReasoningDetails         `json:"reasoning_details,omitempty"`
	ToolCalls        []ChatAssistantMessageToolCall `json:"tool_calls,omitempty"` // If tool calls used (supports incremental updates)
}

// UnmarshalJSON implements custom unmarshalling for ChatStreamResponseChoiceDelta.
// If Reasoning is non-nil and ReasoningDetails is nil/empty, it adds a single
// ChatReasoningDetails entry of type "reasoning.text" with the text set to Reasoning.
func (d *ChatStreamResponseChoiceDelta) UnmarshalJSON(data []byte) error {
	// Alias to avoid infinite recursion
	type Alias ChatStreamResponseChoiceDelta

	// Auxiliary struct to capture xAI's reasoning_content field
	var aux struct {
		Alias
		ReasoningContent *string `json:"reasoning_content,omitempty"` // xAI uses this field name
	}

	if err := Unmarshal(data, &aux); err != nil {
		return err
	}

	// Copy decoded data back into the original type
	*d = ChatStreamResponseChoiceDelta(aux.Alias)

	// Map xAI's reasoning_content to Bifrost's Reasoning field
	// This allows both OpenAI's "reasoning" and xAI's "reasoning_content" to work
	if aux.ReasoningContent != nil && d.Reasoning == nil {
		d.Reasoning = aux.ReasoningContent
	}

	// If Reasoning is present and there are no reasoning_details,
	// synthesize a text reasoning_details entry.
	if d.Reasoning != nil && len(d.ReasoningDetails) == 0 {
		text := *d.Reasoning
		d.ReasoningDetails = []ChatReasoningDetails{
			{
				Index: 0,
				Type:  BifrostReasoningDetailsTypeText,
				Text:  &text,
			},
		}
	}

	return nil
}

// LogProb represents the log probability of a token.
type LogProb struct {
	Bytes   []int   `json:"bytes,omitempty"`
	LogProb float64 `json:"logprob"`
	Token   string  `json:"token"`
}

// ContentLogProb represents log probability information for content.
type ContentLogProb struct {
	Bytes       []int     `json:"bytes"`
	LogProb     float64   `json:"logprob"`
	Token       string    `json:"token"`
	TopLogProbs []LogProb `json:"top_logprobs"`
}

// BifrostLLMUsage represents token usage information
type BifrostLLMUsage struct {
	PromptTokens            int                          `json:"prompt_tokens,omitempty"`
	PromptTokensDetails     *ChatPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokens        int                          `json:"completion_tokens,omitempty"`
	CompletionTokensDetails *ChatCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	TotalTokens             int                          `json:"total_tokens"`
	Cost                    *BifrostCost                 `json:"cost,omitempty"` //Only for the providers which support cost calculation
}

type ChatPromptTokensDetails struct {
	TextTokens  int `json:"text_tokens,omitempty"`
	AudioTokens int `json:"audio_tokens,omitempty"`
	ImageTokens int `json:"image_tokens,omitempty"`

	// For Providers which follow OpenAI's spec, CachedTokens means the number of input tokens read from the cache+input tokens used to create the cache entry. (because they do not differentiate between cache creation and cache read tokens)
	// For Providers which do not follow OpenAI's spec, CachedTokens means only the number of input tokens read from the cache.
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type ChatCompletionTokensDetails struct {
	TextTokens               int  `json:"text_tokens,omitempty"`
	AcceptedPredictionTokens int  `json:"accepted_prediction_tokens,omitempty"`
	AudioTokens              int  `json:"audio_tokens,omitempty"`
	CitationTokens           *int `json:"citation_tokens,omitempty"`
	NumSearchQueries         *int `json:"num_search_queries,omitempty"`
	ReasoningTokens          int  `json:"reasoning_tokens,omitempty"`
	ImageTokens              *int `json:"image_tokens,omitempty"`
	RejectedPredictionTokens int  `json:"rejected_prediction_tokens,omitempty"`

	// This means the number of input tokens used to create the cache entry. (cache creation tokens)
	CachedTokens int `json:"cached_tokens,omitempty"` // Not in OpenAI's schemas, but sent by a few providers (Anthropic, Bedrock are some of them)
}

type BifrostCost struct {
	InputTokensCost     float64 `json:"input_tokens_cost,omitempty"`
	OutputTokensCost    float64 `json:"output_tokens_cost,omitempty"`
	ReasoningTokensCost float64 `json:"reasoning_tokens_cost,omitempty"`
	CitationTokensCost  float64 `json:"citation_tokens_cost,omitempty"`
	SearchQueriesCost   float64 `json:"search_queries_cost,omitempty"`
	RequestCost         float64 `json:"request_cost,omitempty"`
	TotalCost           float64 `json:"total_cost,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshalling for BifrostCost.
func (bc *BifrostCost) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct float
	var costFloat float64
	if err := Unmarshal(data, &costFloat); err == nil {
		bc.TotalCost = costFloat
		return nil
	}

	// Try to unmarshal as a full BifrostCost struct
	// Use a type alias to avoid infinite recursion
	type Alias BifrostCost
	var costStruct Alias
	if err := Unmarshal(data, &costStruct); err == nil {
		*bc = BifrostCost(costStruct)
		return nil
	}

	return fmt.Errorf("cost field is neither a float nor an object")
}

type SearchResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Date        *string `json:"date,omitempty"`
	LastUpdated *string `json:"last_updated,omitempty"`
	Snippet     *string `json:"snippet,omitempty"`
	Source      *string `json:"source,omitempty"`
}

type VideoResult struct {
	URL             string   `json:"url"`
	ThumbnailURL    *string  `json:"thumbnail_url,omitempty"`
	ThumbnailWidth  *int     `json:"thumbnail_width,omitempty"`
	ThumbnailHeight *int     `json:"thumbnail_height,omitempty"`
	Duration        *float64 `json:"duration,omitempty"`
}
