package grants

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// EffectiveAccess is a request's resolved access: the grant its caller holds, the grant scoping
// it, and the mode between them. What is covered here is the fold — every question a consumer can
// ask of a request, and how the two slots combine to answer it.

func TestEffectiveAccess_DegenerateCases(t *testing.T) {
	baseGrant := grantWithProviders(GrantTypeVirtualKey, "vk1", "Caller Key", "openai")
	scopingGrant := grantWithProviders("other", "o1", "Other", "anthropic")

	tests := []struct {
		name          string
		base          *Grant
		scoping       *Grant
		mode          GrantCompositionMode
		wantOpenAI    bool
		wantAnthropic bool
	}{
		{
			name:          "a base grant, nothing scoping it: mode is irrelevant",
			base:          baseGrant,
			mode:          GrantModeIntersect,
			wantOpenAI:    true,
			wantAnthropic: false,
		},
		{
			name:          "neither slot filled: nothing is permitted",
			wantOpenAI:    false,
			wantAnthropic: false,
		},
		{
			name:          "no base grant, union: the scoping grant is the whole access",
			scoping:       scopingGrant,
			mode:          GrantModeUnion,
			wantOpenAI:    false,
			wantAnthropic: true,
		},
		{
			name:          "no base grant, intersect: nothing is permitted",
			scoping:       scopingGrant,
			mode:          GrantModeIntersect,
			wantOpenAI:    false,
			wantAnthropic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			access := NewEffectiveAccess(tt.base, tt.scoping, tt.mode, nil, nil)

			assert.Equal(t, tt.wantOpenAI, access.IsProviderAllowed("openai"), "openai provider")
			assert.Equal(t, tt.wantOpenAI, access.IsModelAllowed("openai", "gpt-4o"), "openai model")
			assert.Equal(t, tt.wantAnthropic, access.IsProviderAllowed("anthropic"), "anthropic provider")
			assert.Equal(t, tt.wantAnthropic, access.IsModelAllowed("anthropic", "claude-sonnet-4"), "anthropic model")
		})
	}
}

func TestEffectiveAccess_NilReceiverPermitsNothing(t *testing.T) {
	var access *EffectiveAccess

	assert.False(t, access.IsProviderAllowed("openai"))
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.False(t, access.IsMCPToolAllowed("client-tool"))
	assert.False(t, access.IsScoped())
	assert.Nil(t, access.Base())
	assert.Nil(t, access.Scoping())
	assert.Equal(t, GrantCompositionMode(""), access.Mode())
	assert.Empty(t, access.GrantedProvidersFor("gpt-4o"))
	assert.Empty(t, access.ProvidersFor("gpt-4o"))
	assert.Empty(t, access.MCPIncludeList())
	assert.Nil(t, access.DenyingGrant("openai", "gpt-4o"))
	assert.Nil(t, access.DenyingGrantForTool("client-tool"))

	keyIDs, restricted := access.KeysFor("openai")
	assert.Nil(t, keyIDs)
	assert.False(t, restricted)
}

// An empty slot is not a grant permitting nothing, and the two must stay distinguishable:
// consumers branch on whether a request has access resolved at all.
func TestEffectiveAccess_EmptySlotIsNotAnEmptyGrant(t *testing.T) {
	empty := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", Name: "Empty Key"}

	withEmptyGrant := NewEffectiveAccess(empty, nil, "", nil, nil)
	assert.NotNil(t, withEmptyGrant.Base(), "the caller holds a grant, which happens to permit nothing")
	assert.False(t, withEmptyGrant.IsProviderAllowed("openai"))
	require.NotNil(t, withEmptyGrant.DenyingGrant("openai", ""))
	assert.Equal(t, "Empty Key", withEmptyGrant.DenyingGrant("openai", "").Name)

	withNoGrant := NewEffectiveAccess(nil, nil, "", nil, nil)
	assert.Nil(t, withNoGrant.Base(), "the caller holds nothing at all")
	assert.False(t, withNoGrant.IsProviderAllowed("openai"))
	assert.Nil(t, withNoGrant.DenyingGrant("openai", ""), "there is no grant to name")
}

func TestEffectiveAccess_ProviderFold(t *testing.T) {
	base := grantWithProviders(GrantTypeVirtualKey, "vk1", "Caller Key", "openai", "bedrock")
	scoping := grantWithProviders("other", "o1", "Other", "openai", "anthropic")

	unionAccess := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
	intersectAccess := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)

	// Held by both.
	assert.True(t, unionAccess.IsProviderAllowed("openai"))
	assert.True(t, intersectAccess.IsProviderAllowed("openai"))

	// Base only.
	assert.True(t, unionAccess.IsProviderAllowed("bedrock"))
	assert.False(t, intersectAccess.IsProviderAllowed("bedrock"))

	// Scoping grant only.
	assert.True(t, unionAccess.IsProviderAllowed("anthropic"))
	assert.False(t, intersectAccess.IsProviderAllowed("anthropic"))

	// Neither.
	assert.False(t, unionAccess.IsProviderAllowed("cohere"))
	assert.False(t, intersectAccess.IsProviderAllowed("cohere"))
}

func TestEffectiveAccess_ModelFold(t *testing.T) {
	base := grantWithModels(GrantTypeVirtualKey, "vk1", "Caller Key", "openai", "gpt-4o", "gpt-4o-mini")
	scoping := grantWithModels("other", "o1", "Other", "openai", "gpt-4o", "o3")

	unionAccess := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
	intersectAccess := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)

	assert.True(t, unionAccess.IsModelAllowed("openai", "gpt-4o"))
	assert.True(t, intersectAccess.IsModelAllowed("openai", "gpt-4o"))

	assert.True(t, unionAccess.IsModelAllowed("openai", "gpt-4o-mini"))
	assert.False(t, intersectAccess.IsModelAllowed("openai", "gpt-4o-mini"))

	assert.True(t, unionAccess.IsModelAllowed("openai", "o3"))
	assert.False(t, intersectAccess.IsModelAllowed("openai", "o3"))

	assert.False(t, unionAccess.IsModelAllowed("openai", "gpt-3.5-turbo"))
	assert.False(t, intersectAccess.IsModelAllowed("openai", "gpt-3.5-turbo"))
}

