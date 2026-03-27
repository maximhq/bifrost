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

// regionPrefixes is a list of region prefixes used in Bedrock deployments
// Based on AWS region naming patterns and Bedrock deployment configurations
var regionPrefixes = []string{
	"us.",     // US regions (us-east-1, us-west-2, etc.)
	"eu.",     // Europe regions (eu-west-1, eu-central-1, etc.)
	"ap.",     // Asia Pacific regions (ap-southeast-1, ap-northeast-1, etc.)
	"ca.",     // Canada regions (ca-central-1, etc.)
	"sa.",     // South America regions (sa-east-1, etc.)
	"af.",     // Africa regions (af-south-1, etc.)
	"global.", // Global deployment prefix
}

// extractPrefix extracts the region prefix ending with '.' from a string
// Only recognizes common region prefixes like "us.", "global.", "eu.", etc.
// Returns the prefix (including the dot) if found, empty string otherwise
func extractPrefix(s string) string {
	for _, prefix := range regionPrefixes {
		if strings.HasPrefix(s, prefix) {
			return prefix
		}
	}
	return ""
}

// removePrefix removes any region prefix ending with '.' from a string
// Only removes common region prefixes like "us.", "global.", "eu.", etc.
// Returns the string without the prefix
func removePrefix(s string) string {
	for _, prefix := range regionPrefixes {
		if strings.HasPrefix(s, prefix) {
			return s[len(prefix):]
		}
	}
	return s
}

// bedrockMatchFns returns DefaultMatchFns extended with Bedrock-specific region prefix matching.
//
// AWS Bedrock model IDs may include region prefixes (e.g. "us.", "eu.", "global.").
// Users often configure allowedModels / aliases WITHOUT the prefix, but the API returns
// model IDs WITH a prefix. This extra MatchFn normalises both sides before comparing,
// ensuring e.g. "anthropic.claude-3-5-sonnet" matches "us.anthropic.claude-3-5-sonnet".
//
// Examples handled by the extra fn:
//
//	"us.anthropic.claude-3" ≈ "anthropic.claude-3"       (strip prefix from API response)
//	"anthropic.claude-3"    ≈ "us.anthropic.claude-3"    (strip prefix from alias value)
//	"eu.claude-3"           ≈ "us.claude-3"              (different prefixes, same base)
func bedrockMatchFns() []providerUtils.MatchFn {
	return append(providerUtils.DefaultMatchFns(),
		func(a, b string) bool {
			aNorm := removePrefix(a)
			bNorm := removePrefix(b)
			return strings.EqualFold(aNorm, bNorm) || schemas.SameBaseModel(aNorm, bNorm)
		},
	)
}

func (response *BedrockListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.ModelSummaries)),
	}

	// Use Bedrock-specific match functions that handle AWS region prefixes
	// (e.g. "us.anthropic.claude-3" ≈ "anthropic.claude-3")
	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          bedrockMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.ModelSummaries {
		result := pipeline.FilterModel(model.ModelID)
		if !result.Include {
			continue
		}
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

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}
