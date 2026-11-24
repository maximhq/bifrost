package registry

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// pastSQLiteParamLimit is more IDs than one SQLite statement may bind (32,766), so a read that
// binds each ID as its own parameter fails on it.
const pastSQLiteParamLimit = 40_000

// withMissingIDs returns ids followed by enough IDs with no row to pass SQLite's parameter limit.
func withMissingIDs(ids ...string) []string {
	out := make([]string, 0, len(ids)+pastSQLiteParamLimit)
	out = append(out, ids...)
	for i := range pastSQLiteParamLimit {
		out = append(out, fmt.Sprintf("missing-%05d", i))
	}
	return out
}

// TestProvidersByNameReadsMoreNamesThanTheParameterLimit checks that a refresh of more provider
// names than one statement may bind still reads the ones that exist.
func TestProvidersByNameReadsMoreNamesThanTheParameterLimit(t *testing.T) {
	store := newProviderDB(t, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {key("o1")},
	})
	rows, err := ProvidersByName(context.Background(), store.DB(), withMissingIDs(string(schemas.OpenAI)))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, string(schemas.OpenAI), ProviderName(rows[0]))
}

// TestProviderKeysByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more key IDs, or
// of more providers' keys, than one statement may bind still reads the ones that exist.
func TestProviderKeysByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {key("o1")},
	})
	recs, err := ProviderKeysByID(ctx, store.DB(), withMissingIDs("o1"))
	require.NoError(t, err)
	require.Equal(t, []string{"o1"}, keyIDs(recs))
	recs, err = ProviderKeysByProviders(ctx, store.DB(), withMissingIDs(string(schemas.OpenAI)))
	require.NoError(t, err)
	require.Equal(t, []string{"o1"}, keyIDs(recs))
}

// TestMCPClientsByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more MCP client IDs
// than one statement may bind still reads the ones that exist, on either side of the limit.
func TestMCPClientsByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, nil)
	require.NoError(t, store.CreateMCPClientConfig(ctx, mcpClient("a")))
	require.NoError(t, store.CreateMCPClientConfig(ctx, mcpClient("z")))
	rows, err := MCPClientsByID(ctx, store.DB(), append(withMissingIDs("a"), "z"))
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, MCPClientID(row))
	}
	slices.Sort(ids)
	require.Equal(t, []string{"a", "z"}, ids, "the client listed after the missing IDs is read too")
}

// TestTeamsByIDReadsMoreTeamsThanTheParameterLimit checks that a refresh of more teams than one
// statement may bind reads every one, with the budgets each holds: the read of the teams and the
// read of their budgets each bind the whole ID list.
func TestTeamsByIDReadsMoreTeamsThanTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	teams := make([]tables.TableTeam, 0, pastSQLiteParamLimit)
	ids := make([]string, 0, pastSQLiteParamLimit)
	for i := range pastSQLiteParamLimit {
		id := fmt.Sprintf("team-%05d", i)
		teams = append(teams, tables.TableTeam{ID: id, Name: id})
		ids = append(ids, id)
	}
	require.NoError(t, db.Omit(clause.Associations).CreateInBatches(teams, 1000).Error)
	held := budgetRow("tb-1", 1)
	held.TeamID = ptr("team-00007")
	insert(t, db, held)

	rows, err := TeamsByID(ctx, db, ids)
	require.NoError(t, err)
	require.Len(t, rows, pastSQLiteParamLimit)
	for _, r := range rows {
		if r.ID == "team-00007" {
			require.Equal(t, []string{"tb-1"}, r.BudgetIDs)
		} else {
			require.Empty(t, r.BudgetIDs)
		}
	}
}

// TestModelConfigsByKeysReadsMoreKeysThanTheParameterLimit checks that a refresh of the model
// configs of more keys than one statement may bind still reads the ones those keys scope.
func TestModelConfigsByKeysReadsMoreKeysThanTheParameterLimit(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	insert(t, db, &tables.TableModelConfig{ID: "mc-1", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeVirtualKey, ScopeID: ptr("vk-1")})
	rows, err := ModelConfigsByKeys(context.Background(), db, withMissingIDs("vk-1"))
	require.NoError(t, err)
	require.Equal(t, []string{"mc-1"}, modelConfigIDsOf(rows))
}

// TestVirtualMCPsByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more virtual MCP
// IDs than one statement may bind still reads the ones that exist; their row IDs are integers.
func TestVirtualMCPsByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	insert(t, db, &tables.TableVirtualMCP{
		Name: "vmcp", EndpointSlug: "vmcp", Enabled: true,
		ParsedTools: []tables.MCPToolSpec{{MCPClientID: "c1", ToolNames: []string{"search"}}},
	})
	ids := []string{NumericID(1)}
	for i := range uint(pastSQLiteParamLimit) {
		ids = append(ids, NumericID(1_000_000+i))
	}
	rows, err := VirtualMCPsByID(context.Background(), db, ids)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, NumericID(1), VirtualMCPID(rows[0]))
}

// TestAgentsByNameReadsMoreNamesThanTheParameterLimit checks that a refresh of more agent names than
// one statement may bind still reads the registrations that exist.
func TestAgentsByNameReadsMoreNamesThanTheParameterLimit(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	require.NoError(t, db.Create(&tables.TableAgentRegistration{Name: "alpha", AgentCardURL: "https://alpha.example/card", Enabled: true}).Error)
	rows, err := AgentsByName(context.Background(), db, withMissingIDs("alpha"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "alpha", rows[0].Name)
}

// TestLimitReadsReadMoreIDsThanTheParameterLimit checks that every budget and rate limit reader,
// by ID and by holder, still reads the limits that exist when it is given more IDs than one
// statement may bind, the holder reads that filter a holder table in a subquery included.
func TestLimitReadsReadMoreIDsThanTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	limitHolders(t, db)
	var lr LimitReads
	budgetReads := []struct {
		name string
		read func(context.Context, *gorm.DB, []string) ([]*Budget, error)
		id   string
		want []string
	}{
		{"by ID", lr.BudgetsByID, "b-key", []string{"b-key"}},
		{"by keys", lr.BudgetsByKeys, "vk-a", []string{"b-key", "b-pc"}},
		{"by teams", lr.BudgetsByTeams, "team-a", []string{"b-team"}},
		{"by customers", lr.BudgetsByCustomers, "cust-a", []string{"b-cust"}},
		{"by model configs", lr.BudgetsByModelConfigs, "mc-a", []string{"b-mc"}},
		{"by providers", lr.BudgetsByProviders, "openai", []string{"b-prov"}},
	}
	for _, c := range budgetReads {
		rows, err := c.read(ctx, db, withMissingIDs(c.id))
		require.NoError(t, err, "budgets %s", c.name)
		require.ElementsMatch(t, c.want, budgetIDsOf(rows), "budgets %s", c.name)
	}
	rateLimitReads := []struct {
		name string
		read func(context.Context, *gorm.DB, []string) ([]*RateLimit, error)
		id   string
		want []string
	}{
		{"by ID", lr.RateLimitsByID, "rl-key", []string{"rl-key"}},
		{"by keys", lr.RateLimitsByKeys, "vk-a", []string{"rl-key", "rl-pc"}},
		{"by teams", lr.RateLimitsByTeams, "team-a", []string{"rl-team"}},
		{"by customers", lr.RateLimitsByCustomers, "cust-a", []string{"rl-cust"}},
		{"by model configs", lr.RateLimitsByModelConfigs, "mc-a", []string{"rl-mc"}},
		{"by providers", lr.RateLimitsByProviders, "openai", []string{"rl-prov"}},
	}
	for _, c := range rateLimitReads {
		rows, err := c.read(ctx, db, withMissingIDs(c.id))
		require.NoError(t, err, "rate limits %s", c.name)
		require.ElementsMatch(t, c.want, rateLimitIDsOf(rows), "rate limits %s", c.name)
	}
}

// TestRoutingRulesByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more routing rule
// IDs than one statement may bind still reads the rules that exist, with their targets.
func TestRoutingRulesByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	require.NoError(t, db.Create(&tables.TableRoutingRule{
		ID: "r-1", Name: "one", Scope: "global", CelExpression: "true",
		Targets: []tables.TableRoutingTarget{{Provider: schemas.Ptr("openai"), Weight: 1}},
	}).Error)
	rows, err := RoutingRulesByID(context.Background(), db, withMissingIDs("r-1"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "r-1", RoutingRuleID(rows[0]))
	require.Len(t, rows[0].Targets, 1)
}

