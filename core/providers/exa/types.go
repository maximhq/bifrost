package exa

// exaSearchBody is the wire shape of an Exa /search request.
//
// ExtraParams carries Exa's own options (type, category, userLocation, ...) and is merged into
// the JSON by CheckContextAndGetRequestBody when extra-params passthrough is enabled.
type exaSearchBody struct {
	Query              string                 `json:"query"`
	NumResults         *int                   `json:"numResults,omitempty"`
	IncludeDomains     []string               `json:"includeDomains,omitempty"`
	ExcludeDomains     []string               `json:"excludeDomains,omitempty"`
	StartPublishedDate *string                `json:"startPublishedDate,omitempty"`
	EndPublishedDate   *string                `json:"endPublishedDate,omitempty"`
	Contents           *exaContentsOptions    `json:"contents,omitempty"`
	ExtraParams        map[string]interface{} `json:"-"`
}

// GetExtraParams returns the caller-supplied passthrough parameters.
func (b *exaSearchBody) GetExtraParams() map[string]interface{} { return b.ExtraParams }

// exaContentsOptions asks Exa for page text. It is sent only when the caller asked for content,
// because Exa prices text retrieval separately from the search itself.
type exaContentsOptions struct {
	Text bool `json:"text"`
}

// exaContentsBody is the wire shape of an Exa /contents request for one page.
type exaContentsBody struct {
	URLs        []string               `json:"urls"`
	Text        bool                   `json:"text"`
	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams returns the caller-supplied passthrough parameters.
func (b *exaContentsBody) GetExtraParams() map[string]interface{} { return b.ExtraParams }

// exaCost is Exa's own price for the call. Exa reports it on every response, which makes it the
// provider-reported cost the cost engine prefers over any catalog row.
type exaCost struct {
	Total float64 `json:"total"`
}

// exaResult is one page in a /search or /contents response.
type exaResult struct {
	ID            string   `json:"id,omitempty"`
	Title         string   `json:"title,omitempty"`
	URL           string   `json:"url,omitempty"`
	PublishedDate *string  `json:"publishedDate,omitempty"`
	Author        string   `json:"author,omitempty"`
	Text          string   `json:"text,omitempty"`
	Highlights    []string `json:"highlights,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	Image         string   `json:"image,omitempty"`
	Favicon       string   `json:"favicon,omitempty"`
}

type exaSearchWireResponse struct {
	Results     []exaResult `json:"results"`
	CostDollars *exaCost    `json:"costDollars,omitempty"`
}

// exaContentsStatus is Exa's per-URL outcome. A /contents call answers 200 even when the page
// could not be read, and reports that here.
type exaContentsStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Tag            string `json:"tag"`
		HTTPStatusCode *int   `json:"httpStatusCode"`
	} `json:"error,omitempty"`
}

type exaContentsWireResponse struct {
	Results     []exaResult         `json:"results"`
	Statuses    []exaContentsStatus `json:"statuses"`
	CostDollars *exaCost            `json:"costDollars,omitempty"`
}
