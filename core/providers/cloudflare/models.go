package cloudflare

import (
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostListModelsResponse converts Cloudflare's native model catalog to Bifrost models.
func (response *CloudflareListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Result)),
	}
	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)
	for _, model := range response.Result {
		if strings.TrimSpace(model.Name) == "" {
			continue
		}
		for _, result := range pipeline.FilterModel(model.Name) {
			entry := schemas.Model{
				ID:            string(providerKey) + "/" + result.ResolvedID,
				Name:          schemas.Ptr(model.Name),
				Description:   schemas.Ptr(model.Description),
				ContextLength: model.contextLength(),
				OwnedBy:       cloudflareModelOwner(model.Name),
				ProviderExtra: marshalCloudflareModel(model),
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data, pipeline.BackfillModels(included)...)
	return bifrostResponse
}

func (model CloudflareModel) contextLength() *int {
	for _, property := range model.Properties {
		if property.PropertyID != "context_window" {
			continue
		}

		var numericValue int
		if err := sonic.Unmarshal(property.Value, &numericValue); err == nil && numericValue > 0 {
			return schemas.Ptr(numericValue)
		}

		var stringValue string
		if err := sonic.Unmarshal(property.Value, &stringValue); err == nil {
			if parsed, err := strconv.Atoi(stringValue); err == nil && parsed > 0 {
				return schemas.Ptr(parsed)
			}
		}
	}
	return nil
}

func cloudflareModelOwner(modelName string) *string {
	const prefix = "@cf/"
	if !strings.HasPrefix(modelName, prefix) {
		return nil
	}
	owner, _, found := strings.Cut(strings.TrimPrefix(modelName, prefix), "/")
	if !found || owner == "" {
		return nil
	}
	return schemas.Ptr(owner)
}

func marshalCloudflareModel(model CloudflareModel) []byte {
	raw, err := sonic.Marshal(model)
	if err != nil {
		return nil
	}
	return raw
}
