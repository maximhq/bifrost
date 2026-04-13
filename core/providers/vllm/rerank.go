package vllm

import (
	"fmt"
	"sort"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToVLLMRerankRequest converts a Bifrost rerank request to vLLM format.
func ToVLLMRerankRequest(bifrostReq *schemas.BifrostRerankRequest) *vLLMRerankRequest {
	if bifrostReq == nil {
		return nil
	}

	vllmReq := &vLLMRerankRequest{
		Model:     bifrostReq.Model,
		Query:     bifrostReq.Query,
		Documents: make([]string, len(bifrostReq.Documents)),
	}

	for i, doc := range bifrostReq.Documents {
		vllmReq.Documents[i] = doc.Text
	}

	if bifrostReq.Params != nil {
		vllmReq.TopN = bifrostReq.Params.TopN
		vllmReq.MaxTokensPerDoc = bifrostReq.Params.MaxTokensPerDoc
		vllmReq.Priority = bifrostReq.Params.Priority
		vllmReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return vllmReq
}

// ToBifrostRerankResponse converts a vLLM rerank response payload to Bifrost format.
func ToBifrostRerankResponse(payload interface{}, documents []schemas.RerankDocument, returnDocuments bool) (*schemas.BifrostRerankResponse, error) {
	vllmResp, err := decodeVLLMRerankResponse(payload)
	if err != nil {
		return nil, err
	}
	if vllmResp == nil {
		return nil, fmt.Errorf("vllm rerank response is nil")
	}

	response := &schemas.BifrostRerankResponse{
		ID:    vllmResp.ID,
		Model: vllmResp.Model,
	}
	if usage, ok := parseTypedVLLMUsage(vllmResp.Usage); ok {
		response.Usage = usage
	}

	seenIndices := make(map[int]struct{}, len(vllmResp.Results))
	response.Results = make([]schemas.RerankResult, 0, len(vllmResp.Results))

	for _, item := range vllmResp.Results {
		index := item.Index
		if index < 0 || index >= len(documents) {
			return nil, fmt.Errorf("invalid vllm rerank response: result index %d out of range", index)
		}
		if _, exists := seenIndices[index]; exists {
			return nil, fmt.Errorf("invalid vllm rerank response: duplicate index %d", index)
		}
		seenIndices[index] = struct{}{}

		relevanceScore, ok := resolveVLLMRelevanceScore(item)
		if !ok {
			return nil, fmt.Errorf("invalid vllm rerank response: relevance_score/score is required")
		}

		result := schemas.RerankResult{
			Index:          index,
			RelevanceScore: relevanceScore,
		}

		if returnDocuments {
			doc := documents[index]
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

	return response, nil
}

func decodeVLLMRerankResponse(payload interface{}) (*VLLMRerankResponse, error) {
	if payload == nil {
		return nil, nil
	}

	switch value := payload.(type) {
	case *VLLMRerankResponse:
		return value, nil
	case VLLMRerankResponse:
		resp := value
		return &resp, nil
	}

	body, err := sonic.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("invalid vllm rerank response: %w", err)
	}

	var response VLLMRerankResponse
	if err := sonic.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("invalid vllm rerank response: %w", err)
	}
	if response.Results == nil {
		return nil, fmt.Errorf("invalid vllm rerank response: missing results")
	}
	return &response, nil
}

func parseTypedVLLMUsage(usage *VLLMRerankUsage) (*schemas.BifrostLLMUsage, bool) {
	if usage == nil {
		return nil, false
	}

	promptTokens := 0
	if usage.PromptTokens != nil {
		promptTokens = *usage.PromptTokens
	} else if usage.InputTokens != nil {
		promptTokens = *usage.InputTokens
	}

	completionTokens := 0
	if usage.CompletionTokens != nil {
		completionTokens = *usage.CompletionTokens
	} else if usage.OutputTokens != nil {
		completionTokens = *usage.OutputTokens
	}

	totalTokens := promptTokens + completionTokens
	if usage.TotalTokens != nil {
		totalTokens = *usage.TotalTokens
	}
	if promptTokens == 0 && completionTokens == 0 && totalTokens == 0 {
		return nil, false
	}

	return &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}, true
}

func resolveVLLMRelevanceScore(result VLLMRerankResult) (float64, bool) {
	if result.RelevanceScore != nil {
		return *result.RelevanceScore, true
	}
	if result.Score != nil {
		return *result.Score, true
	}
	return 0, false
}
