// Package governance provides utility functions for the governance plugin
package governance

import (
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ParseVirtualKeyFromFastHTTPRequest parses the virtual key from FastHTTP request headers.
// Parameters:
//   - req: The FastHTTP request containing headers to parse
//
// Returns:
//   - *string: The virtual key if found, nil otherwise
func ParseVirtualKeyFromFastHTTPRequest(req *fasthttp.RequestCtx) *string {
	vkHeader := string(req.Request.Header.Peek("x-bf-vk"))
	if vkHeader != "" && strings.HasPrefix(strings.ToLower(vkHeader), VirtualKeyPrefix) {
		return bifrost.Ptr(vkHeader)
	}
	authHeader := string(req.Request.Header.Peek("Authorization"))
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
			if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), VirtualKeyPrefix) {
				return bifrost.Ptr(authHeaderValue)
			}
		}
	}
	xAPIKey := string(req.Request.Header.Peek("x-api-key"))
	if xAPIKey != "" && strings.HasPrefix(strings.ToLower(xAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xAPIKey)
	}
	xGoogleAPIKey := string(req.Request.Header.Peek("x-goog-api-key"))
	if xGoogleAPIKey != "" && strings.HasPrefix(strings.ToLower(xGoogleAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xGoogleAPIKey)
	}
	azureAPIKey := string(req.Request.Header.Peek("api-key"))
	if azureAPIKey != "" && strings.HasPrefix(strings.ToLower(azureAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(azureAPIKey)
	}
	return nil
}

// IsModelRequiredForRequest checks if the requested model is required for this request
func IsModelRequiredForRequest(requestType schemas.RequestType) bool {
	// Here we will have to check for some requests which do not need model
	// For example, batches, container, files, videos, passthrough requests
	// For these requests, we will only check for provider filtering
	// Cached content list/retrieve/update/delete target a resource name (cachedContents/{id}),
	// not a model, so they carry no model to filter on; only create binds a cache to a model.
	// Responses retrieve/delete/cancel/input_items target a response_id, not a model.
	// Video edit's model is optional too — the OpenAI SDKs send none and the provider infers it from
	// the source video — so it is evaluated only when the caller supplies one, same as passthrough.
	if requestType == schemas.ListModelsRequest || requestType == schemas.MCPToolExecutionRequest || requestType == schemas.BatchCreateRequest || requestType == schemas.BatchListRequest || requestType == schemas.BatchRetrieveRequest || requestType == schemas.BatchCancelRequest || requestType == schemas.BatchResultsRequest || requestType == schemas.FileUploadRequest || requestType == schemas.FileListRequest || requestType == schemas.FileRetrieveRequest || requestType == schemas.FileDeleteRequest || requestType == schemas.FileContentRequest || requestType == schemas.ContainerCreateRequest || requestType == schemas.ContainerListRequest || requestType == schemas.ContainerRetrieveRequest || requestType == schemas.ContainerDeleteRequest || requestType == schemas.ContainerFileCreateRequest || requestType == schemas.ContainerFileListRequest || requestType == schemas.ContainerFileRetrieveRequest || requestType == schemas.ContainerFileContentRequest || requestType == schemas.ContainerFileDeleteRequest || requestType == schemas.CachedContentListRequest || requestType == schemas.CachedContentRetrieveRequest || requestType == schemas.CachedContentUpdateRequest || requestType == schemas.CachedContentDeleteRequest || requestType == schemas.ResponsesRetrieveRequest || requestType == schemas.ResponsesRetrieveStreamRequest || requestType == schemas.ResponsesDeleteRequest || requestType == schemas.ResponsesCancelRequest || requestType == schemas.ResponsesInputItemsRequest || requestType == schemas.VideoRetrieveRequest || requestType == schemas.VideoDownloadRequest || requestType == schemas.VideoListRequest || requestType == schemas.VideoDeleteRequest || requestType == schemas.VideoRemixRequest || requestType == schemas.VideoEditRequest || requestType == schemas.PassthroughRequest || requestType == schemas.PassthroughStreamRequest {
		return false
	}
	return true
}

// IsModelCheckedWhenPresent reports whether a request type whose model is optional
// should still be checked against the model allowlist when it does carry one.
//
// These are the types IsModelRequiredForRequest excludes because their model may
// legitimately be absent — a file-based batch names none, passthrough forwards raw
// routes, video edit lets the provider infer it. "Optional" must not mean
// "unenforced": when the caller does name a model, the allowlist applies.
func IsModelCheckedWhenPresent(requestType schemas.RequestType) bool {
	switch requestType {
	case schemas.PassthroughRequest, schemas.PassthroughStreamRequest,
		schemas.VideoEditRequest, schemas.BatchCreateRequest:
		return true
	default:
		return false
	}
}

