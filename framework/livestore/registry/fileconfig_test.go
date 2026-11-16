package registry

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// budgetsByID keys budget records by ID.
func budgetsByID(rows []*Budget) map[string]*Budget {
	out := make(map[string]*Budget, len(rows))
	for _, b := range rows {
		out[b.ID] = b
	}
	return out
}

// rateLimitsByID keys rate limit records by ID.
func rateLimitsByID(rows []*RateLimit) map[string]*RateLimit {
	out := make(map[string]*RateLimit, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// TestFromConfigJoins checks how config.json is turned into records for a gateway with no config
// store: each holder's nested budgets and rate limit, a listed limit replacing a nested one with its
// ID, a team picking up listed budgets naming it, a model config falling back to listed budgets
// naming it only when it nests none, configured providers carrying their limits' IDs in name
// order, calendar flags from the holder, and listed limits nothing holds still loaded, with no
// holder and the flag off.
func TestFromConfigJoins(t *testing.T) {
	listedCustomerBudget := tables.TableBudget{ID: "b-cust", MaxLimit: 99, ResetDuration: "1d"}
	cfg := &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			listedCustomerBudget,
			{ID: "b-team-listed", MaxLimit: 7, ResetDuration: "1d", TeamID: ptr("team-1")},
			{ID: "b-mc-listed", MaxLimit: 8, ResetDuration: "1d", ModelConfigID: ptr("mc-2")},
			{ID: "b-mc-ignored", MaxLimit: 9, ResetDuration: "1d", ModelConfigID: ptr("mc-1")},
			{ID: "b-prov", MaxLimit: 10, ResetDuration: "1M"},
			{ID: "b-unheld", MaxLimit: 11, ResetDuration: "1d"},
		},
		RateLimits: []tables.TableRateLimit{
			{ID: "rl-cust", RequestMaxLimit: ptr(int64(99))},
			{ID: "rl-prov", RequestMaxLimit: ptr(int64(10))},
			{ID: "rl-unheld", RequestMaxLimit: ptr(int64(11))},
		},
		Customers: []tables.TableCustomer{{
			ID: "cust-1", Name: "Acme", CalendarAligned: true,
			Budgets:     []tables.TableBudget{{ID: "b-cust", MaxLimit: 1, ResetDuration: "1d"}},
			RateLimitID: ptr("rl-cust"), RateLimit: &tables.TableRateLimit{ID: "rl-cust", RequestMaxLimit: ptr(int64(1))},
		}},
		Teams: []tables.TableTeam{{
			ID: "team-1", Name: "Platform", CustomerID: ptr("cust-1"),
			Budgets:     []tables.TableBudget{{ID: "b-team", MaxLimit: 2, ResetDuration: "1d"}},
			RateLimitID: ptr("rl-team"), RateLimit: &tables.TableRateLimit{ID: "rl-team", RequestMaxLimit: ptr(int64(2))},
		}},
		ModelConfigs: []tables.TableModelConfig{
			{ID: "mc-1", ModelName: "gpt-4o", CalendarAligned: true, Budgets: []tables.TableBudget{{ID: "b-mc1", MaxLimit: 3, ResetDuration: "1d"}}},
			{ID: "mc-2", ModelName: "gpt-4o-mini", RateLimitID: ptr("rl-missing")},
		},
		Providers: []tables.TableProvider{
			{Name: "openai", BudgetID: ptr("b-prov"), RateLimitID: ptr("rl-prov")},
			{Name: "anthropic", BudgetID: ptr("b-anth"), Budget: &tables.TableBudget{ID: "b-anth", MaxLimit: 12, ResetDuration: "1d"}},
		},
		VirtualKeys: []tables.TableVirtualKey{{
			ID: "vk-1", Name: "file key", Value: *schemas.NewSecretVar("sk-file"), CalendarAligned: true,
			Budgets:     []tables.TableBudget{{ID: "b-vk", MaxLimit: 4, ResetDuration: "1d"}},
			RateLimitID: ptr("rl-vk"), RateLimit: &tables.TableRateLimit{ID: "rl-vk", RequestMaxLimit: ptr(int64(4))},
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{{
				ID: 7, Provider: "openai",
				Budgets:     []tables.TableBudget{{ID: "b-pc", MaxLimit: 5, ResetDuration: "1d"}},
				RateLimitID: ptr("rl-pc"), RateLimit: &tables.TableRateLimit{ID: "rl-pc", RequestMaxLimit: ptr(int64(5))},
			}},
		}},
	}

	g := FromConfig(FileInput{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{{ID: "k1"}}}, schemas.Anthropic: {}, schemas.Gemini: {},
	}, Governance: cfg})
	budgets, rateLimits := budgetsByID(g.Budgets), rateLimitsByID(g.RateLimits)
	require.ElementsMatch(t, []string{"b-cust", "b-team", "b-team-listed", "b-mc1", "b-mc-listed", "b-mc-ignored", "b-prov", "b-anth", "b-vk", "b-pc", "b-unheld"}, keysOf(budgets),
		"every listed budget is loaded, with the ones each holder holds")
	require.ElementsMatch(t, []string{"rl-cust", "rl-team", "rl-prov", "rl-vk", "rl-pc", "rl-unheld"}, keysOfRateLimits(rateLimits),
		"every listed rate limit is loaded, with the ones each holder names")
	for _, id := range []string{"b-unheld", "b-mc-ignored"} {
		require.False(t, budgets[id].IsCalendarAligned, "%s, which no holder takes, has the flag off", id)
		require.Empty(t, budgets[id].KeyID+budgets[id].Provider, "%s names no key or provider", id)
	}
	require.False(t, rateLimits["rl-unheld"].IsCalendarAligned)
	require.Empty(t, rateLimits["rl-unheld"].KeyID+rateLimits["rl-unheld"].TeamID+rateLimits["rl-unheld"].CustomerID+rateLimits["rl-unheld"].ModelConfigID+rateLimits["rl-unheld"].Provider)
	require.NotContains(t, g.ModelConfigs[0].BudgetIDs, "b-mc-ignored", "a config nesting budgets does not take listed ones naming it")

	require.Len(t, g.Customers, 1)
	require.Equal(t, &Customer{ID: "cust-1", Name: "Acme", RateLimitID: "rl-cust", BudgetIDs: []string{"b-cust"}, CalendarAligned: true}, g.Customers[0])
	require.Equal(t, 99.0, budgets["b-cust"].MaxLimit, "the listed budget replaces the nested one")
	require.True(t, budgets["b-cust"].IsCalendarAligned)
	require.Equal(t, "cust-1", BudgetCustomerID(budgets["b-cust"]))
	require.Equal(t, int64(99), *rateLimits["rl-cust"].RequestMaxLimit, "the listed rate limit replaces the nested one")
	require.Equal(t, "cust-1", rateLimits["rl-cust"].CustomerID)
	require.True(t, rateLimits["rl-cust"].IsCalendarAligned)

	require.Len(t, g.Teams, 1)
	require.Equal(t, &Team{ID: "team-1", Name: "Platform", CustomerID: "cust-1", RateLimitID: "rl-team", BudgetIDs: []string{"b-team", "b-team-listed"}}, g.Teams[0],
		"a team holds its nested budgets and the listed ones naming it")
	require.Equal(t, "team-1", BudgetTeamID(budgets["b-team-listed"]))
	require.False(t, budgets["b-team"].IsCalendarAligned)
	require.Equal(t, "team-1", rateLimits["rl-team"].TeamID)

	require.Len(t, g.ModelConfigs, 2)
	require.Equal(t, []string{"b-mc1"}, g.ModelConfigs[0].BudgetIDs, "a config nesting budgets ignores listed ones naming it")
	require.True(t, budgets["b-mc1"].IsCalendarAligned)
	require.Equal(t, "mc-1", BudgetModelConfigID(budgets["b-mc1"]))
	require.Equal(t, []string{"b-mc-listed"}, g.ModelConfigs[1].BudgetIDs, "a config nesting none takes the listed budgets naming it")
	require.Equal(t, "rl-missing", g.ModelConfigs[1].RateLimitID, "a rate limit named but declared nowhere keeps its ID")
	require.Equal(t, tables.ModelConfigScopeGlobal, g.ModelConfigs[1].Scope)

	require.Equal(t, []*Provider{
		{Name: schemas.Anthropic, BudgetID: "b-anth"},
		{Name: schemas.Gemini},
		{Name: schemas.OpenAI, BudgetID: "b-prov", RateLimitID: "rl-prov"},
	}, g.Providers, "each configured provider, in name order, names the limits governance gives it, with its settings only")
	require.Equal(t, []*ProviderKey{{Provider: schemas.OpenAI, Key: schemas.Key{ID: "k1"}}}, g.ProviderKeys, "a provider's keys are records of their own")
	require.Equal(t, "openai", BudgetProvider(budgets["b-prov"]))
	require.False(t, budgets["b-prov"].IsCalendarAligned, "a provider has no calendar flag")
	require.Equal(t, "anthropic", BudgetProvider(budgets["b-anth"]), "a provider's nested budget is used when none is listed")
	require.Equal(t, "openai", RateLimitProvider(rateLimits["rl-prov"]))

	require.Len(t, g.VirtualKeys, 1)
	vk := g.VirtualKeys[0]
	require.Equal(t, "sk-file", vk.Value)
	require.Equal(t, []string{"b-vk"}, vk.BudgetIDs)
	require.Equal(t, "rl-vk", vk.RateLimitID)
	require.Equal(t, "vk-1", BudgetKeyID(budgets["b-vk"]))
	require.True(t, budgets["b-vk"].IsCalendarAligned)
	require.Equal(t, "vk-1", rateLimits["rl-vk"].KeyID)
	require.Equal(t, []ProviderLimit{{ConfigID: 7, Provider: "openai", BudgetIDs: []string{"b-pc"}, RateLimitID: "rl-pc"}}, vk.ProviderLimits,
		"a provider config's limits are carried by ID")
	require.Len(t, g.GrantSets, 1, "the key's grant set is kept for the store to intern")
	require.Equal(t, vk.Grants, g.GrantSets[0].ID)
	require.Len(t, g.GrantSets[0].Providers, 1)
	require.Equal(t, "vk-1", BudgetKeyID(budgets["b-pc"]), "a provider config's budget is the key's")
	require.Equal(t, uint(7), *budgets["b-pc"].ProviderConfigID)
	require.True(t, budgets["b-pc"].IsCalendarAligned, "a provider config's limits follow the key's flag")
	require.Equal(t, "vk-1", rateLimits["rl-pc"].KeyID)
	require.True(t, rateLimits["rl-pc"].IsCalendarAligned)

	require.Equal(t, 5.0, cfg.VirtualKeys[0].ProviderConfigs[0].Budgets[0].MaxLimit, "the caller's config is not rewritten")
}

