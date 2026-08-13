// Package grants models access as grants rather than as a property of one entity.
//
// A grant is a named bundle of provider and MCP permissions. Each request folds the grant its
// caller holds together with the grant scoping it into a single EffectiveAccess value, and
// every consumer — access checks, key selection, load balancing, model listings, MCP tool
// filtering — reads that one answer instead of walking entity configs itself.
//
// What this package holds is the model and the fold, not where grants come from. Nothing here
// reads a virtual key, a user, or any other holder: which grants a request carries is decided
// by whoever resolves it, and a request carrying none has no effective access at all — which
// leaves its consumers on the behavior they had before grants existed.
package grants

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Wildcard marks an unrestricted list ("all values allowed").
const Wildcard = "*"

// ProviderConfigGrant is permission to use a provider, with the model and key
// restrictions that come with it. It mirrors the semantics of a virtual key's
// provider config, expressed independently of any particular source.
type ProviderConfigGrant struct {
	Provider          string            // provider name, as configured
	AllowedModels     schemas.WhiteList // ["*"] allows all models; empty allows none (deny-by-default)
	BlacklistedModels schemas.BlackList // blocked models; wins over AllowedModels
	KeyIDs            schemas.WhiteList // ["*"] allows all keys of the provider; empty allows none.
	//                                     Composes with the other grant's list, but only where that
	//                                     grant also authorizes the request for the provider — a
	//                                     grant the request is not proceeding on does not get to say
	//                                     which keys serve it (see KeysFor and ProvidersFor).
	//                                     Matching key IDs is the consumer's job and is exact — do
	//                                     not route it through WhiteList.Contains, which folds case.
	Weight *float64 // load-balancing weight; nil means the provider is not a
	//                  load-balancing candidate. Unlike the permissions, this does not compose:
	//                  there is no meaningful intersection of two preferences, so a scoping grant
	//                  that expresses one wins as the more specific context (see ProvidersFor).

	// Budgets and RateLimits are what this config alone is funded by — the limits that apply to
	// this provider and no other. They sit here rather than in one list on the grant because that
	// is the whole of what discriminates between providers: load balancing asks which provider
	// still has room, and a limit that covers every provider answers the same for all of them, so
	// it cannot help choose and does not belong in the question.
	Budgets    []Limit
	RateLimits []Limit
}

// MCPConfigGrant is permission to execute tools of one MCP client.
type MCPConfigGrant struct {
	Client string // stable client identifier; identifies the client across renames
	//                and decides which grant applies to a client
	ClientName string            // client name, as used in "<client>-<tool>" tool patterns
	Tools      schemas.WhiteList // ["*"] allows every tool of the client; empty allows none
}

// GrantType identifies what kind of entity a grant's access comes from. It is an
// open string: kinds are declared by whoever resolves grants of that kind, so this
// package needs no list of them.
type GrantType string

// The grant kinds this package names. The type stays open, so these are not the only kinds that can
// exist — they are the ones PrettyString can render, which is why they are declared here rather than
// wherever grants of each kind are resolved. A refusal has to read the same whichever kind it names.
const (
	// GrantTypeVirtualKey marks grants whose access comes from a virtual key.
	GrantTypeVirtualKey GrantType = "vk"
	// GrantTypeAccessProfile marks grants whose access comes from a profile attached to a caller.
	GrantTypeAccessProfile GrantType = "access_profile"
	// GrantTypeUnrestricted marks the access a request has when nothing granted it anything — no
	// credential was presented and no profile answers for the caller. It names no holder: there is
	// nobody to attribute it to, and nothing about it to refuse.
	GrantTypeUnrestricted GrantType = "unrestricted"
)

