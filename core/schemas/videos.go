package schemas

// VideoStatus is the lifecycle status of a video job.
type VideoStatus string

const (
	VideoStatusQueued     VideoStatus = "queued"
	VideoStatusInProgress VideoStatus = "in_progress"
	VideoStatusCompleted  VideoStatus = "completed"
	VideoStatusFailed     VideoStatus = "failed"
)

type VideoOutputType string

const (
	VideoOutputTypeBase64 VideoOutputType = "base64"
	VideoOutputTypeURL    VideoOutputType = "url"
)

// VideoCreateError is the error payload when video generation fails.
type VideoCreateError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ContentFilterInfo contains information about content that was filtered due to safety policies.
// This is a provider-agnostic structure for representing content filtering results.
type ContentFilterInfo struct {
	FilteredCount int      `json:"filtered_count,omitempty"` // Number of items filtered
	Reasons       []string `json:"reasons,omitempty"`        // Human-readable reasons for filtering
}

type VideoOutput struct {
	Type        VideoOutputType `json:"type"` // "url" | "base64"
	URL         *string         `json:"url,omitempty"`
	Base64Data  *string         `json:"base64,omitempty"`
	ContentType string          `json:"content_type"`
}

// VideoReferenceInput represents a reference image for video generation
type VideoReferenceInput struct {
	Image         []byte `json:"image"`                    // Image bytes
	ReferenceType string `json:"reference_type,omitempty"` // "style" or "asset" (Gemini: "REFERENCE_TYPE_STYLE" or "REFERENCE_TYPE_ASSET")
}

type VideoObject struct {
	ID                 string            `json:"id"`
	Object             string            `json:"object"` // always "video"
	Model              string            `json:"model"`
	Status             VideoStatus       `json:"status"`
	CreatedAt          int64             `json:"created_at"`
	CompletedAt        *int64            `json:"completed_at,omitempty"`
	ExpiresAt          *int64            `json:"expires_at,omitempty"`
	Progress           *float64          `json:"progress,omitempty"`
	Prompt             string            `json:"prompt"`
	RemixedFromVideoID *string           `json:"remixed_from_video_id,omitempty"`
	Seconds            *string           `json:"seconds"`
	Size               string            `json:"size"`
	Error              *VideoCreateError `json:"error,omitempty"`
}

// --- Video Generation ---

type BifrostVideoGenerationRequest struct {
	Provider       ModelProvider              `json:"provider"`
	Model          string                     `json:"model"`
	Input          *VideoGenerationInput      `json:"input"`
	Params         *VideoGenerationParameters `json:"params,omitempty"`
	Fallbacks      []Fallback                 `json:"fallbacks,omitempty"`
	RawRequestBody []byte                     `json:"-"`
}

func (b *BifrostVideoGenerationRequest) GetRawRequestBody() []byte {
	return b.RawRequestBody
}

func (b *BifrostVideoGenerationRequest) GetExtraParams() map[string]interface{} {
	if b == nil || b.Params == nil {
		return nil
	}
	return b.Params.ExtraParams
}

type VideoGenerationInput struct {
	Prompt         string  `json:"prompt"`
	InputReference *string `json:"input_reference,omitempty"` // Primary image for image-to-video (OpenAI-compatible)
}

