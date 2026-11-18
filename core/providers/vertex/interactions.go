package vertex

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Vertex serves the Interactions API at
// https://{host}/v1beta1/projects/{project}/locations/{region}/interactions: v1 answers 404, and an
// API key is refused, so the key's service-account credentials mint an OAuth token. Google
// documents only the global location today; Bifrost sends the key's own region (global when none
// is set), so a regional key gets Google's answer for that region rather than a silent reroute.

const vertexInteractionsAPIVersion = "v1beta1"

// useVertexInteractions reports whether a Responses request runs on Interactions: the shared rule,
// limited to the models Vertex serves there (Gemini, and the agents).
func useVertexInteractions(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) bool {
	if !gemini.ShouldUseGeminiInteractions(ctx, request) {
		return false
	}
	return gemini.IsGeminiInteractionsAgentRequest(ctx, request) || schemas.IsGeminiModelFamily(ctx, request.Model)
}

// vertexInteractionsLocations are the locations that serve Interactions; Google answers 400 for any
// other.
var vertexInteractionsLocations = []string{"global", "us", "eu"}

// interactionsPlaceKeys returns key as it stands for each place its runs can live: its own project and
// region, then those its aliases override, in alias order. With model set, only the place that model runs.
func interactionsPlaceKeys(key schemas.Key, model string) []schemas.Key {
	if key.VertexKeyConfig == nil || len(key.Aliases) == 0 {
		return []schemas.Key{key}
	}
	at := func(alias *schemas.AliasConfig) schemas.Key {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Config: alias})
		config := *key.VertexKeyConfig
		config.ProjectID = *schemas.NewSecretVar(resolveVertexProjectID(ctx, key))
		config.Region = *schemas.NewSecretVar(resolveVertexRegion(ctx, key))
		placed := key
		placed.VertexKeyConfig = &config
		return placed
	}
	if model != "" {
		if alias := key.Aliases.ResolveConfig(model); alias != nil {
			return []schemas.Key{at(alias)}
		}
		return []schemas.Key{key}
	}
	places := []schemas.Key{key}
	names := make([]string, 0, len(key.Aliases))
	for name := range key.Aliases {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		alias := key.Aliases[name]
		placed := at(&alias)
		if !slices.ContainsFunc(places, func(p schemas.Key) bool {
			return interactionsProject(p) == interactionsProject(placed) && interactionsRegion(p) == interactionsRegion(placed)
		}) {
			places = append(places, placed)
		}
	}
	return places
}

// interactionsProject is the project key's calls go to, trimmed as the target trims it.
func interactionsProject(key schemas.Key) string {
	if key.VertexKeyConfig == nil {
		return ""
	}
	return strings.TrimSpace(key.VertexKeyConfig.ProjectID.GetValue())
}

// interactionsRegion is the location key's calls go to.
func interactionsRegion(key schemas.Key) string {
	if key.VertexKeyConfig != nil {
		if v := key.VertexKeyConfig.Region.GetValue(); v != "" {
			return v
		}
	}
	return "global" // vertexInteractionsCollectionURL's default
}

// InteractionsKeys narrows keys to those that can reach a stored interaction: in a location that serves
// Interactions, then in the location and project given ("" for any). A key counts where its alias for model
// sends it, or, for a call that names no model, in any place its aliases reach. A step no key passes is
// skipped, so Google answers for what is left rather than the call failing for want of a key. keys is not
// modified.
func InteractionsKeys(keys []schemas.Key, model, project, location string) []schemas.Key {
	narrow := func(keep func(schemas.Key) bool) {
		var kept []schemas.Key
		for _, key := range keys {
			if slices.ContainsFunc(interactionsPlaceKeys(key, model), keep) {
				kept = append(kept, key)
			}
		}
		if len(kept) > 0 {
			keys = kept
		}
	}
	serves := func(key schemas.Key) bool {
		return slices.Contains(vertexInteractionsLocations, interactionsRegion(key))
	}
	inLocation := func(key schemas.Key) bool {
		return serves(key) && (location == "" || interactionsRegion(key) == location)
	}
	narrow(serves)
	if location != "" {
		narrow(inLocation)
	}
	if project != "" {
		narrow(func(key schemas.Key) bool {
			return inLocation(key) && interactionsProject(key) == project
		})
	}
	return keys
}

