package grants

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// ProviderConfigSource supplies the configuration of the providers a deployment has set up.
// Resolving a model name against a provider needs that provider's own configuration, so the
// fold reads it from here rather than being handed a copy per request.
type ProviderConfigSource interface {
	GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig
}

// GrantCompositionMode is how a scoping grant combines with the grant the caller holds.
type GrantCompositionMode string

const (
	// GrantModeIntersect permits what the base grant and the scoping grant both permit.
	GrantModeIntersect GrantCompositionMode = "intersect"
	// GrantModeUnion permits what either of them permits.
	GrantModeUnion GrantCompositionMode = "union"
)

// EffectiveAccess is an attempt's resolved access: the grant the caller holds, the grant
// scoping it, and the mode composing the two. It is built once per attempt and never
// mutated, so every consumer of that attempt sees one answer.
//
// Per attempt rather than per request, because a request that fails over resolves again for
// each provider it tries. Configuration can change between two slow calls, and the attempt
// running second has to answer to what is in force when it runs.
//
// An attempt sees it in one of two states. Until its provider and model are settled it answers
// what a request may reach, which is what routing needs; once they are, WithResolvedLimits adds
// the limits the attempt answers to. That is a copy rather than a mutation, so a consumer holding
// the earlier answer keeps reading it — what replaces it is what the context carries.
//
// There are exactly two slots, not two lists. A request has one holder, so it has one base
// grant; and it is scoped by at most one thing at a time, so it has one scoping grant. What
// a list would buy — several holders, or several independent narrowings — is not a request
// shape that exists, and a list costs the fold an any-of pass, a dedupe, and an ambiguity in
// every answer that has to name which grant decided.
//
// Either slot may be empty. No base grant means the caller holds no access of their own,
// which is not the same as holding a grant that permits nothing. No scoping grant means the
// mode is irrelevant and the answer is exactly what the base grant permits.
type EffectiveAccess struct {
	base    *Grant
	scoping *Grant
	mode    GrantCompositionMode

	// The limits this request answers to once its provider and model are settled — every one of
	// them, whoever holds them, already selected for that pair. Nil until they are settled, which
	// is what distinguishes "not resolved yet" from "answers to nothing".
	resolvedBudgets    []Limit
	resolvedRateLimits []Limit

	// Model names are resolved through the catalog, which needs the provider's own
	// configuration; both are nil-tolerant, and without them allowed-models entries are
	// matched by name.
	modelCatalog *modelcatalog.ModelCatalog
	store        ProviderConfigSource
}

// ProviderCandidate is one way a request could be served: a provider, with the weight and keys
// it would operate under. One per provider config, so a request holding two configs for the
// same provider has two candidates that may carry different weights and keys.
type ProviderCandidate struct {
	Provider string
	Weight   *float64          // nil means the candidate has no weight assigned
	KeyIDs   schemas.WhiteList // ["*"] allows all keys of the provider; per candidate, so two
	//                            configs for one provider can carry different keys. Consumers
	//                            stamping a key restriction for the request use KeysFor
	//                            instead — it answers for the request, not per candidate.

	// Grant is the grant whose access this candidate rests on — the one that permitted the
	// provider and the model, and so the one answerable for serving it. A candidate's weight and
	// keys may draw on both slots, which is why the deciding grant is named rather than inferred.
	//
	// What pays for the candidate is not copied here: it is the grant's, and asking the grant
	// with this candidate's provider is the whole of it. Restating the limits per candidate
	// would leave two records of the same fact to keep in step.
	Grant *Grant
}

// NewEffectiveAccess folds the grant the caller holds together with the grant scoping this
// request, under one composition mode.
//
// A request composes under exactly one mode. Choosing it belongs to whoever resolved the
// grants: that layer knows what it asked and what answered. Passing no scoping grant leaves
// the mode irrelevant and the answer is the base grant alone.
func NewEffectiveAccess(
	base *Grant,
	scoping *Grant,
	mode GrantCompositionMode,
	modelCatalog *modelcatalog.ModelCatalog,
	store ProviderConfigSource,
) *EffectiveAccess {
	return &EffectiveAccess{
		base:         base,
		scoping:      scoping,
		mode:         mode,
		modelCatalog: modelCatalog,
		store:        store,
	}
}

