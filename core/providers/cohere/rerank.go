package cohere

import (
	"sort"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToCohereRerankRequest converts a Bifrost rerank request to Cohere format
func ToCohereRerankRequest(bifrostReq *schemas.BifrostRerankRequest) *CohereRerankRequest {
	if bifrostReq == nil {
		return nil
	}

	cohereReq := &CohereRerankRequest{
		Model: bifrostReq.Model,
		Query: bifrostReq.Query,
	}

	documents := make([]CohereRerankDocument, len(bifrostReq.Documents))
	for i, doc := range bifrostReq.Documents {
		documents[i] = CohereRerankDocument{
			Text:     rerankDocumentText(doc),
			ID:       doc.ID,
			Metadata: doc.Meta,
		}
	}
	cohereReq.Documents = documents

	if bifrostReq.Params != nil {
		cohereReq.TopN = bifrostReq.Params.TopN
		cohereReq.MaxTokensPerDoc = bifrostReq.Params.MaxTokensPerDoc
		cohereReq.Priority = bifrostReq.Params.Priority
		cohereReq.ReturnDocuments = bifrostReq.Params.ReturnDocuments
		cohereReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return cohereReq
}

// ToBifrostRerankRequest converts a Cohere rerank request to Bifrost format
func (req *CohereRerankRequest) ToBifrostRerankRequest(ctx *schemas.BifrostContext) *schemas.BifrostRerankRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, "")

	bifrostReq := &schemas.BifrostRerankRequest{
		Provider: provider,
		Model:    model,
		Query:    req.Query,
		Params:   &schemas.RerankParameters{},
	}

	for _, doc := range req.Documents {
		bifrostReq.Documents = append(bifrostReq.Documents, schemas.RerankDocument{
			Text: doc.Text,
			ID:   doc.ID,
			Meta: doc.Metadata,
		})
	}

	if req.TopN != nil {
		bifrostReq.Params.TopN = req.TopN
	}
	if req.MaxTokensPerDoc != nil {
		bifrostReq.Params.MaxTokensPerDoc = req.MaxTokensPerDoc
	}
	if req.Priority != nil {
		bifrostReq.Params.Priority = req.Priority
	}
	if req.ReturnDocuments != nil {
		bifrostReq.Params.ReturnDocuments = req.ReturnDocuments
	}
	if req.ExtraParams != nil {
		bifrostReq.Params.ExtraParams = req.ExtraParams
	}

	return bifrostReq
}

// ToBifrostRerankResponse converts a Cohere rerank response to Bifrost format.
func (response *CohereRerankResponse) ToBifrostRerankResponse(documents []schemas.RerankDocument, returnDocuments bool) *schemas.BifrostRerankResponse {
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
		if result.Index >= 0 && result.Index < len(documents) {
			rerankResult.ID = documents[result.Index].ID
		}
		if result.Document != nil {
			rerankResult.Document = &schemas.RerankDocument{
				Text: result.Document.Text,
				ID:   result.Document.ID,
				Meta: result.Document.Metadata,
			}
			if rerankResult.ID == nil {
				rerankResult.ID = result.Document.ID
			}
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
			resultIndex := bifrostResponse.Results[i].Index
			if resultIndex >= 0 && resultIndex < len(documents) {
				bifrostResponse.Results[i].Document = schemas.Ptr(documents[resultIndex])
			}
		}
	}

	// Convert usage information
	if response.Meta != nil {
		promptTokens := 0
		completionTokens := 0
		hasTokenUsage := false
		if response.Meta.Tokens != nil {
			if response.Meta.Tokens.InputTokens != nil {
				promptTokens = int(*response.Meta.Tokens.InputTokens)
				hasTokenUsage = true
			}
			if response.Meta.Tokens.OutputTokens != nil {
				completionTokens = int(*response.Meta.Tokens.OutputTokens)
				hasTokenUsage = true
			}
		} else if response.Meta.BilledUnits != nil {
			if response.Meta.BilledUnits.InputTokens != nil {
				promptTokens = int(*response.Meta.BilledUnits.InputTokens)
				hasTokenUsage = true
			}
			if response.Meta.BilledUnits.OutputTokens != nil {
				completionTokens = int(*response.Meta.BilledUnits.OutputTokens)
				hasTokenUsage = true
			}
		}

		// Rerank bills in search units, not tokens; carry them so cost calculation has an input.
		var searchUnits *int
		if response.Meta.BilledUnits != nil && response.Meta.BilledUnits.SearchUnits != nil {
			searchUnits = response.Meta.BilledUnits.SearchUnits
		}

		if hasTokenUsage || searchUnits != nil {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			}
			bifrostResponse.Usage.SearchUnits = searchUnits
		}
	}

	return bifrostResponse
}

// ToCohereRerankResponse converts a Bifrost rerank response to Cohere format.
func ToCohereRerankResponse(bifrostResp *schemas.BifrostRerankResponse) *CohereRerankResponse {
	if bifrostResp == nil {
		return nil
	}

	cohereResp := &CohereRerankResponse{
		ID:      bifrostResp.ID,
		Results: make([]CohereRerankResult, 0, len(bifrostResp.Results)),
	}

	for _, result := range bifrostResp.Results {
		cohereResult := CohereRerankResult{
			Index:          result.Index,
			RelevanceScore: result.RelevanceScore,
		}
		if result.Document != nil {
			cohereResult.Document = &CohereRerankDocument{
				Text:     rerankDocumentText(*result.Document),
				ID:       result.Document.ID,
				Metadata: result.Document.Meta,
			}
		}
		cohereResp.Results = append(cohereResp.Results, cohereResult)
	}

	if bifrostResp.Usage != nil {
		billedUnits := &CohereBilledUnits{}
		hasBilledUnits := false
		if bifrostResp.Usage.SearchUnits != nil {
			billedUnits.SearchUnits = bifrostResp.Usage.SearchUnits
			hasBilledUnits = true
		}
		if bifrostResp.Usage.PromptTokens > 0 || bifrostResp.Usage.CompletionTokens > 0 {
			billedUnits.InputTokens = new(bifrostResp.Usage.PromptTokens)
			billedUnits.OutputTokens = new(bifrostResp.Usage.CompletionTokens)
			hasBilledUnits = true
			cohereResp.Meta = &CohereRerankMeta{
				Tokens: &CohereTokenUsage{
					InputTokens:  new(bifrostResp.Usage.PromptTokens),
					OutputTokens: new(bifrostResp.Usage.CompletionTokens),
				},
			}
		}
		if hasBilledUnits {
			if cohereResp.Meta == nil {
				cohereResp.Meta = &CohereRerankMeta{}
			}
			cohereResp.Meta.BilledUnits = billedUnits
		}
	}

	return cohereResp
}

// rerankDocumentText renders a document as the prose Cohere ranks. A structured body is encoded
// so its content still reaches the ranker on a provider with no native JSON document support.
func rerankDocumentText(doc schemas.RerankDocument) string {
	if doc.Text != "" || len(doc.Data) == 0 {
		return doc.Text
	}
	encoded, err := sonic.Marshal(doc.Data)
	if err != nil {
		return doc.Text
	}
	return string(encoded)
}
