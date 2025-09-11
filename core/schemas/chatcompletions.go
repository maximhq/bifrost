package schemas

type ResponseAPIStreamResponseCreated struct {
	Type           string                     `json:"type"` // always "response.created"
	Response       *ResponseAPIStreamResponse `json:"response"`
	SequenceNumber int                        `json:"sequence_number"`
}

type ChatCompletionsExtendedBifrostMessage struct {
	Name *string `json:"name,omitempty"`
}

// ImageContent represents image data in a message.
type InputImage struct {
	URL    string  `json:"url"`
	Detail *string `json:"detail,omitempty"`
}

// InputAudioStruct represents audio data in a message.
// Data carries the audio payload as a string (e.g., data URL or provider-accepted encoded content).
// Format is optional (e.g., "wav", "mp3"); when nil, providers may attempt auto-detection.
type InputAudioStruct struct {
	Data   string  `json:"data"`
	Format *string `json:"format,omitempty"`
}

type InputFile struct {
	FileData *string `json:"file_data,omitempty"` // Base64 encoded file data
	FileID   *string `json:"file_id,omitempty"`   // Reference to uploaded file
	Filename *string `json:"filename,omitempty"`  // Name of the file
}

type ChatCompletionsExtendedContentBlock struct {
	*InputImage
	*InputAudioStruct
	*InputFile
}

type ChatCompletionsToolMessage struct {
	ToolCallID *string `json:"tool_call_id,omitempty"`
}

type ChatCompletionsAssistantMessage struct {
	Refusal     *string      `json:"refusal,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
	ToolCalls   *[]ToolCall  `json:"tool_calls,omitempty"`
}

type Annotation struct {
	Type     string   `json:"type"`
	Citation Citation `json:"url_citation"`

	*ResponsesAPIExtendedOutputMessageTextAnnotation
}

// Citation represents a citation in a response.
type Citation struct {
	StartIndex int          `json:"start_index"`
	EndIndex   int          `json:"end_index"`
	Title      string       `json:"title"`
	URL        *string      `json:"url,omitempty"`
	Sources    *interface{} `json:"sources,omitempty"`
	Type       *string      `json:"type,omitempty"`
}

// ToolCall represents a tool call in a message
type ToolCall struct {
	Type     *string      `json:"type,omitempty"`
	ID       *string      `json:"id,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a call to a function.
type FunctionCall struct {
	Name      *string `json:"name"`
	Arguments string  `json:"arguments"` // stringified json as retured by OpenAI, might not be a valid JSON always
}

// Function represents a function that can be called by the model.
type ChatCompletionsFunction struct {
	Name string `json:"name"` // Name of the function
	Description *string                `json:"description,omitempty"` // Description of the parameters
	*ToolFunction
}

// ToolFunction - Defines a function in your own code the model can choose to call
type ToolFunction struct {
	Parameters FunctionParameters `json:"parameters"` // A JSON schema object describing the parameters
	Strict     bool           `json:"strict"`     // Whether to enforce strict parameter validation
}

type ChatCompletionsCustomTool struct {
	Format *ChatCompletionsCustomToolFormat `json:"format,omitempty"` // The input format
}

type ChatCompletionsCustomToolFormat struct {
	Type string `json:"type"` // always "text"

	*CustomToolTextFormat
	Grammar *CustomToolGrammarFormat `json:"grammar,omitempty"`
}

type ChatCompletionsFunctionTool struct {
	Type     string                            `json:"type"` // "function"
	Function ChatCompletionsToolChoiceFunction `json:"function,omitempty"`
}

type ChatCompletionsExtendedTool struct {
	Function *ChatCompletionsFunction   `json:"function,omitempty"` // Function definition
	Custom   *ChatCompletionsCustomTool `json:"custom,omitempty"`   // Custom tool definition
}

// ToolChoiceFunction represents a specific function to be called.
type ChatCompletionsToolChoiceFunction struct {
	Name string `json:"name"`
}

type ChatCompletionsToolChoiceAllowedTools struct {
	Mode  string                        `json:"mode"` // "auto" | "required"
	Tools []ChatCompletionsFunctionTool `json:"tools"`
}

type ChatCompletionsToolChoiceCustom struct {
}

type ChatCompletionsExtendedToolChoice struct {
	Name *string `json:"name"` // Name of the function to call

	Function     *ChatCompletionsToolChoiceFunction     `json:"function,omitempty"`      // Function to call if type is ToolChoiceTypeFunction
	AllowedTools *ChatCompletionsToolChoiceAllowedTools `json:"allowed_tools,omitempty"` // Allowed tools to call if type is ToolChoiceTypeAllowedTools
	Custom       *ChatCompletionsToolChoiceCustom       `json:"custom,omitempty"`        // Custom tool to call if type is ToolChoiceTypeCustom
}

type ChatCompletionsExtendedUsage struct {
	TokenDetails            *TokenDetails            `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type ChatCompletionsExtendedResponse struct {
	Choices []BifrostResponseChoice `json:"choices,omitempty"`
}
