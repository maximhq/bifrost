package bifrost

import (
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// passthroughRouteAllowed reports whether a provider's passthrough policy forwards req.
// Host-backed routes (UpstreamURL) are not governed by the provider policy: they carry their
// own fixed route list and the caller's credential.
func passthroughRouteAllowed(policy *schemas.PassthroughPolicy, req *schemas.BifrostPassthroughRequest) bool {
	if policy == nil || req.UpstreamURL != "" {
		return true
	}
	for i := range policy.AllowedRoutes {
		if policy.AllowedRoutes[i].Matches(req.Method, req.APIVersion, req.Path) {
			return true
		}
	}
	return false
}

// newPassthroughRouteNotAllowedError names the route as the client called it, so the
// operator can copy it into the provider's allowed routes.
func newPassthroughRouteNotAllowedError(provider schemas.ModelProvider, requestType schemas.RequestType, req *schemas.BifrostPassthroughRequest) schemas.BifrostError {
	routePath := req.Path
	if req.APIVersion != "" {
		routePath = "/" + req.APIVersion + req.Path
	}
	return schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     new(http.StatusForbidden),
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("passthrough route %s %s is not allowed for provider %s; add it to the provider's passthrough allowed routes", req.Method, routePath, provider),
			Code:    new("passthrough_route_not_allowed"),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:               provider,
			RequestType:            requestType,
			OriginalModelRequested: req.Model,
			ResolvedModelUsed:      req.Model,
		},
	}
}