// ContinuationKeys narrows keys to those that reach the stored run a continued turn names: Google keeps a run
// in one project and location, and the request says neither. When keys would send the turn to more than one,
// the run is read in each until one has it, so only such a request pays for the read. keys come back as they
// are when no place has the run, so Google answers for it, and are not modified.
func (provider *VertexProvider) ContinuationKeys(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, keys []schemas.Key) []schemas.Key {
	if len(keys) < 2 || request == nil || request.Params == nil || request.Params.PreviousResponseID == nil || *request.Params.PreviousResponseID == "" {
		return keys
	}
	type location struct{ project, region string }
	places := make([]schemas.Key, len(keys))
	locations := make([]location, len(keys))
	distinct := false
	for i, key := range keys {
		places[i] = interactionsPlaceKeys(key, request.Model)[0]
		locations[i] = location{interactionsProject(places[i]), interactionsRegion(places[i])}
		distinct = distinct || locations[i] != locations[0]
	}
	if !distinct {
		return keys
	}
	// The read is the provider's own: what it learns, and an alias a failed attempt left, stay off the request.
	readCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer readCtx.Cancel()
	asked := map[location]bool{}
	for i, key := range keys {
		if asked[locations[i]] || !slices.Contains(vertexInteractionsLocations, locations[i].region) {
			continue
		}
		readCtx.ClearValue(schemas.BifrostContextKeyResolvedAlias)
		resolved := *request
		if alias := key.Aliases.ResolveConfig(request.Model); alias != nil {
			resolved.Model = alias.ModelID
			readCtx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Config: alias})
		}
		if !useVertexInteractions(readCtx, &resolved) {
			continue
		}
		readCtx.ClearValue(schemas.BifrostContextKeyResolvedAlias) // the place already holds what it overrides
		target, bifrostErr := provider.interactionsTarget(readCtx, places[i])
		if bifrostErr != nil {
			continue
		}
		bifrostErr = provider.evictOnInteractionsAuthError(places[i], gemini.InteractionsExists(readCtx, target, *request.Params.PreviousResponseID))
		if bifrostErr == nil {
			kept := make([]schemas.Key, 0, len(keys))
			for k := range keys {
				if locations[k] == locations[i] {
					kept = append(kept, keys[k])
				}
			}
			return kept
		}
		if ctx.Err() != nil {
			return keys
		}
		// Another key in the same place may be let in where this one was refused.
		asked[locations[i]] = providerUtils.IsNotFoundError(bifrostErr)
	}
	return keys
}

// lookupInteraction runs a call on a stored run in each place key's runs can live until one has it. The
// call names no model, so the alias that made the run is not known. Google answers 404 in the others.
func lookupInteraction[T any](provider *VertexProvider, ctx *schemas.BifrostContext, key schemas.Key, call func(*gemini.GeminiInteractionsTarget) (T, *schemas.BifrostError)) (T, *schemas.BifrostError) {
	places := []schemas.Key{key}
	if schemas.GetResolvedAlias(ctx) == nil {
		places = servingInteractionsPlaces(interactionsPlaceKeys(key, ""))
	}
	result, _, bifrostErr := providerUtils.LookupAcrossKeys(ctx, places, func(place schemas.Key) (T, *schemas.BifrostError) {
		var zero T
		target, bifrostErr := provider.interactionsTarget(ctx, place)
		if bifrostErr != nil {
			return zero, bifrostErr
		}
		result, bifrostErr := call(target)
		return result, provider.evictOnInteractionsAuthError(place, bifrostErr)
	})
	return result, bifrostErr
}

