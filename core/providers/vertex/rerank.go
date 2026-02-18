package vertex

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	vertexDefaultRankingConfigID   = "default_ranking_config"
	vertexMaxRerankRecordsPerQuery = 200
	vertexSyntheticRecordPrefix    = "idx:"
)

// VertexRankRequest represents the Discovery Engine rank API request.
type VertexRankRequest struct {
	Model                         *string            `json:"model,omitempty"`
	Query                         string             `json:"query"`
	Records                       []VertexRankRecord `json:"records"`
	TopN                          *int               `json:"topN,omitempty"`
	IgnoreRecordDetailsInResponse *bool              `json:"ignoreRecordDetailsInResponse,omitempty"`
	UserLabels                    map[string]string  `json:"userLabels,omitempty"`
}

// GetExtraParams implements providerUtils.RequestBodyWithExtraParams.
func (*VertexRankRequest) GetExtraParams() map[string]interface{} {
	return nil
}

// VertexRankRecord represents a record for ranking.
type VertexRankRecord struct {
	ID      string  `json:"id"`
	Title   *string `json:"title,omitempty"`
	Content *string `json:"content,omitempty"`
}

// VertexRankResponse represents the Discovery Engine rank API response.
type VertexRankResponse struct {
	Records []VertexRankedRecord `json:"records"`
}

// VertexRankedRecord represents a ranked record in response.
type VertexRankedRecord struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Title   *string `json:"title,omitempty"`
	Content *string `json:"content,omitempty"`
}

type vertexRerankOptions struct {
	RankingConfig                 string
	IgnoreRecordDetailsInResponse bool
	UserLabels                    map[string]string
}

func buildVertexRankingConfig(projectID, rankingConfigOverride string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("project ID is required for ranking config")
	}

	override := strings.TrimSpace(rankingConfigOverride)
	if override == "" {
		return fmt.Sprintf("projects/%s/locations/global/rankingConfigs/%s", projectID, vertexDefaultRankingConfigID), nil
	}

	override = strings.TrimSuffix(override, ":rank")
	if strings.HasPrefix(override, "projects/") {
		return override, nil
	}
	if strings.Contains(override, "/") {
		return "", fmt.Errorf("invalid ranking_config %q: must be resource name or config ID", rankingConfigOverride)
	}
	return fmt.Sprintf("projects/%s/locations/global/rankingConfigs/%s", projectID, override), nil
}

func getVertexRerankOptions(projectID string, params *schemas.RerankParameters) (*vertexRerankOptions, error) {
	options := &vertexRerankOptions{
		IgnoreRecordDetailsInResponse: true,
	}

	if params == nil || params.ExtraParams == nil {
		rankingConfig, err := buildVertexRankingConfig(projectID, "")
		if err != nil {
			return nil, err
		}
		options.RankingConfig = rankingConfig
		return options, nil
	}

	extraParams := params.ExtraParams

	rankingConfigOverride := ""
	if rawRankingConfig, exists := extraParams["ranking_config"]; exists {
		rankingConfig, ok := schemas.SafeExtractString(rawRankingConfig)
		if !ok {
			return nil, fmt.Errorf("invalid ranking_config: expected string")
		}
		rankingConfigOverride = rankingConfig
	}

	rankingConfig, err := buildVertexRankingConfig(projectID, rankingConfigOverride)
	if err != nil {
		return nil, err
	}
	options.RankingConfig = rankingConfig

	if rawIgnoreRecordDetails, exists := extraParams["ignore_record_details_in_response"]; exists {
		ignoreRecordDetailsInResponse, ok := schemas.SafeExtractBool(rawIgnoreRecordDetails)
		if !ok {
			return nil, fmt.Errorf("invalid ignore_record_details_in_response: expected bool")
		}
		options.IgnoreRecordDetailsInResponse = ignoreRecordDetailsInResponse
	}

	if rawUserLabels, exists := extraParams["user_labels"]; exists {
		userLabels, ok := schemas.SafeExtractStringMap(rawUserLabels)
		if !ok {
			return nil, fmt.Errorf("invalid user_labels: expected map[string]string")
		}
		options.UserLabels = userLabels
	}

	return options, nil
}

