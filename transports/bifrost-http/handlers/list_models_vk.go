package handlers

import (
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	governanceplugin "github.com/maximhq/bifrost/plugins/governance"
	"github.com/valyala/fasthttp"
)

// applyListModelsVirtualKeyProviderFilter narrows provider fan-out for GET /v1/models
// when the request is made with a virtual key. Without this, ListAllModels asks every
// configured provider to list models and governance rejects providers outside the VK,
// creating noisy, expected errors in request logs.
func (h *CompletionHandler) applyListModelsVirtualKeyProviderFilter(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) bool {
	vkValue := governanceplugin.ParseVirtualKeyFromFastHTTPRequest(ctx)
	if vkValue == nil {
		return true
	}

	trimmedVKValue := strings.TrimSpace(*vkValue)
	if trimmedVKValue == "" {
		return true
	}

	if h.config == nil || h.config.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "database store unavailable")
		return false
	}

	vk, err := h.config.ConfigStore.GetVirtualKeyByValue(ctx, trimmedVKValue)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return true
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to resolve virtual key: %v", err))
		return false
	}
	if vk == nil || vk.IsActive == nil || !*vk.IsActive {
		return true
	}

	availableProviders := make([]schemas.ModelProvider, 0, len(vk.ProviderConfigs))
	for _, providerConfig := range vk.ProviderConfigs {
		provider := strings.TrimSpace(providerConfig.Provider)
		if provider == "" {
			continue
		}
		availableProviders = append(availableProviders, schemas.ModelProvider(provider))
	}

	bifrostCtx.SetValue(schemas.BifrostContextKeyAvailableProviders, availableProviders)
	return true
}
