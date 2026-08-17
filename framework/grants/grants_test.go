package grants

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A grant on its own, before anything folds it with another: what it permits, what governs it, and
// the projections that build one. Composition lives in access_test.go.

// The limits are flat and each says what it covers, so a caller asks the grant one question and
// never has to know which level of a holder configured what.
func TestLimitsHeldBy(t *testing.T) {
	t.Run("scoped to one provider and one model", func(t *testing.T) {
		limits := LimitsHeldBy(LimitHolderVirtualKeyModelConfig, "mc-7", "gpt-4o on openai", "openai", "gpt-4o", "b-1", "b-2")

		require.Len(t, limits, 2)
		assert.Equal(t, Limit{
			ID: "b-1", HolderKind: LimitHolderVirtualKeyModelConfig,
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

		// A provider config's names the provider but not the model.
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

// A grant mixes limits from several holders, so a check specific to one holder has to ask about
// that holder. The failure this prevents is quiet: gating a key's budget check on "any budget at
// all" fires it whenever the team has one, and the key's check then passes while the team's budget
// is exhausted.
func TestLimitsFrom(t *testing.T) {
	limits := []Limit{
		{ID: "b-key", HolderKind: LimitHolderVirtualKey, HolderID: "vk1"},
		{ID: "b-key-openai", HolderKind: LimitHolderVirtualKeyProviderConfig, HolderID: "1", Provider: "openai"},
		{ID: "b-team", HolderKind: LimitHolderTeam, HolderID: "team-1"},
		{ID: "b-customer", HolderKind: LimitHolderCustomer, HolderID: "cust-1"},
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
		teamOnly := []Limit{{ID: "b-team", HolderKind: LimitHolderTeam, HolderID: "team-1"}}
		assert.NotEmpty(t, teamOnly)
		assert.Empty(t, LimitsFrom(teamOnly, LimitHolderVirtualKey, LimitHolderVirtualKeyProviderConfig))
	})
}

// A grant answers for what funds its use of one provider, and only that: the limits its configs for
// that provider carry. What funds the holder across every provider is not the grant's to answer,
// because it is not a fact about which provider serves the request.
func TestGrant_LimitsFor(t *testing.T) {
	grant := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1", Name: "Caller Key",
		ProviderConfigGrants: []ProviderConfigGrant{
			{
				Provider:   "openai",
				Budgets:    []Limit{{ID: "b-openai", HolderKind: "vk_provider_config", HolderID: "1", Provider: "openai"}},
				RateLimits: []Limit{{ID: "r-openai", HolderKind: "vk_provider_config", HolderID: "1", Provider: "openai"}},
			},
			{
				Provider: "bedrock",
				Budgets:  []Limit{{ID: "b-bedrock", HolderKind: "vk_provider_config", HolderID: "2", Provider: "bedrock"}},
			},
			// Granted, and funded by nothing of its own.
			{Provider: "anthropic"},
		},
	}

	t.Run("each provider answers with its own configs' limits", func(t *testing.T) {
		assert.Equal(t, []string{"b-openai"}, limitIDs(grant.BudgetsFor("openai")))
		assert.Equal(t, []string{"b-bedrock"}, limitIDs(grant.BudgetsFor("bedrock")))
		assert.Empty(t, grant.BudgetsFor("anthropic"),
			"granted but funded by nothing of its own, which is not the same as unlimited")
	})

	t.Run("budgets and rate limits are asked separately", func(t *testing.T) {
		assert.Equal(t, []string{"r-openai"}, limitIDs(grant.RateLimitsFor("openai")))
		assert.Empty(t, grant.RateLimitsFor("bedrock"), "nothing rate-limits bedrock here")
	})

	t.Run("naming no provider asks about nothing", func(t *testing.T) {
		// There is no unscoped bucket to fall back to: a limit that covers every provider lives
		// outside the grant, so asking without a provider cannot find one.
		assert.Nil(t, grant.BudgetsFor(""))
		assert.Nil(t, grant.RateLimitsFor(""))
	})

	t.Run("each limit says whose it is", func(t *testing.T) {
		// A refusal has to name what refused, and two limits of the same shape are told apart by
		// their holder — which a bare identifier could not do.
		budgets := grant.BudgetsFor("openai")
		require.Len(t, budgets, 1)
		assert.Equal(t, LimitHolderKind("vk_provider_config"), budgets[0].HolderKind)
		assert.Equal(t, "1", budgets[0].HolderID)
	})

	t.Run("a grant governed by nothing", func(t *testing.T) {
		bare := &Grant{Type: GrantTypeVirtualKey, ID: "vk2"}
		assert.Empty(t, bare.BudgetsFor("openai"))
		assert.Empty(t, bare.RateLimitsFor("openai"))

		var missing *Grant
		assert.Nil(t, missing.BudgetsFor("openai"))
		assert.Nil(t, missing.RateLimitsFor("openai"))
	})
}

// A grant that is not there permits nothing and governs nothing. Every predicate takes a nil
// receiver because the slots of an EffectiveAccess are routinely empty — a caller holding no grant
// of their own, or nothing scoping the request — and the fold asks all of them unconditionally
// rather than guarding each call.
func TestGrant_NilReceiver(t *testing.T) {
	var grant *Grant

	assert.False(t, grant.allowsProvider("openai"))
	assert.False(t, grant.blacklistsModel("openai", "gpt-4o"))
	assert.False(t, grant.allowsTool("github-read_file"))
	assert.False(t, grant.grantAllowsModelByName("openai", "gpt-4o"))
	assert.Nil(t, grant.providerConfigFor("openai"))
	assert.Nil(t, grant.weightedProviderConfigFor("openai"))
	assert.Nil(t, grant.BudgetsFor("openai"))
	assert.Nil(t, grant.RateLimitsFor("openai"))

	// An empty list rather than nil: no tool may be executed, which a consumer must be able to
	// tell from no answer at all.
	entries := grant.mcpEntries()
	assert.NotNil(t, entries)
	assert.Empty(t, entries)
}

func TestGrant_GrantAllowsModelByName(t *testing.T) {
	grant := &Grant{
		Type: GrantTypeVirtualKey, ID: "vk1",
		ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}, BlacklistedModels: schemas.BlackList{"o3"}},
			{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}},
		},
	}

	assert.True(t, grant.grantAllowsModelByName("openai", "gpt-4o"))
	assert.False(t, grant.grantAllowsModelByName("openai", "gpt-4o-mini"), "outside the allowlist")
	assert.False(t, grant.grantAllowsModelByName("openai", "o3"), "blacklisted")
	assert.False(t, grant.grantAllowsModelByName("cohere", "command-r"), "provider not held")

	// No model is nothing to filter on, so holding the provider is the whole question. This is
	// what lets a listing ask "which providers are granted at all".
	assert.True(t, grant.grantAllowsModelByName("openai", ""))
	assert.True(t, grant.grantAllowsModelByName("anthropic", ""))
	assert.False(t, grant.grantAllowsModelByName("cohere", ""))
}

