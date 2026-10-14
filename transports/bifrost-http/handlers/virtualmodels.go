package handlers

import (
	"fmt"
	"net/url"
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
func appendVirtualModels(resp *schemas.BifrostListModelsResponse, cfg *lib.Config, provider schemas.ModelProvider) {
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
func virtualModelRetrieve(ctx *schemas.BifrostContext, cfg *lib.Config, pathName string) (*schemas.BifrostModelRetrieveResponse, bool) {
	if !virtualModelsEnabled(cfg) {
		return nil, false
	}
	name := strings.Trim(strings.TrimSpace(pathName), "/")
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	vm, ok := cfg.VirtualModels.Lookup(name)
	if !ok {
		if _, after, split := strings.Cut(name, "/"); split {
			vm, ok = cfg.VirtualModels.Lookup(after)
		}
	}
	if !ok {
		return nil, false
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

// virtualListedModels is the dashboard shape for virtual models. It takes the already
// RBAC-filtered provider list rather than consulting the whole config, so a virtual key
// can never be shown a virtual model built on a provider it was not granted - the same
// deny-by-default that governs the concrete models beside it.
func virtualListedModels(cfg *lib.Config, allowed []schemas.ModelProvider) []listedModel {
	if !virtualModelsEnabled(cfg) {
		return nil
	}
	out := make([]listedModel, 0)
	for _, vm := range cfg.VirtualModels.List() {
		if len(allowed) > 0 {
			granted := false
			for _, provider := range allowed {
				if provider == vm.Provider {
					granted = true
					break
				}
			}
			if !granted {
				continue
			}
		}
		out = append(out, listedModel{Name: vm.Name, Provider: vm.Provider})
	}
	return out
}

// virtualModelsEnabled guards every call site, so a gateway that does not use this feature
// pays nothing at all.
func virtualModelsEnabled(cfg *lib.Config) bool {
	return cfg != nil && cfg.VirtualModels != nil && cfg.VirtualModels.Enabled()
}
