package handlers

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Virtual models are Bifrost's own names, so Bifrost is the authority on their metadata:
// asking a backend about a name it has never heard of returns a 404, which reads to a
// client as "this model does not exist" and makes an SDK that validates against
// /v1/models reject a name that would have worked perfectly well.

// virtualModelDescriptor builds the OpenAI-shaped entry for a virtual model. It carries the virtual
// name as the id, so a client that lists models and then sends one of them sends something
// resolvable. created comes from the installed definition set, not the clock.
func virtualModelDescriptor(vm *schemas.VirtualModel, created int64) schemas.Model {
	ownedBy := string(vm.Provider)
	model := schemas.Model{
		ID:      vm.Name,
		OwnedBy: &ownedBy,
		Created: &created,
		AdditionalAttributes: map[string]string{
			"bifrost:virtual":          "true",
			"bifrost:provider":         string(vm.Provider),
			"bifrost:candidate-models": strings.Join(vm.Models, ","),
			"bifrost:on-probe-error":   string(vm.OnProbeError),
		},
	}
	if vm.Description != "" {
		description := vm.Description
		model.Description = &description
	}
	return model
}

// appendVirtualModels adds one entry per virtual model to a models listing, keeping the
// names the backend already reported ahead of them.
//
// Two deliberate limits. It does not probe: a listing that faned out to every provider
// per /v1/models call would turn a metadata request into a residency read of every GPU on
// the network, so residency belongs on the single-model route where one backend answers.
// And it appends after pagination rather than participating in it, because a virtual model
// is not a row in any provider's list to page over - it is configuration, and dropping it
// from page two onwards would make it look intermittent.
func appendVirtualModels(resp *schemas.BifrostListModelsResponse, cfg *lib.Config, provider schemas.ModelProvider, access schemas.Access, credentialPresented bool) {
	if resp == nil || !virtualModelsEnabled(cfg) {
		return
	}
	seen := make(map[string]bool, len(resp.Data))
	for _, existing := range resp.Data {
		seen[strings.ToLower(existing.ID)] = true
	}
	for _, vm := range cfg.VirtualModels.List() {
		if provider != "" && !strings.EqualFold(string(vm.Provider), string(provider)) {
			continue
		}
		if !virtualModelAllowed(vm, access, credentialPresented) {
			continue
		}
		if seen[strings.ToLower(vm.Name)] {
			continue
		}
		seen[strings.ToLower(vm.Name)] = true
		resp.Data = append(resp.Data, virtualModelDescriptor(vm, cfg.VirtualModels.Created()))
	}
}

// virtualModelRetrieve answers GET /v1/models/{name} for a virtual name, synthesizing the
// metadata and probing the one backend that owns it so the answer says what is actually
// resident. Returns false when the name is not a virtual model, so the caller falls through
// to the normal upstream path untouched.
//
// The name arrives as it did in the path, so it may still carry a provider prefix. Both
// "qwen38" and "omlx/qwen38" address the same virtual model.
//
// A provider the client named - by prefix, or by ?provider= which wins over the prefix exactly as
// modelRetrieveTarget reads them - outranks the overlay. If it is not the owning provider, the
// client wants that provider's own model of this name, which has to stay reachable, so the request
// falls through and that provider answers. Claiming it here 403'd a valid concrete lookup; 403 is
// for access denial only.
func virtualModelRetrieve(ctx *schemas.BifrostContext, cfg *lib.Config, pathName string, requestedProvider schemas.ModelProvider, access schemas.Access, credentialPresented bool) (*schemas.BifrostModelRetrieveResponse, bool) {
	if !virtualModelsEnabled(cfg) {
		return nil, false
	}
	name := strings.Trim(strings.TrimSpace(pathName), "/")
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	requested := strings.ToLower(strings.TrimSpace(string(requestedProvider)))
	vm, ok := cfg.VirtualModels.Lookup(name)
	if !ok {
		if prefix, after, split := strings.Cut(name, "/"); split {
			vm, ok = cfg.VirtualModels.Lookup(after)
			if ok && requested == "" {
				requested = strings.ToLower(strings.TrimSpace(prefix))
			}
		}
	}
	if !ok {
		return nil, false
	}
	// Case-insensitive: clients spell providers as they like, owners are stored lowercased.
	if requested != "" && !strings.EqualFold(requested, string(vm.Provider)) {
		return nil, false
	}

	if !virtualModelAllowed(vm, access, credentialPresented) {
		return nil, true
	}
	descriptor := virtualModelDescriptor(vm, cfg.VirtualModels.Created())
	if _, snapshot, err := cfg.VirtualModels.MemberStatus(ctx, vm.Name); err == nil && snapshot != nil {
		descriptor.AdditionalAttributes["bifrost:residency-authoritative"] = fmt.Sprintf("%t", snapshot.Authoritative)
		descriptor.AdditionalAttributes["bifrost:residency-observed-at"] = snapshot.ObservedAt.UTC().Format(time.RFC3339)
		states := make([]string, 0, len(vm.Models))
		resident := ""
		for _, candidate := range vm.Models {
			state := snapshot.StateOf(candidate)
			states = append(states, candidate+"="+string(state))
			if resident == "" && state == schemas.ResidencyReady {
				resident = candidate
			}
		}
		descriptor.AdditionalAttributes["bifrost:candidate-residency"] = strings.Join(states, ",")
		if resident != "" {
			descriptor.AdditionalAttributes["bifrost:resident-model"] = resident
		}
	}

	return &schemas.BifrostModelRetrieveResponse{Model: descriptor}, true
}

