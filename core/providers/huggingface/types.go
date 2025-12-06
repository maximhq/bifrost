package huggingface

import (
	"encoding/json"
	"fmt"
)

// # MODELS TYPES

// refered from https://huggingface.co/api/models
type HuggingFaceModel struct {
	ID            string   `json:"_id"`
	ModelID       string   `json:"modelId"`
	Likes         int      `json:"likes"`
	TrendingScore int      `json:"trendingScore"`
	Private       bool     `json:"private"`
	Downloads     int      `json:"downloads"`
	Tags          []string `json:"tags"`
	PipelineTag   string   `json:"pipeline_tag"`
	LibraryName   string   `json:"library_name"`
	CreatedAt     string   `json:"createdAt"`
}

type HuggingFaceListModelsResponse struct {
	Models []HuggingFaceModel `json:"models"`
}

// UnmarshalJSON supports both the older object form `{"models": [...]}`
// and the current API which returns a top-level JSON array `[...]`.
func (r *HuggingFaceListModelsResponse) UnmarshalJSON(data []byte) error {
	// Try unmarshaling as an array first (most common for /api/models)
	var arr []HuggingFaceModel
	if err := json.Unmarshal(data, &arr); err == nil {
		r.Models = arr
		return nil
	}

	// Fallback: try object with a `models` field
	var obj struct {
		Models []HuggingFaceModel `json:"models"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		r.Models = obj.Models
		return nil
	}

	return fmt.Errorf("failed to unmarshal HuggingFaceListModelsResponse: unexpected JSON structure")
}

type HuggingFaceInferenceProviderMappingResponse struct {
	ID                       string                                      `json:"_id"`
	ModelID                  string                                      `json:"id"`
	PipelineTag              string                                      `json:"pipeline_tag"`
	InferenceProviderMapping map[string]HuggingFaceInferenceProviderInfo `json:"inferenceProviderMapping"`
}

type HuggingFaceInferenceProviderInfo struct {
	Status          string `json:"status"`
	ProviderModelID string `json:"providerId"`
	Task            string `json:"task"`
	IsModelAuthor   bool   `json:"isModelAuthor"`
}

type HuggingFaceInferenceProviderMapping struct {
	ProviderTask    string
	ProviderModelID string
}

// # CHAT TYPES

// Flexible/chat request types for HuggingFace-like chat completion payloads.
type HuggingFaceChatRequest struct {
	FrequencyPenalty *float64                   `json:"frequency_penalty,omitempty"`
	Logprobs         *bool                      `json:"logprobs,omitempty"`
	MaxTokens        *int                       `json:"max_tokens,omitempty"`
	Messages         []HuggingFaceChatMessage   `json:"messages"`
	Model            string                     `json:"model" validate:"required"`
	PresencePenalty  *float64                   `json:"presence_penalty,omitempty"`
	ResponseFormat   *HuggingFaceResponseFormat `json:"response_format,omitempty"`
	Seed             *int                       `json:"seed,omitempty"`
	Stop             []string                   `json:"stop,omitempty"`
	Stream           *bool                      `json:"stream,omitempty"`
	StreamOptions    *HuggingFaceStreamOptions  `json:"stream_options,omitempty"`
	Temperature      *float64                   `json:"temperature,omitempty"`
	ToolChoice       json.RawMessage            `json:"tool_choice,omitempty"` // flexible: enum or object
	ToolPrompt       *string                    `json:"tool_prompt,omitempty"`
	Tools            []HuggingFaceTool          `json:"tools,omitempty"`
	TopLogprobs      *int                       `json:"top_logprobs,omitempty"`
	TopP             *float64                   `json:"top_p,omitempty"`
	// Allow unknown additional fields to be captured if needed
	Extra json.RawMessage `json:"-"`
}

type HuggingFaceChatMessage struct {
	ID         *string               `json:"id,omitempty"`
	Type       *string               `json:"type,omitempty"`
	Name       *string               `json:"name,omitempty"`
	Role       *string               `json:"role,omitempty"`
	Content    json.RawMessage       `json:"content,omitempty"` // flexible: string or []content items
	ToolCalls  []HuggingFaceToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string               `json:"tool_call_id,omitempty"`
}

// Content item inside a message. Examples: text objects or image_url objects.
type HuggingFaceContentItem struct {
	Text     *string              `json:"text,omitempty"`
	Type     *string              `json:"type,omitempty"`
	ImageURL *HuggingFaceImageRef `json:"image_url,omitempty"`
}

type HuggingFaceImageRef struct {
	URL string `json:"url"`
}

type HuggingFaceToolCall struct {
	ID       *string             `json:"id,omitempty"`
	Type     *string             `json:"type,omitempty"`
	Function HuggingFaceFunction `json:"function,omitempty"`
}

type HuggingFaceFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Arguments   string `json:"arguments,omitempty"`
}

type HuggingFaceResponseFormat struct {
	Type       string                 `json:"type"`
	JSONSchema *HuggingFaceJSONSchema `json:"json_schema,omitempty"`
}

type HuggingFaceJSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type HuggingFaceStreamOptions struct {
	IncludeUsage *bool `json:"include_usage,omitempty"`
}

type HuggingFaceTool struct {
	Type     string                  `json:"type"`
	Function HuggingFaceToolFunction `json:"function"`
}

type HuggingFaceToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type HuggingFaceChatResponse struct {
	ID                string                         `json:"id"`
	Created           int64                          `json:"created"`
	Model             string                         `json:"model"`
	SystemFingerprint string                         `json:"system_fingerprint"`
	Choices           []ChatCompletionOutputComplete `json:"choices"`
	Usage             ChatCompletionOutputUsage      `json:"usage"`
}

type ChatCompletionOutputComplete struct {
	Index        int                           `json:"index"`
	Message      ChatCompletionOutputMessage   `json:"message"`
	FinishReason string                        `json:"finish_reason"`
	Logprobs     *ChatCompletionOutputLogprobs `json:"logprobs,omitempty"`
}

type ChatCompletionOutputLogprobs struct {
	Content []ChatCompletionOutputLogprob `json:"content"`
}

type ChatCompletionOutputLogprob struct {
	Token       string                           `json:"token"`
	Logprob     float32                          `json:"logprob"`
	TopLogprobs []ChatCompletionOutputTopLogprob `json:"top_logprobs"`
}

type ChatCompletionOutputTopLogprob struct {
	Token   string  `json:"token"`
	Logprob float32 `json:"logprob"`
}

// ChatCompletionOutputMessage can be either a text message or a tool-call message.
type ChatCompletionOutputMessage struct {
	// Text message fields
	Role       *string `json:"role,omitempty"`
	Content    *string `json:"content,omitempty"`
	ToolCallID *string `json:"tool_call_id,omitempty"`

	// Tool call message fields
	ToolCalls []ChatCompletionOutputToolCall `json:"tool_calls,omitempty"`
}

type ChatCompletionOutputToolCall struct {
	ID       string                                 `json:"id"`
	Type     string                                 `json:"type"`
	Function ChatCompletionOutputFunctionDefinition `json:"function"`
}

type ChatCompletionOutputFunctionDefinition struct {
	Name        string  `json:"name"`
	Arguments   string  `json:"arguments"`
	Description *string `json:"description,omitempty"`
}

type ChatCompletionOutputUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// HuggingFaceChatStreamResponse represents the streaming response from HuggingFace chat completion API
type HuggingFaceChatStreamResponse struct {
	ID                string                               `json:"id"`
	Created           int64                                `json:"created"`
	Model             string                               `json:"model"`
	SystemFingerprint string                               `json:"system_fingerprint"`
	Object            string                               `json:"object"`
	Choices           []HuggingFaceChatStreamOutputChoice  `json:"choices"`
	Usage             *HuggingFaceChatStreamOutputUsage    `json:"usage,omitempty"`
	TimeInfo          *HuggingFaceChatStreamOutputTimeInfo `json:"time_info,omitempty"`
}

type HuggingFaceChatStreamOutputChoice struct {
	Index        int                                  `json:"index"`
	Delta        HuggingFaceChatStreamOutputDelta     `json:"delta"`
	FinishReason *string                              `json:"finish_reason"`
	Logprobs     *HuggingFaceChatStreamOutputLogprobs `json:"logprobs,omitempty"`
}

// HuggingFaceChatStreamOutputDelta can be either a text message or a tool call delta
type HuggingFaceChatStreamOutputDelta struct {
	// Text message fields
	Role       *string `json:"role,omitempty"`
	Content    *string `json:"content,omitempty"`
	Reasoning  *string `json:"reasoning,omitempty"`
	ToolCallID *string `json:"tool_call_id,omitempty"`

	// Tool call fields
	ToolCalls []HuggingFaceChatStreamOutputDeltaToolCall `json:"tool_calls,omitempty"`
}

type HuggingFaceChatStreamOutputDeltaToolCall struct {
	Index    int                                 `json:"index"`
	ID       string                              `json:"id"`
	Type     string                              `json:"type"`
	Function HuggingFaceChatStreamOutputFunction `json:"function"`
}

type HuggingFaceChatStreamOutputFunction struct {
	Arguments string  `json:"arguments"`
	Name      *string `json:"name,omitempty"`
}

type HuggingFaceChatStreamOutputLogprobs struct {
	Content []HuggingFaceChatStreamOutputLogprob `json:"content"`
}

type HuggingFaceChatStreamOutputLogprob struct {
	Token       string                                  `json:"token"`
	Logprob     float32                                 `json:"logprob"`
	TopLogprobs []HuggingFaceChatStreamOutputTopLogprob `json:"top_logprobs"`
}

type HuggingFaceChatStreamOutputTopLogprob struct {
	Token   string  `json:"token"`
	Logprob float32 `json:"logprob"`
}

type HuggingFaceChatStreamOutputUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type HuggingFaceChatStreamOutputTimeInfo struct {
	QueueTime      float64 `json:"queue_time"`
	PromptTime     float64 `json:"prompt_time"`
	CompletionTime float64 `json:"completion_time"`
	TotalTime      float64 `json:"total_time"`
	Created        float64 `json:"created"`
}

// # RESPONSE TYPES

type HuggingFaceHubError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type HuggingFaceResponseError struct {
	Error   string `json:"error"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// # EMBEDDING TYPES

// HuggingFaceEmbeddingRequest represents the request format for HuggingFace embeddings API
// Based on the HuggingFace Router API specification
type HuggingFaceEmbeddingRequest struct {
	Input               interface{}   `json:"input"` // Can be string or []string
	Provider            string        `json:"provider,omitempty" validate:"required"`
	Model               string        `json:"model" validate:"required"`
	Normalize           *bool         `json:"normalize,omitempty"`
	PromptName          *string       `json:"prompt_name,omitempty"`
	Truncate            *bool         `json:"truncate,omitempty"`
	TruncationDirection *string       `json:"truncation_direction,omitempty"` // "left" or "right"
	EncodingFormat      *EncodingType `json:"encoding_format,omitempty"`
	Dimensions          *int          `json:"dimensions,omitempty"`
}

type EncodingType string

const (
	EncodingTypeFloat  EncodingType = "float"
	EncodingTypeBase64 EncodingType = "base64"
)

// HuggingFaceEmbeddingResponse represents the output of embeddings API
// Based on the actual HuggingFace Router API response format
type HuggingFaceEmbeddingResponse struct {
	Data  []HuggingFaceEmbeddingData `json:"data"`
	Model string                     `json:"model"`
	Usage *HuggingFaceEmbeddingUsage `json:"usage,omitempty"`
}

type HuggingFaceEmbeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
	Object    string    `json:"object"`
}

type HuggingFaceEmbeddingUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// # SPEECH TYPES

// Speech request represents the inputs for Text To Speech inference.
type HuggingFaceSpeechRequest struct {
	Text       string                       `json:"text"`
	Provider   string                       `json:"provider" validate:"required"`
	Model      string                       `json:"model" validate:"required"`
	Parameters *HuggingFaceSpeechParameters `json:"parameters,omitempty"`
	Extra      map[string]any               `json:"-"`
}

// Speech parameters are additional inference parameters for Text To Speech
type HuggingFaceSpeechParameters struct {
	GenerationParameters *HuggingFaceTranscriptionGenerationParameters `json:"generation_parameters,omitempty"`
	Extra                map[string]any                                `json:"-"`
}

// Speech response represents the outputs of inference for the Text To Speech task.
type HuggingFaceSpeechResponse struct {
	Audio HuggingFaceSpeechAudio `json:"audio"`
	Extra map[string]any         `json:"-"`
}

// HuggingFaceSpeechAudio represents the audio object in the speech response
type HuggingFaceSpeechAudio struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	FileName    string `json:"file_name"`
	FileSize    int    `json:"file_size"`
}

