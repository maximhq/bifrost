package schemas

// BifrostSearchRequest is a vendor-neutral web search. Model is a routing and pricing
// handle for the gateway; no search vendor consumes it. A filter the chosen provider
// cannot honour is refused rather than silently dropped. Vendor-only options travel in
// ExtraParams.
type BifrostSearchRequest struct {
	Provider        ModelProvider          `json:"provider"`
	Model           string                 `json:"model,omitempty"`
	Query           string                 `json:"query"`
	MaxResults      *int                   `json:"max_results,omitempty"`
	IncludeContent  *bool                  `json:"include_content,omitempty"`
	IncludeDomains  []string               `json:"include_domains,omitempty"`
	ExcludeDomains  []string               `json:"exclude_domains,omitempty"`
	PublishedAfter  *string                `json:"published_after,omitempty"`  // RFC 3339 date or date-time
	PublishedBefore *string                `json:"published_before,omitempty"` // RFC 3339 date or date-time
	Fallbacks       []Fallback             `json:"fallbacks,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
	RawRequestBody  []byte                 `json:"-"`
}

// GetRawRequestBody returns the raw request body for the search request.
func (r *BifrostSearchRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// BifrostSearchResult is one ranked page. Content is set only when the request asked for
// it. Score is the provider's own relevance score and is not comparable across providers.
// ProviderFields carries what only one vendor returns (DeepSeek's authority, Exa's
// favicon, ...).
type BifrostSearchResult struct {
	URL            string                 `json:"url"`
	Title          string                 `json:"title,omitempty"`
	Snippet        string                 `json:"snippet,omitempty"`
	Content        string                 `json:"content,omitempty"`
	PublishedAt    *string                `json:"published_at,omitempty"`
	Score          *float64               `json:"score,omitempty"`
	ProviderFields map[string]interface{} `json:"provider_fields,omitempty"`
}

// BifrostSearchResponse represents the response from a search request.
//
// An empty Results slice is a normal no-match answer, not an error.
type BifrostSearchResponse struct {
	Results     []BifrostSearchResult      `json:"results"`
	Usage       *BifrostLLMUsage           `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// BifrostFetchRequest represents a request to the DeepSeek Fetch API.
//
// Fetch retrieves one page as Markdown. Like Search it carries no model upstream, so Model
// is a gateway routing and pricing handle.
type BifrostFetchRequest struct {
	Provider       ModelProvider          `json:"provider"`
	Model          string                 `json:"model,omitempty"`
	URL            string                 `json:"url"`
	Fallbacks      []Fallback             `json:"fallbacks,omitempty"`
	ExtraParams    map[string]interface{} `json:"-"`
	RawRequestBody []byte                 `json:"-"`
}

// GetRawRequestBody returns the raw request body for the fetch request.
func (r *BifrostFetchRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// BifrostFetchResponse represents the response from a fetch request.
//
// Title is empty when the upstream could not determine one; Content is Markdown.
type BifrostFetchResponse struct {
	Title       string                     `json:"title,omitempty"`
	Content     string                     `json:"content"`
	Usage       *BifrostLLMUsage           `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
