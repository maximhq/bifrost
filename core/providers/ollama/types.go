// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains the type definitions for Ollama's native API.
package ollama

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ==================== REQUEST TYPES ====================

// OllamaChatRequest represents an Ollama chat completion request using native API.
// See: https://github.com/ollama/ollama/blob/main/docs/api.md#generate-a-chat-completion
type OllamaChatRequest struct {
	Model       string          `json:"model"`                  // Required: Name of the model to use
	Messages    []OllamaMessage `json:"messages"`               // Required: Messages for the chat
	Tools       []OllamaTool    `json:"tools,omitempty"`        // Optional: List of tools the model may use
	Think       *OllamaThinkValue `json:"think,omitempty"`      // Optional: Enable thinking - bool or string ("low"/"medium"/"high")
	Format      interface{}     `json:"format,omitempty"`       // Optional: Format of the response (e.g., "json" or JSON schema)
	Options     *OllamaOptions  `json:"options,omitempty"`      // Optional: Model parameters
	Stream      *bool           `json:"stream,omitempty"`       // Optional: Enable streaming (default: true)
	KeepAlive   *string         `json:"keep_alive,omitempty"`   // Optional: How long to keep model loaded (e.g., "5m", "0" to unload)
	Logprobs    *bool           `json:"logprobs,omitempty"`     // Optional: Return log probabilities
	TopLogprobs *int            `json:"top_logprobs,omitempty"` // Optional: Number of top log probabilities to return
}

// OllamaMessage represents a message in Ollama format.
type OllamaMessage struct {
	Role       string           `json:"role"`                  // "system", "user", "assistant", or "tool"
	Content    string           `json:"content"`               // Message content
	Thinking   *string          `json:"thinking,omitempty"`    // Optional: Thinking content
	Images     []string         `json:"images,omitempty"`      // Optional: Base64 encoded images for multimodal models
	ToolCalls  []OllamaToolCall `json:"tool_calls,omitempty"`  // Optional: Tool calls made by the assistant
	ToolName   *string          `json:"tool_name,omitempty"`   // Optional: Tool name (for tool response messages)
	ToolCallID *string          `json:"tool_call_id,omitempty"` // Optional: Tool call ID for correlation
}

// OllamaToolCall represents a tool call in Ollama format.
type OllamaToolCall struct {
	ID       string                  `json:"id,omitempty"` // Optional: Tool call ID for correlation
	Function OllamaToolCallFunction `json:"function"`
}

// OllamaToolCallFunction represents the function details of a tool call.
type OllamaToolCallFunction struct {
	Index     int                    `json:"index"`     // Index of the tool call
	Name      string                 `json:"name"`      // Function name
	Arguments map[string]interface{} `json:"arguments"` // Function arguments
}

// OllamaTool represents a tool definition in Ollama format.
type OllamaTool struct {
	Type     string             `json:"type"` // "function"
	Function OllamaToolFunction `json:"function"`
}

// OllamaToolFunction represents a function definition for tools.
type OllamaToolFunction struct {
	Name        string                          `json:"name"`
	Description string                          `json:"description"`
	Parameters  *schemas.ToolFunctionParameters `json:"parameters,omitempty"`
}

// OllamaOptions represents model parameters for Ollama requests.
// See: https://github.com/ollama/ollama/blob/main/docs/modelfile.md#valid-parameters-and-values
type OllamaOptions struct {
	// Generation parameters
	NumPredict  *int     `json:"num_predict,omitempty"` // Maximum number of tokens to generate (similar to max_tokens)
	Temperature *float64 `json:"temperature,omitempty"` // Sampling temperature (0.0-2.0)
	TopP        *float64 `json:"top_p,omitempty"`       // Top-p sampling
	TopK        *int     `json:"top_k,omitempty"`       // Top-k sampling
	MinP        *float64 `json:"min_p,omitempty"`       // Min-p sampling
	Seed        *int     `json:"seed,omitempty"`        // Random seed for reproducibility
	Stop        []string `json:"stop,omitempty"`        // Stop sequences

	// Penalty parameters
	RepeatPenalty    *float64 `json:"repeat_penalty,omitempty"`    // Repetition penalty
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`  // Presence penalty
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"` // Frequency penalty
	RepeatLastN      *int     `json:"repeat_last_n,omitempty"`     // Last N tokens for repeat penalty

	// Context and performance
	NumCtx    *int `json:"num_ctx,omitempty"`    // Context window size
	NumBatch  *int `json:"num_batch,omitempty"`  // Batch size for prompt processing
	NumGPU    *int `json:"num_gpu,omitempty"`    // Number of layers to offload to GPU
	NumThread *int `json:"num_thread,omitempty"` // Number of threads

	// Advanced parameters
	Mirostat    *int     `json:"mirostat,omitempty"`     // Mirostat sampling (0, 1, or 2)
	MirostatEta *float64 `json:"mirostat_eta,omitempty"` // Mirostat learning rate
	MirostatTau *float64 `json:"mirostat_tau,omitempty"` // Mirostat target entropy
	TfsZ        *float64 `json:"tfs_z,omitempty"`        // Tail-free sampling
	TypicalP    *float64 `json:"typical_p,omitempty"`    // Typical p sampling

	// Low-level parameters
	UseMlock *bool `json:"use_mlock,omitempty"` // Lock model in memory
	UseMmap  *bool `json:"use_mmap,omitempty"`  // Use memory mapping
	Numa     *bool `json:"numa,omitempty"`      // Enable NUMA support
}

// OllamaThinkValue represents the think parameter which can be a bool or a string.
// Most models accept bool (true/false), while GPT-OSS accepts "low"/"medium"/"high".
type OllamaThinkValue struct {
	BoolVal   *bool
	StringVal *string
}

