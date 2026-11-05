package grant

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// CompositionMode is how a scoping permit combines with the permits the caller holds. These are
// the values a schemas.Access reports from Mode.
type CompositionMode string

const (
	// Intersect permits what the caller's permits and the scoping permit both permit.
	Intersect CompositionMode = "intersect"
	// Union permits what either of them permits.
	Union CompositionMode = "union"
)

// ModelMatcher decides whether model, as the request names it, is one of allowed on provider.
// Whoever resolves a request's access supplies it, because deciding that needs the deployment's own
// view of the provider: how it names models, and which provider-prefixed or cross-provider entries
// in allowed stand for the same model. Without one, entries are matched by name.
type ModelMatcher func(provider, model string, allowed []string) bool

// Access is the one implementation of schemas.Access: the fold of the permits a request carries.
//
// Per attempt rather than per request, because a request that fails over resolves again for each
// provider it tries. Configuration can change between two slow calls, and the attempt running
// second has to answer to what is in force when it runs.
//
// The caller's permits are a list because a caller can hold several at once, and they are read as
// one, as if they were a single permit holding all of their provider and MCP permits: what any of
// them permits, the caller may reach; any key any of them allows may serve the request; and every
// one of them that permits the pair pays for it (see PermitsForModel). Their order is the order the
// resolver gave them, and is the order candidates and refusals are reported in.
//
// There is one scoping slot, not a list. A request is scoped by at most one thing at a time, and
// composing under one mode is what keeps every answer here decidable.
//
// Ceilings are layers of permits that cap the caller's permits without granting anything of their
// own. Each layer is read as one, the way the caller's permits are, and the caller's side permits
// only what its permits and every layer permit. They bound the caller's side before the scoping
// permit composes with it, so a scope keeps its own mode whatever caps the caller. A ceiling never
// offers a provider candidate and never widens a key restriction; it can only take away.
type Access struct {
	bases    []schemas.Permit
	ceilings [][]schemas.Permit
	scoping  schemas.Permit
	mode     CompositionMode
	matcher  ModelMatcher
}

// AccessOption configures an Access beyond what every resolver supplies.
type AccessOption func(*Access)

// WithCeilings caps the caller's permits with layers of permits, outermost last. Nil permits are
// dropped, and so is a layer left with none: a layer with nothing in it caps nothing rather than
// refusing everything, so a resolver that found no cap for one level does not have to say so.
func WithCeilings(layers ...[]schemas.Permit) AccessOption {
	return func(a *Access) {
		for _, layer := range layers {
			held := make([]schemas.Permit, 0, len(layer))
			for _, permit := range layer {
				if !isNilPermit(permit) {
					held = append(held, permit)
				}
			}
			if len(held) > 0 {
				a.ceilings = append(a.ceilings, held)
			}
		}
	}
}

// NewAccess folds the permits the caller holds together with the permit scoping this request,
// under one composition mode.
//
// A request composes under exactly one mode. Choosing it belongs to whoever resolved the permits:
// that layer knows what it asked and what answered. Passing no scoping permit leaves the mode
// irrelevant and the answer is the caller's permits alone. Nil entries in bases are dropped, so a
// resolver that found nothing for one source does not have to say so twice. Ceilings, when a
// resolver has any, are passed as an option (see WithCeilings).
func NewAccess(bases []schemas.Permit, scoping schemas.Permit, mode CompositionMode, matcher ModelMatcher, opts ...AccessOption) *Access {
	held := make([]schemas.Permit, 0, len(bases))
	for _, base := range bases {
		if !isNilPermit(base) {
			held = append(held, base)
		}
	}
	if isNilPermit(scoping) {
		scoping = nil
	}
	access := &Access{
		bases:   held,
		scoping: scoping,
		mode:    mode,
		matcher: matcher,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(access)
		}
	}
	return access
}

// Bases implements schemas.Access.
func (a *Access) Bases() []schemas.Permit {
	if a == nil {
		return nil
	}
	return slices.Clone(a.bases)
}

// Ceilings implements schemas.Access.
func (a *Access) Ceilings() [][]schemas.Permit {
	if a == nil || len(a.ceilings) == 0 {
		return nil
	}
	layers := make([][]schemas.Permit, 0, len(a.ceilings))
	for _, layer := range a.ceilings {
		layers = append(layers, slices.Clone(layer))
	}
	return layers
}

