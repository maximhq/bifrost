package cohere

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

const MinimumReasoningMaxTokens = 1
const DefaultCompletionMaxTokens = 4096 // Only used for relative reasoning max token calculation - not passed in body by default
// Limits for tokenize input api call https://docs.cohere.com/reference/tokenize#request
const (
	cohereTokenizeMinTextLength = 1
	cohereTokenizeMaxTextLength = 65536
)

// ==================== REQUEST TYPES ====================

// CohereChatRequest represents a Cohere  chat completion request
type CohereChatRequest struct {
	Model            string                  `json:"model"`                        // Required: Model to use for chat completion
	Messages         []CohereMessage         `json:"messages"`                     // Required: Array of message objects
	Tools            []CohereChatRequestTool `json:"tools,omitempty"`              // Optional: Tools available for the model
	ToolChoice       *CohereToolChoice       `json:"tool_choice,omitempty"`        // Optional: Tool choice configuration
	Temperature      *float64                `json:"temperature,omitempty"`        // Optional: Sampling temperature
	P                *float64                `json:"p,omitempty"`                  // Optional: Top-p sampling
	K                *int                    `json:"k,omitempty"`                  // Optional: Top-k sampling
	MaxTokens        *int                    `json:"max_tokens,omitempty"`         // Optional: Maximum tokens to generate
	StopSequences    []string                `json:"stop_sequences,omitempty"`     // Optional: Stop sequences
	FrequencyPenalty *float64                `json:"frequency_penalty,omitempty"`  // Optional: Frequency penalty
	PresencePenalty  *float64                `json:"presence_penalty,omitempty"`   // Optional: Presence penalty
	Stream           *bool                   `json:"stream,omitempty"`             // Optional: Enable streaming
	SafetyMode       *string                 `json:"safety_mode,omitempty"`        // Optional: Safety mode
	LogProbs         *bool                   `json:"log_probs,omitempty"`          // Optional: Log probabilities
	StrictToolChoice *bool                   `json:"strict_tool_choice,omitempty"` // Optional: Strict tool choice
	Thinking         *CohereThinking         `json:"thinking,omitempty"`           // Optional: Reasoning configuration
	ResponseFormat   *CohereResponseFormat   `json:"response_format,omitempty"`    // Optional: Format for the response
	ExtraParams      map[string]interface{}  `json:"-"`                            // Optional: Extra parameters
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *CohereChatRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

func (r *CohereChatRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

type CohereChatRequestTool struct {
	Type     string                    `json:"type"` // always "function"
	Function CohereChatRequestFunction `json:"function"`
}

type CohereChatRequestFunction struct {
	Name        string      `json:"name"`                  // Function name
	Parameters  interface{} `json:"parameters,omitempty"`  // Function parameters (JSON string)
	Description *string     `json:"description,omitempty"` // Optional: Function description
}

// CohereMessage represents a message in Cohere  format
type CohereMessage struct {
	Role       string                `json:"role"`                   // Required: Message role (system, user, assistant, tool)
	Content    *CohereMessageContent `json:"content,omitempty"`      // Optional: Message content (string or array of content blocks)
	ToolCalls  []CohereToolCall      `json:"tool_calls,omitempty"`   // Optional: Tool calls (for assistant messages)
	ToolCallID *string               `json:"tool_call_id,omitempty"` // Optional: Tool call ID (for tool messages)
	ToolPlan   *string               `json:"tool_plan,omitempty"`    // Optional: Chain-of-thought style reflection (assistant only)
}

// CohereMessageContent represents flexible content that can be string or content blocks
type CohereMessageContent struct {
	// Use custom marshaling to handle string or []CohereContentBlock
	StringContent *string              `json:"-"`
	BlocksContent []CohereContentBlock `json:"-"`
}

// MarshalJSON implements custom JSON marshaling for CohereMessageContent
func (c *CohereMessageContent) MarshalJSON() ([]byte, error) {
	if c.StringContent != nil {
		return json.Marshal(*c.StringContent)
	}
	if c.BlocksContent != nil {
		return json.Marshal(c.BlocksContent)
	}
	return []byte("null"), nil
}

// UnmarshalJSON implements custom JSON unmarshaling for CohereMessageContent
func (c *CohereMessageContent) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		c.StringContent = &str
		return nil
	}

	// Try to unmarshal as content blocks array
	var blocks []CohereContentBlock
	if err := json.Unmarshal(data, &blocks); err == nil {
		c.BlocksContent = blocks
		return nil
	}

	return fmt.Errorf("content must be either string or array of content blocks")
}

