package bedrock

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// BedrockRerankRequest is the Bedrock Agent Runtime rerank request body.
type BedrockRerankRequest struct {
	Queries                []BedrockRerankQuery          `json:"queries"`
	Sources                []BedrockRerankSource         `json:"sources"`
	RerankingConfiguration BedrockRerankingConfiguration `json:"rerankingConfiguration"`
}

// GetExtraParams implements RequestBodyWithExtraParams.
func (*BedrockRerankRequest) GetExtraParams() map[string]interface{} {
	return nil
}

const (
	bedrockRerankQueryTypeText            = "TEXT"
	bedrockRerankSourceTypeInline         = "INLINE"
	bedrockRerankInlineDocumentTypeText   = "TEXT"
	bedrockRerankConfigurationTypeBedrock = "BEDROCK_RERANKING_MODEL"
)

type BedrockRerankQuery struct {
	Type      string               `json:"type"`
	TextQuery BedrockRerankTextRef `json:"textQuery"`
}

type BedrockRerankSource struct {
	Type                 string                    `json:"type"`
	InlineDocumentSource BedrockRerankInlineSource `json:"inlineDocumentSource"`
}

type BedrockRerankInlineSource struct {
	Type         string                 `json:"type"`
	TextDocument BedrockRerankTextValue `json:"textDocument"`
}

type BedrockRerankTextRef struct {
	Text string `json:"text"`
}

type BedrockRerankTextValue struct {
	Text string `json:"text"`
}

type BedrockRerankingConfiguration struct {
	Type                          string                             `json:"type"`
	BedrockRerankingConfiguration BedrockRerankingModelConfiguration `json:"bedrockRerankingConfiguration"`
}

type BedrockRerankingModelConfiguration struct {
	ModelConfiguration BedrockRerankModelConfiguration `json:"modelConfiguration"`
	NumberOfResults    *int                            `json:"numberOfResults,omitempty"`
}

type BedrockRerankModelConfiguration struct {
	ModelARN                     string                 `json:"modelArn"`
	AdditionalModelRequestFields map[string]interface{} `json:"additionalModelRequestFields,omitempty"`
}

// BedrockRerankResponse is the Bedrock Agent Runtime rerank response body.
type BedrockRerankResponse struct {
	Results   []BedrockRerankResult `json:"results"`
	NextToken *string               `json:"nextToken,omitempty"`
}

type BedrockRerankResult struct {
	Index          int                            `json:"index"`
	RelevanceScore float64                        `json:"relevanceScore"`
	Document       *BedrockRerankResponseDocument `json:"document,omitempty"`
}

type BedrockRerankResponseDocument struct {
	Type         string                  `json:"type,omitempty"`
	TextDocument *BedrockRerankTextValue `json:"textDocument,omitempty"`
}

func (response *BedrockListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.ModelSummaries)),
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

	for _, model := range response.ModelSummaries {
		for _, result := range pipeline.FilterModel(model.ModelID) {
			modelEntry := schemas.Model{
				ID:      string(providerKey) + "/" + result.ResolvedID,
				Name:    schemas.Ptr(model.ModelName),
				OwnedBy: schemas.Ptr(model.ProviderName),
				Architecture: &schemas.Architecture{
					InputModalities:  model.InputModalities,
					OutputModalities: model.OutputModalities,
				},
			}
			if result.AliasValue != "" {
				modelEntry.Alias = schemas.Ptr(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}