// eachProviderConfigOf is the walk the fold shares, and it stops when the visitor says so — the
// coarse gate uses that to stop once it has an answer.
func TestGrant_EachProviderConfigOf(t *testing.T) {
	grant := grantWithProviders(GrantTypeVirtualKey, "vk1", "Key", "openai", "anthropic", "bedrock")

	t.Run("visits every config", func(t *testing.T) {
		seen := []string{}
		completed := grant.eachProviderConfigOf(func(pc *ProviderConfigGrant) bool {
			seen = append(seen, pc.Provider)
			return true
		})
		assert.True(t, completed)
		assert.Equal(t, []string{"openai", "anthropic", "bedrock"}, seen)
	})

	t.Run("stops when the visitor says to, and reports it", func(t *testing.T) {
		seen := []string{}
		completed := grant.eachProviderConfigOf(func(pc *ProviderConfigGrant) bool {
			seen = append(seen, pc.Provider)
			return pc.Provider != "anthropic"
		})
		assert.False(t, completed, "the caller has to know the walk was cut short")
		assert.Equal(t, []string{"openai", "anthropic"}, seen)
	})

	t.Run("a provider named by nothing but whitespace is skipped", func(t *testing.T) {
		// No comparison anywhere would match it, so it could only be selected and then fail
		// downstream.
		blank := &Grant{Type: GrantTypeVirtualKey, ID: "vk2", ProviderConfigGrants: []ProviderConfigGrant{
			{Provider: "  ", AllowedModels: schemas.WhiteList{"*"}},
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}},
		}}
		seen := []string{}
		blank.eachProviderConfigOf(func(pc *ProviderConfigGrant) bool {
			seen = append(seen, pc.Provider)
			return true
		})
		assert.Equal(t, []string{"openai"}, seen)
	})
}

