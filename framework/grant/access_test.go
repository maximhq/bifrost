package grant

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Access is a request's resolved access: the permits its caller holds, the permit scoping it, and
// the mode between them. What is covered here is the fold: every question a consumer can ask of a
// request, and how the two sides combine to answer it.

func TestAccess_DegenerateCases(t *testing.T) {
	basePermit := permitWithProviders(PermitVirtualKey, "vk1", "Caller Key", "openai")
	scopingPermit := permitWithProviders("other", "o1", "Other", "anthropic")

	tests := []struct {
		name          string
		base          *Permit
		scoping       *Permit
		mode          CompositionMode
		wantOpenAI    bool
		wantAnthropic bool
	}{
		{
			name:          "a base permit, nothing scoping it: mode is irrelevant",
			base:          basePermit,
			mode:          Intersect,
			wantOpenAI:    true,
			wantAnthropic: false,
		},
		{
			name:          "neither side filled: nothing is permitted",
			wantOpenAI:    false,
			wantAnthropic: false,
		},
		{
			name:          "no base permit, union: the scoping permit is the whole access",
			scoping:       scopingPermit,
			mode:          Union,
			wantOpenAI:    false,
			wantAnthropic: true,
		},
		{
			name:          "no base permit, intersect: nothing is permitted",
			scoping:       scopingPermit,
			mode:          Intersect,
			wantOpenAI:    false,
			wantAnthropic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			access := NewAccess(held(tt.base), tt.scoping, tt.mode, nil)

			assert.Equal(t, tt.wantOpenAI, access.IsProviderAllowed("openai"), "openai provider")
			assert.Equal(t, tt.wantOpenAI, access.IsModelAllowed("openai", "gpt-4o"), "openai model")
			assert.Equal(t, tt.wantAnthropic, access.IsProviderAllowed("anthropic"), "anthropic provider")
			assert.Equal(t, tt.wantAnthropic, access.IsModelAllowed("anthropic", "claude-sonnet-4"), "anthropic model")
		})
	}
}

func TestAccess_NilReceiverPermitsNothing(t *testing.T) {
	var access *Access

	assert.False(t, access.IsProviderAllowed("openai"))
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.False(t, access.IsMCPToolAllowed("client-tool"))
	assert.False(t, access.IsScoped())
	assert.Nil(t, access.Bases())
	assert.Nil(t, access.Scoping())
	assert.Equal(t, "", access.Mode())
	assert.Empty(t, access.GrantedProvidersForModel("gpt-4o"))
	assert.Empty(t, access.ProvidersForModel("gpt-4o"))
	assert.Empty(t, access.MCPToolIncludeList())
	assert.Equal(t, []string{}, access.NarrowMCPToolIncludeList([]string{"client-tool"}))
	assert.Nil(t, access.PermitsForModel("openai", "gpt-4o"))
	assert.Nil(t, access.DeniedPermitsForModel("openai", "gpt-4o"))
	assert.Nil(t, access.DeniedPermitsForMCPTool("client-tool"))

	keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
	assert.Nil(t, keyIDs)
	assert.False(t, restricted)
}

// Holding no permit is not holding a permit that permits nothing, and the two must stay
// distinguishable: consumers branch on whether a request has access resolved at all.
func TestAccess_NoPermitIsNotAnEmptyPermit(t *testing.T) {
	empty := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", Name: "Empty Key"})

	withEmptyPermit := NewAccess(held(empty), nil, "", nil)
	assert.NotEmpty(t, withEmptyPermit.Bases(), "the caller holds a permit, which happens to permit nothing")
	assert.False(t, withEmptyPermit.IsProviderAllowed("openai"))
	denied := withEmptyPermit.DeniedPermitsForModel("openai", "")
	require.Len(t, denied, 1)
	assert.Equal(t, "Empty Key", denied[0].Name())

	withNoPermit := NewAccess(nil, nil, "", nil)
	assert.Empty(t, withNoPermit.Bases(), "the caller holds nothing at all")
	assert.False(t, withNoPermit.IsProviderAllowed("openai"))
	assert.Nil(t, withNoPermit.DeniedPermitsForModel("openai", ""), "there is no permit to name")
}

func TestAccess_ProviderFold(t *testing.T) {
	base := permitWithProviders(PermitVirtualKey, "vk1", "Caller Key", "openai", "bedrock")
	scoping := permitWithProviders("other", "o1", "Other", "openai", "anthropic")

	unionAccess := NewAccess(held(base), scoping, Union, nil)
	intersectAccess := NewAccess(held(base), scoping, Intersect, nil)

	// Held by both.
	assert.True(t, unionAccess.IsProviderAllowed("openai"))
	assert.True(t, intersectAccess.IsProviderAllowed("openai"))

	// Base only.
	assert.True(t, unionAccess.IsProviderAllowed("bedrock"))
	assert.False(t, intersectAccess.IsProviderAllowed("bedrock"))

	// Scoping permit only.
	assert.True(t, unionAccess.IsProviderAllowed("anthropic"))
	assert.False(t, intersectAccess.IsProviderAllowed("anthropic"))

	// Neither.
	assert.False(t, unionAccess.IsProviderAllowed("cohere"))
	assert.False(t, intersectAccess.IsProviderAllowed("cohere"))
}

func TestAccess_ModelFold(t *testing.T) {
	base := permitWithModels(PermitVirtualKey, "vk1", "Caller Key", "openai", "gpt-4o", "gpt-4o-mini")
	scoping := permitWithModels("other", "o1", "Other", "openai", "gpt-4o", "o3")

	unionAccess := NewAccess(held(base), scoping, Union, nil)
	intersectAccess := NewAccess(held(base), scoping, Intersect, nil)

	assert.True(t, unionAccess.IsModelAllowed("openai", "gpt-4o"))
	assert.True(t, intersectAccess.IsModelAllowed("openai", "gpt-4o"))

	assert.True(t, unionAccess.IsModelAllowed("openai", "gpt-4o-mini"))
	assert.False(t, intersectAccess.IsModelAllowed("openai", "gpt-4o-mini"))

	assert.True(t, unionAccess.IsModelAllowed("openai", "o3"))
	assert.False(t, intersectAccess.IsModelAllowed("openai", "o3"))

	assert.False(t, unionAccess.IsModelAllowed("openai", "gpt-3.5-turbo"))
	assert.False(t, intersectAccess.IsModelAllowed("openai", "gpt-3.5-turbo"))
}

// An empty model asks about the provider alone, which is what lets a consumer gate on the
// provider before it knows the model.
func TestAccess_EmptyModelAsksAboutTheProvider(t *testing.T) {
	base := permitWithModels(PermitVirtualKey, "vk1", "Caller Key", "openai", "gpt-4o")
	access := NewAccess(held(base), nil, "", nil)

	assert.True(t, access.IsModelAllowed("openai", ""))
	assert.False(t, access.IsModelAllowed("anthropic", ""))
	assertPermitsAre(t, access.PermitsForModel("openai", ""), base)
	assert.Nil(t, access.PermitsForModel("anthropic", ""))
}

// Within one permit, several provider permits for a provider are still an any-of: the multiplicity
// that went away was across sources, not inside one.
func TestAccess_ProviderPermitsWithinAPermitAreAnyOf(t *testing.T) {
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
			{Provider: "openai", AllowedModels: []string{"o3"}},
		},
	})
	access := NewAccess(held(base), nil, "", nil)

	assert.True(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.True(t, access.IsModelAllowed("openai", "o3"))
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o-mini"))
}

func TestAccess_BlacklistWins(t *testing.T) {
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, BlacklistedModels: []string{"gpt-4o"}},
			// A second, permissive provider permit for the same provider must not reopen it.
			{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
		},
	})
	access := NewAccess(held(base), nil, "", nil)

	assert.True(t, access.IsProviderAllowed("openai"), "the provider itself stays permitted")
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o"), "blacklisted in one provider permit blocks the provider for that model")
	assert.True(t, access.IsModelAllowed("openai", "o3"))

	// A blacklist on the scoping permit blocks under union too: union widens by what the scoping
	// permit permits, and it does not permit the model at all.
	scoping := newPermit(permitSpec{
		Type: "other", ID: "o1", Name: "Other",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "anthropic", AllowedModels: []string{"*"}, BlacklistedModels: []string{"claude-opus-4"}},
		},
	})
	unionAccess := NewAccess(held(base), scoping, Union, nil)
	assert.True(t, unionAccess.IsModelAllowed("anthropic", "claude-sonnet-4"))
	assert.False(t, unionAccess.IsModelAllowed("anthropic", "claude-opus-4"))
}

func TestAccess_UnknownModeFailsClosed(t *testing.T) {
	base := permitWithProviders(PermitVirtualKey, "vk1", "Caller Key", "openai")
	scoping := permitWithProviders("other", "o1", "Other", "openai")

	access := NewAccess(held(base), scoping, "something-new", nil)

	assert.False(t, access.IsProviderAllowed("openai"))
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.Empty(t, access.GrantedProvidersForModel("gpt-4o"))
	assert.Empty(t, access.MCPToolIncludeList())
	assert.Nil(t, access.PermitsForModel("openai", "gpt-4o"))
}

func TestAccess_ModeIsInertWithoutAScopingPermit(t *testing.T) {
	// A mode with nothing to compose says nothing, whatever it is set to.
	base := permitWithProviders(PermitVirtualKey, "vk1", "Caller Key", "openai")

	for _, mode := range []CompositionMode{Intersect, Union, "something-new", ""} {
		access := NewAccess(held(base), nil, mode, nil)
		assert.True(t, access.IsProviderAllowed("openai"), "mode %q", mode)
		assert.False(t, access.IsScoped(), "mode %q", mode)
	}
}

