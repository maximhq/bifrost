package huggingface

import (
	"bytes"
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
}

type HuggingFaceChatMessage struct {
	ID         *string                   `json:"id,omitempty"`
	Type       *string                   `json:"type,omitempty"`
	Name       *string                   `json:"name,omitempty"`
	Role       *string                   `json:"role,omitempty"`
	Content    HuggingFaceMessageContent `json:"content,omitempty"`
	ToolCalls  []HuggingFaceToolCall     `json:"tool_calls,omitempty"`
	ToolCallID *string                   `json:"tool_call_id,omitempty"`
}

// HuggingFaceMessageContent represents the flexible `content` field on
// HuggingFace chat messages. It can be either a plain string or an array
// of `HuggingFaceContentItem` objects.
type HuggingFaceMessageContent struct {
	String *string                  `json:"-"`
	Items  []HuggingFaceContentItem `json:"-"`
}

// UnmarshalJSON supports either a JSON string or an array of content items.
func (c *HuggingFaceMessageContent) UnmarshalJSON(data []byte) error {
	d := bytes.TrimSpace(data)
	if len(d) == 0 || bytes.Equal(d, []byte("null")) {
		return nil
	}

	// String
	if d[0] == '"' {
		var s string
		if err := json.Unmarshal(d, &s); err != nil {
			return err
		}
		c.String = &s
		return nil
	}

	// Array of content items
	if d[0] == '[' {
		var items []HuggingFaceContentItem
		if err := json.Unmarshal(d, &items); err != nil {
			return err
		}
		c.Items = items
		return nil
	}

	// Fallback: try object (single content item encoded as object)
	if d[0] == '{' {
		var item HuggingFaceContentItem
		if err := json.Unmarshal(d, &item); err == nil {
			c.Items = []HuggingFaceContentItem{item}
			return nil
		}
	}

	return fmt.Errorf("failed to unmarshal HuggingFaceMessageContent: unexpected JSON structure %s", string(d))
}

// MarshalJSON will emit either a JSON string or an array of content items.
func (c HuggingFaceMessageContent) MarshalJSON() ([]byte, error) {
	if c.String != nil {
		return json.Marshal(*c.String)
	}
	if c.Items != nil {
		return json.Marshal(c.Items)
	}
	return []byte("null"), nil
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
	Input               *InputsCustomType `json:"input,omitempty"`    // string or []string used by all inference providers other than hf-inference
	Inputs              *InputsCustomType `json:"inputs,omitempty"`   // string or []string used by hf-inference provider
	Provider            *string           `json:"provider,omitempty"` // used by all inference providers other than hf-inference
	Model               *string           `json:"model,omitempty"`    // used by all inference providers other than hf-inference
	Normalize           *bool             `json:"normalize,omitempty"`
	PromptName          *string           `json:"prompt_name,omitempty"`
	Truncate            *bool             `json:"truncate,omitempty"`
	TruncationDirection *string           `json:"truncation_direction,omitempty"` // "left" or "right"
	EncodingFormat      *EncodingType     `json:"encoding_format,omitempty"`
	Dimensions          *int              `json:"dimensions,omitempty"`
}

func (r *HuggingFaceEmbeddingRequest) MarshalJSON() ([]byte, error) {
	m := make(map[string]any)

	if r.Inputs != nil {
		m["inputs"] = r.Inputs
	} else if r.Input != nil {
		m["input"] = r.Input
	}

	if r.Provider != nil {
		m["provider"] = *r.Provider
	}
	if r.Model != nil {
		m["model"] = *r.Model
	}
	if r.Normalize != nil {
		m["normalize"] = *r.Normalize
	}
	if r.PromptName != nil {
		m["prompt_name"] = *r.PromptName
	}
	if r.Truncate != nil {
		m["truncate"] = *r.Truncate
	}
	if r.TruncationDirection != nil {
		m["truncation_direction"] = *r.TruncationDirection
	}
	if r.EncodingFormat != nil {
		m["encoding_format"] = *r.EncodingFormat
	}
	if r.Dimensions != nil {
		m["dimensions"] = *r.Dimensions
	}

	return json.Marshal(m)
}

type InputsCustomType struct {
	Texts []string `json:"texts,omitempty"`
	Text  *string  `json:"text,omitempty"`
}