// mcpEntries expands a grant's MCP configs into the tool patterns it permits. The rules that are
// easy to get wrong: which config decides for a client, and what an unrestricted client expands to.
func TestGrant_MCPEntries(t *testing.T) {
	t.Run("specific tools become one entry each", func(t *testing.T) {
		grant := grantWithTools(GrantTypeVirtualKey, "vk1", "Key", "github", "read_file", "list_issues")
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, grant.mcpEntries())
	})

	t.Run("an unrestricted client becomes a single wildcard", func(t *testing.T) {
		grant := grantWithTools(GrantTypeVirtualKey, "vk1", "Key", "github", "*")
		assert.Equal(t, []string{"github-*"}, grant.mcpEntries())
	})

	t.Run("a client granted no tool contributes nothing", func(t *testing.T) {
		grant := grantWithTools(GrantTypeVirtualKey, "vk1", "Key", "github")
		assert.Empty(t, grant.mcpEntries())
	})

	t.Run("the first config holding a client decides for it", func(t *testing.T) {
		// A second config for the same client cannot widen the first, which is what stops a
		// permissive duplicate from reopening a narrowed client.
		grant := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", MCPConfigGrants: []MCPConfigGrant{
			{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file"}},
			{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"*"}},
		}}
		assert.Equal(t, []string{"github-read_file"}, grant.mcpEntries())
	})

	t.Run("a client is identified by its id, so a rename does not split it", func(t *testing.T) {
		grant := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", MCPConfigGrants: []MCPConfigGrant{
			{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file"}},
			{Client: "github-id", ClientName: "github-renamed", Tools: schemas.WhiteList{"*"}},
		}}
		assert.Equal(t, []string{"github-read_file"}, grant.mcpEntries(),
			"the same client under two names is still one client")
	})

	t.Run("with no id, the name identifies the client", func(t *testing.T) {
		grant := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", MCPConfigGrants: []MCPConfigGrant{
			{ClientName: "github", Tools: schemas.WhiteList{"read_file"}},
			{ClientName: "github", Tools: schemas.WhiteList{"*"}},
		}}
		assert.Equal(t, []string{"github-read_file"}, grant.mcpEntries())
	})

	t.Run("a config naming no client is skipped", func(t *testing.T) {
		// Its entries would be "-tool", which matches no client anywhere.
		grant := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", MCPConfigGrants: []MCPConfigGrant{
			{Client: "orphan-id", Tools: schemas.WhiteList{"read_file"}},
			{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file"}},
		}}
		assert.Equal(t, []string{"github-read_file"}, grant.mcpEntries())
	})

	t.Run("an unnamed tool is skipped", func(t *testing.T) {
		grant := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", MCPConfigGrants: []MCPConfigGrant{
			{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file", "", "list_issues"}},
		}}
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, grant.mcpEntries())
	})

	t.Run("duplicate entries collapse", func(t *testing.T) {
		grant := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", MCPConfigGrants: []MCPConfigGrant{
			{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file", "read_file"}},
		}}
		assert.Equal(t, []string{"github-read_file"}, grant.mcpEntries())
	})
}