// # TRANSCRIPT TYPES

// HuggingFaceTranscriptionRequest represents the request for Automatic Speech Recognition inference
type HuggingFaceTranscriptionRequest struct {
	Inputs     []byte                                     `json:"inputs"`
	Provider   string                                     `json:"provider" validate:"required"`
	Model      string                                     `json:"model" validate:"required"`
	Parameters *HuggingFaceTranscriptionRequestParameters `json:"parameters,omitempty"`
}

// HuggingFaceTranscriptionRequestParameters contains additional inference parameters for Automatic Speech Recognition
type HuggingFaceTranscriptionRequestParameters struct {
	GenerationParameters *HuggingFaceTranscriptionGenerationParameters `json:"generation_parameters,omitempty"`
	ReturnTimestamps     *bool                                         `json:"return_timestamps,omitempty"`
}

// HuggingFaceTranscriptionGenerationParameters contains parametrization of the text generation process
type HuggingFaceTranscriptionGenerationParameters struct {
	DoSample      *bool                                  `json:"do_sample,omitempty"`
	EarlyStopping *HuggingFaceTranscriptionEarlyStopping `json:"early_stopping,omitempty"`
	EpsilonCutoff *float64                               `json:"epsilon_cutoff,omitempty"`
	EtaCutoff     *float64                               `json:"eta_cutoff,omitempty"`
	MaxLength     *int                                   `json:"max_length,omitempty"`
	MaxNewTokens  *int                                   `json:"max_new_tokens,omitempty"`
	MinLength     *int                                   `json:"min_length,omitempty"`
	MinNewTokens  *int                                   `json:"min_new_tokens,omitempty"`
	NumBeamGroups *int                                   `json:"num_beam_groups,omitempty"`
	NumBeams      *int                                   `json:"num_beams,omitempty"`
	PenaltyAlpha  *float64                               `json:"penalty_alpha,omitempty"`
	Temperature   *float64                               `json:"temperature,omitempty"`
	TopK          *int                                   `json:"top_k,omitempty"`
	TopP          *float64                               `json:"top_p,omitempty"`
	TypicalP      *float64                               `json:"typical_p,omitempty"`
	UseCache      *bool                                  `json:"use_cache,omitempty"`
}

