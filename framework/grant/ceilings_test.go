package grant

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ceilings cap the caller's permits without granting anything of their own. Each layer is read as
// one permit, the caller's side permits only what its permits and every layer permit, and a scoping
// permit composes with the capped result.

func TestAccessCeilings_NoCeilingsLeavesTheAccessAsItWas(t *testing.T) {
	base := permitWithModels(PermitVirtualKey, "vk1", "Caller", "openai", "gpt-4o")

	plain := NewAccess(held(base), nil, "", nil)
	capped := NewAccess(held(base), nil, "", nil, WithCeilings(), WithCeilings(nil, held(nil)))

	assert.Nil(t, capped.Ceilings(), "empty layers cap nothing and are dropped")
	assert.Equal(t, plain.IsModelAllowed("openai", "gpt-4o"), capped.IsModelAllowed("openai", "gpt-4o"))
	assert.Equal(t, plain.IsModelAllowed("openai", "gpt-4o-mini"), capped.IsModelAllowed("openai", "gpt-4o-mini"))
}

func TestAccessCeilings_ACeilingTakesAwayWhatTheCallerHolds(t *testing.T) {
	// The caller holds models a and b; the layer above allows only c. Nothing is left.
	caller := permitWithModels(PermitAccessProfile, "ap1", "Member", "openai", "model-a", "model-b")
	team := permitWithModels(PermitTeamAccessProfile, "t1", "Team", "openai", "model-c")

	access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))

	assert.False(t, access.IsModelAllowed("openai", "model-a"))
	assert.False(t, access.IsModelAllowed("openai", "model-b"))
	assert.False(t, access.IsModelAllowed("openai", "model-c"), "a ceiling grants nothing of its own")
	assert.Empty(t, access.ProvidersForModel("model-c"), "a ceiling never offers a candidate")
	assertPermitsAre(t, access.DeniedPermitsForModel("openai", "model-a"), team)
	assertPermitsAre(t, access.DeniedPermitsForModel("openai", "model-c"), caller)
}

func TestAccessCeilings_ALayerIsReadAsOne(t *testing.T) {
	caller := permitWithModels(PermitAccessProfile, "ap1", "Member", "openai", "model-a", "model-b")
	teamX := permitWithModels(PermitTeamAccessProfile, "t1", "Team X", "openai", "model-a")
	teamY := permitWithModels(PermitTeamAccessProfile, "t2", "Team Y", "openai", "model-b")

	access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(teamX, teamY)))

	assert.True(t, access.IsModelAllowed("openai", "model-a"), "team X allows it")
	assert.True(t, access.IsModelAllowed("openai", "model-b"), "team Y allows it")
	assertPermitsAre(t, access.PermitsForModel("openai", "model-a"), caller, teamX)
}

func TestAccessCeilings_EveryLayerMustAllow(t *testing.T) {
	caller := permitWithModels(PermitAccessProfile, "ap1", "Member", "openai", "model-a", "model-b")
	team := permitWithModels(PermitTeamAccessProfile, "t1", "Team", "openai", "model-a", "model-b")
	customer := permitWithModels(PermitCustomerAccessProfile, "c1", "Customer", "openai", "model-b")

	access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team), held(customer)))

	assert.False(t, access.IsModelAllowed("openai", "model-a"))
	assert.True(t, access.IsModelAllowed("openai", "model-b"))
	assertPermitsAre(t, access.DeniedPermitsForModel("openai", "model-a"), customer)
	assertPermitsAre(t, access.PermitsForModel("openai", "model-b"), caller, team, customer)
}