// allowsTool answers for one pattern rather than expanding the whole list, and the two have to
// agree. A wildcard pattern asks whether the client is granted anything at all.
func TestGrant_AllowsTool(t *testing.T) {
	grant := &Grant{Type: GrantTypeVirtualKey, ID: "vk1", MCPConfigGrants: []MCPConfigGrant{
		{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file"}},
		{Client: "slack-id", ClientName: "slack", Tools: schemas.WhiteList{"*"}},
		{Client: "jira-id", ClientName: "jira", Tools: schemas.WhiteList{}},
		{Client: "orphan-id", Tools: schemas.WhiteList{"*"}},
	}}

	assert.True(t, grant.allowsTool("github-read_file"))
	assert.False(t, grant.allowsTool("github-delete_repo"))
	assert.True(t, grant.allowsTool("github-*"), "the client is granted some tool")

	assert.True(t, grant.allowsTool("slack-anything"), "an unrestricted client permits every tool")
	assert.True(t, grant.allowsTool("slack-*"))

	assert.False(t, grant.allowsTool("jira-create_issue"), "granted no tool")
	assert.False(t, grant.allowsTool("jira-*"))

	assert.False(t, grant.allowsTool("unknown-tool"), "client not held")
	assert.False(t, grant.allowsTool(""))
	assert.False(t, grant.allowsTool("github"), "a bare client name is not a tool pattern")

	// A config naming no client can never match, so it cannot grant through the empty prefix.
	assert.False(t, grant.allowsTool("-read_file"))
}

// The access a request carries when nothing granted it anything. It has to permit everything, because
// that is what a request with no credential and no profile has always been able to do — but it must do
// that by saying so, not by being absent: an absent grant permits nothing, and the two are one bit apart.
func TestUnrestrictedGrant(t *testing.T) {
	t.Run("the type is the only thing that says so", func(t *testing.T) {
		// Nothing else records it, so nothing else can disagree with it — a grant of another type is
		// restricted however it was built.
		assert.True(t, UnrestrictedGrant().isUnrestricted())
		assert.False(t, grantWithProviders(GrantTypeVirtualKey, "vk1", "Key", "openai").isUnrestricted())

		var missing *Grant
		assert.False(t, missing.isUnrestricted(), "and no grant permits nothing, as everywhere else")
	})

	t.Run("permits providers it was never told about", func(t *testing.T) {
		// A deployment with nothing configured must not read as a grant that permits nothing, so the
		// answer cannot come from the enumerated configs.
		grant := UnrestrictedGrant()

		assert.True(t, grant.allowsProvider("openai"))
		assert.True(t, grant.grantAllowsModelByName("openai", "gpt-4o"))
		assert.True(t, grant.allowsTool("some-client-some_tool"))
	})

	t.Run("enumerates nothing, and does not need to", func(t *testing.T) {
		// Every question a consumer asks is answered by an empty grant of this type: a key restriction it
		// does not name is no restriction, and a provider list it does not narrow is one the consumer
		// leaves alone. Listing the deployment's providers here would be a second copy of what the
		// deployment already knows.
		grant := UnrestrictedGrant()

		assert.Empty(t, grantProviderConfigs(grant))
		assert.Empty(t, grant.MCPConfigGrants)
	})

	t.Run("restricts no key and prefers no provider", func(t *testing.T) {
		access := NewEffectiveAccess(UnrestrictedGrant(), nil, "", nil, nil)

		keyIDs, restricted := access.KeysFor("openai")
		assert.False(t, restricted, "every key of a provider may serve the request")
		assert.Nil(t, keyIDs)

		// And no candidate is offered, so a request nothing granted is routed exactly as it was rather
		// than balanced across the deployment.
		assert.Nil(t, access.ProvidersFor("gpt-4o"))
	})

	t.Run("is active, and nothing about it can be refused", func(t *testing.T) {
		grant := UnrestrictedGrant()

		assert.True(t, grant.IsActive, "a fail-closed IsActive would refuse every keyless request")
		assert.False(t, grant.IsExpired)
		assert.Equal(t, GrantTypeUnrestricted, grant.Type)
		assert.Equal(t, "unrestricted access", grant.Type.PrettyString())
	})

	t.Run("access built over it reports itself unrestricted", func(t *testing.T) {
		// What a consumer that narrows something reads, so it leaves it alone instead of narrowing to
		// the empty set.
		assert.True(t, NewEffectiveAccess(UnrestrictedGrant(), nil, "", nil, nil).IsUnrestricted())
		assert.False(t, NewEffectiveAccess(grantWithProviders(GrantTypeVirtualKey, "vk1", "Key", "openai"), nil, "", nil, nil).IsUnrestricted())

		var missing *EffectiveAccess
		assert.False(t, missing.IsUnrestricted(), "no access is not unrestricted access")
	})

	t.Run("it funds nothing of its own", func(t *testing.T) {
		// Nobody holds it, so there is nobody to charge. What a request answers to still comes from the
		// deployment's own limits, which are not the grant's.
		grant := UnrestrictedGrant()

		assert.Empty(t, grant.BudgetsFor("openai"))
		assert.Empty(t, grant.RateLimitsFor("openai"))
	})
}

// grantProviderConfigs lists a grant's provider configs, for assertions about what it enumerates.
func grantProviderConfigs(g *Grant) []ProviderConfigGrant {
	configs := []ProviderConfigGrant{}
	g.eachProviderConfigOf(func(pc *ProviderConfigGrant) bool {
		configs = append(configs, *pc)
		return true
	})
	return configs
}
