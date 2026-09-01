package grant

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The limits are flat and each says what it covers, so a caller asks one question and never has
// to know which level of a holder configured what.
func TestLimitsHeldBy(t *testing.T) {
	t.Run("scoped to one provider and one model", func(t *testing.T) {
		limits := LimitsHeldBy(LimitHolderVirtualKeyModelConfig, "mc-7", "gpt-4o on openai", "openai", "gpt-4o", "b-1", "b-2")

		require.Len(t, limits, 2)
		assert.Equal(t, schemas.Limit{
			ID: "b-1", HolderKind: string(LimitHolderVirtualKeyModelConfig),
			HolderID: "mc-7", HolderName: "gpt-4o on openai",
			Provider: "openai", Model: "gpt-4o",
		}, limits[0])
		assert.Equal(t, "b-2", limits[1].ID)
	})

	t.Run("an empty axis is a wildcard on that axis", func(t *testing.T) {
		// A holder's own budget names neither, so it covers everything the holder does.
		holderWide := LimitsHeldBy(LimitHolderTeam, "team-1", "Platform", "", "", "b-team")
		require.Len(t, holderWide, 1)
		assert.Empty(t, holderWide[0].Provider)
		assert.Empty(t, holderWide[0].Model)

		// A provider permit's names the provider but not the model.
		perProvider := LimitsHeldBy(LimitHolderVirtualKeyProviderConfig, "7", "Some Key", "openai", "", "b-openai")
		require.Len(t, perProvider, 1)
		assert.Equal(t, "openai", perProvider[0].Provider)
		assert.Empty(t, perProvider[0].Model)
	})

	t.Run("a record with no identity cannot be enforced, so it is dropped", func(t *testing.T) {
		limits := LimitsHeldBy(LimitHolderVirtualKey, "vk1", "Key", "", "", "b-1", "", "b-2")
		assert.Equal(t, []string{"b-1", "b-2"}, limitIDs(limits))

		assert.Nil(t, LimitsHeldBy(LimitHolderVirtualKey, "vk1", "Key", "", "", ""),
			"nothing enforceable is nothing at all, not an empty limit")
		assert.Nil(t, LimitsHeldBy(LimitHolderVirtualKey, "vk1", "Key", "", ""))
	})
}

// A request mixes limits from several holders, so a check specific to one holder has to ask about
// that holder. The failure this prevents is quiet: gating a key's budget check on "any budget at
// all" fires it whenever the team has one, and the key's check then passes while the team's budget
// is exhausted.
func TestLimitsFrom(t *testing.T) {
	limits := []schemas.Limit{
		{ID: "b-key", HolderKind: string(LimitHolderVirtualKey), HolderID: "vk1"},
		{ID: "b-key-openai", HolderKind: string(LimitHolderVirtualKeyProviderConfig), HolderID: "1", Provider: "openai"},
		{ID: "b-team", HolderKind: string(LimitHolderTeam), HolderID: "team-1"},
		{ID: "b-customer", HolderKind: string(LimitHolderCustomer), HolderID: "cust-1"},
	}

	t.Run("one holder", func(t *testing.T) {
		assert.Equal(t, []string{"b-team"}, limitIDs(LimitsFrom(limits, LimitHolderTeam)))
		assert.Equal(t, []string{"b-customer"}, limitIDs(LimitsFrom(limits, LimitHolderCustomer)))
	})

	t.Run("several holders, in the order the limits are held", func(t *testing.T) {
		keyHeld := LimitsFrom(limits, LimitHolderVirtualKey, LimitHolderVirtualKeyProviderConfig)
		assert.Equal(t, []string{"b-key", "b-key-openai"}, limitIDs(keyHeld))
	})

	t.Run("a holder that governs nothing here", func(t *testing.T) {
		// Nil rather than empty: there is nothing of this holder's to check, which is what a
		// caller gates on.
		assert.Nil(t, LimitsFrom(limits, "user"))
		assert.Nil(t, LimitsFrom(nil, LimitHolderTeam))
	})

	t.Run("asking about no holder asks about nothing", func(t *testing.T) {
		assert.Nil(t, LimitsFrom(limits))
	})

	// The two questions a caller can ask of one resolved set, and why they are not interchangeable.
	t.Run("what governs this at all, versus what this holder governs", func(t *testing.T) {
		require.Len(t, limits, 4, "everything funding this request, whoever holds it")

		assert.Len(t, LimitsFrom(limits, LimitHolderVirtualKey, LimitHolderVirtualKeyProviderConfig), 2)
		assert.Len(t, LimitsFrom(limits, LimitHolderTeam), 1)

		// A request funded only by its team: "is anything governing this?" says yes, "is the key
		// governing this?" says no. A check on the key's budgets must follow the second.
		teamOnly := []schemas.Limit{{ID: "b-team", HolderKind: string(LimitHolderTeam), HolderID: "team-1"}}
		assert.NotEmpty(t, teamOnly)
		assert.Empty(t, LimitsFrom(teamOnly, LimitHolderVirtualKey, LimitHolderVirtualKeyProviderConfig))
	})
}