func TestAccessCeilings_ProvidersAndBlacklists(t *testing.T) {
	caller := permitWithProviders(PermitAccessProfile, "ap1", "Member", "openai", "anthropic")

	t.Run("a provider the layer does not permit is refused", func(t *testing.T) {
		team := permitWithProviders(PermitTeamAccessProfile, "t1", "Team", "anthropic")
		access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))
		assert.False(t, access.IsProviderAllowed("openai"))
		assert.True(t, access.IsProviderAllowed("anthropic"))
		assert.False(t, access.AllowsEveryModel("openai"))
		assert.True(t, access.AllowsEveryModel("anthropic"))
		assert.Equal(t, []string{"anthropic"}, access.GrantedProvidersForModel("claude-sonnet-4"))
	})

	t.Run("a blacklist in the layer blocks", func(t *testing.T) {
		team := newPermit(permitSpec{
			Type: PermitTeamAccessProfile, ID: "t1", Name: "Team", IsActive: true,
			ProviderPermits: []schemas.ProviderPermit{{
				Provider: "openai", AllowedModels: []string{"*"}, BlacklistedModels: []string{"gpt-4o"},
			}},
		})
		access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))
		assert.False(t, access.IsModelAllowed("openai", "gpt-4o"))
		assert.True(t, access.IsModelAllowed("openai", "gpt-4o-mini"))
		assert.Empty(t, access.GrantedProvidersForModel("gpt-4o"))
	})

	t.Run("a layer that allows all providers caps nothing", func(t *testing.T) {
		team := NewPermit(PermitTeamAccessProfile, "t1", "Team", true, false, nil, nil, WithAllowAllProviders(true))
		access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))
		assert.True(t, access.IsModelAllowed("openai", "gpt-4o"))
		assert.ElementsMatch(t, []string{"openai", "anthropic"}, access.GrantedProvidersForModel(""))
	})
}

func TestAccessCeilings_KeysAreNarrowed(t *testing.T) {
	caller := newPermit(permitSpec{
		Type: PermitAccessProfile, ID: "ap1", Name: "Member", IsActive: true,
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-a", "key-b"}},
			{Provider: "anthropic", AllowedModels: []string{"*"}, KeyIDs: []string{"*"}},
		},
	})
	team := newPermit(permitSpec{
		Type: PermitTeamAccessProfile, ID: "t1", Name: "Team", IsActive: true,
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-b", "key-c"}},
			{Provider: "anthropic", AllowedModels: []string{"*"}, KeyIDs: []string{"key-team"}},
		},
	})
	customer := NewPermit(PermitCustomerAccessProfile, "c1", "Customer", true, false, nil, nil, WithAllowAllProviders(true))

	access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team), held(customer)))

	keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
	assert.True(t, restricted)
	assert.Equal(t, []string{"key-b"}, keyIDs)

	keyIDs, restricted = access.KeysForModel("anthropic", "claude-sonnet-4")
	assert.True(t, restricted, "an unrestricted caller takes the layer's restriction")
	assert.Equal(t, []string{"key-team"}, keyIDs)

	// Without a matcher every candidate here allows every model, so both providers are offered;
	// each carries its own keys narrowed by the team's for that provider.
	candidates := access.ProvidersForModel("gpt-4o")
	require.Len(t, candidates, 2)
	assert.Equal(t, schemas.ProviderCandidate{Provider: "openai", KeyIDs: []string{"key-b"}}, candidates[0])
	assert.Equal(t, schemas.ProviderCandidate{Provider: "anthropic", KeyIDs: []string{"key-team"}}, candidates[1])
}

func TestAccessCeilings_MCPTools(t *testing.T) {
	caller := permitWithTools(PermitAccessProfile, "ap1", "Member", "github", "read_file", "list_issues", "create_issue")

	t.Run("a specific layer narrows a specific list", func(t *testing.T) {
		teamX := permitWithTools(PermitTeamAccessProfile, "t1", "Team X", "github", "read_file")
		teamY := permitWithTools(PermitTeamAccessProfile, "t2", "Team Y", "github", "list_issues")
		access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(teamX, teamY)))
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, access.MCPToolIncludeList())
		assert.True(t, access.IsMCPToolAllowed("github-read_file"))
		assert.False(t, access.IsMCPToolAllowed("github-create_issue"))
		assert.Equal(t, []string{"github-read_file"}, access.NarrowMCPToolIncludeList([]string{"github-read_file", "github-create_issue"}))
		assertPermitsAre(t, access.DeniedPermitsForMCPTool("github-create_issue"), teamX, teamY)
	})

	t.Run("an unrestricted layer keeps the specific list", func(t *testing.T) {
		team := permitWithTools(PermitTeamAccessProfile, "t1", "Team", "github", "*")
		access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))
		assert.Equal(t, []string{"github-read_file", "github-list_issues", "github-create_issue"}, access.MCPToolIncludeList())
	})

	t.Run("a wildcard caller is narrowed to the layer's tools", func(t *testing.T) {
		wildcard := permitWithTools(PermitAccessProfile, "ap2", "Member", "github", "*")
		team := permitWithTools(PermitTeamAccessProfile, "t1", "Team", "github", "read_file")
		access := NewAccess(held(wildcard), nil, "", nil, WithCeilings(held(team)))
		assert.Equal(t, []string{"github-read_file"}, access.MCPToolIncludeList())
	})

	t.Run("a client the layer does not hold is dropped", func(t *testing.T) {
		team := permitWithTools(PermitTeamAccessProfile, "t1", "Team", "slack", "post_message")
		access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))
		assert.Empty(t, access.MCPToolIncludeList())
	})
}