// Within one grant, several configs for a provider are still an any-of: the multiplicity that
// went away was across grants, not inside one.
func TestEffectiveAccess_ConfigsWithinAGrantAreAnyOf(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}},
			{Provider: "openai", AllowedModels: schemas.WhiteList{"o3"}},
		},
	}
	access := NewEffectiveAccess(base, nil, "", nil, nil)

	assert.True(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.True(t, access.IsModelAllowed("openai", "o3"))
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o-mini"))
}

func TestEffectiveAccess_BlacklistWins(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}},
			// A second, permissive config for the same provider must not reopen it.
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}},
		},
	}
	access := NewEffectiveAccess(base, nil, "", nil, nil)

	assert.True(t, access.IsProviderAllowed("openai"), "the provider itself stays permitted")
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o"), "blacklisted in one config blocks the provider for that model")
	assert.True(t, access.IsModelAllowed("openai", "o3"))

	// A blacklist on the scoping grant blocks under union too: union widens by what the scoping
	// grant permits, and it does not permit the model at all.
	scoping := &Grant{
		Type: "other", ID: "o1", Name: "Other",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"claude-opus-4"}},
		},
	}
	unionAccess := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
	assert.True(t, unionAccess.IsModelAllowed("anthropic", "claude-sonnet-4"))
	assert.False(t, unionAccess.IsModelAllowed("anthropic", "claude-opus-4"))
}

func TestEffectiveAccess_UnknownModeFailsClosed(t *testing.T) {
	base := grantWithProviders(GrantTypeVirtualKey, "vk1", "Caller Key", "openai")
	scoping := grantWithProviders("other", "o1", "Other", "openai")

	access := NewEffectiveAccess(base, scoping, "something-new", nil, nil)

	assert.False(t, access.IsProviderAllowed("openai"))
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.Empty(t, access.GrantedProvidersFor("gpt-4o"))
	assert.Empty(t, access.MCPIncludeList())
}

func TestEffectiveAccess_ModeIsInertWithoutAScopingGrant(t *testing.T) {
	// A mode with nothing to compose says nothing, whatever it is set to.
	base := grantWithProviders(GrantTypeVirtualKey, "vk1", "Caller Key", "openai")

	for _, mode := range []GrantCompositionMode{GrantModeIntersect, GrantModeUnion, "something-new", ""} {
		access := NewEffectiveAccess(base, nil, mode, nil, nil)
		assert.True(t, access.IsProviderAllowed("openai"), "mode %q", mode)
		assert.False(t, access.IsScoped(), "mode %q", mode)
	}
}

func TestEffectiveAccess_ModelNamesResolveThroughTheCatalog(t *testing.T) {
	// The catalog is what makes "*" mean "every model this provider actually serves"
	// rather than "every string", so the same grant answers differently with and without
	// one. Without a catalog, entries are matched by name.
	unrestricted := grantWithModels(GrantTypeVirtualKey, "vk1", "Caller Key", "openai", "*")
	catalog := modelcatalog.NewTestCatalog(nil)
	store := &fakeProviderConfigSource{configuredProviders: nil}

	byName := NewEffectiveAccess(unrestricted, nil, "", nil, nil)
	assert.True(t, byName.IsModelAllowed("openai", "not-a-real-model"))

	withCatalog := NewEffectiveAccess(unrestricted, nil, "", catalog, store)
	assert.False(t, withCatalog.IsModelAllowed("openai", "not-a-real-model"),
		"the catalog does not place this model at this provider")

	// A named entry still matches exactly, catalog or not.
	named := grantWithModels(GrantTypeVirtualKey, "vk2", "Caller Key", "openai", "gpt-4o")
	assert.True(t, NewEffectiveAccess(named, nil, "", catalog, store).IsModelAllowed("openai", "gpt-4o"))
	assert.False(t, NewEffectiveAccess(named, nil, "", catalog, store).IsModelAllowed("openai", "o3"))

	// Blacklists never go through the catalog: they are name matches, and they win.
	blacklisted := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk3", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{{
			Provider:          "openai",
			AllowedModels:     schemas.WhiteList{"gpt-4o"},
			BlacklistedModels: schemas.BlackList{"gpt-4o"},
		}},
	}
	assert.False(t, NewEffectiveAccess(blacklisted, nil, "", catalog, store).IsModelAllowed("openai", "gpt-4o"))

	// Half the dependency is no dependency: without a store there is no provider config
	// to resolve against, so matching stays by name.
	assert.True(t, NewEffectiveAccess(unrestricted, nil, "", catalog, nil).IsModelAllowed("openai", "not-a-real-model"))
}

func TestEffectiveAccess_KeysFor(t *testing.T) {
	baseRestricted := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"key-own"}},
			// Only the first config for a provider decides.
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"key-ignored"}},
			{Provider: "bedrock", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"}},
			{Provider: "cohere", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{}},
		},
	}
	scoping := &Grant{
		Type: "other", ID: "o1", Name: "Other",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"key-scoping"}},
			{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"key-scoping"}},
		},
	}

	access := NewEffectiveAccess(baseRestricted, scoping, GrantModeUnion, nil, nil)

	// Both slots hold openai, so under a union the request may use either one's keys. The
	// assertion also pins the return type: consumers type-assert []string, so a
	// schemas.WhiteList would fail their assertion silently and read as "no key restriction".
	keyIDs, restricted := access.KeysFor("openai")
	assert.True(t, restricted)
	assert.Equal(t, []string{"key-own", "key-scoping"}, keyIDs)
	assert.IsType(t, []string{}, keyIDs)

	// A provider gained purely through the scoping grant uses the scoping grant's keys.
	keyIDs, restricted = access.KeysFor("anthropic")
	assert.True(t, restricted)
	assert.Equal(t, []string{"key-scoping"}, keyIDs)

	// Every key of the provider is allowed: no restriction to stamp anywhere.
	keyIDs, restricted = access.KeysFor("bedrock")
	assert.False(t, restricted)
	assert.Nil(t, keyIDs)

	// An empty restriction allows no key, and must not read as "unrestricted".
	keyIDs, restricted = access.KeysFor("cohere")
	assert.True(t, restricted)
	assert.NotNil(t, keyIDs)
	assert.Empty(t, keyIDs)

	// Unknown provider.
	keyIDs, restricted = access.KeysFor("mistral")
	assert.False(t, restricted)
	assert.Nil(t, keyIDs)
}

