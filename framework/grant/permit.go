package grant

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Wildcard marks an unrestricted list: every value is allowed, or every value is blocked, depending
// on which kind of list carries it.
//
// The lists a permit carries are schemas.WhiteList and schemas.BlackList, so what a list means
// travels on its type: a list holding only the wildcard is unrestricted, an empty list holds
// nothing, and membership ignores case. Key IDs are the one exception: they are identifiers, so
// whoever matches them does so exactly rather than through the type's membership methods (see
// composeKeyIDs), and only their wildcard is read off the type.
const Wildcard = "*"

// PermitType identifies what kind of source a permit's access comes from. These are the values a
// schemas.Permit reports from Type. An open string: kinds are declared by whoever resolves permits
// of that kind, so this package needs no list of them.
type PermitType string

// The permit kinds this package names. The type stays open, so these are not the only kinds that
// can exist. They are the ones PrettyString can render, which is why they are declared here rather
// than wherever permits of each kind are resolved: a refusal has to read the same whichever kind it
// names.
const (
	// PermitVirtualKey marks permits whose access comes from a virtual key.
	PermitVirtualKey PermitType = "vk"
	// PermitAccessProfile marks permits whose access comes from a profile attached to a caller.
	PermitAccessProfile PermitType = "access_profile"
	// PermitProject marks permits whose access comes from a project a request names. A project is
	// not something the caller belongs to but something the request opts into, so it grants
	// alongside whatever the caller already holds rather than instead of it. PrettyString needs no
	// case for it: the identifier already reads as prose, which is the only reason the others have
	// one.
	PermitProject PermitType = "project"
)

// PrettyString names the permit kind as a refusal should say it. Refusals are read by whoever made
// the request, so "your virtual key has expired" is the answer and "vk" is not.
//
// A kind it does not know renders as itself. That is not a good message, but it is better than an
// empty one: a refusal that loses its subject cannot be acted on at all.
func (t PermitType) PrettyString() string {
	switch t {
	case PermitVirtualKey:
		return "virtual key"
	case PermitAccessProfile:
		return "access profile"
	default:
		return string(t)
	}
}

// Permit is the one implementation of schemas.Permit.
//
// It is a snapshot for one attempt. The provider permits carry a copy of the configuration they
// came from, so a source reloaded mid-attempt cannot change what that attempt has already resolved.
// A later attempt builds its own and picks the reload up.
type Permit struct {
	permitType PermitType
	id         string
	name       string
	isActive   bool
	isExpired  bool

	providerPermits []schemas.ProviderPermit
	mcpPermits      []schemas.MCPPermit
}

// NewPermit builds a Permit. See schemas.Permit for what each value means. The lists are deep
// copied, down to each entry's own slices and its Weight pointer, so a resolver that keeps its
// slices (or the value behind a Weight pointer) cannot alter what an attempt was admitted under.
func NewPermit(
	permitType PermitType,
	id string,
	name string,
	isActive bool,
	isExpired bool,
	providerPermits []schemas.ProviderPermit,
	mcpPermits []schemas.MCPPermit,
) *Permit {
	return &Permit{
		permitType:      permitType,
		id:              id,
		name:            name,
		isActive:        isActive,
		isExpired:       isExpired,
		providerPermits: cloneProviderPermits(providerPermits),
		mcpPermits:      cloneMCPPermits(mcpPermits),
	}
}

// cloneProviderPermits deep-copies each entry: slices.Clone on the outer slice only copies the
// struct values, and AllowedModels, BlacklistedModels, KeyIDs, and the value behind Weight would
// otherwise still alias the source's own slices and pointer.
func cloneProviderPermits(providerPermits []schemas.ProviderPermit) []schemas.ProviderPermit {
	cloned := slices.Clone(providerPermits)
	for i := range cloned {
		cloned[i].AllowedModels = slices.Clone(cloned[i].AllowedModels)
		cloned[i].BlacklistedModels = slices.Clone(cloned[i].BlacklistedModels)
		cloned[i].KeyIDs = slices.Clone(cloned[i].KeyIDs)
		if cloned[i].Weight != nil {
			weight := *cloned[i].Weight
			cloned[i].Weight = &weight
		}
	}
	return cloned
}

