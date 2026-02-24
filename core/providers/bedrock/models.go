package bedrock

import (
	"slices"
	"strings"

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

// findMatchingAllowedModel finds a matching item in a slice, considering both
// exact match and match with/without region prefixes (e.g., "global.", "us.", "eu."),
// and also checks base model matches (ignoring version suffixes).
// Returns the matched item from the slice if found, empty string otherwise.
// If matched via base model, returns the item from slice (not the value parameter).
func findMatchingAllowedModel(slice []string, value string) string {
	// First check exact matches
	if slices.Contains(slice, value) {
		return value
	}

	// Check with region prefix added/removed
	valuePrefix := extractPrefix(value)
	if valuePrefix != "" {
		// value has a prefix, check if slice contains version without prefix
		withoutPrefix := removePrefix(value)
		if slices.Contains(slice, withoutPrefix) {
			return withoutPrefix
		}
	}

	// Check if any item in slice has a prefix that matches value without prefix
	for _, item := range slice {
		itemPrefix := extractPrefix(item)
		if itemPrefix != "" {
			// item has prefix, check if value matches without the prefix
			itemWithoutPrefix := removePrefix(item)
			if itemWithoutPrefix == value {
				return item
			}
		}
	}

	// Additional layer: check base model matches (ignoring version suffixes)
	// This handles cases where model versions differ but base model is the same
	// Normalize value by removing any region prefix for base model comparison
	valueNormalized := removePrefix(value)

	for _, item := range slice {
		// Normalize item by removing any region prefix for base model comparison
		itemNormalized := removePrefix(item)

		// Check base model match with normalized values (prefix removed from both)
		// Return the item from slice (not value) to use the actual name from allowedModels
		if schemas.SameBaseModel(itemNormalized, valueNormalized) {
			return item
		}
	}
	return ""
}

// findDeploymentMatch finds a matching deployment value in the deployments map,
// considering both exact match and match with/without region prefixes (e.g., "global.", "us.", "eu."),
// and also checks base model matches (ignoring version suffixes).
// The modelID from the API response should match a deployment value (not the alias/key).
// Returns the deployment value and alias if found, empty strings otherwise.
func findDeploymentMatch(deployments map[string]string, modelID string) (deploymentValue, alias string) {
	// Check if any deployment value matches the modelID (with or without prefix)
	for aliasKey, deploymentValue := range deployments {
		// Exact match
		if deploymentValue == modelID || aliasKey == modelID {
			return deploymentValue, aliasKey
		}

		// Check prefix variations
		deploymentPrefix := extractPrefix(deploymentValue)
		modelIDPrefix := extractPrefix(modelID)
		aliasKeyPrefix := extractPrefix(aliasKey)

		// Case 1: deploymentValue or aliasKey has prefix, modelID doesn't
		if (deploymentPrefix != "" && modelIDPrefix == "") || (aliasKeyPrefix != "" && modelIDPrefix == "") {
			if removePrefix(deploymentValue) == modelID || removePrefix(aliasKey) == modelID {
				return deploymentValue, aliasKey
			}
		}

		// Case 2: modelID or aliasKey has prefix, deploymentValue doesn't
		if (modelIDPrefix != "" && deploymentPrefix == "") || (aliasKeyPrefix != "" && deploymentPrefix == "") {
			if removePrefix(modelID) == deploymentValue || removePrefix(modelID) == aliasKey {
				return deploymentValue, aliasKey
			}
		}

		// Case 3: Both have prefixes but different prefixes
		if (deploymentPrefix != "" && modelIDPrefix != "" && deploymentPrefix != modelIDPrefix) || (aliasKeyPrefix != "" && modelIDPrefix != "" && aliasKeyPrefix != modelIDPrefix) {
			if removePrefix(deploymentValue) == removePrefix(modelID) || removePrefix(aliasKey) == removePrefix(modelID) {
				return deploymentValue, aliasKey
			}
		}

		// Additional layer: check base model matches (ignoring version suffixes)
		// This handles cases where model versions differ but base model is the same
		// Normalize both values by removing any region prefix for base model comparison
		deploymentNormalized := removePrefix(deploymentValue)
		modelIDNormalized := removePrefix(modelID)

		// Check base model match with normalized values (prefix removed from both)
		if schemas.SameBaseModel(deploymentNormalized, modelIDNormalized) {
			return deploymentValue, aliasKey
		}
	}
	return "", ""
}

func (response *BedrockListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string, deployments map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.ModelSummaries)),
	}

	deploymentValues := make([]string, 0, len(deployments))
	for _, deployment := range deployments {
		deploymentValues = append(deploymentValues, deployment)
	}

	includedModels := make(map[string]bool)
	for _, model := range response.ModelSummaries {
		modelID := model.ModelID
		matchedAllowedModel := ""
		deploymentValue := ""
		deploymentAlias := ""

		// Filter if model is not present in both lists (when both are non-empty)
		// Empty lists mean "allow all" for that dimension
		// Check considering global prefix variations
		shouldFilter := false
		if !unfiltered && len(allowedModels) > 0 && len(deploymentValues) > 0 {
			// Both lists are present: model must be in allowedModels AND deployments
			// AND the deployment alias must also be in allowedModels
			matchedAllowedModel = findMatchingAllowedModel(allowedModels, model.ModelID)
			deploymentValue, deploymentAlias = findDeploymentMatch(deployments, model.ModelID)
			inDeployments := deploymentAlias != ""

			// Check if deployment alias is also in allowedModels (direct string match)
			deploymentAliasInAllowedModels := false
			if deploymentAlias != "" {
				deploymentAliasInAllowedModels = slices.Contains(allowedModels, deploymentAlias)
			}

			// Filter if: model not in deployments OR deployment alias not in allowedModels
			shouldFilter = !inDeployments || !deploymentAliasInAllowedModels
		} else if !unfiltered && len(allowedModels) > 0 {
			// Only allowedModels is present: filter if model is not in allowedModels
			matchedAllowedModel = findMatchingAllowedModel(allowedModels, model.ModelID)
			shouldFilter = matchedAllowedModel == ""
		} else if !unfiltered && len(deploymentValues) > 0 {
			// Only deployments is present: filter if model is not in deployments
			deploymentValue, deploymentAlias = findDeploymentMatch(deployments, model.ModelID)
			shouldFilter = deploymentValue == ""
		}
		// If both are empty, shouldFilter remains false (allow all)

		if shouldFilter {
			continue
		}

		// Use the matched name from allowedModels or deployments (like Anthropic)
		// Priority: deployment value > matched allowedModel > original model.ModelID
		if deploymentValue != "" {
			modelID = deploymentValue
		} else if matchedAllowedModel != "" {
			modelID = matchedAllowedModel
		}

		modelEntry := schemas.Model{
			ID:      string(providerKey) + "/" + modelID,
			Name:    schemas.Ptr(model.ModelName),
			OwnedBy: schemas.Ptr(model.ProviderName),
			Architecture: &schemas.Architecture{
				InputModalities:  model.InputModalities,
				OutputModalities: model.OutputModalities,
			},
		}
		// Set deployment info if matched via deployments
		if deploymentValue != "" && deploymentAlias != "" {
			modelEntry.ID = string(providerKey) + "/" + deploymentAlias
			// Use the actual deployment value (which might have global prefix)
			modelEntry.Deployment = schemas.Ptr(deploymentValue)
			includedModels[deploymentAlias] = true
		} else {
			includedModels[modelID] = true
		}
		bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			if !includedModels[allowedModel] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(providerKey) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}
