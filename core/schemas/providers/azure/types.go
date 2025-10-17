package azure

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
	Status          string                 `json:"status"`
	Model           string                 `json:"model,omitempty"`
	FineTune        string                 `json:"fine_tune,omitempty"`
	Capabilities    AzureModelCapabilities `json:"capabilities,omitempty"`
	LifecycleStatus string                 `json:"lifecycle_status"`
	Deprecation     *AzureModelDeprecation `json:"deprecation,omitempty"`
	ID              string                 `json:"id"`
	CreatedAt       int                  `json:"created_at"`
	Object          string                 `json:"object"`
}

type AzureModelListResponse struct {
	Object string       `json:"object"`
	Data   []AzureModel `json:"data"`
}
