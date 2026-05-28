package azure

// AzureAPIVersionDefault is the default Azure API version for classic /deployments/ endpoints.
const AzureAPIVersionDefault = "2024-10-21"

// AzureAPIVersionPreview is the preview API version string.
const AzureAPIVersionPreview = "preview"

// AzureAPIVersionImageEditDefault is the default api-version for image-edit endpoints.
const AzureAPIVersionImageEditDefault = "2025-04-01-preview"

const AzureAnthropicAPIVersionDefault = "2023-06-01"

type AzureModelCapabilities struct {
	FineTune       bool `json:"fine_tune"`
	Inference      bool `json:"inference"`
	Completion     bool `json:"completion"`
	ChatCompletion bool `json:"chat_completion"`
	Embeddings     bool `json:"embeddings"`
}

type AzureModelDeprecation struct {
	FineTune  int64 `json:"fine_tune,omitempty"`
	Inference int64 `json:"inference,omitempty"`
}

type AzureModel struct {
	ID              string                 `json:"id"`
	Status          string                 `json:"status"`
	FineTune        string                 `json:"fine_tune,omitempty"`
	Capabilities    AzureModelCapabilities `json:"capabilities,omitempty"`
	LifecycleStatus string                 `json:"lifecycle_status"`
	Deprecation     *AzureModelDeprecation `json:"deprecation,omitempty"`
	CreatedAt       int64                  `json:"created_at"`
	Object          string                 `json:"object"`
}

type AzureListModelsResponse struct {
	Object string       `json:"object"`
	Data   []AzureModel `json:"data"`
}