func TestAccess_ModelNamesResolveThroughTheMatcher(t *testing.T) {
	// The matcher is what makes "*" mean "every model this provider actually serves" rather than
	// "every string", so the same permit answers differently with and without one. Without a
	// matcher, entries are matched by name.
	unrestricted := permitWithModels(PermitVirtualKey, "vk1", "Caller Key", "openai", "*")
	catalog := modelcatalog.NewTestCatalog(nil)
	matcher := catalogMatcher(catalog, &fakeProviderConfigSource{})

	byName := NewAccess(held(unrestricted), nil, "", nil)
	assert.True(t, byName.IsModelAllowed("openai", "not-a-real-model"))

	withCatalog := NewAccess(held(unrestricted), nil, "", matcher)
	assert.False(t, withCatalog.IsModelAllowed("openai", "not-a-real-model"),
		"the catalog does not place this model at this provider")

	// A named entry still matches exactly, catalog or not.
	named := permitWithModels(PermitVirtualKey, "vk2", "Caller Key", "openai", "gpt-4o")
	assert.True(t, NewAccess(held(named), nil, "", matcher).IsModelAllowed("openai", "gpt-4o"))
	assert.False(t, NewAccess(held(named), nil, "", matcher).IsModelAllowed("openai", "o3"))

	// Blacklists never go through the matcher: they are name matches, and they win.
	blacklisted := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk3", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{{
			Provider:          "openai",
			AllowedModels:     []string{"gpt-4o"},
			BlacklistedModels: []string{"gpt-4o"},
		}},
	})
	assert.False(t, NewAccess(held(blacklisted), nil, "", matcher).IsModelAllowed("openai", "gpt-4o"))

	t.Run("the matcher is handed the provider permit's own list, past the blacklist only", func(t *testing.T) {
		type call struct {
			provider, model string
			allowed         []string
		}
		var calls []call
		recording := func(provider, model string, allowed []string) bool {
			calls = append(calls, call{provider, model, allowed})
			return true
		}
		permit := newPermit(permitSpec{
			Type: PermitVirtualKey, ID: "vk4", Name: "Caller Key",
			ProviderPermits: []schemas.ProviderPermit{{
				Provider: "openai", AllowedModels: []string{"gpt-4o"}, BlacklistedModels: []string{"o3"},
			}},
		})
		access := NewAccess(held(permit), nil, "", recording)

		assert.False(t, access.IsModelAllowed("openai", "o3"))
		assert.Empty(t, calls, "a blacklist wins before the matcher is consulted")

		assert.True(t, access.IsModelAllowed("openai", "gpt-4o-mini"), "the matcher's answer is the answer")
		require.Len(t, calls, 1)
		assert.Equal(t, call{"openai", "gpt-4o-mini", []string{"gpt-4o"}}, calls[0])

		assert.False(t, access.IsModelAllowed("anthropic", "claude-sonnet-4"))
		assert.Len(t, calls, 1, "a provider the permit does not hold never reaches the matcher")
	})
}

func TestAccess_KeysFor(t *testing.T) {
	baseRestricted := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-own"}},
			// A second provider permit for the same provider pools its keys with the first's.
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-own-too"}},
			{Provider: "bedrock", AllowedModels: []string{"*"}, KeyIDs: []string{"*"}},
			{Provider: "cohere", AllowedModels: []string{"*"}, KeyIDs: []string{}},
		},
	})
	scoping := newPermit(permitSpec{
		Type: "other", ID: "o1", Name: "Other",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-scoping"}},
			{Provider: "anthropic", AllowedModels: []string{"*"}, KeyIDs: []string{"key-scoping"}},
		},
	})

	access := NewAccess(held(baseRestricted), scoping, Union, nil)

	// Both sides hold openai and both authorize the model, so under a union the request may use
	// either one's keys: the caller's two provider permits pooled, then the scope's. The assertion
	// also pins the return type: consumers type-assert []string, so a named slice type would fail
	// their assertion silently and read as "no key restriction".
	keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
	assert.True(t, restricted)
	assert.Equal(t, []string{"key-own", "key-own-too", "key-scoping"}, keyIDs)
	assert.IsType(t, []string{}, keyIDs)

	// A provider gained purely through the scoping permit uses the scoping permit's keys.
	keyIDs, restricted = access.KeysForModel("anthropic", "claude-sonnet-4")
	assert.True(t, restricted)
	assert.Equal(t, []string{"key-scoping"}, keyIDs)

	// Every key of the provider is allowed: no restriction to stamp anywhere.
	keyIDs, restricted = access.KeysForModel("bedrock", "claude-sonnet-4")
	assert.False(t, restricted)
	assert.Nil(t, keyIDs)

	// An empty restriction allows no key, and must not read as "unrestricted".
	keyIDs, restricted = access.KeysForModel("cohere", "command-r")
	assert.True(t, restricted)
	assert.NotNil(t, keyIDs)
	assert.Empty(t, keyIDs)

	// A provider nobody holds: the request cannot proceed on it, so there is no restriction to
	// report.
	keyIDs, restricted = access.KeysForModel("mistral", "mistral-large")
	assert.False(t, restricted)
	assert.Nil(t, keyIDs)
}

func TestAccess_KeysForComposes(t *testing.T) {
	withKeys := func(id string, keyIDs ...string) *Permit {
		return newPermit(permitSpec{
			Type: PermitType(id), ID: id, Name: id,
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: keyIDs},
			},
		})
	}

	// The reason keys have to compose at all: a scoping permit that narrows the request to two of
	// the provider's keys means it, and the base permit must not reopen the ones it excluded.
	t.Run("a scoping permit narrows the keys it excluded", func(t *testing.T) {
		access := NewAccess(held(withKeys("base", "key-a", "key-b")), withKeys("scoping", "key-b", "key-c"), Intersect, nil)
		keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-b"}, keyIDs, "key-a is the caller's but not the scope's")
	})

	t.Run("union widens", func(t *testing.T) {
		access := NewAccess(held(withKeys("base", "key-a")), withKeys("scoping", "key-b"), Union, nil)
		keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-a", "key-b"}, keyIDs)
	})

	// Disjoint sets under an intersection leave no key that may serve the provider, which is a
	// restriction to nothing rather than the absence of one.
	t.Run("disjoint under intersect allows no key", func(t *testing.T) {
		access := NewAccess(held(withKeys("base", "key-a")), withKeys("scoping", "key-b"), Intersect, nil)
		keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.NotNil(t, keyIDs)
		assert.Empty(t, keyIDs)
	})

	// The wildcard is the universe, not an entry.
	t.Run("the wildcard composes as every key", func(t *testing.T) {
		unrestricted := withKeys("scoping", "*")

		intersected := NewAccess(held(withKeys("base", "key-a")), unrestricted, Intersect, nil)
		keyIDs, restricted := intersected.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-a"}, keyIDs, "intersecting with every key is the other side")

		narrowed := NewAccess(held(withKeys("base", "*")), withKeys("scoping", "key-b"), Intersect, nil)
		keyIDs, restricted = narrowed.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-b"}, keyIDs, "a scope may narrow a caller who held every key")

		widened := NewAccess(held(withKeys("base", "key-a")), unrestricted, Union, nil)
		keyIDs, restricted = widened.KeysForModel("openai", "gpt-4o")
		assert.False(t, restricted, "unioning with every key is no restriction at all")
		assert.Nil(t, keyIDs)

		bothOpen := NewAccess(held(withKeys("base", "*")), unrestricted, Intersect, nil)
		keyIDs, restricted = bothOpen.KeysForModel("openai", "gpt-4o")
		assert.False(t, restricted)
		assert.Nil(t, keyIDs)
	})

	// An unrecognized mode admits nothing, and a request that cannot proceed has no key
	// restriction to report: the refusal comes first, from IsModelAllowed.
	t.Run("an unknown mode admits nothing, so there is nothing to restrict", func(t *testing.T) {
		access := NewAccess(held(withKeys("base", "key-a")), withKeys("scoping", "key-a"), "something-new", nil)
		assert.False(t, access.IsModelAllowed("openai", "gpt-4o"))
		keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
		assert.False(t, restricted)
		assert.Nil(t, keyIDs)
	})

	t.Run("only the admitting side holds the provider", func(t *testing.T) {
		// Under a union the caller's permit admits the request on its own, and a scoping permit
		// that does not authorize the provider has no say in its keys.
		baseOnly := NewAccess(held(withKeys("base", "key-a")), permitWithProviders("scoping", "s", "S", "anthropic"), Union, nil)
		keyIDs, restricted := baseOnly.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-a"}, keyIDs)

		unscoped := NewAccess(held(withKeys("base", "key-a")), nil, Intersect, nil)
		keyIDs, _ = unscoped.KeysForModel("openai", "gpt-4o")
		assert.Equal(t, []string{"key-a"}, keyIDs)

		// Under an intersection the same shape is a refusal, and a refused request has no keys.
		refused := NewAccess(held(withKeys("base", "key-a")), permitWithProviders("scoping", "s", "S", "anthropic"), Intersect, nil)
		assert.False(t, refused.IsModelAllowed("openai", "gpt-4o"))
		keyIDs, restricted = refused.KeysForModel("openai", "gpt-4o")
		assert.False(t, restricted)
		assert.Nil(t, keyIDs)
	})

	// "This provider permit names no key" and "no provider permit was found" both arrive as an
	// empty list, and they are opposite answers: the first permits no key, the second imposes no
	// restriction. Deciding on emptiness rather than on whether a provider permit was found turns
	// the strictest possible restriction into the loosest.
	t.Run("a provider permit recording no key permits none", func(t *testing.T) {
		noKeys := newPermit(permitSpec{
			Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
			ProviderPermits: []schemas.ProviderPermit{{Provider: "openai", AllowedModels: []string{"*"}}},
		})
		access := NewAccess(held(noKeys), nil, "", nil)

		keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted, "a provider permit with no key list restricts to no key")
		assert.NotNil(t, keyIDs)
		assert.Empty(t, keyIDs)

		keyIDs, restricted = access.KeysForModel("anthropic", "claude-sonnet-4")
		assert.False(t, restricted, "a provider neither side holds has no restriction to report")
		assert.Nil(t, keyIDs)
	})

	t.Run("key ids match exactly", func(t *testing.T) {
		// Key IDs are opaque, so a case-folding comparison would intersect two different keys
		// into one and hand the request a key neither side granted.
		access := NewAccess(held(withKeys("base", "Key-A")), withKeys("scoping", "key-a"), Intersect, nil)
		keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Empty(t, keyIDs)
	})

	// Within one permit, two provider permits for a provider are read as one too: the request may
	// use any key either of them allows, and that pooled list is what the scope composes with.
	t.Run("two provider permits for one provider pool their keys", func(t *testing.T) {
		pooled := newPermit(permitSpec{
			Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-a"}},
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-b", "key-a"}},
			},
		})
		keyIDs, restricted := NewAccess(held(pooled), nil, "", nil).KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-a", "key-b"}, keyIDs, "the union, in order, without repeats")

		keyIDs, restricted = NewAccess(held(pooled), withKeys("scoping", "key-b", "key-c"), Intersect, nil).KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-b"}, keyIDs, "the scope narrows the pooled list, not either provider permit's own")

		open := newPermit(permitSpec{
			Type: PermitVirtualKey, ID: "vk2", Name: "Caller Key",
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-a"}},
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"*"}},
			},
		})
		keyIDs, restricted = NewAccess(held(open), nil, "", nil).KeysForModel("openai", "gpt-4o")
		assert.False(t, restricted, "one of them allows every key, so the pool is every key")
		assert.Nil(t, keyIDs)
	})
}