// PrettyString names the grant kind as a refusal should say it. Refusals are read by whoever made the
// request, so "your virtual key has expired" is the answer and "vk" is not.
//
// A kind it does not know renders as itself. That is not a good message, but it is better than an empty
// one: a refusal that loses its subject cannot be acted on at all.
func (gt GrantType) PrettyString() string {
	switch gt {
	case GrantTypeVirtualKey:
		return "virtual key"
	case GrantTypeAccessProfile:
		return "access profile"
	case GrantTypeUnrestricted:
		return "unrestricted access"
	default:
		return string(gt)
	}
}

// LimitHolderKind identifies what kind of entity a limit hangs off. An open string, like GrantType, so
// a deployment resolving a holder of its own can name it without this package changing — but the kinds
// that do exist are named below rather than wherever each is resolved, because telling one limit from
// another is a single vocabulary and a reader should not have to know which layer introduced a kind.
type LimitHolderKind string

// Every kind of limit holder, declared together.
//
// Once resolved for an attempt, a request's limits are one flat list, and these are what tell them
// apart afterwards: a refusal has to say whether it was your key's budget or your team's, and a
// caller looking at several limits of the same shape needs to know which is which. They live in one
// block because that telling-apart is a single vocabulary — a reader deciding what a kind means, or
// adding one, should not have to know which layer happened to introduce it.
//
// Nothing registers or enumerates them. Enforcement covers every kind, so a limit carrying any of
// these is checked and charged by the same path, and a deployment that resolves a holder of its own
// declares its kind here and is enforced by construction.
const (
	// Held by something in the organization, from the narrowest outwards. A provider config's budget
	// is spent only by requests that config serves; the holder's own by every request it makes; and a
	// team's, business unit's or customer's by every request made under anything they contain.
	LimitHolderVirtualKey                  LimitHolderKind = "vk"
	LimitHolderVirtualKeyProviderConfig    LimitHolderKind = "vk_provider_config"
	LimitHolderAccessProfile               LimitHolderKind = "access_profile"
	LimitHolderAccessProfileProviderConfig LimitHolderKind = "access_profile_provider_config"
	LimitHolderTeam                        LimitHolderKind = "team"
	LimitHolderBusinessUnit                LimitHolderKind = "business_unit"
	LimitHolderCustomer                    LimitHolderKind = "customer"

	// Held by no one in the organization. A provider's limits are the deployment's own, and a model
	// config's are scoped to a (model, provider) pattern — neither is anybody's allowance, so a refusal
	// naming one of these is telling the caller about the deployment rather than about themselves.
	LimitHolderProvider    LimitHolderKind = "provider"
	LimitHolderModelConfig LimitHolderKind = "model_config"

	// A model config can also be scoped to whoever set it, and each scope is its own kind. The key's and
	// the user's are separate because a caller can be told to stop counting a request against the key
	// that granted it while still counting it against the user who made it — one kind covering both
	// would make that unexpressible.
	LimitHolderVirtualKeyModelConfig LimitHolderKind = "vk_model_config"
	// LimitHolderUserModelConfig covers two different origins that share the user's scope: a per-model
	// limit assigned to the user directly, and one a deployment derived from something the user holds
	// and stored against them.
	//
	// They are not the same money and they do not share a lifecycle — detaching whatever produced the
	// derived one removes it, while the directly assigned one survives — but at this level they are
	// indistinguishable, so a refusal naming this kind cannot say which of the two ran out. Telling them
	// apart needs whatever recorded the derivation, which is the deployment's to know, not this
	// package's; a deployment that wants the distinction in its refusals declares a kind for it.
	LimitHolderUserModelConfig LimitHolderKind = "user_model_config"
)

