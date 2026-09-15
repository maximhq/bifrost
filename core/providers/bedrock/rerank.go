package bedrock

import (
	"fmt"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// resolveBedrockRerankModelARN returns the foundation-model ARN to put in a rerank
// request body. Bedrock's Rerank API is the one Bedrock surface that names its model
// by ARN rather than by bare ID (its docs' own example is
// "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0"), so a caller
// passing the same bare ID that works on every other route had no way through.
// Bifrost already resolves a region for each Bedrock call, so it builds the ARN here
// instead of pushing that asymmetry onto callers.
//
// An identifier that is already an ARN is returned untouched: it may name an
// inference profile or a cross-account model that this function could not rebuild.
// An empty identifier stays empty so the caller reports the missing model itself.
func resolveBedrockRerankModelARN(ctx *schemas.BifrostContext, key schemas.Key, model string) string {
	model = strings.TrimSpace(model)
	if model == "" || strings.HasPrefix(model, "arn:") {
		return model
	}

	region := resolveBedrockRegion(ctx, key, model)
	// resolveBedrockRegion reads a "<region>/<model>" prefix but leaves it in place;
	// it must not survive into the ARN's resource segment.
	_, bareModel := parseBedrockRegionAndModel(model)

	return fmt.Sprintf("arn:%s:bedrock:%s::foundation-model/%s", awsPartitionForRegion(region), region, bareModel)
}

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
		inlineSource := BedrockRerankInlineSource{
			Type:         bedrockRerankInlineDocumentTypeText,
			TextDocument: &BedrockRerankTextValue{Text: doc.Text},
		}
		// Bedrock ranks structured bodies natively, so a JSON document goes over as-is.
		if doc.Text == "" && len(doc.Data) > 0 {
			inlineSource = BedrockRerankInlineSource{
				Type:         bedrockRerankInlineDocumentTypeJSON,
				JSONDocument: doc.Data,
			}
		}
		bedrockReq.Sources[i] = BedrockRerankSource{
			Type:                 bedrockRerankSourceTypeInline,
			InlineDocumentSource: inlineSource,
		}
	}

	if bifrostReq.Params == nil {
		return bedrockReq, nil
	}

	bedrockReq.NextToken = bifrostReq.Params.NextToken

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

	// Bedrock validates this map against a per-model allowlist and rejects the whole request on
	// an unknown key: COHERE_RERANK accepts only max_tokens_per_doc. Priority has no Bedrock
	// equivalent, so it is dropped rather than forwarded - sending it 400s a request that
	// succeeds on Cohere, which would break fallback chains. ExtraParams stays a passthrough:
	// the caller opted into those keys explicitly.
	additionalFields := make(map[string]interface{})
	if bifrostReq.Params.MaxTokensPerDoc != nil {
		additionalFields["max_tokens_per_doc"] = *bifrostReq.Params.MaxTokensPerDoc
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
		Results:   make([]schemas.RerankResult, 0, len(response.Results)),
		NextToken: response.NextToken,
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
			if result.Document.TextDocument != nil {
				rerankResult.Document = &schemas.RerankDocument{Text: result.Document.TextDocument.Text}
			} else if len(result.Document.JSONDocument) > 0 {
				rerankResult.Document = &schemas.RerankDocument{Data: result.Document.JSONDocument}
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

// ToBedrockRerankResponse converts a Bifrost rerank response into Bedrock Agent Runtime format.
// The live Rerank API returns only index and score per result, so a document is echoed back only
// when the canonical response actually carries one.
func ToBedrockRerankResponse(bifrostResp *schemas.BifrostRerankResponse) *BedrockRerankResponse {
	if bifrostResp == nil {
		return nil
	}

	bedrockResp := &BedrockRerankResponse{
		Results:   make([]BedrockRerankResult, 0, len(bifrostResp.Results)),
		NextToken: bifrostResp.NextToken,
	}

	for _, result := range bifrostResp.Results {
		bedrockResult := BedrockRerankResult{
			Index:          result.Index,
			RelevanceScore: result.RelevanceScore,
		}
		if result.Document != nil {
			switch {
			case len(result.Document.Data) > 0:
				bedrockResult.Document = &BedrockRerankResponseDocument{
					Type:         bedrockRerankInlineDocumentTypeJSON,
					JSONDocument: result.Document.Data,
				}
			default:
				bedrockResult.Document = &BedrockRerankResponseDocument{
					Type:         bedrockRerankInlineDocumentTypeText,
					TextDocument: &BedrockRerankTextValue{Text: result.Document.Text},
				}
			}
		}

		bedrockResp.Results = append(bedrockResp.Results, bedrockResult)
	}

	return bedrockResp
}

// ToBifrostRerankRequest converts a Bedrock Agent Runtime rerank request to Bifrost format.
func (req *BedrockRerankRequest) ToBifrostRerankRequest(ctx *schemas.BifrostContext) *schemas.BifrostRerankRequest {
	if req == nil {
		return nil
	}

	modelARN := req.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.ModelARN
	provider, model := schemas.ParseModelString(modelARN, "")

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
		doc := schemas.RerankDocument{}
		if source.InlineDocumentSource.TextDocument != nil {
			doc.Text = source.InlineDocumentSource.TextDocument.Text
		}
		if len(source.InlineDocumentSource.JSONDocument) > 0 {
			doc.Data = source.InlineDocumentSource.JSONDocument
		}
		bifrostReq.Documents = append(bifrostReq.Documents, doc)
	}

	bifrostReq.Params.NextToken = req.NextToken

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
