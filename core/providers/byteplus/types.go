package byteplus

// BytePlusError is the error object Ark returns on failures, both as the HTTP error body and inside media results.
type BytePlusError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BytePlusSequentialImageOptions bounds a Seedream sequential (group) generation.
type BytePlusSequentialImageOptions struct {
	MaxImages int `json:"max_images"`
}

// BytePlusImageGenerationRequest is the body for POST /api/v3/images/generations.
type BytePlusImageGenerationRequest struct {
	Model                            string                          `json:"model"`
	Prompt                           string                          `json:"prompt"`
	Image                            []string                        `json:"image,omitempty"` // reference images as URLs or data URIs
	Size                             *string                         `json:"size,omitempty"`  // "WIDTHxHEIGHT" or a tier such as "2K"
	Seed                             *int                            `json:"seed,omitempty"`
	ResponseFormat                   *string                         `json:"response_format,omitempty"` // "url" or "b64_json"
	OutputFormat                     *string                         `json:"output_format,omitempty"`
	Background                       *string                         `json:"background,omitempty"` // "transparent", "opaque", "auto"
	Watermark                        *bool                           `json:"watermark,omitempty"`
	SequentialImageGeneration        *string                         `json:"sequential_image_generation,omitempty"` // "auto" or "disabled"
	SequentialImageGenerationOptions *BytePlusSequentialImageOptions `json:"sequential_image_generation_options,omitempty"`
	Stream                           *bool                           `json:"stream,omitempty"`
	ExtraParams                      map[string]interface{}          `json:"-"`
}

func (r *BytePlusImageGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// BytePlusImageData is one generated image. A failed image in a sequential batch carries Error instead of a payload.
type BytePlusImageData struct {
	URL     string         `json:"url,omitempty"`
	B64JSON string         `json:"b64_json,omitempty"`
	Size    string         `json:"size,omitempty"`
	Error   *BytePlusError `json:"error,omitempty"`
}

// BytePlusImageGenerationResponse is the body returned by POST /api/v3/images/generations.
type BytePlusImageGenerationResponse struct {
	Model     string              `json:"model"`
	Created   int64               `json:"created,omitempty"`
	CreatedAt int64               `json:"created_at,omitempty"`
	Data      []BytePlusImageData `json:"data"`
	Usage     *BytePlusImageUsage `json:"usage,omitempty"`
	Error     *BytePlusError      `json:"error,omitempty"`
}

// BytePlusImageUsage is the usage block of a Seedream response or completed stream event.
type BytePlusImageUsage struct {
	GeneratedImages int `json:"generated_images"`
	OutputTokens    int `json:"output_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

// Seedream stream event types. Each image arrives whole in its own event, and completed carries only usage.
const (
	bytePlusImageStreamPartialSucceeded = "image_generation.partial_succeeded"
	bytePlusImageStreamPartialFailed    = "image_generation.partial_failed"
	bytePlusImageStreamCompleted        = "image_generation.completed"
)

// BytePlusImageStreamEvent is one SSE event of a streamed Seedream generation.
type BytePlusImageStreamEvent struct {
	Type       string              `json:"type"`
	Model      string              `json:"model,omitempty"`
	Created    int64               `json:"created,omitempty"`
	ImageIndex *int                `json:"image_index,omitempty"`
	URL        string              `json:"url,omitempty"`
	B64JSON    string              `json:"b64_json,omitempty"`
	Size       string              `json:"size,omitempty"`
	Usage      *BytePlusImageUsage `json:"usage,omitempty"`
	Error      *BytePlusError      `json:"error,omitempty"`
}

// BytePlusVideoContentItem is one entry of the Seedance task content array.
type BytePlusVideoContentItem struct {
	Type      string                 `json:"type"` // "text", "image_url", "video_url", "audio_url", "draft_task"
	Role      string                 `json:"role,omitempty"`
	Text      string                 `json:"text,omitempty"`
	ImageURL  *BytePlusVideoMediaURL `json:"image_url,omitempty"`
	VideoURL  *BytePlusVideoMediaURL `json:"video_url,omitempty"`
	AudioURL  *BytePlusVideoMediaURL `json:"audio_url,omitempty"`
	DraftTask *BytePlusDraftTask     `json:"draft_task,omitempty"`
}

// BytePlusDraftTask names the draft task a final Seedance video is generated from.
type BytePlusDraftTask struct {
	ID string `json:"id"`
}

// BytePlusVideoMediaURL wraps a media URL or data URI.
type BytePlusVideoMediaURL struct {
	URL string `json:"url"`
}

// BytePlusVideoGenerationRequest is the body for POST /api/v3/contents/generations/tasks.
type BytePlusVideoGenerationRequest struct {
	Model         string                     `json:"model"`
	Content       []BytePlusVideoContentItem `json:"content"`
	Duration      *int                       `json:"duration,omitempty"`
	Resolution    *string                    `json:"resolution,omitempty"` // "480p", "720p", "1080p"
	Ratio         *string                    `json:"ratio,omitempty"`      // "16:9", "9:16", "1:1", ..., or "adaptive"
	Seed          *int                       `json:"seed,omitempty"`
	GenerateAudio *bool                      `json:"generate_audio,omitempty"`
	CallbackURL   *string                    `json:"callback_url,omitempty"`
	OutputFormat  *string                    `json:"output_format,omitempty"` // "mp4" or "mov"
	ExtraParams   map[string]interface{}     `json:"-"`
}

func (r *BytePlusVideoGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// BytePlusVideoTaskCreationResponse is returned by POST /api/v3/contents/generations/tasks.
type BytePlusVideoTaskCreationResponse struct {
	ID string `json:"id"`
}

type BytePlusVideoTaskStatus string

const (
	BytePlusVideoTaskStatusQueued    BytePlusVideoTaskStatus = "queued"
	BytePlusVideoTaskStatusRunning   BytePlusVideoTaskStatus = "running"
	BytePlusVideoTaskStatusSucceeded BytePlusVideoTaskStatus = "succeeded"
	BytePlusVideoTaskStatusFailed    BytePlusVideoTaskStatus = "failed"
	BytePlusVideoTaskStatusCancelled BytePlusVideoTaskStatus = "cancelled"
	BytePlusVideoTaskStatusExpired   BytePlusVideoTaskStatus = "expired"
)

// BytePlusVideoTask is the task object returned by GET /api/v3/contents/generations/tasks/{id}.
type BytePlusVideoTask struct {
	ID        string                  `json:"id"`
	Model     string                  `json:"model"`
	Status    BytePlusVideoTaskStatus `json:"status"`
	Error     *BytePlusError          `json:"error,omitempty"`
	CreatedAt int64                   `json:"created_at"`
	UpdatedAt int64                   `json:"updated_at"`
	Content   *struct {
		VideoURL     string `json:"video_url,omitempty"`
		LastFrameURL string `json:"last_frame_url,omitempty"`
	} `json:"content,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Ratio      string `json:"ratio,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Usage      *struct {
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// BytePlusVideoTaskListResponse is returned by GET /api/v3/contents/generations/tasks.
type BytePlusVideoTaskListResponse struct {
	Items []BytePlusVideoTask `json:"items"`
	Total int                 `json:"total"`
}