// Limit is one budget or one rate limit a request answers to: what to load when enforcing it, and
// whose it is.
//
// ID is an identifier, never the record. A budget's usage is live and replaced in place as
// requests spend, so a copy taken when the grant was built would answer from a balance that has
// since moved; whoever enforces a limit loads it by ID at the moment it enforces.
//
// HolderKind, HolderID and HolderName say where the limit came from, because a refusal has to be
// able to name what refused — "the key's monthly budget" and "your team's" are different answers
// to the user, and a bare identifier cannot tell them apart. HolderID also keeps two limits of
// the same kind distinct, which matters when one holder carries several.
//
// Which requests a limit applies to is not recorded on it. It is settled by where the limit comes
// from — a provider config's own limits fund that provider, a model config's fund that model, a
// holder's fund everything under it — and by when it is gathered, since a request can fail over to
// another provider or have its model re-resolved by a routing rule. So a limit that has been gathered
// for an attempt is one to enforce; there is nothing further to match it against.
type Limit struct {
	ID string

	HolderKind LimitHolderKind
	HolderID   string
	HolderName string

	// What the limit was gathered for, when it is scoped to either. Descriptive only — nothing
	// selects on them — so that a limit which is recorded on a log row or named in a refusal can say
	// what it was scoped to.
	Provider string
	Model    string
}

// Grant is a named bundle of access, and of what pays for exercising it.
//
// (Type, ID) is a grant's identity: it names the entity whose access this is, and is what
// attribution refers to. Name is for display in denial messages and logs — renaming an entity
// changes Name, never its identity.
//
// What pays for it is here only where it is per-provider — on each provider config, which is what
// lets load balancing tell one provider from another. What funds the holder whichever provider serves
// is deliberately absent: it answers the same for every provider, so a grant is not where it belongs
// and asking a grant for it would invite asking once per candidate.
type Grant struct {
	Type                 GrantType
	ID                   string
	Name                 string
	IsActive             bool
	IsExpired            bool
	ProviderConfigGrants []ProviderConfigGrant
	MCPConfigGrants      []MCPConfigGrant
}

// isUnrestricted reports whether the grant permits what it does not enumerate — read from the type,
// which is the only thing that says so, rather than from a second field that could disagree with it.
//
// What a grant does enumerate still answers normally: an unrestricted grant carries a real provider
// config per provider the deployment has, so every rule about providers, models and keys applies to it
// unchanged. This covers the two things enumeration cannot settle — a provider the deployment has not
// configured, and MCP clients, which a grant names one at a time.
func (g *Grant) isUnrestricted() bool {
	return g != nil && g.Type == GrantTypeUnrestricted
}

// UnrestrictedGrant is the access a request carries when nothing granted it anything: every provider,
// every model, every key, every MCP tool.
//
// It exists so a request nobody authorised still has an access to be governed through, instead of being
// the one shape every consumer has to special-case.
//
// It enumerates nothing, and does not need to. Every question a consumer asks of it is already answered
// correctly by an empty grant of this type: what it permits comes from the type, and what it restricts is
// nothing — a key restriction it does not name is no restriction, and a provider list it does not narrow
// is one a consumer must leave alone rather than narrow to the empty set. Listing the deployment's
// providers here would only be a second copy of something the deployment already knows.
func UnrestrictedGrant() *Grant {
	return &Grant{Type: GrantTypeUnrestricted, IsActive: true}
}

// BudgetsFor returns the budgets that funding this grant's use of provider draws on, and
// RateLimitsFor the rate limits. Only the ones configured against that provider: what a holder is
// funded by across every provider is not the grant's to answer, because it is not a fact about which
// provider serves the request.
//
// Order is as configured, so a caller enforcing all of them reports the first refusal it hits and one
// picking a single limit sorts by Specificity.
func (g *Grant) BudgetsFor(provider string) []Limit {
	return g.configLimits(provider, func(pc *ProviderConfigGrant) []Limit { return pc.Budgets })
}

// RateLimitsFor is BudgetsFor for rate limits.
func (g *Grant) RateLimitsFor(provider string) []Limit {
	return g.configLimits(provider, func(pc *ProviderConfigGrant) []Limit { return pc.RateLimits })
}

