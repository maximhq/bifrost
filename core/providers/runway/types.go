package runway

import (
	"fmt"

	"github.com/bytedance/sonic"
)

type Reference struct {
	Type string `json:"type"` // always image
	URI  string `json:"uri"`
}

type ReferenceImage struct {
	URI string `json:"uri"`
	Tag string `json:"tag"`
}

type PromptImageObject struct {
	URI      string `json:"uri"`
	Position string `json:"position"`
}

type PromptImage struct {
	PromptImageStr    *string
	PromptImageObject []PromptImageObject
}

// custom marshal for PromptImage
// MarshalJSON implements custom JSON marshalling for PromptImage.
// It marshals either PromptImageStr or PromptImageObject directly without wrapping.
func (pi PromptImage) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if pi.PromptImageStr != nil && pi.PromptImageObject != nil {
		return nil, fmt.Errorf("both PromptImageStr and PromptImageObject are set; only one should be non-nil")
	}

	if pi.PromptImageStr != nil {
		return sonic.Marshal(*pi.PromptImageStr)
	}
	if pi.PromptImageObject != nil {
		return sonic.Marshal(pi.PromptImageObject)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for PromptImage.
// It determines whether "content" is a string or array and assigns to the appropriate field.
func (pi *PromptImage) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	pi.PromptImageStr = nil
	pi.PromptImageObject = nil

	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		pi.PromptImageStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of PromptImageObject
	var arrayContent []PromptImageObject
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		pi.PromptImageObject = arrayContent
		return nil
	}

	return fmt.Errorf("promptImage field is neither a string nor an array of PromptImageObject")
}

type RunwayVideoGenerationRequest struct {
	Model             string                 `json:"model"`
	PromptText        *string                `json:"promptText,omitempty"`
	PromptImage       *PromptImage           `json:"promptImage,omitempty"`
	VideoURI          *string                `json:"videoUri,omitempty"`
	References        []Reference            `json:"references,omitempty"`      // for video to video generation
	ReferenceImages   []ReferenceImage       `json:"referenceImages,omitempty"` // for text to video generation
	Seed              *int                   `json:"seed,omitempty"`
	Ratio             *string                `json:"ratio,omitempty"`
	Duration          *int                   `json:"duration,omitempty"`
	Audio             *bool                  `json:"audio,omitempty"` // for veo models
	ContentModeration *ContentModeration     `json:"contentModeration,omitempty"`
	ExtraParams       map[string]interface{} `json:"-"`
}

func (r *RunwayVideoGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

type ContentModeration struct {
	PublicFigureThreshold *string `json:"public_figure_threshold,omitempty"`
}

type RunwayTaskCreationResponse struct {
	ID string `json:"id"`
}

type RunwayTaskStatus string

const (
	RunwayTaskStatusPending   RunwayTaskStatus = "PENDING"
	RunwayTaskStatusThrottled RunwayTaskStatus = "THROTTLED"
	RunwayTaskStatusCancelled RunwayTaskStatus = "CANCELLED"
	RunwayTaskStatusRunning   RunwayTaskStatus = "RUNNING"
	RunwayTaskStatusFailed    RunwayTaskStatus = "FAILED"
	RunwayTaskStatusSucceeded RunwayTaskStatus = "SUCCEEDED"
)

type RunwayTaskDetailsResponse struct {
	Status    RunwayTaskStatus `json:"status"`
	ID        string           `json:"id"`
	CreatedAt string           `json:"created_at"`
	Output    []string         `json:"output,omitempty"`
}

type RunwayAPIError struct {
	Error string `json:"error"`
}