func TestEffectiveAccess_KeysForComposes(t *testing.T) {
	withKeys := func(id string, keyIDs ...string) *Grant {
		return &Grant{
			Type: GrantType(id), ID: id, Name: id,
			ProviderConfigGrants: []ProviderConfigGrant{
				{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList(keyIDs)},
			},
		}
	}

	// The reason keys have to compose at all: a scoping grant that narrows the request to two of
	// the provider's keys means it, and the base grant must not reopen the ones it excluded.
	t.Run("a scoping grant narrows the keys it excluded", func(t *testing.T) {
		access := NewEffectiveAccess(
			withKeys("base", "key-a", "key-b"),
			withKeys("scoping", "key-b", "key-c"),
			GrantModeIntersect, nil, nil,
		)
		keyIDs, restricted := access.KeysFor("openai")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-b"}, keyIDs, "key-a is the caller's but not the scope's")
	})

	t.Run("union widens", func(t *testing.T) {
		access := NewEffectiveAccess(
			withKeys("base", "key-a"),
			withKeys("scoping", "key-b"),
			GrantModeUnion, nil, nil,
		)
		keyIDs, restricted := access.KeysFor("openai")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-a", "key-b"}, keyIDs)
	})

	// Disjoint sets under an intersection leave no key that may serve the provider, which is a
	// restriction to nothing rather than the absence of one.
	t.Run("disjoint under intersect allows no key", func(t *testing.T) {
		access := NewEffectiveAccess(
			withKeys("base", "key-a"),
			withKeys("scoping", "key-b"),
			GrantModeIntersect, nil, nil,
		)
		keyIDs, restricted := access.KeysFor("openai")
		assert.True(t, restricted)
		assert.NotNil(t, keyIDs)
		assert.Empty(t, keyIDs)
	})

	// The wildcard is the universe, not an entry.
	t.Run("the wildcard composes as every key", func(t *testing.T) {
		unrestricted := withKeys("scoping", "*")

		intersected := NewEffectiveAccess(withKeys("base", "key-a"), unrestricted, GrantModeIntersect, nil, nil)
		keyIDs, restricted := intersected.KeysFor("openai")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-a"}, keyIDs, "intersecting with every key is the other side")

		narrowed := NewEffectiveAccess(withKeys("base", "*"), withKeys("scoping", "key-b"), GrantModeIntersect, nil, nil)
		keyIDs, restricted = narrowed.KeysFor("openai")
		assert.True(t, restricted)
		assert.Equal(t, []string{"key-b"}, keyIDs, "a scope may narrow a caller who held every key")

		widened := NewEffectiveAccess(withKeys("base", "key-a"), unrestricted, GrantModeUnion, nil, nil)
		keyIDs, restricted = widened.KeysFor("openai")
		assert.False(t, restricted, "unioning with every key is no restriction at all")
		assert.Nil(t, keyIDs)

		bothOpen := NewEffectiveAccess(withKeys("base", "*"), unrestricted, GrantModeIntersect, nil, nil)
		keyIDs, restricted = bothOpen.KeysFor("openai")
		assert.False(t, restricted)
		assert.Nil(t, keyIDs)
	})

	t.Run("an unknown mode allows no key", func(t *testing.T) {
		access := NewEffectiveAccess(withKeys("base", "key-a"), withKeys("scoping", "key-a"), "something-new", nil, nil)
		keyIDs, restricted := access.KeysFor("openai")
		assert.True(t, restricted)
		assert.Empty(t, keyIDs)
	})

	t.Run("only one slot holds the provider", func(t *testing.T) {
		// Nothing to compose with, so that slot's keys stand as they are.
		baseOnly := NewEffectiveAccess(withKeys("base", "key-a"), grantWithProviders("scoping", "s", "S", "anthropic"), GrantModeIntersect, nil, nil)
		keyIDs, _ := baseOnly.KeysFor("openai")
		assert.Equal(t, []string{"key-a"}, keyIDs)

		unscoped := NewEffectiveAccess(withKeys("base", "key-a"), nil, GrantModeIntersect, nil, nil)
		keyIDs, _ = unscoped.KeysFor("openai")
		assert.Equal(t, []string{"key-a"}, keyIDs)
	})

	// "This config names no key" and "no config was found" both arrive as an empty list, and they
	// are opposite answers: the first permits no key, the second imposes no restriction. Deciding
	// on emptiness rather than on whether a config was found turns the strictest possible
	// restriction into the loosest.
	t.Run("a config recording no key permits none", func(t *testing.T) {
		noKeys := &Grant{
			Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
			ProviderConfigGrants: []ProviderConfigGrant{{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}}},
		}
		access := NewEffectiveAccess(noKeys, nil, "", nil, nil)

		keyIDs, restricted := access.KeysFor("openai")
		assert.True(t, restricted, "a config with no key list restricts to no key")
		assert.NotNil(t, keyIDs)
		assert.Empty(t, keyIDs)

		keyIDs, restricted = access.KeysFor("anthropic")
		assert.False(t, restricted, "a provider neither slot holds has no restriction to report")
		assert.Nil(t, keyIDs)
	})

	t.Run("key ids match exactly", func(t *testing.T) {
		// Key IDs are opaque, so a case-folding comparison would intersect two different keys
		// into one and hand the request a key neither slot granted.
		access := NewEffectiveAccess(withKeys("base", "Key-A"), withKeys("scoping", "key-a"), GrantModeIntersect, nil, nil)
		keyIDs, restricted := access.KeysFor("openai")
		assert.True(t, restricted)
		assert.Empty(t, keyIDs)
	})
}