// cloneMCPPermits deep-copies each entry's Tools slice, for the same reason cloneProviderPermits
// copies each entry's nested slices.
func cloneMCPPermits(mcpPermits []schemas.MCPPermit) []schemas.MCPPermit {
	cloned := slices.Clone(mcpPermits)
	for i := range cloned {
		cloned[i].Tools = slices.Clone(cloned[i].Tools)
	}
	return cloned
}

// Type implements schemas.Permit.
func (p *Permit) Type() string {
	if p == nil {
		return ""
	}
	return string(p.permitType)
}

// ID implements schemas.Permit.
func (p *Permit) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

// Name implements schemas.Permit.
func (p *Permit) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

// IsActive implements schemas.Permit.
func (p *Permit) IsActive() bool {
	return p != nil && p.isActive
}

// IsExpired implements schemas.Permit.
func (p *Permit) IsExpired() bool {
	return p != nil && p.isExpired
}

// ProviderPermits implements schemas.Permit. A copy, not the permit's own slice: the permit is a
// snapshot for the whole attempt, read by every consumer that asks what it permits, and a caller
// mutating what it got back must not change what the next reader sees.
func (p *Permit) ProviderPermits() []schemas.ProviderPermit {
	if p == nil {
		return nil
	}
	return cloneProviderPermits(p.providerPermits)
}

// MCPPermits implements schemas.Permit. See ProviderPermits for why this returns a copy.
func (p *Permit) MCPPermits() []schemas.MCPPermit {
	if p == nil {
		return nil
	}
	return cloneMCPPermits(p.mcpPermits)
}

// The rules below are written against schemas.Permit rather than *Permit, so the fold asks every
// permit it holds the same questions whichever implementation answers them.

// isNilPermit reports whether p is nothing at all, including an interface holding a nil pointer.
func isNilPermit(p schemas.Permit) bool {
	return isNil(p)
}

// allowsProvider reports whether the permit permits provider at all. A permit with no provider
// permit permits none (deny by default), and so does a permit that is not there.
func allowsProvider(p schemas.Permit, provider string) bool {
	if isNilPermit(p) {
		return false
	}
	for _, pp := range p.ProviderPermits() {
		if pp.Provider == provider {
			return true
		}
	}
	return false
}

// blacklistsModel reports whether any of the permit's provider permits for provider blocks model.
// One blocking provider permit blocks the provider for that model outright.
func blacklistsModel(p schemas.Permit, provider string, model string) bool {
	if isNilPermit(p) {
		return false
	}
	for _, pp := range p.ProviderPermits() {
		if pp.Provider == provider && pp.BlacklistedModels.IsBlocked(model) {
			return true
		}
	}
	return false
}

// allowsTool reports whether the permit permits toolPattern. The MCP permit that holds a client
// decides for that client: a client with a permit here is never widened by another permit of the
// same source.
//
// A client is identified by its stable id, the same as mcpEntries: a rename can leave two entries
// for the same client under different names, and the first one still has to be the one that
// decides, not whichever one the pattern's current name happens to match.
func allowsTool(p schemas.Permit, toolPattern string) bool {
	if isNilPermit(p) {
		return false
	}
	handledClients := make(map[string]struct{})
	for _, mp := range p.MCPPermits() {
		clientName := mp.ClientName
		if clientName == "" {
			continue
		}
		clientKey := mp.Client
		if clientKey == "" {
			clientKey = clientName
		}
		if _, handled := handledClients[clientKey]; handled {
			continue
		}
		handledClients[clientKey] = struct{}{}
		if toolPattern != clientName+"-"+Wildcard && !strings.HasPrefix(toolPattern, clientName+"-") {
			continue
		}
		if toolPattern == clientName+"-"+Wildcard {
			return len(mp.Tools) > 0
		}
		if mp.Tools.IsUnrestricted() {
			return true
		}
		return mp.Tools.Contains(strings.TrimPrefix(toolPattern, clientName+"-"))
	}
	return false
}

