package exa

import "github.com/maximhq/bifrost/core/schemas"

// defaultExaBaseURL is Exa's API root, used when the provider config names none.
const defaultExaBaseURL = "https://api.exa.ai"

// exaMaxResults is the largest numResults Exa accepts without a sales-raised limit.
const exaMaxResults = 100

// toBifrost maps a page onto the neutral result. Exa has no snippet of its own: its first
// highlight is one when the caller asked for highlights through ExtraParams. What has no neutral
// field is kept in ProviderFields under Exa's own names.
func (r exaResult) toBifrost() schemas.BifrostSearchResult {
	fields := map[string]interface{}{}
	for name, value := range map[string]string{"id": r.ID, "author": r.Author, "summary": r.Summary, "image": r.Image, "favicon": r.Favicon} {
		if value != "" {
			fields[name] = value
		}
	}
	if len(r.Highlights) > 1 {
		fields["highlights"] = r.Highlights
	}
	if len(fields) == 0 {
		fields = nil
	}
	result := schemas.BifrostSearchResult{
		URL:            r.URL,
		Title:          r.Title,
		Content:        r.Text,
		PublishedAt:    r.PublishedDate,
		ProviderFields: fields,
	}
	if len(r.Highlights) > 0 {
		result.Snippet = r.Highlights[0]
	}
	return result
}

// costUsage reports Exa's price as the call's usage, or nil when Exa reported none.
func costUsage(cost *exaCost) *schemas.BifrostLLMUsage {
	if cost == nil {
		return nil
	}
	return &schemas.BifrostLLMUsage{Cost: &schemas.BifrostCost{TotalCost: cost.Total}}
}

// attachRawFields copies optional raw request and response bodies onto the extra fields,
// leaving them unset when the caller did not ask for them.
func attachRawFields(extraFields *schemas.BifrostResponseExtraFields, rawRequest, rawResponse interface{}) {
	if rawRequest != nil {
		extraFields.RawRequest = rawRequest
	}
	if rawResponse != nil {
		extraFields.RawResponse = rawResponse
	}
}