// servingInteractionsPlaces drops the places whose location does not serve Interactions, unless none does.
func servingInteractionsPlaces(places []schemas.Key) []schemas.Key {
	var serving []schemas.Key
	for _, place := range places {
		if slices.Contains(vertexInteractionsLocations, interactionsRegion(place)) {
			serving = append(serving, place)
		}
	}
	if len(serving) == 0 {
		return places[:1]
	}
	return serving
}

// pollInteraction sends a passthrough GET of a stored run, as job accounting does, to each place key's runs
// can live, under the rules lookupInteraction follows. ok is false for any other request, and for a key with
// no place but its own.
func (provider *VertexProvider) pollInteraction(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest) (resp *schemas.BifrostPassthroughResponse, bifrostErr *schemas.BifrostError, ok bool) {
	if len(key.Aliases) == 0 || req == nil || req.Method != fasthttp.MethodGet || !strings.Contains(req.Path, "/interactions/") || schemas.GetResolvedAlias(ctx) != nil {
		return nil, nil, false
	}
	places := interactionsPlaceKeys(key, "")
	if len(places) < 2 {
		return nil, nil, false
	}
	var refused *schemas.BifrostPassthroughResponse
	for _, place := range servingInteractionsPlaces(places) {
		resp, bifrostErr = provider.passthroughOnce(ctx, place, req)
		if bifrostErr != nil || resp == nil {
			return resp, bifrostErr, true
		}
		switch resp.StatusCode {
		case fasthttp.StatusNotFound:
		case fasthttp.StatusUnauthorized, fasthttp.StatusForbidden, fasthttp.StatusTooManyRequests:
			refused = resp
		default:
			return resp, nil, true
		}
	}
	if refused != nil {
		return refused, nil, true // the run may sit behind the place that refused
	}
	return resp, nil, true
}

// interactionsTarget is the Vertex Interactions endpoint for key, authenticated with an OAuth token
// from the key's credentials.
func (provider *VertexProvider) interactionsTarget(ctx *schemas.BifrostContext, key schemas.Key) (*gemini.GeminiInteractionsTarget, *schemas.BifrostError) {
	projectID := strings.TrimSpace(resolveVertexProjectID(ctx, key))
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}
	target := &gemini.GeminiInteractionsTarget{
		CollectionURL:              vertexInteractionsCollectionURL(resolveVertexRegion(ctx, key), projectID),
		Headers:                    map[string]string{},
		ExtraHeaders:               provider.networkConfig.ExtraHeaders,
		Client:                     provider.client,
		StreamingClient:            provider.streamingClient,
		StreamIdleTimeoutInSeconds: provider.networkConfig.StreamIdleTimeoutInSeconds,
		ProviderName:               provider.GetProviderKey(),
		SendBackRawRequest:         providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		SendBackRawResponse:        providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		Logger:                     provider.logger,
		PollAPIVersion:             vertexInteractionsAPIVersion,
	}
	// A key with only an API key would fall through to the host's default credentials below.
	if key.Value.GetValue() != "" && vertexCredentialIdentity(key) == "" {
		return nil, providerUtils.NewConfigurationError("the interactions API on Vertex does not accept an API key; configure the key with OAuth credentials")
	}
	tokenSource, err := provider.getAuthTokenSource(key)
	if err != nil {
		provider.removeVertexClient(key)
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		provider.removeVertexClient(key)
		return nil, providerUtils.NewBifrostOperationError("error getting token", err)
	}
	target.Headers["Authorization"] = "Bearer " + token.AccessToken
	return target, nil
}

// evictOnInteractionsAuthError drops the key's cached token source when Google refused the call
// with 401 or 403, as the generateContent paths do, so the next call mints a fresh token instead of
// reusing the rejected one. It returns bifrostErr unchanged.
func (provider *VertexProvider) evictOnInteractionsAuthError(key schemas.Key, bifrostErr *schemas.BifrostError) *schemas.BifrostError {
	if bifrostErr != nil && bifrostErr.StatusCode != nil && (*bifrostErr.StatusCode == fasthttp.StatusUnauthorized || *bifrostErr.StatusCode == fasthttp.StatusForbidden) {
		provider.removeVertexClient(key)
	}
	return bifrostErr
}

