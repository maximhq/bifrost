package grants

import "github.com/maximhq/bifrost/core/schemas"

// An attempt's resolved access travels on the context under
// schemas.BifrostContextKeyGovernanceEffectiveAccess, declared with every other context key
// rather than privately here: the key is part of what a request carries between layers, and a
// registry with one entry missing is how two names for the same thing get invented.
//
// The pair below stays the sanctioned way in and out. Reading through EffectiveAccessFromContext
// keeps the type assertion in one place, and writing through RecordEffectiveAccess keeps the
// no-access-is-not-empty-access rule in one place.

// EffectiveAccessFromContext returns the access resolved for this attempt, or nil when none
// has been: a request that carries no grant, one on a path that runs before resolution, or an
// attempt whose predecessor's answer was cleared so this one resolves its own.
func EffectiveAccessFromContext(ctx *schemas.BifrostContext) *EffectiveAccess {
	if ctx == nil {
		return nil
	}
	access, _ := ctx.Value(schemas.BifrostContextKeyGovernanceEffectiveAccess).(*EffectiveAccess)
	return access
}

// RecordEffectiveAccess records the access resolved for an attempt, so everything downstream
// reads one answer rather than resolving its own. Recording nothing is a no-op: a request with
// no access resolved must stay indistinguishable from one nobody has resolved yet, or a
// consumer would read "resolved, and permits nothing" where it should read "not resolved".
func RecordEffectiveAccess(ctx *schemas.BifrostContext, access *EffectiveAccess) {
	if ctx == nil || access == nil {
		return
	}
	ctx.SetValue(schemas.BifrostContextKeyGovernanceEffectiveAccess, access)
}
