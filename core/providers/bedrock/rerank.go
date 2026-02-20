package bedrock

import (
	"fmt"
	"sort"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockRerankRequest converts a Bifrost rerank request into Bedrock Agent Runtime format.
func ToBedrockRerankRequest(bifrostReq *schemas.BifrostRerankRequest, modelARN string) (*BedrockRerankRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost rerank request is nil")
	}
	if strings.TrimSpace(modelARN) == "" {
		return nil, fmt.Errorf("bedrock rerank model ARN is empty")
	}
	if len(bifrostReq.Documents) == 0 {
		return nil, fmt.Errorf("documents are required for rerank request")
	}

	bedrockReq := &BedrockRerankRequest{
		Queries: []BedrockRerankQuery{
			{
				Type: bedrockRerankQueryTypeText,
				TextQuery: BedrockRerankTextRef{
					Text: bifrostReq.Query,
				},
			},
		},
		Sources: make([]BedrockRerankSource, len(bifrostReq.Documents)),
		RerankingConfiguration: BedrockRerankingConfiguration{
			Type: bedrockRerankConfigurationTypeBedrock,
			BedrockRerankingConfiguration: BedrockRerankingModelConfiguration{
				ModelConfiguration: BedrockRerankModelConfiguration{
					ModelARN: modelARN,
				},
			},
		},
	}

	for i, doc := range bifrostReq.Documents {
		bedrockReq.Sources[i] = BedrockRerankSource{
			Type: bedrockRerankSourceTypeInline,
			InlineDocumentSource: BedrockRerankInlineSource{
				Type: bedrockRerankInlineDocumentTypeText,
				TextDocument: BedrockRerankTextValue{
					Text: doc.Text,
				},
			},
		}
	}

	if bifrostReq.Params == nil {
		return bedrockReq, nil
	}

	if bifrostReq.Params.TopN != nil {
		topN := *bifrostReq.Params.TopN
		if topN < 1 {
			return nil, fmt.Errorf("top_n must be at least 1")
		}
		if topN > len(bifrostReq.Documents) {
			topN = len(bifrostReq.Documents)
		}
		bedrockReq.RerankingConfiguration.BedrockRerankingConfiguration.NumberOfResults = schemas.Ptr(topN)
	}

	additionalFields := make(map[string]interface{})
	if bifrostReq.Params.MaxTokensPerDoc != nil {
		additionalFields["max_tokens_per_doc"] = *bifrostReq.Params.MaxTokensPerDoc
	}
	if bifrostReq.Params.Priority != nil {
		additionalFields["priority"] = *bifrostReq.Params.Priority
	}
	for k, v := range bifrostReq.Params.ExtraParams {
		additionalFields[k] = v
	}
	if len(additionalFields) > 0 {
		bedrockReq.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.AdditionalModelRequestFields = additionalFields
	}

	return bedrockReq, nil
}

// ToBifrostRerankResponse converts a Bedrock rerank response into Bifrost format.
func (response *BedrockRerankResponse) ToBifrostRerankResponse(documents []schemas.RerankDocument, returnDocuments bool) *schemas.BifrostRerankResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostRerankResponse{
		Results: make([]schemas.RerankResult, 0, len(response.Results)),
	}

	for _, result := range response.Results {
		rerankResult := schemas.RerankResult{
			Index:          result.Index,
			RelevanceScore: result.RelevanceScore,
		}
		if result.Document != nil && result.Document.TextDocument != nil {
			rerankResult.Document = &schemas.RerankDocument{
				Text: result.Document.TextDocument.Text,
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

	return bifrostResponse
}

// ToBifrostRerankRequest converts a Bedrock Agent Runtime rerank request to Bifrost format.
func (req *BedrockRerankRequest) ToBifrostRerankRequest(ctx *schemas.BifrostContext) *schemas.BifrostRerankRequest {
	if req == nil {
		return nil
	}

	modelARN := req.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.ModelARN
	provider, model := schemas.ParseModelString(modelARN, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Bedrock))

	bifrostReq := &schemas.BifrostRerankRequest{
		Provider: provider,
		Model:    model,
		Params:   &schemas.RerankParameters{},
	}

	// Extract query from the first query entry
	if len(req.Queries) > 0 {
		bifrostReq.Query = req.Queries[0].TextQuery.Text
	}

	// Convert sources to documents
	for _, source := range req.Sources {
		bifrostReq.Documents = append(bifrostReq.Documents, schemas.RerankDocument{
			Text: source.InlineDocumentSource.TextDocument.Text,
		})
	}

	// Extract TopN from NumberOfResults
	if req.RerankingConfiguration.BedrockRerankingConfiguration.NumberOfResults != nil {
		bifrostReq.Params.TopN = req.RerankingConfiguration.BedrockRerankingConfiguration.NumberOfResults
	}

	// Pass AdditionalModelRequestFields as ExtraParams
	if fields := req.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.AdditionalModelRequestFields; len(fields) > 0 {
		bifrostReq.Params.ExtraParams = fields
	}

	return bifrostReq
}
