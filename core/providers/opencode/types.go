package opencode

import schemas "github.com/maximhq/bifrost/core/schemas"

type routeExecutionMetadata struct {
	Route    resolvedRoute
	Provider schemas.ModelProvider
	ModelID  string
}
