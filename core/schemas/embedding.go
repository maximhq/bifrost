package schemas

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

type BifrostEmbeddingRequest struct {
	Provider       ModelProvider        `json:"provider"`
	Model          string               `json:"model"`
	Input          []EmbeddingContent   `json:"input,omitempty"`
	Params         *EmbeddingParameters `json:"params,omitempty"`
	Fallbacks      []Fallback           `json:"fallbacks,omitempty"`
	RawRequestBody []byte               `json:"-"` // set bifrost-use-raw-request-body to true in ctx to use the raw request body. Bifrost will directly send this to the downstream provider.
}

func (r *BifrostEmbeddingRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// BifrostBatchEmbeddingRequest supports batch embeddings where each item can carry its own
type BifrostBatchEmbeddingRequest struct {
	Provider       ModelProvider               `json:"provider"`
	Model          string                      `json:"model"`
	Params         *EmbeddingParameters        `json:"params,omitempty"` // default for all items
	Items          []BifrostEmbeddingBatchItem `json:"items"`
	Fallbacks      []Fallback                  `json:"fallbacks,omitempty"`
	RawRequestBody []byte                      `json:"-"`
}

func (r *BifrostBatchEmbeddingRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

func (r *BifrostBatchEmbeddingRequest) Validate() error {
	if r == nil || len(r.Items) == 0 {
		return fmt.Errorf("batch embedding request has no items")
	}
	for i, item := range r.Items {
		if err := item.Content.Validate(); err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}
	}
	return nil
}

// BifrostEmbeddingBatchItem is one entry in a BifrostBatchEmbeddingRequest.
// Params nil means inherit from BifrostBatchEmbeddingRequest.Params.
type BifrostEmbeddingBatchItem struct {
	Content EmbeddingContent     `json:"content"`
	Params  *EmbeddingParameters `json:"params,omitempty"`
}

// EffectiveParams returns the item-level Params if set, otherwise a clone of the
// batch-level default. The clone prevents concurrent batch items from racing on
// a shared defaultParams pointer when providers read (and occasionally write) it.
func (i *BifrostEmbeddingBatchItem) EffectiveParams(defaultParams *EmbeddingParameters) *EmbeddingParameters {
	if i.Params != nil {
		return i.Params
	}
	return defaultParams.Clone()
}

type BifrostEmbeddingResponse struct {
	Data        []EmbeddingData            `json:"data"` // Maps to "data" field in provider responses (e.g., OpenAI embedding format)
	Model       string                     `json:"model"`
	Object      string                     `json:"object"` // "list"
	Usage       *BifrostLLMUsage           `json:"usage"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BackfillParams copies request metadata into the response when the provider omitted it (e.g. model in JSON).
func (r *BifrostEmbeddingResponse) BackfillParams(request *BifrostEmbeddingRequest) {
	if r == nil || request == nil {
		return
	}
	if strings.TrimSpace(r.Model) == "" && strings.TrimSpace(request.Model) != "" {
		r.Model = request.Model
	}
}

type EmbeddingContent []EmbeddingContentPart

type EmbeddingContentPartType string

const (
	EmbeddingContentPartTypeText   EmbeddingContentPartType = "text"
	EmbeddingContentPartTypeImage  EmbeddingContentPartType = "image"
	EmbeddingContentPartTypeAudio  EmbeddingContentPartType = "audio"
	EmbeddingContentPartTypeFile   EmbeddingContentPartType = "file"
	EmbeddingContentPartTypeVideo  EmbeddingContentPartType = "video"
	EmbeddingContentPartTypeTokens EmbeddingContentPartType = "tokens"
)

// EmbeddingVideoConfig carries optional video segment parameters.
// Supported by providers that return per-segment embeddings (e.g. Vertex multimodalembedding@001).
type EmbeddingVideoConfig struct {
	StartOffsetSec *int `json:"start_offset_sec,omitempty"` // where to begin sampling
	EndOffsetSec   *int `json:"end_offset_sec,omitempty"`   // where to stop sampling
	IntervalSec    *int `json:"interval_sec,omitempty"`     // gap between embeddings (min 4s)
}

type EmbeddingContentPart struct {
	Type EmbeddingContentPartType `json:"type"`

	Text        *string               `json:"text,omitempty"`
	Image       *EmbeddingMediaPart   `json:"image,omitempty"`
	Audio       *EmbeddingMediaPart   `json:"audio,omitempty"`
	File        *EmbeddingMediaPart   `json:"file,omitempty"`
	Video       *EmbeddingMediaPart   `json:"video,omitempty"`
	VideoConfig *EmbeddingVideoConfig `json:"video_config,omitempty"` // optional segment config for video parts
	Tokens      []int                 `json:"tokens,omitempty"`
}

type EmbeddingMediaPart struct {
	Data     *string `json:"data,omitempty"`
	URL      *string `json:"url,omitempty"`
	MIMEType *string `json:"mime_type,omitempty"`
	Filename *string `json:"filename,omitempty"`
}

func (m *EmbeddingMediaPart) Validate() error {
	if m == nil {
		return fmt.Errorf("embedding media payload is nil")
	}
	set := 0
	if m.Data != nil {
		if *m.Data == "" {
			return fmt.Errorf("embedding media data is empty")
		}
		set++
	}
	if m.URL != nil {
		if *m.URL == "" {
			return fmt.Errorf("embedding media url is empty")
		}
		set++
	}
	if set != 1 {
		return fmt.Errorf("embedding media payload must set exactly one of data or url")
	}
	return nil
}

func (p EmbeddingContentPart) Validate() error {
	set := 0
	if p.Text != nil {
		set++
	}
	if p.Image != nil {
		set++
	}
	if p.Audio != nil {
		set++
	}
	if p.File != nil {
		set++
	}
	if p.Video != nil {
		set++
	}
	if len(p.Tokens) > 0 {
		set++
	}
	if set != 1 {
		return fmt.Errorf("embedding content part must set exactly one modality")
	}

	switch p.Type {
	case EmbeddingContentPartTypeText:
		if p.Text == nil || *p.Text == "" {
			return fmt.Errorf("embedding content part type %q requires text payload", p.Type)
		}
	case EmbeddingContentPartTypeImage:
		if p.Image == nil {
			return fmt.Errorf("embedding content part type %q requires image payload", p.Type)
		}
		return p.Image.Validate()
	case EmbeddingContentPartTypeAudio:
		if p.Audio == nil {
			return fmt.Errorf("embedding content part type %q requires audio payload", p.Type)
		}
		return p.Audio.Validate()
	case EmbeddingContentPartTypeFile:
		if p.File == nil {
			return fmt.Errorf("embedding content part type %q requires file payload", p.Type)
		}
		return p.File.Validate()
	case EmbeddingContentPartTypeVideo:
		if p.Video == nil {
			return fmt.Errorf("embedding content part type %q requires video payload", p.Type)
		}
		return p.Video.Validate()
	case EmbeddingContentPartTypeTokens:
		if len(p.Tokens) == 0 {
			return fmt.Errorf("embedding content part type %q requires tokens payload", p.Type)
		}
	default:
		return fmt.Errorf("unsupported embedding content part type %q", p.Type)
	}

	return nil
}

func (c EmbeddingContent) Validate() error {
	if len(c) == 0 {
		return fmt.Errorf("embedding content is empty")
	}
	for _, part := range c {
		if err := part.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ValidateEmbeddingInput validates a []EmbeddingContent input slice.
func ValidateEmbeddingInput(input []EmbeddingContent) error {
	if len(input) == 0 {
		return fmt.Errorf("embedding input is empty")
	}
	for _, content := range input {
		if err := content.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type EmbeddingParameters struct {
	EncodingFormat *string `json:"encoding_format,omitempty"` // Format for embedding output (e.g., "float", "base64")
	Dimensions     *int    `json:"dimensions,omitempty"`      // Number of dimensions for embedding output
	TaskType       *string `json:"task_type,omitempty"`       // Intended embedding task
	Title          *string `json:"title,omitempty"`           // Optional title for the content
	AutoTruncate   *bool   `json:"auto_truncate,omitempty"`   // Automatically truncate long inputs

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

// Encoding names for the representations a provider can return for one input.
const (
	EmbeddingEncodingFloat   = "float"
	EmbeddingEncodingInt8    = "int8"
	EmbeddingEncodingUint8   = "uint8"
	EmbeddingEncodingBinary  = "binary"
	EmbeddingEncodingUbinary = "ubinary"
	EmbeddingEncodingBase64  = "base64"
)

// Clone returns a shallow copy of p with ExtraParams deep-copied so callers
// cannot mutate the original map through the returned pointer.
func (p *EmbeddingParameters) Clone() *EmbeddingParameters {
	if p == nil {
		return nil
	}
	clone := *p
	clone.ExtraParams = maps.Clone(p.ExtraParams)
	return &clone
}

// EmbeddingModality identifies which modality produced an embedding vector.
// Only set when the provider returns separate vectors per modality (e.g. Vertex
// multimodalembedding@001 which returns textEmbedding, imageEmbedding, and
// videoEmbeddings as distinct vectors in the same embedding space).
// Empty for providers that return a single unified vector.
type EmbeddingModality string

const (
	EmbeddingModalityText  EmbeddingModality = "text"
	EmbeddingModalityImage EmbeddingModality = "image"
	EmbeddingModalityVideo EmbeddingModality = "video"
	EmbeddingModalityAudio EmbeddingModality = "audio"
)

// EmbeddingVideoSegment holds the time range of a video embedding segment.
// Only set when Modality is EmbeddingModalityVideo.
type EmbeddingVideoSegment struct {
	StartOffsetSec int `json:"start_offset_sec"`
	EndOffsetSec   int `json:"end_offset_sec"`
}

type EmbeddingData struct {
	Index        int                    `json:"index"`
	Object       string                 `json:"object"`                  // "embedding"
	Modality     EmbeddingModality      `json:"modality,omitempty"`      // set for multimodal providers
	VideoSegment *EmbeddingVideoSegment `json:"video_segment,omitempty"` // set for video modality only
	Embedding    EmbeddingStruct        `json:"embedding"`               // can be string, []float64, [][]float64, []int8, or []int32
	// EncodingFormat names the representation this vector was returned as when a
	// provider emits several for one input; int8/binary and uint8/ubinary are
	// otherwise indistinguishable once decoded into EmbeddingStruct.
	EncodingFormat string `json:"encoding_format,omitempty"`
}

// UnmarshalJSON routes the vector by its declared encoding. EmbeddingStruct decodes a
// JSON number array as []float64 on the first attempt, so without this an int8 or
// int32 representation would come back as floats — losing the distinction on any
// path that stores the response as JSON and reads it back, such as a cache hit.
func (d *EmbeddingData) UnmarshalJSON(data []byte) error {
	// alias suppresses this method to avoid infinite recursion; the outer
	// Embedding (json.RawMessage) shadows alias.Embedding so the vector is held
	// undecoded until the encoding is known.
	type alias EmbeddingData
	aux := &struct {
		*alias
		Embedding json.RawMessage `json:"embedding"`
	}{alias: (*alias)(d)}
	if err := Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.Embedding) == 0 {
		return nil
	}

	switch d.EncodingFormat {
	case EmbeddingEncodingInt8, EmbeddingEncodingBinary:
		var values []int8
		if err := Unmarshal(aux.Embedding, &values); err == nil {
			d.Embedding = EmbeddingStruct{EmbeddingInt8Array: values}
			return nil
		}
	case EmbeddingEncodingUint8, EmbeddingEncodingUbinary:
		var values []int32
		if err := Unmarshal(aux.Embedding, &values); err == nil {
			d.Embedding = EmbeddingStruct{EmbeddingInt32Array: values}
			return nil
		}
	}
	return d.Embedding.UnmarshalJSON(aux.Embedding)
}

type EmbeddingStruct struct {
	// Embedding responses preserve provider precision in normalized API output.
	EmbeddingStr        *string
	EmbeddingArray      []float64
	Embedding2DArray    [][]float64
	EmbeddingInt8Array  []int8  // for int8 / binary formats
	EmbeddingInt32Array []int32 // for uint8 / ubinary formats
}

func (be EmbeddingStruct) MarshalJSON() ([]byte, error) {
	if be.EmbeddingStr != nil {
		return MarshalSorted(be.EmbeddingStr)
	}
	if be.EmbeddingArray != nil {
		return MarshalSorted(be.EmbeddingArray)
	}
	if be.Embedding2DArray != nil {
		return MarshalSorted(be.Embedding2DArray)
	}
	if be.EmbeddingInt8Array != nil {
		return Marshal(be.EmbeddingInt8Array)
	}
	if be.EmbeddingInt32Array != nil {
		return Marshal(be.EmbeddingInt32Array)
	}
	return nil, fmt.Errorf("no embedding found")
}

func (be *EmbeddingStruct) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := Unmarshal(data, &stringContent); err == nil {
		be.EmbeddingStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of float64
	var arrayContent []float64
	if err := Unmarshal(data, &arrayContent); err == nil {
		be.EmbeddingArray = arrayContent
		return nil
	}

	// Try to unmarshal as a direct 2D array of float64
	var arrayContent2D [][]float64
	if err := Unmarshal(data, &arrayContent2D); err == nil {
		be.Embedding2DArray = arrayContent2D
		return nil
	}

	// Try to unmarshal as a direct array of int8
	var int8Content []int8
	if err := Unmarshal(data, &int8Content); err == nil {
		be.EmbeddingInt8Array = int8Content
		return nil
	}

	// Try to unmarshal as a direct array of int32
	var int32Content []int32
	if err := Unmarshal(data, &int32Content); err == nil {
		be.EmbeddingInt32Array = int32Content
		return nil
	}

	return fmt.Errorf("embedding field is neither a string, []float64, [][]float64, []int8, nor []int32")
}