// Scoping implements schemas.Access.
func (a *Access) Scoping() schemas.Permit {
	if a == nil {
		return nil
	}
	return a.scoping
}

// Mode implements schemas.Access.
func (a *Access) Mode() string {
	if a == nil {
		return ""
	}
	return string(a.mode)
}

// IsScoped implements schemas.Access.
func (a *Access) IsScoped() bool {
	return a != nil && a.scoping != nil
}

// IsProviderAllowed implements schemas.Access.
func (a *Access) IsProviderAllowed(provider string) bool {
	if a == nil {
		return false
	}
	base := a.callerAllows(func(p schemas.Permit) bool { return allowsProvider(p, provider) })
	if a.scoping == nil {
		return base
	}
	return a.compose(base, allowsProvider(a.scoping, provider))
}

// IsModelAllowed implements schemas.Access.
func (a *Access) IsModelAllowed(provider string, model string) bool {
	if a == nil {
		return false
	}
	base := a.callerAllows(func(p schemas.Permit) bool { return a.permitAllowsModel(p, provider, model) })
	if a.scoping == nil {
		return base
	}
	return a.compose(base, a.permitAllowsModel(a.scoping, provider, model))
}

// AllowsEveryModel implements schemas.Access.
func (a *Access) AllowsEveryModel(provider string) bool {
	if a == nil {
		return false
	}
	base := a.callerAllows(func(p schemas.Permit) bool { return permitAllowsEveryModel(p, provider) })
	if a.scoping == nil {
		return base
	}
	return a.compose(base, permitAllowsEveryModel(a.scoping, provider))
}

// permitAllowsEveryModel reports whether the permit permits every model on provider. Its provider
// permits for the provider are read as a union, as permitAllowsModel reads them: one unrestricted
// entry is enough, while a blacklist on any entry is decisive (blacklistsModel blocks the model
// whatever another entry allows). A provider the permit lists no permit for is every-model only
// under allow-all, as in permitAllowsModel.
func permitAllowsEveryModel(p schemas.Permit, provider string) bool {
	if isNilPermit(p) {
		return false
	}
	found, unrestricted := false, false
	for _, pp := range p.ProviderPermits() {
		if pp.Provider != provider {
			continue
		}
		found = true
		if len(pp.BlacklistedModels) > 0 {
			return false
		}
		if pp.AllowedModels.IsUnrestricted() {
			unrestricted = true
		}
	}
	if !found {
		return p.AllowsAllProviders()
	}
	return unrestricted
}

// IsMCPToolAllowed implements schemas.Access.
func (a *Access) IsMCPToolAllowed(toolPattern string) bool {
	if a == nil || toolPattern == "" {
		return false
	}
	base := a.callerAllows(func(p schemas.Permit) bool { return allowsTool(p, toolPattern) })
	if a.scoping == nil {
		return base
	}
	return a.compose(base, allowsTool(a.scoping, toolPattern))
}

// IsAgentAllowed implements schemas.Access.
func (a *Access) IsAgentAllowed(agentName string) bool {
	if a == nil || agentName == "" {
		return false
	}
	base := a.callerAllows(func(p schemas.Permit) bool { return allowsAgent(p, agentName) })
	if a.scoping == nil {
		return base
	}
	return a.compose(base, allowsAgent(a.scoping, agentName))
}

// IsRelayAllowed implements schemas.Access.
func (a *Access) IsRelayAllowed(endpointName string) bool {
	if a == nil || endpointName == "" {
		return false
	}
	base := a.callerAllows(func(p schemas.Permit) bool { return allowsRelay(p, endpointName) })
	if a.scoping == nil {
		return base
	}
	return a.compose(base, allowsRelay(a.scoping, endpointName))
}

