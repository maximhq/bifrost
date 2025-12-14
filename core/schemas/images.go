package schemas

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
	N              *int                   `json:"n,omitempty"`               // Number of images (1-10)
	Size           *string                `json:"size,omitempty"`            // "256x256", "512x512", "1024x1024", "1792x1024", "1024x1792", 1536x1024", "1024x1536", "auto"
	Quality        *string                `json:"quality,omitempty"`         // "auto", "high", "medium", "low"
	Style          *string                `json:"style,omitempty"`           // "natural", "vivid"
	ResponseFormat *string                `json:"response_format,omitempty"` // "url", "b64_json"
	User           *string                `json:"user,omitempty"`
	ExtraParams    map[string]interface{} `json:"extra_params,omitempty"`
}

// BifrostImageGenerationResponse represents the image generation response in bifrost format
type BifrostImageGenerationResponse struct {
	ID          string                     `json:"id"`
	Created     int64                      `json:"created"`
	Model       string                     `json:"model"`
	Data        []ImageData                `json:"data"`
	Usage       *ImageUsage                `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields,omitempty"`
}

type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	Index         int    `json:"index"`
}

type ImageUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Streaming Response
type BifrostImageGenerationStreamResponse struct {
	ID            string                     `json:"id"`
	Type          string                     `json:"type"`                     // "image_generation.partial_image", "image_generation.completed", "error"
	Index         int                        `json:"index"`                    // Which image (0-N)
	ChunkIndex    int                        `json:"chunk_index"`              // Chunk order within image
	PartialB64    string                     `json:"partial_b64,omitempty"`    // Base64 chunk
	RevisedPrompt string                     `json:"revised_prompt,omitempty"` // On first chunk
	Usage         *ImageUsage                `json:"usage,omitempty"`          // On final chunk
	Error         *BifrostError              `json:"error,omitempty"`
	ExtraFields   BifrostResponseExtraFields `json:"extra_fields"`
}
