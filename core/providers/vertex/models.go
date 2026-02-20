package vertex

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
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

// formatDeploymentName converts a deployment alias into a human-readable name.
// It splits the alias by "-" or "_", capitalizes each word, and joins them with spaces.
// Example: "gemini-pro" → "Gemini Pro", "claude_3_opus" → "Claude 3 Opus"
func formatDeploymentName(alias string) string {
	caser := cases.Title(language.English)

	// Try splitting by hyphen first, then underscore
	var parts []string
	if strings.Contains(alias, "-") {
		parts = strings.Split(alias, "-")
	} else if strings.Contains(alias, "_") {
		parts = strings.Split(alias, "_")
	} else {
		// No delimiter found, just capitalize the whole string
		return caser.String(strings.ToLower(alias))
	}

	// Capitalize each part
	for i, part := range parts {
		if part != "" {
			parts[i] = caser.String(strings.ToLower(part))
		}
	}

	return strings.Join(parts, " ")
}

// findDeploymentMatch finds a matching deployment value in the deployments map.
// Returns the deployment value and alias if found, empty strings otherwise.
func findDeploymentMatch(deployments map[string]string, customModelID string) (deploymentValue, alias string) {
	// Check exact match by deployment value
	for aliasKey, depValue := range deployments {
		if depValue == customModelID {
			return depValue, aliasKey
		}
	}
	// Check exact match by alias/key
	if deployment, ok := deployments[customModelID]; ok {
		return deployment, customModelID
	}
	return "", ""
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
func (response *VertexListModelsResponse) ToBifrostListModelsResponse(allowedModels []string, deployments map[string]string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	// Track which model IDs have been added to avoid duplicates
	addedModelIDs := make(map[string]bool)

	// First pass: Process all models from the Vertex AI API response (custom models)
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

			// Filter if model is not present in both lists (when both are non-empty)
			// Empty lists mean "allow all" for that dimension
			var deploymentValue, deploymentAlias string
			shouldFilter := false
			if len(allowedModels) > 0 && len(deployments) > 0 {
				// Both lists are present: model must be in allowedModels AND deployments
				// AND the deployment alias must also be in allowedModels
				deploymentValue, deploymentAlias = findDeploymentMatch(deployments, customModelID)
				inDeployments := deploymentAlias != ""

				// Check if deployment alias is also in allowedModels (direct string match)
				deploymentAliasInAllowedModels := false
				if deploymentAlias != "" {
					deploymentAliasInAllowedModels = slices.Contains(allowedModels, deploymentAlias)
				}

				// Filter if: model not in deployments OR deployment alias not in allowedModels
				shouldFilter = !inDeployments || !deploymentAliasInAllowedModels
			} else if len(allowedModels) > 0 {
				// Only allowedModels is present: filter if model is not in allowedModels
				shouldFilter = !slices.Contains(allowedModels, customModelID)
			} else if len(deployments) > 0 {
				// Only deployments is present: filter if model is not in deployments
				deploymentValue, deploymentAlias = findDeploymentMatch(deployments, customModelID)
				shouldFilter = deploymentValue == ""
			}
			// If both are empty, shouldFilter remains false (allow all)

			if shouldFilter {
				continue
			}

			modelID := customModelID

			modelEntry := schemas.Model{
				ID:          string(schemas.Vertex) + "/" + modelID,
				Name:        schemas.Ptr(model.DisplayName),
				Description: schemas.Ptr(model.Description),
				Created:     schemas.Ptr(model.VersionCreateTime.Unix()),
			}
			// Set deployment info if matched via deployments
			if deploymentValue != "" && deploymentAlias != "" {
				modelEntry.ID = string(schemas.Vertex) + "/" + deploymentAlias
				modelEntry.Deployment = schemas.Ptr(deploymentValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
			addedModelIDs[modelEntry.ID] = true
		}
	}

	// Second pass: Add non-custom models from deployments
	// Non-custom models are identified by having non-digit characters in their deployment values
	for alias, deploymentValue := range deployments {
		// Skip if deployment value contains only digits (custom model, already processed)
		if schemas.IsAllDigitsASCII(deploymentValue) {
			continue
		}

		// Check if this deployment alias is allowed
		if len(allowedModels) > 0 {
			// If allowedModels is non-empty, only include if alias is in the list
			if !slices.Contains(allowedModels, alias) {
				continue
			}
		}

		// Check if model already exists in the list
		modelID := string(schemas.Vertex) + "/" + alias
		if addedModelIDs[modelID] {
			continue
		}

		// Create model entry for non-custom model
		modelName := formatDeploymentName(alias)
		modelEntry := schemas.Model{
			ID:          modelID,
			Name:        schemas.Ptr(modelName),
			Description: nil, // No description available for non-custom models
			Created:     nil, // No creation time available for non-custom models
			Deployment:  schemas.Ptr(deploymentValue),
		}

		bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
		addedModelIDs[modelID] = true
	}

	// Third pass: Add non-custom models from allowedModels that aren't in deployments
	// This handles cases where a model is specified in allowedModels but not explicitly mapped in deployments
	if len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			// Skip if model is all digits (custom model ID)
			if schemas.IsAllDigitsASCII(allowedModel) {
				continue
			}

			// Skip if model is already in deployments (already processed in second pass)
			if _, existsInDeployments := deployments[allowedModel]; existsInDeployments {
				continue
			}

			// Check if model already exists in the list
			modelID := string(schemas.Vertex) + "/" + allowedModel
			if addedModelIDs[modelID] {
				continue
			}

			// Create model entry for allowed model
			// Use the model name itself as the deployment value
			modelName := formatDeploymentName(allowedModel)
			modelEntry := schemas.Model{
				ID:          modelID,
				Name:        schemas.Ptr(modelName),
				Description: nil, // No description available for models from allowedModels
				Created:     nil, // No creation time available for models from allowedModels
				Deployment:  schemas.Ptr(allowedModel),
			}

			bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
			addedModelIDs[modelID] = true
		}
	}

	bifrostResponse.NextPageToken = response.NextPageToken

	return bifrostResponse
}
