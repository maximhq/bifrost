package schemas

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

type ResponsesAPIExtendedRequestParams struct {
	Background         *bool           `json:"background,omitempty"`
	Conversation       *string         `json:"conversation,omitempty"`
	Include            *[]string       `json:"include,omitempty"` // Supported values: "web_search_call.action.sources", "code_interpreter_call.outputs", "computer_call_output.output.image_url", "file_search_call.results", "message.input_image.image_url", "message.output_text.logprobs", "reasoning.encrypted_content"
	Instructions       *string         `json:"instructions,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	MaxToolCalls       *int            `json:"max_tool_calls,omitempty"`
	MetaData           *map[string]any `json:"meta_data,omitempty"`
	Model              string          `json:"model"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID *string         `json:"previous_response_id,omitempty"`
	PromptCacheKey     *string         `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier   *string         `json:"safety_identifier,omitempty"`
	ServiceTier        *string         `json:"service_tier,omitempty"`
	Store              *bool           `json:"store,omitempty"`
	Stream             *bool           `json:"stream,omitempty"`
	StreamOptions      *StreamOptions  `json:"stream_options,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	Text               *TextConfig     `json:"text,omitempty"`
	TopLogProbs        *int            `json:"top_logprobs,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
	Truncation         *string         `json:"truncation,omitempty"`
}

type ResponseAPIExtendedResponse struct {
	*ResponsesAPIExtendedRequestParams

	CreatedAt         int                               `json:"created_at"`                   // Unix timestamp when Response was created
	Conversation      *ResponsesAPIResponseConversation `json:"conversation,omitempty"`       // The conversation that this response belongs to
	IncompleteDetails *ResponsesAPIResponseIncomplete   `json:"incomplete_details,omitempty"` // Details about why the response is incomplete
	Instructions      *[]BifrostMessage                 `json:"instructions,omitempty"`
	Output            *[]BifrostMessage                 `json:"output,omitempty"`
	Prompt            *ResponsesAPIPrompt               `json:"prompt,omitempty"`           // Reference to a prompt template and variables
	PromptCacheKey    *string                           `json:"prompt_cache_key,omitempty"` // Used by OpenAI to cache responses
	Reasoning         *ResponsesAPIReasoning            `json:"reasoning,omitempty"`        // Configuration options for reasoning models
	Text              *TextConfig                       `json:"text,omitempty"`             // Configuration for text response
	Truncation        *string                           `json:"truncation,omitempty"`       // Truncation strategy ("auto" | "disabled")
}

type ResponseAPIStreamResponse struct {
	*ResponseAPIExtendedResponse

	Instructions *[]InputMessage  `json:"instructions,omitempty"`
	Output       *[]OutputMessage `json:"output,omitempty"`
}

type ResponsesAPIPrompt struct {
	ID        string         `json:"id"`
	Variables map[string]any `json:"variables"`
	Version   *string        `json:"version,omitempty"`
}

type ResponsesAPIReasoning struct {
	Effort          *string `json:"effort,omitempty"`           // "minimal" | "low" | "medium" | "high"
	GenerateSummary *string `json:"generate_summary,omitempty"` // Deprecated: use summary instead
	Summary         *string `json:"summary,omitempty"`          // "auto" | "concise" | "detailed"
}

type ResponsesAPIResponseConversation struct {
	ID string `json:"id"` // The unique ID of the conversation
}

type ResponsesAPIResponseError struct {
	Code    string `json:"code"`    // The error code for the response
	Message string `json:"message"` // A human-readable description of the error
}

type ResponsesAPIResponseIncomplete struct {
	Reason string `json:"reason"` // The reason why the response is incomplete
}

type ResponsesAPIExtendedResponseUsage struct {
	InputTokens         int                               `json:"input_tokens"`          // Number of input tokens
	InputTokensDetails  *ResponsesAPIResponseInputTokens  `json:"input_tokens_details"`  // Detailed breakdown of input tokens
	OutputTokens        int                               `json:"output_tokens"`         // Number of output tokens
	OutputTokensDetails *ResponsesAPIResponseOutputTokens `json:"output_tokens_details"` // Detailed breakdown of output tokens
}

type ResponsesAPIResponseUsage struct {
	*ResponsesAPIExtendedResponseUsage
	TotalTokens int `json:"total_tokens"` // Total number of tokens used
}

type ResponsesAPIResponseInputTokens struct {
	CachedTokens int `json:"cached_tokens"` // Tokens retrieved from cache
}

type ResponsesAPIResponseOutputTokens struct {
	ReasoningTokens int `json:"reasoning_tokens"` // Number of reasoning tokens
}

// =============================================================================
// 2. INPUT MESSAGE STRUCTURES
// =============================================================================

type ResponsesAPIExtendedBifrostMessage struct {
	ID     *string `json:"id,omitempty"`     // Common ID field for most item types
	Type   *string `json:"type"`             // "message" | "file_search_call" | "computer_call" | "computer_call_output" | "web_search_call" | "function_call" | "function_call_output" | "code_interpreter_call" | "local_shell_call" | "local_shell_call_output" | "mcp_call" | "custom_tool_call" | "custom_tool_call_output" | "image_generation_call" | "mcp_list_tools" | "mcp_approval_request" | "mcp_approval_responses" | "reasoning" | "item_reference" | "refusal"
	Status *string `json:"status,omitempty"` // "in_progress" | "completed" | "incomplete" | "interpreting" | "failed"

	*InputItemReference
}

type ResponsesAPIToolMessage struct {
	Name   *string `json:"name,omitempty"`    // Common name field for tool calls
	CallID *string `json:"call_id,omitempty"` // Common call ID for tool calls and outputs

	// Tool calls and outputs
	*ComputerToolCallOutput
	*FunctionToolCallOutput
	*CodeInterpreterToolCall
	*LocalShellCallOutput
	*CustomToolCallOutput
	*ImageGenerationCall
	*FileSearchToolCall
	*MCPToolCall

	// MCP-specific
	*MCPListTools
	*MCPApprovalRequest
	*MCPApprovalResponse
}

type ResponsesAPIExtendedAssistantMessage struct {
	Text     string                      `json:"text"`
	LogProbs *[]OutputMessageTextLogProb `json:"logprobs,omitempty"` // Token log probabilities

	*ComputerToolCall
	*FunctionToolCall
	*LocalShellCall
	*CustomToolCall
	*WebSearchToolCall

	*Reasoning
}

// ResponsesAPIInputItem is a union type that can contain different types of input items
// Only one of the fields should be set at a time
type ResponsesAPIInputItem struct {
	Type   *string `json:"type"`              // "message" | "file_search_call" | "computer_call" | "computer_call_output" | "web_search_call" | "function_call" | "function_call_output" | "code_interpreter_call" | "local_shell_call" | "local_shell_call_output" | "mcp_call" | "custom_tool_call" | "custom_tool_call_output" | "image_generation_call" | "mcp_list_tools" | "mcp_approval_request" | "mcp_approval_responses" | "reasoning" | "item_reference" | "refusal"
	ID     *string `json:"id,omitempty"`      // Common ID field for most item types
	Status *string `json:"status,omitempty"`  // "in_progress" | "completed" | "incomplete" | "interpreting" | "failed"
	CallID *string `json:"call_id,omitempty"` // Common call ID for tool calls and outputs
	Name   *string `json:"name,omitempty"`    // Common name field for tool calls

	// Messages
	*InputMessage
	*OutputMessage

	// Tool calls and outputs
	*FileSearchToolCall
	*ComputerToolCall
	*ComputerToolCallOutput
	*WebSearchToolCall
	*FunctionToolCall
	*FunctionToolCallOutput
	*CodeInterpreterToolCall
	*LocalShellCall
	*LocalShellCallOutput
	*MCPToolCall
	*CustomToolCall
	*CustomToolCallOutput
	*ImageGenerationCall

	// MCP-specific
	*MCPListTools
	*MCPApprovalRequest
	*MCPApprovalResponse

	// Reasoning
	*Reasoning

	*InputItemReference
}

// InputMessage represents a message in the conversation
type InputMessage struct {
	Role    string              `json:"role"` // "user" | "assistant" | "system" | "developer"
	Content InputMessageContent `json:"content"`
}

// InputMessageContent is a union type that can be either a string or array of content blocks
type InputMessageContent struct {
	ContentStr    *string                     // Simple text content
	ContentBlocks *[]InputMessageContentBlock // Rich content with multiple media types
}

type ResponsesAPIExtendedContentBlock struct {
	*InputMessageContentBlockImage
	*InputMessageContentBlockFile
	Audio *InputMessageContentBlockAudio `json:"input_audio,omitempty"`
}

// InputMessageContentBlock represents different types of content (text, image, file, audio)
// Only one of the content type fields should be set
type InputMessageContentBlock struct {
	Type string `json:"type"` // "input_text" | "input_image" | "input_file" | "input_audio"
	*InputMessageContentBlockText
	*InputMessageContentBlockImage
	*InputMessageContentBlockFile
	Audio *InputMessageContentBlockAudio `json:"input_audio,omitempty"`
}

type InputMessageContentBlockText struct {
	Text string `json:"text"`
}

type InputMessageContentBlockImage struct {
	Detail   *string `json:"detail,omitempty"`    // "low" | "high" | "auto"
	FileID   *string `json:"file_id,omitempty"`   // Reference to uploaded file
	ImageURL *string `json:"image_url,omitempty"` // Direct URL or base64 encoded image
}

type InputMessageContentBlockFile struct {
	FileData *string `json:"file_data,omitempty"` // Base64 encoded file data
	FileID   *string `json:"file_id,omitempty"`   // Reference to uploaded file
	FileURL  *string `json:"file_url,omitempty"`  // Direct URL to file
	Filename *string `json:"filename,omitempty"`  // Name of the file
}

type InputMessageContentBlockAudio struct {
	Format string `json:"format"` // "mp3" or "wav"
	Data   string `json:"data"`   // base64 encoded audio data
}

// InputItemReference represents a reference to an existing item by ID
type InputItemReference struct {
	// No fields needed here since Type and ID are now in the top-level struct
}

// =============================================================================
// 3. OUTPUT MESSAGE STRUCTURES
// =============================================================================

type OutputMessage struct {
	Role    string               `json:"role"` // always "assistant"
	Content OutputMessageContent `json:"content"`
}

// OutputMessageContent is a union type for different output content types
// Either OutputText or Refusal should be set, not both
type OutputMessageContent struct {
	*OutputMessageText    // Normal text output from the model
	*OutputMessageRefusal // Model refusal to answer
}

type OutputMessageText struct {
	Type        string                         `json:"type"`                  // always "output_text"
	Annotations *[]OutputMessageTextAnnotation `json:"annotations,omitempty"` // Citations and references
	Text        string                         `json:"text"`
	LogProbs    *[]OutputMessageTextLogProb    `json:"logprobs,omitempty"` // Token log probabilities
}

type ResponsesAPIExtendedOutputMessageText struct {
	Annotations *[]OutputMessageTextAnnotation `json:"annotations,omitempty"` // Citations and references
	LogProbs    *[]OutputMessageTextLogProb    `json:"logprobs,omitempty"`    // Token log probabilities
}

// OutputMessageTextLogProb represents log probability information for content.
type OutputMessageTextLogProb struct {
	Bytes       []int     `json:"bytes"`
	LogProb     float64   `json:"logprob"`
	Token       string    `json:"token"`
	TopLogProbs []LogProb `json:"top_logprobs"`
}

type ResponsesAPIExtendedOutputMessageTextAnnotation struct {
	Index      *int    `json:"index,omitempty"`       // Common index field (FileCitation, FilePath)
	FileID     *string `json:"file_id,omitempty"`     // Common file ID field (FileCitation, ContainerFileCitation, FilePath)
	StartIndex *int    `json:"start_index,omitempty"` // Common start index field (URLCitation, ContainerFileCitation)
	EndIndex   *int    `json:"end_index,omitempty"`   // Common end index field (URLCitation, ContainerFileCitation)

	*OutputMessageTextAnnotationFileCitation          // Citation to a file
	*OutputMessageTextAnnotationURLCitation           // Citation to a web URL
	*OutputMessageTextAnnotationContainerFileCitation // Citation to container file
	*OutputMessageTextAnnotationFilePath              // Reference to a file path
}

// OutputMessageTextAnnotation is a union type for different annotation types
// Only one of the annotation fields should be set
type OutputMessageTextAnnotation struct {
	Type string `json:"type"` // "file_citation" | "url_citation" | "container_file_citation" | "file_path"
	ResponsesAPIExtendedOutputMessageTextAnnotation
}

type OutputMessageTextAnnotationFileCitation struct {
	Filename string `json:"filename"`
}

type OutputMessageTextAnnotationURLCitation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type OutputMessageTextAnnotationContainerFileCitation struct {
	ContainerID string `json:"container_id"`
	Filename    string `json:"filename"`
}

type OutputMessageTextAnnotationFilePath struct {
	// No unique fields needed here since Type, FileID, and Index are now in the top-level struct
}

type OutputMessageRefusal struct {
	Type    string `json:"type"` // always "refusal"
	Refusal string `json:"refusal"`
}

// =============================================================================
// 4. TOOL CALL STRUCTURES (organized by tool type)
// =============================================================================

// -----------------------------------------------------------------------------
// File Search Tool
// -----------------------------------------------------------------------------

type FileSearchToolCall struct {
	Queries []string                   `json:"queries"`
	Results []FileSearchToolCallResult `json:"results,omitempty"`
}

type FileSearchToolCallResult struct {
	Attributes *map[string]any `json:"attributes,omitempty"`
	FileID     *string         `json:"file_id,omitempty"`
	Filename   *string         `json:"filename,omitempty"`
	Score      *float64        `json:"score,omitempty"`
	Text       *string         `json:"text,omitempty"`
}

// -----------------------------------------------------------------------------
// Computer Tool
// -----------------------------------------------------------------------------
type ComputerToolCall struct {
	Action              ComputerAction       `json:"action"`
	PendingSafetyChecks []PendingSafetyCheck `json:"pending_safety_checks"`
}

type PendingSafetyCheck struct {
	ID      string `json:"id"`
	Context string `json:"context"`
	Message string `json:"message"`
}

// ComputerAction represents the different types of computer actions
type ComputerAction struct {
	Type string `json:"type"`        // "click" | "double_click" | "drag" | "keypress" | "move" | "screenshot" | "scroll" | "type" | "wait"
	X    *int   `json:"x,omitempty"` // Common X coordinate field (Click, DoubleClick, Move, Scroll)
	Y    *int   `json:"y,omitempty"` // Common Y coordinate field (Click, DoubleClick, Move, Scroll)

	// Action types - only one should be set
	Click       *ComputerActionClick       `json:",omitempty"`
	DoubleClick *ComputerActionDoubleClick `json:",omitempty"`
	Drag        *ComputerActionDrag        `json:",omitempty"`
	KeyPress    *ComputerActionKeyPress    `json:",omitempty"`
	Move        *ComputerActionMove        `json:",omitempty"`
	Screenshot  *ComputerActionScreenshot  `json:",omitempty"`
	Scroll      *ComputerActionScroll      `json:",omitempty"`
	ActionType  *ComputerActionType        `json:",omitempty"` // Renamed to avoid collision with Type field
	Wait        *ComputerActionWait        `json:",omitempty"`
}

// ClickAction - A click action
type ComputerActionClick struct {
	Button string `json:"button"` // "left" | "right" | "wheel" | "back" | "forward"
}

// DoubleClickAction - A double click action
type ComputerActionDoubleClick struct {
	// No unique fields needed since Type, X, Y are now in the top-level struct
}

// DragAction - A drag action
type ComputerActionDrag struct {
	Path []Coordinate `json:"path"`
}

type Coordinate struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// KeyPressAction - A collection of keypresses the model would like to perform
type ComputerActionKeyPress struct {
	Keys []string `json:"keys"`
}

// MoveAction - A mouse move action
type ComputerActionMove struct {
	// No unique fields needed since Type, X, Y are now in the top-level struct
}

// ScreenshotAction - A screenshot action
type ComputerActionScreenshot struct {
	// No unique fields needed since Type is now in the top-level struct
}

// ScrollAction - A scroll action
type ComputerActionScroll struct {
	ScrollX int `json:"scroll_x"`
	ScrollY int `json:"scroll_y"`
}

// TypeAction - An action to type in text
type ComputerActionType struct {
	Text string `json:"text"`
}

// WaitAction - A wait action
type ComputerActionWait struct {
	// No unique fields needed since Type is now in the top-level struct
}

// Computer Tool Call Output - contains the results from executing a computer tool call
type ComputerToolCallOutput struct {
	Output                   ComputerToolCallOutputData `json:"output"`
	AcknowledgedSafetyChecks []AcknowledgedSafetyCheck  `json:"acknowledged_safety_checks,omitempty"`
}

// ComputerToolCallOutputData - A computer screenshot image used with the computer use tool
type ComputerToolCallOutputData struct {
	Type     string  `json:"type"` // always "computer_screenshot"
	FileID   *string `json:"file_id,omitempty"`
	ImageURL *string `json:"image_url,omitempty"`
}

// AcknowledgedSafetyCheck - The safety checks reported by the API that have been acknowledged by the developer
type AcknowledgedSafetyCheck struct {
	ID      string  `json:"id"`
	Code    *string `json:"code,omitempty"`
	Message *string `json:"message,omitempty"`
}

// -----------------------------------------------------------------------------
// Web Search Tool
// -----------------------------------------------------------------------------
type WebSearchToolCall struct {
	Action WebSearchAction `json:"action"`
}

// WebSearchAction represents the different types of web search actions
type WebSearchAction struct {
	Type string  `json:"type"`          // "search" | "open_page" | "find"
	URL  *string `json:"url,omitempty"` // Common URL field (OpenPage, Find)

	// Action types - only one should be set
	Search   *WebSearchActionSearch   `json:",omitempty"`
	OpenPage *WebSearchActionOpenPage `json:",omitempty"`
	Find     *WebSearchActionFind     `json:",omitempty"`
}

// WebSearchActionSearch - Action type "search" - Performs a web search query
type WebSearchActionSearch struct {
	Query   string            `json:"query"`
	Sources []WebSearchSource `json:"sources,omitempty"`
}

// WebSearchSource - The sources used in the search
type WebSearchSource struct {
	Type string `json:"type"` // always "url"
	URL  string `json:"url"`
}

// WebSearchActionOpenPage - Action type "open_page" - Opens a specific URL from search results
type WebSearchActionOpenPage struct {
	// No unique fields needed since Type and URL are now in the top-level struct
}

// WebSearchActionFind - Action type "find" - Searches for a pattern within a loaded page
type WebSearchActionFind struct {
	Pattern string `json:"pattern"`
}

// -----------------------------------------------------------------------------
// Function Tool
// -----------------------------------------------------------------------------
type FunctionToolCall struct {
	Arguments string `json:"arguments"`
}

// Function Tool Call Output - contains the results from executing a function tool call
type FunctionToolCallOutput struct {
	Output string `json:"output"`
}

// -----------------------------------------------------------------------------
// Reasoning
// -----------------------------------------------------------------------------

type Reasoning struct {
	Summary          []ReasoningContent `json:"summary"`
	Content          []ReasoningContent `json:"content"`
	EncryptedContent *string            `json:"encrypted_content,omitempty"`
}

type ReasoningContent struct {
	Type string `json:"type"` // "summary_text" | "reasoning_text"
	Text string `json:"text"`
}

// -----------------------------------------------------------------------------
// Image Generation Tool
// -----------------------------------------------------------------------------
type ImageGenerationCall struct {
	Result string `json:"result"`
}

// -----------------------------------------------------------------------------
// Code Interpreter Tool
// -----------------------------------------------------------------------------
type CodeInterpreterToolCall struct {
	Code        *string                 `json:"code"`         // The code to run, or null if not available
	ContainerID string                  `json:"container_id"` // The ID of the container used to run the code
	Outputs     []CodeInterpreterOutput `json:"outputs"`      // The outputs generated by the code interpreter, can be null
}

// CodeInterpreterOutput represents the different types of code interpreter outputs
type CodeInterpreterOutput struct {
	// Output types - only one should be set
	Logs  *CodeInterpreterOutputLogs  `json:",omitempty"`
	Image *CodeInterpreterOutputImage `json:",omitempty"`
}

// CodeInterpreterOutputLogs - The logs output from the code interpreter
type CodeInterpreterOutputLogs struct {
	Logs string `json:"logs"`
	Type string `json:"type"` // always "logs"
}

// CodeInterpreterOutputImage - The image output from the code interpreter
type CodeInterpreterOutputImage struct {
	Type string `json:"type"` // always "image"
	URL  string `json:"url"`
}

// -----------------------------------------------------------------------------
// Local Shell Tool
// -----------------------------------------------------------------------------
type LocalShellCall struct {
	Action LocalShellCallAction `json:"action"`
}

type LocalShellCallAction struct {
	Command          []string `json:"command"`
	Env              []string `json:"env"`
	Type             string   `json:"type"` // always "exec"
	TimeoutMS        *int     `json:"timeout_ms,omitempty"`
	User             *string  `json:"user,omitempty"`
	WorkingDirectory *string  `json:"working_directory,omitempty"`
}

type LocalShellCallOutput struct {
	Output string `json:"output"`
}

// -----------------------------------------------------------------------------
// MCP (Model Context Protocol) Tools
// -----------------------------------------------------------------------------
type MCPListTools struct {
	ServerLabel string    `json:"server_label"`
	Tools       []MCPTool `json:"tools"`
	Error       *string   `json:"error,omitempty"`
}

type MCPTool struct {
	Name        string          `json:"name"`
	InputSchema map[string]any  `json:"input_schema"`
	Description *string         `json:"description,omitempty"`
	Annotations *map[string]any `json:"annotations,omitempty"`
}

// MCP Approval Request - requests approval for a specific action within MCP
type MCPApprovalRequest struct {
	Action MCPAction `json:"action"`
}

type MCPAction struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // always "mcp_approval_request"
	Name        string `json:"name"`
	ServerLabel string `json:"server_label"`
	Arguments   string `json:"arguments"`
}

// MCP Approval Response - contains the response to an approval request within MCP
type MCPApprovalResponse struct {
	ApprovalResponseID string  `json:"approval_response_id"`
	Approve            bool    `json:"approve"`
	Reason             *string `json:"reason,omitempty"`
}

// MCP Tool Call - an invocation of a tool on an MCP server
type MCPToolCall struct {
	Arguments   string  `json:"arguments"`        // A JSON string of the arguments passed to the tool
	ServerLabel string  `json:"server_label"`     // The label of the MCP server running the tool
	Error       *string `json:"error,omitempty"`  // The error from the tool call, if any
	Output      *string `json:"output,omitempty"` // The output from the tool call
}

// -----------------------------------------------------------------------------
// Custom Tools
// -----------------------------------------------------------------------------
type CustomToolCallOutput struct {
	Output string `json:"output"` // The output from the custom tool call generated by your code
}

// Custom Tool Call - a call to a custom tool created by the model
type CustomToolCall struct {
	Input string `json:"input"` // The input for the custom tool call generated by the model
}

// =============================================================================
// 5. CONFIGURATION STRUCTURES
// =============================================================================

// Text configuration options

type TextConfig struct {
	Format    *TextConfigFormat `json:"format,omitempty"`    // An object specifying the format that the model must output
	Verbosity *string           `json:"verbosity,omitempty"` // "low" | "medium" | "high" or null
}

// TextFormat specifies the format that the model must output
type TextConfigFormat struct {
	Type       string          `json:"type"`                  // "text" | "json_schema" | "json_object"
	JSONSchema *JSONSchemaSpec `json:"json_schema,omitempty"` // when type == "json_schema"
}

// JSONSchemaSpec represents a JSON schema specification
type JSONSchemaSpec struct {
	Name        string         `json:"name"`
	Schema      map[string]any `json:"schema"` // JSON Schema (subset)
	Type        string         `json:"type"`   // always "json_schema"
	Description *string        `json:"description,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

// =============================================================================
// 6. TOOL CHOICE CONFIGURATION
// =============================================================================

type ResponsesAPIExtendedToolChoice struct {
	Mode        *string `json:"mode,omitempty"`         //"none" | "auto" | "required"
	Name        *string `json:"name,omitempty"`         // Common name field for function/MCP/custom tools
	ServerLabel *string `json:"server_label,omitempty"` // Common server label field for MCP tools

	// Object types - only one should be set
	*ToolChoiceAllowedTools
	*ToolChoiceHostedTool
	*ToolChoiceFunctionTool
	*ToolChoiceMCPTool
	*ToolChoiceCustomTool
}

// ResponsesAPIToolChoice represents how the model should select tools - can be string or object
type ResponsesAPIToolChoice struct {
	Type *string `json:"type,omitempty"` // "allowed_tools" | "file_search" | "web_search_preview" | "computer_use_preview" | "code_interpreter" | "image_generation" | "function" | "mcp" | "custom"
	ResponsesAPIExtendedToolChoice
}

// ToolChoiceAllowedTools - Constrains the tools available to the model to a pre-defined set
type ToolChoiceAllowedTools struct {
	Mode  string                     `json:"mode"` // "auto" | "required"
	Tools []ToolChoiceAllowedToolDef `json:"tools"`
}

// ToolChoiceAllowedToolDef - Definition of an allowed tool
type ToolChoiceAllowedToolDef struct {
	Type        string  `json:"type"`                   // "function" | "mcp" | "image_generation"
	Name        *string `json:"name,omitempty"`         // for function tools
	ServerLabel *string `json:"server_label,omitempty"` // for MCP tools
}

// ToolChoiceHostedTool - Indicates the model should use a built-in tool
type ToolChoiceHostedTool struct {
	// No unique fields needed since Type is now in the top-level struct
}

// ToolChoiceFunctionTool - Force the model to call a specific function
type ToolChoiceFunctionTool struct {
	// No unique fields needed since Type and Name are now in the top-level struct
}

// ToolChoiceMCPTool - Force the model to call a specific tool on a remote MCP server
type ToolChoiceMCPTool struct {
	// No unique fields needed since Type, ServerLabel, and Name are now in the top-level struct
}

// ToolChoiceCustomTool - Force the model to call a specific custom tool
type ToolChoiceCustomTool struct {
	// No unique fields needed since Type and Name are now in the top-level struct
}

// =============================================================================
// 7. TOOL CONFIGURATION STRUCTURES
// =============================================================================

type ResponsesAPIExtendedTool struct {
	Name        *string `json:"name,omitempty"`        // Common name field (Function, Custom tools)
	Description *string `json:"description,omitempty"` // Common description field (Function, Custom tools)

	*ToolFunction
	*ToolFileSearch
	*ToolComputerUsePreview
	*ToolWebSearch
	*ToolMCP
	*ToolCodeInterpreter
	*ToolImageGeneration
	*ToolLocalShell
	*ToolCustom
	*ToolWebSearchPreview
}

// Tool represents different types of tools the model can use
type ResponsesAPITool struct {
	Type string `json:"type"` // "function" | "file_search" | "computer_use_preview" | "web_search" | "web_search_2025_08_26" | "mcp" | "code_interpreter" | "image_generation" | "local_shell" | "custom" | "web_search_preview" | "web_search_preview_2025_03_11"
	ResponsesAPIExtendedTool
}

// ToolFileSearch - A tool that searches for relevant content from uploaded files
type ToolFileSearch struct {
	VectorStoreIDs []string                  `json:"vector_store_ids"`          // The IDs of the vector stores to search
	Filters        *FileSearchFilter         `json:"filters,omitempty"`         // A filter to apply
	MaxNumResults  *int                      `json:"max_num_results,omitempty"` // Maximum results (1-50)
	RankingOptions *FileSearchRankingOptions `json:"ranking_options,omitempty"` // Ranking options for search
}

// FileSearchFilter - A filter to apply to file search
type FileSearchFilter struct {
	// Filter types - only one should be set
	Comparison *FileSearchComparisonFilter `json:",omitempty"`
	Compound   *FileSearchCompoundFilter   `json:",omitempty"`
}

// FileSearchComparisonFilter - Compare a specified attribute key to a value
type FileSearchComparisonFilter struct {
	Key   string      `json:"key"`   // The key to compare against the value
	Type  string      `json:"type"`  // "eq" | "ne" | "gt" | "gte" | "lt" | "lte"
	Value interface{} `json:"value"` // The value to compare (string, number, or boolean)
}

// FileSearchCompoundFilter - Combine multiple filters using and or or
type FileSearchCompoundFilter struct {
	Filters []FileSearchFilter `json:"filters"` // Array of filters to combine
	Type    string             `json:"type"`    // "and" | "or"
}

// FileSearchRankingOptions - Ranking options for search
type FileSearchRankingOptions struct {
	Ranker         *string  `json:"ranker,omitempty"`          // The ranker to use
	ScoreThreshold *float64 `json:"score_threshold,omitempty"` // Score threshold (0-1)
}

// ToolComputerUsePreview - A tool that controls a virtual computer
type ToolComputerUsePreview struct {
	DisplayHeight int    `json:"display_height"` // The height of the computer display
	DisplayWidth  int    `json:"display_width"`  // The width of the computer display
	Environment   string `json:"environment"`    // The type of computer environment to control
}

// ToolWebSearch - Search the Internet for sources related to the prompt
type ToolWebSearch struct {
	Filters           *WebSearchFilters      `json:"filters,omitempty"`             // Filters for the search
	SearchContextSize *string                `json:"search_context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *WebSearchUserLocation `json:"user_location,omitempty"`       // The approximate location of the user
}

// WebSearchFilters - Filters for web search
type WebSearchFilters struct {
	AllowedDomains []string `json:"allowed_domains"` // Allowed domains for the search
}

// WebSearchUserLocation - The approximate location of the user
type WebSearchUserLocation struct {
	City     *string `json:"city,omitempty"`     // Free text input for the city
	Country  *string `json:"country,omitempty"`  // Two-letter ISO country code
	Region   *string `json:"region,omitempty"`   // Free text input for the region
	Timezone *string `json:"timezone,omitempty"` // IANA timezone
	Type     *string `json:"type,omitempty"`     // always "approximate"
}

// ToolMCP - Give the model access to additional tools via remote MCP servers
type ToolMCP struct {
	ServerLabel       string                  `json:"server_label"`                 // A label for this MCP server
	AllowedTools      *MCPAllowedTools        `json:"allowed_tools,omitempty"`      // List of allowed tool names or filter
	Authorization     *string                 `json:"authorization,omitempty"`      // OAuth access token
	ConnectorID       *string                 `json:"connector_id,omitempty"`       // Service connector ID
	Headers           *map[string]string      `json:"headers,omitempty"`            // Optional HTTP headers
	RequireApproval   *MCPToolApprovalSetting `json:"require_approval,omitempty"`   // Tool approval settings
	ServerDescription *string                 `json:"server_description,omitempty"` // Optional server description
	ServerURL         *string                 `json:"server_url,omitempty"`         // The URL for the MCP server
}

// MCPAllowedTools - List of allowed tool names or a filter object
type MCPAllowedTools struct {
	// Either a simple array of tool names or a filter object
	ToolNames *[]string      `json:",omitempty"`
	Filter    *MCPToolFilter `json:",omitempty"`
}

// MCPToolFilter - A filter object to specify which tools are allowed
type MCPToolFilter struct {
	ReadOnly  *bool     `json:"read_only,omitempty"`  // Whether tool is read-only
	ToolNames *[]string `json:"tool_names,omitempty"` // List of allowed tool names
}

// MCPToolApprovalSetting - Specify which tools require approval
type MCPToolApprovalSetting struct {
	// Either a string setting or filter objects
	Setting *string                `json:",omitempty"` // "always" | "never"
	Always  *MCPToolApprovalFilter `json:"always,omitempty"`
	Never   *MCPToolApprovalFilter `json:"never,omitempty"`
}

// MCPToolApprovalFilter - Filter for approval settings
type MCPToolApprovalFilter struct {
	ReadOnly  *bool     `json:"read_only,omitempty"`  // Whether tool is read-only
	ToolNames *[]string `json:"tool_names,omitempty"` // List of tool names
}

// ToolCodeInterpreter - A tool that runs Python code
type ToolCodeInterpreter struct {
	Container interface{} `json:"container"` // Container ID or object with file IDs
}

// ToolImageGeneration - A tool that generates images
type ToolImageGeneration struct {
	Background        *string                   `json:"background,omitempty"`         // "transparent" | "opaque" | "auto"
	InputFidelity     *string                   `json:"input_fidelity,omitempty"`     // "high" | "low"
	InputImageMask    *ImageGenerationInputMask `json:"input_image_mask,omitempty"`   // Optional mask for inpainting
	Model             *string                   `json:"model,omitempty"`              // Image generation model
	Moderation        *string                   `json:"moderation,omitempty"`         // Moderation level
	OutputCompression *int                      `json:"output_compression,omitempty"` // Compression level (0-100)
	OutputFormat      *string                   `json:"output_format,omitempty"`      // "png" | "webp" | "jpeg"
	PartialImages     *int                      `json:"partial_images,omitempty"`     // Number of partial images (0-3)
	Quality           *string                   `json:"quality,omitempty"`            // "low" | "medium" | "high" | "auto"
	Size              *string                   `json:"size,omitempty"`               // Image size
}

// ImageGenerationInputMask - Optional mask for inpainting
type ImageGenerationInputMask struct {
	FileID   *string `json:"file_id,omitempty"`   // File ID for the mask image
	ImageURL *string `json:"image_url,omitempty"` // Base64-encoded mask image
}

// ToolLocalShell - A tool that allows executing shell commands locally
type ToolLocalShell struct {
	// No unique fields needed since Type is now in the top-level struct
}

// ToolCustom - A custom tool that processes input using a specified format
type ToolCustom struct {
	Format *CustomToolFormat `json:"format,omitempty"` // The input format
}

// CustomToolFormat - The input format for the custom tool
type CustomToolFormat struct {
	Type string `json:"type"` // always "text"

	// Format types - only one should be set
	*CustomToolTextFormat
	*CustomToolGrammarFormat
}

// CustomToolTextFormat - Unconstrained free-form text
type CustomToolTextFormat struct {
}

// CustomToolGrammarFormat - A grammar defined by the user
type CustomToolGrammarFormat struct {
	Definition string `json:"definition"` // The grammar definition
	Syntax     string `json:"syntax"`     // "lark" | "regex"
}

// ToolWebSearchPreview - Web search tool preview variant
type ToolWebSearchPreview struct {
	SearchContextSize *string                `json:"search_context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *WebSearchUserLocation `json:"user_location,omitempty"`       // The user's location
}