// Helper methods for CohereMessageContent

// NewStringContent creates a CohereMessageContent with string content
func NewStringContent(content string) *CohereMessageContent {
	return &CohereMessageContent{
		StringContent: &content,
	}
}

// NewBlocksContent creates a CohereMessageContent with content blocks
func NewBlocksContent(blocks []CohereContentBlock) *CohereMessageContent {
	return &CohereMessageContent{
		BlocksContent: blocks,
	}
}

// IsString returns true if content is a string
func (c *CohereMessageContent) IsString() bool {
	return c.StringContent != nil
}

// IsBlocks returns true if content is content blocks
func (c *CohereMessageContent) IsBlocks() bool {
	return c.BlocksContent != nil
}

// GetString returns the string content (nil if not string)
func (c *CohereMessageContent) GetString() *string {
	return c.StringContent
}

// GetBlocks returns the content blocks (nil if not blocks)
func (c *CohereMessageContent) GetBlocks() []CohereContentBlock {
	return c.BlocksContent
}

type CohereContentBlockType string

const (
	CohereContentBlockTypeText     CohereContentBlockType = "text"
	CohereContentBlockTypeImage    CohereContentBlockType = "image_url"
	CohereContentBlockTypeThinking CohereContentBlockType = "thinking"
	CohereContentBlockTypeDocument CohereContentBlockType = "document"
)

// CohereContentBlock represents a content block in Cohere  format
// This is a union type that can be text, image_url, thinking, or document
type CohereContentBlock struct {
	Type CohereContentBlockType `json:"type"` // Required: Content block type

	// Text content block
	Text *string `json:"text,omitempty"`

	// Image URL content block
	ImageURL *CohereImageURL `json:"image_url,omitempty"`

	// Thinking content block (assistant only)
	Thinking *string `json:"thinking,omitempty"`

	// Document content block (tool messages)
	Document *CohereDocument `json:"document,omitempty"`
}

// CohereImageURL represents an image URL content block
type CohereImageURL struct {
	URL string `json:"url"` // Required: Image URL
}

// CohereDocument represents a document content block
type CohereDocument struct {
	Data schemas.OrderedMap `json:"data"`         // Required: Document data as key-value pairs
	ID   *string            `json:"id,omitempty"` // Optional: Document ID for citations
}

// CohereThinking represents reasoning configuration
type CohereThinking struct {
	Type        CohereThinkingType `json:"type"`                   // Required: Reasoning type (enabled, disabled)
	TokenBudget *int               `json:"token_budget,omitempty"` // Optional: Maximum thinking tokens (>=1)
}

// CohereThinkingType represents the type of reasoning
type CohereThinkingType string

const (
	ThinkingTypeEnabled  CohereThinkingType = "enabled"
	ThinkingTypeDisabled CohereThinkingType = "disabled"
)

// CohereResponseFormat represents the response format configuration for Cohere chat requests
type CohereResponseFormat struct {
	Type       CohereResponseFormatType `json:"type"`             // Required: Response format type
	JSONSchema *interface{}             `json:"schema,omitempty"` // Optional: JSON schema for structured output (not used when type is "text")
}

// CohereResponseFormatType represents the type of response format
type CohereResponseFormatType string

const (
	ResponseFormatTypeText       CohereResponseFormatType = "text"
	ResponseFormatTypeJSONObject CohereResponseFormatType = "json_object"
)

// CohereToolChoice represents tool choice configuration
type CohereToolChoice string

const (
	ToolChoiceRequired CohereToolChoice = "REQUIRED"
	ToolChoiceNone     CohereToolChoice = "NONE"
	ToolChoiceAuto     CohereToolChoice = "AUTO"
)

// CohereToolCall represents a tool call in Cohere  format
type CohereToolCall struct {
	ID       *string         `json:"id,omitempty"` // Optional: Tool call ID
	Type     string          `json:"type"`         // Required: Tool call type (must be "function")
	Function *CohereFunction `json:"function"`     // Required: Function call details
}