func (i *InputsCustomType) UnmarshalJSON(data []byte) error {
	d := bytes.TrimSpace(data)
	if len(d) == 0 || bytes.Equal(d, []byte("null")) {
		return nil
	}

	// If it's a JSON string
	if d[0] == '"' {
		var singleText string
		if err := json.Unmarshal(d, &singleText); err == nil {
			i.Text = &singleText
			return nil
		}
	}

	// If it's a JSON array: ["a","b"]
	if d[0] == '[' {
		var texts []string
		if err := json.Unmarshal(d, &texts); err == nil {
			i.Texts = texts
			return nil
		}
	}

	if d[0] == '{' {
		var obj struct {
			Texts []string `json:"texts"`
			Text  *string  `json:"text"`
		}
		if err := json.Unmarshal(d, &obj); err == nil {
			if obj.Text != nil {
				i.Text = obj.Text
			}
			if obj.Texts != nil {
				i.Texts = obj.Texts
			}
			return nil
		}
	}

	return fmt.Errorf("failed to unmarshal InputsCustomType: expected string, array of strings, or object")
}

// MarshalJSON will encode InputsCustomType as either a JSON array of strings
// or a single JSON string (not wrapped in an object). This ensures the
// router-style `inputs` field is emitted as `"inputs": [..]` instead of
// `"inputs": {"texts": [...]}`.
func (i InputsCustomType) MarshalJSON() ([]byte, error) {
	if len(i.Texts) > 0 {
		return json.Marshal(i.Texts)
	}
	if i.Text != nil {
		return json.Marshal(*i.Text)
	}
	return []byte("null"), nil
}

type EncodingType string

const (
	EncodingTypeFloat  EncodingType = "float"
	EncodingTypeBase64 EncodingType = "base64"
)

// HuggingFaceEmbeddingResponse represents the output of embeddings API
// Based on the actual HuggingFace Router API response format
type HuggingFaceEmbeddingResponse struct {
	Data  []HuggingFaceEmbeddingData `json:"data,omitempty"`
	Model *string                    `json:"model,omitempty"`
	Usage *HuggingFaceEmbeddingUsage `json:"usage,omitempty"`
}

// UnmarshalJSON implements custom unmarshaling to handle both:
// 1. Standard JSON object format: {"data": [...], "model": "...", "usage": {...}}
// 2. Nested array format: [[[float32, ...], ...], ...]
func (r *HuggingFaceEmbeddingResponse) UnmarshalJSON(data []byte) error {
	// Try unmarshaling as standard object format first
	type Alias HuggingFaceEmbeddingResponse
	var obj Alias
	if err := json.Unmarshal(data, &obj); err == nil {
		// Check if it's actually an object with expected fields
		if obj.Data != nil || obj.Model != nil || obj.Usage != nil {
			*r = HuggingFaceEmbeddingResponse(obj)
			return nil
		}
	}

	// Try unmarshaling as nested array format: [[[float32, ...], ...], ...]
	var nestedArrays [][][]float32
	if err := json.Unmarshal(data, &nestedArrays); err == nil {
		// Convert nested arrays to HuggingFaceEmbeddingData format
		r.Data = make([]HuggingFaceEmbeddingData, 0)

		// Each top-level array represents embeddings for one input
		for inputIdx, inputEmbeddings := range nestedArrays {
			// For each input, we might have multiple token embeddings
			// We'll flatten them or take the last one (usually the sentence embedding)
			if len(inputEmbeddings) > 0 {
				// Take the last embedding as it's typically the pooled/sentence embedding
				lastEmbedding := inputEmbeddings[len(inputEmbeddings)-1]
				r.Data = append(r.Data, HuggingFaceEmbeddingData{
					Embedding: lastEmbedding,
					Index:     inputIdx,
					Object:    "embedding",
				})
			}
		}
		return nil
	}

	// Try unmarshaling as simple 2D array format: [[float32, ...], ...]
	var simpleArrays [][]float32
	if err := json.Unmarshal(data, &simpleArrays); err == nil {
		r.Data = make([]HuggingFaceEmbeddingData, 0, len(simpleArrays))
		for idx, embedding := range simpleArrays {
			r.Data = append(r.Data, HuggingFaceEmbeddingData{
				Embedding: embedding,
				Index:     idx,
				Object:    "embedding",
			})
		}
		return nil
	}

	return fmt.Errorf("failed to unmarshal HuggingFaceEmbeddingResponse: unexpected JSON structure")
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
}

// Speech response represents the outputs of inference for the Text To Speech task.
type HuggingFaceSpeechResponse struct {
	Audio HuggingFaceSpeechAudio `json:"audio"`
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
	Inputs     []byte                                     `json:"inputs,omitempty"`    // raw audio bytes
	AudioURL   string                                     `json:"audio_url,omitempty"` // URL to audio file only needed for fal ai
	Provider   *string                                    `json:"provider,omitempty"`
	Model      *string                                    `json:"model,omitempty"`
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

	return fmt.Errorf("early_stopping must be a boolean or string, got: %s", string(data))
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