func TestEffectiveAccess_KeysForReturnsACopy(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"key-own"}},
		},
	}
	access := NewEffectiveAccess(base, nil, "", nil, nil)

	keyIDs, _ := access.KeysFor("openai")
	keyIDs[0] = "mutated"

	fresh, _ := access.KeysFor("openai")
	assert.Equal(t, []string{"key-own"}, fresh, "a consumer must not be able to edit the grant")
}

// Weight is a preference, so it does not intersect: the scoping grant sets it where it has an
// opinion, and the candidate's own weight stands where it does not.
func TestEffectiveAccess_WeightFollowsTheScopingGrant(t *testing.T) {
	weighted := func(id string, weight *float64) *Grant {
		return &Grant{
			Type: GrantType(id), ID: id, Name: id,
			ProviderConfigGrants: []ProviderConfigGrant{
				{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"}, Weight: weight},
			},
		}
	}

	for _, mode := range []GrantCompositionMode{GrantModeUnion, GrantModeIntersect} {
		access := NewEffectiveAccess(weighted("base", schemas.Ptr(0.7)), weighted("scoping", schemas.Ptr(0.1)), mode, nil, nil)
		candidates := access.ProvidersFor("gpt-4o")
		require.Len(t, candidates, 1)
		assert.Equal(t, schemas.Ptr(0.1), candidates[0].Weight, "mode %q", mode)
		assert.Same(t, access.Base(), candidates[0].Grant, "the base grant still serves it", "mode %q", mode)
	}

	t.Run("a scoping grant with no weight leaves the base weight alone", func(t *testing.T) {
		access := NewEffectiveAccess(weighted("base", schemas.Ptr(0.7)), weighted("scoping", nil), GrantModeIntersect, nil, nil)
		candidates := access.ProvidersFor("gpt-4o")
		require.Len(t, candidates, 1)
		assert.Equal(t, schemas.Ptr(0.7), candidates[0].Weight)
	})

	t.Run("no scoping grant leaves the base weight alone", func(t *testing.T) {
		access := NewEffectiveAccess(weighted("base", schemas.Ptr(0.7)), nil, "", nil, nil)
		candidates := access.ProvidersFor("gpt-4o")
		require.Len(t, candidates, 1)
		assert.Equal(t, schemas.Ptr(0.7), candidates[0].Weight)
	})
}

func TestEffectiveAccess_ProvidersFor(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{
				Provider:      "openai",
				AllowedModels: schemas.WhiteList{"*"},
				KeyIDs:        schemas.WhiteList{"key-own"},
				Weight:        schemas.Ptr(0.7),
			},
			{
				// No weight: still a candidate, so the caller can see it and decide.
				Provider:      "bedrock",
				AllowedModels: schemas.WhiteList{"*"},
				KeyIDs:        schemas.WhiteList{"*"},
			},
			{
				// Does not permit the model.
				Provider:      "cohere",
				AllowedModels: schemas.WhiteList{"command-r"},
			},
		},
	}
	scoping := &Grant{
		Type: "other", ID: "o1", Name: "Other",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"key-scoping"}, Weight: schemas.Ptr(0.1)},
			{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"key-scoping"}, Weight: schemas.Ptr(0.3)},
		},
	}

	t.Run("base grant only", func(t *testing.T) {
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		candidates := access.ProvidersFor("gpt-4o")

		require.Len(t, candidates, 2)
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, schemas.Ptr(0.7), candidates[0].Weight)
		assert.Same(t, base, candidates[0].Grant,
			"the grant is what a caller asks for the limits covering this provider")
		assert.Equal(t, "bedrock", candidates[1].Provider)
		assert.Nil(t, candidates[1].Weight)
	})

	t.Run("union adds providers with the scoping grant's keys and weight", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
		candidates := access.ProvidersFor("gpt-4o")

		require.Len(t, candidates, 3)
		// A provider both slots hold is served once, by the base grant, under the union of their
		// keys and the weight the scoping grant sets for it.
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, schemas.Ptr(0.1), candidates[0].Weight, "the scoping grant's preference")
		assert.Equal(t, schemas.WhiteList{"key-own", "key-scoping"}, candidates[0].KeyIDs)
		assert.Equal(t, "bedrock", candidates[1].Provider)
		// The added provider operates under the scoping grant.
		assert.Equal(t, "anthropic", candidates[2].Provider)
		assert.Equal(t, schemas.Ptr(0.3), candidates[2].Weight)
		assert.Equal(t, schemas.WhiteList{"key-scoping"}, candidates[2].KeyIDs)
		assert.Same(t, scoping, candidates[2].Grant)
	})

	t.Run("intersect never adds a provider", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		candidates := access.ProvidersFor("gpt-4o")

		require.Len(t, candidates, 1)
		assert.Equal(t, "openai", candidates[0].Provider)
		assert.Equal(t, schemas.Ptr(0.1), candidates[0].Weight, "the scoping grant's preference")
		// The two key lists are disjoint, so intersecting them leaves no key that may serve this
		// candidate. It is still reported: whether an unusable candidate is worth attempting is
		// the caller's decision, the same as an unweighted one.
		assert.NotNil(t, candidates[0].KeyIDs)
		assert.Empty(t, candidates[0].KeyIDs)
	})

	t.Run("no model means no candidates", func(t *testing.T) {
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		assert.Empty(t, access.ProvidersFor(""))
	})
}

func TestEffectiveAccess_ProvidersForPerConfigGranularity(t *testing.T) {
	// Two configs for one provider: only the one permitting the model is a candidate,
	// and a blacklist in either of them removes both.
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"o3"}, Weight: schemas.Ptr(0.2)},
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}, Weight: schemas.Ptr(0.8)},
		},
	}
	access := NewEffectiveAccess(base, nil, "", nil, nil)

	candidates := access.ProvidersFor("gpt-4o")
	require.Len(t, candidates, 1)
	assert.Equal(t, schemas.Ptr(0.8), candidates[0].Weight)

	blacklisted := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk2", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}, Weight: schemas.Ptr(0.2)},
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}, Weight: schemas.Ptr(0.8)},
		},
	}
	assert.Empty(t, NewEffectiveAccess(blacklisted, nil, "", nil, nil).ProvidersFor("gpt-4o"))
}

