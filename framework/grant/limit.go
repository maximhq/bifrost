package grant

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

// LimitHolderKind identifies what kind of entity a limit hangs off. These are the values a
// schemas.Limit carries in HolderKind. An open string, like PermitType, so a deployment resolving a
// holder of its own can name it without this package changing. The kinds that do exist are named
// below rather than wherever each is resolved, because telling one limit from another is a single
// vocabulary and a reader should not have to know which layer introduced a kind.
type LimitHolderKind string

// Every kind of limit holder, declared together and grouped by holder. Within a holder the kinds
// run from the widest scope to the narrowest: the holder's own limits, spent by every request it
// makes; its per-provider limits, spent only by requests that provider serves; and its per-model
// limits, spent only by requests for that model.
//
// Once resolved for an attempt, a request's limits are one flat list, and these are what tell them
// apart afterwards: a refusal has to say whether it was your key's budget or your team's, and a
// caller looking at several limits of the same shape needs to know which is which.
//
// Nothing registers or enumerates them. Enforcement covers every kind, so a limit carrying any of
// these is checked and charged by the same path, and a deployment that resolves a holder of its own
// declares its kind here and is enforced by construction.
const (
	// Held by a virtual key.
	LimitHolderVirtualKey               LimitHolderKind = "vk"
	LimitHolderVirtualKeyProviderConfig LimitHolderKind = "vk_provider_config"
	LimitHolderVirtualKeyModelConfig    LimitHolderKind = "vk_model_config"

	// Held by an access profile attached to a user. Named for the user rather than generically,
	// because a profile is currently only attachable to a user; if it becomes attachable to other
	// kinds of holder, each will need its own kind so a refusal can say whose profile it was.
	//
	// There is deliberately no third, per-model-config kind alongside these two: a profile's
	// per-model limits are not a distinct pool with its own lifecycle, they are the same money as
	// the profile's own, so they are attributed to LimitHolderUserAccessProfile rather than
	// splitting into a kind of their own.
	LimitHolderUserAccessProfile               LimitHolderKind = "user_access_profile"
	LimitHolderUserAccessProfileProviderConfig LimitHolderKind = "user_access_profile_provider_config"

	// Held by the organization above the caller. Spent by every request made under anything they
	// contain.
	LimitHolderTeam         LimitHolderKind = "team"
	LimitHolderBusinessUnit LimitHolderKind = "business_unit"
	LimitHolderCustomer     LimitHolderKind = "customer"

	// Held by a project, which is not part of the organization above the caller. A project is a
	// scope a request opts into rather than a container the caller sits inside, so what it funds
	// is spent by whoever names it and by nobody else: a caller's own limits and their
	// organization's are untouched by it, and the same caller reaching the same model outside the
	// project answers to none of these.
	LimitHolderProject               LimitHolderKind = "project"
	LimitHolderProjectProviderConfig LimitHolderKind = "project_provider_config"
	LimitHolderProjectModelConfig    LimitHolderKind = "project_model_config"

	// Held by no one in the organization. A provider's limits are the deployment's own, and a model
	// config's are scoped to a (model, provider) pattern. Neither is anybody's allowance, so a
	// refusal naming one of these is telling the caller about the deployment rather than about
	// themselves.
	LimitHolderProvider    LimitHolderKind = "provider"
	LimitHolderModelConfig LimitHolderKind = "model_config"
)

// LimitsHeldBy names the limits one holder imposes, from the identifiers of the records that
// enforce them. provider and model are recorded on each for description, empty when the limit is
// scoped to neither; see schemas.Limit for why nothing selects on them.
//
// Identifiers rather than records, for the reason on schemas.Limit, and it is also what keeps this
// package out of the business of what a budget row looks like: whoever reads those rows already
// knows their shape, and all this needs from them is an identity. Records without one are skipped,
// since a limit that cannot be loaded cannot be enforced.
func LimitsHeldBy(kind LimitHolderKind, holderID, holderName, provider, model string, ids ...string) []schemas.Limit {
	if len(ids) == 0 {
		return nil
	}
	limits := make([]schemas.Limit, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		limits = append(limits, schemas.Limit{
			ID:         id,
			HolderKind: string(kind),
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
// questions, and a request's limits come from several holders at once. A check written against one
// holder, a key's budgets say, must gate on that holder's own limits, or it fires on a team's
// budget and then reports on the key's, which passes while the team is exhausted. Passing no kinds
// keeps nothing, since asking about no holder is asking about nothing.
func LimitsFrom(limits []schemas.Limit, kinds ...LimitHolderKind) []schemas.Limit {
	if len(limits) == 0 || len(kinds) == 0 {
		return nil
	}
	held := make([]schemas.Limit, 0, len(limits))
	for _, limit := range limits {
		if slices.Contains(kinds, LimitHolderKind(limit.HolderKind)) {
			held = append(held, limit)
		}
	}
	if len(held) == 0 {
		return nil
	}
	return held
}

// Limits is the one implementation of schemas.Limits.
//
// Resolving them is the caller's to do and its answer is taken as given. Which holders are charged
// is a question about how a deployment is configured, and this package would have to learn what a
// project, a team or a model config is in order to have an opinion. So it holds the answer without
// deriving it.
//
// One limit reached twice is still one limit, so what is held is deduplicated. That is not an
// opinion about which holders are charged; it is that charging one budget twice for a single request
// is never what a deployment meant, however its holders overlap. A team or customer reached through
// two permits, or a customer named directly as well as through its team, arrives twice and must not
// be billed twice.
type Limits struct {
	budgets    []schemas.Limit
	rateLimits []schemas.Limit
}

// NewLimits holds the limits an attempt answers to. Both lists are copied as well as deduplicated,
// so a caller that keeps its slice cannot alter what the attempt is held to.
func NewLimits(budgets, rateLimits []schemas.Limit) *Limits {
	return &Limits{
		budgets:    dedupedLimits(budgets),
		rateLimits: dedupedLimits(rateLimits),
	}
}

// Budgets implements schemas.Limits. A copy, not the internal slice: enforcement, billing, and
// logging all read the same settled Limits for one attempt, and a caller sorting or overwriting
// what it got back must not change what the next reader sees.
func (l *Limits) Budgets() []schemas.Limit {
	if l == nil {
		return nil
	}
	return slices.Clone(l.budgets)
}

// RateLimits implements schemas.Limits. See Budgets for why this returns a copy.
func (l *Limits) RateLimits() []schemas.Limit {
	if l == nil {
		return nil
	}
	return slices.Clone(l.rateLimits)
}

// dedupedLimits keeps the first occurrence of each limit, in the order given. First rather than
// last because the order is the order refusals report in, what the deployment imposes before what
// the holder is funded by, and a limit reached again later has already been accounted for.
//
// A limit without an ID is dropped rather than kept: whoever enforces a limit loads it by ID, so
// one that names no record cannot be enforced, and treating every unnamed limit as the same limit
// would silently collapse distinct ones. The same rule LimitsHeldBy applies when it builds them.
func dedupedLimits(limits []schemas.Limit) []schemas.Limit {
	if len(limits) == 0 {
		return nil
	}
	deduped := make([]schemas.Limit, 0, len(limits))
	seen := make(map[string]struct{}, len(limits))
	for _, limit := range limits {
		if limit.ID == "" {
			continue
		}
		if _, already := seen[limit.ID]; already {
			continue
		}
		seen[limit.ID] = struct{}{}
		deduped = append(deduped, limit)
	}
	if len(deduped) == 0 {
		return nil
	}
	return deduped
}