// PermitsForModel implements schemas.Access.
func (a *Access) PermitsForModel(provider string, model string) []schemas.Permit {
	if a == nil || !a.IsModelAllowed(provider, model) {
		return nil
	}
	permits := make([]schemas.Permit, 0, len(a.bases)+1)
	// A caller side a ceiling refuses is not what the request proceeds on, so none of it is named:
	// only a union scope can have admitted the pair, and the scope answers for it alone.
	allowsPair := func(p schemas.Permit) bool { return a.permitAllowsModel(p, provider, model) }
	if a.ceilingsAllow(allowsPair) {
		for _, base := range a.bases {
			if allowsPair(base) {
				permits = append(permits, base)
			}
		}
		// A ceiling the request proceeds under answers for it too: its holder's money for the pair is
		// spent by everything it caps, so it is named alongside the caller's permits.
		for _, layer := range a.ceilings {
			for _, permit := range layer {
				if allowsPair(permit) {
					permits = append(permits, permit)
				}
			}
		}
	}
	if a.scoping != nil {
		permits = append(permits, a.scoping)
	}
	return permits
}

// KeysForModel implements schemas.Access.
//
// The caller's side is the union of every key restriction its permitting permits hold for the
// provider, across all of their provider permits for it: the caller's permits are read as one, and
// a request served by any of them may use any key any of them allows. That is composed with the
// scoping permit's restriction only where the scoping permit also authorizes the pair. A scoping
// permit that narrows a request to two of the provider's keys has to mean it, and taking the
// caller's list instead would hand the request a key the scope refused; but a permit the request is
// not proceeding on has no say, which is why the model is asked for.
//
// The result is a plain []string rather than any list type of the permit's own: it is handed
// straight to consumers that type-assert []string, where a named slice type would fail the
// assertion silently and read as "no restriction at all".
func (a *Access) KeysForModel(provider string, model string) (keyIDs []string, restricted bool) {
	if a == nil || !a.IsModelAllowed(provider, model) {
		// The request cannot proceed on this provider, so there is no key restriction to report.
		return nil, false
	}
	var callerKeys schemas.WhiteList
	callerHolds := false
	// A caller side that does not permit the pair - none of its permits does, or a ceiling refuses
	// it - is not what the request proceeds on: only a union scope can have admitted it, and then the
	// scope's restriction is the whole of it.
	if a.callerAllows(func(p schemas.Permit) bool { return a.permitAllowsModel(p, provider, model) }) {
		callerKeys, callerHolds = a.unionKeysHeldBy(a.bases, provider, model)
		callerKeys, callerHolds = a.capKeys(callerKeys, callerHolds, provider, model)
	}
	var scopeKeys schemas.WhiteList
	scopeHolds := false
	if a.scoping != nil {
		scopeKeys, scopeHolds = a.unionKeysHeldBy([]schemas.Permit{a.scoping}, provider, model)
	}
	var granted schemas.WhiteList
	switch {
	case callerHolds && scopeHolds:
		granted = composeKeyIDs(callerKeys, scopeKeys, a.mode)
	case callerHolds:
		granted = callerKeys
	case scopeHolds:
		// The request proceeds on the scoping permit alone, so its restriction is the whole of it.
		granted = scopeKeys
	default:
		// Neither side holds a provider permit, so there is no restriction to report. That is a
		// different answer from a permit whose restriction happens to name no key, which must not
		// read as "every key is allowed", so it is decided here, on whether a provider permit was
		// found at all, rather than on whether the list came back empty.
		return nil, false
	}
	if granted.IsUnrestricted() {
		return nil, false
	}
	// A copy, and always non-nil: a consumer must not be able to edit the permit through the answer,
	// and an empty restriction allows no key, which is not "no restriction at all".
	keyIDs = make([]string, 0, len(granted))
	keyIDs = append(keyIDs, granted...)
	return keyIDs, true
}

// capKeys narrows the caller's key restriction by every ceiling layer that restricts the provider's
// keys for the pair. A layer holding no provider permit for the provider says nothing about its keys
// and leaves the restriction as it was; a layer that does hold one intersects with it, and becomes
// the whole restriction when the caller's permits held none.
func (a *Access) capKeys(keys schemas.WhiteList, holds bool, provider string, model string) (schemas.WhiteList, bool) {
	for _, layer := range a.ceilings {
		layerKeys, layerHolds := a.unionKeysHeldBy(layer, provider, model)
		if !layerHolds {
			continue
		}
		if !holds {
			keys, holds = layerKeys, true
			continue
		}
		keys = composeKeyIDs(keys, layerKeys, Intersect)
	}
	return keys, holds
}

