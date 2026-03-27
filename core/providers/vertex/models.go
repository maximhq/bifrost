package vertex

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
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

const (
	vertexDefaultRankingConfigID   = "default_ranking_config"
	vertexMaxRerankRecordsPerQuery = 200
	vertexSyntheticRecordPrefix    = "idx:"
)

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

// ToBifrostListModelsResponse converts a Vertex AI list models response to Bifrost's format.
// It processes both custom models (from the API response) and non-custom models (from deployments and allowedModels).
//
// Custom models are those with digit-only deployment values, extracted from the API response.
// Non-custom models are those with non-digit characters in their deployment values or model names.
//
// The function performs three passes:
// 1. First pass: Process all models from the Vertex AI API response (custom models)
// 2. Second pass: Add non-custom models from deployments that aren't already in the list
// 3. Third pass: Add non-custom models from allowedModels that aren't in deployments or already added
//
// Filtering logic:
// - If allowedModels is empty, all models are allowed
// - If allowedModels is non-empty, only models/deployments with keys in allowedModels are included
// - Deployments map is used to match model IDs to aliases and filter accordingly
func (response *VertexListModelsResponse) ToBifrostListModelsResponse(allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
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
		ProviderKey:       schemas.Vertex,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	// Process all models from the Vertex AI API response (custom deployed models).
	// The model ID is extracted from the endpoint URL last segment.
	for _, model := range response.Models {
		if len(model.DeployedModels) == 0 {
			continue
		}
		for _, deployedModel := range model.DeployedModels {
			endpoint := strings.TrimSuffix(deployedModel.Endpoint, "/")
			parts := strings.Split(endpoint, "/")
			if len(parts) == 0 {
				continue
			}
			customModelID := parts[len(parts)-1]
			if customModelID == "" {
				continue
			}

			for _, result := range pipeline.FilterModel(customModelID) {
				resolvedKey := strings.ToLower(result.ResolvedID)
				if included[resolvedKey] {
					continue
				}
				modelEntry := schemas.Model{
					ID:          string(schemas.Vertex) + "/" + result.ResolvedID,
					Name:        schemas.Ptr(model.DisplayName),
					Description: schemas.Ptr(model.Description),
					Created:     schemas.Ptr(model.VersionCreateTime.Unix()),
				}
				if result.AliasValue != "" {
					modelEntry.Alias = schemas.Ptr(result.AliasValue)
				}
				bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
				included[resolvedKey] = true
			}
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	bifrostResponse.NextPageToken = response.NextPageToken

	return bifrostResponse
}

// ToBifrostListModelsResponse converts a Vertex AI publisher models response to Bifrost's format.
// This is for foundation models from the Model Garden (publishers.models.list endpoint).
func (response *VertexListPublisherModelsResponse) ToBifrostListModelsResponse(allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.PublisherModels)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Vertex,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.PublisherModels {
		// Extract model ID from name (format: "publishers/google/models/gemini-1.5-pro")
		modelID := extractModelIDFromName(model.Name)
		if modelID == "" {
			continue
		}

		for _, result := range pipeline.FilterModel(modelID) {
			// Extract display name from supported actions if available
			displayName := result.ResolvedID
			if model.SupportedActions != nil && model.SupportedActions.Deploy != nil && model.SupportedActions.Deploy.ModelDisplayName != "" {
				displayName = model.SupportedActions.Deploy.ModelDisplayName
			}
			modelEntry := schemas.Model{
				ID:   string(schemas.Vertex) + "/" + result.ResolvedID,
				Name: schemas.Ptr(displayName),
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

	bifrostResponse.NextPageToken = response.NextPageToken

	return bifrostResponse
}