func TestAccess_KeysForReturnsACopy(t *testing.T) {
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-own"}},
		},
	})
	access := NewAccess(held(base), nil, "", nil)

	keyIDs, _ := access.KeysForModel("openai", "gpt-4o")
	require.NotEmpty(t, keyIDs)
	keyIDs[0] = "mutated"

	fresh, _ := access.KeysForModel("openai", "gpt-4o")
	assert.Equal(t, []string{"key-own"}, fresh, "a consumer must not be able to edit the permit")
}

// A candidate's keys are a copy for the same reason, on both paths that would otherwise hand out
// the permit's own slice: the candidate's own keys when nothing composed them, and the side
// composeKeyIDs passes through untouched when the other side is unrestricted.
func TestAccess_ProvidersForCandidateKeysAreACopy(t *testing.T) {
	newBase := func() *Permit {
		return newPermit(permitSpec{
			Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-1", "key-2"}},
			},
		})
	}

	t.Run("the candidate's own keys", func(t *testing.T) {
		base := newBase()
		candidates := NewAccess(held(base), nil, "", nil).ProvidersForModel("gpt-4o")
		require.Len(t, candidates, 1)
		require.NotEmpty(t, candidates[0].KeyIDs)
		candidates[0].KeyIDs[0] = "mutated"

		assert.Equal(t, schemas.WhiteList{"key-1", "key-2"}, base.ProviderPermits()[0].KeyIDs)
	})

	t.Run("keys composeKeyIDs passes through untouched", func(t *testing.T) {
		base := newBase()
		scoping := newPermit(permitSpec{
			Type: PermitProject, ID: "p1", Name: "Project",
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"*"}},
			},
		})
		candidates := NewAccess(held(base), scoping, Intersect, nil).ProvidersForModel("gpt-4o")
		require.Len(t, candidates, 1)
		require.NotEmpty(t, candidates[0].KeyIDs)
		candidates[0].KeyIDs[0] = "mutated"

		assert.Equal(t, schemas.WhiteList{"key-1", "key-2"}, base.ProviderPermits()[0].KeyIDs)
	})
}

// Weight is a preference, so it does not intersect: the scoping permit sets it where it has an
// opinion, and the candidate's own weight stands where it does not.
func TestAccess_WeightFollowsTheScopingPermit(t *testing.T) {
	weighted := func(id string, weight *float64) *Permit {
		return newPermit(permitSpec{
			Type: PermitType(id), ID: id, Name: id,
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"*"}, Weight: weight},
			},
		})
	}

	for _, mode := range []CompositionMode{Union, Intersect} {
		t.Run(string(mode), func(t *testing.T) {
			base := weighted("base", ptr(0.7))
			scoping := weighted("scoping", ptr(0.1))
			access := NewAccess(held(base), scoping, mode, nil)
			candidates := access.ProvidersForModel("gpt-4o")
			require.Len(t, candidates, 1)
			assert.Equal(t, ptr(0.1), candidates[0].Weight)
			// The base permit still serves it, and both permit the pair, so both pay for it.
			assertPermitsAre(t, access.PermitsForModel(candidates[0].Provider, "gpt-4o"), base, scoping)
		})
	}

	t.Run("a scoping permit with no weight leaves the base weight alone", func(t *testing.T) {
		access := NewAccess(held(weighted("base", ptr(0.7))), weighted("scoping", nil), Intersect, nil)
		candidates := access.ProvidersForModel("gpt-4o")
		require.Len(t, candidates, 1)
		assert.Equal(t, ptr(0.7), candidates[0].Weight)
	})

	t.Run("no scoping permit leaves the base weight alone", func(t *testing.T) {
		access := NewAccess(held(weighted("base", ptr(0.7))), nil, "", nil)
		candidates := access.ProvidersForModel("gpt-4o")
		require.Len(t, candidates, 1)
		assert.Equal(t, ptr(0.7), candidates[0].Weight)
	})
}

func TestAccess_ProvidersFor(t *testing.T) {
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{
				Provider:      "openai",
				AllowedModels: []string{"*"},
				KeyIDs:        []string{"key-own"},
				Weight:        ptr(0.7),
			},
			{
				// No weight: still a candidate, so the caller can see it and decide.
				Provider:      "bedrock",
				AllowedModels: []string{"*"},
				KeyIDs:        []string{"*"},
			},
			{
				// Does not permit the model.
				Provider:      "cohere",
				AllowedModels: []string{"command-r"},
			},
		},
	})
	scoping := newPermit(permitSpec{
		Type: "other", ID: "o1", Name: "Other",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-scoping"}, Weight: ptr(0.1)},
			{Provider: "anthropic", AllowedModels: []string{"*"}, KeyIDs: []string{"key-scoping"}, Weight: ptr(0.3)},
		},
	})

	t.Run("base permit only", func(t *testing.T) {
		access := NewAccess(held(base), nil, "", nil)
		candidates := access.ProvidersForModel("gpt-4o")

		require.Len(t, candidates, 2)
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, ptr(0.7), candidates[0].Weight)
		// The permits are what a caller asks for the limits covering this provider.
		assertPermitsAre(t, access.PermitsForModel(candidates[0].Provider, "gpt-4o"), base)
		assert.Equal(t, "bedrock", candidates[1].Provider)
		assert.Nil(t, candidates[1].Weight)
	})

	t.Run("union adds providers with the scoping permit's keys and weight", func(t *testing.T) {
		access := NewAccess(held(base), scoping, Union, nil)
		candidates := access.ProvidersForModel("gpt-4o")

		require.Len(t, candidates, 3)
		// A provider both sides hold is served once, by the base permit, under the union of their
		// keys and the weight the scoping permit sets for it.
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, ptr(0.1), candidates[0].Weight, "the scoping permit's preference")
		assert.Equal(t, schemas.WhiteList{"key-own", "key-scoping"}, candidates[0].KeyIDs)
		assertPermitsAre(t, access.PermitsForModel(candidates[0].Provider, "gpt-4o"), base, scoping)
		assert.Equal(t, "bedrock", candidates[1].Provider)
		assertPermitsAre(t, access.PermitsForModel(candidates[1].Provider, "gpt-4o"), base, scoping)
		// The added provider operates under the scoping permit, which alone permits it.
		assert.Equal(t, "anthropic", candidates[2].Provider)
		assert.Equal(t, ptr(0.3), candidates[2].Weight)
		assert.Equal(t, schemas.WhiteList{"key-scoping"}, candidates[2].KeyIDs)
		assertPermitsAre(t, access.PermitsForModel(candidates[2].Provider, "gpt-4o"), scoping)
	})

	t.Run("intersect never adds a provider", func(t *testing.T) {
		access := NewAccess(held(base), scoping, Intersect, nil)
		candidates := access.ProvidersForModel("gpt-4o")

		require.Len(t, candidates, 1)
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, ptr(0.1), candidates[0].Weight, "the scoping permit's preference")
		// The two key lists are disjoint, so intersecting them leaves no key that may serve this
		// candidate. It is still reported: whether an unusable candidate is worth attempting is
		// the caller's decision, the same as an unweighted one.
		assert.NotNil(t, candidates[0].KeyIDs)
		assert.Empty(t, candidates[0].KeyIDs)
	})

	t.Run("no model means no candidates", func(t *testing.T) {
		access := NewAccess(held(base), nil, "", nil)
		assert.Empty(t, access.ProvidersForModel(""))
	})
}