// TestFromConfigNestedRateLimitNeedsItsID checks that a rate limit nested under a holder that does not
// name it by rate_limit_id is not the holder's, as config.json has always been read.
func TestFromConfigNestedRateLimitNeedsItsID(t *testing.T) {
	g := FromConfig(FileInput{Governance: &configstore.GovernanceConfig{
		Teams: []tables.TableTeam{{ID: "team-1", Name: "Platform", RateLimit: &tables.TableRateLimit{ID: "rl-nested", RequestMaxLimit: ptr(int64(2))}}},
	}})
	require.Len(t, g.Teams, 1)
	require.Empty(t, g.Teams[0].RateLimitID)
	require.Empty(t, g.RateLimits)
}

// TestFromConfigWithoutGovernance checks that no governance config builds no governance records,
// and providers with no limits.
func TestFromConfigWithoutGovernance(t *testing.T) {
	g := FromConfig(FileInput{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}})
	require.Empty(t, g.VirtualKeys)
	require.Empty(t, g.Budgets)
	require.Equal(t, []*Provider{{Name: schemas.OpenAI}}, g.Providers)
}

// TestFromConfigProviderKeys checks that a provider's keys become key records under it, ordered by
// their position in config.json, while its provider record holds its settings only and the
// caller's config keeps its keys.
func TestFromConfigProviderKeys(t *testing.T) {
	providers := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{{ID: "z-first"}, {ID: "a-second"}}, SendBackRawResponse: true},
	}
	g := FromConfig(FileInput{Providers: providers})
	require.Equal(t, []*Provider{{Name: schemas.OpenAI, Settings: configstore.ProviderSettings{SendBackRawResponse: true}}}, g.Providers)
	require.Equal(t, []*ProviderKey{
		{Provider: schemas.OpenAI, Order: 0, Key: schemas.Key{ID: "z-first"}},
		{Provider: schemas.OpenAI, Order: 1, Key: schemas.Key{ID: "a-second"}},
	}, g.ProviderKeys, "a key's order is its position in config.json, not its ID")
	require.Len(t, providers[schemas.OpenAI].Keys, 2, "the caller's config is not rewritten")
}

