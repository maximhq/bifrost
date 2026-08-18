package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The grant model and the fold are the framework's, and tested there. What this file covers is
// this package's side: turning a virtual key into the grant it carries, and resolving what
// grants a request carries at all.

// grantWithProviders builds a grant permitting each provider for all models.
func grantWithProviders(grantType grants.GrantType, id, name string, providers ...string) *grants.Grant {
	grant := &grants.Grant{Type: grantType, ID: id, Name: name, IsActive: true}
	for _, provider := range providers {
		grant.ProviderConfigGrants = append(grant.ProviderConfigGrants, grants.ProviderConfigGrant{
			Provider:      provider,
			AllowedModels: schemas.WhiteList{"*"},
			KeyIDs:        schemas.WhiteList{"*"},
		})
	}
	return grant
}

// ---------------------------------------------------------------------------
// the built-in virtual key source
// ---------------------------------------------------------------------------

func vkMCPConfig(clientID, clientName string, tools ...string) configstoreTables.TableVirtualKeyMCPConfig {
	return configstoreTables.TableVirtualKeyMCPConfig{
		MCPClient: configstoreTables.TableMCPClient{
			ClientID: clientID,
			Name:     clientName,
		},
		ToolsToExecute: schemas.WhiteList(tools),
	}
}

// vkGrant builds the grant a key carries, with the given clients open to every key. The
// builder belongs to the store that owns the key data, so tests reach it through one.
func vkGrant(vk *configstoreTables.TableVirtualKey, openClients map[string]string) *grants.Grant {
	store := &LocalGovernanceStore{inMemoryStore: &mockInMemoryStore{allowAllClients: openClients}}
	return store.grantForVirtualKey(emptyCtx(), vk)
}

// A key also answers to where it sits in the organization, so the grant carries its team's and
// customer's limits alongside its own. The limits come from the store's team and customer records
// rather than the key's preloaded relations, which carry only enough to say which team it is.
func TestGrantForVirtualKey_OrganizationLimits(t *testing.T) {
	teamRateLimit, customerRateLimit := "rl-team", "rl-customer"
	team := &configstoreTables.TableTeam{
		ID: "team-1", Name: "Platform",
		RateLimitID: &teamRateLimit,
		Budgets:     []configstoreTables.TableBudget{{ID: "budget-team"}},
	}
	customer := &configstoreTables.TableCustomer{
		ID: "cust-1", Name: "Acme",
		RateLimitID: &customerRateLimit,
		Budgets:     []configstoreTables.TableBudget{{ID: "budget-customer"}},
	}

	// What funds a key across every provider is answered by HolderLimits, not carried on the grant:
	// none of it can tell one provider from another, so load balancing has no use for it.
	storeWith := func(vk *configstoreTables.TableVirtualKey) ([]grants.Limit, []grants.Limit) {
		gs := &LocalGovernanceStore{inMemoryStore: &mockInMemoryStore{}}
		gs.teams.Store(team.ID, team)
		gs.customers.Store(customer.ID, customer)
		gs.virtualKeysByID.Store(vk.ID, vk)
		ctx := emptyCtx()
		return gs.HolderLimits(ctx, gs.grantForVirtualKey(ctx, vk))
	}

	t.Run("a customer reached through the key's team", func(t *testing.T) {
		vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
		vk.Budgets = []configstoreTables.TableBudget{{ID: "budget-key"}}
		vk.TeamID = &team.ID
		vk.Team = &configstoreTables.TableTeam{ID: team.ID, Name: team.Name, CustomerID: &customer.ID}

		budgets, rateLimits := storeWith(vk)

		require.Len(t, budgets, 3, "the key's own, its team's, and the customer containing that team")
		assert.Equal(t, []string{"budget-key", "budget-team", "budget-customer"}, limitIDsOf(budgets))
		assert.Equal(t, []grants.LimitHolderKind{
			grants.LimitHolderVirtualKey, grants.LimitHolderTeam, grants.LimitHolderCustomer,
		}, holderKindsOf(budgets))
		assert.Equal(t, "Platform", budgets[1].HolderName, "a refusal has to be able to name the team")
		assert.Equal(t, []string{"rl-team", "rl-customer"}, limitIDsOf(rateLimits))

		// None of them name a provider: they govern every one, and answer for whichever serves.
		for _, budget := range budgets {
			assert.Empty(t, budget.Provider, budget.ID)
		}
	})

	t.Run("a key attached straight to a customer", func(t *testing.T) {
		vk := buildVirtualKey("vk-2", "sk-bf-direct", "Direct Key", true)
		vk.CustomerID = &customer.ID

		budgets, rateLimits := storeWith(vk)

		assert.Equal(t, []string{"budget-customer"}, limitIDsOf(budgets), "no team in the chain")
		assert.Equal(t, []string{"rl-customer"}, limitIDsOf(rateLimits))
	})

	t.Run("a key's own customer wins over its team's", func(t *testing.T) {
		other := &configstoreTables.TableCustomer{ID: "cust-2", Name: "Other", Budgets: []configstoreTables.TableBudget{{ID: "budget-other"}}}
		vk := buildVirtualKey("vk-3", "sk-bf-both", "Both Key", true)
		vk.CustomerID = &other.ID
		vk.TeamID = &team.ID
		vk.Team = &configstoreTables.TableTeam{ID: team.ID, Name: team.Name, CustomerID: &customer.ID}

		gs := &LocalGovernanceStore{inMemoryStore: &mockInMemoryStore{}}
		gs.teams.Store(team.ID, team)
		gs.customers.Store(customer.ID, customer)
		gs.customers.Store(other.ID, other)

		gs.virtualKeysByID.Store(vk.ID, vk)
		ctx := emptyCtx()
		budgets, _ := gs.HolderLimits(ctx, gs.grantForVirtualKey(ctx, vk))

		assert.Equal(t, []string{"budget-team", "budget-other"}, limitIDsOf(budgets),
			"the customer the key names, not the one its team belongs to")
	})

	t.Run("no organization above the key", func(t *testing.T) {
		vk := buildVirtualKey("vk-4", "sk-bf-lonely", "Lonely Key", true)
		vk.Budgets = []configstoreTables.TableBudget{{ID: "budget-key"}}

		budgets, _ := storeWith(vk)

		assert.Equal(t, []string{"budget-key"}, limitIDsOf(budgets))
	})

	t.Run("a team the store has never heard of contributes nothing", func(t *testing.T) {
		missing := "team-gone"
		vk := buildVirtualKey("vk-5", "sk-bf-stale", "Stale Key", true)
		vk.TeamID = &missing

		budgets, rateLimits := storeWith(vk)

		assert.Empty(t, budgets)
		assert.Empty(t, rateLimits)
	})
}