// ToVertexRankRequest converts a Bifrost rerank request to Discovery Engine rank API format.
func ToVertexRankRequest(bifrostReq *schemas.BifrostRerankRequest, modelDeployment string, options *vertexRerankOptions) (*VertexRankRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost rerank request is nil")
	}
	if options == nil {
		return nil, fmt.Errorf("vertex rerank options are nil")
	}
	if len(bifrostReq.Documents) == 0 {
		return nil, fmt.Errorf("documents are required for rerank request")
	}
	if len(bifrostReq.Documents) > vertexMaxRerankRecordsPerQuery {
		return nil, fmt.Errorf("vertex rerank supports up to %d records per request", vertexMaxRerankRecordsPerQuery)
	}

	rankRequest := &VertexRankRequest{
		Query:   bifrostReq.Query,
		Records: make([]VertexRankRecord, len(bifrostReq.Documents)),
	}

	for i, doc := range bifrostReq.Documents {
		recordID := fmt.Sprintf("%s%d", vertexSyntheticRecordPrefix, i)
		content := doc.Text
		record := VertexRankRecord{
			ID:      recordID,
			Content: &content,
		}

		if doc.Meta != nil {
			if rawTitle, exists := doc.Meta["title"]; exists {
				if title, ok := schemas.SafeExtractString(rawTitle); ok && strings.TrimSpace(title) != "" {
					record.Title = &title
				}
			}
		}

		rankRequest.Records[i] = record
	}

	if bifrostReq.Params != nil && bifrostReq.Params.TopN != nil {
		topN := *bifrostReq.Params.TopN
		if topN < 1 {
			return nil, fmt.Errorf("top_n must be at least 1")
		}
		if topN > len(bifrostReq.Documents) {
			topN = len(bifrostReq.Documents)
		}
		rankRequest.TopN = &topN
	}

	if trimmedModel := strings.TrimSpace(modelDeployment); trimmedModel != "" {
		rankRequest.Model = &trimmedModel
	}

	ignoreRecordDetailsInResponse := options.IgnoreRecordDetailsInResponse
	rankRequest.IgnoreRecordDetailsInResponse = &ignoreRecordDetailsInResponse

	if len(options.UserLabels) > 0 {
		rankRequest.UserLabels = options.UserLabels
	}

	return rankRequest, nil
}

func parseVertexSyntheticRecordIndex(recordID string, maxDocs int) (int, error) {
	if !strings.HasPrefix(recordID, vertexSyntheticRecordPrefix) {
		return 0, fmt.Errorf("invalid record id %q: expected prefix %q", recordID, vertexSyntheticRecordPrefix)
	}
	indexStr := strings.TrimPrefix(recordID, vertexSyntheticRecordPrefix)
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return 0, fmt.Errorf("invalid record id %q: %w", recordID, err)
	}
	if index < 0 || index >= maxDocs {
		return 0, fmt.Errorf("record id %q maps to out-of-range index %d", recordID, index)
	}
	return index, nil
}

// ToBifrostRerankResponse converts a Discovery Engine rank response to Bifrost format.
func (response *VertexRankResponse) ToBifrostRerankResponse(documents []schemas.RerankDocument, returnDocuments bool) (*schemas.BifrostRerankResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("vertex rerank response is nil")
	}

	results := make([]schemas.RerankResult, 0, len(response.Records))
	seenIndices := make(map[int]struct{}, len(response.Records))

	for _, record := range response.Records {
		index, err := parseVertexSyntheticRecordIndex(record.ID, len(documents))
		if err != nil {
			return nil, err
		}

		if _, seen := seenIndices[index]; seen {
			return nil, fmt.Errorf("duplicate record id mapping for index %d", index)
		}
		seenIndices[index] = struct{}{}

		result := schemas.RerankResult{
			Index:          index,
			RelevanceScore: record.Score,
		}

		if returnDocuments {
			doc := documents[index]
			result.Document = &doc
		}

		results = append(results, result)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].RelevanceScore == results[j].RelevanceScore {
			return results[i].Index < results[j].Index
		}
		return results[i].RelevanceScore > results[j].RelevanceScore
	})

	return &schemas.BifrostRerankResponse{
		Results: results,
	}, nil
}

func parseDiscoveryEngineErrorMessage(responseBody []byte) string {
	if len(responseBody) == 0 {
		return ""
	}

	var errorResponse map[string]interface{}
	if err := sonic.Unmarshal(responseBody, &errorResponse); err == nil {
		if rawError, exists := errorResponse["error"]; exists {
			if errorMap, ok := rawError.(map[string]interface{}); ok {
				if message, ok := schemas.SafeExtractString(errorMap["message"]); ok && strings.TrimSpace(message) != "" {
					return message
				}
			}
		}
	}

	rawString := strings.TrimSpace(string(responseBody))
	if rawString == "" {
		return ""
	}

	return rawString
}