func TestAccess_ProvidersForPerProviderPermitGranularity(t *testing.T) {
	// Two provider permits for one provider: only the one permitting the model is a candidate,
	// and a blacklist in either of them removes both.
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"o3"}, Weight: ptr(0.2)},
			{Provider: "openai", AllowedModels: []string{"gpt-4o"}, Weight: ptr(0.8)},
		},
	})
	access := NewAccess(held(base), nil, "", nil)

	candidates := access.ProvidersForModel("gpt-4o")
	require.Len(t, candidates, 1)
	assert.Equal(t, ptr(0.8), candidates[0].Weight)

	blacklisted := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk2", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, BlacklistedModels: []string{"gpt-4o"}, Weight: ptr(0.2)},
			{Provider: "openai", AllowedModels: []string{"gpt-4o"}, Weight: ptr(0.8)},
		},
	})
	assert.Empty(t, NewAccess(held(blacklisted), nil, "", nil).ProvidersForModel("gpt-4o"))
}

// A union means either side can be the one authorizing a request, which makes "the access permits
// this pair" and "this permit permits this pair" different questions. A permit that blacklists the
// model must not serve it even while the other side permits it, and the request must still be
// served, by the side that does.
func TestAccess_UnionServesFromThePermitThatPermits(t *testing.T) {
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{{
			Provider:          "openai",
			AllowedModels:     []string{"*"},
			BlacklistedModels: []string{"gpt-4o"},
			KeyIDs:            []string{"key-own"},
		}},
	})
	scoping := newPermit(permitSpec{
		Type: "other", ID: "o1", Name: "Other",
		ProviderPermits: []schemas.ProviderPermit{{
			Provider:      "openai",
			AllowedModels: []string{"*"},
			KeyIDs:        []string{"key-scoping"},
		}},
	})
	access := NewAccess(held(base), scoping, Union, nil)

	require.True(t, access.IsModelAllowed("openai", "gpt-4o"), "the scoping permit permits it")
	// And it alone is named: a permit that blacklists the model does not permit the pair.
	assertPermitsAre(t, access.PermitsForModel("openai", "gpt-4o"), scoping)

	candidates := access.ProvidersForModel("gpt-4o")
	require.Len(t, candidates, 1, "the request is permitted, so something must be able to serve it")
	// Served by the permit that permits the model, not the one that blacklists it.
	assertPermitsAre(t, access.PermitsForModel(candidates[0].Provider, "gpt-4o"), scoping)
	assert.Equal(t, schemas.WhiteList{"key-scoping"}, candidates[0].KeyIDs,
		"the blacklisting permit does not get to say which keys serve a request it refused")

	// KeysForModel agrees with the candidate: the admitting permit's keys, composed with nothing,
	// because the caller's permit is not authorizing this request.
	keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
	assert.True(t, restricted)
	assert.Equal(t, []string{"key-scoping"}, keyIDs)

	// The coarse gate has to agree: a provider the base permit holds but cannot serve is still
	// granted, through the scoping permit.
	assert.Equal(t, []string{"openai"}, access.GrantedProvidersForModel("gpt-4o"))

	// A model both sides permit is served by the base permit, as usual, under the union of their
	// keys, and both are named for it.
	other := access.ProvidersForModel("o3")
	require.Len(t, other, 1)
	assert.Equal(t, schemas.WhiteList{"key-own", "key-scoping"}, other[0].KeyIDs)
	assertPermitsAre(t, access.PermitsForModel(other[0].Provider, "o3"), base, scoping)
}

func TestAccess_GrantedProvidersFor(t *testing.T) {
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
			{Provider: "bedrock", AllowedModels: []string{"*"}, BlacklistedModels: []string{"gpt-4o"}},
			{Provider: "  ", AllowedModels: []string{"*"}},
		},
	})
	scoping := newPermit(permitSpec{
		Type: "other", ID: "o1", Name: "Other",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}},
			{Provider: "anthropic", AllowedModels: []string{"*"}},
		},
	})

	t.Run("base permit only", func(t *testing.T) {
		access := NewAccess(held(base), nil, "", nil)
		assert.Equal(t, []string{"openai"}, access.GrantedProvidersForModel("gpt-4o"))
		// No model to filter on keeps every granted provider.
		assert.Equal(t, []string{"openai", "bedrock"}, access.GrantedProvidersForModel(""))
	})

	t.Run("union", func(t *testing.T) {
		access := NewAccess(held(base), scoping, Union, nil)
		assert.Equal(t, []string{"openai", "anthropic"}, access.GrantedProvidersForModel("gpt-4o"))
	})

	t.Run("intersect", func(t *testing.T) {
		access := NewAccess(held(base), scoping, Intersect, nil)
		assert.Equal(t, []string{"openai"}, access.GrantedProvidersForModel("gpt-4o"))

		// A provider the caller holds but the scoping permit does not is dropped.
		narrow := permitWithProviders("other", "o2", "Other", "anthropic")
		narrowAccess := NewAccess(held(base), narrow, Intersect, nil)
		assert.Empty(t, narrowAccess.GrantedProvidersForModel("gpt-4o"))
	})

	t.Run("empty is not nil", func(t *testing.T) {
		// An empty allowlist means "no provider is permitted", which a consumer must be able to
		// tell apart from "nothing was published".
		access := NewAccess(nil, nil, "", nil)
		providers := access.GrantedProvidersForModel("gpt-4o")
		assert.NotNil(t, providers)
		assert.Empty(t, providers)
	})
}

// The coarse gate reports providers, not provider permits, so a permit holding several for one
// provider still names it once. A consumer treats the result as a set: a repeated provider would
// read as two ways in where there is one.
func TestAccess_GrantedProvidersForDeduplicates(t *testing.T) {
	base := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
			{Provider: "openai", AllowedModels: []string{"gpt-4o", "o3"}},
			{Provider: "anthropic", AllowedModels: []string{"*"}},
		},
	})
	access := NewAccess(held(base), nil, "", nil)

	assert.Equal(t, []string{"openai", "anthropic"}, access.GrantedProvidersForModel("gpt-4o"),
		"both openai provider permits permit it, and it is still one provider")
	assert.Equal(t, []string{"openai", "anthropic"}, access.GrantedProvidersForModel(""))

	// A model only the second provider permit permits still names the provider once.
	assert.Equal(t, []string{"openai", "anthropic"}, access.GrantedProvidersForModel("o3"))

	// And ProvidersForModel, which reports provider permits rather than providers, sees both: the two
	// answer different questions about the same permit.
	require.Len(t, access.ProvidersForModel("gpt-4o"), 3, "two openai provider permits plus anthropic")
}

func TestAccess_GrantedProvidersForIgnoresTheMatcher(t *testing.T) {
	// This coarse gate must not resolve model names even when a matcher is available: the layers
	// consuming it run their own resolution.
	base := permitWithModels(PermitVirtualKey, "vk1", "Caller Key", "openai", "*")
	catalog := modelcatalog.NewTestCatalog(nil)
	matcher := catalogMatcher(catalog, &fakeProviderConfigSource{})

	access := NewAccess(held(base), nil, "", matcher)
	assert.Equal(t, []string{"openai"}, access.GrantedProvidersForModel("not-a-real-model"))
	assert.False(t, access.IsModelAllowed("openai", "not-a-real-model"), "the exact check does resolve it")
}

func TestAccess_IsMCPToolAllowed(t *testing.T) {
	base := permitWithTools(PermitVirtualKey, "vk1", "Caller Key", "github", "read_file", "list_issues")
	scoping := permitWithTools("other", "o1", "Other", "github", "read_file")

	t.Run("base permit only", func(t *testing.T) {
		access := NewAccess(held(base), nil, "", nil)
		assert.True(t, access.IsMCPToolAllowed("github-read_file"))
		assert.True(t, access.IsMCPToolAllowed("github-list_issues"))
		assert.False(t, access.IsMCPToolAllowed("github-delete_repo"))
		assert.False(t, access.IsMCPToolAllowed("slack-post_message"))
		assert.True(t, access.IsMCPToolAllowed("github-*"), "the client is granted some tool")
		assert.False(t, access.IsMCPToolAllowed(""))
	})

	t.Run("intersect", func(t *testing.T) {
		access := NewAccess(held(base), scoping, Intersect, nil)
		assert.True(t, access.IsMCPToolAllowed("github-read_file"))
		assert.False(t, access.IsMCPToolAllowed("github-list_issues"))
	})

	t.Run("union", func(t *testing.T) {
		wider := permitWithTools("other", "o2", "Other", "slack", "post_message")
		access := NewAccess(held(base), wider, Union, nil)
		assert.True(t, access.IsMCPToolAllowed("github-list_issues"))
		assert.True(t, access.IsMCPToolAllowed("slack-post_message"))
		assert.False(t, access.IsMCPToolAllowed("slack-delete_channel"))
	})

	t.Run("a client with no tools granted is not permitted", func(t *testing.T) {
		empty := permitWithTools(PermitVirtualKey, "vk2", "Caller Key", "github")
		access := NewAccess(held(empty), nil, "", nil)
		assert.False(t, access.IsMCPToolAllowed("github-read_file"))
		assert.False(t, access.IsMCPToolAllowed("github-*"))
	})

	t.Run("an unrestricted client permits every tool", func(t *testing.T) {
		all := permitWithTools(PermitVirtualKey, "vk3", "Caller Key", "github", "*")
		access := NewAccess(held(all), nil, "", nil)
		assert.True(t, access.IsMCPToolAllowed("github-anything"))
		assert.True(t, access.IsMCPToolAllowed("github-*"))
	})

	t.Run("within a permit, the first MCP permit for a client is the answer", func(t *testing.T) {
		single := newPermit(permitSpec{
			Type: PermitVirtualKey, ID: "vk4", Name: "Caller Key",
			MCPPermits: []schemas.MCPPermit{
				{Client: "github-id", ClientName: "github", Tools: []string{"read_file"}},
				{Client: "github-id", ClientName: "github", Tools: []string{"*"}},
			},
		})
		assert.False(t, NewAccess(held(single), nil, "", nil).IsMCPToolAllowed("github-delete_repo"))
	})
}