func holderKindsOf(limits []grants.Limit) []grants.LimitHolderKind {
	kinds := make([]grants.LimitHolderKind, 0, len(limits))
	for _, limit := range limits {
		kinds = append(kinds, limit.HolderKind)
	}
	return kinds
}

func TestGrantForVirtualKey_Identity(t *testing.T) {
	vk := buildVirtualKey("vk-1", "sk-bf-test", "Data Team Key", true)

	grant := vkGrant(vk, nil)

	require.NotNil(t, grant)
	assert.Equal(t, grants.GrantTypeVirtualKey, grant.Type)
	assert.Equal(t, "vk-1", grant.ID)
	assert.Equal(t, "Data Team Key", grant.Name, "the display name a denial quotes")

	assert.Nil(t, vkGrant(nil, nil), "no key, no grant")
}

func TestGrantForVirtualKey_ProviderConfigs(t *testing.T) {
	vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		{
			Provider:          "openai",
			AllowedModels:     schemas.WhiteList{"gpt-4o"},
			BlacklistedModels: schemas.BlackList{"gpt-4o-mini"},
			AllowAllKeys:      true,
			Weight:            schemas.Ptr(0.7),
		},
		{
			Provider:      "anthropic",
			AllowedModels: schemas.WhiteList{"*"},
			Keys: []configstoreTables.TableKey{
				{KeyID: "key-a"},
				{KeyID: "key-b"},
			},
		},
		{
			// No allow-all flag and no keys: the key may use none of the provider's keys.
			Provider:      "bedrock",
			AllowedModels: schemas.WhiteList{"*"},
		},
	}

	grant := vkGrant(vk, nil)
	require.Len(t, grant.ProviderConfigGrants, 3)

	openai := grant.ProviderConfigGrants[0]
	assert.Equal(t, "openai", openai.Provider)
	assert.Equal(t, schemas.WhiteList{"gpt-4o"}, openai.AllowedModels)
	assert.Equal(t, schemas.BlackList{"gpt-4o-mini"}, openai.BlacklistedModels)
	assert.Equal(t, schemas.WhiteList{"*"}, openai.KeyIDs, "allow-all becomes the wildcard")
	assert.Equal(t, schemas.Ptr(0.7), openai.Weight)

	anthropic := grant.ProviderConfigGrants[1]
	assert.Equal(t, schemas.WhiteList{"key-a", "key-b"}, anthropic.KeyIDs)
	assert.Nil(t, anthropic.Weight, "no weight configured stays no weight")

	bedrock := grant.ProviderConfigGrants[2]
	assert.NotNil(t, bedrock.KeyIDs, "an empty restriction is not the absence of one")
	assert.Empty(t, bedrock.KeyIDs)
}

// A key is governed at two levels, and the grant carries only one of them: each provider config's
// limits, spent by what that config serves. The key's own — spent whichever provider serves — are not
// here, because they answer the same for every provider and so cannot help choose between them.
func TestGrantForVirtualKey_Limits(t *testing.T) {
	keyRateLimitID := "rl-key"
	openaiRateLimitID := "rl-openai"
	vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
	vk.RateLimitID = &keyRateLimitID
	vk.Budgets = []configstoreTables.TableBudget{{ID: "budget-key-daily"}, {ID: "budget-key-monthly"}}
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		{
			ID:            7,
			Provider:      "openai",
			AllowedModels: schemas.WhiteList{"*"},
			RateLimitID:   &openaiRateLimitID,
			Budgets:       []configstoreTables.TableBudget{{ID: "budget-openai"}},
		},
		{
			// Governed by nothing of its own: the key's budget is what pays for it.
			ID:            8,
			Provider:      "anthropic",
			AllowedModels: schemas.WhiteList{"*"},
		},
	}

	grant := vkGrant(vk, nil)

	// Each config's limits sit on that config, so which provider they answer for is where they are
	// rather than something to filter on.
	openaiBudgets := grant.BudgetsFor("openai")
	require.Len(t, openaiBudgets, 1, "the openai config's own")
	assert.Equal(t, grants.Limit{
		ID: "budget-openai", HolderKind: grants.LimitHolderVirtualKeyProviderConfig,
		HolderID: "7", HolderName: "Key", Provider: "openai",
	}, openaiBudgets[0])

	assert.Empty(t, grant.BudgetsFor("anthropic"), "a config governed by nothing of its own")
	assert.Equal(t, []string{"rl-openai"}, limitIDsOf(grant.RateLimitsFor("openai")))
	assert.Empty(t, grant.RateLimitsFor("anthropic"))

	// The key's own limits are not the grant's to answer — HolderLimits is.
	assert.Empty(t, grant.BudgetsFor(""), "and there is no unscoped bucket on the grant to find them in")

	// Identifiers, not records: a budget's usage moves as requests spend, so the grant must not
	// hold a copy of a balance. Editing the rows it was built from cannot reach it.
	vk.ProviderConfigs[0].Budgets[0].ID = "mutated"
	vk.ProviderConfigs[0].RateLimitID = nil
	assert.Equal(t, "budget-openai", grant.BudgetsFor("openai")[0].ID)
	assert.Equal(t, "rl-openai", grant.RateLimitsFor("openai")[0].ID)
}

