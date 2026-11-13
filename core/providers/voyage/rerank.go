package voyage

import (
	"fmt"
	"sort"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// maxRerankDocuments is Voyage's per-request cap on documents.
const maxRerankDocuments = 1000

// ToVoyageRerankRequest converts a Bifrost rerank request to Voyage's /v1/rerank body.
//
// Voyage ranks plain strings, so every document needs text; a structured Data document is not
// flattened into a guess. return_documents is sent only when asked for: the response documents
// are rebuilt from the request by index, so the caller's IDs and metadata come back intact.
func ToVoyageRerankRequest(request *schemas.BifrostRerankRequest) (*VoyageRerankRequest, error) {
	if request == nil || strings.TrimSpace(request.Model) == "" {
		return nil, providerUtils.InvalidRequestErrorf("voyage rerank needs a model: there is no default model")
	}
	name, model, ok := lookupModel(request.Model)
	if !ok {
		return nil, providerUtils.InvalidRequestErrorf("%q is not a Voyage model this provider serves", request.Model)
	}
	if model.endpoint != endpointRerank {
		return nil, providerUtils.InvalidRequestErrorf("%q is a %s model, not a rerank model", name, model.endpoint)
	}
	if strings.TrimSpace(request.Query) == "" {
		return nil, providerUtils.InvalidRequestErrorf("voyage rerank needs a query")
	}
	if len(request.Documents) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("voyage rerank needs at least one document")
	}
	if len(request.Documents) > maxRerankDocuments {
		return nil, providerUtils.InvalidRequestErrorf("voyage rerank accepts at most %d documents, got %d", maxRerankDocuments, len(request.Documents))
	}

	documents := make([]string, len(request.Documents))
	for i, document := range request.Documents {
		if strings.TrimSpace(document.Text) == "" {
			return nil, providerUtils.InvalidRequestErrorf("document %d has no text; Voyage ranks text documents only", i)
		}
		documents[i] = document.Text
	}

	body := &VoyageRerankRequest{Model: name, Query: request.Query, Documents: documents}
	if params := request.Params; params != nil {
		if params.TopN != nil {
			if *params.TopN < 1 {
				return nil, providerUtils.InvalidRequestErrorf("top_n must be at least 1")
			}
			body.TopK = params.TopN
		}
		if params.ReturnDocuments != nil && *params.ReturnDocuments {
			body.ReturnDocuments = schemas.Ptr(true)
		}
		if raw, present := params.ExtraParams["truncation"]; present {
			truncation, ok := schemas.SafeExtractBool(raw)
			if !ok {
				return nil, providerUtils.InvalidRequestErrorf("truncation must be a boolean")
			}
			body.Truncation = &truncation
		}
		body.ExtraParams = params.ExtraParams
	}
	return body, nil
}

// ToBifrostRerankResponse maps a /v1/rerank reply to Bifrost's shape, highest score first.
func (r *VoyageRerankResponse) ToBifrostRerankResponse(request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, error) {
	tokens, err := totalTokens(r.Usage)
	if err != nil {
		return nil, err
	}
	returnDocuments := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments

	results := make([]schemas.RerankResult, 0, len(r.Data))
	for _, entry := range r.Data {
		if entry.RelevanceScore == nil {
			return nil, fmt.Errorf("voyage rerank result for document %d has no relevance_score", entry.Index)
		}
		result := schemas.RerankResult{Index: entry.Index, RelevanceScore: *entry.RelevanceScore}
		switch {
		case len(request.Documents) == 0:
			// Large payload mode: the request was streamed unparsed, so documents come from Voyage's reply.
			if entry.Document != nil {
				result.Document = &schemas.RerankDocument{Text: *entry.Document}
			}
		case entry.Index < 0 || entry.Index >= len(request.Documents):
			return nil, fmt.Errorf("voyage rerank returned index %d outside the %d documents sent", entry.Index, len(request.Documents))
		default:
			document := request.Documents[entry.Index]
			result.ID = document.ID
			if returnDocuments {
				result.Document = &document
			}
		}
		results = append(results, result)
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].RelevanceScore > results[j].RelevanceScore })

	return &schemas.BifrostRerankResponse{
		Results: results,
		Model:   responseModel(r.Model, request.Model),
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: tokens, TotalTokens: tokens},
	}, nil
}