// OllamaLogprob represents a token log probability in Ollama format.
type OllamaLogprob struct {
	Token      string            `json:"token"`                 // The token string
	Logprob    float64           `json:"logprob"`               // Log probability of this token
	Bytes      []int             `json:"bytes,omitempty"`       // Byte representation of the token
	TopLogprobs []OllamaLogprob  `json:"top_logprobs,omitempty"` // Top alternative tokens
}

// ==================== RESPONSE TYPES ====================

// OllamaChatResponse represents an Ollama chat completion response.
type OllamaChatResponse struct {
	Model              string          `json:"model"`                          // Model used for generation
	CreatedAt          string          `json:"created_at"`                     // Timestamp when response was created
	Message            *OllamaMessage  `json:"message,omitempty"`              // Generated message
	Done               bool            `json:"done"`                           // Whether generation is complete
	DoneReason         *string         `json:"done_reason,omitempty"`          // Reason for completion ("stop", "length", "load", "unload")
	TotalDuration      *int64          `json:"total_duration,omitempty"`       // Total time in nanoseconds
	LoadDuration       *int64          `json:"load_duration,omitempty"`        // Time to load model in nanoseconds
	PromptEvalCount    *int            `json:"prompt_eval_count,omitempty"`    // Number of tokens in prompt
	PromptEvalDuration *int64          `json:"prompt_eval_duration,omitempty"` // Time to evaluate prompt in nanoseconds
	EvalCount          *int            `json:"eval_count,omitempty"`           // Number of tokens generated
	EvalDuration       *int64          `json:"eval_duration,omitempty"`        // Time to generate response in nanoseconds
	Logprobs           []OllamaLogprob `json:"logprobs,omitempty"`             // Token log probabilities
}

type OllamaEmbeddingInput struct {
	Text  *string
	Texts []string
}

// ==================== EMBEDDING TYPES ====================

// OllamaEmbeddingRequest represents an Ollama embedding request.
// See: https://github.com/ollama/ollama/blob/main/docs/api.md#generate-embeddings
type OllamaEmbeddingRequest struct {
	Model      string               `json:"model"`                  // Required: Name of the embedding model
	Input      OllamaEmbeddingInput `json:"input"`                  // Required: Text to embed (string or []string)
	Truncate   *bool                `json:"truncate,omitempty"`     // Optional: Truncate input to fit context length
	Dimensions *int                 `json:"dimensions,omitempty"`   // Optional: Number of dimensions for the embedding
	Options    *OllamaOptions       `json:"options,omitempty"`      // Optional: Model parameters
	KeepAlive  *string              `json:"keep_alive,omitempty"`   // Optional: How long to keep model loaded
}

// OllamaEmbeddingResponse represents an Ollama embedding response.
type OllamaEmbeddingResponse struct {
	Model           string      `json:"model"`                       // Model used for embedding
	Embeddings      [][]float32 `json:"embeddings"`                  // Generated embeddings
	TotalDuration   *int64      `json:"total_duration,omitempty"`    // Total time in nanoseconds
	LoadDuration    *int64      `json:"load_duration,omitempty"`     // Time to load model in nanoseconds
	PromptEvalCount *int        `json:"prompt_eval_count,omitempty"` // Number of tokens processed
}

// ==================== LIST MODELS TYPES ====================

// OllamaListModelsResponse represents the response from /api/tags endpoint.
type OllamaListModelsResponse struct {
	Models []OllamaModel `json:"models"`
}

// OllamaModel represents a model in Ollama's list.
type OllamaModel struct {
	Name       string             `json:"name"`        // Model name (e.g., "llama3.2:latest")
	Model      string             `json:"model"`       // Model identifier
	ModifiedAt time.Time          `json:"modified_at"` // Last modified timestamp
	Size       int64              `json:"size"`        // Model size in bytes
	Digest     string             `json:"digest"`      // Model digest
	Details    OllamaModelDetails `json:"details"`     // Model details
}

// OllamaModelDetails contains detailed information about a model.
type OllamaModelDetails struct {
	ParentModel       string   `json:"parent_model,omitempty"` // Parent model name
	Format            string   `json:"format"`                 // Model format (e.g., "gguf")
	Family            string   `json:"family"`                 // Model family (e.g., "llama")
	Families          []string `json:"families,omitempty"`     // Additional families
	ParameterSize     string   `json:"parameter_size"`         // Parameter count (e.g., "8B")
	QuantizationLevel string   `json:"quantization_level"`     // Quantization (e.g., "Q4_0")
}

// ==================== ERROR TYPES ====================

// OllamaError represents an error response from Ollama's API.
type OllamaError struct {
	Error string `json:"error"`
}

// ==================== STREAMING TYPES ====================

// OllamaStreamResponse represents a single streaming chunk from Ollama.
// It's the same structure as OllamaChatResponse but used during streaming.
type OllamaStreamResponse struct {
	Model              string          `json:"model"`
	CreatedAt          string          `json:"created_at"`
	Message            *OllamaMessage  `json:"message,omitempty"`
	Done               bool            `json:"done"`
	DoneReason         *string         `json:"done_reason,omitempty"`
	TotalDuration      *int64          `json:"total_duration,omitempty"`
	LoadDuration       *int64          `json:"load_duration,omitempty"`
	PromptEvalCount    *int            `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration *int64          `json:"prompt_eval_duration,omitempty"`
	EvalCount          *int            `json:"eval_count,omitempty"`
	EvalDuration       *int64          `json:"eval_duration,omitempty"`
	Logprobs           []OllamaLogprob `json:"logprobs,omitempty"`
}