// CohereFunction represents a function call
type CohereFunction struct {
	Name      *string `json:"name,omitempty"`      // Optional: Function name
	Arguments string  `json:"arguments,omitempty"` // Optional: Function arguments (JSON string)
}

// CohereParameterDefinition represents a parameter definition for a Cohere tool.
// It defines the type, description, and whether the parameter is required.
type CohereParameterDefinition struct {
	Type        string  `json:"type"`                  // Type of the parameter
	Description *string `json:"description,omitempty"` // Optional description of the parameter
	Required    bool    `json:"required"`              // Whether the parameter is required
}

// CohereTool represents a tool definition for the Cohere API.
// It includes the tool's name, description, and parameter definitions.
type CohereTool struct {
	Name                 string                               `json:"name"`                  // Name of the tool
	Description          string                               `json:"description"`           // Description of the tool
	ParameterDefinitions map[string]CohereParameterDefinition `json:"parameter_definitions"` // Definitions of the tool's parameters
}

// CohereCountTokensRequest represents a Cohere tokenize request
type CohereCountTokensRequest struct {
	Model       string                 `json:"model"` // Required: Model whose tokenizer should be used
	Text        string                 `json:"text"`  // Required: Text to tokenize (1-65536 chars)
	ExtraParams map[string]interface{} `json:"-"`     // Optional: Extra parameters
}

