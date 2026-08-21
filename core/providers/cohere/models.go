package cohere

import (
	"strings"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// CohereRerankDocument is Cohere's overloaded document field. Requests accept a bare string or an
// object; only the object's "text" is ranked, every other key rides along. Responses always use
// the object form, so this always marshals as an object and only unmarshalling is lenient.
type CohereRerankDocument struct {
	Text     string                 `json:"text"`
	ID       *string                `json:"id,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// UnmarshalJSON accepts a bare string or an object, folding any key other than "text" and "id"
// into Metadata so passthrough fields survive a round trip.
func (d *CohereRerankDocument) UnmarshalJSON(data []byte) error {
	var text string
	if err := sonic.Unmarshal(data, &text); err == nil {
		*d = CohereRerankDocument{Text: text}
		return nil
	}

	var fields map[string]interface{}
	if err := sonic.Unmarshal(data, &fields); err != nil {
		return err
	}

	*d = CohereRerankDocument{}
	if text, ok := fields["text"].(string); ok {
		d.Text = text
	}
	if id, ok := fields["id"].(string); ok {
		d.ID = &id
	}
	metadata := make(map[string]interface{})
	if nested, ok := fields["metadata"].(map[string]interface{}); ok {
		for k, v := range nested {
			metadata[k] = v
		}
	}
	for k, v := range fields {
		if k != "text" && k != "id" && k != "metadata" {
			metadata[k] = v
		}
	}
	if len(metadata) > 0 {
		d.Metadata = metadata
	}
	return nil
}

// CohereRerankRequest represents a Cohere rerank API request.
type CohereRerankRequest struct {
	Model           string                 `json:"model"`
	Query           string                 `json:"query"`
	Documents       []CohereRerankDocument `json:"documents"`
	TopN            *int                   `json:"top_n,omitempty"`
	MaxTokensPerDoc *int                   `json:"max_tokens_per_doc,omitempty"`
	Priority        *int                   `json:"priority,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns extra parameters for the rerank request.
func (r *CohereRerankRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// CohereRerankResult represents a single result from Cohere rerank.
type CohereRerankResult struct {
	Index          int                   `json:"index"`
	RelevanceScore float64               `json:"relevance_score"`
	Document       *CohereRerankDocument `json:"document,omitempty"`
}

// CohereRerankResponse represents a Cohere rerank API response.
type CohereRerankResponse struct {
	ID      string               `json:"id,omitempty"`
	Results []CohereRerankResult `json:"results"`
	Meta    *CohereRerankMeta    `json:"meta,omitempty"`
}

// CohereRerankMeta represents metadata in Cohere rerank response.
type CohereRerankMeta struct {
	APIVersion   *CohereEmbeddingAPIVersion `json:"api_version,omitempty"`
	BilledUnits  *CohereBilledUnits         `json:"billed_units,omitempty"`
	Tokens       *CohereTokenUsage          `json:"tokens,omitempty"`
	CachedTokens *int                       `json:"cached_tokens,omitempty"`
	Warnings     []string                   `json:"warnings,omitempty"`
}

func (response *CohereListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
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
		for _, result := range pipeline.FilterModel(model.Name) {
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
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}