// A key may hold two configs for one provider, governed separately — nothing makes a provider unique
// within a key. Asking the grant about that provider answers for both, and what keeps them distinct is
// the holder each names.
func TestGrantForVirtualKey_LimitsOfTwoConfigsForOneProvider(t *testing.T) {
	firstRateLimit, secondRateLimit := "rl-first", "rl-second"
	vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		{
			ID: 1, Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"},
			RateLimitID: &firstRateLimit,
			Budgets:     []configstoreTables.TableBudget{{ID: "budget-first"}},
		},
		{
			ID: 2, Provider: "openai", AllowedModels: schemas.WhiteList{"o3"},
			RateLimitID: &secondRateLimit,
			Budgets:     []configstoreTables.TableBudget{{ID: "budget-second"}},
		},
	}

	grant := vkGrant(vk, nil)

	budgets := grant.BudgetsFor("openai")
	require.Len(t, budgets, 2, "both configs govern openai traffic")
	assert.Equal(t, []string{"budget-first", "budget-second"}, []string{budgets[0].ID, budgets[1].ID})
	assert.Equal(t, []string{"1", "2"}, []string{budgets[0].HolderID, budgets[1].HolderID},
		"the config each came from, which is what tells two limits on one provider apart")

	rateLimits := grant.RateLimitsFor("openai")
	require.Len(t, rateLimits, 2)
	assert.Equal(t, []string{"rl-first", "rl-second"}, []string{rateLimits[0].ID, rateLimits[1].ID})
}

func TestGrantForVirtualKey_MCPConfigs(t *testing.T) {
	t.Run("the key's own configs are carried through", func(t *testing.T) {
		vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
		vk.MCPConfigs = []configstoreTables.TableVirtualKeyMCPConfig{
			vkMCPConfig("github-id", "github", "read_file"),
			vkMCPConfig("slack-id", "slack", "*"),
		}

		grant := vkGrant(vk, nil)

		require.Len(t, grant.MCPConfigGrants, 2)
		assert.Equal(t, grants.MCPConfigGrant{Client: "github-id", ClientName: "github", Tools: schemas.WhiteList{"read_file"}}, grant.MCPConfigGrants[0])
		assert.Equal(t, grants.MCPConfigGrant{Client: "slack-id", ClientName: "slack", Tools: schemas.WhiteList{"*"}}, grant.MCPConfigGrants[1])
	})

	t.Run("clients open to every key grant all their tools", func(t *testing.T) {
		vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
		open := map[string]string{"jira-id": "jira"}

		grant := vkGrant(vk, open)

		require.Len(t, grant.MCPConfigGrants, 1)
		assert.Equal(t, grants.MCPConfigGrant{Client: "jira-id", ClientName: "jira", Tools: schemas.WhiteList{"*"}}, grant.MCPConfigGrants[0])
	})

	t.Run("an explicit config owns its client, and is never widened", func(t *testing.T) {
		vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
		vk.MCPConfigs = []configstoreTables.TableVirtualKeyMCPConfig{
			vkMCPConfig("github-id", "github", "read_file"),
			// Configured with no tool at all: the client is still the key's to decide.
			vkMCPConfig("jira-id", "jira"),
		}
		open := map[string]string{"github-id": "github", "jira-id": "jira", "slack-id": "slack"}

		grant := vkGrant(vk, open)

		require.Len(t, grant.MCPConfigGrants, 3)
		assert.Equal(t, schemas.WhiteList{"read_file"}, grant.MCPConfigGrants[0].Tools, "not widened to all tools")
		assert.Empty(t, grant.MCPConfigGrants[1].Tools, "an empty config stays empty")
		assert.Equal(t, "slack-id", grant.MCPConfigGrants[2].Client, "only the unconfigured client is added")
	})

	t.Run("open clients are ordered, so the grant is stable", func(t *testing.T) {
		vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
		open := map[string]string{"c-id": "c", "a-id": "a", "b-id": "b"}

		for range 8 {
			grant := vkGrant(vk, open)
			require.Len(t, grant.MCPConfigGrants, 3)
			assert.Equal(t, []string{"a-id", "b-id", "c-id"}, []string{
				grant.MCPConfigGrants[0].Client,
				grant.MCPConfigGrants[1].Client,
				grant.MCPConfigGrants[2].Client,
			}, "map iteration order must not leak into the grant")
		}
	})
}

// ---------------------------------------------------------------------------
// equivalence with the existing virtual-key walkers
// ---------------------------------------------------------------------------

