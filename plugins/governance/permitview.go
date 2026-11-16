package governance

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

// permitViewTTL is how long a permit view is kept. A view leaves early when a provider, MCP client
// or virtual MCP changes; the TTL is what removes the view of a set no key holds anymore, and it
// makes a live set's view be built again once per TTL.
const permitViewTTL = 10 * time.Minute

// permitView is what a grant set confers, built once and shared by every key that holds the set
// until a provider, MCP client or virtual MCP changes or permitViewTTL passes. Nothing in it is
// changed once built: a permit copies what it takes.
type permitView struct {
	providers         []schemas.ProviderPermit
	mcp               []schemas.MCPPermit
	agents            []string
	relays            []string
	allowAllProviders bool
	// unnamedClients are the MCP clients the set grants tools of that have no configured name. Each
	// permit built from the view warns about them under its own key's name.
	unnamedClients []string
	// virtualMCPs are the enabled virtual MCPs the set assigns, which each permit built from the
	// view records as addressable for its request.
	virtualMCPs []*tables.TableVirtualMCP
}

// permitForVirtualKey builds the permit a virtual key confers from its grant set's view: one
// provider permit per configured provider, and one MCP permit per MCP client the key may execute
// tools of: its own MCP configs, plus the clients allowed by default. A client the key configures
// explicitly is never widened by the allowed-by-default clients: the explicit config decides,
// including when it grants no tool at all.
//
// The result is a snapshot for one attempt: the permit copies what it takes from the view, so a
// key reloaded mid-attempt cannot change what that attempt has already resolved. A later attempt
// builds its own and picks the reload up. What funds the key is not here: see HolderLimits and
// PermitProviderLimits, which read it by the permit's identity when an attempt's provider is known.
func (gs *LocalGovernanceStore) permitForVirtualKey(ctx context.Context, vk *registry.VirtualKey) *grant.Permit {
	if vk == nil {
		return nil
	}
	view := gs.permitViewOf(vk.Grants)
	for _, def := range view.virtualMCPs {
		// A key's direct assignment makes the vMCP addressable at /mcp/<slug>.
		RecordGrantedVirtualMCP(ctx, def)
	}
	for _, clientID := range view.unnamedClients {
		WarnUnnamedMCPClient(gs.logger, "virtual key", vk.Name, clientID)
	}
	return grant.NewPermit(grant.PermitVirtualKey, vk.ID, vk.Name, vk.Active, vk.IsExpiredAt(time.Now()), view.providers, view.mcp,
		grant.WithAgentPermits(view.agents), grant.WithAllowAllProviders(view.allowAllProviders),
		grant.WithRelayPermits(view.relays))
}

// permitViewOf returns the view of the grant set id names, building it when the table holds none
// from the current generation. The table stores a view for permitViewTTL under the generation read
// before it was built, so a view built while a change bumped the generation is built again on the
// next read. A set the store does not hold grants nothing, not even the MCP clients allowed by
// default, which the key's own set may have restricted or blocked, and its empty view is not kept.
func (gs *LocalGovernanceStore) permitViewOf(id registry.GrantSetID) *permitView {
	return gs.permitViews.GetOrCompute(livestore.Hash(id), func() (*permitView, time.Duration, bool) {
		set, ok := gs.live.Collections.GrantSet(id)
		if !ok {
			return &permitView{}, 0, false
		}
		return gs.buildPermitView(set), permitViewTTL, true
	})
}

// buildPermitView computes what set confers against the providers, MCP clients and virtual MCPs the
// live store holds now.
func (gs *LocalGovernanceStore) buildPermitView(set *registry.GrantSet) *permitView {
	v := &permitView{allowAllProviders: set.AllowAllProviders}
	allowedByDefaultClients := MCPClientsAllowedByDefault(gs.live)

	v.providers = make([]schemas.ProviderPermit, 0, len(set.Providers))
	for i := range set.Providers {
		p := &set.Providers[i]
		v.providers = append(v.providers, schemas.ProviderPermit{
			Provider:          p.Provider,
			AllowedModels:     p.AllowedModels,
			BlacklistedModels: p.BlacklistedModels,
			KeyIDs:            p.KeyIDs,
			Weight:            p.Weight,
		})
	}
	if set.AllowAllProviders {
		v.providers = AppendAllProviderPermits(v.providers, ConfiguredProviderNames(gs.live))
	}

	// A key's own MCP configs and its Virtual MCPs build one accumulator, so a Virtual MCP grants
	// exactly like a config: owns the clients it names, unions per client, blocks allowed-by-default.
	acc := NewMCPToolAccumulator(len(set.MCP) + len(set.VirtualMCPIDs))
	for i := range set.MCP {
		acc.addMCPGrant(&set.MCP[i])
	}
	for _, id := range set.VirtualMCPIDs {
		def := gs.virtualMCPByID(id)
		if def == nil || !def.Enabled {
			continue
		}
		acc.AddVirtualMCP(def)
		v.virtualMCPs = append(v.virtualMCPs, def)
	}
	v.mcp, v.unnamedClients = MCPPermitsOf(acc, MCPClientNames(gs.live))
	v.mcp = AppendMCPPermitsAllowedByDefault(v.mcp, acc.ConfiguredClients(), allowedByDefaultClients)

	v.agents = make([]string, 0, len(set.Agents))
	v.agents = append(v.agents, set.Agents...)
	v.relays = make([]string, 0, len(set.Relays))
	v.relays = append(v.relays, set.Relays...)
	return v
}