// virtualListedModels is the dashboard shape for virtual models. It takes the provider list the
// caller has already been narrowed to, rather than consulting the whole config, so a virtual key
// cannot be shown a model built on a provider it was not granted. An empty provider list shows
// nothing: there is no provider in play for the name to belong to.
//
// The search filter is applied here rather than by the caller: concrete names are narrowed by the
// same predicate a few lines up, and a search for one string that answered with every virtual name
// would be a filter that quietly does not cover half the list. Keeping it inside the function is
// what stops it being forgotten by the next call site.
func virtualListedModels(cfg *lib.Config, allowed []schemas.ModelProvider, access schemas.Access, credentialPresented bool, query string) []listedModel {
	if !virtualModelsEnabled(cfg) {
		return nil
	}
	out := make([]listedModel, 0)
	for _, vm := range cfg.VirtualModels.List() {
		if !slices.Contains(allowed, vm.Provider) || !virtualModelAllowed(vm, access, credentialPresented) {
			continue
		}
		if !matchesModelQuery(vm.Name, query) {
			continue
		}
		out = append(out, listedModel{Name: vm.Name, Provider: vm.Provider})
	}
	return out
}

// virtualModelAccess returns what the request may reach, or nil when nothing has been settled
// for it. Nil is the same reading the concrete models listing gives: no governance deployed, or no
// grant on a route that does not carry one. Authentication is settled upstream by the auth layer;
// this answers only what a settled caller may see, so a virtual name's candidates are never
// published ahead of the grants that cover them. Callers pair it with virtualKeyPresented to tell
// an unrestricted request apart from a refused one, which nil alone cannot express.
func (h *CompletionHandler) virtualModelAccess(ctx *schemas.BifrostContext) schemas.Access {
	return resolveVirtualModelAccess(ctx, h.modelsManager)
}

// resolveVirtualModelAccess is the same answer for callers that reach governance through their own
// manager rather than the inference handler's.
func resolveVirtualModelAccess(ctx *schemas.BifrostContext, manager ModelsManager) schemas.Access {
	if manager == nil {
		return nil
	}
	access, err := manager.ResolveAccess(ctx)
	if err != nil {
		return nil
	}
	return access
}

// virtualModelAllowed requires grants for every candidate before exposing its metadata.
//
// An unresolved access is two different cases and they must not share a default. A caller that
// presented no credential keeps the unfiltered reading the concrete listing gives, since
// authentication is the auth layer's job and these two routes do not run it. A caller that DID
// present one and resolved to nothing is not that caller: admitting them would publish a virtual
// name and its candidate layout to a credential governance refused, and the retrieve route would
// go on to spend a residency probe on it. So an unresolved presented credential is denied.
func virtualModelAllowed(vm *schemas.VirtualModel, access schemas.Access, credentialPresented bool) bool {
	if access == nil {
		return !credentialPresented
	}
	if !access.IsProviderAllowed(string(vm.Provider)) {
		return false
	}
	for _, model := range vm.Models {
		if !access.IsModelAllowed(string(vm.Provider), model) {
			return false
		}
	}
	return true
}

// virtualModelsEnabled guards every call site, so a gateway that does not use this feature
// pays nothing at all.
func virtualModelsEnabled(cfg *lib.Config) bool {
	return cfg != nil && cfg.VirtualModels != nil && cfg.VirtualModels.Enabled()
}

// virtualKeyPresented reports whether the request offered a virtual key. It reads the headers
// rather than the resolved grants because these routes run outside the auth middleware, so on
// them a presented key is exactly what the client says it presented. It reuses the canonical
// header parser the auth middleware uses, so "presented" means the same thing here as there.
func virtualKeyPresented(ctx *fasthttp.RequestCtx) bool {
	return ctx != nil && virtualKeyFromHeaders(ctx) != ""
}
