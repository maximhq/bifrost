package cohere

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// CohereRerankRequest represents a Cohere rerank API request.
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

// CohereRerankResult represents a single result from Cohere rerank.
type CohereRerankResult struct {
	Index          int             `json:"index"`
	RelevanceScore float64         `json:"relevance_score"`
	Document       json.RawMessage `json:"document,omitempty"`
}

// CohereRerankResponse represents a Cohere rerank API response.
type CohereRerankResponse struct {
	ID      string               `json:"id"`
	Results []CohereRerankResult `json:"results"`
	Meta    *CohereRerankMeta    `json:"meta,omitempty"`
}

// CohereRerankMeta represents metadata in Cohere rerank response.
type CohereRerankMeta struct {
	APIVersion  *CohereEmbeddingAPIVersion `json:"api_version,omitempty"`
	BilledUnits *CohereBilledUnits         `json:"billed_units,omitempty"`
	Tokens      *CohereTokenUsage          `json:"tokens,omitempty"`
}

func (response *CohereListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	if !unfiltered && allowedModels.IsEmpty() {
		return bifrostResponse
	}

	includedModels := make(map[string]bool)
	for _, model := range response.Models {
		if !unfiltered && allowedModels.IsRestricted() && !allowedModels.Contains(model.Name) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:               string(providerKey) + "/" + model.Name,
			Name:             schemas.Ptr(model.Name),
			ContextLength:    schemas.Ptr(int(model.ContextLength)),
			SupportedMethods: model.Endpoints,
		})
		includedModels[strings.ToLower(model.Name)] = true
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && allowedModels.IsRestricted() {
		for _, allowedModel := range allowedModels {
			if !includedModels[strings.ToLower(allowedModel)] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(providerKey) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}