// A union means either slot can be the one authorizing a request, which makes "the access
// permits this pair" and "this grant permits this pair" different questions. A grant that
// blacklists the model must not serve it even while the other slot permits it — and the request
// must still be served, by the slot that does.
func TestEffectiveAccess_UnionServesFromTheGrantThatPermits(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{{
			Provider:          "openai",
			AllowedModels:     schemas.WhiteList{"*"},
			BlacklistedModels: schemas.BlackList{"gpt-4o"},
			KeyIDs:            schemas.WhiteList{"key-own"},
		}},
	}
	scoping := &Grant{
		Type: "other", ID: "o1", Name: "Other",
		ProviderConfigGrants: []ProviderConfigGrant{{
			Provider:      "openai",
			AllowedModels: schemas.WhiteList{"*"},
			KeyIDs:        schemas.WhiteList{"key-scoping"},
		}},
	}
	access := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)

	require.True(t, access.IsModelAllowed("openai", "gpt-4o"), "the scoping grant permits it")

	candidates := access.ProvidersFor("gpt-4o")
	require.Len(t, candidates, 1, "the request is permitted, so something must be able to serve it")
	assert.Same(t, scoping, candidates[0].Grant, "served by the grant that permits the model, not the one that blacklists it")
	assert.Equal(t, schemas.WhiteList{"key-scoping"}, candidates[0].KeyIDs,
		"the blacklisting grant does not get to say which keys serve a request it refused")

	// The coarse gate has to agree: a provider the base grant holds but cannot serve is still
	// granted, through the scoping grant.
	assert.Equal(t, []schemas.ModelProvider{"openai"}, access.GrantedProvidersFor("gpt-4o"))

	// A model both slots permit is served by the base grant, as usual.
	other := access.ProvidersFor("o3")
	require.Len(t, other, 1)
	assert.Same(t, base, other[0].Grant)
}

func TestEffectiveAccess_GrantedProvidersFor(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}},
			{Provider: "bedrock", AllowedModels: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}},
			{Provider: "  ", AllowedModels: schemas.WhiteList{"*"}},
		},
	}
	scoping := &Grant{
		Type: "other", ID: "o1", Name: "Other",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}},
			{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}},
		},
	}

	t.Run("base grant only", func(t *testing.T) {
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		assert.Equal(t, []schemas.ModelProvider{"openai"}, access.GrantedProvidersFor("gpt-4o"))
		// No model to filter on keeps every granted provider.
		assert.Equal(t, []schemas.ModelProvider{"openai", "bedrock"}, access.GrantedProvidersFor(""))
	})

	t.Run("union", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
		assert.Equal(t, []schemas.ModelProvider{"openai", "anthropic"}, access.GrantedProvidersFor("gpt-4o"))
	})

	t.Run("intersect", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		assert.Equal(t, []schemas.ModelProvider{"openai"}, access.GrantedProvidersFor("gpt-4o"))

		// A provider the caller holds but the scoping grant does not is dropped.
		narrow := grantWithProviders("other", "o2", "Other", "anthropic")
		narrowAccess := NewEffectiveAccess(base, narrow, GrantModeIntersect, nil, nil)
		assert.Empty(t, narrowAccess.GrantedProvidersFor("gpt-4o"))
	})

	t.Run("empty is not nil", func(t *testing.T) {
		// An empty allowlist means "no provider is permitted", which a consumer must be
		// able to tell apart from "nothing was published".
		access := NewEffectiveAccess(nil, nil, "", nil, nil)
		providers := access.GrantedProvidersFor("gpt-4o")
		assert.NotNil(t, providers)
		assert.Empty(t, providers)
	})
}

// The coarse gate reports providers, not configs, so a grant holding several configs for one
// provider still names it once. A consumer treats the result as a set — a repeated provider would
// read as two ways in where there is one.
func TestEffectiveAccess_GrantedProvidersForDeduplicates(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}},
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o", "o3"}},
			{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}},
		},
	}
	access := NewEffectiveAccess(base, nil, "", nil, nil)

	assert.Equal(t, []schemas.ModelProvider{"openai", "anthropic"}, access.GrantedProvidersFor("gpt-4o"),
		"both openai configs permit it, and it is still one provider")
	assert.Equal(t, []schemas.ModelProvider{"openai", "anthropic"}, access.GrantedProvidersFor(""))

	// A model only the second config permits still names the provider once.
	assert.Equal(t, []schemas.ModelProvider{"openai", "anthropic"}, access.GrantedProvidersFor("o3"))

	// And ProvidersFor, which reports configs rather than providers, sees both — the two answer
	// different questions about the same grant.
	require.Len(t, access.ProvidersFor("gpt-4o"), 3, "two openai configs plus anthropic")
}

func TestEffectiveAccess_GrantedProvidersForIgnoresTheCatalog(t *testing.T) {
	// This coarse gate must not resolve model names even when a catalog is available:
	// the layers consuming it run their own resolution.
	base := grantWithModels(GrantTypeVirtualKey, "vk1", "Caller Key", "openai", "*")
	catalog := modelcatalog.NewTestCatalog(nil)
	store := &fakeProviderConfigSource{}

	access := NewEffectiveAccess(base, nil, "", catalog, store)
	assert.Equal(t, []schemas.ModelProvider{"openai"}, access.GrantedProvidersFor("not-a-real-model"))
	assert.False(t, access.IsModelAllowed("openai", "not-a-real-model"), "the exact check does resolve it")
}