// parseVirtualKeyFromHTTPRequest parses the virtual key from HTTP request headers.
// It checks multiple headers in order: x-bf-vk, Authorization (Bearer token), x-api-key, and x-goog-api-key.
// Parameters:
//   - req: The HTTP request containing headers to parse
//
// Returns:
//   - *string: The virtual key if found, nil otherwise
func parseVirtualKeyFromHTTPRequest(req *schemas.HTTPRequest) *string {
	var virtualKeyValue string
	vkHeader := req.CaseInsensitiveHeaderLookup("x-bf-vk")
	if vkHeader != "" && strings.HasPrefix(strings.ToLower(vkHeader), VirtualKeyPrefix) {
		return new(vkHeader)
	}
	authHeader := req.CaseInsensitiveHeaderLookup("Authorization")
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
			if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), VirtualKeyPrefix) {
				virtualKeyValue = authHeaderValue
			}
		}
	}
	if virtualKeyValue != "" {
		return new(virtualKeyValue)
	}
	xAPIKey := req.CaseInsensitiveHeaderLookup("x-api-key")
	if xAPIKey != "" && strings.HasPrefix(strings.ToLower(xAPIKey), VirtualKeyPrefix) {
		return new(xAPIKey)
	}
	// Checking x-goog-api-key header
	xGoogleAPIKey := req.CaseInsensitiveHeaderLookup("x-goog-api-key")
	if xGoogleAPIKey != "" && strings.HasPrefix(strings.ToLower(xGoogleAPIKey), VirtualKeyPrefix) {
		return new(xGoogleAPIKey)
	}
	return nil
}

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	return *w
}

// filterModelsForAccess drops the models a request may not use from a listing. The request's
// access decides, so a model reachable only through a grant composed onto the request is kept,
// and one the composition removed is dropped: the listing and the request agree by construction
// rather than by two implementations happening to match.
func (p *GovernancePlugin) filterModelsForAccess(ctx *schemas.BifrostContext, models []schemas.Model) []schemas.Model {
	access, err := p.ResolveAccess(ctx)
	if err != nil || access == nil {
		// Nothing resolved: the request presented a credential that carries no permit, or reached
		// here with no grant at all, so it may list nothing.
		return []schemas.Model{}
	}

	filtered := make([]schemas.Model, 0, len(models))
	for _, model := range models {
		provider, modelName := schemas.ParseModelString(model.ID, "")
		if access.IsModelAllowed(string(provider), modelName) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// validateRequiredHeaders checks that all configured required headers are present in the request.
// Headers are compared case-insensitively (both sides lowercased).
// Returns a BifrostError with status 400 if any required headers are missing, or nil if all present.
func (p *GovernancePlugin) validateRequiredHeaders(ctx *schemas.BifrostContext) *schemas.BifrostError {
	if p.requiredHeaders == nil || len(*p.requiredHeaders) == 0 {
		return nil
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if headers == nil {
		headers = map[string]string{}
	}
	var missing []string
	for _, h := range *p.requiredHeaders {
		if _, ok := headers[strings.ToLower(h)]; !ok {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		return &schemas.BifrostError{
			Type:       bifrost.Ptr("missing_required_headers"),
			StatusCode: bifrost.Ptr(400),
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("missing required headers: %s", strings.Join(missing, ", ")),
			},
		}
	}
	return nil
}

// modelProviders types a list of provider names the way the routing layers read them.
func modelProviders(names []string) []schemas.ModelProvider {
	providers := make([]schemas.ModelProvider, 0, len(names))
	for _, name := range names {
		providers = append(providers, schemas.ModelProvider(name))
	}
	return providers
}

// hasDirectKeyAuth returns true when the transport accepted an admin-enabled direct provider key.
func hasDirectKeyAuth(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	return ok
}

// presentedGrantBearingCredential reports whether the request carried a credential that should have
// produced a grant. Two do: a virtual key, and the identity a caller authenticated as. Each names an
// entity whose access is configured somewhere, so failing to find that entity is a failed
// authentication rather than an absent one.
//
// Both, not just the key, because a deployment may resolve grants from either. Reading only the key
// would refuse a caller who authenticated as themselves for not holding a key, and would let one whose
// configured access has gone missing proceed as though they had presented nothing, which is
// unrestricted.
//
// It asks what the request carried, never what that resolved to, and it asks the identity the
// transport settled: what was presented is that layer's to say. A context nothing settled an identity
// on, as one built outside a transport is, is read the way it always was.
//
// A direct key is not one of them, though it is a credential the request carried. It is a raw provider
// key supplied to bypass the configured key pool, so nothing in the governance model describes it and
// nothing could have resolved a grant for it. Counting it here would refuse every direct-key request for
// lacking an access it was never meant to have. It still answers the mandatory-auth question (something
// was presented), which is why that step asks about it separately.
func presentedGrantBearingCredential(ctx *schemas.BifrostContext) bool {
	if identity := ctx.Grant().Identity(); identity != nil {
		return identity.Presented() || identity.User() != nil
	}
	return false
}

// pruneMCPIncludeToolsFromContext narrows a caller-provided include-tools list (stamped on ctx
// from the x-bf-mcp-include-tools header in lib/ctx.go) down to the tools the request's access
// allows, and writes the pruned list back to ctx. Returns true when a caller list was present,
// regardless of how many entries survived. The narrowing rule itself is the access's own, so every
// surface that honours the header narrows it identically.
func (p *GovernancePlugin) pruneMCPIncludeToolsFromContext(ctx *schemas.BifrostContext, access schemas.Access) bool {
	existing := ctx.Value(schemas.MCPContextKeyIncludeTools)
	if existing == nil {
		return false
	}
	requested, _ := existing.([]string)
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, access.NarrowMCPToolIncludeList(requested))
	return true
}