// allowsModelByName is the name-matching counterpart of Access.permitAllowsModel, for the coarse
// provider gates that must not resolve model names.
//
// blacklistsModel is checked first, across every provider-permit entry for provider, before any
// entry gets to allow the model: a permit can hold more than one provider-permit entry for the
// same provider (two provider configs on one key, for instance), and one entry allowing what a
// later entry blacklists must not let the model through first.
func allowsModelByName(p schemas.Permit, provider string, model string) bool {
	if isNilPermit(p) {
		return false
	}
	if model != "" && blacklistsModel(p, provider, model) {
		return false
	}
	for _, pp := range p.ProviderPermits() {
		if pp.Provider != provider {
			continue
		}
		if model == "" {
			return true
		}
		if providerPermitAllowsModel(&pp, model) {
			return true
		}
	}
	return false
}

// providerPermitAllowsModel applies one provider permit's model rules by name only, resolving
// nothing. An empty model means there is nothing to filter on, which keeps the permit.
func providerPermitAllowsModel(pp *schemas.ProviderPermit, model string) bool {
	if model == "" {
		return true
	}
	return pp.AllowedModels.IsAllowed(model) && !pp.BlacklistedModels.IsBlocked(model)
}

// weightedProviderPermitFor returns the permit's first provider permit for provider that sets a
// weight, or nil when none does.
func weightedProviderPermitFor(p schemas.Permit, provider string) *schemas.ProviderPermit {
	if isNilPermit(p) {
		return nil
	}
	pps := p.ProviderPermits()
	for i := range pps {
		pp := &pps[i]
		if pp.Provider == provider && pp.Weight != nil {
			return pp
		}
	}
	return nil
}

// providerPermitFor returns the permit's first provider permit for provider, or nil when it holds
// none.
func providerPermitFor(p schemas.Permit, provider string) *schemas.ProviderPermit {
	if isNilPermit(p) {
		return nil
	}
	pps := p.ProviderPermits()
	for i := range pps {
		if pps[i].Provider == provider {
			return &pps[i]
		}
	}
	return nil
}

// eachProviderPermit visits the permit's provider permits. A provider named by nothing but
// whitespace is skipped: no comparison anywhere would match it, so it could only ever be selected
// and then fail downstream. visit returns false to stop, which this reports back.
func eachProviderPermit(p schemas.Permit, visit func(pp *schemas.ProviderPermit) bool) bool {
	if isNilPermit(p) {
		return true
	}
	pps := p.ProviderPermits()
	for i := range pps {
		pp := &pps[i]
		if strings.TrimSpace(pp.Provider) == "" {
			continue
		}
		if !visit(pp) {
			return false
		}
	}
	return true
}

// mcpEntries returns the tool patterns a permit permits, in MCP permit order, with duplicates
// collapsed. The first MCP permit holding a client decides for that client.
func mcpEntries(p schemas.Permit) []string {
	if isNilPermit(p) {
		return []string{}
	}
	mps := p.MCPPermits()
	entries := newUniqueEntries(len(mps))
	handledClients := make(map[string]struct{})
	for _, mp := range mps {
		if mp.ClientName == "" {
			continue
		}
		clientKey := mp.Client
		if clientKey == "" {
			clientKey = mp.ClientName
		}
		if _, handled := handledClients[clientKey]; handled {
			continue
		}
		handledClients[clientKey] = struct{}{}
		if len(mp.Tools) == 0 {
			continue
		}
		if mp.Tools.IsUnrestricted() {
			entries.add(mp.ClientName + "-" + Wildcard)
			continue
		}
		for _, tool := range mp.Tools {
			if tool == "" {
				continue
			}
			entries.add(mp.ClientName + "-" + tool)
		}
	}
	return entries.list()
}
