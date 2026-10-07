package schemas

import "github.com/bytedance/sonic"

// RerankDocument represents a document to be reranked.
//
// Text carries prose content. Data carries a structured body for providers that rank JSON
// natively; providers without native support serialize it into text. Meta rides along as
// passthrough metadata and is not ranked.
type RerankDocument struct {
	Text string                 `json:"text"`
	Data map[string]interface{} `json:"data,omitempty"`
	ID   *string                `json:"id,omitempty"`
	Meta map[string]interface{} `json:"meta,omitempty"`
}

// rerankDocumentFields mirrors RerankDocument without the custom unmarshaler, so the object
// branch below can decode without recursing.
type rerankDocumentFields struct {
	Text string                 `json:"text"`
	Data map[string]interface{} `json:"data,omitempty"`
	ID   *string                `json:"id,omitempty"`
	Meta map[string]interface{} `json:"meta,omitempty"`
}

// UnmarshalJSON accepts either a bare string or an object. Every rerank API in the wild takes
// `documents: ["a", "b"]`, so the canonical route accepts that form and normalizes it.
func (d *RerankDocument) UnmarshalJSON(data []byte) error {
	var text string
	if err := sonic.Unmarshal(data, &text); err == nil {
		*d = RerankDocument{Text: text}
		return nil
	}

	var fields rerankDocumentFields
	if err := sonic.Unmarshal(data, &fields); err != nil {
		return err
	}
	*d = RerankDocument(fields)
	return nil
}

// RerankParameters contains optional parameters for a rerank request.
type RerankParameters struct {
	TopN            *int                   `json:"top_n,omitempty"`
	MaxTokensPerDoc *int                   `json:"max_tokens_per_doc,omitempty"`
	Priority        *int                   `json:"priority,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	NextToken       *string                `json:"next_token,omitempty"`
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
//
// Index addresses the document by position. ID carries the caller's document identifier when the
// request supplied one; it is identity rather than content, so it survives even when documents are
// not returned. Document carries the content back and is set only when ReturnDocuments is on.
type RerankResult struct {
	Index          int             `json:"index"`
	RelevanceScore float64         `json:"relevance_score"`
	ID             *string         `json:"id,omitempty"`
	Document       *RerankDocument `json:"document,omitempty"`
}

// BifrostRerankResponse represents the response from a rerank request.
type BifrostRerankResponse struct {
	ID          string                     `json:"id,omitempty"`
	Results     []RerankResult             `json:"results"`
	Model       string                     `json:"model"`
	Usage       *BifrostLLMUsage           `json:"usage,omitempty"`
	NextToken   *string                    `json:"next_token,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