// mcpScenarios covers the shapes the MCP grant rules distinguish — configured or not,
// unrestricted or specific, empty, and open-to-every-key with and without a config — with the
// include list and the per-tool answers each one must produce.
func mcpScenarios() []struct {
	name            string
	vk              *configstoreTables.TableVirtualKey
	open            map[string]string
	wantIncludeList []string
	wantTool        map[string]bool
} {
	withConfigs := func(configs ...configstoreTables.TableVirtualKeyMCPConfig) *configstoreTables.TableVirtualKey {
		vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
		vk.MCPConfigs = configs
		return vk
	}

	return []struct {
		name            string
		vk              *configstoreTables.TableVirtualKey
		open            map[string]string
		wantIncludeList []string
		wantTool        map[string]bool
	}{
		{
			name:            "nothing configured, nothing open",
			vk:              withConfigs(),
			wantIncludeList: []string{},
			wantTool:        map[string]bool{"github-read_file": false, "github-*": false},
		},
		{
			name:            "specific tools",
			vk:              withConfigs(vkMCPConfig("github-id", "github", "read_file", "list_issues")),
			wantIncludeList: []string{"github-read_file", "github-list_issues"},
			wantTool: map[string]bool{
				"github-read_file": true, "github-list_issues": true,
				"github-delete_repo": false, "github-*": true, "slack-post": false,
			},
		},
		{
			name:            "unrestricted client",
			vk:              withConfigs(vkMCPConfig("github-id", "github", "*")),
			wantIncludeList: []string{"github-*"},
			wantTool:        map[string]bool{"github-anything": true, "github-*": true, "slack-post": false},
		},
		{
			name:            "client configured with no tools",
			vk:              withConfigs(vkMCPConfig("github-id", "github")),
			wantIncludeList: []string{},
			wantTool:        map[string]bool{"github-read_file": false, "github-*": false},
		},
		{
			name:            "only an open client",
			vk:              withConfigs(),
			open:            map[string]string{"jira-id": "jira"},
			wantIncludeList: []string{"jira-*"},
			wantTool:        map[string]bool{"jira-create_issue": true, "jira-*": true, "github-read_file": false},
		},
		{
			name:            "open client also configured specifically",
			vk:              withConfigs(vkMCPConfig("jira-id", "jira", "create_issue")),
			open:            map[string]string{"jira-id": "jira"},
			wantIncludeList: []string{"jira-create_issue"},
			wantTool:        map[string]bool{"jira-create_issue": true, "jira-delete_issue": false, "jira-*": true},
		},
		{
			name:            "open client configured with no tools",
			vk:              withConfigs(vkMCPConfig("jira-id", "jira")),
			open:            map[string]string{"jira-id": "jira"},
			wantIncludeList: []string{},
			wantTool:        map[string]bool{"jira-create_issue": false, "jira-*": false},
		},
		{
			name:            "several clients, mixed",
			vk:              withConfigs(vkMCPConfig("github-id", "github", "read_file"), vkMCPConfig("slack-id", "slack", "*")),
			open:            map[string]string{"jira-id": "jira", "github-id": "github"},
			wantIncludeList: []string{"github-read_file", "slack-*", "jira-*"},
			wantTool: map[string]bool{
				"github-read_file": true, "github-delete_repo": false,
				"slack-post_message": true, "jira-create_issue": true, "unknown-tool": false,
			},
		},
	}
}

func TestGrantForVirtualKey_MCPIncludeList(t *testing.T) {
	// The include-tools list a key produces, across the shapes the MCP grant rules
	// distinguish. Now that the fold is the only implementation of those rules, these are the
	// expectations themselves rather than a comparison against a second one.
	for _, scenario := range mcpScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			grant := vkGrant(scenario.vk, scenario.open)
			got := grants.NewEffectiveAccess(grant, nil, "", nil, nil).MCPIncludeList()

			assert.ElementsMatch(t, scenario.wantIncludeList, got)
		})
	}
}

func TestGrantForVirtualKey_ToolChecks(t *testing.T) {
	// Per-tool decisions over the same shapes: an explicit config owns its client and is never
	// widened by an open client, an unrestricted config grants every tool, an empty one grants
	// none, and a wildcard pattern asks whether the client is granted anything at all.
	for _, scenario := range mcpScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			grant := vkGrant(scenario.vk, scenario.open)
			access := grants.NewEffectiveAccess(grant, nil, "", nil, nil)

			for pattern, want := range scenario.wantTool {
				assert.Equal(t, want, access.IsMCPToolAllowed(pattern), "pattern %q", pattern)
			}
			assert.False(t, access.IsMCPToolAllowed(""), "an empty pattern names no tool")
		})
	}
}

func TestGrantForVirtualKey_ProviderAndModelRules(t *testing.T) {
	// The rules the key's own grant must keep, now that the fold is the only implementation of
	// them: a provider with no config is not permitted, a configured provider is, a model must
	// be in the allowlist, and a blacklisted model is refused however permissive the allowlist.
	vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o", "o3"}, BlacklistedModels: schemas.BlackList{"o3"}},
		{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}},
		{Provider: "bedrock", AllowedModels: schemas.WhiteList{}},
	}
	access := grants.NewEffectiveAccess(vkGrant(vk, nil), nil, "", nil, nil)

	assert.True(t, access.IsProviderAllowed("openai"))
	assert.True(t, access.IsProviderAllowed("anthropic"))
	assert.True(t, access.IsProviderAllowed("bedrock"), "configured, even with no model allowed")
	assert.False(t, access.IsProviderAllowed("cohere"), "not configured at all")

	assert.True(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.False(t, access.IsModelAllowed("openai", "o3"), "blacklisted wins over the allowlist")
	assert.False(t, access.IsModelAllowed("openai", "gpt-4o-mini"), "not in the allowlist")
	assert.True(t, access.IsModelAllowed("anthropic", "claude-sonnet-4"), "wildcard allows any model")
	assert.False(t, access.IsModelAllowed("bedrock", "anything"), "empty allowlist permits nothing")
	assert.False(t, access.IsModelAllowed("cohere", "command-r"))

	// A key with no provider config at all permits nothing — deny by default.
	bare := buildVirtualKey("vk-2", "sk-bf-bare", "Bare Key", true)
	bareAccess := grants.NewEffectiveAccess(vkGrant(bare, nil), nil, "", nil, nil)
	assert.False(t, bareAccess.IsProviderAllowed("openai"))
	assert.False(t, bareAccess.IsModelAllowed("openai", "gpt-4o"))
}