// Base returns the grant the caller holds, or nil when they hold none.
func (a *EffectiveAccess) Base() *Grant {
	if a == nil {
		return nil
	}
	return a.base
}

// Scoping returns the grant scoping this request, or nil when nothing scopes it.
func (a *EffectiveAccess) Scoping() *Grant {
	if a == nil {
		return nil
	}
	return a.scoping
}

// Mode returns the composition mode governing this request. It is meaningless when nothing
// scopes the request.
func (a *EffectiveAccess) Mode() GrantCompositionMode {
	if a == nil {
		return ""
	}
	return a.mode
}

// IsScoped reports whether a scoping grant applies to this request.
func (a *EffectiveAccess) IsScoped() bool {
	return a != nil && a.scoping != nil
}

// BudgetsFor returns every budget a request served by provider using model spends against: the
// base grant's, then the scoping grant's.
//
// Unlike access, limits do not compose under the mode, and this is the one place the two parts of
// a grant behave differently. Access asks whether a request may proceed, so a union can widen it
// and an intersection narrow it. A limit asks whether the request can be afforded, and there is no
// such thing as affording something because a different budget has room — every limit covering the
// request has to permit it, whichever slot it came from. A union-mode scoping grant that widened
// what a caller may reach still governs the spend of what it admitted.
//
// A scoping grant's limits therefore apply to everything it scopes, including providers the base
// grant authorized on its own: the request happens inside that scope, so it is that scope's spend.
func (a *EffectiveAccess) BudgetsFor(provider string) []Limit {
	if a == nil {
		return nil
	}
	return append(a.base.BudgetsFor(provider), a.scoping.BudgetsFor(provider)...)
}

// RateLimitsFor is BudgetsFor for rate limits.
func (a *EffectiveAccess) RateLimitsFor(provider string) []Limit {
	if a == nil {
		return nil
	}
	return append(a.base.RateLimitsFor(provider), a.scoping.RateLimitsFor(provider)...)
}

// WithResolvedLimits returns a copy of the access carrying the limits this request answers to, now
// that its provider and model are settled.
//
// Resolving is the caller's to do and its answer is taken as given: which holders are charged is a
// question about how a deployment is configured, and this package would have to learn what a project,
// a team or a model config is in order to have an opinion. So it holds the answer without deriving
// it, and what a consumer reads is one list rather than several sources to combine in the right order.
//
// One limit reached twice is still one limit, so what is stored is deduplicated. That is not an opinion
// about which holders are charged — it is that charging one budget twice for a single request is never
// what a deployment meant, however its holders overlap. A team or customer reached through both slots,
// or a customer named directly as well as through its team, arrives twice and must not be billed twice.
//
// A copy rather than a mutation, because the access a request was admitted under is what its earlier
// decisions were made against, and something still holding that answer must keep reading it.
func (a *EffectiveAccess) WithResolvedLimits(budgets, rateLimits []Limit) *EffectiveAccess {
	if a == nil {
		return nil
	}
	// Copied as well as deduplicated, so a caller that keeps its slice cannot alter what the request is
	// held to.
	resolved := *a
	resolved.resolvedBudgets = dedupedLimits(budgets)
	resolved.resolvedRateLimits = dedupedLimits(rateLimits)
	return &resolved
}

// dedupedLimits keeps the first occurrence of each limit, in the order given. First rather than last
// because the order is the order refusals report in — what the deployment imposes before what the
// holder is funded by — and a limit reached again later has already been accounted for.
func dedupedLimits(limits []Limit) []Limit {
	if len(limits) == 0 {
		return nil
	}
	deduped := make([]Limit, 0, len(limits))
	seen := make(map[string]struct{}, len(limits))
	for _, limit := range limits {
		if _, already := seen[limit.ID]; already {
			continue
		}
		seen[limit.ID] = struct{}{}
		deduped = append(deduped, limit)
	}
	return deduped
}

// ResolvedBudgets is every budget this request counts against, and ResolvedRateLimits every rate
// limit. Both are nil until the request's provider and model are settled and its limits resolved, so
// a caller can tell that apart from a request that answers to nothing.
func (a *EffectiveAccess) ResolvedBudgets() []Limit {
	if a == nil {
		return nil
	}
	return a.resolvedBudgets
}

