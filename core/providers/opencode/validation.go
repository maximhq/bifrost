package opencode

import (
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func validateChatRoute(route resolvedRoute, request *schemas.BifrostChatRequest) *schemas.BifrostError {
	if request == nil {
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("chat request is nil"))
	}
	if request.Model == "" {
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("chat request model is empty"))
	}
	switch route.Adapter {
	case adapterOpenAIChat, adapterAnthropicMessages, adapterGeminiNative:
		return nil
	case adapterOpenAIResponses:
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("chat completion is not supported for responses-routed model %q", request.Model))
	default:
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("unsupported chat route adapter %q", route.Adapter))
	}
}

func validateResponsesRoute(route resolvedRoute, request *schemas.BifrostResponsesRequest) *schemas.BifrostError {
	if request == nil {
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("responses request is nil"))
	}
	if request.Model == "" {
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("responses request model is empty"))
	}
	switch route.Adapter {
	case adapterOpenAIResponses, adapterAnthropicMessages, adapterGeminiNative:
		return nil
	case adapterOpenAIChat:
		if len(request.Input) == 0 {
			return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("responses request input is empty"))
		}
		return nil
	default:
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("unsupported responses route adapter %q", route.Adapter))
	}
}