// TestModelRoutersByIDReadsMoreRoutersThanTheParameterLimit checks that a refresh of more model
// routers than one statement may bind reads every one, with its targets: the read of the routers
// and the read of their targets each bind the whole ID list.
func TestModelRoutersByIDReadsMoreRoutersThanTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, nil)
	db := store.DB()
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{ID: "mr-00000", Name: "mr-00000", ComplexityAnalyzerConfig: configstore.ComplexityAnalyzerConfig{
		TierBoundaries: configstore.ComplexityTierBoundaries{SimpleMedium: 0.10, MediumComplex: 0.30},
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords: []string{"hello"}, MediumKeywords: []string{"api"}, ComplexKeywords: []string{"tradeoffs"},
		},
	}}))
	insert(t, db, &tables.TableModelRouterTarget{RouterID: "mr-00000", Tier: "simple", Model: ptr("gpt-4o-mini"), Weight: 1})
	var row tables.TableModelRouter
	require.NoError(t, db.First(&row, "id = ?", "mr-00000").Error)
	ids := []string{row.ID}
	clones := make([]tables.TableModelRouter, 0, pastSQLiteParamLimit)
	for i := 1; i < pastSQLiteParamLimit; i++ {
		clone := row
		clone.ID = fmt.Sprintf("mr-%05d", i)
		clone.Name = clone.ID
		clones = append(clones, clone)
		ids = append(ids, clone.ID)
	}
	require.NoError(t, db.Omit(clause.Associations).CreateInBatches(clones, 200).Error)

	rows, err := ModelRoutersByID(ctx, db, append(ids, "missing"))
	require.NoError(t, err)
	require.Len(t, rows, pastSQLiteParamLimit)
	for _, r := range rows {
		if r.Router.ID == "mr-00000" {
			require.Len(t, r.Router.Targets, 1)
		} else {
			require.Empty(t, r.Router.Targets)
		}
	}
}

// TestPromptsByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more prompt IDs than one
// statement may bind still reads the prompts that exist.
func TestPromptsByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	insert(t, db, &tables.TablePrompt{ID: "p-1", Name: "one"})
	rows, err := PromptsByID(context.Background(), db, withMissingIDs("p-1"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "p-1", PromptID(rows[0]))
}

// TestPromptVersionsReadMoreVersionsThanTheParameterLimit checks that reading more prompt versions
// than one statement may bind, through their prompt or by ID, reads every one with its messages:
// the read of the versions and the read of their messages each bind the whole ID list, whose row
// IDs are integers.
func TestPromptVersionsReadMoreVersionsThanTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	insert(t, db, &tables.TablePrompt{ID: "p-1", Name: "one"})
	versions := make([]tables.TablePromptVersion, 0, pastSQLiteParamLimit)
	for i := range pastSQLiteParamLimit {
		versions = append(versions, tables.TablePromptVersion{PromptID: "p-1", VersionNumber: i + 1, Provider: "openai", Model: "gpt-4o"})
	}
	require.NoError(t, db.Omit(clause.Associations).CreateInBatches(versions, 500).Error)
	first := versions[0].ID
	insert(t, db, &tables.TablePromptVersionMessage{PromptID: "p-1", VersionID: first, OrderIndex: 0, Message: tables.PromptMessage(`{"role":"user","content":"hi"}`)})

	check := func(name string, rows []*PromptVersion, err error) {
		t.Helper()
		require.NoError(t, err, name)
		require.Len(t, rows, pastSQLiteParamLimit, name)
		for _, v := range rows {
			if v.ID == first {
				require.Len(t, v.Messages, 1, name)
			} else {
				require.Empty(t, v.Messages, name)
			}
		}
	}
	rows, err := PromptVersionsByPrompts(ctx, db, []string{"p-1"})
	check("through the prompt", rows, err)
	ids := make([]string, 0, pastSQLiteParamLimit+1)
	for _, v := range versions {
		ids = append(ids, NumericID(v.ID))
	}
	rows, err = PromptVersionsByID(ctx, db, append(ids, NumericID(1_000_000)))
	check("by ID", rows, err)
}

// TestPricingOverridesByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more pricing
// override IDs than one statement may bind still reads the overrides that exist.
func TestPricingOverridesByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	insert(t, db, &tables.TablePricingOverride{ID: "po-1", Name: "one", ScopeKind: "global", MatchType: "exact", Pattern: "gpt-4o"})
	rows, err := PricingOverridesByID(context.Background(), db, withMissingIDs("po-1"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "po-1", PricingOverrideID(rows[0]))
}

// TestWebhookEndpointsByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more webhook
// endpoint IDs than one statement may bind still reads the endpoints that exist.
func TestWebhookEndpointsByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	insert(t, db, &tables.TableWebhookEndpoint{
		ID: "wh-1", Name: "billing-hook", URL: "https://93.184.216.34/hook",
		Events: []tables.WebhookEvent{tables.WebhookEventAsyncJobCompleted},
	})
	rows, err := WebhookEndpointsByID(context.Background(), db, withMissingIDs("wh-1"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "wh-1", WebhookEndpointID(rows[0]))
}