// ResolvedRateLimits is ResolvedBudgets for rate limits.
func (a *EffectiveAccess) ResolvedRateLimits() []Limit {
	if a == nil {
		return nil
	}
	return a.resolvedRateLimits
}

// IsUnrestricted reports whether this access permits whatever it does not enumerate — the shape a request
// has when nothing granted it anything. A consumer that narrows something has nothing to narrow to, and
// must leave it alone rather than narrow it to the empty set.
func (a *EffectiveAccess) IsUnrestricted() bool {
	if a == nil {
		return false
	}
	return a.base.isUnrestricted() || a.scoping.isUnrestricted()
}

// IsProviderAllowed reports whether the request may use provider at all.
func (a *EffectiveAccess) IsProviderAllowed(provider schemas.ModelProvider) bool {
	if a == nil {
		return false
	}
	base := a.base.allowsProvider(provider)
	if a.scoping == nil {
		return base
	}
	return a.compose(base, a.scoping.allowsProvider(provider))
}

// IsModelAllowed reports whether the request may use model on provider. Blacklisted
// models lose to nothing: a grant that blacklists the model does not permit it, whatever
// its allowed-models list says.
func (a *EffectiveAccess) IsModelAllowed(provider schemas.ModelProvider, model string) bool {
	if a == nil {
		return false
	}
	base := a.grantAllowsModel(a.base, provider, model)
	if a.scoping == nil {
		return base
	}
	return a.compose(base, a.grantAllowsModel(a.scoping, provider, model))
}

// IsMCPToolAllowed reports whether the request may execute toolPattern, which is either
// "<client>-<tool>" or the "<client>-*" wildcard standing for every tool of a client.
// A wildcard is permitted when the client is granted any tool at all; narrowing a
// wildcard down to the tools actually granted is MCPIncludeList's job.
func (a *EffectiveAccess) IsMCPToolAllowed(toolPattern string) bool {
	if a == nil || toolPattern == "" {
		return false
	}
	base := a.base.allowsTool(toolPattern)
	if a.scoping == nil {
		return base
	}
	return a.compose(base, a.scoping.allowsTool(toolPattern))
}

// KeysFor returns the provider keys the request may use, and whether that list is
// restrictive at all. restricted is false when every key of the provider is allowed, in
// which case keyIDs is nil; when restricted is true, only keyIDs may be used and an
// empty list allows none.
//
// Keys compose like every other permission: when both slots hold the provider, the answer is
// their intersection or their union, whichever the mode says. A key restriction that did not
// compose would be an escape — a scoping grant that narrows a request to two of the provider's
// keys has to mean it, and taking the base grant's list instead would hand the request a key
// the scoping grant refused.
//
// The result is a plain []string rather than the grant's own WhiteList: it is handed
// straight to consumers that type-assert []string, where a named slice type would fail
// the assertion silently and read as "no restriction at all".
func (a *EffectiveAccess) KeysFor(provider schemas.ModelProvider) (keyIDs []string, restricted bool) {
	if a == nil {
		return nil, false
	}
	baseConfig := a.base.providerConfigFor(provider)
	scopingConfig := a.scoping.providerConfigFor(provider)
	var granted schemas.WhiteList
	switch {
	case baseConfig != nil && scopingConfig != nil:
		granted = a.composeKeyIDs(baseConfig.KeyIDs, scopingConfig.KeyIDs, a.mode)
	case baseConfig != nil:
		granted = baseConfig.KeyIDs
	case scopingConfig != nil:
		granted = scopingConfig.KeyIDs
	default:
		// Neither slot holds the provider, so there is no restriction to report. That is a
		// different answer from a config whose restriction happens to name no key, which must
		// not read as "every key is allowed" — so it is decided here, on whether a config was
		// found at all, rather than on whether the list came back empty.
		return nil, false
	}
	if granted.IsUnrestricted() {
		return nil, false
	}
	// A copy, and always non-nil: a consumer must not be able to edit the grant through the
	// answer, and an empty restriction allows no key, which is not "no restriction at all".
	keyIDs = make([]string, 0, len(granted))
	keyIDs = append(keyIDs, granted...)
	return keyIDs, true
}

