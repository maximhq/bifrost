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

// VLLMRerankResponse is the vLLM rerank response body.
type VLLMRerankResponse struct {
	ID      string             `json:"id,omitempty"`
	Model   string             `json:"model,omitempty"`
	Usage   *VLLMRerankUsage   `json:"usage,omitempty"`
	Results []VLLMRerankResult `json:"results"`
}

// VLLMRerankResult is a single vLLM rerank result.
type VLLMRerankResult struct {
	Index          int      `json:"index"`
	RelevanceScore *float64 `json:"relevance_score,omitempty"`
	Score *float64 `json:"score,omitempty"`
}

// VLLMRerankUsage captures token counts returned by vLLM rerank.
// Each field has an alternate name to handle different vLLM versions.
type VLLMRerankUsage struct {
	PromptTokens     *int `json:"prompt_tokens,omitempty"`
	InputTokens      *int `json:"input_tokens,omitempty"`  // alternate for PromptTokens
	CompletionTokens *int `json:"completion_tokens,omitempty"`
	OutputTokens     *int `json:"output_tokens,omitempty"`  // alternate for CompletionTokens
	TotalTokens      *int `json:"total_tokens,omitempty"`
}