// The holder kinds are one vocabulary for telling limits apart after they have been flattened into
// a single list, so two kinds must never share a value: a collision would silently merge what a
// refusal needs to distinguish, and nothing would fail to compile.
func TestLimitHolderKindsAreDistinct(t *testing.T) {
	kinds := []LimitHolderKind{
		LimitHolderVirtualKey, LimitHolderVirtualKeyProviderConfig,
		LimitHolderUserAccessProfile, LimitHolderUserAccessProfileProviderConfig,
		LimitHolderTeam, LimitHolderBusinessUnit, LimitHolderCustomer,
		LimitHolderProject, LimitHolderProjectProviderConfig,
		LimitHolderProvider, LimitHolderModelConfig,
		LimitHolderVirtualKeyModelConfig, LimitHolderProjectModelConfig,
	}
	seen := make(map[LimitHolderKind]bool, len(kinds))
	for _, k := range kinds {
		assert.NotEmpty(t, string(k), "a holder kind with no value cannot name a holder")
		assert.False(t, seen[k], "%q is declared twice", k)
		seen[k] = true
	}
	assert.Len(t, seen, len(kinds))
}

// A project's own limits and its per-provider limits have to be separable, for the same reason the
// holder's own and its provider permit's are everywhere else: what one provider's permit funds
// cannot answer for a request served by another.
func TestProjectLimitsSeparateTheirHolderFromTheirProviderConfig(t *testing.T) {
	limits := []schemas.Limit{
		{ID: "b-project", HolderKind: string(LimitHolderProject), HolderID: "proj-1"},
		{ID: "b-project-openai", HolderKind: string(LimitHolderProjectProviderConfig), HolderID: "proj-1:7"},
		{ID: "b-key", HolderKind: string(LimitHolderVirtualKey), HolderID: "vk-1"},
	}
	assert.Equal(t, []string{"b-project"}, limitIDs(LimitsFrom(limits, LimitHolderProject)))
	assert.Equal(t, []string{"b-project-openai"}, limitIDs(LimitsFrom(limits, LimitHolderProjectProviderConfig)))
	assert.Equal(t, []string{"b-project", "b-project-openai"},
		limitIDs(LimitsFrom(limits, LimitHolderProject, LimitHolderProjectProviderConfig)))
}

