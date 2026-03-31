package schemas

// RerankDocument represents a document to be reranked.
type RerankDocument struct {
	Text string                 `json:"text"`
	ID   *string                `json:"id,omitempty"`
	Meta map[string]interface{} `json:"meta,omitempty"`
}

// RerankParameters contains optional parameters for a rerank request.
type RerankParameters struct {
	TopN            *int                   `json:"top_n,omitempty"`
	MaxTokensPerDoc *int                   `json:"max_tokens_per_doc,omitempty"`
	Priority        *int                   `json:"priority,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// BifrostRerankRequest represents a request to rerank documents by relevance to a query.
type BifrostRerankRequest struct {
	Provider       ModelProvider     `json:"provider"`
	Model          string            `json:"model"`
	Query          string            `json:"query"`
	Documents      []RerankDocument  `json:"documents"`
	Params         *RerankParameters `json:"params,omitempty"`
	Fallbacks      []Fallback        `json:"fallbacks,omitempty"`
	RawRequestBody []byte            `json:"-"`
}

// GetRawRequestBody returns the raw request body for the rerank request.
func (r *BifrostRerankRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// RerankResult represents a single reranked document with its relevance score.
type RerankResult struct {
	Index          int             `json:"index"`
	RelevanceScore float64         `json:"relevance_score"`
	Document       *RerankDocument `json:"document,omitempty"`
}

// BifrostRerankResponse represents the response from a rerank request.
type BifrostRerankResponse struct {
	ID          string                     `json:"id,omitempty"`
	Results     []RerankResult             `json:"results"`
	Model       string                     `json:"model"`
	Usage       *BifrostLLMUsage           `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
