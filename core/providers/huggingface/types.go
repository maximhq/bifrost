package huggingface

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
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
	if err := sonic.Unmarshal(data, &arr); err == nil {
		r.Models = arr
		return nil
	}

	// Fallback: try object with a `models` field
	var obj struct {
		Models []HuggingFaceModel `json:"models"`
	}
	if err := sonic.Unmarshal(data, &obj); err == nil {
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
	Messages         []schemas.ChatMessage      `json:"messages"`
	Model            string                     `json:"model" validate:"required"`
	PresencePenalty  *float64                   `json:"presence_penalty,omitempty"`
	ResponseFormat   *HuggingFaceResponseFormat `json:"response_format,omitempty"`
	Seed             *int                       `json:"seed,omitempty"`
	Stop             []string                   `json:"stop,omitempty"`
	Stream           *bool                      `json:"stream,omitempty"`
	StreamOptions    *schemas.ChatStreamOptions `json:"stream_options,omitempty"`
	Temperature      *float64                   `json:"temperature,omitempty"`
	ToolChoice       *HuggingFaceToolChoice     `json:"tool_choice,omitempty"`
	ToolPrompt       *string                    `json:"tool_prompt,omitempty"`
	Tools            []schemas.ChatTool         `json:"tools,omitempty"`
	TopLogprobs      *int                       `json:"top_logprobs,omitempty"`
	TopP             *float64                   `json:"top_p,omitempty"`
}

// HuggingFaceToolChoice represents the flexible `tool_choice` field which
// can be either one of the enum strings: "auto", "none", "required",
// or an object with a `function` sub-object containing a required `name`.
type HuggingFaceToolChoice struct {
	EnumValue *EnumStringType

	// Function holds the function object when the field is a JSON object.
	Function *schemas.ChatToolChoiceFunction
}

type EnumStringType string

const (
	EnumStringTypeAuto     EnumStringType = "auto"
	EnumStringTypeNone     EnumStringType = "none"
	EnumStringTypeRequired EnumStringType = "required"
)

// MarshalJSON will emit either a JSON string for enum values or an object
// containing the `function` key and `type` field.
func (t HuggingFaceToolChoice) MarshalJSON() ([]byte, error) {
	if t.EnumValue != nil {
		return sonic.Marshal(*t.EnumValue)
	}
	if t.Function != nil {
		return sonic.Marshal(struct {
			Type     string                          `json:"type"`
			Function *schemas.ChatToolChoiceFunction `json:"function"`
		}{
			Type:     "function",
			Function: t.Function,
		})
	}
	return []byte("null"), nil
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

// # RESPONSE TYPES

type HuggingFaceHubError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type HuggingFaceResponseError struct {
	Error   string                   `json:"error"`
	Type    string                   `json:"type"`
	Message string                   `json:"message"`
	Detail  []HuggingFaceErrorDetail `json:"detail,omitempty"` // FastAPI validation errors
}

type HuggingFaceErrorDetail struct {
	Loc  []interface{}          `json:"loc"`
	Msg  string                 `json:"msg"`
	Type string                 `json:"type"`
	Ctx  map[string]interface{} `json:"ctx,omitempty"`
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

type InputsCustomType struct {
	Texts []string `json:"texts,omitempty"`
	Text  *string  `json:"text,omitempty"`
}

func (i *InputsCustomType) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}

	// Try string
	var text string
	if err := sonic.Unmarshal(data, &text); err == nil {
		i.Text = &text
		return nil
	}

	// Try array
	var texts []string
	if err := sonic.Unmarshal(data, &texts); err == nil {
		i.Texts = texts
		return nil
	}

	// Try object
	type alias InputsCustomType
	var obj alias
	if err := sonic.Unmarshal(data, &obj); err == nil {
		*i = InputsCustomType(obj)
		return nil
	}

	return fmt.Errorf("failed to unmarshal InputsCustomType: expected string, array, or object")
}

func (i InputsCustomType) MarshalJSON() ([]byte, error) {
	if len(i.Texts) > 0 {
		return sonic.Marshal(i.Texts)
	}
	if i.Text != nil {
		return sonic.Marshal(*i.Text)
	}
	return []byte("null"), nil
}

type EncodingType string

const (
	EncodingTypeFloat  EncodingType = "float"
	EncodingTypeBase64 EncodingType = "base64"
)

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
		return sonic.Marshal(*e.BoolValue)
	}
	if e.StringValue != nil {
		return sonic.Marshal(*e.StringValue)
	}
	return []byte("null"), nil
}

