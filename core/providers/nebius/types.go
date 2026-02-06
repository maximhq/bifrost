package nebius

import (
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// NebiusImageGenerationRequest represents a Nebius image generation request
type NebiusImageGenerationRequest struct {
	Model             *string                `json:"model"`
	Prompt            *string                `json:"prompt"`
	Loras             []NebiusLora           `json:"loras,omitempty"`               // List of compatible LoRAs
	Width             *int                   `json:"width,omitempty"`               // Width of output image (64-2048)
	Height            *int                   `json:"height,omitempty"`              // Height of output image (64-2048)
	NumInferenceSteps *int                   `json:"num_inference_steps,omitempty"` // number of denoising steps
	Seed              *int                   `json:"seed,omitempty"`                // seed for image generation
	GuidanceScale     *int                   `json:"guidance_scale,omitempty"`      // 0-100
	NegativePrompt    *string                `json:"negative_prompt,omitempty"`
	ResponseExtension *string                `json:"response_extension,omitempty"` // webp, jpg, png
	ResponseFormat    *string                `json:"response_format,omitempty"`    // b64_json, url
	Fallbacks         []string               `json:"fallbacks,omitempty"`
	ExtraParams       map[string]interface{} `json:"-"`
}

// GetExtraParams implements the RequestBodyWithExtraParams interface
func (r *NebiusImageGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

type NebiusLora struct {
	URL   string `json:"url"`
	Scale int    `json:"scale"`
}

// NebiusImageGenerationResponse represents a Nebius image generation response
type NebiusImageGenerationResponse struct {
	Data []schemas.ImageData `json:"data"`
	Id   string              `json:"id"`
}

// NebiusError represents the error response format from Nebius API
type NebiusError struct {
	Detail *NebiusErrorDetail `json:"detail,omitempty"`
}

// NebiusErrorDetail handles both string (simple errors) and array (validation errors) formats
type NebiusErrorDetail struct {
	Message          *string                 `json:"-"`
	ValidationErrors []NebiusErrorDetailItem `json:"-"`
}

// NebiusErrorDetailItem represents a single validation error entry
type NebiusErrorDetailItem struct {
	Loc  []string `json:"loc"`
	Msg  string   `json:"msg"`
	Type string   `json:"type"`
}

// UnmarshalJSON implements custom JSON unmarshaling to handle both string and array formats from Nebius API.
func (d *NebiusErrorDetail) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as an array (validation errors)
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var validationErrors []NebiusErrorDetailItem
		if err := sonic.Unmarshal(data, &validationErrors); err != nil {
			return err
		}
		d.ValidationErrors = validationErrors
		return nil
	}

	// If not an array, try to unmarshal as a string
	var message string
	if err := sonic.Unmarshal(data, &message); err != nil {
		return err
	}
	d.Message = &message
	return nil
}