type VideoGenerationParameters struct {
	Seconds *string `json:"seconds,omitempty"`
	Size    string  `json:"size,omitempty"`

	NegativePrompt *string        `json:"negative_prompt,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	VideoURI       *string        `json:"video_uri,omitempty"` // for video to video generation
	Audio          *bool          `json:"audio,omitempty"`
	ExtraParams    map[string]any `json:"-"`
}

// BifrostVideoGenerationResponse represents the video generation job response in bifrost format.
type BifrostVideoGenerationResponse struct {
	ID                 string             `json:"id,omitempty"`
	CompletedAt        *int64             `json:"completed_at,omitempty"`          // Unix timestamp (seconds) when the job completed
	CreatedAt          int64              `json:"created_at,omitempty"`            // Unix timestamp (seconds) when the job was created
	Error              *VideoCreateError  `json:"error,omitempty"`                 // Error payload if generation failed
	ExpiresAt          *int64             `json:"expires_at,omitempty"`            // Unix timestamp (seconds) when downloadable assets expire
	Model              string             `json:"model,omitempty"`                 // Video generation model that produced the job
	Object             string             `json:"object,omitempty"`                // Object type, always "video"
	Progress           *float64           `json:"progress,omitempty"`              // Approximate completion percentage (0-100)
	Prompt             string             `json:"prompt,omitempty"`                // Prompt used to generate the video
	RemixedFromVideoID *string            `json:"remixed_from_video_id,omitempty"` // Source video ID if this is a remix
	Seconds            *string            `json:"seconds,omitempty"`               // Duration of the generated clip in seconds
	Size               string             `json:"size,omitempty"`                  // Resolution of the generated video
	Status             VideoStatus        `json:"status,omitempty"`                // Current lifecycle status of the video job
	Videos             []VideoOutput      `json:"videos,omitempty"`                // Generated videos (supports multiple videos)
	ContentFilter      *ContentFilterInfo `json:"content_filter,omitempty"`        // Information about content filtering (if applicable)

	ExtraFields BifrostResponseExtraFields `json:"extra_fields,omitempty"`
}

// --- Video Remix ---

type BifrostVideoRemixRequest struct {
	ID             string                `json:"id"`
	Provider       ModelProvider         `json:"provider"`
	Input          *VideoGenerationInput `json:"input"`
	ExtraParams    map[string]any        `json:"-"`
	RawRequestBody []byte                `json:"-"`
}

func (b *BifrostVideoRemixRequest) GetRawRequestBody() []byte {
	return b.RawRequestBody
}

func (b *BifrostVideoRemixRequest) GetExtraParams() map[string]interface{} {
	if b == nil {
		return nil
	}
	return b.ExtraParams
}

// --- Video List ---

type BifrostVideoListRequest struct {
	Provider ModelProvider `json:"provider"`
	After    *string       `json:"after,omitempty"`
	Limit    *int          `json:"limit,omitempty"`
	Order    *string       `json:"order,omitempty"`
}

type BifrostVideoListResponse struct {
	Object      string                     `json:"object"` // "list"
	Data        []VideoObject              `json:"data"`
	FirstID     *string                    `json:"first_id,omitempty"`
	HasMore     *bool                      `json:"has_more,omitempty"`
	LastID      *string                    `json:"last_id,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// --- Video Retrieve / Delete ---

type BifrostVideoReferenceRequest struct {
	Provider ModelProvider `json:"provider"`
	ID       string        `json:"id"`
}

type BifrostVideoDeleteRequest = BifrostVideoReferenceRequest
type BifrostVideoRetrieveRequest = BifrostVideoReferenceRequest

type BifrostVideoDeleteResponse struct {
	ID          string                     `json:"id"`
	Deleted     bool                       `json:"deleted"`
	Object      string                     `json:"object,omitempty"` // "video.deleted"
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// --- Video Download ---

type BifrostVideoDownloadRequest struct {
	Provider    ModelProvider         `json:"provider"`
	ID          string                `json:"id"`
	Variant     *VideoDownloadVariant `json:"variant,omitempty"`
	ExtraParams map[string]any        `json:"-"`
}

type VideoDownloadVariant string

const (
	VideoDownloadVariantVideo       VideoDownloadVariant = "video"
	VideoDownloadVariantThumbnail   VideoDownloadVariant = "thumbnail"
	VideoDownloadVariantSpriteSheet VideoDownloadVariant = "sprite_sheet"
)

type BifrostVideoDownloadResponse struct {
	VideoID     string `json:"video_id"`
	Content     []byte `json:"-"`                      // Raw video content (not serialized)
	ContentType string `json:"content_type,omitempty"` // MIME type (e.g., "video/mp4", "image/png" for thumbnails)

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

type VideoLogParams struct {
	VideoID string `json:"video_id"`
}