func TestAccess_MCPIncludeList(t *testing.T) {
	base := permitWithTools(PermitVirtualKey, "vk1", "Caller Key", "github", "read_file", "list_issues")

	t.Run("base permit only", func(t *testing.T) {
		access := NewAccess(held(base), nil, "", nil)
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, access.MCPToolIncludeList())
	})

	t.Run("an unrestricted client becomes a wildcard entry", func(t *testing.T) {
		all := permitWithTools(PermitVirtualKey, "vk2", "Caller Key", "github", "*")
		access := NewAccess(held(all), nil, "", nil)
		assert.Equal(t, []string{"github-*"}, access.MCPToolIncludeList())
	})

	t.Run("a client with no tools granted contributes nothing", func(t *testing.T) {
		empty := permitWithTools(PermitVirtualKey, "vk3", "Caller Key", "github")
		access := NewAccess(held(empty), nil, "", nil)
		assert.Empty(t, access.MCPToolIncludeList())
	})

	t.Run("union merges both sides", func(t *testing.T) {
		scoping := permitWithTools("other", "o1", "Other", "slack", "post_message")
		access := NewAccess(held(base), scoping, Union, nil)
		assert.Equal(t, []string{"github-read_file", "github-list_issues", "slack-post_message"}, access.MCPToolIncludeList())
	})

	t.Run("intersect keeps what both sides permit", func(t *testing.T) {
		scoping := permitWithTools("other", "o1", "Other", "github", "read_file")
		access := NewAccess(held(base), scoping, Intersect, nil)
		assert.Equal(t, []string{"github-read_file"}, access.MCPToolIncludeList())
	})

	t.Run("intersect narrows a wildcard down to the other side's tools", func(t *testing.T) {
		wildcardBase := permitWithTools(PermitVirtualKey, "vk4", "Caller Key", "github", "*")
		scoping := permitWithTools("other", "o1", "Other", "github", "read_file")
		access := NewAccess(held(wildcardBase), scoping, Intersect, nil)
		assert.Equal(t, []string{"github-read_file"}, access.MCPToolIncludeList(),
			"passing the wildcard through would read as every tool of the client")
	})

	// The mirror of the case above, and the reason narrowing consults the scoping permit rather
	// than its entry list: an unrestricted client expands to a bare "github-*", so asking whether
	// that list contains "github-read_file" answers no for every tool the scope in fact permits.
	t.Run("intersect keeps specific tools against an unrestricted scope", func(t *testing.T) {
		scoping := permitWithTools("other", "o1", "Other", "github", "*")
		access := NewAccess(held(base), scoping, Intersect, nil)
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, access.MCPToolIncludeList(),
			"intersecting a specific list with every tool is the specific list")
	})

	t.Run("intersect keeps a wildcard only when both sides are unrestricted", func(t *testing.T) {
		wildcardBase := permitWithTools(PermitVirtualKey, "vk5", "Caller Key", "github", "*")
		wildcardScoping := permitWithTools("other", "o1", "Other", "github", "*")
		access := NewAccess(held(wildcardBase), wildcardScoping, Intersect, nil)
		assert.Equal(t, []string{"github-*"}, access.MCPToolIncludeList())
	})

	t.Run("intersect drops clients the other side does not hold", func(t *testing.T) {
		scoping := permitWithTools("other", "o1", "Other", "slack", "post_message")
		access := NewAccess(held(base), scoping, Intersect, nil)
		assert.Empty(t, access.MCPToolIncludeList())
	})
}

func TestAccess_DeniedBy(t *testing.T) {
	base := permitWithModels(PermitVirtualKey, "vk1", "Caller Key", "openai", "gpt-4o")
	scoping := permitWithModels("other", "o1", "Other Name", "openai", "o3")

	t.Run("allowed requests have no denying permit", func(t *testing.T) {
		access := NewAccess(held(base), nil, "", nil)
		assert.Nil(t, access.DeniedPermitsForModel("openai", "gpt-4o"))
		assert.Nil(t, access.DeniedPermitsForModel("openai", ""))
	})

	t.Run("the base permit is named when it denies", func(t *testing.T) {
		access := NewAccess(held(base), nil, "", nil)
		denied := access.DeniedPermitsForModel("anthropic", "claude-sonnet-4")
		require.Len(t, denied, 1)
		assert.Equal(t, "Caller Key", denied[0].Name())
		assert.Equal(t, string(PermitVirtualKey), denied[0].Type())
	})

	// Under a union either side's permission would have changed the answer, so a refusal names
	// both: the caller's permit first, then the scope.
	t.Run("under a union both sides are named when both deny", func(t *testing.T) {
		access := NewAccess(held(base), scoping, Union, nil)
		denied := access.DeniedPermitsForModel("anthropic", "claude-sonnet-4")
		require.Len(t, denied, 2)
		assert.Same(t, base, denied[0])
		assert.Same(t, scoping, denied[1])
	})

	t.Run("the scoping permit is named when it alone denies", func(t *testing.T) {
		access := NewAccess(held(base), scoping, Intersect, nil)
		denied := access.DeniedPermitsForModel("openai", "gpt-4o")
		require.Len(t, denied, 1)
		assert.Equal(t, "Other Name", denied[0].Name())
		assert.Equal(t, "other", denied[0].Type())

		// Provider-level denials are attributed the same way.
		narrow := permitWithProviders("other", "o2", "Narrow", "anthropic")
		providerAccess := NewAccess(held(base), narrow, Intersect, nil)
		denied = providerAccess.DeniedPermitsForModel("openai", "")
		require.Len(t, denied, 1)
		assert.Equal(t, "Narrow", denied[0].Name())
	})

	t.Run("under an intersection a refusing base is named without the scope", func(t *testing.T) {
		// The scope did not stand in the way: with no base permitting, its own verdict changes
		// nothing.
		access := NewAccess(held(base), scoping, Intersect, nil)
		denied := access.DeniedPermitsForModel("openai", "o3")
		require.Len(t, denied, 1)
		assert.Same(t, base, denied[0])
	})

	t.Run("a refused request always names a permit", func(t *testing.T) {
		for _, mode := range []CompositionMode{Union, Intersect} {
			access := NewAccess(held(base), scoping, mode, nil)
			for _, model := range []string{"gpt-4o", "o3", "gpt-4o-mini"} {
				if access.IsModelAllowed("openai", model) {
					continue
				}
				assert.NotEmpty(t, access.DeniedPermitsForModel("openai", model), "mode %q model %q", mode, model)
			}
		}
	})

	t.Run("nothing is named when the caller holds no permit", func(t *testing.T) {
		access := NewAccess(nil, scoping, Intersect, nil)
		assert.False(t, access.IsModelAllowed("openai", "o3"))
		assert.Nil(t, access.DeniedPermitsForModel("openai", "o3"), "the caller holds no permit to name")

		// Under a union the scope was the only thing that could have permitted it, so a refusal
		// still has it to name.
		unionAccess := NewAccess(nil, scoping, Union, nil)
		assert.False(t, unionAccess.IsModelAllowed("openai", "gpt-4o-mini"))
		denied := unionAccess.DeniedPermitsForModel("openai", "gpt-4o-mini")
		require.Len(t, denied, 1)
		assert.Same(t, scoping, denied[0])
	})

	t.Run("tool denials are attributed too", func(t *testing.T) {
		baseTools := permitWithTools(PermitVirtualKey, "vk1", "Caller Key", "github", "read_file")
		scopingTools := permitWithTools("other", "o1", "Other Name", "github", "list_issues")

		access := NewAccess(held(baseTools), scopingTools, Intersect, nil)

		denied := access.DeniedPermitsForMCPTool("github-read_file")
		require.Len(t, denied, 1)
		assert.Equal(t, "Other Name", denied[0].Name())

		denied = access.DeniedPermitsForMCPTool("github-list_issues")
		require.Len(t, denied, 1)
		assert.Equal(t, "Caller Key", denied[0].Name())

		assert.Nil(t, access.DeniedPermitsForMCPTool("github-*"), "both sides grant the client some tool")
	})
}

func TestAccess_Accessors(t *testing.T) {
	base := permitWithProviders(PermitVirtualKey, "vk1", "Caller Key", "openai")
	scoping := permitWithProviders("other", "o1", "Other", "anthropic")

	access := NewAccess(held(base), scoping, Union, nil)
	assert.Equal(t, string(Union), access.Mode())
	assert.True(t, access.IsScoped())
	bases := access.Bases()
	require.Len(t, bases, 1)
	assert.Same(t, base, bases[0])
	assert.Same(t, scoping, access.Scoping())

	bare := NewAccess(held(base), nil, "", nil)
	assert.False(t, bare.IsScoped())
	assert.Nil(t, bare.Scoping())
	assert.Equal(t, "", bare.Mode())

	t.Run("bases are handed out as a copy", func(t *testing.T) {
		mine := access.Bases()
		mine[0] = nil
		fresh := access.Bases()
		require.Len(t, fresh, 1)
		assert.Same(t, base, fresh[0], "a consumer must not be able to edit the access through the answer")
	})
}