// unionKeysHeldBy is the union of the key restrictions that permits which authorize model on
// provider hold for that provider, and whether any of them holds one at all. A permit that does not
// authorize the pair has no say.
func (a *Access) unionKeysHeldBy(permits []schemas.Permit, provider string, model string) (keys schemas.WhiteList, holds bool) {
	for _, permit := range permits {
		if !a.permitAllowsModel(permit, provider, model) {
			continue
		}
		for _, pp := range permit.ProviderPermits() {
			if pp.Provider != provider {
				continue
			}
			holds = true
			keys = composeKeyIDs(keys, pp.KeyIDs, Union)
		}
	}
	return keys, holds
}

// ProvidersForModel implements schemas.Access. Model names resolve through the matcher here. Unlike
// GrantedProvidersForModel, this is the decision itself rather than a gate over one, so cross-provider
// naming and provider-prefixed entries must resolve exactly as the deployment resolves them.
func (a *Access) ProvidersForModel(model string) []schemas.ProviderCandidate {
	if a == nil || model == "" {
		return nil
	}

	candidates := make([]schemas.ProviderCandidate, 0, a.providerPermitCount())
	a.eachProviderPermit(func(owner schemas.Permit, pp *schemas.ProviderPermit, isBase bool) bool {
		provider := pp.Provider
		// Three separate questions, none of which implies another. Whether the composed access
		// permits the pair at all:
		if !a.IsModelAllowed(provider, model) {
			return false
		}
		// Whether *this* permit permits it: under a union the other side may be the one authorizing
		// the request, and a permit that blacklists the model must not serve it. One blacklisting
		// provider permit blocks the provider for that model across the whole permit, so a
		// permissive one cannot reopen it:
		if blacklistsModel(owner, provider, model) {
			return false
		}
		// And whether this provider permit in particular permits it, so a permit holding several for
		// a provider only offers the ones that can serve the model:
		if !a.permitsModel(pp, model) {
			return false
		}
		// And, for the caller's permits, whether every ceiling permits it: a capped-out candidate can
		// only have been admitted by a union scope, which then serves the provider itself.
		if isBase && !a.ceilingsAllow(func(p schemas.Permit) bool { return a.permitAllowsModel(p, provider, model) }) {
			return false
		}
		// Weight does not compose, because it is a preference rather than a permission and there is
		// no meaningful intersection of two preferences. Instead the scoping permit wins where it
		// expresses one: it is the more specific context, so a scope that wants a provider preferred
		// differently inside it says so and is obeyed. Where it expresses none, the candidate's own
		// weight stands.
		selectedWeight := pp.Weight
		if isBase && a.scoping != nil {
			if scoped := weightedProviderPermitFor(a.scoping, provider); scoped != nil {
				selectedWeight = scoped.Weight
			}
		}
		// Only the permits that actually permit model on this provider have a say. A permit that is
		// not authorizing this request does not get to restrict which of the provider's keys serve
		// it; under a union that is the whole point, since the request is proceeding on the other
		// side's authority alone. So the composition is the exception here, not the rule: the
		// candidate's own keys stand unless the scoping permit both authorizes the model and holds
		// the provider. A candidate offered through the scoping permit alone composes with nothing,
		// because it is offered only where none of the caller's permits could serve the model. Per
		// candidate the keys are this provider permit's own; KeysForModel is where the caller's
		// permits are read as one.
		selectedKeyIDs := pp.KeyIDs
		// The caller's candidates are capped by every ceiling layer that restricts the provider's
		// keys. The verdict above already established each layer permits the pair.
		if isBase {
			selectedKeyIDs = a.capCandidateKeys(selectedKeyIDs, provider, model)
		}
		if isBase && a.scoping != nil && a.permitAllowsModel(a.scoping, provider, model) {
			if scoped := providerPermitFor(a.scoping, provider); scoped != nil {
				selectedKeyIDs = composeKeyIDs(pp.KeyIDs, scoped.KeyIDs, a.mode)
			}
		}
		// A copy, as in KeysForModel: selectedKeyIDs is the permit's own slice whenever nothing composed
		// it, and a consumer editing the candidate must not edit the permit through it.
		candidates = append(candidates, schemas.ProviderCandidate{
			Provider: provider,
			Weight:   selectedWeight,
			KeyIDs:   slices.Clone(selectedKeyIDs),
		})
		return true
	})

	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// capCandidateKeys narrows one candidate's own keys by every ceiling layer holding a key restriction
// for the provider, as capKeys does for the caller's permits read as one.
func (a *Access) capCandidateKeys(keyIDs schemas.WhiteList, provider string, model string) schemas.WhiteList {
	for _, layer := range a.ceilings {
		if layerKeys, layerHolds := a.unionKeysHeldBy(layer, provider, model); layerHolds {
			keyIDs = composeKeyIDs(keyIDs, layerKeys, Intersect)
		}
	}
	return keyIDs
}

// GrantedProvidersForModel implements schemas.Access. Because this gate cannot ask ProvidersForModel
// anything, the composition it needs is re-derived by name below.
func (a *Access) GrantedProvidersForModel(model string) []string {
	if a == nil {
		return nil
	}

	allowed := make([]string, 0, a.providerPermitCount())
	if a.IsScoped() && a.mode != Union && a.mode != Intersect {
		// Unrecognized mode: permit nothing, as everywhere else.
		return allowed
	}
	intersecting := a.IsScoped() && a.mode == Intersect

	seen := make(map[string]struct{})
	a.eachProviderPermit(func(_ schemas.Permit, pp *schemas.ProviderPermit, isBase bool) bool {
		if _, dup := seen[pp.Provider]; dup {
			return true
		}
		if !providerPermitAllowsModel(pp, model) {
			return false
		}
		// Every ceiling caps the caller's permits, and only theirs: a provider gained through a union
		// scope is the scope's grant.
		if isBase && !a.ceilingsAllow(func(p schemas.Permit) bool { return allowsModelByName(p, pp.Provider, model) }) {
			return false
		}
		if intersecting {
			// A scoping permit cannot widen an intersection, and what the caller holds must also be
			// permitted by the scoping permit.
			if !isBase || !allowsModelByName(a.scoping, pp.Provider, model) {
				return false
			}
		}
		seen[pp.Provider] = struct{}{}
		allowed = append(allowed, pp.Provider)
		return true
	})
	return allowed
}

// eachProviderPermit visits the provider permits the request could be served from: every provider
// permit of every one of the caller's permits, in order, then the scoping permit's for providers
// none of them could serve. The coarse provider gate and candidate selection walk it identically
// rather than each keeping its own copy of the rule.
//
// The caller's permits do not shadow one another: they are read as one permit, and within one
// permit two provider permits for the same provider are both offered, which is what lets a holder
// run a provider under two weights or key sets. Only the scoping permit is shadowed, and only for
// providers the caller's permits actually served, which accept reports. Shadowing on merely
// *holding* a provider would lose it whenever the caller holds it but cannot serve this particular
// request; under a union that is a request the access permits, through the scoping permit, with
// nothing left to serve it.
func (a *Access) eachProviderPermit(accept func(owner schemas.Permit, pp *schemas.ProviderPermit, isBase bool) bool) {
	served := make(map[string]struct{})
	for _, base := range a.bases {
		eachProviderPermit(base, func(pp *schemas.ProviderPermit) bool {
			if accept(base, pp, true) {
				served[pp.Provider] = struct{}{}
			}
			return true
		})
	}
	eachProviderPermit(a.scoping, func(pp *schemas.ProviderPermit) bool {
		if _, done := served[pp.Provider]; done {
			return true
		}
		accept(a.scoping, pp, false)
		return true
	})
}

// providerPermitCount is how many provider permits the request holds across every permit, for
// sizing results.
func (a *Access) providerPermitCount() int {
	count := 0
	for _, base := range a.bases {
		count += len(base.ProviderPermits())
	}
	if a.scoping != nil {
		count += len(a.scoping.ProviderPermits())
	}
	return count
}

// MCPToolIncludeList implements schemas.Access.
//
// The caller's permits are read as one, so their entries are merged in order before the scoping
// permit is composed on: a tool any of them grants, the caller may execute.
func (a *Access) MCPToolIncludeList() []string {
	if a == nil {
		return nil
	}

	merged := newUniqueEntries(0)
	for _, base := range a.bases {
		for _, entry := range mcpEntries(base) {
			merged.add(entry)
		}
	}
	base := merged.list()
	for _, layer := range a.ceilings {
		base = intersectMCPEntries(base, layer, layerMCPEntries(layer))
	}
	if !a.IsScoped() {
		return base
	}
	return a.composeMCPEntries(base, mcpEntries(a.scoping))
}

// NarrowMCPToolIncludeList implements schemas.Access.
//
// A "<client>-*" wildcard is kept as a wildcard only when the access grants that client every
// tool, because downstream a wildcard reads as every tool of the client; otherwise it is replaced by
// the specific tools the access grants for that client, which may be none. A specific entry is kept
// when the access permits it. Empty entries and repeats are dropped. No access at all permits
// nothing.
func (a *Access) NarrowMCPToolIncludeList(requested []string) []string {
	kept := newUniqueEntries(len(requested))
	granted := a.MCPToolIncludeList()
	grantedSet := make(map[string]struct{}, len(granted))
	for _, entry := range granted {
		grantedSet[entry] = struct{}{}
	}
	for _, entry := range requested {
		if entry == "" {
			continue
		}
		clientName, isWildcard := strings.CutSuffix(entry, "-"+Wildcard)
		if !isWildcard {
			if a.IsMCPToolAllowed(entry) {
				kept.add(entry)
			}
			continue
		}
		if _, everyTool := grantedSet[entry]; everyTool {
			kept.add(entry)
			continue
		}
		for _, grantedEntry := range granted {
			if strings.HasPrefix(grantedEntry, clientName+"-") {
				kept.add(grantedEntry)
			}
		}
	}
	return kept.list()
}

// composeMCPEntries folds the caller's tool patterns with the scoping permit's under the request's
// mode, the counterpart of compose for verdicts and composeKeyIDs for provider keys. An
// unrecognized mode permits nothing, as everywhere else.
//
// The scoping permit is read off the receiver rather than passed in, because scoped has to be that
// permit's own expansion: narrowing asks the permit questions its expansion cannot answer, so a
// caller free to supply entries from elsewhere could quietly widen access.
func (a *Access) composeMCPEntries(base, scoped []string) []string {
	switch a.mode {
	case Union:
		merged := newUniqueEntries(len(base) + len(scoped))
		for _, entry := range base {
			merged.add(entry)
		}
		for _, entry := range scoped {
			merged.add(entry)
		}
		return merged.list()
	case Intersect:
		return intersectMCPEntries(base, []schemas.Permit{a.scoping}, scoped)
	}
	return []string{}
}

// intersectMCPEntries keeps what both the caller's tool patterns and a set of narrowing permits,
// read as one, permit. scoped has to be those permits' own expansion.
//
// A wildcard is passed through only when both sides hold one, because downstream a "<client>-*"
// entry reads as every tool of that client. Which side carries the wildcard decides how the other
// is consulted, and this is why narrowing needs the permits and not just their entries: an
// unrestricted client expands to a bare "<client>-*", so testing a specific entry for membership in
// that expansion would drop every tool the narrowing permits in fact permit. The permits are asked
// instead. In the other direction there is nothing to ask: a wildcard has to be replaced by the
// narrowing side's specific entries for the client, which only the expansion lists.
func intersectMCPEntries(base []string, narrowing []schemas.Permit, scoped []string) []string {
	scopedSet := make(map[string]struct{}, len(scoped))
	for _, entry := range scoped {
		scopedSet[entry] = struct{}{}
	}
	kept := newUniqueEntries(len(base))
	for _, entry := range base {
		clientName, isWildcard := strings.CutSuffix(entry, "-"+Wildcard)
		if !isWildcard {
			if anyPermitAllowsTool(narrowing, entry) {
				kept.add(entry)
			}
			continue
		}
		if _, unrestrictedOnBothSides := scopedSet[entry]; unrestrictedOnBothSides {
			kept.add(entry)
			continue
		}
		for _, scopedEntry := range scoped {
			if strings.HasPrefix(scopedEntry, clientName+"-") {
				kept.add(scopedEntry)
			}
		}
	}
	return kept.list()
}

// anyPermitAllowsTool reports whether any of permits permits toolPattern, reading them as one.
func anyPermitAllowsTool(permits []schemas.Permit, toolPattern string) bool {
	for _, permit := range permits {
		if allowsTool(permit, toolPattern) {
			return true
		}
	}
	return false
}

// layerMCPEntries merges the tool patterns of a ceiling layer's permits in order, the way the
// caller's permits are merged: a layer is read as one permit.
func layerMCPEntries(layer []schemas.Permit) []string {
	merged := newUniqueEntries(0)
	for _, permit := range layer {
		for _, entry := range mcpEntries(permit) {
			merged.add(entry)
		}
	}
	return merged.list()
}

// DeniedPermitsForModel implements schemas.Access.
//
// What is named is whatever refused and whose permission would have changed the answer. The
// caller's permits are read as one, so when none of them permits the pair all of them refused and
// all are named: a caller holding two profiles is told that neither permits the model, not that one
// of them does not. Under an intersection, when one of them permits it and the scoping permit does
// not, the scoping permit alone stands in the way and alone is named. Under a union both sides must
// have refused, and both are named. A caller holding no permit at all has nothing to be named.
func (a *Access) DeniedPermitsForModel(provider string, model string) []schemas.Permit {
	if a == nil {
		return nil
	}
	return a.deniedBy(func(p schemas.Permit) bool { return a.permitAllowsModel(p, provider, model) })
}

// DeniedPermitsForAgent implements schemas.Access.
func (a *Access) DeniedPermitsForAgent(agentName string) []schemas.Permit {
	if a == nil {
		return nil
	}
	return a.deniedBy(func(p schemas.Permit) bool { return allowsAgent(p, agentName) })
}

// DeniedPermitsForRelay implements schemas.Access.
func (a *Access) DeniedPermitsForRelay(endpointName string) []schemas.Permit {
	if a == nil {
		return nil
	}
	return a.deniedBy(func(p schemas.Permit) bool { return allowsRelay(p, endpointName) })
}

// DeniedPermitsForMCPTool implements schemas.Access.
func (a *Access) DeniedPermitsForMCPTool(toolPattern string) []schemas.Permit {
	if a == nil {
		return nil
	}
	return a.deniedBy(func(p schemas.Permit) bool { return allowsTool(p, toolPattern) })
}

// deniedBy identifies which permits refused, given a verdict per permit.
//
// A ceiling refusing what the caller's permits allow is named in their place: it alone stood in the
// way on the caller's side. Every layer that refused is named, outermost last.
func (a *Access) deniedBy(permits func(schemas.Permit) bool) []schemas.Permit {
	baseAllows := a.anyBase(permits)
	var refusingCeilings []schemas.Permit
	if baseAllows {
		refusingCeilings = a.refusingCeilings(permits)
		baseAllows = len(refusingCeilings) == 0
	}
	if a.scoping == nil {
		if baseAllows {
			return nil
		}
		if len(refusingCeilings) > 0 {
			return refusingCeilings
		}
		return a.refusingBases()
	}
	scopingAllows := permits(a.scoping)
	if a.compose(baseAllows, scopingAllows) {
		return nil
	}
	if baseAllows {
		return []schemas.Permit{a.scoping}
	}
	refused := refusingCeilings
	if len(refused) == 0 {
		refused = a.refusingBases()
	}
	if a.mode == Union && !scopingAllows {
		refused = append(refused, a.scoping)
	}
	return refused
}

// refusingBases is every permit the caller holds, for naming when none of them permitted. Nil when
// they hold none.
func (a *Access) refusingBases() []schemas.Permit {
	if len(a.bases) == 0 {
		return nil
	}
	return slices.Clone(a.bases)
}

// refusingCeilings is every permit of every ceiling layer that none of whose permits satisfies
// permits. Nil when every layer allows.
func (a *Access) refusingCeilings(permits func(schemas.Permit) bool) []schemas.Permit {
	var refused []schemas.Permit
	for _, layer := range a.ceilings {
		if !anyPermit(layer, permits) {
			refused = append(refused, layer...)
		}
	}
	return refused
}

// callerAllows reports whether the caller's side satisfies permits: some permit the caller holds
// does, and every ceiling layer allows it too.
func (a *Access) callerAllows(permits func(schemas.Permit) bool) bool {
	return a.anyBase(permits) && a.ceilingsAllow(permits)
}

// ceilingsAllow reports whether every ceiling layer has a permit satisfying permits. True when
// nothing caps the caller.
func (a *Access) ceilingsAllow(permits func(schemas.Permit) bool) bool {
	for _, layer := range a.ceilings {
		if !anyPermit(layer, permits) {
			return false
		}
	}
	return true
}

// anyPermit reports whether any permit in layer satisfies permits.
func anyPermit(layer []schemas.Permit, permits func(schemas.Permit) bool) bool {
	for _, permit := range layer {
		if permits(permit) {
			return true
		}
	}
	return false
}

// anyBase reports whether any of the caller's permits satisfies permits. The caller's permits are
// read as one everywhere, and this is the one place that reading is written down.
func (a *Access) anyBase(permits func(schemas.Permit) bool) bool {
	for _, base := range a.bases {
		if permits(base) {
			return true
		}
	}
	return false
}

// compose applies the request's composition mode between the two sides' verdicts. It is only
// meaningful with a scoping permit; callers answer the unscoped case with the caller's permits
// alone. An unrecognized mode permits nothing.
func (a *Access) compose(baseAllows, scopingAllows bool) bool {
	switch a.mode {
	case Union:
		return baseAllows || scopingAllows
	case Intersect:
		return baseAllows && scopingAllows
	}
	return false
}

// composeKeyIDs folds two key restrictions under a composition mode. Order follows the first
// argument, so the result is stable. Key IDs are identifiers, so membership here is exact rather
// than through the list type's case-folding methods; only the wildcard is read off the type.
//
// The wildcard is the universe rather than an entry: it stands for every key the provider has, so
// intersecting with it yields the other side untouched and unioning with it is unrestricted. An
// unrecognized mode permits nothing, as everywhere else.
func composeKeyIDs(first, second schemas.WhiteList, mode CompositionMode) schemas.WhiteList {
	switch mode {
	case Intersect:
		if first.IsUnrestricted() {
			return second
		}
		if second.IsUnrestricted() {
			return first
		}
		shared := make(schemas.WhiteList, 0, len(first))
		for _, keyID := range first {
			if slices.Contains(second, keyID) {
				shared = append(shared, keyID)
			}
		}
		return shared
	case Union:
		if first.IsUnrestricted() || second.IsUnrestricted() {
			return schemas.WhiteList{Wildcard}
		}
		merged := make(schemas.WhiteList, 0, len(first)+len(second))
		merged = append(merged, first...)
		for _, keyID := range second {
			if !slices.Contains(merged, keyID) {
				merged = append(merged, keyID)
			}
		}
		return merged
	}
	return []string{}
}

// permitAllowsModel reports whether the permit permits model on provider: blacklisted by no
// provider permit of that provider, and allowed by at least one. A permit that is not there permits
// nothing. An empty model asks about the provider alone.
func (a *Access) permitAllowsModel(p schemas.Permit, provider string, model string) bool {
	if isNilPermit(p) {
		return false
	}
	if model == "" {
		return allowsProvider(p, provider)
	}
	if blacklistsModel(p, provider, model) {
		return false
	}
	pps := p.ProviderPermits()
	found := false
	for i := range pps {
		pp := &pps[i]
		if pp.Provider != provider {
			continue
		}
		found = true
		if a.permitsModel(pp, model) {
			return true
		}
	}
	// A provider the permit lists keeps its own model rules even under allow-all; only a provider it
	// lists no permit for is opened up by allow-all, and then every model of it is allowed.
	if !found && p.AllowsAllProviders() {
		return true
	}
	return false
}

// permitsModel applies one provider permit's allowed-models rule, through the matcher when there is
// one and by name otherwise.
func (a *Access) permitsModel(pp *schemas.ProviderPermit, model string) bool {
	if a.matcher == nil {
		return pp.AllowedModels.IsAllowed(model)
	}
	return a.matcher(pp.Provider, model, pp.AllowedModels)
}

// Compile-time checks that this package implements the shapes core declares.
var (
	_ schemas.Grant    = (*Grant)(nil)
	_ schemas.Identity = (*Identity)(nil)
	_ schemas.Permit   = (*Permit)(nil)
	_ schemas.Access   = (*Access)(nil)
	_ schemas.Limits   = (*Limits)(nil)
)
