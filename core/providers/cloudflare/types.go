package cloudflare

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

// CloudflareModelProperty is one property of a Workers AI model, such as context_window.
type CloudflareModelProperty struct {
	PropertyID string      `json:"property_id"`
	Value      interface{} `json:"value"`
}

// CloudflareModel is one model in the Workers AI model search response.
type CloudflareModel struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Task        CloudflareModelTask       `json:"task"`
	Properties  []CloudflareModelProperty `json:"properties"`
}

// CloudflareListModelsResponse is the response of GET /accounts/{account_id}/ai/models/search.
type CloudflareListModelsResponse struct {
	Result []CloudflareModel `json:"result"`
}