// keysOf lists a budget map's IDs.
func keysOf(m map[string]*Budget) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

// keysOfRateLimits lists a rate limit map's IDs.
func keysOfRateLimits(m map[string]*RateLimit) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

// TestFromConfigKeepsTeamsContentLoggingDecision checks that a config.json team's own content-logging
// decision reaches its record on a gateway without a config store: true and false are kept, and an
// unset field stays nil so the team inherits.
func TestFromConfigKeepsTeamsContentLoggingDecision(t *testing.T) {
	on, off := true, false
	g := FromConfig(FileInput{Governance: &configstore.GovernanceConfig{Teams: []tables.TableTeam{
		{ID: "t-off", Name: "logs off", DisableContentLogging: &on},
		{ID: "t-on", Name: "logs on", DisableContentLogging: &off},
		{ID: "t-inherit", Name: "inherits"},
	}}})
	got := map[string]*bool{}
	for _, team := range g.Teams {
		got[team.ID] = team.DisableContentLogging
	}
	require.NotNil(t, got["t-off"])
	require.True(t, *got["t-off"], "a team that turns content logging off keeps it off")
	require.NotNil(t, got["t-on"])
	require.False(t, *got["t-on"], "a team that keeps content logging on says so")
	require.Nil(t, got["t-inherit"], "a team without the field inherits")
}
