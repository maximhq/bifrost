package crowllm

import (
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// CrowLLMImageGenerationRequest represents a CrowLLM image generation request
type CrowLLMImageGenerationRequest struct {
	Model             *string                `json:"model"`
	Prompt            *string                `json:"prompt"`
	Loras             []CrowLLMLora          `json:"loras,omitempty"`               // List of compatible LoRAs
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
func (r *CrowLLMImageGenerationRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

type CrowLLMLora struct {
	URL   string `json:"url"`
	Scale int    `json:"scale"`
}

// CrowLLMImageGenerationResponse represents a CrowLLM image generation response
type CrowLLMImageGenerationResponse struct {
	Data []schemas.ImageData `json:"data"`
	Id   string              `json:"id"`
}

// CrowLLMError represents the error response format from CrowLLM API
type CrowLLMError struct {
	Detail *CrowLLMErrorDetail `json:"detail,omitempty"`
}

// CrowLLMErrorDetail handles both string (simple errors) and array (validation errors) formats
type CrowLLMErrorDetail struct {
	Message          *string                  `json:"-"`
	ValidationErrors []CrowLLMErrorDetailItem `json:"-"`
}

// CrowLLMErrorDetailItem represents a single validation error entry
type CrowLLMErrorDetailItem struct {
	Loc  []string `json:"loc"`
	Msg  string   `json:"msg"`
	Type string   `json:"type"`
}

// UnmarshalJSON implements custom JSON unmarshaling to handle both string and array formats from CrowLLM API.
func (d *CrowLLMErrorDetail) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as an array (validation errors)
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var validationErrors []CrowLLMErrorDetailItem
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
