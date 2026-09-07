package governance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	providerScopeTestVKValue     = "sk-bf-provider-scope"
	providerScopeTestOpenAIModel = "gpt-4o"
	// The pricing fixture attributes this model to bedrock, so a bedrock request costs something.
	// A model nobody prices costs nothing, and nothing is what a budget is never charged.
	providerScopeTestBedrockModel = "openai.gpt-5.5"

	// What the fixture's rates make of the token split below. Budgets are charged in dollars, so
	// this is the amount a single request moves and the amount a request billed twice would exceed.
	providerScopeTestOpenAICost  = 0.0055
	providerScopeTestBedrockCost = 0.0165
)

// providerScopeTestCatalog prices requests from the committed pricing fixture, read through a file URL so
// no test here depends on the network. Cost is what separates the two charging paths: a budget is only
// billed for a non-zero one, so without pricing the budget half of provider scoping would be asserted
// against a path that never runs.
func providerScopeTestCatalog(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()

	abs, err := filepath.Abs("../../framework/modelcatalog/datasheet/testdata/pricing.json")
	require.NoError(t, err)

	ds := datasheet.New(nil, NewMockLogger(), datasheet.Config{URL: "file://" + abs})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))

	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// providerScopeTestProviderConfig gives one of the key's providers a budget and a rate limit of its own,
// which on the holder's side is the only place a limit can be attached to a single provider rather than
// to the holder itself.
func providerScopeTestProviderConfig(provider string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) configstoreTables.TableVirtualKeyProviderConfig {
	pc := buildProviderConfigWithRateLimit(provider, []string{"*"}, rateLimit)
	pc.Budgets = []configstoreTables.TableBudget{*budget}
	return pc
}

// providerScopeTestStore builds a key that funds itself across every provider it may use, belongs to a
// team that does the same, and additionally carries a budget and a rate limit on each of two providers.
// Both providers are configured identically so neither direction of the comparison is privileged: any
// asymmetry an assertion finds is the code's, not the fixture's.
//
// The deployment itself contributes nothing here (no provider row and no model config), so every limit
// on the resolved list is one a holder funds, and the exact-set assertions read as a statement about
// provider scoping alone.
func providerScopeTestStore(t *testing.T) *LocalGovernanceStore {
	t.Helper()

	keyBudget := buildBudget("b-scope-key", 1000, "1d")
	keyRL := buildRateLimitWithUsage("rl-scope-key", 1_000_000, 0, 1_000_000, 0)
	teamBudget := buildBudget("b-scope-team", 1000, "1d")
	teamRL := buildRateLimitWithUsage("rl-scope-team", 1_000_000, 0, 1_000_000, 0)
	openaiBudget := buildBudget("b-scope-openai", 1000, "1d")
	openaiRL := buildRateLimitWithUsage("rl-scope-openai", 1_000_000, 0, 1_000_000, 0)
	bedrockBudget := buildBudget("b-scope-bedrock", 1000, "1d")
	bedrockRL := buildRateLimitWithUsage("rl-scope-bedrock", 1_000_000, 0, 1_000_000, 0)

	team := buildTeam("team-scope", "Scoped Team", teamBudget)
	team.RateLimitID = &teamRL.ID

	vk := buildVirtualKeyWithBudget("vk-scope", providerScopeTestVKValue, "Provider Scoped VK", keyBudget)
	vk.RateLimit = keyRL
	vk.RateLimitID = &keyRL.ID
	vk.TeamID = &team.ID
	vk.Team = team
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		providerScopeTestProviderConfig("openai", openaiBudget, openaiRL),
		providerScopeTestProviderConfig("bedrock", bedrockBudget, bedrockRL),
	}

	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Teams:       []configstoreTables.TableTeam{*team},
		Budgets: []configstoreTables.TableBudget{
			*keyBudget, *teamBudget, *openaiBudget, *bedrockBudget,
		},
		RateLimits: []configstoreTables.TableRateLimit{
			*keyRL, *teamRL, *openaiRL, *bedrockRL,
		},
	}, nil, nil)
	require.NoError(t, err)

	return store
}

// providerScopeTestChatRequest is a chat request whose provider is already settled, so the pair the
// limits are narrowed against is exactly the one the case names.
func providerScopeTestChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: provider, Model: model},
	}
}

// providerScopeTestResponse is a completed chat response the accounting path counts. The model it names
// as originally requested has to be non-empty, and priced, or nothing is billed at all, which would make
// a nothing-was-charged assertion pass for the wrong reason.
func providerScopeTestResponse(provider schemas.ModelProvider, model string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: model,
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 600, CompletionTokens: 400, TotalTokens: 1000},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               provider,
				OriginalModelRequested: model,
			},
		},
	}
}