// configLimits gathers one kind of limit from every config this grant holds for provider. Every
// config, not the first: two configs for one provider are funded separately, and a request served by
// that provider draws on both.
func (g *Grant) configLimits(provider string, of func(pc *ProviderConfigGrant) []Limit) []Limit {
	if g == nil || provider == "" {
		return nil
	}
	var limits []Limit
	for i := range g.ProviderConfigGrants {
		pc := &g.ProviderConfigGrants[i]
		if pc.Provider != provider {
			continue
		}
		limits = append(limits, of(pc)...)
	}
	return limits
}

// LimitsHeldBy names the limits one holder imposes, from the identifiers of the records that enforce
// them. provider and model are recorded on each for description, empty when the limit is scoped to
// neither; see Limit for why nothing selects on them.
//
// Identifiers rather than records, for the reason on Limit, and it is also what keeps this package
// out of the business of what a budget row looks like: whoever reads those rows already knows their
// shape, and all this needs from them is an identity. Records without one are skipped, since a
// limit that cannot be loaded cannot be enforced.
func LimitsHeldBy(kind LimitHolderKind, holderID, holderName, provider, model string, ids ...string) []Limit {
	if len(ids) == 0 {
		return nil
	}
	limits := make([]Limit, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		limits = append(limits, Limit{
			ID:         id,
			HolderKind: kind,
			HolderID:   holderID,
			HolderName: holderName,
			Provider:   provider,
			Model:      model,
		})
	}
	if len(limits) == 0 {
		return nil
	}
	return limits
}

// LimitsFrom keeps the limits held by any of kinds, and is how a caller that enforces one holder's
// limits asks about exactly that holder.
//
// It exists because "does anything govern this?" and "does this holder govern this?" are different
// questions, and a grant now carries limits from several holders at once. A check written against
// one holder — a key's budgets, say — must gate on that holder's own limits, or it fires on a
// team's budget and then reports on the key's, which passes while the team is exhausted. Passing
// no kinds keeps nothing, since asking about no holder is asking about nothing.
func LimitsFrom(limits []Limit, kinds ...LimitHolderKind) []Limit {
	if len(limits) == 0 || len(kinds) == 0 {
		return nil
	}
	held := make([]Limit, 0, len(limits))
	for _, limit := range limits {
		if slices.Contains(kinds, limit.HolderKind) {
			held = append(held, limit)
		}
	}
	if len(held) == 0 {
		return nil
	}
	return held
}

// allowsProvider reports whether the grant permits provider at all. A grant with no provider
// config permits none (deny-by-default), and so does a grant that is not there.
func (g *Grant) allowsProvider(provider schemas.ModelProvider) bool {
	if g == nil {
		return false
	}
	// The type decides permission, the configs decide enumeration, and neither stands in for the other:
	// a grant that permits every provider still lists only the ones a caller can be offered, and a
	// deployment with none configured must not read as a grant that permits none.
	if g.isUnrestricted() {
		return true
	}
	for _, pc := range g.ProviderConfigGrants {
		if pc.Provider == string(provider) {
			return true
		}
	}
	return false
}

// blacklistsModel reports whether any of the grant's configs for provider blocks model.
// One blocking config blocks the provider for that model outright.
func (g *Grant) blacklistsModel(provider string, model string) bool {
	if g == nil {
		return false
	}
	for _, pc := range g.ProviderConfigGrants {
		if pc.Provider == provider && pc.BlacklistedModels.IsBlocked(model) {
			return true
		}
	}
	return false
}

// allowsTool reports whether the grant permits toolPattern. The grant that holds a
// client decides for that client: a client with a config here is never widened by
// another config of the same grant.
func (g *Grant) allowsTool(toolPattern string) bool {
	if g == nil {
		return false
	}
	// Clients cannot be enumerated the way providers can, so this is the one permission decided directly
	// rather than through a config of its own.
	if g.isUnrestricted() {
		return true
	}
	for _, mc := range g.MCPConfigGrants {
		clientName := mc.ClientName
		if clientName == "" {
			continue
		}
		if toolPattern != clientName+"-"+Wildcard && !strings.HasPrefix(toolPattern, clientName+"-") {
			continue
		}
		if toolPattern == clientName+"-"+Wildcard {
			return !mc.Tools.IsEmpty()
		}
		if mc.Tools.IsUnrestricted() {
			return true
		}
		return mc.Tools.Contains(strings.TrimPrefix(toolPattern, clientName+"-"))
	}
	return false
}

