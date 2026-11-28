// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"unsafe"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/tidwall/gjson"
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
	// Azure AI Speech clients send their key as Ocp-Apim-Subscription-Key.
	speechAPIKey := string(req.Request.Header.Peek("Ocp-Apim-Subscription-Key"))
	if speechAPIKey != "" && strings.HasPrefix(strings.ToLower(speechAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(speechAPIKey)
	}
	// ElevenLabs clients send their key as xi-api-key.
	elevenLabsAPIKey := string(req.Request.Header.Peek("xi-api-key"))
	if elevenLabsAPIKey != "" && strings.HasPrefix(strings.ToLower(elevenLabsAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(elevenLabsAPIKey)
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
	if requestType == schemas.ListModelsRequest || requestType == schemas.MCPToolExecutionRequest || requestType == schemas.RelayRequest || requestType == schemas.BatchCreateRequest || requestType == schemas.BatchListRequest || requestType == schemas.BatchRetrieveRequest || requestType == schemas.BatchCancelRequest || requestType == schemas.BatchResultsRequest || requestType == schemas.FileUploadRequest || requestType == schemas.FileListRequest || requestType == schemas.FileRetrieveRequest || requestType == schemas.FileDeleteRequest || requestType == schemas.FileContentRequest || requestType == schemas.ContainerCreateRequest || requestType == schemas.ContainerListRequest || requestType == schemas.ContainerRetrieveRequest || requestType == schemas.ContainerDeleteRequest || requestType == schemas.ContainerFileCreateRequest || requestType == schemas.ContainerFileListRequest || requestType == schemas.ContainerFileRetrieveRequest || requestType == schemas.ContainerFileContentRequest || requestType == schemas.ContainerFileDeleteRequest || requestType == schemas.CachedContentListRequest || requestType == schemas.CachedContentRetrieveRequest || requestType == schemas.CachedContentUpdateRequest || requestType == schemas.CachedContentDeleteRequest || requestType == schemas.ResponsesRetrieveRequest || requestType == schemas.ResponsesRetrieveStreamRequest || requestType == schemas.ResponsesDeleteRequest || requestType == schemas.ResponsesCancelRequest || requestType == schemas.ResponsesInputItemsRequest || requestType == schemas.VideoRetrieveRequest || requestType == schemas.VideoDownloadRequest || requestType == schemas.VideoListRequest || requestType == schemas.VideoDeleteRequest || requestType == schemas.VideoRemixRequest || requestType == schemas.VideoEditRequest || requestType == schemas.PassthroughRequest || requestType == schemas.PassthroughStreamRequest || requestType == schemas.LiveContentRequest || requestType == schemas.LiveSidebandRequest {
		return false
	}
	return true
}

// batchModelBoundByProvider reports whether a provider's batch API runs the model named on the
// batch create request. Gemini (models/{m}:batchGenerateContent), Vertex (job model) and Bedrock
// (required modelId) do; OpenAI and Azure take only the uploaded input, whose rows each name their
// own model, so the request's model is at most a pricing hint there. A custom provider runs its
// base provider's batch API under its own name, so it is judged by the base type the configured
// providers record for it; a provider the store does not know is judged by its name.
func batchModelBoundByProvider(provider schemas.ModelProvider, configured *livestore.Store) bool {
	switch configuredBaseProvider(configured, provider) {
	case schemas.OpenAI, schemas.Azure:
		return false
	default:
		return true
	}
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

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
// A negative or non-finite weight counts as 0: the API rejects one, but config.json and older rows
// can still carry it, and summed into the draw it would select the first provider whatever the
// other weights say.
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	if *w < 0 || math.IsNaN(*w) || math.IsInf(*w, 0) {
		return 0
	}
	return *w
}

// filterModelsForAccess drops the models a request may not use from a listing. The access decides,
// so a model reachable only through a permit composed onto the request is kept, and one the
// composition removed is dropped: the listing and the request agree by construction rather than by
// two implementations happening to match.
//
// The access is handed in rather than resolved here. A listing is produced after the request has
// been evaluated, and resolving again at that point would answer for whatever configuration has
// become since; what a caller may list is what it was admitted under. No access lists nothing: a
// request that presented nothing is not narrowed at all, and the caller gates on that before asking.
func (p *GovernancePlugin) filterModelsForAccess(access schemas.Access, models []schemas.Model) []schemas.Model {
	if access == nil {
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

// filterPassthroughModelList trims a raw passthrough model list to the models access allows, using the filter the transport registered for the route.
func (p *GovernancePlugin) filterPassthroughModelList(ctx *schemas.BifrostContext, access schemas.Access, provider string, resp *schemas.BifrostPassthroughResponse) {
	filter, ok := ctx.Value(schemas.BifrostContextKeyPassthroughModelListFilter).(schemas.PassthroughModelListFilter)
	if !ok || filter == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	body, err := filter(resp.Body, func(model string) bool {
		return access != nil && access.IsModelAllowed(provider, model)
	})
	if err != nil {
		// Fail open: every turn is still checked against the allowlist.
		p.logger.Warn("governance: passthrough model list left unfiltered: %v", err)
		return
	}
	resp.Body = body
	// The upstream ETag names the unfiltered list.
	for _, headers := range []map[string]string{resp.Headers, resp.ExtraFields.ProviderResponseHeaders} {
		for k := range headers {
			if strings.EqualFold(k, "etag") {
				delete(headers, k)
			}
		}
	}
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
	// A context with no grant at all reads as nothing presented, the same answer an empty
	// identity gives. Worth stating explicitly because Grant() returns a nil interface rather
	// than a zero value, so reaching straight for Identity() panics: every request that reaches
	// Evaluate has a grant, but a transport asking this question directly (realtime admission)
	// can hold a context built before one was ever recorded.
	g := ctx.Grant()
	if g == nil {
		return false
	}
	if identity := g.Identity(); identity != nil {
		return identity.Presented() || identity.User() != nil
	}
	return false
}

// ungrantedUserAdmitted reports whether a signed-in user nothing grants access to is to be served
// anyway, as a key-less request is, rather than refused as access not found. Only a caller that
// asked for it and only for a user: Warp asks, because reaching its chat route already proves the
// user's role allows it, and on a deployment without access profiles no user ever holds a permit. A
// virtual key the request presents is never admitted this way - a key that resolves to nothing is a
// dead key whoever presents it - and neither is a request that names no user at all.
func ungrantedUserAdmitted(ctx *schemas.BifrostContext) bool {
	if !bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeyAdmitUngrantedUser) {
		return false
	}
	g := ctx.Grant()
	if g == nil {
		return false
	}
	identity := g.Identity()
	if identity == nil || identity.User() == nil {
		return false
	}
	return identity.Credential().Kind != string(grant.CredentialVirtualKey)
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

// PresentedAnyCredential reports whether the request carried any credential at all that answers
// the mandatory-authentication question: a grant-bearing one (virtual key or authenticated
// identity), or a direct provider key. It is the exact question Evaluate's first step asks, and
// asking it alone costs nothing - it reads the context and settles no limits.
//
// Exported so a transport can admit or refuse a connection on the same answer, rather than
// reimplementing "was this authenticated" beside this one. Realtime is the caller that needs it:
// a WebSocket upgrade opens an upstream provider session on the operator's key before any turn
// exists to evaluate, so admission has to be decided at the upgrade, while the per-turn pipeline
// keeps owning access and limits. Callers must still let the per-request pipeline run - this
// answers only whether a credential was presented, never whether it grants what is being asked
// for, and it deliberately settles no limits so an admission check cannot double-count usage
// against the turns that follow.
func PresentedAnyCredential(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	return presentedGrantBearingCredential(ctx) || hasDirectKeyAuth(ctx)
}

// PresentedCredentialResolved reports whether the credential the request presented resolved to
// usable access. It is the second admission question realtime asks after PresentedAnyCredential:
// not "was something presented" but "did what was presented turn out to exist". It reads the
// answer ResolveAccess recorded on the request's grant rather than resolving anything itself, so
// callers must run the per-request pipeline (PreRequestHook) first; asked earlier it reports the
// credential unresolved, because it is.
//
// A direct provider key resolves to nothing by design (nothing in the governance model describes
// it), so it counts as resolved here: refusing it for lacking an access it was never meant to
// have would close direct-key requests entirely. A grant-bearing credential that resolved to no
// access, or to access whose permit is unusable (revoked, expired, inactive), is exactly the
// forged-or-revoked case this question exists to catch. Like PresentedAnyCredential, it settles
// no limits, so admission cannot double-count usage against the turns that follow.
func PresentedCredentialResolved(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	if hasDirectKeyAuth(ctx) {
		return true
	}
	if !presentedGrantBearingCredential(ctx) {
		return false
	}
	g := ctx.Grant()
	if g == nil {
		return false
	}
	access := g.Access()
	return access != nil && unusablePermit(access) == nil
}

// ResolvedVirtualKeyID returns the id of the virtual key the request resolved to, or "" when it
// presented none, presented one that matched no row, or matched a row whose access may not be used
// (inactive or expired). Like PresentedCredentialResolved it reads what the per-request pipeline
// recorded on the grant, so callers run PreRequestHook first, and it settles no limits.
//
// A transport that keys per-caller state on the virtual key uses this rather than the header or
// PresentedCredentialResolved: a direct key admits a request on its own, so the virtual key sent
// beside it is not checked by that answer, and a dead key's row names no caller entitled to a share.
func ResolvedVirtualKeyID(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	g := ctx.Grant()
	if g == nil {
		return ""
	}
	identity := g.Identity()
	if identity == nil {
		return ""
	}
	vk := identity.VirtualKey()
	if vk == nil || vk.ID == "" {
		return ""
	}
	access := g.Access()
	if access == nil || unusablePermit(access) != nil {
		return ""
	}
	return vk.ID
}

// AppendAllProviderPermits completes a permit that grants every provider. Such a permit names none,
// so the providers it grants by the flag alone are materialised here, from what the deployment has
// configured, and the permit then carries its whole grant in one readable list.
//
// Doing it at permit construction rather than where a consumer reads the permit is what keeps every
// consumer honest: enumerating provider permits and asking whether the permit allows a provider give
// the same answer, so a listing cannot refuse what the request path admits. Built per request, so a
// provider added after the permit was last written is granted by the same rule.
//
// A provider the permit already names keeps its own entry: those are overrides, and the flag widens
// the set rather than relaxing them. A materialised entry narrows nothing - every model, every key,
// nothing blocked - and carries no weight, because a weight is a routing preference a provider
// config expresses and this one expresses none.
func AppendAllProviderPermits(permits []schemas.ProviderPermit, configured []string) []schemas.ProviderPermit {
	named := make(map[string]struct{}, len(permits))
	for i := range permits {
		named[permits[i].Provider] = struct{}{}
	}
	for _, provider := range configured {
		if provider == "" {
			continue
		}
		if _, dup := named[provider]; dup {
			continue
		}
		named[provider] = struct{}{}
		permits = append(permits, schemas.ProviderPermit{
			Provider:      provider,
			AllowedModels: schemas.WhiteList{"*"},
			KeyIDs:        schemas.WhiteList{"*"},
		})
	}
	return permits
}

// isLiveSessionContinuation reports a billing unit of an admitted live session: the continuation
// flag counts only on a live request, so no other path can skip request limits by setting it.
func isLiveSessionContinuation(ctx context.Context) bool {
	continuation, _ := ctx.Value(schemas.BifrostContextKeySessionContinuation).(bool)
	requestType, _ := ctx.Value(schemas.BifrostContextKeyHTTPRequestType).(schemas.RequestType)
	return continuation && requestType == schemas.LiveRequest
}

// configuredBaseProvider is the provider whose implementation runs provider's requests: the base
// type of a custom provider, otherwise provider itself. It reads that one provider's configuration.
func configuredBaseProvider(live *livestore.Store, provider schemas.ModelProvider) schemas.ModelProvider {
	p, ok := live.Collections.Providers.Get(string(provider))
	if !ok || p.Config.CustomProviderConfig == nil || p.Config.CustomProviderConfig.BaseProviderType == "" {
		return provider
	}
	return p.Config.CustomProviderConfig.BaseProviderType
}

// ConfiguredProviderNames lists the providers configured in live, for holders allowed every
// provider.
func ConfiguredProviderNames(live *livestore.Store) []string {
	var names []string
	live.Collections.Providers.Range(func(p *registry.Provider) bool {
		names = append(names, string(p.Name))
		return true
	})
	return names
}

// MCPClientNames maps the ID of every MCP client configured in live to its name, so permits can
// name the clients they reach.
func MCPClientNames(live *livestore.Store) map[string]string {
	names := make(map[string]string)
	live.Collections.MCPClients.Range(func(c *registry.MCPClient) bool {
		if c.Config.ID != "" && c.Config.Name != "" {
			names[c.Config.ID] = c.Config.Name
		}
		return true
	})
	return names
}

// MCPClientsAllowedByDefault maps the ID of every MCP client in live open to every holder without
// an assignment to its name.
func MCPClientsAllowedByDefault(live *livestore.Store) map[string]string {
	open := make(map[string]string)
	live.Collections.MCPClients.Range(func(c *registry.MCPClient) bool {
		if c.Config.AllowByDefault {
			open[c.Config.ID] = c.Config.Name
		}
		return true
	})
	return open
}

// objectOwnershipEnforced reports whether provider objects are restricted to the virtual key
// that created them (client.enforce_vk_object_ownership). It is on unless configured off. The
// transport's getter reads under the same lock config saves hold, so a save never races a request.
func (p *GovernancePlugin) objectOwnershipEnforced() bool {
	return p.objectOwnershipEnforcedFn == nil || p.objectOwnershipEnforcedFn()
}

// candidateExclusionReason is what a routing log says about a candidate governance would not fund.
//
// Routing logs are read by whoever made the request, so this says what happened in plain words rather
// than naming the decision that produced it. The store answers with a Decision because that is what its
// other checks answer with; turning one into a sentence belongs here, with the log.
func candidateExclusionReason(decision Decision, err error) string {
	switch {
	case isRateLimitViolation(decision):
		return "rate limit violated"
	case isBudgetViolation(decision):
		return "budget limit violated"
	case err != nil:
		return err.Error()
	default:
		return "governance would not fund it"
	}
}

// namedProjectNothingScoped reports whether the request named a project and ended up scoped by
// none, and what it named it.
//
// The same shape as presentedGrantBearingCredential above, for the same reason: what the request
// asked for is read off its own input, and only comparing that against what resolution produced can
// tell "asked for nothing" from "asked for something that is not there". A project named with no
// resolved project behind it was not granted (it does not exist, or it does not admit this caller),
// and those are not told apart here, exactly as a credential that resolves to nothing does not say
// whether it never existed or was revoked.
//
// A header that arrived empty still counts as having asked. It named no project, so no project can
// be resolved from it, and treating it as not having asked would serve the request against the
// caller's own access instead of refusing a scope it could not be given.
//
// The resolved id rather than the scoping slot, because that id is set at exactly the moment a
// project is admitted. Reading the slot would answer this question wrongly the moment anything other
// than a project fills it.
func namedProjectNothingScoped(ctx *schemas.BifrostContext) (string, bool) {
	if bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceProjectID) != "" {
		return "", false
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	for _, header := range []string{schemas.HeaderGovernanceProjectID, schemas.HeaderGovernanceProjectName} {
		if named, sent := headers[header]; sent {
			return strings.TrimSpace(named), true
		}
	}
	return "", false
}

// unusablePermit is the refusal for a request that carries a permit that may not be used: one that
// is inactive, or has expired. Whether it may be used is settled when the permit is built, so this
// reads the answer off it. Nil when every permit the request carries may be used, or it carries
// none, which the funnel answers separately by what was presented.
//
// Every slot is judged, because a request scoped by a second permit answers to both, and a permit
// that has been deactivated or has expired grants nothing whichever slot it sits in. Nothing in the
// fold reads either flag (it answers what a permit enumerates, not whether the permit may still be
// used), so a scoping permit nobody checked here would go on permitting everything it names.
func unusablePermit(access schemas.Access) *EvaluationResult {
	if access == nil {
		return nil
	}
	permits := make([]schemas.Permit, 0, len(access.Bases())+1)
	permits = append(permits, access.Bases()...)
	permits = append(permits, access.Scoping())
	for _, permit := range permits {
		if permit == nil {
			continue
		}
		if !permit.IsActive() {
			return &EvaluationResult{
				Decision: DecisionAccessBlocked,
				Reason:   fmt.Sprintf("%s is inactive", grant.PermitType(permit.Type()).PrettyString()),
			}
		}
		if permit.IsExpired() {
			return &EvaluationResult{
				Decision: DecisionAccessBlocked,
				Reason:   fmt.Sprintf("%s has expired", grant.PermitType(permit.Type()).PrettyString()),
			}
		}
	}
	return nil
}

// refusal builds the error a governance decision refuses a request with.
func refusal(result *EvaluationResult, statusCode int, errorType schemas.ErrorType) *schemas.BifrostError {
	return &schemas.BifrostError{
		// Type stays the raw decision: it is the client-visible error contract.
		Type:       new(string(result.Decision)),
		StatusCode: new(statusCode),
		Error: &schemas.ErrorField{
			Message: result.Reason,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{ErrorType: errorType},
	}
}

// newAccess folds the permits into an access whose model checks resolve key aliases the way key
// selection will for this request: through the model catalog, over the keys that can serve it.
func (p *GovernancePlugin) newAccess(ctx *schemas.BifrostContext, bases []schemas.Permit, levelMode grant.CompositionMode, scoping schemas.Permit, mode grant.CompositionMode) *grant.Access {
	return grant.NewAccess(bases, levelMode, scoping, mode, nil).
		WithKeyScopedMatcher(p.modelMatcher(ctx)).
		WithAliasTargets(p.aliasTargets(ctx))
}

// modelMatcher is how an access resolves a model name against a provider's allowed-models list:
// through the model catalog, with the provider's own configuration, so cross-provider naming and
// provider-prefixed entries resolve exactly as they do everywhere else. Without a catalog, names
// are matched as written. A name that only a key alias makes allowed is judged over the keys that
// can serve this request (see allowedThroughKeys).
func (p *GovernancePlugin) modelMatcher(ctx *schemas.BifrostContext) grant.KeyScopedModelMatcher {
	if p.modelCatalog == nil {
		return nil
	}
	return func(provider, model string, allowed, keyIDs []string) bool {
		var providerConfig *configstore.ProviderConfig
		var keys []schemas.Key
		if stored, ok := p.live.Collections.Providers.Get(provider); ok {
			config := stored.Config
			providerConfig = &config
			if list, ok := p.live.Views.ProviderKeyLists.Get(provider); ok {
				keys = *list
			}
		}
		return allowedThroughKeys(contextOf(ctx), p.modelCatalog, schemas.ModelProvider(provider), model, providerConfig, keys, allowed, keyIDs)
	}
}

// contextOf is ctx as a context.Context, nil when ctx is. The access closures hold the concrete
// pointer, a word smaller than the interface, since they are built for every request.
func contextOf(ctx *schemas.BifrostContext) context.Context {
	if ctx == nil {
		return nil
	}
	return ctx
}

// allowedThroughKeys is IsModelAllowedForProvider with the key-alias rule held to the keys key
// selection can pick for this request: only keys the permit's key restriction allows, and the pinned
// key when the request pins one. The catalog's own check is passed no keys, so it is the direct
// match only; keys is the provider's key list, shared and read only, and only the alias rule here
// reads it.
//
// The pin is consulted only when the unpinned keys refuse the alias. Narrowing a set of keys that
// all map the name to an allowed model can only turn the answer into a refusal where the pin names
// no key that can serve the name, and core refuses that request at key selection anyway; so the
// common alias request reads nothing from the context. A request that runs as written (a direct key,
// skip key selection) never has an alias resolved, but is admitted on the literal access (see
// literalAccessFor), so this access only widens what such a request may be told it reaches.
func allowedThroughKeys(ctx context.Context, catalog *modelcatalog.ModelCatalog, provider schemas.ModelProvider, model string, providerConfig *configstore.ProviderConfig, keys []schemas.Key, allowed, keyIDs []string) bool {
	if providerConfig == nil || !modelcatalog.HasKeyAliases(keys) {
		return catalog.IsModelAllowedForProvider(provider, model, providerConfig, nil, schemas.WhiteList(allowed))
	}
	if catalog.IsModelAllowedForProvider(provider, model, providerConfig, nil, schemas.WhiteList(allowed)) {
		return true
	}
	if !modelcatalog.MayBeKeyAlias(provider, model, keys) {
		return false
	}
	if catalog.KeyAliasesResolveToAllowed(provider, model, keys, schemas.WhiteList(keyIDs), schemas.WhiteList(allowed)) {
		return true
	}
	pinned, ok := pinnedKeys(ctx, keys)
	if !ok || len(pinned) == len(keys) || runsAsWritten(ctx) {
		return false
	}
	return catalog.KeyAliasesResolveToAllowed(provider, model, pinned, schemas.WhiteList(keyIDs), schemas.WhiteList(allowed))
}

// aliasTargets is how an access learns which models a requested name can become through the
// provider's key aliases, resolved only at key selection: over the pinned key when the request pins
// one, and over the keys the permit allows. A request that runs as written becomes nothing else.
func (p *GovernancePlugin) aliasTargets(ctx *schemas.BifrostContext) grant.AliasTargets {
	return func(provider, model string, keyIDs []string, visit func(target string)) {
		list, ok := p.live.Views.ProviderKeyLists.Get(provider)
		if !ok {
			return
		}
		all := *list // shared, read only
		if !modelcatalog.HasKeyAliases(all) || !modelcatalog.MayBeKeyAlias(schemas.ModelProvider(provider), model, all) || runsAsWritten(contextOf(ctx)) {
			return
		}
		keys, ok := pinnedKeys(contextOf(ctx), all)
		if !ok {
			return
		}
		modelcatalog.KeyAliasTargets(model, keys, schemas.WhiteList(keyIDs), visit)
	}
}

// resolveLimits settles the limits an attempt answers to, now that the provider and model it will
// use are known, and records them on the request's grant for everything downstream.
//
// Until this runs, only the limits that can tell one provider from another are consulted, by load
// balancing, one candidate at a time. What funds a holder across every provider, and what a model
// costs, are settled here.
//
// The result is one list, not several sources a caller combines: what is checked, what is named as
// a co-payer on the log row, and what is charged all read the same answer, so they cannot disagree
// about which limits an attempt was subject to. It is replaced whole for the next attempt, which
// may use another provider.
//
// A request carrying no access is still subject to the deployment's own limits, and they are
// settled for it the same way. A context carrying no grant is a wiring fault evaluation has
// already refused by the time this runs; it settles nothing.
//
// What the list holds is the store's to assemble (see GovernanceStore.GatherLimitsForRequestKind): a store that
// composes several possible payers settles there on who pays, and this only records the answer. An
// error is a request whose payers cannot be settled at all; nothing is recorded for it, and the
// funnel refuses it rather than serving it against half an answer.
//
// A budget's scope is applied here, once, so that what is checked, logged and charged agree: an
// MCP tool execution answers only to "mcp" and "shared" budgets, every other request only to "model"
// and "shared" ones. See BudgetsForRequestKind.
func resolveLimits(ctx *schemas.BifrostContext, store GovernanceStore, requestType schemas.RequestType, provider schemas.ModelProvider, model string) (schemas.Limits, error) {
	g := ctx.Grant()
	if g == nil {
		return nil, nil
	}
	return resolveLimitsWith(ctx, store, g.Access(), requestType, provider, model)
}

// resolveLimitsWith is resolveLimits over a given access rather than the grant's own, for a request
// whose model check used a different one (see literalAccessFor). The grant is still where the
// settled limits are recorded.
func resolveLimitsWith(ctx *schemas.BifrostContext, store GovernanceStore, access schemas.Access, requestType schemas.RequestType, provider schemas.ModelProvider, model string) (schemas.Limits, error) {
	g := ctx.Grant()
	if g == nil {
		return nil, nil
	}
	traffic := configstoreTables.RequestScope(requestType)
	budgets, rateLimits, err := store.GatherLimitsForRequestKind(ctx, access, provider, model, traffic)
	if err != nil {
		return nil, err
	}
	budgets = BudgetsForRequestKind(ctx, store, budgets, traffic)
	limits := grant.NewLimits(budgets, rateLimits)
	g.SetLimits(limits)
	return limits, nil
}

// refuseUnchargedBodyModelLimits refuses a passthrough request when a model its body names settled
// a limit that the request's own pass did not: usage is charged against that pass alone, and a mixed
// task array cannot be split by model, so the body model's budget or rate limit would be checked
// and never charged. The caller sends one model per request instead.
func refuseUnchargedBodyModelLimits(ctx *schemas.BifrostContext, bodyModelLimits map[string]string) *schemas.LLMPluginShortCircuit {
	if len(bodyModelLimits) == 0 {
		return nil
	}
	charged := map[string]struct{}{}
	if g := ctx.Grant(); g != nil && g.Limits() != nil {
		for _, limit := range append(g.Limits().Budgets(), g.Limits().RateLimits()...) {
			charged[limit.ID] = struct{}{}
		}
	}
	var models []string
	for id, model := range bodyModelLimits {
		if _, ok := charged[id]; !ok && !slices.Contains(models, model) {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return nil
	}
	slices.Sort(models)
	ctx.SetValue(governanceRejectedContextKey, true)
	return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
		Type:       bifrost.Ptr(string(DecisionModelLimitsUnattributable)),
		StatusCode: bifrost.Ptr(400),
		Error: &schemas.ErrorField{Message: fmt.Sprintf("the request body names models with budgets or rate limits of their own (%s) that one request cannot be charged against; send one model per request",
			strings.Join(models, ", "))},
	}}
}

// serverToolModels returns every distinct model the request's server tools name: the "model" of an
// advisor tool (Anthropic Messages, /v1/responses and the Go SDK all arrive as Responses requests).
// Every place a tool can ride to the provider is read, not only the parsed tools: ExtraParams
// "tools" (merged onto the wire when extra-params passthrough is on), a raw request body (sent
// verbatim when raw forwarding is on, and only set then), a passthrough body, and inline batch
// items. JSON is walked key by key so a duplicated or escaped "tools", "type" or "model" key names
// every model a provider's parser could pick. A provider prefix ("anthropic/<id>") is dropped the
// way the provider drops it before forwarding. A request without such a tool allocates nothing.
func serverToolModels(req *schemas.BifrostRequest) []string {
	var models []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" {
			return
		}
		if prefixProvider, bare := schemas.ParseModelString(m, ""); prefixProvider != "" {
			m = bare
		}
		if !slices.Contains(models, m) {
			// m may view a request body read without a copy (see jsonView); keep a copy of its own.
			models = append(models, strings.Clone(m))
		}
	}
	if rr := req.ResponsesRequest; rr != nil {
		if rr.Params != nil {
			for i := range rr.Params.Tools {
				if advisor := rr.Params.Tools[i].ResponsesToolAdvisor; advisor != nil {
					add(advisor.Model)
				}
			}
			addToolsValueModels(rr.Params.ExtraParams["tools"], add)
		}
		addBodyToolModels(rr.RawRequestBody, add)
	}
	if cr := req.ChatRequest; cr != nil {
		if cr.Params != nil {
			addToolsValueModels(cr.Params.ExtraParams["tools"], add)
		}
		addBodyToolModels(cr.RawRequestBody, add)
	}
	if req.PassthroughRequest != nil {
		addBodyToolModels(req.PassthroughRequest.Body, add)
	}
	if br := req.BatchCreateRequest; br != nil {
		for i := range br.Requests {
			addToolsValueModels(br.Requests[i].Body["tools"], add)
			addToolsValueModels(br.Requests[i].Params["tools"], add)
		}
		if len(br.RawRequestBody) > 0 {
			forEachKey(gjson.Parse(jsonView(br.RawRequestBody)), "requests", func(items gjson.Result) {
				items.ForEach(func(_, item gjson.Result) bool {
					forEachKey(item, "params", func(params gjson.Result) { addToolArrayModels(params, add) })
					forEachKey(item, "body", func(body gjson.Result) { addToolArrayModels(body, add) })
					return true
				})
			})
		}
	}
	return models
}

// addToolsValueModels adds the advisor models of a decoded "tools" value (ExtraParams, a batch
// item), whatever Go shape it arrived in, by reading the JSON it will be sent as.
func addToolsValueModels(tools any, add func(string)) {
	var raw []byte
	switch v := tools.(type) {
	case nil:
		return
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		var err error
		if raw, err = schemas.Marshal(v); err != nil {
			return
		}
	}
	addToolListModels(gjson.Parse(jsonView(raw)), add)
}

// addBodyToolModels adds the advisor models of a JSON request body's "tools" arrays.
func addBodyToolModels(body []byte, add func(string)) {
	if len(body) > 0 {
		addToolArrayModels(gjson.Parse(jsonView(body)), add)
	}
}

// jsonView returns body as a string without copying it, for a read-only gjson scan that finishes
// before the hook returns; any string kept from the scan is cloned.
func jsonView(body []byte) string {
	return unsafe.String(unsafe.SliceData(body), len(body))
}

// addToolArrayModels adds the advisor models of every "tools" array of object.
func addToolArrayModels(object gjson.Result, add func(string)) {
	forEachKey(object, "tools", func(tools gjson.Result) { addToolListModels(tools, add) })
}

// addToolListModels adds the models of each advisor tool in a tools array.
func addToolListModels(tools gjson.Result, add func(string)) {
	if !tools.IsArray() {
		return
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		isAdvisor := false
		forEachKey(tool, "type", func(t gjson.Result) {
			if t.Type == gjson.String && strings.HasPrefix(t.Str, "advisor") {
				isAdvisor = true
			}
		})
		if isAdvisor {
			forEachKey(tool, "model", func(m gjson.Result) {
				if m.Type == gjson.String {
					add(m.Str)
				}
			})
		}
		return true
	})
}

// forEachKey calls fn with the value of every member of object named name, duplicates included.
func forEachKey(object gjson.Result, name string, fn func(gjson.Result)) {
	if !object.IsObject() {
		return
	}
	object.ForEach(func(key, value gjson.Result) bool {
		if key.Str == name {
			fn(value)
		}
		return true
	})
}

// passthroughBodyModels returns every distinct model a passthrough body names: the "model" of a
// JSON object body, or of each task in a JSON array body (Runware). The router has already
// refused malformed model keys, so a model that is not a string is simply not one here.
func passthroughBodyModels(req *schemas.BifrostRequest) []string {
	if req.PassthroughRequest == nil || len(req.PassthroughRequest.Body) == 0 || !gjson.ValidBytes(req.PassthroughRequest.Body) {
		return nil
	}
	root := gjson.ParseBytes(req.PassthroughRequest.Body)
	seen := make(map[string]struct{})
	var models []string
	add := func(object gjson.Result) bool {
		if value := object.Get("model"); value.Type == gjson.String {
			if m := strings.TrimSpace(value.Str); m != "" {
				if _, ok := seen[m]; !ok {
					seen[m] = struct{}{}
					models = append(models, m)
				}
			}
		}
		return true
	}
	switch {
	case root.IsObject():
		add(root)
	case root.IsArray():
		root.ForEach(func(_, task gjson.Result) bool {
			if task.IsObject() {
				add(task)
			}
			return true
		})
	}
	return models
}

// bodyNamesModels reports whether a passthrough body's "model" is a Bifrost model. SageMaker's is the
// container's own served name: the endpoint in the path is the model, and the router already set it.
func (p *GovernancePlugin) bodyNamesModels(provider schemas.ModelProvider) bool {
	return configuredBaseProvider(p.live, provider) != schemas.SageMaker
}

// namesModelsAsResources reports whether a provider name reaches Gemini or Vertex, whose bodies
// name a model by its resource name rather than its bare id.
func (p *GovernancePlugin) namesModelsAsResources(provider schemas.ModelProvider) bool {
	base := configuredBaseProvider(p.live, provider)
	return base == schemas.Gemini || base == schemas.Vertex
}

// bareResourceModel reduces a GenAI or Vertex model resource name ("models/<id>",
// "tunedModels/<id>", "publishers/google/models/<id>", "projects/<p>/locations/<l>/publishers/
// google/models/<id>") to the bare id, the way the router reduces the request model, so a body and
// the request it rides on are judged on the same name. Anything else is returned unchanged.
func bareResourceModel(model string) string {
	if !strings.HasPrefix(model, "models/") && !strings.HasPrefix(model, "tunedModels/") &&
		!strings.HasPrefix(model, "projects/") && !strings.HasPrefix(model, "publishers/") {
		return model
	}
	parts := strings.Split(model, "/")
	for i, part := range parts {
		if (part == "models" || part == "tunedModels") && i+1 < len(parts) {
			id := parts[i+1]
			if colon := strings.Index(id, ":"); colon > 0 {
				id = id[:colon]
			}
			if id = strings.TrimSpace(id); id != "" {
				return id
			}
			break
		}
	}
	return model
}

// isOpaqueBatchInput reports whether a batch create's work is an uploaded file or blob, so the
// models it will run are not visible on the request. Inline items beside a file do not make the
// batch inline: no provider runs them then. OpenAI, Azure and Bedrock submit the file and drop
// the items, Gemini and Vertex refuse the mix, so the items are at most a decoy for the model
// check and the file rule has to hold.
func isOpaqueBatchInput(req *schemas.BifrostRequest) bool {
	if req.RequestType != schemas.BatchCreateRequest || req.BatchCreateRequest == nil {
		return false
	}
	batch := req.BatchCreateRequest
	return batch.InputFileID != "" || (batch.InputBlob != nil && strings.TrimSpace(*batch.InputBlob) != "")
}

// BatchItemModels returns the distinct models the inline items of a batch create name. They reach
// the provider as written, unlike the request's own model, so governance holds them to the literal
// name.
func BatchItemModels(req *schemas.BifrostRequest) []string {
	if req.RequestType != schemas.BatchCreateRequest || req.BatchCreateRequest == nil {
		return nil
	}
	var models []string
	for _, item := range req.BatchCreateRequest.Requests {
		forEachItemModel(item, func(m string) {
			if !slices.Contains(models, m) {
				models = append(models, m)
			}
		})
	}
	return models
}

// forEachItemModel calls fn with every non-empty string "model" an inline batch item names, Body
// first and then Params. Providers disagree on which map they send (Anthropic sends Params and
// falls back to Body), so every model an item could run is visited rather than the first found.
func forEachItemModel(item schemas.BatchRequestItem, fn func(model string)) {
	for _, body := range [2]map[string]any{item.Body, item.Params} {
		if m, ok := body["model"].(string); ok && m != "" {
			fn(m)
		}
	}
}

// BatchCreateModels returns every distinct model an inline batch create will run,
// starting with the request's own model.
func BatchCreateModels(req *schemas.BifrostRequest, model string) []string {
	if req.RequestType != schemas.BatchCreateRequest || req.BatchCreateRequest == nil || len(req.BatchCreateRequest.Requests) == 0 {
		return []string{model}
	}
	seen := make(map[string]struct{})
	models := make([]string, 0, 1)
	add := func(m string) {
		if m == "" {
			return
		}
		if _, exists := seen[m]; exists {
			return
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	add(model)
	for _, item := range req.BatchCreateRequest.Requests {
		forEachItemModel(item, add)
	}
	if len(models) == 0 {
		// No item named a model: fall back to the model-less evaluation so the
		// provider-level and virtual-key checks still run.
		return []string{model}
	}
	return models
}

// stampMCPToolAccessForCodemode narrows the request's MCP include-tools list to
// the access's grant before a codemode meta-tool runs. A caller-provided list
// can only narrow the grant, never expand it; with none provided the grant
// itself is stamped. A request carrying no access is unrestricted and is left
// untouched, as it is everywhere else. A credential that resolves to nothing,
// or a context access cannot be resolved for at all (no grant installed), gets
// an empty list; evaluation refuses both before this runs, so that list only
// fails closed should it ever be reached. An ungranted user the caller asked to have admitted is
// the exception on both sides, served and shown everything, as a key-less
// request is.
func (p *GovernancePlugin) stampMCPToolAccessForCodemode(ctx *schemas.BifrostContext) {
	access, err := p.ResolveAccess(ctx)
	if err != nil {
		if ctx != nil {
			ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{})
		}
		return
	}
	if access == nil {
		if presentedGrantBearingCredential(ctx) && !ungrantedUserAdmitted(ctx) {
			ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{})
		}
		return
	}
	if p.pruneMCPIncludeToolsFromContext(ctx, access) {
		return
	}
	if tools := access.MCPToolIncludeList(); tools != nil {
		ctx.SetValue(schemas.MCPContextKeyIncludeTools, tools)
	}
}

// agentEnabled reports whether an agent-gateway registration with this name exists and is enabled.
func (p *GovernancePlugin) agentEnabled(name string) bool {
	a, ok := p.live.Collections.Agents.Get(name)
	return ok && a.Enabled
}

// defaultAgentPermit is the permit agents allowed by default give a caller identified by a user or
// a key; nil when there is no such caller or no enabled agent is allowed by default. It names
// agentName, the agent the request asks for, when that agent is enabled and allowed by default,
// and no other: the access it joins is asked about that agent only. While any agent is allowed by
// default the permit is given even when it names none, so a caller refused another agent is
// refused that agent, with the default access among the permits that refused.
func (p *GovernancePlugin) defaultAgentPermit(identity schemas.Identity, agentName string) schemas.Permit {
	if identity == nil || (identity.User() == nil && identity.VirtualKey() == nil) {
		return nil
	}
	if _, open := p.live.Collections.Agents.GetBy(registry.AgentsAllowedByDefault, registry.AllowedByDefaultKey); !open {
		return nil
	}
	allowedByDefault := make([]string, 0, 1)
	if a, ok := p.live.Collections.Agents.Get(agentName); ok && a.Enabled && a.AllowByDefault {
		allowedByDefault = append(allowedByDefault, agentName)
	}
	return grant.NewPermit("agent_default", "agent_default", "default agent access", true, false, nil, nil, grant.WithAgentPermits(allowedByDefault))
}

// responseSucceeded reports whether result counts as a successful request. A passthrough call
// relays the upstream's error status as a response rather than an error, so it is judged by that
// status: a call the provider refused does not count against request limits, the same as a refused
// inference request. Only the request count reads this; tokens and cost a refused call reported
// are still charged.
func responseSucceeded(result *schemas.BifrostResponse) bool {
	if result == nil {
		return false
	}
	if pt := result.PassthroughResponse; pt != nil && pt.StatusCode >= 400 {
		return false
	}
	return true
}

// limitsForKeyThatRan narrows the limits settled for an alias to the target of the key that ran.
//
// The check runs before key selection, so it answers to the limits of every model the eligible keys
// map the alias to. Only one of them ran, so charging the rest would spend limits of models that
// never ran. When the settled limits carried alias targets for this (provider, model) and core
// reports the key that ran (see keyThatRan), the limits are gathered again with that key's targets alone, and the
// settled list is kept only where the two agree: nothing is charged that was not checked. Without
// alias targets, or without a reported key, the settled list is returned as is. A request naming no
// alias pays one context read.
func (p *GovernancePlugin) limitsForKeyThatRan(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgets, rateLimits []schemas.Limit) ([]schemas.Limit, []schemas.Limit) {
	settled, _ := ctx.Value(aliasLimitTargetsContext).(settledAliasTargets)
	if len(settled.targets) == 0 || settled.provider != provider || settled.model != model {
		return budgets, rateLimits
	}
	// The usual case: every eligible key maps the alias to one model, and the key that ran did.
	if ra := schemas.GetResolvedAlias(ctx); len(settled.targets) == 1 && ra != nil && ra.Config != nil && ra.Config.ModelID == settled.targets[0] {
		return budgets, rateLimits
	}
	keyID := keyThatRan(ctx)
	g := ctx.Grant()
	if keyID == "" || g == nil {
		return budgets, rateLimits
	}
	ctx.SetValue(chargedKeyContext, keyID)
	ranBudgets, ranRateLimits, err := p.store.GatherLimitsForRequestKind(ctx, g.Access(), provider, model, configstoreTables.LimitScopeModel)
	ctx.ClearValue(chargedKeyContext)
	if err != nil {
		return budgets, rateLimits
	}
	return limitsAlsoIn(budgets, ranBudgets), limitsAlsoIn(rateLimits, ranRateLimits)
}

// keyThatRan is the key of the attempt being charged: the selected key, or, once core has cleared it
// after a final error (which can still carry billed usage), the key the attempt trail's last record
// names. Empty when neither says.
func keyThatRan(ctx *schemas.BifrostContext) string {
	if keyID, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string); keyID != "" {
		return keyID
	}
	if trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord); len(trail) > 0 {
		return trail[len(trail)-1].KeyID
	}
	return ""
}

// limitsAlsoIn is the limits of settled whose IDs ran also names, in settled's order.
func limitsAlsoIn(settled, ran []schemas.Limit) []schemas.Limit {
	keep := make(map[string]bool, len(ran))
	for _, limit := range ran {
		keep[limit.ID] = true
	}
	out := make([]schemas.Limit, 0, len(settled))
	for _, limit := range settled {
		if keep[limit.ID] {
			out = append(out, limit)
		}
	}
	return out
}

// GenerateVirtualKey is a helper function
func GenerateVirtualKey() string {
	return VirtualKeyPrefix + uuid.NewString()
}