func TestGrantForVirtualKey_KeysForMatchesTheVKStamping(t *testing.T) {
	// The stamping walks the key's configs and stamps only when the config restricts keys,
	// taking the first config for the provider.
	vk := buildVirtualKey("vk-1", "sk-bf-test", "Key", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, Keys: []configstoreTables.TableKey{{KeyID: "key-us-1"}}},
		{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, Keys: []configstoreTables.TableKey{{KeyID: "key-ignored"}}},
		{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true},
	}
	access := grants.NewEffectiveAccess(vkGrant(vk, nil), nil, "", nil, nil)

	keyIDs, restricted := access.KeysFor("openai")
	assert.True(t, restricted)
	assert.Equal(t, []string{"key-us-1"}, keyIDs, "the first config for the provider decides")

	keyIDs, restricted = access.KeysFor("anthropic")
	assert.False(t, restricted, "allow-all stamps no restriction")
	assert.Nil(t, keyIDs)
}

// ---------------------------------------------------------------------------
// per-request resolution
// ---------------------------------------------------------------------------

// grantSetStore stands in for a store that resolves grant holders beyond virtual keys. It
// records how often it was asked, and can replace the own side to stand for a caller whose
// grants do not come from a key.
type grantSetStore struct {
	GovernanceStore
	baseOverride *grants.Grant
	scoping      *grants.Grant
	mode         grants.GrantCompositionMode
	// resolvesNothing makes the store answer with no grant at all, as a store that resolves callers
	// beyond keys does when the caller it was asked about has no access configured.
	resolvesNothing bool
	calls           int
}

func (s *grantSetStore) GetGrantSet(ctx *schemas.BifrostContext) (*grants.Grant, *grants.Grant, grants.GrantCompositionMode) {
	s.calls++
	if s.resolvesNothing {
		return nil, nil, ""
	}
	base := s.baseOverride
	if base == nil {
		base, _, _ = s.GovernanceStore.GetGrantSet(ctx)
	}
	return base, s.scoping, s.mode
}

// newAccessTestPlugin builds a plugin over a store that serves vk, optionally wrapped so the
// store composes further grants onto every request.
func newAccessTestPlugin(t *testing.T, vk *configstoreTables.TableVirtualKey, wrap *grantSetStore) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	local.inMemoryStore = &mockInMemoryStore{}

	var store GovernanceStore = local
	if wrap != nil {
		wrap.GovernanceStore = local
		store = wrap
	}

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)},
		logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

func TestEnsureEffectiveAccess(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})

	t.Run("a store that knows only keys answers with the key's own grant", func(t *testing.T) {
		plugin := newAccessTestPlugin(t, vk, nil)

		access := plugin.ensureEffectiveAccess(newPreRequestCtx(nil, nil))

		require.NotNil(t, access)
		assert.False(t, access.IsScoped())
		assert.Equal(t, grants.GrantCompositionMode(""), access.Mode())
		require.NotNil(t, access.Base())
		assert.Equal(t, vk.ID, access.Base().ID)
		assert.Equal(t, grants.GrantTypeVirtualKey, access.Base().Type)
		assert.True(t, access.IsModelAllowed("openai", "gpt-4o"))
		assert.True(t, access.IsMCPToolAllowed("sentry-read_file"))
	})

	t.Run("a grant the store scopes the request with is folded in", func(t *testing.T) {
		store := &grantSetStore{
			scoping: grantWithProviders("other", "o1", "Other", "anthropic"),
			mode:    grants.GrantModeUnion,
		}
		plugin := newAccessTestPlugin(t, vk, store)

		access := plugin.ensureEffectiveAccess(newPreRequestCtx(nil, nil))

		require.NotNil(t, access)
		assert.Equal(t, 1, store.calls, "asked once per resolution")
		assert.True(t, access.IsScoped())
		assert.Equal(t, grants.GrantModeUnion, access.Mode())
		assert.True(t, access.IsProviderAllowed("openai"), "the key's own provider")
		assert.True(t, access.IsProviderAllowed("anthropic"), "the grant scoping the request")
	})

	t.Run("the store may answer for a caller whose grants are not a key's", func(t *testing.T) {
		// The base slot is the store's to decide: a caller can hold a grant without holding a
		// key, and such a request must still resolve to real access.
		store := &grantSetStore{baseOverride: grantWithProviders("other", "u1", "Someone", "bedrock")}
		plugin := newAccessTestPlugin(t, vk, store)

		// No key on the request at all — the store still answers for the caller.
		access := plugin.ensureEffectiveAccess(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))

		require.NotNil(t, access)
		require.NotNil(t, access.Base())
		assert.Equal(t, "u1", access.Base().ID)
		assert.True(t, access.IsProviderAllowed("bedrock"))
	})

	t.Run("a request presenting nothing resolves to unrestricted access", func(t *testing.T) {
		plugin := newAccessTestPlugin(t, vk, nil)

		// Nobody granted it anything, and it is restricted by nothing — so it still has an access to be
		// governed through rather than being the one shape every consumer special-cases.
		access := plugin.ensureEffectiveAccess(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
		require.NotNil(t, access)
		assert.True(t, access.IsUnrestricted())
		assert.Equal(t, grants.GrantTypeUnrestricted, access.Base().Type)
		assert.True(t, access.IsProviderAllowed("a-provider-nobody-configured"))
		assert.True(t, access.IsMCPToolAllowed("any-client-any_tool"))

		assert.Nil(t, plugin.ensureEffectiveAccess(nil), "and no context resolves to nothing at all")
	})
}

// Resolving happens once per attempt. A second caller finds the answer already recorded and gets
// the same object back, without the store being asked again — which is what lets every path that
// might be the first to see a request call this unconditionally.
func TestEnsureEffectiveAccessResolvesOnce(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	store := &grantSetStore{}
	plugin := newAccessTestPlugin(t, vk, store)
	ctx := newPreRequestCtx(nil, nil)

	first := plugin.ensureEffectiveAccess(ctx)
	require.NotNil(t, first)
	assert.Equal(t, 1, store.calls)

	second := plugin.ensureEffectiveAccess(ctx)

	assert.Same(t, first, second, "the recorded answer, not a freshly folded one")
	assert.Equal(t, 1, store.calls, "the store is asked once per attempt")
	assert.Same(t, first, grants.EffectiveAccessFromContext(ctx), "and it is what everything downstream reads")
}

