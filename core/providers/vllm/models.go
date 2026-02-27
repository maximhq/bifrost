package vllm

// vLLMRerankRequest is the vLLM rerank request body.
type vLLMRerankRequest struct {
	Model           string                 `json:"model"`
	Query           string                 `json:"query"`
	Documents       []string               `json:"documents"`
	TopN            *int                   `json:"top_n,omitempty"`
	MaxTokensPerDoc *int                   `json:"max_tokens_per_doc,omitempty"`
	Priority        *int                   `json:"priority,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns passthrough parameters for providerUtils.CheckContextAndGetRequestBody.
func (r *vLLMRerankRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}