func (r *CohereCountTokensRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// CohereEmbeddingRequest represents a Cohere embedding request
type CohereEmbeddingRequest struct {
	Model           string                 `json:"model"`                      // Required: ID of embedding model
	InputType       string                 `json:"input_type"`                 // Required: Type of input for v3+ models
	Texts           []string               `json:"texts,omitempty"`            // Optional: Array of strings to embed (max 96)
	Images          []string               `json:"images,omitempty"`           // Optional: Array of image data URIs (max 1)
	Inputs          []CohereEmbeddingInput `json:"inputs,omitempty"`           // Optional: Array of mixed text/image inputs (max 96)
	MaxTokens       *int                   `json:"max_tokens,omitempty"`       // Optional: Max tokens to embed per input
	OutputDimension *int                   `json:"output_dimension,omitempty"` // Optional: Embedding dimensions (256, 512, 1024, 1536)
	EmbeddingTypes  []string               `json:"embedding_types,omitempty"`  // Optional: Types of embeddings to return
	Truncate        *string                `json:"truncate,omitempty"`         // Optional: How to handle long inputs
	ExtraParams     map[string]interface{} `json:"-"`                          // Optional: Extra parameters
}

func (r *CohereEmbeddingRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// CohereEmbeddingInput represents a mixed text/image input
type CohereEmbeddingInput struct {
	Content []CohereContentBlock `json:"content"` // Required: Array of content blocks (reuses chat content blocks)
}

// CohereEmbeddingResponse represents a Cohere embedding response
type CohereEmbeddingResponse struct {
	ID           string                     `json:"id"`                      // Response ID
	Embeddings   *CohereEmbeddingData       `json:"embeddings,omitempty"`    // Embedding data object
	ResponseType *string                    `json:"response_type,omitempty"` // Response type (embeddings_floats, embeddings_by_type)
	Texts        []string                   `json:"texts,omitempty"`         // Original text entries
	Images       []CohereEmbeddingImageInfo `json:"images,omitempty"`        // Original image entries
	Meta         *CohereEmbeddingMeta       `json:"meta,omitempty"`          // Response metadata
}

// CohereEmbeddingData represents the embeddings object with different types
type CohereEmbeddingData struct {
	Float   [][]float32 `json:"float,omitempty"`   // Float embeddings
	Int8    [][]int8    `json:"int8,omitempty"`    // Int8 embeddings
	Uint8   [][]uint8   `json:"uint8,omitempty"`   // Uint8 embeddings
	Binary  [][]int8    `json:"binary,omitempty"`  // Binary embeddings
	Ubinary [][]uint8   `json:"ubinary,omitempty"` // Unsigned binary embeddings
	Base64  []string    `json:"base64,omitempty"`  // Base64 embeddings
}

// CohereEmbeddingImageInfo represents image information in the response
type CohereEmbeddingImageInfo struct {
	Width    int64  `json:"width"`     // Width in pixels
	Height   int64  `json:"height"`    // Height in pixels
	Format   string `json:"format"`    // Image format
	BitDepth int64  `json:"bit_depth"` // Bit depth
}

// CohereEmbeddingMeta represents metadata in embedding response
type CohereEmbeddingMeta struct {
	APIVersion  *CohereEmbeddingAPIVersion `json:"api_version,omitempty"`  // API version info
	BilledUnits *CohereBilledUnits         `json:"billed_units,omitempty"` // Billing information
	Tokens      *CohereTokenUsage          `json:"tokens,omitempty"`       // Token usage
	Warnings    []string                   `json:"warnings,omitempty"`     // Any warnings
}

// CohereEmbeddingAPIVersion represents API version information
type CohereEmbeddingAPIVersion struct {
	Version        *string `json:"version,omitempty"`         // API version
	IsDeprecated   *bool   `json:"is_deprecated,omitempty"`   // Deprecation status
	IsExperimental *bool   `json:"is_experimental,omitempty"` // Experimental status
}

// ==================== RESPONSE TYPES ====================

// CohereCountTokensResponse represents the response from the tokenize endpoint
type CohereCountTokensResponse struct {
	Tokens       []int               `json:"tokens"`
	TokenStrings []string            `json:"token_strings,omitempty"`
	Meta         *CohereTokenizeMeta `json:"meta,omitempty"`
}

// CohereTokenizeMeta captures metadata returned by the tokenize endpoint
type CohereTokenizeMeta struct {
	APIVersion *CohereTokenizeAPIVersion `json:"api_version,omitempty"`
}

// CohereTokenizeAPIVersion describes API version metadata
type CohereTokenizeAPIVersion struct {
	Version *string `json:"version,omitempty"`
}

// CohereChatResponse represents a Cohere  chat completion response
type CohereChatResponse struct {
	ID           string              `json:"id"`                      // Unique identifier for the generated reply
	FinishReason *CohereFinishReason `json:"finish_reason,omitempty"` // Reason for completion
	Message      *CohereMessage      `json:"message,omitempty"`       // Generated message from assistant
	Usage        *CohereUsage        `json:"usage,omitempty"`         // Token usage information
	LogProbs     []CohereLogProb     `json:"logprobs,omitempty"`      // Log probabilities (if requested)
}

// CohereFinishReason represents the reason a chat request has finished
type CohereFinishReason string

const (
	FinishReasonComplete     CohereFinishReason = "COMPLETE"      // Model finished sending complete message
	FinishReasonStopSequence CohereFinishReason = "STOP_SEQUENCE" // Stop sequence was reached
	FinishReasonMaxTokens    CohereFinishReason = "MAX_TOKENS"    // Max tokens exceeded
	FinishReasonToolCall     CohereFinishReason = "TOOL_CALL"     // Model generated tool call
	FinishReasonError        CohereFinishReason = "ERROR"         // Generation failed due to internal error
	FinishReasonTimeout      CohereFinishReason = "TIMEOUT"       // Timeout
)

// CohereUsage represents token usage information
type CohereUsage struct {
	BilledUnits  *CohereBilledUnits `json:"billed_units,omitempty"`  // Billed usage information
	Tokens       *CohereTokenUsage  `json:"tokens,omitempty"`        // Token usage details
	CachedTokens *int               `json:"cached_tokens,omitempty"` // Cached tokens
}

// CohereBilledUnits represents billed usage information
type CohereBilledUnits struct {
	InputTokens     *int `json:"input_tokens,omitempty"`    // Number of billed input tokens
	OutputTokens    *int `json:"output_tokens,omitempty"`   // Number of billed output tokens
	SearchUnits     *int `json:"search_units,omitempty"`    // Number of billed search units
	Classifications *int `json:"classifications,omitempty"` // Number of billed classification units
}

// CohereTokenUsage represents detailed token usage
type CohereTokenUsage struct {
	InputTokens  *int `json:"input_tokens"`  // Number of input tokens used
	OutputTokens *int `json:"output_tokens"` // Number of output tokens produced
}

// CohereLogProb represents log probability information
type CohereLogProb struct {
	TokenIDs []int     `json:"token_ids"`          // Token IDs of each token in text chunk
	Text     *string   `json:"text,omitempty"`     // Text chunk for log probabilities
	LogProbs []float64 `json:"logprobs,omitempty"` // Log probability of each token
}

type CohereCitationType string

const (
	CitationTypeTextContent     CohereCitationType = "TEXT_CONTENT"
	CitationTypeThinkingContent CohereCitationType = "THINKING_CONTENT"
	CitationTypePlan            CohereCitationType = "PLAN"
)

type CohereSourceType string

const (
	SourceTypeTool     CohereSourceType = "tool"
	SourceTypeDocument CohereSourceType = "document"
)

// CohereCitation represents a citation in the response
type CohereCitation struct {
	Start        int                `json:"start"`             // Start position of cited text
	End          int                `json:"end"`               // End position of cited text
	Text         string             `json:"text"`              // Cited text
	Sources      []CohereSource     `json:"sources,omitempty"` // Citation sources
	ContentIndex int                `json:"content_index"`     // Content index of the citation
	Type         CohereCitationType `json:"type"`              // Type of citation
}

// CohereSource represents a citation source
type CohereSource struct {
	Type       CohereSourceType `json:"type"`                  // Source type ("tool" or "document")
	ID         *string          `json:"id,omitempty"`          // Source ID (nullable)
	ToolOutput *map[string]any  `json:"tool_output,omitempty"` // Tool output (for tool sources)
	Document   *map[string]any  `json:"document,omitempty"`    // Document data (for document sources)
}

// ==================== STREAMING TYPES ====================

// CohereStreamEventType represents the type of streaming event
type CohereStreamEventType string

const (
	StreamEventMessageStart  CohereStreamEventType = "message-start"
	StreamEventContentStart  CohereStreamEventType = "content-start"
	StreamEventContentDelta  CohereStreamEventType = "content-delta"
	StreamEventContentEnd    CohereStreamEventType = "content-end"
	StreamEventToolPlanDelta CohereStreamEventType = "tool-plan-delta"
	StreamEventToolCallStart CohereStreamEventType = "tool-call-start"
	StreamEventToolCallDelta CohereStreamEventType = "tool-call-delta"
	StreamEventToolCallEnd   CohereStreamEventType = "tool-call-end"
	StreamEventCitationStart CohereStreamEventType = "citation-start"
	StreamEventCitationEnd   CohereStreamEventType = "citation-end"
	StreamEventMessageEnd    CohereStreamEventType = "message-end"
	StreamEventDebug         CohereStreamEventType = "debug"
)

// CohereStreamEvent represents a unified streaming event from Cohere  API
type CohereStreamEvent struct {
	Type  CohereStreamEventType `json:"type"`
	ID    *string               `json:"id,omitempty"`    // For message-start
	Index *int                  `json:"index,omitempty"` // For indexed events
	Delta *CohereStreamDelta    `json:"delta,omitempty"`
}

// CohereStreamDelta represents the delta content in streaming events
type CohereStreamDelta struct {
	Message      *CohereStreamMessage `json:"message,omitempty"`
	FinishReason *CohereFinishReason  `json:"finish_reason,omitempty"`
	Usage        *CohereUsage         `json:"usage,omitempty"`
}

type CohereStreamToolCallStruct struct {
	CohereToolCallObject *CohereToolCall
	CohereToolCallArray  []CohereToolCall
}

// JSON marshaling for CohereStreamToolCall
func (c *CohereStreamToolCallStruct) MarshalJSON() ([]byte, error) {
	if c.CohereToolCallObject != nil {
		return sonic.Marshal(c.CohereToolCallObject)
	}
	if c.CohereToolCallArray != nil {
		return sonic.Marshal(c.CohereToolCallArray)
	}
	return sonic.Marshal(nil)
}

func (c *CohereStreamToolCallStruct) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	// Try to unmarshal as array first
	var toolCallArray []CohereToolCall
	if err := sonic.Unmarshal(data, &toolCallArray); err == nil {
		c.CohereToolCallArray = toolCallArray
		return nil
	}

	// Try to unmarshal as single object
	var toolCallObject CohereToolCall
	if err := sonic.Unmarshal(data, &toolCallObject); err == nil {
		c.CohereToolCallObject = &toolCallObject
		return nil
	}

	return fmt.Errorf("tool_calls field is neither array nor object")
}

type CohereStreamContentStruct struct {
	CohereStreamContentObject *CohereStreamContent
	CohereStreamContentArray  []CohereStreamContent
}

func (c *CohereStreamContentStruct) MarshalJSON() ([]byte, error) {
	if c.CohereStreamContentObject != nil {
		return sonic.Marshal(c.CohereStreamContentObject)
	}
	if c.CohereStreamContentArray != nil {
		return sonic.Marshal(c.CohereStreamContentArray)
	}
	return sonic.Marshal(nil)
}

func (c *CohereStreamContentStruct) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	// Try to unmarshal as array first
	var contentArray []CohereStreamContent
	if err := sonic.Unmarshal(data, &contentArray); err == nil {
		c.CohereStreamContentArray = contentArray
		return nil
	}

	// Try to unmarshal as single object
	var contentObject CohereStreamContent
	if err := sonic.Unmarshal(data, &contentObject); err == nil {
		c.CohereStreamContentObject = &contentObject
		return nil
	}

	return fmt.Errorf("content field is neither array nor object")
}