// Once per attempt, not once per request. Core clears the recorded answer before it fails over, so
// the attempt that runs next resolves for itself. This is what stops a request from being governed by
// configuration that has since changed: a request failing over across several slow calls is exactly
// where a grant gets revoked or a limit gets attached under it, and the attempt running second has to
// answer for what is in force when it runs, not for what admitted its predecessor.
func TestEnsureEffectiveAccessResolvesAgainForTheNextAttempt(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	store := &grantSetStore{}
	plugin := newAccessTestPlugin(t, vk, store)
	ctx := newPreRequestCtx(nil, nil)

	first := plugin.ensureEffectiveAccess(ctx)
	require.NotNil(t, first)
	require.False(t, first.IsProviderAllowed("anthropic"), "not reachable while the first attempt runs")

	// What core does between attempts, and a grant widened while the first attempt was in flight.
	ctx.ClearValue(schemas.BifrostContextKeyGovernanceEffectiveAccess)
	store.scoping = grantWithProviders("other", "o1", "Other", "anthropic")
	store.mode = grants.GrantModeUnion

	second := plugin.ensureEffectiveAccess(ctx)

	require.NotNil(t, second)
	assert.NotSame(t, first, second, "a cleared answer is resolved again, not recovered")
	assert.Equal(t, 2, store.calls)
	assert.True(t, second.IsProviderAllowed("anthropic"), "the new attempt answers to configuration as it stands now")
	assert.Same(t, second, grants.EffectiveAccessFromContext(ctx), "and that is what the attempt's consumers read")
}

// A request whose presented credential resolves to nothing records nothing, so it stays
// indistinguishable from one nobody has resolved yet. Asking repeatedly re-asks the store, which is the
// price of not caching a negative.
func TestEnsureEffectiveAccessRecordsNothingForAnUnresolvableCredential(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	store := &grantSetStore{}
	plugin := newAccessTestPlugin(t, vk, store)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-unknown")

	assert.Nil(t, plugin.ensureEffectiveAccess(ctx))
	assert.Nil(t, grants.EffectiveAccessFromContext(ctx), "nothing resolved is not access permitting nothing")

	assert.Nil(t, plugin.ensureEffectiveAccess(ctx))
	assert.Equal(t, 2, store.calls)
}

func TestPreRequestHookStashesEffectiveAccess(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	store := &grantSetStore{
		scoping: grantWithProviders("other", "o1", "Other", "anthropic"),
		mode:    grants.GrantModeUnion,
	}
	plugin := newAccessTestPlugin(t, vk, store)
	ctx := newPreRequestCtx(nil, nil)

	require.NoError(t, plugin.PreRequestHook(ctx, newChatRequest()))

	access := grants.EffectiveAccessFromContext(ctx)
	require.NotNil(t, access, "the hook resolves access for every request carrying a key")
	require.NotNil(t, access.Base())
	assert.Equal(t, vk.ID, access.Base().ID)
	assert.True(t, access.IsModelAllowed("openai", "gpt-4o"))
	assert.True(t, access.IsProviderAllowed("anthropic"), "the store's contribution reached the hook")
}

func TestPreRequestHookWithoutACredentialResolvesUnrestrictedAccess(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	plugin := newAccessTestPlugin(t, vk, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	require.NoError(t, plugin.PreRequestHook(ctx, newChatRequest()))

	// Nothing granted it anything, so nothing restricts it — and it still carries an access, which is what
	// lets every consumer read one answer instead of special-casing its absence.
	access := grants.EffectiveAccessFromContext(ctx)
	require.NotNil(t, access)
	assert.True(t, access.IsUnrestricted())

	// And nothing narrowed its tools to the empty set on the way through.
	assert.Nil(t, ctx.Value(schemas.MCPContextKeyIncludeTools))
}

// ---------------------------------------------------------------------------
// hot-path cost
// ---------------------------------------------------------------------------

// benchVK is a key of a realistic size: several providers, several MCP clients.
func benchVK() *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey("vk-bench", "sk-bf-bench", "bench-key", true)
	for _, provider := range []string{"openai", "anthropic", "bedrock", "vertex", "groq"} {
		vk.ProviderConfigs = append(vk.ProviderConfigs, configstoreTables.TableVirtualKeyProviderConfig{
			Provider:      provider,
			AllowedModels: schemas.WhiteList{"gpt-4o", "claude-sonnet-4", "o3"},
			AllowAllKeys:  true,
			Weight:        schemas.Ptr(1.0),
		})
	}
	for _, client := range []string{"github", "slack", "jira"} {
		vk.MCPConfigs = append(vk.MCPConfigs, vkMCPConfig(client+"-id", client, "read", "write"))
	}
	return vk
}

func benchStore(b *testing.B, vk *configstoreTables.TableVirtualKey) *LocalGovernanceStore {
	b.Helper()
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	if err != nil {
		b.Fatal(err)
	}
	store.inMemoryStore = &mockInMemoryStore{allowAllClients: map[string]string{"sentry-id": "sentry", "pager-id": "pager"}}
	return store
}

func benchPlugin(b *testing.B, store *LocalGovernanceStore) *GovernancePlugin {
	b.Helper()
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)},
		NewMockLogger(), store, nil, nil, nil, store.inMemoryStore)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := plugin.Cleanup(); err != nil {
			b.Fatal(err)
		}
	})
	return plugin
}