func TestEffectiveAccess_IsMCPToolAllowed(t *testing.T) {
	base := grantWithTools(GrantTypeVirtualKey, "vk1", "Caller Key", "github", "read_file", "list_issues")
	scoping := grantWithTools("other", "o1", "Other", "github", "read_file")

	t.Run("base grant only", func(t *testing.T) {
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		assert.True(t, access.IsMCPToolAllowed("github-read_file"))
		assert.True(t, access.IsMCPToolAllowed("github-list_issues"))
		assert.False(t, access.IsMCPToolAllowed("github-delete_repo"))
		assert.False(t, access.IsMCPToolAllowed("slack-post_message"))
		assert.True(t, access.IsMCPToolAllowed("github-*"), "the client is granted some tool")
		assert.False(t, access.IsMCPToolAllowed(""))
	})

	t.Run("intersect", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		assert.True(t, access.IsMCPToolAllowed("github-read_file"))
		assert.False(t, access.IsMCPToolAllowed("github-list_issues"))
	})

	t.Run("union", func(t *testing.T) {
		wider := grantWithTools("other", "o2", "Other", "slack", "post_message")
		access := NewEffectiveAccess(base, wider, GrantModeUnion, nil, nil)
		assert.True(t, access.IsMCPToolAllowed("github-list_issues"))
		assert.True(t, access.IsMCPToolAllowed("slack-post_message"))
		assert.False(t, access.IsMCPToolAllowed("slack-delete_channel"))
	})

	t.Run("a client with no tools granted is not permitted", func(t *testing.T) {
		empty := grantWithTools(GrantTypeVirtualKey, "vk2", "Caller Key", "github")
		access := NewEffectiveAccess(empty, nil, "", nil, nil)
		assert.False(t, access.IsMCPToolAllowed("github-read_file"))
		assert.False(t, access.IsMCPToolAllowed("github-*"))
	})

	t.Run("an unrestricted client permits every tool", func(t *testing.T) {
		all := grantWithTools(GrantTypeVirtualKey, "vk3", "Caller Key", "github", "*")
		access := NewEffectiveAccess(all, nil, "", nil, nil)
		assert.True(t, access.IsMCPToolAllowed("github-anything"))
		assert.True(t, access.IsMCPToolAllowed("github-*"))
	})

	t.Run("within a grant, the first config for a client is the answer", func(t *testing.T) {
		single := &Grant{
			Type: GrantTypeVirtualKey, ID: "vk4", Name: "Caller Key",
			MCPConfigGrants: []MCPConfigGrant{
				{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file"}},
				{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"*"}},
			},
		}
		assert.False(t, NewEffectiveAccess(single, nil, "", nil, nil).IsMCPToolAllowed("github-delete_repo"))
	})
}

func TestEffectiveAccess_MCPIncludeList(t *testing.T) {
	base := grantWithTools(GrantTypeVirtualKey, "vk1", "Caller Key", "github", "read_file", "list_issues")

	t.Run("base grant only", func(t *testing.T) {
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, access.MCPIncludeList())
	})

	t.Run("an unrestricted client becomes a wildcard entry", func(t *testing.T) {
		all := grantWithTools(GrantTypeVirtualKey, "vk2", "Caller Key", "github", "*")
		access := NewEffectiveAccess(all, nil, "", nil, nil)
		assert.Equal(t, []string{"github-*"}, access.MCPIncludeList())
	})

	t.Run("a client with no tools granted contributes nothing", func(t *testing.T) {
		empty := grantWithTools(GrantTypeVirtualKey, "vk3", "Caller Key", "github")
		access := NewEffectiveAccess(empty, nil, "", nil, nil)
		assert.Empty(t, access.MCPIncludeList())
	})

	t.Run("union merges both slots", func(t *testing.T) {
		scoping := grantWithTools("other", "o1", "Other", "slack", "post_message")
		access := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
		assert.Equal(t, []string{"github-read_file", "github-list_issues", "slack-post_message"}, access.MCPIncludeList())
	})

	t.Run("intersect keeps what both slots permit", func(t *testing.T) {
		scoping := grantWithTools("other", "o1", "Other", "github", "read_file")
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		assert.Equal(t, []string{"github-read_file"}, access.MCPIncludeList())
	})

	t.Run("intersect narrows a wildcard down to the other slot's tools", func(t *testing.T) {
		wildcardBase := grantWithTools(GrantTypeVirtualKey, "vk4", "Caller Key", "github", "*")
		scoping := grantWithTools("other", "o1", "Other", "github", "read_file")
		access := NewEffectiveAccess(wildcardBase, scoping, GrantModeIntersect, nil, nil)
		assert.Equal(t, []string{"github-read_file"}, access.MCPIncludeList(),
			"passing the wildcard through would read as every tool of the client")
	})

	// The mirror of the case above, and the reason narrowing consults the scoping grant rather
	// than its entry list: an unrestricted client expands to a bare "github-*", so asking whether
	// that list contains "github-read_file" answers no for every tool the scope in fact permits.
	t.Run("intersect keeps specific tools against an unrestricted scope", func(t *testing.T) {
		scoping := grantWithTools("other", "o1", "Other", "github", "*")
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, access.MCPIncludeList(),
			"intersecting a specific list with every tool is the specific list")
	})

	t.Run("intersect keeps a wildcard only when both slots are unrestricted", func(t *testing.T) {
		wildcardBase := grantWithTools(GrantTypeVirtualKey, "vk5", "Caller Key", "github", "*")
		wildcardScoping := grantWithTools("other", "o1", "Other", "github", "*")
		access := NewEffectiveAccess(wildcardBase, wildcardScoping, GrantModeIntersect, nil, nil)
		assert.Equal(t, []string{"github-*"}, access.MCPIncludeList())
	})

	t.Run("intersect drops clients the other slot does not hold", func(t *testing.T) {
		scoping := grantWithTools("other", "o1", "Other", "slack", "post_message")
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		assert.Empty(t, access.MCPIncludeList())
	})
}

