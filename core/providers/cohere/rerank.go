package cohere

import (
	"sort"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// CohereRerankRequest represents a Cohere rerank API request
type CohereRerankRequest struct {
	Model           string                 `json:"model"`
	Query           string                 `json:"query"`
	Documents       []string               `json:"documents"`
	TopN            *int                   `json:"top_n,omitempty"`
	MaxTokensPerDoc *int                   `json:"max_tokens_per_doc,omitempty"`
	Priority        *int                   `json:"priority,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns extra parameters for the rerank request.
func (r *CohereRerankRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// CohereRerankResult represents a single result from Cohere rerank
type CohereRerankResult struct {
	Index          int                    `json:"index"`
	RelevanceScore float64                `json:"relevance_score"`
	Document       map[string]interface{} `json:"document,omitempty"`
}

// CohereRerankResponse represents a Cohere rerank API response
type CohereRerankResponse struct {
	ID      string               `json:"id"`
	Results []CohereRerankResult `json:"results"`
	Meta    *CohereRerankMeta    `json:"meta,omitempty"`
}

// CohereRerankMeta represents metadata in Cohere rerank response
type CohereRerankMeta struct {
	APIVersion  *CohereEmbeddingAPIVersion `json:"api_version,omitempty"`
	BilledUnits *CohereBilledUnits         `json:"billed_units,omitempty"`
	Tokens      *CohereTokenUsage          `json:"tokens,omitempty"`
}

// ToCohereRerankRequest converts a Bifrost rerank request to Cohere format
func ToCohereRerankRequest(bifrostReq *schemas.BifrostRerankRequest) *CohereRerankRequest {
	if bifrostReq == nil {
		return nil
	}

	cohereReq := &CohereRerankRequest{
		Model: bifrostReq.Model,
		Query: bifrostReq.Query,
	}

	// Cohere v2 expects documents as a list of strings.
	documents := make([]string, len(bifrostReq.Documents))
	for i, doc := range bifrostReq.Documents {
		documents[i] = formatCohereRerankDocument(doc)
	}
	cohereReq.Documents = documents

	if bifrostReq.Params != nil {
		cohereReq.TopN = bifrostReq.Params.TopN
		cohereReq.MaxTokensPerDoc = bifrostReq.Params.MaxTokensPerDoc
		cohereReq.Priority = bifrostReq.Params.Priority
		cohereReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return cohereReq
}

// ToBifrostRerankRequest converts a Cohere rerank request to Bifrost format
func (req *CohereRerankRequest) ToBifrostRerankRequest(ctx *schemas.BifrostContext) *schemas.BifrostRerankRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Cohere))

	bifrostReq := &schemas.BifrostRerankRequest{
		Provider: provider,
		Model:    model,
		Query:    req.Query,
		Params:   &schemas.RerankParameters{},
	}

	// Convert documents
	for _, doc := range req.Documents {
		bifrostReq.Documents = append(bifrostReq.Documents, schemas.RerankDocument{
			Text: doc,
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
	if req.ExtraParams != nil {
		bifrostReq.Params.ExtraParams = req.ExtraParams
	}

	return bifrostReq
}

// ToBifrostRerankResponse converts a Cohere rerank response to Bifrost format
func (response *CohereRerankResponse) ToBifrostRerankResponse() *schemas.BifrostRerankResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostRerankResponse{
		ID: response.ID,
	}

	// Convert results
	for _, result := range response.Results {
		rerankResult := schemas.RerankResult{
			Index:          result.Index,
			RelevanceScore: result.RelevanceScore,
		}

		// Convert document if present
		if result.Document != nil {
			doc := &schemas.RerankDocument{}
			if text, ok := result.Document["text"].(string); ok {
				doc.Text = text
			}
			if id, ok := result.Document["id"].(string); ok {
				doc.ID = &id
			}
			// Collect remaining fields as meta
			meta := make(map[string]interface{})
			for k, v := range result.Document {
				if k != "text" && k != "id" {
					meta[k] = v
				}
			}
			if len(meta) > 0 {
				doc.Meta = meta
			}
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
		if hasTokenUsage {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			}
		}
	}

	return bifrostResponse
}

func formatCohereRerankDocument(doc schemas.RerankDocument) string {
	if doc.ID == nil && len(doc.Meta) == 0 {
		return doc.Text
	}

	// Keep metadata/id available by encoding a structured string document.
	documentPayload := map[string]interface{}{
		"text": doc.Text,
	}
	if doc.ID != nil {
		documentPayload["id"] = *doc.ID
	}
	if len(doc.Meta) > 0 {
		documentPayload["metadata"] = doc.Meta
	}

	encoded, err := sonic.Marshal(documentPayload)
	if err != nil {
		return doc.Text
	}
	return string(encoded)
}
