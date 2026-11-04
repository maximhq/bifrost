package machgen

import (
	"slices"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// machgenTaskOutputModality maps a MachGen task type to what it produces. UPSCALE is absent
// because it outputs whichever media it is given.
var machgenTaskOutputModality = map[string]string{
	"T2V": "video", "I2V": "video", "R2V": "video", "F2F": "video", "SHARPEN": "video",
	"T2I": "image", "I2I": "image",
	"T2S": "audio", "T2D": "audio", "T2SFX": "audio", "T2M": "audio",
}

// ToBifrostListModelsResponse converts MachGen's model catalog to Bifrost's list models format.
// Task types are reported as supported methods, so callers can tell T2V from I2V-capable models.
func ToBifrostListModelsResponse(
	models []MachGenModel,
	providerKey schemas.ModelProvider,
	allowedModels schemas.WhiteList,
	blacklistedModels schemas.BlackList,
	aliases schemas.KeyAliases,
	unfiltered bool,
) *schemas.BifrostListModelsResponse {
	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(models)),
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

	for _, model := range models {
		if model.Model == "" {
			continue
		}
		taskTypes := make([]string, 0, len(model.Surfaces))
		var outputs []string
		for _, surface := range model.Surfaces {
			taskTypes = append(taskTypes, surface.TaskType)
			if modality, ok := machgenTaskOutputModality[surface.TaskType]; ok && !slices.Contains(outputs, modality) {
				outputs = append(outputs, modality)
			}
		}
		slices.Sort(outputs)

		for _, result := range pipeline.FilterModel(model.Model) {
			bifrostModel := schemas.Model{
				ID:               string(providerKey) + "/" + result.ResolvedID,
				OwnedBy:          schemas.Ptr("machgen"),
				SupportedMethods: taskTypes,
			}
			if len(outputs) > 0 {
				bifrostModel.Architecture = &schemas.Architecture{OutputModalities: outputs}
			}
			if result.AliasValue != "" {
				bifrostModel.Alias = &result.AliasValue
			}
			bifrostResponse.Data = append(bifrostResponse.Data, bifrostModel)
		}
	}

	return bifrostResponse
}
