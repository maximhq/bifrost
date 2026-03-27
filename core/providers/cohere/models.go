package cohere

import (
	"encoding/json"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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

func (response *CohereListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.Models {
		// Cohere uses model.Name as the model identifier
		result := pipeline.FilterModel(model.Name)
		if !result.Include {
			continue
		}
		entry := schemas.Model{
			ID:               string(providerKey) + "/" + result.ResolvedID,
			Name:             schemas.Ptr(model.Name),
			ContextLength:    schemas.Ptr(int(model.ContextLength)),
			SupportedMethods: model.Endpoints,
		}
		if result.AliasValue != "" {
			entry.Alias = schemas.Ptr(result.AliasValue)
		}
		bifrostResponse.Data = append(bifrostResponse.Data, entry)
		included[strings.ToLower(result.ResolvedID)] = true
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}