// UnmarshalJSON implements custom JSON unmarshaling for HuggingFaceTranscriptionEarlyStopping
func (e *HuggingFaceTranscriptionEarlyStopping) UnmarshalJSON(data []byte) error {
	// Try boolean first
	var boolVal bool
	if err := sonic.Unmarshal(data, &boolVal); err == nil {
		e.BoolValue = &boolVal
		return nil
	}

	// Try string
	var stringVal string
	if err := sonic.Unmarshal(data, &stringVal); err == nil {
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

type (
	HuggingFaceGenerationParameters = HuggingFaceTranscriptionGenerationParameters
	HuggingFaceEarlyStoppingUnion   = HuggingFaceTranscriptionEarlyStopping
)

// # IMAGE GENERATION TYPES

// HuggingFaceHFInferenceImageGenerationRequest for hf-inference image generation
type HuggingFaceHFInferenceImageGenerationRequest struct {
	Inputs string `json:"inputs"`
}

// HuggingFaceFalAIImageGenerationRequest for fal-ai image generation
type HuggingFaceFalAIImageGenerationRequest struct {
	Prompt                string                `json:"prompt"`
	NumImages             *int                  `json:"num_images,omitempty"`
	ResponseFormat        *string               `json:"response_format,omitempty"`
	ImageSize             *HuggingFaceFalAISize `json:"image_size,omitempty"`
	NegativePrompt        *string               `json:"negative_prompt,omitempty"`
	GuidanceScale         *float64              `json:"guidance_scale,omitempty"`
	NumInferenceSteps     *int                  `json:"num_inference_steps,omitempty"`
	Seed                  *int                  `json:"seed,omitempty"`
	OutputFormat          *string               `json:"output_format,omitempty"`
	SyncMode              *bool                 `json:"sync_mode,omitempty"`
	EnableSafetyChecker   *bool                 `json:"enable_safety_checker,omitempty"`
	Acceleration          *string               `json:"acceleration,omitempty"`
	EnablePromptExpansion *bool                 `json:"enable_prompt_expansion,omitempty"`
}

type HuggingFaceFalAISize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// HuggingFaceFalAIImageGenerationResponse for fal-ai image generation
// Matches the API envelope structure with top-level metadata and data array
type HuggingFaceFalAIImageGenerationResponse struct {
	RequestID string          `json:"request_id,omitempty"`
	Status    string          `json:"status,omitempty"`
	CreatedAt *int64          `json:"created_at,omitempty"`
	Data      *FalAIImageData `json:"data,omitempty"`
	// Legacy flattened fields for backward compatibility
	Images          []FalAIImage  `json:"images,omitempty"`
	Timings         *FalAITimings `json:"timings,omitempty"`
	Seed            *int64        `json:"seed,omitempty"`
	HasNSFWConcepts []bool        `json:"has_nsfw_concepts,omitempty"`
	Prompt          string        `json:"prompt,omitempty"`
}

// FalAIImageData wraps the image data in the API envelope
type FalAIImageData struct {
	Images          []FalAIImage  `json:"images,omitempty"`
	Timings         *FalAITimings `json:"timings,omitempty"`
	Seed            *int64        `json:"seed,omitempty"`
	HasNSFWConcepts []bool        `json:"has_nsfw_concepts,omitempty"`
	Prompt          string        `json:"prompt,omitempty"`
}

type FalAIImage struct {
	URL         string `json:"url,omitempty"`
	B64JSON     string `json:"b64_json,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type FalAITimings struct {
	Inference float64 `json:"inference"`
}

// HuggingFaceTogetherImageGenerationRequest for together image generation
type HuggingFaceTogetherImageGenerationRequest struct {
	Prompt         string  `json:"prompt"`
	Model          string  `json:"model"`
	ResponseFormat *string `json:"response_format,omitempty"`
	Size           *string `json:"size,omitempty"`
	Width          *int    `json:"width,omitempty"`
	Height         *int    `json:"height,omitempty"`
	N              *int    `json:"n,omitempty"`
	Steps          *int    `json:"steps,omitempty"`
}

// HuggingFaceTogetherImageGenerationResponse for together image generation
type HuggingFaceTogetherImageGenerationResponse struct {
	ID     string                         `json:"id"`
	Model  string                         `json:"model"`
	Object string                         `json:"object"`
	Data   []HuggingFaceTogetherImageData `json:"data"`
}

type HuggingFaceTogetherImageData struct {
	B64JSON string                      `json:"b64_json,omitempty"`
	URL     string                      `json:"url,omitempty"`
	Index   int                         `json:"index"`
	Timings *HuggingFaceTogetherTimings `json:"timings,omitempty"`
}

type HuggingFaceTogetherTimings struct {
	Inference float64 `json:"inference"`
}

// HuggingFaceFalAIImageStreamRequest for fal-ai image generation streaming
type HuggingFaceFalAIImageStreamRequest struct {
	Prompt                string                `json:"prompt"`
	ResponseFormat        *string               `json:"response_format,omitempty"`
	NumImages             *int                  `json:"num_images,omitempty"`
	ImageSize             *HuggingFaceFalAISize `json:"image_size,omitempty"`
	GuidanceScale         *float64              `json:"guidance_scale,omitempty"`
	Seed                  *int                  `json:"seed,omitempty"`
	NumInferenceSteps     *int                  `json:"num_inference_steps,omitempty"`
	Acceleration          *string               `json:"acceleration,omitempty"`
	EnablePromptExpansion *bool                 `json:"enable_prompt_expansion,omitempty"`
	SyncMode              *bool                 `json:"sync_mode,omitempty"`
	EnableSafetyChecker   *bool                 `json:"enable_safety_checker,omitempty"`
	OutputFormat          *string               `json:"output_format,omitempty"`
}

// HuggingFaceFalAIImageStreamResponse for fal-ai SSE events
type HuggingFaceFalAIImageStreamResponse struct {
	Data   *FalAIImageData `json:"data,omitempty"`
	Images []FalAIImage    `json:"images,omitempty"`
}

// HuggingFaceFalAIImageEditRequest for fal-ai image edit
type HuggingFaceFalAIImageEditRequest struct {
	Prompt              string                `json:"prompt"`
	ImageURL            *string               `json:"image_url,omitempty"`  // For single image models
	ImageURLs           []string              `json:"image_urls,omitempty"` // For multi-image models
	NumImages           *int                  `json:"num_images,omitempty"`
	ImageSize           *HuggingFaceFalAISize `json:"image_size,omitempty"`
	GuidanceScale       *float64              `json:"guidance_scale,omitempty"`
	NumInferenceSteps   *int                  `json:"num_inference_steps,omitempty"`
	SyncMode            *bool                 `json:"sync_mode,omitempty"`
	Seed                *int                  `json:"seed,omitempty"`
	OutputFormat        *string               `json:"output_format,omitempty"`
	EnableSafetyChecker *bool                 `json:"enable_safety_checker,omitempty"`
	Acceleration        *string               `json:"acceleration,omitempty"`
}
