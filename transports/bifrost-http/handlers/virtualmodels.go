package handlers

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// Virtual models are Bifrost's own names, so Bifrost is the authority on their metadata:
// asking a backend about a name it has never heard of returns a 404, which reads to a
// client as "this model does not exist" and makes an SDK that validates against
// /v1/models reject a name that would have worked perfectly well.

// virtualModelDescriptor builds the OpenAI-shaped entry for a virtual model. It carries
// the virtual name as the id, so a client that lists models and then sends one of them
// sends something resolvable rather than something that only exists in an operator's head.
func virtualModelDescriptor(vm *schemas.VirtualModel) schemas.Model {
	ownedBy := string(vm.Provider)
	created := time.Now().Unix()
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
func appendVirtualModels(resp *schemas.BifrostListModelsResponse, cfg *lib.Config, provider schemas.ModelProvider, access schemas.Access) {
	if resp == nil || !virtualModelsEnabled(cfg) {
		return
	}
	seen := make(map[string]bool, len(resp.Data))
	for _, existing := range resp.Data {
		seen[strings.ToLower(existing.ID)] = true
	}
	for _, vm := range cfg.VirtualModels.List() {
		if provider != "" && vm.Provider != provider {
			continue
		}
		if !virtualModelAllowed(vm, access) {
			continue
		}
		if seen[strings.ToLower(vm.Name)] {
			continue
		}
		seen[strings.ToLower(vm.Name)] = true
		resp.Data = append(resp.Data, virtualModelDescriptor(vm))
	}
}

// virtualModelRetrieve answers GET /v1/models/{name} for a virtual name, synthesizing the
// metadata and probing the one backend that owns it so the answer says what is actually
// resident. Returns false when the name is not a virtual model, so the caller falls through
// to the normal upstream path untouched.
//
// The name arrives as it did in the path, so it may still carry a provider prefix. Both
// "qwen38" and "omlx/qwen38" address the same virtual model.
func virtualModelRetrieve(ctx *schemas.BifrostContext, cfg *lib.Config, pathName string, access schemas.Access) (*schemas.BifrostModelRetrieveResponse, bool) {
	if !virtualModelsEnabled(cfg) {
		return nil, false
	}
	name := strings.Trim(strings.TrimSpace(pathName), "/")
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	vm, ok := cfg.VirtualModels.Lookup(name)
	if !ok {
		if prefix, after, split := strings.Cut(name, "/"); split {
			vm, ok = cfg.VirtualModels.Lookup(after)
			if ok && prefix != string(vm.Provider) {
				return nil, true
			}
		}
	}
	if !ok {
		return nil, false
	}

	if !virtualModelAllowed(vm, access) {
		return nil, true
	}
	descriptor := virtualModelDescriptor(vm)
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
func virtualListedModels(cfg *lib.Config, allowed []schemas.ModelProvider, access schemas.Access) []listedModel {
	if !virtualModelsEnabled(cfg) {
		return nil
	}
	out := make([]listedModel, 0)
	for _, vm := range cfg.VirtualModels.List() {
		if !slices.Contains(allowed, vm.Provider) || !virtualModelAllowed(vm, access) {
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
// published ahead of the grants that cover them.
func (h *CompletionHandler) virtualModelAccess(ctx *schemas.BifrostContext) schemas.Access {
	if h.modelsManager == nil {
		return nil
	}
	access, err := h.modelsManager.ResolveAccess(ctx)
	if err != nil {
		return nil
	}
	return access
}

// resolveVirtualModelAccess is the same answer for the management listing, which reaches it
// through its own manager rather than the inference handler's.
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
func virtualModelAllowed(vm *schemas.VirtualModel, access schemas.Access) bool {
	if access == nil {
		return true
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
