package cloudflare

import "encoding/json"

// CloudflareListModelsResponse is Cloudflare's standard API envelope for model search.
type CloudflareListModelsResponse struct {
	Success    bool                  `json:"success"`
	Errors     []CloudflareAPIError  `json:"errors"`
	Messages   []json.RawMessage     `json:"messages"`
	Result     []CloudflareModel     `json:"result"`
	ResultInfo *CloudflareResultInfo `json:"result_info,omitempty"`
}

// CloudflareAPIError is an error entry in Cloudflare's API envelope.
type CloudflareAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// CloudflareResultInfo describes the pagination state returned by the API.
type CloudflareResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

// CloudflareModel is the subset of Workers AI model metadata surfaced by Bifrost.
type CloudflareModel struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Task        CloudflareModelTask       `json:"task"`
	CreatedAt   string                    `json:"created_at"`
	Properties  []CloudflareModelProperty `json:"properties"`
}

// CloudflareModelTask identifies the task family associated with a model.
type CloudflareModelTask struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CloudflareModelProperty is a provider-defined model metadata field.
type CloudflareModelProperty struct {
	PropertyID string          `json:"property_id"`
	Value      json.RawMessage `json:"value"`
}
