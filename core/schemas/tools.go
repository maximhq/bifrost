package schemas

// BifrostSearchRequest represents a request to the DeepSeek Search API.
//
// Search is a web-search call rather than a model inference call: the upstream
// contract takes a query and returns ranked pages, and it carries no model. The
// Model field is therefore a routing and pricing handle for the gateway, not a
// value DeepSeek consumes.
type BifrostSearchRequest struct {
	Provider       ModelProvider          `json:"provider"`
	Model          string                 `json:"model,omitempty"`
	Query          string                 `json:"query"`
	Count          *int                   `json:"count,omitempty"`
	OriQuestion    *string                `json:"ori_question,omitempty"`
	Localization   *string                `json:"localization,omitempty"`
	NeedContent    *bool                  `json:"need_content,omitempty"`
	Fallbacks      []Fallback             `json:"fallbacks,omitempty"`
	ExtraParams    map[string]interface{} `json:"-"`
	RawRequestBody []byte                 `json:"-"`
}

// GetRawRequestBody returns the raw request body for the search request.
func (r *BifrostSearchRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// BifrostSearchResult is one ranked page returned by Search.
//
// Snippet is display-length text, Summary is a model-shaped paragraph, and Content carries
// page text only when the request asked for it. Date is kept as the upstream RFC 3339 string
// so a result is never silently re-zoned; Authority and RerankScore are pointers because the
// upstream uses -1 and 0 to mean "unknown" and "not scored" rather than a real measurement.
type BifrostSearchResult struct {
	Title       string   `json:"title,omitempty"`
	URL         string   `json:"url,omitempty"`
	Snippet     string   `json:"snippet,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Content     string   `json:"content,omitempty"`
	SiteName    string   `json:"site_name,omitempty"`
	SiteIcon    string   `json:"site_icon,omitempty"`
	Date        *string  `json:"date,omitempty"`
	Authority   *int     `json:"authority,omitempty"`
	RerankScore *float64 `json:"rerank_score,omitempty"`
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