// Limits are the one part of a grant that does not compose under the mode. A request has to be
// affordable to everything covering it, so both slots' limits bind — and bind identically whether the
// scoping grant widened access or narrowed it.
func TestEffectiveAccess_LimitsDoNotComposeUnderTheMode(t *testing.T) {
	base := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{
				Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"},
				Budgets:    []Limit{{ID: "b-key-openai", HolderKind: "vk_provider_config", HolderID: "1", Provider: "openai"}},
				RateLimits: []Limit{{ID: "r-key-openai", HolderKind: "vk_provider_config", HolderID: "1", Provider: "openai"}},
			},
		},
	}
	scoping := &Grant{
		Type: "project", ID: "p1", Name: "Some Project",
		ProviderConfigGrants: []ProviderConfigGrant{
			{
				Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"},
				Budgets: []Limit{{ID: "b-project-openai", HolderKind: "project", HolderID: "p1", Provider: "openai"}},
			},
			{
				Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"},
				Budgets: []Limit{{ID: "b-project-anthropic", HolderKind: "project", HolderID: "p1", Provider: "anthropic"}},
			},
		},
	}

	for _, mode := range []GrantCompositionMode{GrantModeUnion, GrantModeIntersect} {
		access := NewEffectiveAccess(base, scoping, mode, nil, nil)

		assert.Equal(t, []string{"b-key-openai", "b-project-openai"}, limitIDs(access.BudgetsFor("openai")),
			"mode %q: both slots' budgets bind, the base grant's first", mode)
		assert.Equal(t, []string{"r-key-openai"}, limitIDs(access.RateLimitsFor("openai")), "mode %q", mode)
	}

	// A provider the base grant authorized on its own is still inside the scope, so the scoping
	// grant's spend on it still counts — even under a union, where the base needed no help to reach it.
	unionAccess := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
	assert.Equal(t, []string{"b-project-anthropic"}, limitIDs(unionAccess.BudgetsFor("anthropic")),
		"the scope funds the provider only it granted")

	t.Run("one slot only", func(t *testing.T) {
		unscoped := NewEffectiveAccess(base, nil, "", nil, nil)
		assert.Equal(t, []string{"b-key-openai"}, limitIDs(unscoped.BudgetsFor("openai")))

		scopeOnly := NewEffectiveAccess(nil, scoping, GrantModeUnion, nil, nil)
		assert.Equal(t, []string{"b-project-openai"}, limitIDs(scopeOnly.BudgetsFor("openai")))
	})

	t.Run("nothing to answer with", func(t *testing.T) {
		var missing *EffectiveAccess
		assert.Nil(t, missing.BudgetsFor("openai"))
		assert.Nil(t, missing.RateLimitsFor("openai"))

		bare := NewEffectiveAccess(&Grant{ID: "vk2"}, nil, "", nil, nil)
		assert.Empty(t, bare.BudgetsFor("openai"))
		assert.Empty(t, bare.RateLimitsFor("openai"))
	})

	// Reading the answer must not let a caller edit either grant's list through it.
	t.Run("the answer is a copy", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		budgets := access.BudgetsFor("openai")
		require.NotEmpty(t, budgets)
		budgets[0].ID = "mutated"
		assert.Equal(t, "b-key-openai", base.ProviderConfigGrants[0].Budgets[0].ID)
		assert.Equal(t, "b-key-openai", access.BudgetsFor("openai")[0].ID)
	})
}

func TestEffectiveAccess_DenyingGrant(t *testing.T) {
	base := grantWithModels(GrantTypeVirtualKey, "vk1", "Caller Key", "openai", "gpt-4o")
	scoping := grantWithModels("other", "o1", "Other Name", "openai", "o3")

	t.Run("allowed requests have no denying grant", func(t *testing.T) {
		access := NewEffectiveAccess(base, nil, "", nil, nil)
		assert.Nil(t, access.DenyingGrant("openai", "gpt-4o"))
		assert.Nil(t, access.DenyingGrant("openai", ""))
	})

	t.Run("the base grant is named when it denies", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
		denying := access.DenyingGrant("anthropic", "claude-sonnet-4")
		require.NotNil(t, denying)
		assert.Equal(t, "Caller Key", denying.Name)
		assert.Equal(t, GrantTypeVirtualKey, denying.Type)
	})

	t.Run("the scoping grant is named when it denies", func(t *testing.T) {
		access := NewEffectiveAccess(base, scoping, GrantModeIntersect, nil, nil)
		denying := access.DenyingGrant("openai", "gpt-4o")
		require.NotNil(t, denying)
		assert.Equal(t, "Other Name", denying.Name)
		assert.Equal(t, GrantType("other"), denying.Type)

		// Provider-level denials are attributed the same way.
		narrow := grantWithProviders("other", "o2", "Narrow", "anthropic")
		providerAccess := NewEffectiveAccess(base, narrow, GrantModeIntersect, nil, nil)
		denying = providerAccess.DenyingGrant("openai", "")
		require.NotNil(t, denying)
		assert.Equal(t, "Narrow", denying.Name)
	})

	// With one grant per slot the denying grant is never ambiguous: whichever slot refused
	// holds exactly one grant, and that is the one the denial quotes.
	t.Run("a refused request always names a grant", func(t *testing.T) {
		for _, mode := range []GrantCompositionMode{GrantModeUnion, GrantModeIntersect} {
			access := NewEffectiveAccess(base, scoping, mode, nil, nil)
			for _, model := range []string{"gpt-4o", "o3", "gpt-4o-mini"} {
				if access.IsModelAllowed("openai", model) {
					continue
				}
				assert.NotNil(t, access.DenyingGrant("openai", model), "mode %q model %q", mode, model)
			}
		}
	})

	t.Run("nothing is named when the refusing slot is empty", func(t *testing.T) {
		access := NewEffectiveAccess(nil, scoping, GrantModeIntersect, nil, nil)
		assert.False(t, access.IsModelAllowed("openai", "o3"))
		assert.Nil(t, access.DenyingGrant("openai", "o3"), "the caller holds no grant to name")
	})

	t.Run("tool denials are attributed too", func(t *testing.T) {
		baseTools := grantWithTools(GrantTypeVirtualKey, "vk1", "Caller Key", "github", "read_file")
		scopingTools := grantWithTools("other", "o1", "Other Name", "github", "list_issues")

		access := NewEffectiveAccess(baseTools, scopingTools, GrantModeIntersect, nil, nil)

		denying := access.DenyingGrantForTool("github-read_file")
		require.NotNil(t, denying)
		assert.Equal(t, "Other Name", denying.Name)

		denying = access.DenyingGrantForTool("github-list_issues")
		require.NotNil(t, denying)
		assert.Equal(t, "Caller Key", denying.Name)

		assert.Nil(t, access.DenyingGrantForTool("github-*"), "both slots grant the client some tool")
	})
}