func TestAccessCeilings_AgentsAndRelays(t *testing.T) {
	caller := NewPermit(PermitAccessProfile, "ap1", "Member", true, false, nil, nil,
		WithAgentPermits([]string{"agent-a", "agent-b"}), WithRelayPermits([]string{"relay-a", "relay-b"}))
	team := NewPermit(PermitTeamAccessProfile, "t1", "Team", true, false, nil, nil,
		WithAgentPermits([]string{"agent-a"}), WithRelayPermits([]string{"relay-b"}))

	access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))

	assert.True(t, access.IsAgentAllowed("agent-a"))
	assert.False(t, access.IsAgentAllowed("agent-b"))
	assertPermitsAre(t, access.DeniedPermitsForAgent("agent-b"), team)
	assert.False(t, access.IsRelayAllowed("relay-a"))
	assert.True(t, access.IsRelayAllowed("relay-b"))
	assertPermitsAre(t, access.DeniedPermitsForRelay("relay-a"), team)
}

func TestAccessCeilings_ComposeWithAScope(t *testing.T) {
	caller := permitWithModels(PermitAccessProfile, "ap1", "Member", "openai", "model-a", "model-b")
	team := permitWithModels(PermitTeamAccessProfile, "t1", "Team", "openai", "model-a")
	project := permitWithModels(PermitProject, "p1", "Project", "openai", "model-a", "model-b", "model-c")

	t.Run("intersect narrows the capped caller further", func(t *testing.T) {
		narrow := permitWithModels(PermitProject, "p2", "Project", "openai", "model-b")
		access := NewAccess(held(caller), narrow, Intersect, nil, WithCeilings(held(team)))
		assert.False(t, access.IsModelAllowed("openai", "model-a"), "the project refuses it")
		assert.False(t, access.IsModelAllowed("openai", "model-b"), "the team refuses it")
		assertPermitsAre(t, access.DeniedPermitsForModel("openai", "model-a"), narrow)
		assertPermitsAre(t, access.DeniedPermitsForModel("openai", "model-b"), team)
	})

	t.Run("union lets the scope add to the capped caller", func(t *testing.T) {
		access := NewAccess(held(caller), project, Union, nil, WithCeilings(held(team)))
		assert.True(t, access.IsModelAllowed("openai", "model-a"))
		assert.True(t, access.IsModelAllowed("openai", "model-c"), "the project grants it")

		// model-b is the caller's but capped out, so the scope alone serves it.
		candidates := access.ProvidersForModel("model-b")
		require.Len(t, candidates, 1)
		assertPermitsAre(t, access.PermitsForModel("openai", "model-b"), project)
	})
}

func TestAccessCeilings_AccessorReturnsACopy(t *testing.T) {
	caller := permitWithModels(PermitAccessProfile, "ap1", "Member", "openai", "model-a")
	team := permitWithModels(PermitTeamAccessProfile, "t1", "Team", "openai", "model-a")
	access := NewAccess(held(caller), nil, "", nil, WithCeilings(held(team)))

	layers := access.Ceilings()
	require.Len(t, layers, 1)
	layers[0][0] = nil
	assertPermitsAre(t, access.Ceilings()[0], team)
}
