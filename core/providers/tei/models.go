package tei

// teiRerankRequest is Hugging Face Text Embeddings Inference's rerank request body.
type teiRerankRequest struct {
	Query               string                 `json:"query"`
	Texts               []string               `json:"texts"`
	TopN                *int                   `json:"top_n,omitempty"`
	MaxTokensPerDoc     *int                   `json:"max_tokens_per_doc,omitempty"`
	Priority            *int                   `json:"priority,omitempty"`
	Truncate            *bool                  `json:"truncate,omitempty"`
	TruncationDirection *string                `json:"truncation_direction,omitempty"`
	RawScores           *bool                  `json:"raw_scores,omitempty"`
	ReturnText          *bool                  `json:"return_text,omitempty"`
	ExtraParams         map[string]interface{} `json:"-"`
}

// GetExtraParams returns passthrough parameters for providerUtils.CheckContextAndGetRequestBody.
func (r *teiRerankRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

type teiRank struct {
	Index int     `json:"index"`
	Text  *string `json:"text,omitempty"`
	Score float64 `json:"score"`
}

type teiErrorResponse struct {
	Error     string `json:"error"`
	ErrorType string `json:"error_type"`
}