// A limit a holder carries answers the same for every provider the holder may reach; a limit hanging off
// one of its provider configs answers only for that provider. Both halves are asserted from the same
// fixture and in both directions, because a filter that compared each config against some fixed provider
// instead of the request's would satisfy one direction while draining a provider nobody used on the other.
//
// The sets are asserted exactly rather than by membership. A superset is the whole failure mode here, so
// an assertion that only checks what is present cannot see it.
func TestProviderScopedLimitsFundOnlyTheirOwnProvider(t *testing.T) {
	store := providerScopeTestStore(t)

	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		budgets    []string
		rateLimits []string
	}{
		{
			name:       "an openai request is funded by the key, its team and the key's openai config",
			provider:   schemas.OpenAI,
			model:      providerScopeTestOpenAIModel,
			budgets:    []string{"b-scope-key", "b-scope-team", "b-scope-openai"},
			rateLimits: []string{"rl-scope-key", "rl-scope-team", "rl-scope-openai"},
		},
		{
			name:       "a bedrock request is funded by the same key and team, and by bedrock's config alone",
			provider:   schemas.Bedrock,
			model:      providerScopeTestBedrockModel,
			budgets:    []string{"b-scope-key", "b-scope-team", "b-scope-bedrock"},
			rateLimits: []string{"rl-scope-key", "rl-scope-team", "rl-scope-bedrock"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := resolverCtx(store, providerScopeTestVKValue)

			settled := settleAttemptLimits(ctx, store, tc.provider, tc.model)
			require.NotNil(t, settled, "the key resolves, so the request has a grant to settle limits on")

			assert.ElementsMatch(t, tc.budgets, limitIDsOf(settled.Budgets()),
				"the holder's own budgets plus this provider's, and no other provider's")
			assert.ElementsMatch(t, tc.rateLimits, limitIDsOf(settled.RateLimits()),
				"the holder's own rate limits plus this provider's, and no other provider's")
		})
	}
}

// Assembling the list and charging it are separate steps, so a provider's limits could be correctly left
// off a request's list and still be billed for it. This runs the request through both hooks and reads the
// counters afterwards, which is the only way to see that second step. Budgets and rate limits are charged
// through separate store calls, so both are read: a provider-scoping regression in one of them says
// nothing about the other.
//
// The charged sets are derived from what actually moved and compared exactly, so they fail both for a
// provider that was billed without being on the list and for one on the list that was never billed. The
// counters are then pinned to what one request costs rather than to merely non-zero, because a limit
// reached twice through two walks over the holder's shape moves every counter it owns and so stays inside
// that derived set.
func TestARequestIsChargedOnlyToTheProviderConfigItUsed(t *testing.T) {
	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		budgets    []string
		rateLimits []string
		cost       float64
	}{
		{
			name:       "an openai request leaves the key's bedrock limits untouched",
			provider:   schemas.OpenAI,
			model:      providerScopeTestOpenAIModel,
			budgets:    []string{"b-scope-key", "b-scope-team", "b-scope-openai"},
			rateLimits: []string{"rl-scope-key", "rl-scope-team", "rl-scope-openai"},
			cost:       providerScopeTestOpenAICost,
		},
		{
			name:       "a bedrock request leaves the key's openai limits untouched",
			provider:   schemas.Bedrock,
			model:      providerScopeTestBedrockModel,
			budgets:    []string{"b-scope-key", "b-scope-team", "b-scope-bedrock"},
			rateLimits: []string{"rl-scope-key", "rl-scope-team", "rl-scope-bedrock"},
			cost:       providerScopeTestBedrockCost,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := providerScopeTestStore(t)
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, NewMockLogger(), store, nil, providerScopeTestCatalog(t), nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

			ctx := resolverCtx(store, providerScopeTestVKValue)

			_, shortCircuit, err := plugin.PreLLMHook(ctx, providerScopeTestChatRequest(tc.provider, tc.model))
			require.NoError(t, err)
			require.Nil(t, shortCircuit, "every limit has room, so the request is admitted")

			_, _, err = plugin.PostLLMHook(ctx, providerScopeTestResponse(tc.provider, tc.model), nil)
			require.NoError(t, err)
			plugin.wg.Wait()

			// What the log row names as co-payers, which a node replaying usage it never saw acts on.
			recordedRateLimits, _ := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
			assert.ElementsMatch(t, tc.rateLimits, recordedRateLimits,
				"the request is recorded as answering to this provider's rate limits and no other provider's")
			recordedBudgets, _ := ctx.Value(schemas.BifrostContextKeyGovernanceBudgetIDs).([]string)
			assert.ElementsMatch(t, tc.budgets, recordedBudgets,
				"and to this provider's budgets and no other provider's")

			data := store.GetGovernanceData(context.Background())

			chargedRateLimits := []string{}
			for id, rateLimit := range data.RateLimits {
				if rateLimit.RequestCurrentUsage > 0 || rateLimit.TokenCurrentUsage > 0 {
					chargedRateLimits = append(chargedRateLimits, id)
				}
			}
			// Required rather than merely asserted: the per-limit reads below are meaningless, and index
			// into nothing, once the set they walk is not the set the store holds.
			require.ElementsMatch(t, tc.rateLimits, chargedRateLimits,
				"no rate limit was counted that the request did not answer to, and none it answered to was skipped")

			chargedBudgets := []string{}
			for id, budget := range data.Budgets {
				if budget.CurrentUsage > 0 {
					chargedBudgets = append(chargedBudgets, id)
				}
			}
			require.ElementsMatch(t, tc.budgets, chargedBudgets,
				"no budget was billed that the request did not answer to, and none it answered to was skipped")

			for _, id := range tc.rateLimits {
				assert.Equal(t, int64(1), data.RateLimits[id].RequestCurrentUsage, id)
				assert.Equal(t, int64(1000), data.RateLimits[id].TokenCurrentUsage, id)
			}
			for _, id := range tc.budgets {
				assert.InDelta(t, tc.cost, data.Budgets[id].CurrentUsage, 1e-9, id)
			}
		})
	}
}
