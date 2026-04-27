package lib

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// ApplyVirtualKeyProviderFilter resolves the current virtual key and injects the
// allowed provider list into the request context for list-models fan-out.
func ApplyVirtualKeyProviderFilter(ctx *schemas.BifrostContext, store configstore.ConfigStore) error {
	if ctx == nil || store == nil {
		return nil
	}

	if existing, ok := ctx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider); ok && len(existing) > 0 {
		return nil
	}

	virtualKey, _ := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	virtualKey = strings.TrimSpace(virtualKey)
	if virtualKey == "" {
		return nil
	}

	vk, err := store.GetVirtualKeyByValue(ctx, virtualKey)
	if err != nil || vk == nil || !vk.IsActive {
		return err
	}
	if len(vk.ProviderConfigs) == 0 {
		return nil
	}

	availableProviders := make([]schemas.ModelProvider, 0, len(vk.ProviderConfigs))
	seen := make(map[schemas.ModelProvider]struct{}, len(vk.ProviderConfigs))
	for _, pc := range vk.ProviderConfigs {
		provider := schemas.ModelProvider(strings.TrimSpace(pc.Provider))
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		availableProviders = append(availableProviders, provider)
	}

	if len(availableProviders) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyAvailableProviders, availableProviders)
	}

	return nil
}
