package schemas

type ImageEventType string

const (
	ImageGenerationEventTypePartial   ImageEventType = "image_generation.partial_image"
	ImageGenerationEventTypeCompleted ImageEventType = "image_generation.completed"
	ImageGenerationEventTypeError     ImageEventType = "error"
	ImageEditEventTypePartial         ImageEventType = "image_edit.partial_image"
	ImageEditEventTypeCompleted       ImageEventType = "image_edit.completed"
	ImageEditEventTypeError           ImageEventType = "error"
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
	InputImages       []string               `json:"input_images,omitempty"` // input images for image generation, base64 encoded or URL
	AspectRatio       *string                `json:"aspect_ratio,omitempty"` // aspect ratio of the image
	Resolution        *string                `json:"resolution,omitempty"`   // resolution of the image
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
	Type              ImageEventType             `json:"type,omitempty"`
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

// BifrostImageEditRequest represents an image edit request in bifrost format
type BifrostImageEditRequest struct {
	Provider       ModelProvider        `json:"provider"`
	Model          string               `json:"model"`
	Input          *ImageEditInput      `json:"input"`
	Params         *ImageEditParameters `json:"params,omitempty"`
	Fallbacks      []Fallback           `json:"fallbacks,omitempty"`
	RawRequestBody []byte               `json:"-"`
}

// GetRawRequestBody implements [utils.RequestBodyGetter].
func (b *BifrostImageEditRequest) GetRawRequestBody() []byte {
	return b.RawRequestBody
}

type ImageEditInput struct {
	Images []ImageInput `json:"images"`
	Prompt string       `json:"prompt"`
}

type ImageInput struct {
	Image []byte `json:"image"`
}

type ImageEditParameters struct {
	Type              *string                `json:"type,omitempty"`           // "inpainting", "outpainting", "background_removal",
	Background        *string                `json:"background,omitempty"`     // "transparent", "opaque", "auto"
	InputFidelity     *string                `json:"input_fidelity,omitempty"` // "low", "high"
	Mask              []byte                 `json:"mask,omitempty"`
	N                 *int                   `json:"n,omitempty"`                  // number of images to generate (1-10)
	OutputCompression *int                   `json:"output_compression,omitempty"` // compression level (0-100%)
	OutputFormat      *string                `json:"output_format,omitempty"`      // "png", "webp", "jpeg"
	PartialImages     *int                   `json:"partial_images,omitempty"`     // 0-3
	Quality           *string                `json:"quality,omitempty"`            // "auto", "high", "medium", "low", "standard"
	ResponseFormat    *string                `json:"response_format,omitempty"`    // "url", "b64_json"
	Size              *string                `json:"size,omitempty"`               // "256x256", "512x512", "1024x1024", "1536x1024", "1024x1536", "auto"
	User              *string                `json:"user,omitempty"`
	NegativePrompt    *string                `json:"negative_prompt,omitempty"`     // negative prompt for image editing
	Seed              *int                   `json:"seed,omitempty"`                // seed for image editing
	NumInferenceSteps *int                   `json:"num_inference_steps,omitempty"` // number of inference steps
	ExtraParams       map[string]interface{} `json:"-"`
}

// BifrostImageVariationRequest represents an image variation request in bifrost format
type BifrostImageVariationRequest struct {
	Provider       ModelProvider             `json:"provider"`
	Model          string                    `json:"model"`
	Input          *ImageVariationInput      `json:"input"`
	Params         *ImageVariationParameters `json:"params,omitempty"`
	Fallbacks      []Fallback                `json:"fallbacks,omitempty"`
	RawRequestBody []byte                    `json:"-"`
}

// GetRawRequestBody implements [utils.RequestBodyGetter].
func (b *BifrostImageVariationRequest) GetRawRequestBody() []byte {
	return b.RawRequestBody
}

type ImageVariationInput struct {
	Image ImageInput `json:"image"`
}

type ImageVariationParameters struct {
	N              *int                   `json:"n,omitempty"`               // Number of images (1-10)
	ResponseFormat *string                `json:"response_format,omitempty"` // "url", "b64_json"
	Size           *string                `json:"size,omitempty"`            // "256x256", "512x512", "1024x1024", "1792x1024", "1024x1792", "1536x1024", "1024x1536", "auto"
	User           *string                `json:"user,omitempty"`
	ExtraParams    map[string]interface{} `json:"-"`
}

// BifrostImageVariationResponse represents the image variation response in bifrost format
// It uses the same structure as image generation response
type BifrostImageVariationResponse = BifrostImageGenerationResponse
