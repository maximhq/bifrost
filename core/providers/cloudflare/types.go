package cloudflare

import "github.com/maximhq/bifrost/core/providers/typesafe"

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