func TestAccess_NewAccessDropsWhatIsNotThere(t *testing.T) {
	first := permitWithProviders(PermitAccessProfile, "ap1", "First", "openai")
	second := permitWithProviders(PermitAccessProfile, "ap2", "Second", "anthropic")

	// A resolver that found nothing for one source does not have to say so twice: nil entries,
	// bare or typed, are dropped, and a typed-nil scoping permit is no scoping permit.
	access := NewAccess([]schemas.Permit{nil, first, (*Permit)(nil), second}, (*Permit)(nil), Intersect, nil)

	bases := access.Bases()
	require.Len(t, bases, 2)
	assert.Same(t, first, bases[0])
	assert.Same(t, second, bases[1])

	assert.False(t, access.IsScoped())
	assert.Nil(t, access.Scoping())
	assert.True(t, access.IsModelAllowed("anthropic", "claude-sonnet-4"),
		"the mode is inert with nothing scoping the request")
}

// twoProfilesAndAProject is the shape a caller holding several permits has: two profiles, in
// attribution order, and a project the request may name. The profiles overlap on openai and
// bedrock, disagree about what each permits there, and each holds a provider the other does not.
func twoProfilesAndAProject() (first, second, project *Permit) {
	first = newPermit(permitSpec{
		Type: PermitAccessProfile, ID: "ap1", Name: "First Profile",
		ProviderPermits: []schemas.ProviderPermit{
			{
				Provider: "openai", AllowedModels: []string{"gpt-4o"}, KeyIDs: []string{"key-first"}, Weight: ptr(0.5),
			},
			{
				Provider: "bedrock", AllowedModels: []string{"*"}, BlacklistedModels: []string{"claude-opus-4"}, KeyIDs: []string{"key-first-bedrock"},
			},
		},
		MCPPermits: []schemas.MCPPermit{{Client: "github-id", ClientName: "github", Tools: []string{"read_file"}}},
	})
	second = newPermit(permitSpec{
		Type: PermitAccessProfile, ID: "ap2", Name: "Second Profile",
		ProviderPermits: []schemas.ProviderPermit{
			{
				Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-second"}, Weight: ptr(0.9),
			},
			{
				Provider: "bedrock", AllowedModels: []string{"*"}, KeyIDs: []string{"key-second-bedrock"},
			},
			{
				Provider: "anthropic", AllowedModels: []string{"claude-sonnet-4"}, KeyIDs: []string{"key-second-anthropic"},
			},
		},
		MCPPermits: []schemas.MCPPermit{
			{Client: "slack-id", ClientName: "slack", Tools: []string{"post_message"}},
			{Client: "github-id", ClientName: "github", Tools: []string{"list_issues"}},
		},
	})
	project = newPermit(permitSpec{
		Type: PermitProject, ID: "p1", Name: "Some Project",
		ProviderPermits: []schemas.ProviderPermit{
			{
				Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-first", "key-project"},
			},
			{
				Provider: "gemini", AllowedModels: []string{"*"}, KeyIDs: []string{"key-project"}, Weight: ptr(0.2),
			},
		},
		MCPPermits: []schemas.MCPPermit{{Client: "jira-id", ClientName: "jira", Tools: []string{"*"}}},
	})
	return first, second, project
}

// The caller's permits are read as one: what any of them permits, the caller may reach.
func TestAccess_SeveralBasesAreReadAsOne(t *testing.T) {
	first, second, _ := twoProfilesAndAProject()
	access := NewAccess(held(first, second), nil, "", nil)

	assert.True(t, access.IsModelAllowed("openai", "gpt-4o"), "the first permits it")
	assert.True(t, access.IsModelAllowed("openai", "o3"), "only the second permits it")
	assert.True(t, access.IsModelAllowed("anthropic", "claude-sonnet-4"), "a provider only the second holds")
	assert.True(t, access.IsProviderAllowed("anthropic"))
	assert.False(t, access.IsModelAllowed("anthropic", "claude-opus-4"), "neither permits it")
	assert.False(t, access.IsProviderAllowed("cohere"), "neither holds it")

	// A blacklist blocks the permit that carries it, not the caller: the second permit does not
	// blacklist the model, so through it the caller may reach it.
	assert.True(t, access.IsModelAllowed("bedrock", "claude-opus-4"))

	assert.True(t, access.IsMCPToolAllowed("github-read_file"), "the first grants it")
	assert.True(t, access.IsMCPToolAllowed("github-list_issues"), "only the second grants it")
	assert.True(t, access.IsMCPToolAllowed("slack-post_message"))
	assert.False(t, access.IsMCPToolAllowed("jira-create_issue"))
}

// The attribution rule: every one of the caller's permits that permits the pair is named, in their
// order, then the scoping permit whenever the request is scoped. The caller's permits are read as
// one, so each that permits the pair covers the request and each is charged. The scope is named
// whether or not it holds the pair itself: the request happens inside it and is its spend.
func TestAccess_PermitsForModelNamesEveryPermittingPermit(t *testing.T) {
	first, second, project := twoProfilesAndAProject()

	unscoped := NewAccess(held(first, second), nil, "", nil)
	unionAccess := NewAccess(held(first, second), project, Union, nil)
	intersectAccess := NewAccess(held(first, second), project, Intersect, nil)
	projectAlone := NewAccess(nil, project, Union, nil)

	tests := []struct {
		name     string
		access   *Access
		provider string
		model    string
		want     []schemas.Permit // nil: the request may not use the pair at all
	}{
		{name: "both bases permit it: both are named, in their order", access: unscoped, provider: "openai", model: "gpt-4o", want: []schemas.Permit{first, second}},
		{name: "only the second permits the model", access: unscoped, provider: "openai", model: "o3", want: []schemas.Permit{second}},
		{name: "a provider only the second holds", access: unscoped, provider: "anthropic", model: "claude-sonnet-4", want: []schemas.Permit{second}},
		{name: "the first blacklists it, the second permits it", access: unscoped, provider: "bedrock", model: "claude-opus-4", want: []schemas.Permit{second}},
		{name: "the provider alone names every base holding it", access: unscoped, provider: "openai", model: "", want: []schemas.Permit{first, second}},
		{name: "no base permits the model", access: unscoped, provider: "anthropic", model: "claude-opus-4"},
		{name: "no base holds the provider", access: unscoped, provider: "cohere", model: "command-r"},

		{name: "union: the scope follows every base that permits", access: unionAccess, provider: "openai", model: "gpt-4o", want: []schemas.Permit{first, second, project}},
		{name: "union: a base comes before the scope", access: unionAccess, provider: "openai", model: "o3", want: []schemas.Permit{second, project}},
		{name: "union: the scope is named even where it does not hold the provider", access: unionAccess, provider: "bedrock", model: "claude-sonnet-4", want: []schemas.Permit{first, second, project}},
		{name: "union: gained through the scope alone", access: unionAccess, provider: "gemini", model: "gemini-pro", want: []schemas.Permit{project}},
		{name: "union: nothing permits it", access: unionAccess, provider: "cohere", model: "command-r"},

		{name: "intersect: both sides permit", access: intersectAccess, provider: "openai", model: "gpt-4o", want: []schemas.Permit{first, second, project}},
		{name: "intersect: one base and the scope", access: intersectAccess, provider: "openai", model: "o3", want: []schemas.Permit{second, project}},
		{name: "intersect: the scope does not hold the provider", access: intersectAccess, provider: "bedrock", model: "claude-sonnet-4"},
		{name: "intersect: an intersection cannot widen", access: intersectAccess, provider: "gemini", model: "gemini-pro"},

		{name: "no base at all: the scope alone", access: projectAlone, provider: "gemini", model: "gemini-pro", want: []schemas.Permit{project}},
		{name: "no base at all: refused where the scope does not permit", access: projectAlone, provider: "bedrock", model: "claude-sonnet-4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.access.PermitsForModel(tt.provider, tt.model)
			if tt.want == nil {
				assert.False(t, tt.access.IsModelAllowed(tt.provider, tt.model), "a refused pair answers to no permit")
				assert.Nil(t, got)
				return
			}
			assert.True(t, tt.access.IsModelAllowed(tt.provider, tt.model))
			assertPermitsAre(t, got, tt.want...)
		})
	}

	t.Run("the answer is a copy", func(t *testing.T) {
		mine := unscoped.PermitsForModel("openai", "gpt-4o")
		require.Len(t, mine, 2)
		mine[0] = nil
		assertPermitsAre(t, unscoped.PermitsForModel("openai", "gpt-4o"), first, second)
	})
}

