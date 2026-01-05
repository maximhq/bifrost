package schemas

type ImageGenerationEventType string

const (
	ImageGenerationEventTypePartial   ImageGenerationEventType = "image_generation.partial_image"
	ImageGenerationEventTypeCompleted ImageGenerationEventType = "image_generation.completed"
	ImageGenerationEventTypeError     ImageGenerationEventType = "error"
)

// BifrostImageGenerationRequest represents an image generation request in bifrost format
type BifrostImageGenerationRequest struct {
	Provider       ModelProvider              `json:"provider"`
	Model          string                     `json:"model"`
	Input          *ImageGenerationInput      `json:"input"`
	Params         *ImageGenerationParameters `json:"params,omitempty"`
	Fallbacks      []Fallback                 `json:"fallbacks,omitempty"`
	RawRequestBody []byte                     `json:"-"`
}

// GetRawRequestBody implements utils.RequestBodyGetter.
func (b *BifrostImageGenerationRequest) GetRawRequestBody() []byte {
	return b.RawRequestBody
}

type ImageGenerationInput struct {
	Prompt string `json:"prompt"`
}

type ImageGenerationParameters struct {
	N                 *int                   `json:"n,omitempty"`                   // Number of images (1-10)
	Background        *string                `json:"background,omitempty"`          // "transparent", "opaque", "auto"
	Moderation        *string                `json:"moderation,omitempty"`          // "low", "auto"
	PartialImages     *int                   `json:"partial_images,omitempty"`      // 0-3
	Size              *string                `json:"size,omitempty"`                // "256x256", "512x512", "1024x1024", "1792x1024", "1024x1792", "1536x1024", "1024x1536", "auto"
	Quality           *string                `json:"quality,omitempty"`             // "auto", "high", "medium", "low", "hd", "standard"
	OutputCompression *int                   `json:"output_compression,omitempty"`  // compression level (0-100%)
	OutputFormat      *string                `json:"output_format,omitempty"`       // "png", "webp", "jpeg"
	Style             *string                `json:"style,omitempty"`               // "natural", "vivid"
	ResponseFormat    *string                `json:"response_format,omitempty"`     // "url", "b64_json"
	Seed              *int                   `json:"seed,omitempty"`                // seed for image generation
	NegativePrompt    *string                `json:"negative_prompt,omitempty"`     // negative prompt for image generation
	NumInferenceSteps *int                   `json:"num_inference_steps,omitempty"` // number of inference steps
	User              *string                `json:"user,omitempty"`
	ExtraParams       map[string]interface{} `json:"-"`
}

// BifrostImageGenerationResponse represents the image generation response in bifrost format
type BifrostImageGenerationResponse struct {
	ID      string      `json:"id,omitempty"`
	Created int64       `json:"created,omitempty"`
	Model   string      `json:"model,omitempty"`
	Data    []ImageData `json:"data"`

	*ImageGenerationResponseParameters

	Usage       *ImageUsage                `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields,omitempty"`
}

type ImageGenerationResponseParameters struct {
	Background   string `json:"background,omitempty"`
	OutputFormat string `json:"output_format,omitempty"`
	Quality      string `json:"quality,omitempty"`
	Size         string `json:"size,omitempty"`
}

type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	Index         int    `json:"index"`
}

type ImageUsage struct {
	InputTokens         int                `json:"input_tokens,omitempty"` // Always text tokens unless InputTokensDetails is not nil
	InputTokensDetails  *ImageTokenDetails `json:"input_tokens_details,omitempty"`
	TotalTokens         int                `json:"total_tokens,omitempty"`
	OutputTokens        int                `json:"output_tokens,omitempty"` // Always image tokens unless OutputTokensDetails is not nil
	OutputTokensDetails *ImageTokenDetails `json:"output_tokens_details,omitempty"`
}

type ImageTokenDetails struct {
	NImages     int `json:"-"` // Number of images generated (used internally for bifrost)
	ImageTokens int `json:"image_tokens,omitempty"`
	TextTokens  int `json:"text_tokens,omitempty"`
}

// Streaming Response
type BifrostImageGenerationStreamResponse struct {
	ID                string                     `json:"id,omitempty"`
	Type              ImageGenerationEventType   `json:"type,omitempty"`
	Index             int                        `json:"-"` // Which image (0-N)
	ChunkIndex        int                        `json:"-"` // Chunk order within image
	PartialImageIndex *int                       `json:"partial_image_index,omitempty"`
	SequenceNumber    int                        `json:"sequence_number,omitempty"`
	B64JSON           string                     `json:"b64_json,omitempty"`
	URL               string                     `json:"url,omitempty"`
	CreatedAt         int64                      `json:"created_at,omitempty"`
	Size              string                     `json:"size,omitempty"`
	Quality           string                     `json:"quality,omitempty"`
	Background        string                     `json:"background,omitempty"`
	OutputFormat      string                     `json:"output_format,omitempty"`
	RevisedPrompt     string                     `json:"revised_prompt,omitempty"`
	Usage             *ImageUsage                `json:"usage,omitempty"`
	Error             *BifrostError              `json:"error,omitempty"`
	RawRequest        string                     `json:"-"`
	RawResponse       string                     `json:"-"`
	ExtraFields       BifrostResponseExtraFields `json:"extra_fields,omitempty"`
}