// ProvidersFor returns every provider config the request may serve model from, in evaluation
// order: the base grant's first, then providers gained purely through the scoping grant. Model
// names resolve through the catalog here — unlike GrantedProvidersFor, this is the decision
// itself rather than a gate over one, so cross-provider naming and provider-prefixed entries must
// resolve exactly as they always did.
//
// Weight is passed through untouched: filtering unweighted candidates, and applying budget and
// rate-limit exclusions, is the caller's decision.
func (a *EffectiveAccess) ProvidersFor(model string) []ProviderCandidate {
	if a == nil || model == "" {
		return nil
	}

	candidates := make([]ProviderCandidate, 0, a.providerConfigCount())
	a.eachProviderConfig(func(grant *Grant, pc *ProviderConfigGrant, _ bool) bool {
		// Three separate questions, none of which implies another. Whether the composed access
		// permits the pair at all:
		if !a.IsModelAllowed(schemas.ModelProvider(pc.Provider), model) {
			return false
		}
		// Whether *this* grant permits it — under a union the other slot may be the one
		// authorizing the request, and a grant that blacklists the model must not serve it. One
		// blacklisting config blocks the provider for that model across the whole grant, so a
		// permissive config cannot reopen it:
		if grant.blacklistsModel(pc.Provider, model) {
			return false
		}
		// And whether this config in particular permits it, so a grant holding several configs
		// for a provider only offers the ones that can serve the model:
		if !a.permitsModel(pc, model) {
			return false
		}
		// Weight does not compose, because it is a preference rather than a permission and there is no
		// meaningful intersection of two preferences. Instead the scoping grant wins where it expresses
		// one: it is the more specific context, so a project that wants a provider preferred differently
		// inside it says so and is obeyed. Where it expresses none, the candidate's own weight stands.
		selectedWeight := pc.Weight
		if a.scoping != nil && grant != a.scoping {
			if scoped := a.scoping.weightedProviderConfigFor(pc.Provider); scoped != nil {
				selectedWeight = scoped.Weight
			}
		}
		// Only the grants that actually permit model on this provider have a say. A grant that is not
		// authorizing this request does not get to restrict which of the provider's keys serve it —
		// under a union that is the whole point, since the request is proceeding on the other grant's
		// authority alone. So the composition is the exception here, not the rule: the candidate's own
		// keys stand unless the other slot both authorizes the model and holds the provider.
		provider := schemas.ModelProvider(pc.Provider)
		otherGrant := a.scoping
		if grant == a.scoping {
			otherGrant = a.base
		}
		selectedKeyIDs := pc.KeyIDs
		if a.grantAllowsModel(otherGrant, provider, model) {
			if otherProviderConfig := otherGrant.providerConfigFor(provider); otherProviderConfig != nil {
				selectedKeyIDs = a.composeKeyIDs(pc.KeyIDs, otherProviderConfig.KeyIDs, a.mode)
			}
		}
		candidates = append(candidates, ProviderCandidate{
			Provider: pc.Provider,
			Weight:   selectedWeight,
			KeyIDs:   selectedKeyIDs,
			Grant:    grant,
		})
		return true
	})

	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// GrantedProvidersFor returns the providers that permit model, for consumers that gate
// on the provider rather than on the exact model — routing layers and model listings.
// An empty model means "no model to filter on", which keeps every granted provider.
//
// Model names are matched against allowed-models entries directly here: these consumers sit on
// top of the model-catalog resolution their own layers already run, so this is a coarse gate and
// must not resolve names itself. ProvidersFor is the counterpart that does, and because this one
// cannot ask it anything, the composition it needs is re-derived by name below.
func (a *EffectiveAccess) GrantedProvidersFor(model string) []schemas.ModelProvider {
	if a == nil {
		return nil
	}

	allowed := make([]schemas.ModelProvider, 0, a.providerConfigCount())
	if a.IsScoped() && a.mode != GrantModeUnion && a.mode != GrantModeIntersect {
		// Unrecognized mode: permit nothing, as everywhere else.
		return allowed
	}
	intersecting := a.IsScoped() && a.mode == GrantModeIntersect

	seen := make(map[string]struct{})
	a.eachProviderConfig(func(_ *Grant, pc *ProviderConfigGrant, isBase bool) bool {
		if _, dup := seen[pc.Provider]; dup {
			return true
		}
		if !pc.isModelAllowed(model) {
			return false
		}
		if intersecting {
			// A scoping grant cannot widen an intersection, and what the caller holds must also
			// be permitted by the scoping grant.
			if !isBase || !a.scoping.grantAllowsModelByName(pc.Provider, model) {
				return false
			}
		}
		seen[pc.Provider] = struct{}{}
		allowed = append(allowed, schemas.ModelProvider(pc.Provider))
		return true
	})
	return allowed
}

// eachProviderConfig visits the provider configs the request could be served from: the base
// grant's first, then the scoping grant's for providers the base grant could not serve. The
// coarse provider gate and candidate selection walk it identically rather than each keeping its
// own copy of the rule.
//
// accept reports whether the visitor took the config, and that is what decides shadowing —
// the base grant shadows the scoping grant only for providers it actually served. Shadowing on
// merely *holding* a provider would lose it whenever the base grant holds it but cannot serve
// this particular request: under a union that is a request the access permits, through the
// scoping grant, with nothing left to serve it.
func (a *EffectiveAccess) eachProviderConfig(accept func(grant *Grant, pc *ProviderConfigGrant, isBase bool) bool) {
	served := make(map[string]struct{})
	a.base.eachProviderConfigOf(func(pc *ProviderConfigGrant) bool {
		if accept(a.base, pc, true) {
			served[pc.Provider] = struct{}{}
		}
		return true
	})
	a.scoping.eachProviderConfigOf(func(pc *ProviderConfigGrant) bool {
		if _, done := served[pc.Provider]; done {
			return true
		}
		accept(a.scoping, pc, false)
		return true
	})
}

// providerConfigCount is how many provider configs the request holds across both slots, for
// sizing results.
func (a *EffectiveAccess) providerConfigCount() int {
	count := 0
	if a.base != nil {
		count += len(a.base.ProviderConfigGrants)
	}
	if a.scoping != nil {
		count += len(a.scoping.ProviderConfigGrants)
	}
	return count
}

// MCPIncludeList returns the tool patterns the request may execute, as
// "<client>-<tool>" entries and "<client>-*" wildcards for clients granted every tool.
// An empty result means no tool may be executed.
func (a *EffectiveAccess) MCPIncludeList() []string {
	if a == nil {
		return nil
	}

	base := a.base.mcpEntries()
	if !a.IsScoped() {
		return base
	}
	return a.composeMCPEntries(base, a.scoping.mcpEntries())
}

// composeMCPEntries folds two grants' tool patterns under the request's mode — the counterpart of
// compose for verdicts and composeKeyIDs for provider keys. An unrecognized mode permits nothing,
// as everywhere else.
//
// The scoping grant is read off the receiver rather than passed in, because scoped has to be that
// grant's own expansion: narrowing asks the grant questions its expansion cannot answer, so a
// caller free to supply entries from elsewhere could quietly widen access.
func (a *EffectiveAccess) composeMCPEntries(base, scoped []string) []string {
	switch a.mode {
	case GrantModeUnion:
		merged := newUniqueEntries(len(base) + len(scoped))
		for _, entry := range base {
			merged.add(entry)
		}
		for _, entry := range scoped {
			merged.add(entry)
		}
		return merged.list()
	case GrantModeIntersect:
		// A wildcard is passed through only when both sides hold one, because downstream a "<client>-*"
		// entry reads as every tool of that client. Which side carries the wildcard decides how the other
		// is consulted, and this is why narrowing needs the grant and not just its entries: an
		// unrestricted client expands to a bare "<client>-*", so testing a specific entry for membership
		// in that expansion would drop every tool the scoping grant in fact permits. The grant is asked
		// instead. In the other direction there is nothing to ask — a wildcard has to be replaced by the
		// scoping grant's specific entries for the client, which only the expansion lists.
		scopedSet := make(map[string]struct{}, len(scoped))
		for _, entry := range scoped {
			scopedSet[entry] = struct{}{}
		}
		kept := newUniqueEntries(len(base))
		for _, entry := range base {
			clientName, isWildcard := strings.CutSuffix(entry, "-"+Wildcard)
			if !isWildcard {
				if a.scoping.allowsTool(entry) {
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
	return []string{}
}

// DenyingGrant returns the grant that denied a provider or model, so the denial can name it.
// An empty model asks about the provider alone.
//
// With one grant per slot there is always exactly one to name: whichever side refused. It
// returns nil only when the request is allowed, or when the side that refused holds no grant
// at all — a caller with no base grant has nothing to be named.
func (a *EffectiveAccess) DenyingGrant(provider schemas.ModelProvider, model string) *Grant {
	if a == nil {
		return nil
	}
	if model == "" {
		return a.denyingGrant(
			a.base.allowsProvider(provider),
			a.scoping.allowsProvider(provider),
		)
	}
	return a.denyingGrant(
		a.grantAllowsModel(a.base, provider, model),
		a.grantAllowsModel(a.scoping, provider, model),
	)
}

// DenyingGrantForTool is DenyingGrant for an MCP tool pattern.
func (a *EffectiveAccess) DenyingGrantForTool(toolPattern string) *Grant {
	if a == nil {
		return nil
	}
	return a.denyingGrant(
		a.base.allowsTool(toolPattern),
		a.scoping.allowsTool(toolPattern),
	)
}

// compose applies the request's composition mode between the two slots' verdicts. It is only
// meaningful with a scoping grant — callers answer the unscoped case with the base alone. An
// unrecognized mode permits nothing.
func (a *EffectiveAccess) compose(baseAllows, scopingAllows bool) bool {
	switch a.mode {
	case GrantModeUnion:
		return baseAllows || scopingAllows
	case GrantModeIntersect:
		return baseAllows && scopingAllows
	}
	return false
}

// composeKeyIDs folds two key restrictions under a composition mode. Order follows the first
// argument, so the result is stable.
//
// The wildcard is the universe rather than an entry: it stands for every key the provider has,
// so intersecting with it yields the other side untouched and unioning with it is unrestricted.
// An unrecognized mode permits nothing, as everywhere else.
func (a *EffectiveAccess) composeKeyIDs(first, second schemas.WhiteList, mode GrantCompositionMode) schemas.WhiteList {
	switch mode {
	case GrantModeIntersect:
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
	case GrantModeUnion:
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
	return schemas.WhiteList{}
}

// denyingGrant identifies which slot denied, given both slots' verdicts.
func (a *EffectiveAccess) denyingGrant(baseAllows, scopingAllows bool) *Grant {
	if a.scoping == nil {
		if baseAllows {
			return nil
		}
		return a.base
	}
	if a.compose(baseAllows, scopingAllows) {
		return nil
	}
	if !baseAllows {
		return a.base
	}
	return a.scoping
}

// grantAllowsModel reports whether the grant permits model on provider: blacklisted by no
// config of that provider, and allowed by at least one. A grant that is not there permits
// nothing.
func (a *EffectiveAccess) grantAllowsModel(g *Grant, provider schemas.ModelProvider, model string) bool {
	if g == nil {
		return false
	}
	if g.blacklistsModel(string(provider), model) {
		return false
	}
	// A grant that permits what it does not enumerate permits this too. Deciding it from the configs
	// would answer "no model on any provider" for a deployment that has configured none, which is the
	// opposite of what the grant says.
	if g.isUnrestricted() {
		return true
	}
	for i := range g.ProviderConfigGrants {
		pc := &g.ProviderConfigGrants[i]
		if pc.Provider != string(provider) {
			continue
		}
		if a.permitsModel(pc, model) {
			return true
		}
	}
	return false
}

// permitsModel applies one config's allowed-models rule. The catalog resolves the model
// name against the provider (cross-provider naming, provider-prefixed entries), which
// needs the provider's own configuration; without a catalog the entries are matched by
// name.
func (a *EffectiveAccess) permitsModel(pc *ProviderConfigGrant, model string) bool {
	if a.modelCatalog == nil || a.store == nil {
		return pc.AllowedModels.IsAllowed(model)
	}
	provider := schemas.ModelProvider(pc.Provider)
	providerConfig, ok := a.store.GetConfiguredProviders()[provider]
	providerConfigPtr := &providerConfig
	if !ok {
		providerConfigPtr = nil
	}
	return a.modelCatalog.IsModelAllowedForProvider(provider, model, providerConfigPtr, pc.AllowedModels)
}
