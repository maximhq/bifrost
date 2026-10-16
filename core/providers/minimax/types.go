package minimax

// MiniMaxBaseResp is the status envelope MiniMax attaches to most responses; a non-zero StatusCode is a failure even on HTTP 200.
type MiniMaxBaseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

// MiniMaxImageGenerationRequest is the body for POST /v1/image_generation.
type MiniMaxImageGenerationRequest struct {
	Model           string                 `json:"model"`
	Prompt          string                 `json:"prompt"`
	AspectRatio     *string                `json:"aspect_ratio,omitempty"`
	Width           *int                   `json:"width,omitempty"`
	Height          *int                   `json:"height,omitempty"`
	N               *int                   `json:"n,omitempty"`
	ResponseFormat  *string                `json:"response_format,omitempty"` // "url" or "base64"
	Seed            *int                   `json:"seed,omitempty"`
	PromptOptimizer *bool                  `json:"prompt_optimizer,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

func (r *MiniMaxImageGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// MiniMaxImageGenerationResponse is the body returned by POST /v1/image_generation.
type MiniMaxImageGenerationResponse struct {
	ID   string `json:"id"`
	Data struct {
		ImageURLs   []string `json:"image_urls,omitempty"`
		ImageBase64 []string `json:"image_base64,omitempty"`
	} `json:"data"`
	Metadata struct {
		SuccessCount any `json:"success_count,omitempty"`
		FailedCount  any `json:"failed_count,omitempty"`
	} `json:"metadata"`
	BaseResp *MiniMaxBaseResp `json:"base_resp,omitempty"`
}

// MiniMaxVideoContentItem is one entry of the H3 video request content array.
type MiniMaxVideoContentItem struct {
	Type     string                `json:"type"` // "text", "image_url", "video_url", "audio_url"
	Role     string                `json:"role,omitempty"`
	Text     string                `json:"text,omitempty"`
	ImageURL *MiniMaxVideoMediaURL `json:"image_url,omitempty"`
	VideoURL *MiniMaxVideoMediaURL `json:"video_url,omitempty"`
	AudioURL *MiniMaxVideoMediaURL `json:"audio_url,omitempty"`
}

// MiniMaxVideoMediaURL wraps a media URL, data URL or mm_file:// reference.
type MiniMaxVideoMediaURL struct {
	URL string `json:"url"`
}

// MiniMaxVideoGenerationRequest is the body for POST /v2/video_generation.
type MiniMaxVideoGenerationRequest struct {
	Model       string                    `json:"model"`
	Content     []MiniMaxVideoContentItem `json:"content"`
	Resolution  string                    `json:"resolution"`
	Duration    int                       `json:"duration"`
	Ratio       *string                   `json:"ratio,omitempty"`
	CallbackURL *string                   `json:"callback_url,omitempty"`
	ExtraParams map[string]interface{}    `json:"-"`
}

func (r *MiniMaxVideoGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// MiniMaxVideoTaskCreationResponse is returned by POST /v2/video_generation.
type MiniMaxVideoTaskCreationResponse struct {
	TaskID   string           `json:"task_id"`
	BaseResp *MiniMaxBaseResp `json:"base_resp,omitempty"`
}

type MiniMaxVideoTaskStatus string

const (
	MiniMaxVideoTaskStatusQueued    MiniMaxVideoTaskStatus = "queued"
	MiniMaxVideoTaskStatusRunning   MiniMaxVideoTaskStatus = "running"
	MiniMaxVideoTaskStatusSucceeded MiniMaxVideoTaskStatus = "succeeded"
	MiniMaxVideoTaskStatusFailed    MiniMaxVideoTaskStatus = "failed"
	MiniMaxVideoTaskStatusCancelled MiniMaxVideoTaskStatus = "cancelled"
)

// MiniMaxVideoTask is the task object returned by GET /v2/query/video_generation/{task_id}.
type MiniMaxVideoTask struct {
	ID        string                 `json:"id"`
	Model     string                 `json:"model"`
	Status    MiniMaxVideoTaskStatus `json:"status"`
	Error     *MiniMaxTaskError      `json:"error,omitempty"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
	Content   *struct {
		URL    string `json:"url,omitempty"`
		Prompt string `json:"prompt,omitempty"`
	} `json:"content,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Ratio      string `json:"ratio,omitempty"`
	Usage      *struct {
		TotalSeconds  float64 `json:"total_seconds,omitempty"`
		OutputSeconds float64 `json:"output_seconds,omitempty"`
	} `json:"usage,omitempty"`
}

// MiniMaxTaskError is the failure detail on a failed video task.
type MiniMaxTaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MiniMaxVideoTaskResponse wraps the queried task.
type MiniMaxVideoTaskResponse struct {
	Task     *MiniMaxVideoTask `json:"task"`
	BaseResp *MiniMaxBaseResp  `json:"base_resp,omitempty"`
}

// MiniMaxVideoDeleteResponse is returned by DELETE /v2/video_generation/{task_id}.
type MiniMaxVideoDeleteResponse struct {
	TaskID string `json:"task_id"`
	Action string `json:"action"` // "cancelled" or "deleted"
	Status string `json:"status"`
}
