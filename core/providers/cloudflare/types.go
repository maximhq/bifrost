package cloudflare

import (
	"github.com/maximhq/bifrost/core/providers/typesafe"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// CloudflareErrorDetail is one entry of the errors array in a Cloudflare API envelope.
type CloudflareErrorDetail struct {
	Code    interface{} `json:"code,omitempty"` // numeric for API errors, a string for request validation errors
	Message string      `json:"message,omitempty"`
}

// CloudflareError is the envelope Cloudflare returns for a failed request.
type CloudflareError struct {
	Success bool                    `json:"success"`
	Errors  []CloudflareErrorDetail `json:"errors"`
}

// CloudflareModelTask is the task a Workers AI model serves, such as "Text Generation".
type CloudflareModelTask struct {
	Name string `json:"name"`
}

// CloudflareModel is one model in the Workers AI model search response.
type CloudflareModel struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Task        CloudflareModelTask `json:"task"`
}

// CloudflareListModelsResponse is the response of GET /accounts/{account_id}/ai/models/search.
type CloudflareListModelsResponse struct {
	Result []CloudflareModel `json:"result"`
}

// CloudflareDecisionRequest is the body of POST /ai/run/@cf/cloudflare/{model}: Typesafe's
// systemone request plus Clef's images. It is only ever encoded; decoding it would run
// Typesafe's UnmarshalJSON on the embedded pointer and fail.
type CloudflareDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	Images []string `json:"images,omitempty"` // base64 data URLs
}

// CloudflareDecisionResponse is the Workers AI envelope around a Clef decision.
type CloudflareDecisionResponse struct {
	Result  *typesafe.TypesafeDecisionResponse `json:"result"`
	Success bool                               `json:"success"`
}

// CloudflareExtractionRequest is Clairic's body for POST /ai/run/@cf/cloudflare/clairic. It
// carries only the properties Clairic's schema defines, since Clairic answers 422 to any other.
type CloudflareExtractionRequest struct {
	Model         string                                 `json:"model"`
	State         string                                 `json:"state"`
	Fields        map[string]schemas.ExtractionFieldSpec `json:"fields"`
	Images        []schemas.ExtractionImage              `json:"images,omitempty"`
	ReferenceDate *string                                `json:"reference_date,omitempty"`
	Options       *CloudflareExtractionOptions           `json:"options,omitempty"`
	ExtraParams   map[string]interface{}                 `json:"-"`
}

// GetExtraParams implements providerUtils.RequestBodyWithExtraParams.
func (r *CloudflareExtractionRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// CloudflareExtractionOptions are Clairic's request options.
type CloudflareExtractionOptions struct {
	IncludeQuantiles *bool `json:"include_quantiles,omitempty"`
	ReturnStateText  *bool `json:"return_state_text,omitempty"`
}

// CloudflareExtractionUsage is Clairic's token accounting. Input tokens are the state, image,
// and schema tokens of every pass together.
type CloudflareExtractionUsage struct {
	StateTokens  int `json:"state_tokens"`
	ImageTokens  int `json:"image_tokens"`
	SchemaTokens int `json:"schema_tokens"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Passes       int `json:"passes"`
}

// CloudflareExtractionResult is Clairic's answer to an extraction request.
type CloudflareExtractionResult struct {
	Model     string                              `json:"model"`
	Answers   map[string]schemas.ExtractionAnswer `json:"answers"`
	Truncated bool                                `json:"truncated"`
	StateText *string                             `json:"state_text,omitempty"`
	Usage     *CloudflareExtractionUsage          `json:"usage,omitempty"`
}

// CloudflareExtractionResponse is the Workers AI envelope around a Clairic answer.
type CloudflareExtractionResponse struct {
	Result  *CloudflareExtractionResult `json:"result"`
	Success bool                        `json:"success"`
}
