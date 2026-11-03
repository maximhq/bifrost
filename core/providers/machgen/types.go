package machgen

// # GENERATE TYPES

// MachGenVideoConfig is the video_config object of a MachGen generate request.
type MachGenVideoConfig struct {
	DurationSecs   *int    `json:"duration_secs,omitempty"`
	Height         *int    `json:"height,omitempty"`
	AspectRatio    *string `json:"aspect_ratio,omitempty"`
	Audio          *bool   `json:"audio,omitempty"`
	NegativePrompt *string `json:"negative_prompt,omitempty"`
}

// MachGenImageConfig is the image_config object of a MachGen generate request.
type MachGenImageConfig struct {
	Height      *int    `json:"height,omitempty"`
	AspectRatio *string `json:"aspect_ratio,omitempty"`
	InferSteps  *int    `json:"infer_steps,omitempty"`
}

// MachGenGenerateRequest is the body of POST /api/v0/generate. A single endpoint serves every
// model and task type; task_type selects the operation (T2V, I2V, R2V, T2I, I2I, ...).
type MachGenGenerateRequest struct {
	Model         string              `json:"model"`
	TaskType      string              `json:"task_type"`
	Prompt        string              `json:"prompt"`
	Seed          *int                `json:"seed,omitempty"`
	EnhancePrompt *bool               `json:"enhance_prompt,omitempty"`
	VideoConfig   *MachGenVideoConfig `json:"video_config,omitempty"`
	ImageConfig   *MachGenImageConfig `json:"image_config,omitempty"`

	SrcImageURLs      []string         `json:"src_image_urls,omitempty"`
	SrcVideoURLs      []string         `json:"src_video_urls,omitempty"`
	SrcAudioURLs      []string         `json:"src_audio_urls,omitempty"`
	SubjectToImageIDs map[string][]int `json:"subject_to_image_ids,omitempty"`
	SubjectToVideoIDs map[string][]int `json:"subject_to_video_ids,omitempty"`
	SubjectToAudioIDs map[string][]int `json:"subject_to_audio_ids,omitempty"`

	ExtraParams map[string]interface{} `json:"-"`
}

func (r *MachGenGenerateRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// MachGenGenerateResponse is the response of POST /api/v0/generate.
type MachGenGenerateResponse struct {
	TaskID string `json:"task_id"`
}

// # TASK TYPES

type MachGenTaskStatus string

const (
	MachGenTaskStatusPending   MachGenTaskStatus = "PENDING"
	MachGenTaskStatusRunning   MachGenTaskStatus = "RUNNING"
	MachGenTaskStatusCompleted MachGenTaskStatus = "COMPLETED"
	MachGenTaskStatusFailed    MachGenTaskStatus = "FAILED"
)

// MachGenTaskMetadata is the subset of the resolved task parameters Bifrost reads back.
type MachGenTaskMetadata struct {
	Prompt       string   `json:"prompt"`
	Model        string   `json:"model,omitempty"`
	Height       *int     `json:"height,omitempty"`
	Width        *int     `json:"width,omitempty"`
	DurationSecs *float64 `json:"duration_secs,omitempty"`
}

// MachGenTaskOutput maps an output type to the asset URL. Assets are served behind the same
// bearer token as the API, so they are fetched through /api/v0/assets rather than handed out.
type MachGenTaskOutput struct {
	Video string `json:"video,omitempty"`
	Image string `json:"image,omitempty"`
}

// MachGenTaskStatusResponse is the response of GET /api/v0/tasks/{task_id}.
type MachGenTaskStatusResponse struct {
	TaskID     string               `json:"task_id"`
	Status     MachGenTaskStatus    `json:"status"`
	ErrorMsg   *string              `json:"error_msg,omitempty"`
	Metadata   *MachGenTaskMetadata `json:"metadata,omitempty"`
	TaskOutput *MachGenTaskOutput   `json:"task_output,omitempty"`
}

// # ERROR TYPES

// MachGenAPIError is the body of a rejected request. detail is a string for most errors and a
// list of validation entries for 422.
type MachGenAPIError struct {
	Detail interface{} `json:"detail"`
}

// # MODELS TYPES

// MachGenModelSurface is one task type a model serves, e.g. T2V or I2I.
type MachGenModelSurface struct {
	TaskType string `json:"task_type"`
}

// MachGenModel is one entry of GET /api/v0/models: a model the API key may use.
type MachGenModel struct {
	Model    string                `json:"model"`
	Surfaces []MachGenModelSurface `json:"surfaces"`
}

// MachGenModelListResponse is the response of GET /api/v0/models.
type MachGenModelListResponse struct {
	Models []MachGenModel `json:"models"`
}
