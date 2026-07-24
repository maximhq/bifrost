package ollama

import (
	"errors"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// OllamaProvider implements the Provider interface for Ollama's API.
type OllamaProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// OllamaChatRequest is the request type for Ollama's native /api/chat endpoint.
type OllamaChatRequest struct {
	Model       string              `json:"model"`
	Messages    []OllamaChatMessage `json:"messages"`
	Tools       []OllamaTool        `json:"tools,omitempty"`
	Format      any                 `json:"format,omitempty"`     // "json" or JSON schema object
	Think       any                 `json:"think,omitempty"`      // bool or "high"/"medium"/"low"
	KeepAlive   any                 `json:"keep_alive,omitempty"` // string (e.g. "5m") or number (seconds)
	Stream      *bool               `json:"stream,omitempty"`     // ollama defaults to true
	Options     *OllamaOptions      `json:"options,omitempty"`
	Logprobs    *bool               `json:"logprobs,omitempty"`
	TopLogprobs *int                `json:"top_logprobs,omitempty"`

	// Bifrost specific field
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OllamaChatRequest) IsStreamingRequested() bool {
	return r.Stream == nil || *r.Stream
}

// OllamaChatResponse is the response type for Ollama's native /api/chat endpoint.
type OllamaChatResponse struct {
	Model     string             `json:"model"`
	CreatedAt time.Time          `json:"created_at"`
	Message   *OllamaChatMessage `json:"message"`

	*OllamaCommonResponseFields
}

type OllamaCommonResponseFields struct {
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason"`
	TotalDuration      int    `json:"total_duration"`
	LoadDuration       int    `json:"load_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int    `json:"prompt_eval_duration"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int    `json:"eval_duration"`
	LogProbs           []struct {
		OllamaLogProb
		TopLogprobs []OllamaLogProb `json:"top_logprobs,omitempty"`
	} `json:"logprobs,omitempty"`
}

type OllamaLogProb struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []byte  `json:"bytes,omitempty"`
}

type OllamaMessageRole string

const (
	OllamaMessageRoleSystem    OllamaMessageRole = "system"
	OllamaMessageRoleUser      OllamaMessageRole = "user"
	OllamaMessageRoleAssistant OllamaMessageRole = "assistant"
	OllamaMessageRoleTool      OllamaMessageRole = "tool"
)

type OllamaChatMessage struct {
	Role      OllamaMessageRole `json:"role"`
	Content   string            `json:"content"`
	Images    []string          `json:"images,omitempty"`
	ToolCalls []OllamaToolCall  `json:"tool_calls,omitempty"`
	Thinking  *string           `json:"thinking,omitempty"`
}

type OllamaToolType string

const (
	OllamaToolTypeFunction OllamaToolType = "function"
)

// OllamaTool is the tool definition for ollama
type OllamaTool struct {
	Type     OllamaToolType      `json:"type"`
	Function *OllamaToolFunction `json:"function"`
}

// OllamaToolCall is the tool call for ollama
type OllamaToolCall struct {
	Function *OllamaToolFunction `json:"function"`
}

type OllamaToolFunction struct {
	Name        string                          `json:"name"`
	Parameters  *schemas.ToolFunctionParameters `json:"arguments"`
	Description string                          `json:"description,omitempty"`
}

// OllamaEmbedInput is a []string that unmarshals from either a JSON string or array of strings.
type OllamaEmbedInput []string

func (e *OllamaEmbedInput) UnmarshalJSON(data []byte) error {
	var s string
	if err := sonic.Unmarshal(data, &s); err == nil {
		*e = OllamaEmbedInput{s}
		return nil
	}
	var ss []string
	if err := sonic.Unmarshal(data, &ss); err == nil {
		*e = OllamaEmbedInput(ss)
		return nil
	}
	return errors.New("input must be a string or array of strings")
}

// OllamaEmbedRequest is the request type for Ollama's /api/embed endpoint.
type OllamaEmbedRequest struct {
	Model      string           `json:"model"`
	Input      OllamaEmbedInput `json:"input"`
	Truncate   *bool            `json:"truncate,omitempty"`
	Dimensions *int             `json:"dimensions,omitempty"`
	KeepAlive  string           `json:"keep_alive,omitempty"`
	Options    *OllamaOptions   `json:"options,omitempty"`
}

// OllamaEmbedResponse is the response for /api/embed.
type OllamaEmbedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	TotalDuration   int64       `json:"total_duration,omitempty"`
	LoadDuration    int64       `json:"load_duration,omitempty"`
	PromptEvalCount int         `json:"prompt_eval_count,omitempty"`
}

// OllamaGenerateRequest is the request type for Ollama's /api/generate endpoint.
type OllamaGenerateRequest struct {
	Model       string         `json:"model"`
	Prompt      string         `json:"prompt,omitempty"`
	Suffix      string         `json:"suffix,omitempty"`
	Images      []string       `json:"images,omitempty"`
	System      string         `json:"system,omitempty"`
	Format      any            `json:"format,omitempty"` // "json" or JSON schema object
	Think       any            `json:"think,omitempty"`  // bool or "high"/"medium"/"low"
	Raw         *bool          `json:"raw,omitempty"`
	KeepAlive   any            `json:"keep_alive,omitempty"` // string (e.g. "5m") or number (seconds)
	Stream      *bool          `json:"stream,omitempty"`     // ollama defaults to true
	Options     *OllamaOptions `json:"options,omitempty"`
	Logprobs    *bool          `json:"logprobs,omitempty"`
	TopLogprobs *int           `json:"top_logprobs,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface.
func (r *OllamaGenerateRequest) IsStreamingRequested() bool {
	return r.Stream == nil || *r.Stream
}

// OllamaGenerateResponse is the response for /api/generate (streaming and non-streaming).
type OllamaGenerateResponse struct {
	Model              string `json:"model"`
	CreatedAt          string `json:"created_at"`
	Response           string `json:"response"`
	Thinking           string `json:"thinking,omitempty"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason,omitempty"`
	TotalDuration      int64  `json:"total_duration,omitempty"`
	LoadDuration       int64  `json:"load_duration,omitempty"`
	PromptEvalCount    int    `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64  `json:"prompt_eval_duration,omitempty"`
	EvalCount          int    `json:"eval_count,omitempty"`
	EvalDuration       int64  `json:"eval_duration,omitempty"`
}

// OllamaOptions holds Ollama model generation options.
type OllamaOptions struct {
	Temperature      *float64      `json:"temperature,omitempty"`
	TopP             *float64      `json:"top_p,omitempty"`
	TopK             *int          `json:"top_k,omitempty"`
	MinP             *float64      `json:"min_p,omitempty"`
	Seed             *int          `json:"seed,omitempty"`
	NumPredict       *int          `json:"num_predict,omitempty"`
	NumCtx           *int          `json:"num_ctx,omitempty"`
	Stop             OllamaStopSeq `json:"stop,omitempty"`
	FrequencyPenalty *float64      `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64      `json:"presence_penalty,omitempty"`
}

// OllamaStopSeq unmarshals from either a JSON string or an array of strings.
type OllamaStopSeq []string

func (s *OllamaStopSeq) UnmarshalJSON(data []byte) error {
	var str string
	if err := sonic.Unmarshal(data, &str); err == nil {
		*s = OllamaStopSeq{str}
		return nil
	}
	var arr []string
	if err := sonic.Unmarshal(data, &arr); err == nil {
		*s = OllamaStopSeq(arr)
		return nil
	}
	return errors.New("stop must be a string or array of strings")
}
