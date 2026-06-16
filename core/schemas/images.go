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

// BackfillParams populates response fields from the original request that are needed
// for cost calculation but may not be returned by the provider.
// - NumInputImages on ImageUsage (count of input images from the request)
// - Size on ImageGenerationResponseParameters (from request params if not in response)
// - Quality (low, medium, high, auto) only
func (r *BifrostImageGenerationResponse) BackfillParams(req *BifrostRequest) {
	if r == nil || req == nil {
		return
	}
	numInputImages, size, quality := getNumInputImagesSizeAndQualityFromRequest(req)

	// Backfill Model from whichever inner request carries it. Some provider APIs
	// (notably OpenAI /v1/images/*) omit model in the response body.
	if r.Model == "" {
		switch {
		case req.ImageGenerationRequest != nil:
			r.Model = req.ImageGenerationRequest.Model
		case req.ImageEditRequest != nil:
			r.Model = req.ImageEditRequest.Model
		case req.ImageVariationRequest != nil:
			r.Model = req.ImageVariationRequest.Model
		}
	}

	// Backfill NumInputImages
	if numInputImages > 0 {
		if r.Usage == nil {
			r.Usage = &ImageUsage{}
		}
		r.Usage.NumInputImages = numInputImages
	}

	// Backfill Size if not already present from provider response
	if size != "" && (r.ImageGenerationResponseParameters == nil || r.ImageGenerationResponseParameters.Size == "") {
		if r.ImageGenerationResponseParameters == nil {
			r.ImageGenerationResponseParameters = &ImageGenerationResponseParameters{}
		}
		r.ImageGenerationResponseParameters.Size = size
	}

	// Backfill Quality if not already present from provider response
	if quality != "" && (r.ImageGenerationResponseParameters == nil || r.ImageGenerationResponseParameters.Quality == "") {
		if r.ImageGenerationResponseParameters == nil {
			r.ImageGenerationResponseParameters = &ImageGenerationResponseParameters{}
		}
		r.ImageGenerationResponseParameters.Quality = quality
	}
}

// getModelFromRequest extracts the model from any image-related request.
func getModelFromRequest(req *BifrostRequest) string {
	if req == nil {
		return ""
	}
	switch {
	case req.ImageGenerationRequest != nil:
		return req.ImageGenerationRequest.Model
	case req.ImageEditRequest != nil:
		return req.ImageEditRequest.Model
	case req.ImageVariationRequest != nil:
		return req.ImageVariationRequest.Model
	}
	return ""
}

// getNumInputImagesSizeAndQualityFromRequest extracts request params for cost calculation.
// Quality is only returned when it is one of low, medium, high, auto.
func getNumInputImagesSizeAndQualityFromRequest(req *BifrostRequest) (numInputImages int, size string, quality string) {
	if req == nil {
		return 0, "", ""
	}

	switch {
	case req.ImageGenerationRequest != nil:
		if req.ImageGenerationRequest.Params != nil {
			p := req.ImageGenerationRequest.Params
			numInputImages = len(p.InputImages)
			if p.Size != nil {
				size = *p.Size
			}
			if p.Quality != nil {
				quality = normalizeImageQuality(*p.Quality)
			}
		}
	case req.ImageEditRequest != nil:
		if req.ImageEditRequest.Input != nil {
			numInputImages = len(req.ImageEditRequest.Input.Images)
		}
		if req.ImageEditRequest.Params != nil {
			p := req.ImageEditRequest.Params
			if p.Size != nil {
				size = *p.Size
			}
			if p.Quality != nil {
				quality = normalizeImageQuality(*p.Quality)
			}
		}
	case req.ImageVariationRequest != nil:
		if req.ImageVariationRequest.Input != nil {
			numInputImages = 1
		}
		if req.ImageVariationRequest.Params != nil && req.ImageVariationRequest.Params.Size != nil {
			size = *req.ImageVariationRequest.Params.Size
		}
	}
	return numInputImages, size, quality
}

// normalizeImageQuality returns the quality string only if it is supported by gpt-image-1.5 (low, medium, high, auto).
// All other values (hd, standard, etc.) are discarded and return empty.
func normalizeImageQuality(q string) string {
	switch q {
	case "low", "medium", "high", "auto":
		return q
	default:
		return ""
	}
}

type ImageGenerationResponseParameters struct {
	Background    string    `json:"background,omitempty"`
	OutputFormat  string    `json:"output_format,omitempty"`
	Quality       string    `json:"quality,omitempty"`
	Size          string    `json:"size,omitempty"`
	FinishReasons []*string `json:"finish_reasons,omitempty"`
	Seeds         []int     `json:"seeds,omitempty"`
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
	NumInputImages      int                `json:"num_input_images,omitempty"` // Number of input images from the request (populated by Bifrost)
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

// BackfillParams populates response fields from the original request that are needed
// for cost calculation but may not be returned by the provider.
// - NumInputImages on ImageUsage (count of input images from the request)
// - Size on ImageGenerationResponseParameters (from request params if not in response)
// - Quality (low, medium, high, auto) only
func (r *BifrostImageGenerationStreamResponse) BackfillParams(req *BifrostRequest) {
	numInputImages, size, quality := getNumInputImagesSizeAndQualityFromRequest(req)

	// Backfill NumInputImages
	if numInputImages > 0 {
		if r.Usage == nil {
			r.Usage = &ImageUsage{}
		}
		r.Usage.NumInputImages = numInputImages
	}

	// Backfill Size if not already present from provider response
	if size != "" && r.Size == "" {
		r.Size = size
	}

	// Backfill Quality if not already present (only low, medium, high, auto)
	if quality != "" && r.Quality == "" {
		r.Quality = quality
	}
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
	Type              *string                `json:"type,omitempty"`           // "inpainting", "outpainting", "background_removal", "remove_background", "erase_object", "recolor", "search_replace", "control_sketch", "control_structure", "style_guide", "style_transfer", "upscale_fast", "upscale_creative", "upscale_conservative"
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
