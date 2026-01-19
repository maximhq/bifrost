package bedrock

import (
	"encoding/json"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// DefaultBedrockRegion is the default region for Bedrock
const DefaultBedrockRegion = "us-east-1"
const MinimumReasoningMaxTokens = 1
const DefaultCompletionMaxTokens = 4096 // Only used for relative reasoning max token calculation - not passed in body by default

// ==================== REQUEST TYPES ====================

// BedrockTextCompletionRequest represents a Bedrock text completion request
// Combines both Anthropic-style and standard completion parameters
type BedrockTextCompletionRequest struct {
	ModelID string `json:"-"` // Model ID (sent in URL path, not body)

	// Required field
	Prompt string `json:"prompt"` // The text prompt to complete

	// Token control parameters (both naming conventions supported)
	MaxTokens         *int `json:"max_tokens,omitempty"`           // Maximum number of tokens to generate (standard format)
	MaxTokensToSample *int `json:"max_tokens_to_sample,omitempty"` // Maximum number of tokens to generate (Anthropic format)

	// Sampling parameters
	Temperature *float64 `json:"temperature,omitempty"` // Controls randomness in generation (0.0-1.0)
	TopP        *float64 `json:"top_p,omitempty"`       // Nucleus sampling parameter (0.0-1.0)
	TopK        *int     `json:"top_k,omitempty"`       // Top-k sampling parameter

	// Stop sequences (both naming conventions supported)
	Stop          []string `json:"stop,omitempty"`           // Stop sequences (standard format)
	StopSequences []string `json:"stop_sequences,omitempty"` // Stop sequences (Anthropic format)

	// Messages API parameters (Anthropic Claude 3)
	Messages         []BedrockMessage `json:"messages,omitempty"`
	System           interface{}      `json:"system,omitempty"`
	AnthropicVersion string           `json:"anthropic_version,omitempty"`
	Stream           bool             `json:"-"` // Whether streaming is requested (internal)
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *BedrockTextCompletionRequest) IsStreamingRequested() bool {
	return r.Stream
}

type BedrockServiceTier struct {
	Type string `json:"type"` // Service tier type: "reserved" | "priority" | "default" | "flex"
}

// BedrockConverseRequest represents a Bedrock Converse API request
type BedrockConverseRequest struct {
	ModelID                           string                           `json:"-"`                                           // Model ID (sent in URL path, not body)
	Messages                          []BedrockMessage                 `json:"messages,omitempty"`                          // Array of messages for the conversation
	System                            []BedrockSystemMessage           `json:"system,omitempty"`                            // System messages/prompts
	InferenceConfig                   *BedrockInferenceConfig          `json:"inferenceConfig,omitempty"`                   // Inference parameters
	ToolConfig                        *BedrockToolConfig               `json:"toolConfig,omitempty"`                        // Tool configuration
	GuardrailConfig                   *BedrockGuardrailConfig          `json:"guardrailConfig,omitempty"`                   // Guardrail configuration
	AdditionalModelRequestFields      schemas.OrderedMap               `json:"additionalModelRequestFields,omitempty"`      // Model-specific parameters (untyped)
	AdditionalModelResponseFieldPaths []string                         `json:"additionalModelResponseFieldPaths,omitempty"` // Additional response field paths
	PerformanceConfig                 *BedrockPerformanceConfig        `json:"performanceConfig,omitempty"`                 // Performance configuration
	PromptVariables                   map[string]BedrockPromptVariable `json:"promptVariables,omitempty"`                   // Prompt variables for prompt management
	RequestMetadata                   map[string]string                `json:"requestMetadata,omitempty"`                   // Request metadata
	ServiceTier                       *BedrockServiceTier              `json:"serviceTier,omitempty"`                       // Service tier configuration (note: camelCase in both request and response)
	Stream                            bool                             `json:"-"`                                           // Whether streaming is requested (internal, not in JSON)

	// Extra params for advanced use cases
	ExtraParams map[string]interface{} `json:"extra_params,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *BedrockConverseRequest) IsStreamingRequested() bool {
	return r.Stream
}

// Known fields for BedrockConverseRequest
var bedrockConverseRequestKnownFields = map[string]bool{
	"messages":                          true,
	"system":                            true,
	"inferenceConfig":                   true,
	"toolConfig":                        true,
	"guardrailConfig":                   true,
	"additionalModelRequestFields":      true,
	"additionalModelResponseFieldPaths": true,
	"performanceConfig":                 true,
	"promptVariables":                   true,
	"requestMetadata":                   true,
	"serviceTier":                       true,
	"stream":                            true,
	"extra_params":                      true,
	"fallbacks":                         true,
}

// UnmarshalJSON implements custom JSON unmarshalling for BedrockConverseRequest.
// This captures all unregistered fields into ExtraParams.
func (r *BedrockConverseRequest) UnmarshalJSON(data []byte) error {
	// Create an alias type to avoid infinite recursion
	type Alias BedrockConverseRequest

	// First, unmarshal into the alias to populate all known fields
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(r),
	}

	if err := sonic.Unmarshal(data, aux); err != nil {
		return err
	}

	// Parse JSON to extract unknown fields
	var rawData map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &rawData); err != nil {
		return err
	}

	// Initialize ExtraParams if not already initialized
	if r.ExtraParams == nil {
		r.ExtraParams = make(map[string]interface{})
	}

	// Extract unknown fields
	for key, value := range rawData {
		if !bedrockConverseRequestKnownFields[key] {
			var v interface{}
			if err := sonic.Unmarshal(value, &v); err != nil {
				continue // Skip fields that can't be unmarshaled
			}
			r.ExtraParams[key] = v
		}
	}

	return nil
}

type BedrockMessageRole string

const (
	BedrockMessageRoleUser      BedrockMessageRole = "user"
	BedrockMessageRoleAssistant BedrockMessageRole = "assistant"
)

// BedrockMessage represents a message in the conversation
type BedrockMessage struct {
	Role    BedrockMessageRole    `json:"role"`    // Required: "user" or "assistant"
	Content []BedrockContentBlock `json:"content"` // Required: Array of content blocks
}

// BedrockSystemMessage represents a system message
type BedrockSystemMessage struct {
	Text         *string              `json:"text,omitempty"`         // Text system message
	GuardContent *BedrockGuardContent `json:"guardContent,omitempty"` // Guard content for guardrails
	CachePoint   *BedrockCachePoint   `json:"cachePoint,omitempty"`   // Cache point for the system message
}

// BedrockContentBlock represents a content block that can be text, image, document, toolUse, or toolResult
type BedrockContentBlock struct {
	// Text content
	Text *string `json:"text,omitempty"`

	// Image content
	Image *BedrockImageSource `json:"image,omitempty"`

	// Document content
	Document *BedrockDocumentSource `json:"document,omitempty"`

	// Tool use content
	ToolUse *BedrockToolUse `json:"toolUse,omitempty"`

	// Tool result content
	ToolResult *BedrockToolResult `json:"toolResult,omitempty"`

	// Guard content (for guardrails)
	GuardContent *BedrockGuardContent `json:"guardContent,omitempty"`

	// Reasoning content
	ReasoningContent *BedrockReasoningContent `json:"reasoningContent,omitempty"`

	// For Tool Call Result content
	JSON interface{} `json:"json,omitempty"`

	// Cache point for the content block
	CachePoint *BedrockCachePoint `json:"cachePoint,omitempty"`
}

type BedrockCachePointType string

const (
	BedrockCachePointTypeDefault BedrockCachePointType = "default"
)

// BedrockCachePoint
type BedrockCachePoint struct {
	Type BedrockCachePointType `json:"type"`
}

// BedrockImageSource represents image content
type BedrockImageSource struct {
	Format string                 `json:"format"` // Required: Image format (png, jpeg, gif, webp)
	Source BedrockImageSourceData `json:"source"` // Required: Image source data
}

// BedrockImageSourceData represents the source of image data
type BedrockImageSourceData struct {
	Bytes *string `json:"bytes,omitempty"` // Base64-encoded image bytes
}

// BedrockDocumentSource represents document content
type BedrockDocumentSource struct {
	Format string                     `json:"format"` // Required: Document format (pdf, csv, doc, docx, xls, xlsx, html, txt, md)
	Name   string                     `json:"name"`   // Required: Document name
	Source *BedrockDocumentSourceData `json:"source"` // Required: Document source data
}

// BedrockDocumentSourceData represents the source of document data
type BedrockDocumentSourceData struct {
	Bytes *string `json:"bytes,omitempty"` // Base64-encoded document bytes
	Text  *string `json:"text,omitempty"`  // Plain text content
}

// BedrockToolUse represents a tool use request
type BedrockToolUse struct {
	ToolUseID string      `json:"toolUseId"` // Required: Unique identifier for this tool use
	Name      string      `json:"name"`      // Required: Name of the tool to use
	Input     interface{} `json:"input"`     // Required: Input parameters for the tool (JSON object)
}

// BedrockToolResult represents the result of a tool use
type BedrockToolResult struct {
	ToolUseID string                `json:"toolUseId"`        // Required: ID of the tool use this result corresponds to
	Content   []BedrockContentBlock `json:"content"`          // Required: Content of the tool result
	Status    *string               `json:"status,omitempty"` // Optional: Status of tool execution ("success" or "error")
}

// BedrockGuardContent represents guard content for guardrails
type BedrockGuardContent struct {
	Text *BedrockGuardContentText `json:"text,omitempty"`
}

type BedrockReasoningContent struct {
	ReasoningText *BedrockReasoningContentText `json:"reasoningText,omitempty"`
}

type BedrockReasoningContentText struct {
	Text      *string `json:"text,omitempty"`
	Signature *string `json:"signature,omitempty"`
}

// BedrockGuardContentText represents text content for guardrails
type BedrockGuardContentText struct {
	Text       string                    `json:"text"`                 // Required: Text content
	Qualifiers []BedrockContentQualifier `json:"qualifiers,omitempty"` // Optional: Content qualifiers
}

// BedrockContentQualifier represents qualifiers for guard content
type BedrockContentQualifier string

const (
	ContentQualifierGrounding    BedrockContentQualifier = "grounding_source"
	ContentQualifierSearchResult BedrockContentQualifier = "search_result"
	ContentQualifierQuery        BedrockContentQualifier = "query"
)

// BedrockInferenceConfig represents inference configuration parameters
type BedrockInferenceConfig struct {
	MaxTokens     *int     `json:"maxTokens,omitempty"`     // Maximum number of tokens to generate
	StopSequences []string `json:"stopSequences,omitempty"` // Sequences that will stop generation
	Temperature   *float64 `json:"temperature,omitempty"`   // Sampling temperature (0.0 to 1.0)
	TopP          *float64 `json:"topP,omitempty"`          // Top-p sampling parameter (0.0 to 1.0)
}

// BedrockToolConfig represents tool configuration
type BedrockToolConfig struct {
	Tools      []BedrockTool      `json:"tools,omitempty"`      // Available tools
	ToolChoice *BedrockToolChoice `json:"toolChoice,omitempty"` // Tool choice strategy
}

// BedrockTool represents a tool definition
type BedrockTool struct {
	ToolSpec   *BedrockToolSpec   `json:"toolSpec,omitempty"`   // Tool specification
	CachePoint *BedrockCachePoint `json:"cachePoint,omitempty"` // Cache point for the tool
}

// BedrockToolSpec represents the specification of a tool
type BedrockToolSpec struct {
	Name        string                 `json:"name"`                  // Required: Tool name
	Description *string                `json:"description,omitempty"` // Optional: Tool description
	InputSchema BedrockToolInputSchema `json:"inputSchema"`           // Required: JSON schema for tool input
}

// BedrockToolInputSchema represents the input schema for a tool (union type)
type BedrockToolInputSchema struct {
	JSON interface{} `json:"json,omitempty"` // The JSON schema for the tool
}

// BedrockToolChoice represents tool choice configuration
type BedrockToolChoice struct {
	// Union type - only one should be set
	Auto *BedrockToolChoiceAuto `json:"auto,omitempty"`
	Any  *BedrockToolChoiceAny  `json:"any,omitempty"`
	Tool *BedrockToolChoiceTool `json:"tool,omitempty"`
}

// BedrockToolChoiceAuto represents automatic tool choice
type BedrockToolChoiceAuto struct{}

// BedrockToolChoiceAny represents any tool choice
type BedrockToolChoiceAny struct{}

// BedrockToolChoiceTool represents specific tool choice
type BedrockToolChoiceTool struct {
	Name string `json:"name"` // Required: Name of the specific tool to use
}

// BedrockGuardrailConfig represents guardrail configuration
type BedrockGuardrailConfig struct {
	GuardrailIdentifier string  `json:"guardrailIdentifier"` // Required: Guardrail identifier
	GuardrailVersion    string  `json:"guardrailVersion"`    // Required: Guardrail version
	Trace               *string `json:"trace,omitempty"`     // Optional: Trace level ("enabled" or "disabled")
}

// BedrockPerformanceConfig represents performance configuration
type BedrockPerformanceConfig struct {
	Latency *string `json:"latency,omitempty"` // Latency optimization ("standard" or "optimized")
}

// BedrockPromptVariable represents a prompt variable
type BedrockPromptVariable struct {
	Text *string `json:"text,omitempty"` // Text value for the variable
}

// ==================== RESPONSE TYPES ====================

// BedrockAnthropicTextResponse represents the response structure from Bedrock's Anthropic text completion API.
// It includes the completion text and stop reason information.
type BedrockAnthropicTextResponse struct {
	Completion string `json:"completion"`  // Generated completion text
	StopReason string `json:"stop_reason"` // Reason for completion termination
	Stop       string `json:"stop"`        // Stop sequence that caused completion to stop
}

// BedrockMistralTextResponse represents the response structure from Bedrock's Mistral text completion API.
// It includes multiple output choices with their text and stop reasons.
type BedrockMistralTextResponse struct {
	Outputs []struct {
		Text       string `json:"text"`        // Generated text
		StopReason string `json:"stop_reason"` // Reason for completion termination
	} `json:"outputs"` // Array of output choices
}

// BedrockConverseResponse represents a Bedrock Converse API response
type BedrockConverseResponse struct {
	Output                        *BedrockConverseOutput    `json:"output"`                                  // Required: Response output
	StopReason                    string                    `json:"stopReason"`                              // Required: Reason for stopping
	Usage                         *BedrockTokenUsage        `json:"usage"`                                   // Required: Token usage information
	Metrics                       *BedrockConverseMetrics   `json:"metrics"`                                 // Required: Response metrics
	AdditionalModelResponseFields map[string]interface{}    `json:"additionalModelResponseFields,omitempty"` // Optional: Additional model-specific response fields
	PerformanceConfig             *BedrockPerformanceConfig `json:"performanceConfig,omitempty"`             // Optional: Performance configuration used
	ServiceTier                   *BedrockServiceTier       `json:"serviceTier,omitempty"`                   // Optional: Service tier that was used
	Trace                         *BedrockConverseTrace     `json:"trace,omitempty"`                         // Optional: Guardrail trace information
}

// BedrockConverseOutput represents the output of a Converse request (union type)
type BedrockConverseOutput struct {
	Message *BedrockMessage `json:"message,omitempty"` // Generated message (most common case)
}

// BedrockTokenUsage represents token usage information
type BedrockTokenUsage struct {
	InputTokens           int `json:"inputTokens"`           // Number of input tokens
	OutputTokens          int `json:"outputTokens"`          // Number of output tokens
	TotalTokens           int `json:"totalTokens"`           // Total number of tokens (input + output)
	CacheReadInputTokens  int `json:"cacheReadInputTokens"`  // Number of cached tokens read
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"` // Number of cached tokens written
}

// BedrockConverseMetrics represents response metrics
type BedrockConverseMetrics struct {
	LatencyMs int64 `json:"latencyMs"` // Response latency in milliseconds
}

// BedrockConverseTrace represents guardrail trace information
type BedrockConverseTrace struct {
	Guardrail *BedrockGuardrailTrace `json:"guardrail,omitempty"` // Guardrail trace details
}

// BedrockGuardrailTrace represents detailed guardrail trace information
type BedrockGuardrailTrace struct {
	Action            *string                      `json:"action,omitempty"`            // Action taken by guardrail
	InputAssessments  []BedrockGuardrailAssessment `json:"inputAssessments,omitempty"`  // Input assessments
	OutputAssessments []BedrockGuardrailAssessment `json:"outputAssessments,omitempty"` // Output assessments
	Trace             *BedrockGuardrailTraceDetail `json:"trace,omitempty"`             // Detailed trace information
}

// BedrockGuardrailAssessment represents a guardrail assessment
type BedrockGuardrailAssessment struct {
	TopicPolicy         *BedrockGuardrailTopicPolicy         `json:"topicPolicy,omitempty"`         // Topic policy assessment
	ContentPolicy       *BedrockGuardrailContentPolicy       `json:"contentPolicy,omitempty"`       // Content policy assessment
	WordPolicy          *BedrockGuardrailWordPolicy          `json:"wordPolicy,omitempty"`          // Word policy assessment
	SensitiveInfoPolicy *BedrockGuardrailSensitiveInfoPolicy `json:"sensitiveInfoPolicy,omitempty"` // Sensitive information policy assessment
}

// BedrockGuardrailTopicPolicy represents topic policy assessment
type BedrockGuardrailTopicPolicy struct {
	Topics []BedrockGuardrailTopic `json:"topics,omitempty"` // Topics identified
}

// BedrockGuardrailTopic represents a topic identified by guardrail
type BedrockGuardrailTopic struct {
	Name   *string `json:"name,omitempty"`   // Topic name
	Type   *string `json:"type,omitempty"`   // Topic type
	Action *string `json:"action,omitempty"` // Action taken
}

// BedrockGuardrailContentPolicy represents content policy assessment
type BedrockGuardrailContentPolicy struct {
	Filters []BedrockGuardrailContentFilter `json:"filters,omitempty"` // Content filters applied
}

// BedrockGuardrailContentFilter represents a content filter
type BedrockGuardrailContentFilter struct {
	Type       *string `json:"type,omitempty"`       // Filter type
	Confidence *string `json:"confidence,omitempty"` // Confidence level
	Action     *string `json:"action,omitempty"`     // Action taken
}

// BedrockGuardrailWordPolicy represents word policy assessment
type BedrockGuardrailWordPolicy struct {
	CustomWords      []BedrockGuardrailCustomWord      `json:"customWords,omitempty"`      // Custom words detected
	ManagedWordLists []BedrockGuardrailManagedWordList `json:"managedWordLists,omitempty"` // Managed word lists matched
}

// BedrockGuardrailCustomWord represents a custom word detected
type BedrockGuardrailCustomWord struct {
	Match  *string `json:"match,omitempty"`  // Matched word
	Action *string `json:"action,omitempty"` // Action taken
}

// BedrockGuardrailManagedWordList represents a managed word list match
type BedrockGuardrailManagedWordList struct {
	Match  *string `json:"match,omitempty"`  // Matched word
	Type   *string `json:"type,omitempty"`   // Word list type
	Action *string `json:"action,omitempty"` // Action taken
}

// BedrockGuardrailSensitiveInfoPolicy represents sensitive information policy assessment
type BedrockGuardrailSensitiveInfoPolicy struct {
	PIIEntities []BedrockGuardrailPIIEntity `json:"piiEntities,omitempty"` // PII entities detected
	Regexes     []BedrockGuardrailRegex     `json:"regexes,omitempty"`     // Regex patterns matched
}

// BedrockGuardrailPIIEntity represents a PII entity detected
type BedrockGuardrailPIIEntity struct {
	Type   *string `json:"type,omitempty"`   // PII entity type
	Match  *string `json:"match,omitempty"`  // Matched text
	Action *string `json:"action,omitempty"` // Action taken
}

// BedrockGuardrailRegex represents a regex pattern match
type BedrockGuardrailRegex struct {
	Name   *string `json:"name,omitempty"`   // Regex name
	Match  *string `json:"match,omitempty"`  // Matched text
	Action *string `json:"action,omitempty"` // Action taken
}

// BedrockGuardrailTraceDetail represents detailed guardrail trace
type BedrockGuardrailTraceDetail struct {
	Trace *string `json:"trace,omitempty"` // Detailed trace information
}

// ==================== ERROR TYPES ====================

// BedrockError represents a Bedrock API error response
type BedrockError struct {
	Type    string  `json:"__type"`         // Error type
	Message string  `json:"message"`        // Error message
	Code    *string `json:"code,omitempty"` // Optional error code
}

// ==================== STREAMING RESPONSE TYPES ====================

// BedrockConverseStreamResponse represents the overall streaming response structure
type BedrockConverseStreamResponse struct {
	Events []BedrockStreamEvent `json:"-"` // Events are parsed from the stream, not JSON
}

// BedrockStreamEvent represents a union type for all possible streaming events
type BedrockStreamEvent struct {
	// Flat structure matching actual Bedrock API response
	Role              *string                   `json:"role,omitempty"`              // For messageStart events
	ContentBlockIndex *int                      `json:"contentBlockIndex,omitempty"` // For content block events
	Delta             *BedrockContentBlockDelta `json:"delta,omitempty"`             // For contentBlockDelta events
	StopReason        *string                   `json:"stopReason,omitempty"`        // For messageStop events

	// Start field for tool use events
	Start *BedrockContentBlockStart `json:"start,omitempty"` // For contentBlockStart events

	// Metadata and usage (can appear at top level)
	Usage   *BedrockTokenUsage      `json:"usage,omitempty"`   // Usage information
	Metrics *BedrockConverseMetrics `json:"metrics,omitempty"` // Performance metrics
	Trace   *BedrockConverseTrace   `json:"trace,omitempty"`   // Trace information

	// Additional fields
	AdditionalModelResponseFields interface{} `json:"additionalModelResponseFields,omitempty"`

	// For InvokeModelWithResponseStream (Legacy API)
	InvokeModelRawChunk []byte `json:"invokeModelRawChunk,omitempty"` // Raw bytes for legacy invoke stream
}

// BedrockMessageStartEvent indicates the start of a message
type BedrockMessageStartEvent struct {
	Role string `json:"role"` // "assistant" or "user"
}

// BedrockContentBlockStart contains details about the starting content block
type BedrockContentBlockStart struct {
	ToolUse *BedrockToolUseStart `json:"toolUse,omitempty"`
}

// BedrockToolUseStart contains details about a tool use block start
type BedrockToolUseStart struct {
	ToolUseID string `json:"toolUseId"` // Unique identifier for the tool use
	Name      string `json:"name"`      // Name of the tool being used
}

// BedrockContentBlockDelta represents the incremental content
type BedrockContentBlockDelta struct {
	Text             *string                      `json:"text,omitempty"`             // Text content delta
	ReasoningContent *BedrockReasoningContentText `json:"reasoningContent,omitempty"` // Reasoning content delta
	ToolUse          *BedrockToolUseDelta         `json:"toolUse,omitempty"`          // Tool use delta
}

// BedrockToolUseDelta represents incremental tool use content
type BedrockToolUseDelta struct {
	Input string `json:"input"` // Incremental input for the tool (JSON string)
}

// BedrockMessageStopEvent indicates the end of a message
type BedrockMessageStopEvent struct {
	StopReason                    string      `json:"stopReason"`
	AdditionalModelResponseFields interface{} `json:"additionalModelResponseFields,omitempty"`
}

// BedrockMetadataEvent provides metadata about the response
type BedrockMetadataEvent struct {
	Usage   *BedrockTokenUsage      `json:"usage,omitempty"`   // Token usage information
	Metrics *BedrockConverseMetrics `json:"metrics,omitempty"` // Performance metrics
	Trace   *BedrockConverseTrace   `json:"trace,omitempty"`   // Trace information
}

// ==================== EMBEDDING TYPES ====================

// BedrockTitanEmbeddingRequest represents a Bedrock Titan embedding request
type BedrockTitanEmbeddingRequest struct {
	InputText string `json:"inputText"` // Required: Text to embed
	// Note: Titan models have fixed dimensions and don't support the dimensions parameter
	// ExtraParams can be used for any additional model-specific parameters
}

// BedrockTitanEmbeddingResponse represents a Bedrock Titan embedding response
type BedrockTitanEmbeddingResponse struct {
	Embedding           []float32 `json:"embedding"`           // The embedding vector
	InputTextTokenCount int       `json:"inputTextTokenCount"` // Number of tokens in input
}

const TaskTypeTextImage = "TEXT_IMAGE"

// BedrockImageGenerationRequest represents a Bedrock image generation request
type BedrockImageGenerationRequest struct {
	TaskType              *string                   `json:"taskType"`              // Should be "TEXT_IMAGE"
	TextToImageParams     *BedrockTextToImageParams `json:"textToImageParams"`     // Parameters for text-to-image
	ImageGenerationConfig *ImageGenerationConfig    `json:"imageGenerationConfig"` // Image generation config
}

type BedrockTextToImageParams struct {
	Text         string  `json:"text"`                   // Prompt for image generation
	NegativeText *string `json:"negativeText,omitempty"` // Negative prompt for image generation
	Style        *string `json:"style,omitempty"`        // Style for image generation
}

type ImageGenerationConfig struct {
	NumberOfImages *int     `json:"numberOfImages,omitempty"`
	Height         *int     `json:"height,omitempty"`
	Width          *int     `json:"width,omitempty"`
	CfgScale       *float64 `json:"cfgScale,omitempty"`
	Quality        *string  `json:"quality,omitempty"`
	Seed           *int     `json:"seed,omitempty"`
}

// BedrockImageGenerationResponse represents a Bedrock image generation response
type BedrockImageGenerationResponse struct {
	Images    []string `json:"images"`    // list of Base64 encoded images
	MaskImage string   `json:"maskImage"` // Base64 encoded mask image (optional)
	Error     string   `json:"error"`     // error message (if present)
}

// ==================== MODELS TYPES ====================
type BedrockModelLifecycle struct {
	Status string `json:"status"`
}

type BedrockModel struct {
	CustomizationsSupported    []string              `json:"customizationsSupported,omitempty"`
	InferenceTypesSupported    []string              `json:"inferenceTypesSupported,omitempty"`
	InputModalities            []string              `json:"inputModalities,omitempty"`
	ModelArn                   string                `json:"modelArn"`
	ModelID                    string                `json:"modelId"`
	ModelLifecycle             BedrockModelLifecycle `json:"modelLifecycle,omitempty"`
	ModelName                  string                `json:"modelName"`
	OutputModalities           []string              `json:"outputModalities,omitempty"`
	ProviderName               string                `json:"providerName"`
	ResponseStreamingSupported bool                  `json:"responseStreamingSupported"`
}

// BedrockListModelsResponse represents the response from AWS Bedrock's ListFoundationModels API
type BedrockListModelsResponse struct {
	ModelSummaries []BedrockModel `json:"modelSummaries"`
}

// ==================== FILE TYPES (S3 WRAPPER) ====================

// BedrockFileUploadRequest wraps S3 PutObject for Bedrock file operations
type BedrockFileUploadRequest struct {
	Bucket   string `json:"bucket"`             // S3 bucket name
	Key      string `json:"key,omitempty"`      // S3 object key (optional, auto-generated if empty)
	Body     []byte `json:"-"`                  // File content (not serialized to JSON)
	Filename string `json:"filename,omitempty"` // Original filename
	Purpose  string `json:"purpose,omitempty"`  // Purpose of the file (e.g., "batch")
}

// BedrockFileUploadResponse wraps S3 PutObject response
type BedrockFileUploadResponse struct {
	S3Uri       string `json:"s3Uri"`                 // Full S3 URI (s3://bucket/key)
	ETag        string `json:"etag,omitempty"`        // S3 ETag
	Bucket      string `json:"bucket"`                // S3 bucket name
	Key         string `json:"key"`                   // S3 object key
	SizeBytes   int64  `json:"sizeBytes"`             // File size in bytes
	ContentType string `json:"contentType,omitempty"` // MIME content type
	CreatedAt   int64  `json:"createdAt,omitempty"`   // Unix timestamp of creation
}

// BedrockFileListRequest wraps S3 ListObjectsV2 request
type BedrockFileListRequest struct {
	Bucket            string `json:"bucket"`                      // S3 bucket name
	Prefix            string `json:"prefix,omitempty"`            // S3 key prefix filter
	MaxKeys           int    `json:"maxKeys,omitempty"`           // Maximum number of keys to return
	ContinuationToken string `json:"continuationToken,omitempty"` // Pagination token
}

// BedrockFileListResponse wraps S3 ListObjectsV2 response
type BedrockFileListResponse struct {
	Files                 []BedrockFileInfo `json:"files"`                           // List of file info
	IsTruncated           bool              `json:"isTruncated"`                     // Whether there are more results
	NextContinuationToken string            `json:"nextContinuationToken,omitempty"` // Token for next page
}

// BedrockFileInfo represents S3 object metadata
type BedrockFileInfo struct {
	S3Uri        string `json:"s3Uri"`                  // Full S3 URI
	Key          string `json:"key"`                    // S3 object key
	SizeBytes    int64  `json:"sizeBytes"`              // File size in bytes
	LastModified int64  `json:"lastModified,omitempty"` // Unix timestamp of last modification
	ETag         string `json:"etag,omitempty"`         // S3 ETag
}

// BedrockFileRetrieveRequest wraps S3 HeadObject request
type BedrockFileRetrieveRequest struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
	S3Uri  string `json:"s3Uri"` // Full S3 URI (s3://bucket/key)
	ETag   string `json:"etag"`  // S3 ETag
}