type CohereStreamCitationStruct struct {
	CohereStreamCitationObject *CohereCitation
	CohereStreamCitationArray  []CohereCitation
}

func (c *CohereStreamCitationStruct) MarshalJSON() ([]byte, error) {
	if c.CohereStreamCitationObject != nil {
		return sonic.Marshal(c.CohereStreamCitationObject)
	}
	if c.CohereStreamCitationArray != nil {
		return sonic.Marshal(c.CohereStreamCitationArray)
	}
	return sonic.Marshal(nil)
}

func (c *CohereStreamCitationStruct) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	// Try to unmarshal as array first
	var citationArray []CohereCitation
	if err := sonic.Unmarshal(data, &citationArray); err == nil {
		c.CohereStreamCitationArray = citationArray
		return nil
	}

	// Try to unmarshal as single object
	var citationObject CohereCitation
	if err := sonic.Unmarshal(data, &citationObject); err == nil {
		c.CohereStreamCitationObject = &citationObject
		return nil
	}

	return fmt.Errorf("citations field is neither array nor object")
}

// CohereStreamMessage represents the message part of streaming deltas
type CohereStreamMessage struct {
	Role      *string                     `json:"role,omitempty"`       // For message-start
	Content   *CohereStreamContentStruct  `json:"content,omitempty"`    // For content events (object)
	ToolPlan  *string                     `json:"tool_plan,omitempty"`  // For tool-plan-delta
	ToolCalls *CohereStreamToolCallStruct `json:"tool_calls,omitempty"` // For tool-call events (flexible)
	Citations *CohereStreamCitationStruct `json:"citations,omitempty"`  // For citation events
}

// CohereStreamContent represents content in streaming events
type CohereStreamContent struct {
	Type     CohereContentBlockType `json:"type,omitempty"`     // For content-start
	Text     *string                `json:"text,omitempty"`     // For content deltas
	Thinking *string                `json:"thinking,omitempty"` // For thinking deltas
}

// ==================== ERROR TYPES ====================

// CohereError represents an error response from the Cohere  API
type CohereError struct {
	Type    string  `json:"type"`           // Error type
	Message string  `json:"message"`        // Error message
	Code    *string `json:"code,omitempty"` // Optional error code
}

// ==================== MODEL TYPES ====================

type CohereModel struct {
	Name             string   `json:"name"`
	IsDeprecated     bool     `json:"is_deprecated"`
	Endpoints        []string `json:"endpoints"`
	Finetuned        bool     `json:"finetuned"`
	ContextLength    int      `json:"context_length"`
	TokenizerURL     string   `json:"tokenizer_url"`
	DefaultEndpoints []string `json:"default_endpoints"`
	Features         []string `json:"features"`
}

type CohereListModelsResponse struct {
	Models        []CohereModel `json:"models"`
	NextPageToken string        `json:"next_page_token"`
}
