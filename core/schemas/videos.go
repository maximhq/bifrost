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
	ID          string          `json:"id,omitempty"` // provider-side asset identifier, when one is assigned
	Type        VideoOutputType `json:"type"`         // "url" | "base64"
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
	VideoURI       *string `json:"video_uri,omitempty"`       // Source video for video-to-video and video tool tasks
}

type VideoGenerationParameters struct {
	Seconds *string `json:"seconds,omitempty"`
	Size    string  `json:"size,omitempty"`

	NegativePrompt *string        `json:"negative_prompt,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	Type           *string        `json:"type,omitempty"` // operation selector, e.g. "3d", "upscale"
	Audio          *bool          `json:"audio,omitempty"`
	ExtraParams    map[string]any `json:"-"`

	// Upscale operations (type "upscale"); mutually exclusive.
	UpscaleFactor    *int `json:"upscale_factor,omitempty"`
	TargetMegapixels *int `json:"target_megapixels,omitempty"`
}

// DefaultVideoDuration is the default video duration in seconds for Gemini/Vertex when not specified.
const DefaultVideoDuration = "8"

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
	Usage              *VideoUsage        `json:"usage,omitempty"`                 // Provider-reported usage/cost, when the provider returns it

	ExtraFields BifrostResponseExtraFields `json:"extra_fields,omitempty"`
}

// VideoUsage carries provider-reported usage for a video (or other async media) generation job.
// Currently only the provider-computed cost is modeled; providers that report an exact price
// (e.g. Runware's per-task cost) populate Cost so pricing uses it verbatim instead of a datasheet
// estimate.
type VideoUsage struct {
	Cost *BifrostCost `json:"cost,omitempty"`
}

// getSecondsFromVideoRequest extracts Seconds from video-related requests.
func getSecondsFromVideoRequest(req *BifrostRequest) *string {
	if req == nil {
		return nil
	}
	useDefaultForSeconds := func(p ModelProvider) bool {
		return p == Gemini || p == Vertex
	}
	if req.VideoGenerationRequest != nil {
		var seconds *string
		if req.VideoGenerationRequest.Params != nil {
			seconds = req.VideoGenerationRequest.Params.Seconds
		}
		if seconds == nil && useDefaultForSeconds(req.VideoGenerationRequest.Provider) {
			seconds = Ptr(DefaultVideoDuration)
		}
		return seconds
	}
	if req.VideoRemixRequest != nil && useDefaultForSeconds(req.VideoRemixRequest.Provider) {
		return Ptr(DefaultVideoDuration)
	}
	return nil
}

// BackfillParams populates response fields from the original request that are needed
// for cost calculation but may not be returned by the provider.
// - Seconds (duration from request params or default)
func (r *BifrostVideoGenerationResponse) BackfillParams(req *BifrostRequest) {
	if r == nil || req == nil {
		return
	}
	seconds := getSecondsFromVideoRequest(req)
	if seconds != nil {
		r.Seconds = seconds
	}
	if r.Model == "" {
		switch {
		case req.VideoGenerationRequest != nil:
			r.Model = req.VideoGenerationRequest.Model
		case req.VideoEditRequest != nil:
			r.Model = req.VideoEditRequest.Model
		}
	}
}

// --- Video Edit ---

// BifrostVideoEditRequest represents a video edit request in bifrost format. Model is optional:
// when the source video is referenced by an ID that already carries its provider suffix, the
// provider is resolved from that ID and the upstream API infers the model from the source.
type BifrostVideoEditRequest struct {
	Provider       ModelProvider        `json:"provider"`
	Model          string               `json:"model,omitempty"`
	Input          *VideoEditInput      `json:"input"`
	Params         *VideoEditParameters `json:"params,omitempty"`
	Fallbacks      []Fallback           `json:"fallbacks,omitempty"`
	RawRequestBody []byte               `json:"-"`
}

// GetRawRequestBody implements [utils.RequestBodyGetter].
func (b *BifrostVideoEditRequest) GetRawRequestBody() []byte {
	return b.RawRequestBody
}

func (b *BifrostVideoEditRequest) GetExtraParams() map[string]interface{} {
	if b == nil || b.Params == nil {
		return nil
	}
	return b.Params.ExtraParams
}

type VideoEditInput struct {
	Video  VideoInput `json:"video"`
	Prompt string     `json:"prompt"`
}

// VideoInput is the source video for an edit, in whichever form the caller supplied it. Exactly
// one of the three is expected; providers that cannot accept a given form reject it.
type VideoInput struct {
	Video []byte `json:"video,omitempty"` // raw bytes, from a multipart upload
	URL   string `json:"url,omitempty"`   // public URL or data URI
	ID    string `json:"id,omitempty"`    // provider-side video ID
}

// VideoEditParameters holds the knobs video edit APIs actually accept. Geometry and duration are
// deliberately absent: an edit inherits both from its source video, so no provider takes them.
type VideoEditParameters struct {
	Type         *string        `json:"type,omitempty"` // operation selector, e.g. "upscale", "background_removal"
	Seed         *int           `json:"seed,omitempty"`
	OutputFormat *string        `json:"output_format,omitempty"` // container, e.g. "mp4", "webm", "mov"
	ExtraParams  map[string]any `json:"-"`

	// Upscale operations (type "upscale"); mutually exclusive.
	UpscaleFactor    *int `json:"upscale_factor,omitempty"`
	TargetMegapixels *int `json:"target_megapixels,omitempty"`
}

// BifrostVideoEditResponse is the video edit job response. Video edit returns the same job object
// as generation, so callers poll and download it through the existing video endpoints.
type BifrostVideoEditResponse = BifrostVideoGenerationResponse

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
