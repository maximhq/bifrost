package azure

import (
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// azureContentFilterErrorCode is the error code Azure OpenAI answers with when its
// content filter blocks a prompt.
const azureContentFilterErrorCode = "content_filter"

// parseAzureError parses an Azure OpenAI error the way any OpenAI-compatible error is
// parsed and, when Azure's content filter blocked the prompt, adds the categories that
// caused the block as Error.ContentFilterDetails. Without it the classification was only
// reachable through the raw provider response.
func parseAzureError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp)
	if bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != azureContentFilterErrorCode {
		return bifrostErr
	}
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return bifrostErr
	}
	bifrostErr.Error.ContentFilterDetails = azureContentFilterDetails(body)
	return bifrostErr
}

// azureContentFilterDetails reads the categories that blocked a prompt out of an Azure
// error body. Chat and text completions carry one result object under
// error.innererror.content_filter_result (content_filter_results in Azure's API spec);
// the Responses API carries a list under error.content_filters, one entry per classified
// source. Only filtered categories are kept, and of each only its name, severity and
// detected flag, so citations, blocklist names, offsets and the rest of the body stay
// out of the error. Returns nil when no category was filtered.
func azureContentFilterDetails(body []byte) *schemas.ContentFilterDetails {
	errorNode := gjson.GetBytes(body, "error")
	var categories []schemas.ContentFilterCategory
	collect := func(results gjson.Result) {
		if !results.IsObject() {
			return
		}
		results.ForEach(func(name, result gjson.Result) bool {
			categories = addAzureFilteredCategory(categories, name.String(), result)
			return true
		})
	}
	collect(errorNode.Get("innererror.content_filter_result"))
	collect(errorNode.Get("innererror.content_filter_results"))
	errorNode.Get("content_filters").ForEach(func(_, entry gjson.Result) bool {
		collect(entry.Get("content_filter_results"))
		return true
	})
	if len(categories) == 0 {
		return nil
	}
	return &schemas.ContentFilterDetails{Categories: categories}
}

// addAzureFilteredCategory appends the category when Azure filtered it, or folds it into
// an earlier entry for the same category: the Responses API classifies a category once
// per source, and the caller needs one verdict per category, at its worst severity.
func addAzureFilteredCategory(categories []schemas.ContentFilterCategory, name string, result gjson.Result) []schemas.ContentFilterCategory {
	var filtered bool
	var severity *string
	var detected *bool
	switch {
	case result.IsObject():
		filtered = result.Get("filtered").Bool()
		if s := result.Get("severity"); s.Type == gjson.String {
			severity = schemas.Ptr(s.String())
		}
		if d := result.Get("detected"); d.Type == gjson.True || d.Type == gjson.False {
			detected = schemas.Ptr(d.Bool())
		}
	case result.IsArray():
		// Older API versions list custom_blocklists as [{"filtered":...,"id":...}].
		result.ForEach(func(_, entry gjson.Result) bool {
			filtered = entry.Get("filtered").Bool()
			return !filtered
		})
	}
	if !filtered || name == "" {
		return categories
	}
	for i := range categories {
		if categories[i].Category != name {
			continue
		}
		if severity != nil && (categories[i].Severity == nil || azureSeverityRank(*severity) > azureSeverityRank(*categories[i].Severity)) {
			categories[i].Severity = severity
		}
		if detected != nil && (categories[i].Detected == nil || *detected) {
			categories[i].Detected = detected
		}
		return categories
	}
	return append(categories, schemas.ContentFilterCategory{Category: name, Severity: severity, Filtered: true, Detected: detected})
}

// azureSeverityRank orders Azure's severity levels so a merge keeps the worst one. An
// unrecognized level ranks lowest.
func azureSeverityRank(severity string) int {
	switch severity {
	case "safe":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	}
	return -1
}