func TestEffectiveAccess_Accessors(t *testing.T) {
	base := grantWithProviders(GrantTypeVirtualKey, "vk1", "Caller Key", "openai")
	scoping := grantWithProviders("other", "o1", "Other", "anthropic")

	access := NewEffectiveAccess(base, scoping, GrantModeUnion, nil, nil)
	assert.Equal(t, GrantModeUnion, access.Mode())
	assert.True(t, access.IsScoped())
	assert.Same(t, base, access.Base())
	assert.Same(t, scoping, access.Scoping())

	bare := NewEffectiveAccess(base, nil, "", nil, nil)
	assert.False(t, bare.IsScoped())
	assert.Nil(t, bare.Scoping())
	assert.Equal(t, GrantCompositionMode(""), bare.Mode())
}

// The limits a request answers to are resolved by its caller and held as one list. This package does
// not work them out: which holders are charged is a question about how a deployment is configured, and
// answering it here would mean learning what a project, a team or a model config is.
func TestEffectiveAccessResolvedLimits(t *testing.T) {
	access := NewEffectiveAccess(
		grantWithProviders(GrantTypeVirtualKey, "vk1", "Key", "openai", "bedrock"), nil, "", nil, nil)
	budgets := []Limit{
		{ID: "b-provider", HolderKind: "provider", HolderID: "openai", Provider: "openai"},
		{ID: "b-key", HolderKind: LimitHolderVirtualKey, HolderID: "vk1"},
	}
	rateLimits := []Limit{{ID: "r-team", HolderKind: LimitHolderTeam, HolderID: "team-1"}}

	t.Run("nothing resolved reads as nothing resolved", func(t *testing.T) {
		// Not as "answers to nothing": a caller has to tell a request whose limits are not settled
		// yet from one that is genuinely unlimited.
		assert.Nil(t, access.ResolvedBudgets())
		assert.Nil(t, access.ResolvedRateLimits())
	})

	t.Run("what was resolved is what comes back, in order", func(t *testing.T) {
		resolved := access.WithResolvedLimits(budgets, rateLimits)

		assert.Equal(t, []string{"b-provider", "b-key"}, limitIDs(resolved.ResolvedBudgets()),
			"the order given, which is the order refusals report in")
		assert.Equal(t, []string{"r-team"}, limitIDs(resolved.ResolvedRateLimits()))
	})

	t.Run("the access it came from is untouched", func(t *testing.T) {
		access.WithResolvedLimits(budgets, rateLimits)

		assert.Nil(t, access.ResolvedBudgets(), "a copy, so what earlier decisions were made against still reads")
	})

	t.Run("what the request may reach is unchanged", func(t *testing.T) {
		resolved := access.WithResolvedLimits(budgets, rateLimits)

		assert.True(t, resolved.IsProviderAllowed("openai"))
		assert.True(t, resolved.IsProviderAllowed("bedrock"), "resolving limits does not narrow reach")
		assert.Equal(t, access.Mode(), resolved.Mode())
		assert.Equal(t, access.Base().ID, resolved.Base().ID)
	})

	t.Run("the caller cannot alter what the request is held to", func(t *testing.T) {
		mine := append([]Limit(nil), budgets...)
		resolved := access.WithResolvedLimits(mine, nil)
		mine[0].ID = "mutated"

		assert.Equal(t, "b-provider", resolved.ResolvedBudgets()[0].ID)
	})

	t.Run("a limit reached twice is one limit", func(t *testing.T) {
		// Holders overlap: a team or customer can be reached through both slots, and a customer can be
		// named directly as well as through its team. Charging the same budget twice for one request is
		// never what that meant.
		team := Limit{ID: "b-team", HolderKind: LimitHolderTeam, HolderID: "team-1"}
		resolved := access.WithResolvedLimits([]Limit{
			{ID: "b-provider", HolderKind: "provider", HolderID: "openai", Provider: "openai"},
			team,
			{ID: "b-key", HolderKind: LimitHolderVirtualKey, HolderID: "vk1"},
			team,
		}, []Limit{
			{ID: "r-team", HolderKind: LimitHolderTeam, HolderID: "team-1"},
			{ID: "r-team", HolderKind: LimitHolderTeam, HolderID: "team-1"},
		})

		assert.Equal(t, []string{"b-provider", "b-team", "b-key"}, limitIDs(resolved.ResolvedBudgets()),
			"the first occurrence, so the order refusals report in survives")
		assert.Equal(t, []string{"r-team"}, limitIDs(resolved.ResolvedRateLimits()))
	})

	t.Run("the same limit reached as two holders keeps the first", func(t *testing.T) {
		// A degenerate configuration, but it has to resolve to something deterministic rather than to
		// one row billed under two names.
		resolved := access.WithResolvedLimits([]Limit{
			{ID: "b-shared", HolderKind: LimitHolderTeam, HolderID: "team-1"},
			{ID: "b-shared", HolderKind: LimitHolderCustomer, HolderID: "cust-1"},
		}, nil)

		require.Len(t, resolved.ResolvedBudgets(), 1)
		assert.Equal(t, LimitHolderTeam, resolved.ResolvedBudgets()[0].HolderKind)
	})

	t.Run("no access resolves to no access", func(t *testing.T) {
		var missing *EffectiveAccess
		assert.Nil(t, missing.WithResolvedLimits(budgets, rateLimits))
		assert.Nil(t, missing.ResolvedBudgets())
		assert.Nil(t, missing.ResolvedRateLimits())
	})
}