// BedrockFileRetrieveResponse wraps S3 HeadObject response
type BedrockFileRetrieveResponse struct {
	S3Uri        string `json:"s3Uri"`                  // Full S3 URI
	Key          string `json:"key"`                    // S3 object key
	SizeBytes    int64  `json:"sizeBytes"`              // File size in bytes
	LastModified int64  `json:"lastModified,omitempty"` // Unix timestamp of last modification
	ContentType  string `json:"contentType,omitempty"`  // MIME content type
	ETag         string `json:"etag,omitempty"`         // S3 ETag
}

// BedrockFileDeleteRequest wraps S3 DeleteObject request
type BedrockFileDeleteRequest struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
	S3Uri  string `json:"s3Uri"` // Full S3 URI (s3://bucket/key)
	ETag   string `json:"etag"`  // S3 ETag
}

// BedrockFileDeleteResponse wraps S3 DeleteObject response
type BedrockFileDeleteResponse struct {
	S3Uri   string `json:"s3Uri"`   // Full S3 URI that was deleted
	Deleted bool   `json:"deleted"` // Whether deletion was successful
}

// BedrockFileContentRequest wraps S3 GetObject request
type BedrockFileContentRequest struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix,omitempty"`
	S3Uri  string `json:"s3Uri"` // Full S3 URI (s3://bucket/key)
	ETag   string `json:"etag"`  // S3 ETag
}

// BedrockFileContentResponse wraps S3 GetObject response
type BedrockFileContentResponse struct {
	S3Uri       string `json:"s3Uri"`                 // Full S3 URI
	Content     []byte `json:"-"`                     // File content (not serialized to JSON)
	ContentType string `json:"contentType,omitempty"` // MIME content type
	SizeBytes   int64  `json:"sizeBytes"`             // File size in bytes
}
