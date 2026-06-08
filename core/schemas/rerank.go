package schemas

// RerankDocument represents a document to be reranked.
type RerankDocument struct {
	Text string                 `json:"text"`
	ID   *string                `json:"id,omitempty"`
	Meta map[string]interface{} `json:"meta,omitempty"`
}

// UnmarshalJSON accepts both Cohere/vLLM-compatible shapes for a rerank
// document: a bare JSON string ("hello") or a JSON object ({"text":"hello"}).
//
// The Cohere Rerank API and vLLM both accept either form, and many OpenAI-
// compatible clients (including Open WebUI) send documents as a string array
// (e.g. `"documents": ["a", "b"]`). Without this method, requests of that
// shape are rejected at JSON-decode time with a 400 from /v1/rerank.
func (d *RerankDocument) UnmarshalJSON(data []byte) error {
	// Try a bare string first.
	var s string
	if err := Unmarshal(data, &s); err == nil {
		d.Text = s
		d.ID = nil
		d.Meta = nil
		return nil
	}
	// Fall back to the object form. The alias trick avoids recursion
	// into this UnmarshalJSON while preserving the full field set.
	type alias RerankDocument
	var a alias
	if err := Unmarshal(data, &a); err != nil {
		return err
	}
	*d = RerankDocument(a)
	return nil
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
