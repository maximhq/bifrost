package azure

// DefaultAzureAPIVersion is the default Azure OpenAI API version to use when not specified.
const DefaultAzureAPIVersion = "2024-10-21"

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
	CreatedAt       int64                    `json:"created_at"`
	Object          string                 `json:"object"`
}

type AzureListModelsResponse struct {
	Object string       `json:"object"`
	Data   []AzureModel `json:"data"`
}
