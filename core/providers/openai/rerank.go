package openai

import (
	"encoding/json"
	"sort"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAIRerankRequest converts a Bifrost rerank request to OpenAI-compatible format
func ToOpenAIRerankRequest(request *schemas.BifrostRerankRequest) *OpenAIRerankRequest {
	if request == nil {
		return nil
	}

	converted := &OpenAIRerankRequest{
		Model:     request.Model,
		Query:     request.Query,
		Documents: request.Documents,
	}
	if request.Params != nil {
		converted.TopN = request.Params.TopN
		converted.MaxTokensPerDoc = request.Params.MaxTokensPerDoc
		converted.Priority = request.Params.Priority
		converted.ExtraParams = request.Params.ExtraParams
	}
	return converted
}

// MarshalJSON serializes "documents" into the shape most OpenAI-compatible
// rerank servers expect: a plain string array when none of the documents
// carry an id/meta, otherwise a uniform array of {text,id,meta} objects
// (r.Documents marshals natively into that shape). The array is never a mix
// of strings and objects, since some strict string-array upstreams (e.g.
// llama.cpp) reject a "documents" array containing any non-string element.
// A nil/empty Documents slice is left untouched so it keeps marshaling to
// null/[] exactly as the native struct tag would.
func (r *OpenAIRerankRequest) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	type Alias OpenAIRerankRequest

	if len(r.Documents) == 0 {
		return sonic.Marshal((*Alias)(r))
	}

	allPlain := true
	for _, doc := range r.Documents {
		if doc.ID != nil || len(doc.Meta) > 0 {
			allPlain = false
			break
		}
	}
	if !allPlain {
		return sonic.Marshal((*Alias)(r))
	}

	plainDocs := make([]string, len(r.Documents))
	for i, doc := range r.Documents {
		plainDocs[i] = doc.Text
	}
	override := struct {
		*Alias
		Documents []string `json:"documents"`
	}{
		Alias:     (*Alias)(r),
		Documents: plainDocs,
	}
	return sonic.Marshal(override)
}

// ToBifrostRerankResponse converts an OpenAI-compatible rerank response to Bifrost format
func (response *OpenAIRerankResponse) ToBifrostRerankResponse(documents []schemas.RerankDocument, returnDocuments bool) *schemas.BifrostRerankResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostRerankResponse{
		ID: response.ID,
	}
	for _, result := range response.Results {
		rerankResult := schemas.RerankResult{
			Index:          result.Index,
			RelevanceScore: result.RelevanceScore,
		}
		if doc := parseOpenAIRerankDocument(result.Document); doc != nil {
			rerankResult.Document = doc
		}
		bifrostResponse.Results = append(bifrostResponse.Results, rerankResult)
	}
	sort.SliceStable(bifrostResponse.Results, func(i, j int) bool {
		if bifrostResponse.Results[i].RelevanceScore == bifrostResponse.Results[j].RelevanceScore {
			return bifrostResponse.Results[i].Index < bifrostResponse.Results[j].Index
		}
		return bifrostResponse.Results[i].RelevanceScore > bifrostResponse.Results[j].RelevanceScore
	})
	if returnDocuments {
		for i := range bifrostResponse.Results {
			// Preserve any document the upstream already returned; only backfill from the request otherwise.
			if bifrostResponse.Results[i].Document != nil {
				continue
			}
			resultIndex := bifrostResponse.Results[i].Index
			if resultIndex >= 0 && resultIndex < len(documents) {
				bifrostResponse.Results[i].Document = schemas.Ptr(documents[resultIndex])
			}
		}
	}
	if response.Usage != nil {
		bifrostResponse.Usage = response.Usage
	} else if response.Meta != nil {
		bifrostResponse.Usage = openAIRerankUsage(response.Meta.Tokens)
		if bifrostResponse.Usage == nil {
			bifrostResponse.Usage = openAIRerankUsage(response.Meta.BilledUnits)
		}
	}
	return bifrostResponse
}

// parseOpenAIRerankDocument parses a rerank document returned by the upstream,
// accepting either a bare string or an object with text/id/metadata fields.
func parseOpenAIRerankDocument(raw json.RawMessage) *schemas.RerankDocument {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if err := sonic.Unmarshal(raw, &text); err == nil {
		return &schemas.RerankDocument{Text: text}
	}

	var docMap map[string]interface{}
	if err := sonic.Unmarshal(raw, &docMap); err != nil {
		return nil
	}
	doc := &schemas.RerankDocument{}
	populated := false
	if text, ok := docMap["text"].(string); ok {
		doc.Text = text
		populated = true
	}
	if id, ok := docMap["id"].(string); ok {
		doc.ID = &id
		populated = true
	}
	meta := make(map[string]interface{})
	if rawMeta, ok := docMap["metadata"].(map[string]interface{}); ok {
		for k, v := range rawMeta {
			meta[k] = v
		}
	} else if rawMeta, ok := docMap["meta"].(map[string]interface{}); ok {
		for k, v := range rawMeta {
			meta[k] = v
		}
	}
	for k, v := range docMap {
		if k != "text" && k != "id" && k != "metadata" && k != "meta" {
			meta[k] = v
		}
	}
	if len(meta) > 0 {
		doc.Meta = meta
		populated = true
	}
	if !populated {
		return nil
	}
	return doc
}

// openAIRerankUsage maps rerank token/billing counts onto Bifrost usage.
func openAIRerankUsage(tokens *OpenAIRerankTokenUsage) *schemas.BifrostLLMUsage {
	if tokens == nil {
		return nil
	}
	promptTokens := 0
	completionTokens := 0
	hasUsage := false
	if tokens.InputTokens != nil {
		promptTokens = int(*tokens.InputTokens)
		hasUsage = true
	}
	if tokens.OutputTokens != nil {
		completionTokens = int(*tokens.OutputTokens)
		hasUsage = true
	}
	totalTokens := promptTokens + completionTokens
	if tokens.SearchUnits != nil {
		// Cohere-shaped rerank upstreams bill via billed_units.search_units instead of
		// token counts (input/output tokens are typically null for rerank). Surface the
		// search-unit count in the usage total so search-unit-billed providers aren't dropped.
		totalTokens += int(*tokens.SearchUnits)
		hasUsage = true
	}
	if !hasUsage {
		return nil
	}
	return &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
}