// The caller's permits are read as one for keys too: any key any of the permitting bases allows may
// serve the request, and that pooled list is what the scope composes with.
func TestAccess_KeysForPoolTheCallersPermits(t *testing.T) {
	first, second, project := twoProfilesAndAProject()

	t.Run("the union of every permitting base's keys", func(t *testing.T) {
		access := NewAccess(held(first, second), nil, "", nil)

		keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-first", "key-second"}, keyIDs, "both permit it, so either one's keys may serve it")

		keyIDs, restricted = access.KeysForModel("openai", "o3")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-second"}, keyIDs, "the first does not permit it, and has no say")

		keyIDs, restricted = access.KeysForModel("bedrock", "claude-opus-4")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-second-bedrock"}, keyIDs, "the first blacklists it, and has no say")

		keyIDs, restricted = access.KeysForModel("anthropic", "claude-opus-4")
		assert.False(t, restricted, "a refused pair has no key restriction to report")
		assert.Nil(t, keyIDs)
	})

	t.Run("composed with the scope only where the scope authorizes the pair", func(t *testing.T) {
		unionAccess := NewAccess(held(first, second), project, Union, nil)

		keyIDs, _ := unionAccess.KeysForModel("openai", "gpt-4o")
		assert.Equal(t, []string{"key-first", "key-second", "key-project"}, keyIDs, "both bases' keys unioned with the project's")

		keyIDs, _ = unionAccess.KeysForModel("openai", "o3")
		assert.Equal(t, []string{"key-second", "key-first", "key-project"}, keyIDs, "the second's keys unioned with the project's")

		keyIDs, _ = unionAccess.KeysForModel("bedrock", "claude-sonnet-4")
		assert.Equal(t, []string{"key-first-bedrock", "key-second-bedrock"}, keyIDs, "the project does not hold bedrock, so the pooled keys compose with nothing")

		keyIDs, _ = unionAccess.KeysForModel("gemini", "gemini-pro")
		assert.Equal(t, []string{"key-project"}, keyIDs, "gained through the project alone")

		intersectAccess := NewAccess(held(first, second), project, Intersect, nil)

		keyIDs, _ = intersectAccess.KeysForModel("openai", "gpt-4o")
		assert.Equal(t, []string{"key-first"}, keyIDs, "of the pooled keys, only the one the project also allows")

		keyIDs, restricted := intersectAccess.KeysForModel("openai", "o3")
		assert.True(t, restricted)
		assert.Empty(t, keyIDs, "the second's keys and the project's are disjoint")

		keyIDs, restricted = intersectAccess.KeysForModel("bedrock", "claude-sonnet-4")
		assert.False(t, restricted, "refused under an intersection")
		assert.Nil(t, keyIDs)
	})

	t.Run("a wildcard in any permitting base opens the caller's side", func(t *testing.T) {
		everyKey := newPermit(permitSpec{
			Type: PermitAccessProfile, ID: "ap3", Name: "Every Key",
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"*"}},
			},
		})

		keyIDs, restricted := NewAccess(held(first, everyKey), nil, "", nil).KeysForModel("openai", "gpt-4o")
		assert.False(t, restricted, "one base allows every key, so the pool is every key")
		assert.Nil(t, keyIDs)

		keyIDs, restricted = NewAccess(held(first, everyKey), project, Intersect, nil).KeysForModel("openai", "gpt-4o")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-first", "key-project"}, keyIDs, "the scope still narrows an open pool")
	})
}

// The caller's permits are read as one, so nothing shadows across them: every base is offered in
// full, in base order, and a provider two bases serve is two candidates, each under its own weight
// and keys. Only the scoping permit is shadowed, and only for providers some base actually served:
// a provider a base holds but cannot serve for this model is still offered from whatever can.
func TestAccess_ProvidersForOffersEveryBaseInFull(t *testing.T) {
	first, second, project := twoProfilesAndAProject()

	t.Run("a provider two bases serve is offered once per base", func(t *testing.T) {
		access := NewAccess(held(first, second), project, Union, nil)
		candidates := access.ProvidersForModel("gpt-4o")

		require.Len(t, candidates, 5, "the first's two providers, the second's two, then the project's own")
		// The first base, in full.
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, ptr(0.5), candidates[0].Weight, "the project sets no weight for openai")
		assert.Equal(t, schemas.WhiteList{"key-first", "key-project"}, candidates[0].KeyIDs, "the first's keys unioned with the project's")
		assert.Equal(t, "bedrock", candidates[1].Provider)
		assert.Nil(t, candidates[1].Weight)
		assert.Equal(t, schemas.WhiteList{"key-first-bedrock"}, candidates[1].KeyIDs, "the project does not hold bedrock")
		// Then the second, in full: the same providers again, under its own weight and keys.
		assert.Equal(t, "openai", candidates[2].Provider)
		assert.Equal(t, ptr(0.9), candidates[2].Weight)
		assert.Equal(t, schemas.WhiteList{"key-second", "key-first", "key-project"}, candidates[2].KeyIDs, "the second's keys unioned with the project's")
		assert.Equal(t, "bedrock", candidates[3].Provider)
		assert.Nil(t, candidates[3].Weight)
		assert.Equal(t, schemas.WhiteList{"key-second-bedrock"}, candidates[3].KeyIDs)
		// Then what only the project holds. Its openai is shadowed: a base served it.
		assert.Equal(t, "gemini", candidates[4].Provider)
		assert.Equal(t, ptr(0.2), candidates[4].Weight)
		assert.Equal(t, schemas.WhiteList{"key-project"}, candidates[4].KeyIDs)

		// Both openai candidates rest on the same answer: every permit that permits the pair.
		assertPermitsAre(t, access.PermitsForModel("openai", "gpt-4o"), first, second, project)
		assertPermitsAre(t, access.PermitsForModel("bedrock", "gpt-4o"), first, second, project)
		assertPermitsAre(t, access.PermitsForModel("gemini", "gpt-4o"), project)
	})

	t.Run("a provider the first base holds but cannot serve is offered from the second", func(t *testing.T) {
		access := NewAccess(held(first, second), project, Union, nil)
		candidates := access.ProvidersForModel("claude-opus-4")

		require.Len(t, candidates, 3)
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, ptr(0.9), candidates[0].Weight, "the first does not allow the model on openai, so only the second offers it")
		assert.Equal(t, schemas.WhiteList{"key-second", "key-first", "key-project"}, candidates[0].KeyIDs)
		assertPermitsAre(t, access.PermitsForModel("openai", "claude-opus-4"), second, project)
		assert.Equal(t, "bedrock", candidates[1].Provider)
		assert.Equal(t, schemas.WhiteList{"key-second-bedrock"}, candidates[1].KeyIDs, "the first blacklists the model on bedrock, so only the second offers it")
		assertPermitsAre(t, access.PermitsForModel("bedrock", "claude-opus-4"), second, project)
		assert.Equal(t, "gemini", candidates[2].Provider)
		assertPermitsAre(t, access.PermitsForModel("gemini", "claude-opus-4"), project)
	})

	t.Run("evaluation order is base order, each base in full", func(t *testing.T) {
		access := NewAccess(held(first, second), nil, "", nil)
		candidates := access.ProvidersForModel("o3")

		require.Len(t, candidates, 3, "bedrock from the first, then openai and bedrock from the second")
		assert.Equal(t, "bedrock", candidates[0].Provider)
		assert.Equal(t, schemas.WhiteList{"key-first-bedrock"}, candidates[0].KeyIDs)
		assert.Equal(t, "openai", candidates[1].Provider)
		assert.Equal(t, ptr(0.9), candidates[1].Weight)
		assert.Equal(t, "bedrock", candidates[2].Provider)
		assert.Equal(t, schemas.WhiteList{"key-second-bedrock"}, candidates[2].KeyIDs, "served by the first already, and offered again from the second")
		assertPermitsAre(t, access.PermitsForModel("openai", "o3"), second)
		assertPermitsAre(t, access.PermitsForModel("bedrock", "o3"), first, second)
	})

	t.Run("under an intersection each base's candidate composes with the scope on its own", func(t *testing.T) {
		access := NewAccess(held(first, second), project, Intersect, nil)
		candidates := access.ProvidersForModel("gpt-4o")

		require.Len(t, candidates, 2, "openai from each base: the project does not hold bedrock, and no base holds gemini")
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, ptr(0.5), candidates[0].Weight)
		assert.Equal(t, schemas.WhiteList{"key-first"}, candidates[0].KeyIDs, "the first's keys intersected with the project's")
		assert.Equal(t, "openai", candidates[1].Provider)
		assert.Equal(t, ptr(0.9), candidates[1].Weight)
		assert.NotNil(t, candidates[1].KeyIDs)
		assert.Empty(t, candidates[1].KeyIDs, "the second's keys and the project's are disjoint, and the candidate is still reported")
	})

	t.Run("within one base nothing shadows either", func(t *testing.T) {
		twice := newPermit(permitSpec{
			Type: PermitAccessProfile, ID: "ap3", Name: "Twice",
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-a"}, Weight: ptr(0.2)},
				{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-b"}, Weight: ptr(0.8)},
			},
		})
		access := NewAccess(held(first, twice), nil, "", nil)
		candidates := access.ProvidersForModel("o3")

		require.Len(t, candidates, 3, "bedrock from the first, then both openai provider permits of the second")
		assert.Equal(t, "bedrock", candidates[0].Provider)
		assert.Equal(t, "openai", candidates[1].Provider)
		assert.Equal(t, ptr(0.2), candidates[1].Weight)
		assert.Equal(t, "openai", candidates[2].Provider)
		assert.Equal(t, ptr(0.8), candidates[2].Weight)
		assertPermitsAre(t, access.PermitsForModel("openai", "o3"), twice)
	})
}

func TestAccess_GrantedProvidersForMergesBases(t *testing.T) {
	first, second, project := twoProfilesAndAProject()

	t.Run("deduplicated, in base order", func(t *testing.T) {
		access := NewAccess(held(first, second), nil, "", nil)
		assert.Equal(t, []string{"openai", "bedrock", "anthropic"}, access.GrantedProvidersForModel(""))
		assert.Equal(t, []string{"openai", "bedrock"}, access.GrantedProvidersForModel("gpt-4o"))
		assert.Equal(t, []string{"bedrock", "openai"}, access.GrantedProvidersForModel("o3"),
			"openai is named by the first permit that grants it for the model")
	})

	t.Run("under an intersection only what the scope also allows by name", func(t *testing.T) {
		access := NewAccess(held(first, second), project, Intersect, nil)
		assert.Equal(t, []string{"openai"}, access.GrantedProvidersForModel("gpt-4o"))
		assert.Equal(t, []string{"openai"}, access.GrantedProvidersForModel(""))
	})

	t.Run("under a union the scope's own providers follow", func(t *testing.T) {
		access := NewAccess(held(first, second), project, Union, nil)
		assert.Equal(t, []string{"openai", "bedrock", "gemini"}, access.GrantedProvidersForModel("gpt-4o"))
	})
}