// The limits a request answers to are resolved by its caller and held as one list. This package
// does not work them out: which holders are charged is a question about how a deployment is
// configured, and answering it here would mean learning what a project, a team or a model config
// is.
func TestNewLimits(t *testing.T) {
	budgets := []schemas.Limit{
		{ID: "b-provider", HolderKind: string(LimitHolderProvider), HolderID: "openai", Provider: "openai"},
		{ID: "b-key", HolderKind: string(LimitHolderVirtualKey), HolderID: "vk1"},
	}
	rateLimits := []schemas.Limit{{ID: "r-team", HolderKind: string(LimitHolderTeam), HolderID: "team-1"}}

	t.Run("nothing resolved reads as nothing", func(t *testing.T) {
		// Nil rather than empty on either list: nothing of that kind applies, which is what a
		// caller gates on.
		empty := NewLimits(nil, nil)
		assert.Nil(t, empty.Budgets())
		assert.Nil(t, empty.RateLimits())

		assert.Nil(t, NewLimits([]schemas.Limit{}, []schemas.Limit{}).Budgets())
	})

	t.Run("what was resolved is what comes back, in order", func(t *testing.T) {
		limits := NewLimits(budgets, rateLimits)

		assert.Equal(t, []string{"b-provider", "b-key"}, limitIDs(limits.Budgets()),
			"the order given, which is the order refusals report in")
		assert.Equal(t, []string{"r-team"}, limitIDs(limits.RateLimits()))
	})

	t.Run("either list can be resolved on its own", func(t *testing.T) {
		assert.Nil(t, NewLimits(budgets, nil).RateLimits())
		assert.Equal(t, []string{"b-provider", "b-key"}, limitIDs(NewLimits(budgets, nil).Budgets()))
		assert.Nil(t, NewLimits(nil, rateLimits).Budgets())
		assert.Equal(t, []string{"r-team"}, limitIDs(NewLimits(nil, rateLimits).RateLimits()))
	})

	t.Run("the caller cannot alter what the attempt is held to", func(t *testing.T) {
		mine := append([]schemas.Limit(nil), budgets...)
		limits := NewLimits(mine, nil)
		mine[0].ID = "mutated"

		assert.Equal(t, "b-provider", limits.Budgets()[0].ID)
	})

	t.Run("mutating what a getter returned does not alter what a later reader sees", func(t *testing.T) {
		// Enforcement, billing, and logging all read one settled Limits for an attempt. A getter
		// handing back its own slice would let whichever reads first corrupt what the rest read
		// after it.
		limits := NewLimits(budgets, rateLimits)

		got := limits.Budgets()
		got[0].ID = "mutated"
		assert.Equal(t, "b-provider", limits.Budgets()[0].ID)

		gotRateLimits := limits.RateLimits()
		gotRateLimits[0].ID = "mutated"
		assert.Equal(t, "r-team", limits.RateLimits()[0].ID)
	})

	t.Run("a limit reached twice is one limit", func(t *testing.T) {
		// Holders overlap: a team or customer can be reached through two permits, and a customer
		// can be named directly as well as through its team. Charging the same budget twice for
		// one request is never what that meant.
		team := schemas.Limit{ID: "b-team", HolderKind: string(LimitHolderTeam), HolderID: "team-1"}
		limits := NewLimits([]schemas.Limit{
			{ID: "b-provider", HolderKind: string(LimitHolderProvider), HolderID: "openai", Provider: "openai"},
			team,
			{ID: "b-key", HolderKind: string(LimitHolderVirtualKey), HolderID: "vk1"},
			team,
		}, []schemas.Limit{
			{ID: "r-team", HolderKind: string(LimitHolderTeam), HolderID: "team-1"},
			{ID: "r-team", HolderKind: string(LimitHolderTeam), HolderID: "team-1"},
		})

		assert.Equal(t, []string{"b-provider", "b-team", "b-key"}, limitIDs(limits.Budgets()),
			"the first occurrence, so the order refusals report in survives")
		assert.Equal(t, []string{"r-team"}, limitIDs(limits.RateLimits()))
	})

	t.Run("the same limit reached as two holders keeps the first", func(t *testing.T) {
		// A degenerate configuration, but it has to resolve to something deterministic rather than
		// to one row billed under two names.
		limits := NewLimits([]schemas.Limit{
			{ID: "b-shared", HolderKind: string(LimitHolderTeam), HolderID: "team-1"},
			{ID: "b-shared", HolderKind: string(LimitHolderCustomer), HolderID: "cust-1"},
		}, nil)

		require.Len(t, limits.Budgets(), 1)
		assert.Equal(t, string(LimitHolderTeam), limits.Budgets()[0].HolderKind)
	})

	t.Run("an unnamed limit is dropped rather than collapsed", func(t *testing.T) {
		// Whoever enforces a limit loads it by ID, so one that names no record cannot be enforced,
		// and deduplicating by ID would otherwise fold every unnamed limit into the first of them.
		limits := NewLimits([]schemas.Limit{
			{ID: "", HolderKind: string(LimitHolderTeam), HolderID: "team-1"},
			{ID: "b-key", HolderKind: string(LimitHolderVirtualKey), HolderID: "vk1"},
			{ID: "", HolderKind: string(LimitHolderCustomer), HolderID: "cust-1"},
		}, []schemas.Limit{{ID: ""}, {ID: ""}})

		assert.Equal(t, []string{"b-key"}, limitIDs(limits.Budgets()))
		assert.Nil(t, limits.RateLimits(), "nothing named survives, so nothing is resolved")
	})

	t.Run("no limits answer to nothing", func(t *testing.T) {
		var missing *Limits
		assert.Nil(t, missing.Budgets())
		assert.Nil(t, missing.RateLimits())
	})
}
