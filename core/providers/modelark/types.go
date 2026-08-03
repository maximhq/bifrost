package modelark

type ModelArkURLRef struct {
	URL string `json:"url"`
}

type ModelArkContentPart struct {
	Type     string          `json:"type"` // "text" or "image_url"
	Text     string          `json:"text,omitempty"`
	ImageURL *ModelArkURLRef `json:"image_url,omitempty"`
}

type ModelArkVideoGenerationRequest struct {
	Model         string                 `json:"model"`
	Content       []ModelArkContentPart  `json:"content"`
	Duration      *int                   `json:"duration,omitempty"`
	Ratio         *string                `json:"ratio,omitempty"`      // "adaptive" or an aspect ratio such as "16:9"
	Resolution    *string                `json:"resolution,omitempty"` // frame height tier: "480p", "720p", "1080p"
	Seed          *int                   `json:"seed,omitempty"`
	GenerateAudio *bool                  `json:"generate_audio,omitempty"`
	Watermark     *bool                  `json:"watermark,omitempty"`
	CallbackURL   *string                `json:"callback_url,omitempty"`
	ExtraParams   map[string]interface{} `json:"-"`
}

func (r *ModelArkVideoGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

type ModelArkTaskCreationResponse struct {
	ID    string         `json:"id"`
	Error *ModelArkError `json:"error,omitempty"`
}

type ModelArkTaskStatus string

const (
	ModelArkTaskStatusQueued    ModelArkTaskStatus = "queued"
	ModelArkTaskStatusRunning   ModelArkTaskStatus = "running"
	ModelArkTaskStatusSucceeded ModelArkTaskStatus = "succeeded"
	ModelArkTaskStatusFailed    ModelArkTaskStatus = "failed"
	ModelArkTaskStatusCancelled ModelArkTaskStatus = "cancelled"
)

type ModelArkTaskContent struct {
	VideoURL string `json:"video_url"`
}

type ModelArkTaskDetailsResponse struct {
	ID         string               `json:"id"`
	Model      string               `json:"model"`
	Status     ModelArkTaskStatus   `json:"status"`
	Content    *ModelArkTaskContent `json:"content,omitempty"`
	Error      *ModelArkError       `json:"error,omitempty"`
	CreatedAt  int64                `json:"created_at"`
	UpdatedAt  int64                `json:"updated_at"`
	Duration   int                  `json:"duration,omitempty"`
	Resolution string               `json:"resolution,omitempty"`
}

type ModelArkError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ModelArkAPIError struct {
	Error *ModelArkError `json:"error,omitempty"`
}