// HuggingFaceTranscriptionEarlyStopping controls the stopping condition for beam-based methods
// Can be a boolean or the string "never"
type HuggingFaceTranscriptionEarlyStopping struct {
	BoolValue   *bool
	StringValue *string
}

// MarshalJSON implements custom JSON marshaling for HuggingFaceTranscriptionEarlyStopping
func (e HuggingFaceTranscriptionEarlyStopping) MarshalJSON() ([]byte, error) {
	if e.BoolValue != nil {
		return json.Marshal(*e.BoolValue)
	}
	if e.StringValue != nil {
		return json.Marshal(*e.StringValue)
	}
	return []byte("null"), nil
}

// UnmarshalJSON implements custom JSON unmarshaling for HuggingFaceTranscriptionEarlyStopping
func (e *HuggingFaceTranscriptionEarlyStopping) UnmarshalJSON(data []byte) error {
	// Try boolean first
	var boolVal bool
	if err := json.Unmarshal(data, &boolVal); err == nil {
		e.BoolValue = &boolVal
		return nil
	}

	// Try string
	var stringVal string
	if err := json.Unmarshal(data, &stringVal); err == nil {
		e.StringValue = &stringVal
		return nil
	}

	return nil
}

// HuggingFaceTranscriptionResponse represents the output of Automatic Speech Recognition inference
type HuggingFaceTranscriptionResponse struct {
	Text   string                                  `json:"text"`
	Chunks []HuggingFaceTranscriptionResponseChunk `json:"chunks,omitempty"`
}

// HuggingFaceTranscriptionResponseChunk represents an audio chunk identified by the model
type HuggingFaceTranscriptionResponseChunk struct {
	Text      string    `json:"text"`
	Timestamp []float64 `json:"timestamp"`
}

type HuggingFaceGenerationParameters = HuggingFaceTranscriptionGenerationParameters
type HuggingFaceEarlyStoppingUnion = HuggingFaceTranscriptionEarlyStopping
