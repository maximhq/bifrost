package tei

import (
	"fmt"
	"sort"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToTEIRerankRequest converts a Bifrost rerank request to TEI format.
func ToTEIRerankRequest(bifrostReq *schemas.BifrostRerankRequest) *teiRerankRequest {
	if bifrostReq == nil {
		return nil
	}

	teiReq := &teiRerankRequest{
		Query: bifrostReq.Query,
		Texts: make([]string, len(bifrostReq.Documents)),
	}

	for i, doc := range bifrostReq.Documents {
		teiReq.Texts[i] = doc.Text
	}

	if bifrostReq.Params != nil {
		teiReq.TopN = bifrostReq.Params.TopN
		teiReq.MaxTokensPerDoc = bifrostReq.Params.MaxTokensPerDoc
		teiReq.Priority = bifrostReq.Params.Priority
		teiReq.ExtraParams = bifrostReq.Params.ExtraParams
		if bifrostReq.Params.ReturnDocuments != nil {
			teiReq.ReturnText = bifrostReq.Params.ReturnDocuments
		}
	}

	return teiReq
}

// ToBifrostRerankResponse converts a TEI rerank response payload to Bifrost format.
func ToBifrostRerankResponse(ranks []teiRank, documents []schemas.RerankDocument, returnDocuments bool, topN *int) (*schemas.BifrostRerankResponse, error) {
	seenIndices := make(map[int]struct{}, len(ranks))
	response := &schemas.BifrostRerankResponse{
		Results: make([]schemas.RerankResult, 0, len(ranks)),
	}

	for _, rank := range ranks {
		if rank.Index < 0 || rank.Index >= len(documents) {
			return nil, fmt.Errorf("invalid tei rerank response: result index %d out of range", rank.Index)
		}
		if _, exists := seenIndices[rank.Index]; exists {
			return nil, fmt.Errorf("invalid tei rerank response: duplicate index %d", rank.Index)
		}
		seenIndices[rank.Index] = struct{}{}

		result := schemas.RerankResult{
			Index:          rank.Index,
			RelevanceScore: rank.Score,
		}
		if returnDocuments {
			doc := documents[rank.Index]
			if rank.Text != nil {
				doc.Text = *rank.Text
			}
			result.Document = &doc
		}

		response.Results = append(response.Results, result)
	}

	sort.SliceStable(response.Results, func(i, j int) bool {
		if response.Results[i].RelevanceScore == response.Results[j].RelevanceScore {
			return response.Results[i].Index < response.Results[j].Index
		}
		return response.Results[i].RelevanceScore > response.Results[j].RelevanceScore
	})

	if topN != nil && *topN >= 0 && *topN < len(response.Results) {
		response.Results = response.Results[:*topN]
	}

	return response, nil
}

func teiProviderResponseError(message string, err error, requestBody []byte, responseBody []byte, sendBackRawRequest bool, sendBackRawResponse bool, ctx *schemas.BifrostContext) *schemas.BifrostError {
	return providerUtils.EnrichError(
		ctx,
		providerUtils.NewBifrostOperationError(message, err),
		requestBody,
		responseBody,
		sendBackRawRequest,
		sendBackRawResponse,
	)
}