// vertexInteractionsCollectionURL is the Interactions collection for a project and region (global
// when the key names none).
func vertexInteractionsCollectionURL(region, projectID string) string {
	if region == "" {
		region = "global"
	}
	return fmt.Sprintf("%s/interactions", getVertexProjectLocationURL(region, vertexInteractionsAPIVersion, projectID))
}

// setInteractionsServiceTier sends the service-tier header on an Interactions create under the same
// conditions as generateContent: a supported tier on the global endpoint, unless the provider's
// extra headers already set it.
func (provider *VertexProvider) setInteractionsServiceTier(ctx *schemas.BifrostContext, key schemas.Key, target *gemini.GeminiInteractionsTarget, request *schemas.BifrostResponsesRequest) {
	if request.Params == nil || request.Params.ServiceTier == nil {
		return
	}
	for name := range provider.networkConfig.ExtraHeaders {
		// Header names match in any case, as the executors apply them.
		if strings.EqualFold(name, VertexServiceTierHeader) {
			return
		}
	}
	region := resolveVertexRegion(ctx, key)
	if region == "" {
		region = "global" // vertexInteractionsCollectionURL's default
	}
	if v := vertexServiceTierHeaderValue(region, request.Model, *request.Params.ServiceTier); v != "" {
		target.Headers[VertexServiceTierHeader] = v
	}
}

func (provider *VertexProvider) interactionsResponses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	provider.setInteractionsServiceTier(ctx, key, target, request)
	resp, bifrostErr := gemini.InteractionsResponses(ctx, target, request)
	return resp, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

func (provider *VertexProvider) interactionsResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	provider.setInteractionsServiceTier(ctx, key, target, request)
	stream, bifrostErr := gemini.InteractionsResponsesStream(ctx, target, postHookRunner, postHookSpanFinalizer, request)
	return stream, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

// ResponsesRetrieve implements schemas.ResponsesLifecycleProvider (GET /interactions/{id}).
func (provider *VertexProvider) ResponsesRetrieve(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return lookupInteraction(provider, ctx, key, func(target *gemini.GeminiInteractionsTarget) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return gemini.InteractionsRetrieve(ctx, target, req)
	})
}

// ResponsesRetrieveStream implements schemas.ResponsesLifecycleProvider (GET
// /interactions/{id}?stream=true).
func (provider *VertexProvider) ResponsesRetrieveStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return lookupInteraction(provider, ctx, key, func(target *gemini.GeminiInteractionsTarget) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return gemini.InteractionsRetrieveStream(ctx, target, postHookRunner, postHookSpanFinalizer, req)
	})
}

// ResponsesDelete implements schemas.ResponsesLifecycleProvider (DELETE /interactions/{id}).
func (provider *VertexProvider) ResponsesDelete(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesDeleteRequest) (*schemas.BifrostResponsesDeleteResponse, *schemas.BifrostError) {
	return lookupInteraction(provider, ctx, key, func(target *gemini.GeminiInteractionsTarget) (*schemas.BifrostResponsesDeleteResponse, *schemas.BifrostError) {
		return gemini.InteractionsDelete(ctx, target, req)
	})
}

// ResponsesCancel implements schemas.ResponsesLifecycleProvider (POST /interactions/{id}/cancel).
func (provider *VertexProvider) ResponsesCancel(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesCancelRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return lookupInteraction(provider, ctx, key, func(target *gemini.GeminiInteractionsTarget) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return gemini.InteractionsCancel(ctx, target, req)
	})
}

// ResponsesInputItems implements schemas.ResponsesLifecycleProvider from the input steps of GET
// /interactions/{id}.
func (provider *VertexProvider) ResponsesInputItems(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesInputItemsRequest) (*schemas.BifrostResponsesInputItemsResponse, *schemas.BifrostError) {
	return lookupInteraction(provider, ctx, key, func(target *gemini.GeminiInteractionsTarget) (*schemas.BifrostResponsesInputItemsResponse, *schemas.BifrostError) {
		return gemini.InteractionsInputItems(ctx, target, req)
	})
}