// benchCtx carries only the key: what resolution costs is exactly what these benchmarks
// measure, so the access must not already be on the context.
func benchCtx(_ *configstoreTables.TableVirtualKey) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-bench")
	return ctx
}

// BenchmarkEnsureEffectiveAccess is what a request pays once, up front, to resolve access.
func BenchmarkEnsureEffectiveAccess(b *testing.B) {
	vk := benchVK()
	ctx := benchCtx(vk)

	b.ReportAllocs()
	for b.Loop() {
		if grants.EffectiveAccessFromContext(ctx) == nil {
			b.Fatal("no access resolved")
		}
	}
}

// BenchmarkEffectiveAccessChecks is the same questions answered off already-resolved access,
// which is what every call site pays per attempt once they read it instead of the key.
func BenchmarkEffectiveAccessChecks(b *testing.B) {
	vk := benchVK()
	access := grants.EffectiveAccessFromContext(benchCtx(vk))
	if access == nil {
		b.Fatal("no access resolved")
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = access.IsProviderAllowed("groq")
		_ = access.IsModelAllowed("groq", "o3")
		_ = access.MCPIncludeList()
		_ = access.IsMCPToolAllowed("github-read")
	}
}

// filterModelsForAccess is what a virtual-key caller sees in a models listing. It answers from
// the same access the request path enforces, so the listing cannot advertise a model the very
// next request would refuse.
func TestFilterModelsForAccess(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}},
		{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"claude-3-haiku"}},
	})
	plugin := newAccessTestPlugin(t, vk, nil)

	models := []schemas.Model{
		{ID: "openai/gpt-4o"},
		{ID: "openai/gpt-4o-mini"},
		{ID: "anthropic/claude-3-5-sonnet"},
		{ID: "anthropic/claude-3-haiku"},
		{ID: "bedrock/nova-pro"},
	}

	t.Run("keeps only what the request may use", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")

		filtered := plugin.filterModelsForAccess(plugin.ensureEffectiveAccess(ctx), models)

		ids := make([]string, 0, len(filtered))
		for _, model := range filtered {
			ids = append(ids, model.ID)
		}
		// gpt-4o-mini is outside the allowlist, claude-3-haiku is blacklisted, and bedrock is
		// not granted at all.
		assert.Equal(t, []string{"openai/gpt-4o", "anthropic/claude-3-5-sonnet"}, ids)
	})

	t.Run("a credential that resolves to nothing lists nothing", func(t *testing.T) {
		// Presenting a key that grants nothing is not the same as presenting none: the key was the
		// authority for the listing and it turned out to grant nothing, so nothing is listed.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-unknown")

		filtered := plugin.filterModelsForAccess(plugin.ensureEffectiveAccess(ctx), models)

		assert.NotNil(t, filtered)
		assert.Empty(t, filtered)
	})

	t.Run("presenting nothing lists everything", func(t *testing.T) {
		// A request nobody granted anything is unrestricted, so a listing has nothing to narrow —
		// the opposite of the case above, and the reason the two are told apart by what was presented.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		filtered := plugin.filterModelsForAccess(plugin.ensureEffectiveAccess(ctx), models)

		assert.Len(t, filtered, len(models))
	})
}

// Evaluation is the funnel every caller passes through, so the grants a request holds without a
// key are enforced there too — not only where a key is presented.
func TestEvaluateEnforcesGrantsHeldWithoutAKey(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	held := grantWithProviders("other", "h1", "Holder", "openai")

	t.Run("a provider the request does not hold is refused", func(t *testing.T) {
		plugin := newAccessTestPlugin(t, vk, &grantSetStore{baseOverride: held})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		result, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-3-5-sonnet",
		})

		require.NotNil(t, bifrostErr)
		assert.Equal(t, DecisionProviderBlocked, result.Decision)
		assert.Contains(t, result.Reason, "Holder", "the denial names the grant in the way")
	})

	t.Run("a provider it does hold is allowed", func(t *testing.T) {
		plugin := newAccessTestPlugin(t, vk, &grantSetStore{baseOverride: held})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		result, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
		})

		assert.Nil(t, bifrostErr)
		assert.Equal(t, DecisionAllow, result.Decision)
	})

	// Unchanged for a deployment whose store answers only for keys: a request with no key resolves
	// no grants, so nothing gates it.
	t.Run("a request holding nothing is unaffected", func(t *testing.T) {
		plugin := newAccessTestPlugin(t, vk, nil)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		result, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-3-5-sonnet",
		})

		assert.Nil(t, bifrostErr)
		assert.Equal(t, DecisionAllow, result.Decision)
	})
}

// A credential the request presented that resolves to no grant is a failed authentication, not an
// anonymous request. The two are indistinguishable from the access alone — there is no grant either
// way — so the funnel separates them by asking whether anything was presented at all.
func TestEvaluateRefusesAPresentedCredentialThatGrantsNothing(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	plugin := newAccessTestPlugin(t, vk, nil)

	t.Run("a credential that resolves to nothing is refused", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-revoked")

		result, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.OpenAI,
			Model:       "gpt-4o",
		})

		require.NotNil(t, bifrostErr, "a revoked credential must not read as an anonymous request")
		assert.Equal(t, DecisionAccessNotFound, result.Decision)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 401, *bifrostErr.StatusCode, "authentication failed rather than permission denied")
	})

	t.Run("an authenticated identity whose access is missing is refused too", func(t *testing.T) {
		// A caller can be granted access by something other than a key, and a store that resolves such
		// callers answers with nothing when the one it was asked about has none. That must refuse: reading
		// only the key would let the caller through as though they had presented nothing — which is
		// unrestricted — the moment whatever configures their access stopped answering for them.
		identityPlugin := newAccessTestPlugin(t, vk, &grantSetStore{resolvesNothing: true})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUserID, "user-with-no-access")

		result, bifrostErr := identityPlugin.Evaluate(ctx, &EvaluationRequest{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.OpenAI,
			Model:       "gpt-4o",
		})

		require.NotNil(t, bifrostErr, "an identity that resolves to nothing must not read as an anonymous request")
		assert.Equal(t, DecisionAccessNotFound, result.Decision)
	})

	t.Run("presenting nothing is not a failed authentication", func(t *testing.T) {
		// Whether an anonymous request is allowed at all is the deployment's mandatory-auth
		// decision, made before this; it is not an access refusal.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		result, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.OpenAI,
			Model:       "gpt-4o",
		})

		assert.Nil(t, bifrostErr)
		assert.Equal(t, DecisionAllow, result.Decision)
	})
}