func TestAccess_MCPIncludeListMergesBases(t *testing.T) {
	first, second, project := twoProfilesAndAProject()

	t.Run("entries merge in base order", func(t *testing.T) {
		access := NewAccess(held(first, second), nil, "", nil)
		assert.Equal(t, []string{"github-read_file", "slack-post_message", "github-list_issues"}, access.MCPToolIncludeList())
	})

	t.Run("then compose with the scope", func(t *testing.T) {
		unionAccess := NewAccess(held(first, second), project, Union, nil)
		assert.Equal(t, []string{"github-read_file", "slack-post_message", "github-list_issues", "jira-*"}, unionAccess.MCPToolIncludeList())

		intersectAccess := NewAccess(held(first, second), project, Intersect, nil)
		assert.Empty(t, intersectAccess.MCPToolIncludeList(), "the project holds none of the caller's clients")
	})

	t.Run("a tool no base grants names every base", func(t *testing.T) {
		access := NewAccess(held(first, second), nil, "", nil)
		denied := access.DeniedPermitsForMCPTool("jira-create_issue")
		require.Len(t, denied, 2)
		assert.Same(t, first, denied[0])
		assert.Same(t, second, denied[1])

		assert.Nil(t, access.DeniedPermitsForMCPTool("github-list_issues"), "granted by the second")
	})
}

func TestAccess_DeniedByNamesEveryRefusingSide(t *testing.T) {
	first, second, project := twoProfilesAndAProject()

	t.Run("every base when none permits", func(t *testing.T) {
		access := NewAccess(held(first, second), nil, "", nil)
		denied := access.DeniedPermitsForModel("anthropic", "claude-opus-4")
		require.Len(t, denied, 2)
		assert.Same(t, first, denied[0])
		assert.Same(t, second, denied[1])
	})

	t.Run("under a union, the scope too when it refused", func(t *testing.T) {
		access := NewAccess(held(first, second), project, Union, nil)
		denied := access.DeniedPermitsForModel("cohere", "command-r")
		require.Len(t, denied, 3)
		assert.Same(t, first, denied[0])
		assert.Same(t, second, denied[1])
		assert.Same(t, project, denied[2])

		assert.Nil(t, access.DeniedPermitsForModel("gemini", "gemini-pro"), "the scope permits it")
	})

	t.Run("under an intersection, only the scope when a base permits", func(t *testing.T) {
		access := NewAccess(held(first, second), project, Intersect, nil)
		denied := access.DeniedPermitsForModel("bedrock", "claude-sonnet-4")
		require.Len(t, denied, 1)
		assert.Same(t, project, denied[0])

		denied = access.DeniedPermitsForModel("cohere", "command-r")
		require.Len(t, denied, 2, "no base permits, and the scope's refusal changes nothing")
		assert.Same(t, first, denied[0])
		assert.Same(t, second, denied[1])
	})
}

func TestAccess_NarrowMCPIncludeList(t *testing.T) {
	specific := permitWithTools(PermitVirtualKey, "vk1", "Caller Key", "sentry", "find_projects", "search_issues")
	everyTool := permitWithTools(PermitVirtualKey, "vk2", "Caller Key", "sentry", "*")

	t.Run("a specific entry is kept when permitted and dropped when not", func(t *testing.T) {
		access := NewAccess(held(specific), nil, "", nil)
		assert.Equal(t, []string{"sentry-find_projects"}, access.NarrowMCPToolIncludeList([]string{"sentry-find_projects", "sentry-delete_project"}))
	})

	t.Run("a specific entry narrows within a client granted every tool", func(t *testing.T) {
		access := NewAccess(held(everyTool), nil, "", nil)
		assert.Equal(t, []string{"sentry-search_issues"}, access.NarrowMCPToolIncludeList([]string{"sentry-search_issues"}))
	})

	t.Run("a wildcard is kept only when the client is granted every tool", func(t *testing.T) {
		assert.Equal(t, []string{"sentry-*"}, NewAccess(held(everyTool), nil, "", nil).NarrowMCPToolIncludeList([]string{"sentry-*"}))
		assert.Equal(t, []string{"sentry-find_projects", "sentry-search_issues"},
			NewAccess(held(specific), nil, "", nil).NarrowMCPToolIncludeList([]string{"sentry-*"}),
			"passing the wildcard through would read as every tool of the client")
	})

	t.Run("a wildcard for a client not granted at all yields nothing", func(t *testing.T) {
		access := NewAccess(held(specific), nil, "", nil)
		assert.Equal(t, []string{}, access.NarrowMCPToolIncludeList([]string{"github-*"}))
	})

	t.Run("empties are dropped and repeats collapse across a wildcard and a specific entry", func(t *testing.T) {
		access := NewAccess(held(specific), nil, "", nil)
		assert.Equal(t, []string{"sentry-find_projects", "sentry-search_issues"},
			access.NarrowMCPToolIncludeList([]string{"", "sentry-*", "sentry-find_projects"}))
	})

	t.Run("an empty request permits nothing, and so does no request", func(t *testing.T) {
		access := NewAccess(held(everyTool), nil, "", nil)
		assert.Equal(t, []string{}, access.NarrowMCPToolIncludeList([]string{""}))
		assert.Equal(t, []string{}, access.NarrowMCPToolIncludeList(nil))
	})

	t.Run("no access permits nothing", func(t *testing.T) {
		var access *Access
		assert.Equal(t, []string{}, access.NarrowMCPToolIncludeList([]string{"sentry-*"}))
	})

	t.Run("narrowing answers for the composed access", func(t *testing.T) {
		scoping := permitWithTools("other", "o1", "Other", "sentry", "find_projects")
		access := NewAccess(held(everyTool), scoping, Intersect, nil)
		assert.Equal(t, []string{"sentry-find_projects"}, access.NarrowMCPToolIncludeList([]string{"sentry-*", "sentry-search_issues"}),
			"the wildcard narrows to what both sides permit, and a tool the scope withholds is dropped")
	})
}

// A candidate is only ever offered by a permit that permits the model on its provider, and every
// such permit is among those the request answers to for the pair, which is what lets the permit be
// left off the candidate: whoever needs to know what a candidate costs asks PermitsForModel with
// the candidate's provider and the model. The walk below reproduces ProvidersForModel's own
// acceptance rule to recover the offering permit of every candidate, in order, and pins that
// PermitsForModel names it. A candidate offered through the scoping permit alone is one no base
// could serve, so the scoping permit is then the only permit named.
func TestAccess_CandidatesAreOfferedByPermitsForModel(t *testing.T) {
	first, second, project := twoProfilesAndAProject()
	twice := newPermit(permitSpec{
		Type: PermitAccessProfile, ID: "ap3", Name: "Twice",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-a"}, Weight: ptr(0.2)},
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-b"}, Weight: ptr(0.8)},
		},
	})
	accesses := map[string]*Access{
		"two bases, union with a project":     NewAccess(held(first, second), project, Union, nil),
		"two bases, intersect with a project": NewAccess(held(first, second), project, Intersect, nil),
		"two bases, unscoped":                 NewAccess(held(first, second), nil, "", nil),
		"second base first":                   NewAccess(held(second, first), project, Union, nil),
		"one base holding a provider twice":   NewAccess(held(first, twice), project, Union, nil),
		"project alone":                       NewAccess(nil, project, Union, nil),
	}
	models := []string{"gpt-4o", "o3", "claude-opus-4", "claude-sonnet-4", "gemini-pro", "nothing-permits-this"}

	type offer struct {
		provider string
		owner    schemas.Permit
	}
	for name, access := range accesses {
		for _, model := range models {
			var offers []offer
			access.eachProviderPermit(func(owner schemas.Permit, pp *schemas.ProviderPermit, _ bool) bool {
				if !access.IsModelAllowed(pp.Provider, model) || blacklistsModel(owner, pp.Provider, model) || !access.permitsModel(pp, model) {
					return false
				}
				offers = append(offers, offer{provider: pp.Provider, owner: owner})
				return true
			})
			candidates := access.ProvidersForModel(model)
			require.Len(t, candidates, len(offers), "%s / %s", name, model)
			for i, candidate := range candidates {
				assert.Equal(t, offers[i].provider, candidate.Provider, "%s / %s / candidate %d", name, model, i)
				permits := access.PermitsForModel(candidate.Provider, model)
				assert.True(t, containsPermit(permits, offers[i].owner),
					"%s / %s / %s: the offering permit is among those the request answers to", name, model, candidate.Provider)
				if offers[i].owner == access.Scoping() {
					assertPermitsAre(t, permits, access.Scoping())
				}
			}
		}
	}
}

// containsPermit reports whether permits holds want, by identity.
func containsPermit(permits []schemas.Permit, want schemas.Permit) bool {
	for _, permit := range permits {
		if permit == want {
			return true
		}
	}
	return false
}

// assertPermitsAre pins the exact permits, in order, by identity.
func assertPermitsAre(t *testing.T, got []schemas.Permit, want ...schemas.Permit) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		assert.Same(t, want[i], got[i], "permit %d", i)
	}
}