// grantAllowsModelByName is the name-matching counterpart of EffectiveAccess.grantAllowsModel,
// for the coarse provider gates that must not resolve model names.
func (g *Grant) grantAllowsModelByName(provider string, model string) bool {
	if g == nil {
		return false
	}
	if g.isUnrestricted() {
		return true
	}
	for i := range g.ProviderConfigGrants {
		pc := &g.ProviderConfigGrants[i]
		if pc.Provider != provider {
			continue
		}
		if model == "" {
			return true
		}
		if pc.isModelAllowed(model) {
			return true
		}
	}
	return false
}

// isModelAllowed applies this config's model rules by name only, resolving nothing through the
// catalog. An empty model means there is nothing to filter on, which keeps the config.
func (pc *ProviderConfigGrant) isModelAllowed(model string) bool {
	if model == "" {
		return true
	}
	return pc.AllowedModels.IsAllowed(model) && !pc.BlacklistedModels.IsBlocked(model)
}

// weightedProviderConfigFor returns the grant's first config for provider that sets a weight, or nil
// when it sets none.
func (g *Grant) weightedProviderConfigFor(provider string) *ProviderConfigGrant {
	if g == nil {
		return nil
	}
	for i := range g.ProviderConfigGrants {
		pc := &g.ProviderConfigGrants[i]
		if pc.Provider == provider && pc.Weight != nil {
			return pc
		}
	}
	return nil
}

// providerConfigFor returns the grant's first config for provider, or nil when it holds none.
func (g *Grant) providerConfigFor(provider schemas.ModelProvider) *ProviderConfigGrant {
	if g == nil {
		return nil
	}
	for i := range g.ProviderConfigGrants {
		if g.ProviderConfigGrants[i].Provider == string(provider) {
			return &g.ProviderConfigGrants[i]
		}
	}
	return nil
}

// eachProviderConfigOf visits the grant's provider configs. A provider named by nothing but
// whitespace is skipped: no comparison anywhere would match it, so it could only ever be
// selected and then fail downstream. visit returns false to stop, which this reports back.
func (g *Grant) eachProviderConfigOf(visit func(pc *ProviderConfigGrant) bool) bool {
	if g == nil {
		return true
	}
	for i := range g.ProviderConfigGrants {
		pc := &g.ProviderConfigGrants[i]
		if strings.TrimSpace(pc.Provider) == "" {
			continue
		}
		if !visit(pc) {
			return false
		}
	}
	return true
}

// mcpEntries returns the tool patterns a grant permits, in config order, with duplicates
// collapsed. The first config holding a client decides for that client.
func (g *Grant) mcpEntries() []string {
	if g == nil {
		return []string{}
	}
	entries := newUniqueEntries(len(g.MCPConfigGrants))
	handledClients := make(map[string]struct{})
	for _, mc := range g.MCPConfigGrants {
		if mc.ClientName == "" {
			continue
		}
		clientKey := mc.Client
		if clientKey == "" {
			clientKey = mc.ClientName
		}
		if _, handled := handledClients[clientKey]; handled {
			continue
		}
		handledClients[clientKey] = struct{}{}
		if mc.Tools.IsEmpty() {
			continue
		}
		if mc.Tools.IsUnrestricted() {
			entries.add(mc.ClientName + "-" + Wildcard)
			continue
		}
		for _, tool := range mc.Tools {
			if tool == "" {
				continue
			}
			entries.add(mc.ClientName + "-" + tool)
		}
	}
	return entries.list()
}