// Mandatory authentication asks what the request carried, and a deployment may authenticate a caller
// without issuing them a key. Refusing such a caller for holding no key would reject exactly the
// requests the setting exists to admit — its own message offers a user token as an alternative.
func TestMandatoryAuthAcceptsAnAuthenticatedIdentity(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)
	local.inMemoryStore = &mockInMemoryStore{}

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(true)},
		logger, local, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

	evaluate := func(t *testing.T, stamp func(ctx *schemas.BifrostContext)) *schemas.BifrostError {
		t.Helper()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		stamp(ctx)
		_, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.OpenAI,
			Model:       "gpt-4o",
		})
		return bifrostErr
	}

	t.Run("presenting nothing is refused", func(t *testing.T) {
		bifrostErr := evaluate(t, func(*schemas.BifrostContext) {})

		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 401, *bifrostErr.StatusCode)
	})

	t.Run("an authenticated identity satisfies it", func(t *testing.T) {
		// The contrast with the case above is the whole assertion: the same request, refused when it
		// presents nothing and admitted when it presents an identity, is Step 1 reading what was presented
		// rather than what it resolved to. What such a caller may then do is the store's answer, not this
		// step's — a store that resolves identities refuses one it has no access for.
		bifrostErr := evaluate(t, func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyUserID, "user-1")
		})

		assert.Nil(t, bifrostErr, "not refused for presenting nothing, because it presented an identity")
	})
}

// Whether a credential may be used at all is settled when its grant is created, so the funnel reads
// it off the grant rather than resolving the credential again. Inactive and expired are reported
// distinctly, and inactive wins when a key is both — a key switched off is not a key that ran out.
func TestEvaluateRefusesAGrantThatMayNotBeUsed(t *testing.T) {
	newPlugin := func(t *testing.T, mutate func(vk *configstoreTables.TableVirtualKey)) (*GovernancePlugin, *schemas.BifrostContext) {
		vk := buildVKForMCPStamping([]string{"read_file"})
		mutate(vk)
		return newAccessTestPlugin(t, vk, nil), newPreRequestCtx(nil, nil)
	}
	request := &EvaluationRequest{
		RequestType: schemas.ChatCompletionRequest,
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
	}

	t.Run("an expired grant", func(t *testing.T) {
		past := time.Now().UTC().Add(-time.Second)
		plugin, ctx := newPlugin(t, func(vk *configstoreTables.TableVirtualKey) { vk.ExpiresAt = &past })

		result, bifrostErr := plugin.Evaluate(ctx, request)

		require.NotNil(t, bifrostErr)
		assert.Equal(t, DecisionAccessBlocked, result.Decision)
		assert.Contains(t, result.Reason, "expired")
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 403, *bifrostErr.StatusCode)
	})

	t.Run("an inactive grant, even with a future expiry", func(t *testing.T) {
		future := time.Now().UTC().Add(time.Hour)
		plugin, ctx := newPlugin(t, func(vk *configstoreTables.TableVirtualKey) {
			inactive := false
			vk.IsActive = &inactive
			vk.ExpiresAt = &future
		})

		result, bifrostErr := plugin.Evaluate(ctx, request)

		require.NotNil(t, bifrostErr)
		assert.Equal(t, DecisionAccessBlocked, result.Decision)
		assert.Contains(t, result.Reason, "inactive", "switched off is not the same as run out")
	})

	// The refusal names what kind of thing was refused, so a deployment granting access through
	// something other than a key does not report it as one.
	t.Run("the refusal names the grant's kind", func(t *testing.T) {
		past := time.Now().UTC().Add(-time.Second)
		plugin, ctx := newPlugin(t, func(vk *configstoreTables.TableVirtualKey) { vk.ExpiresAt = &past })

		result, _ := plugin.Evaluate(ctx, request)

		assert.Contains(t, result.Reason, "virtual key")
	})
}

// A holder's own model configs are stored under a scope name, and a grant names its holder by type. For
// virtual keys the two spell it differently, so the lookup has to translate rather than cast — a
// near-miss finds nothing and reads as "this key configured no model limits", which is indistinguishable
// from the truth until a budget that should have refused a request quietly never does.
//
// The test builds the grant the way production does, because a test that constructs a scope-shaped type
// of its own would agree with a broken lookup.
func TestGrantFindsItsOwnScopedModelConfigs(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-scoped", "Scoped VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	budget := buildBudget("b-vk-model", 100.0, "1h")
	modelConfig := buildVKScopedModelConfig("mc-vk", "gpt-4o", nil, vk.ID, budget, nil)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	ctx := resolverCtx(store, "sk-bf-scoped")
	grant, _, _ := store.GetGrantSet(ctx)
	require.NotNil(t, grant)
	require.Equal(t, grants.GrantTypeVirtualKey, grant.Type, "the type production stamps, not a scope name")

	budgets, _ := store.ProviderAndModelLimits(ctx, grant, schemas.OpenAI, "gpt-4o")

	assert.Contains(t, limitIDsOf(budgets), "b-vk-model",
		"a key's own model budget must be found through the grant production builds for it")
}
