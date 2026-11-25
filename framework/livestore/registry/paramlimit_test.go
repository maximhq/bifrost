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
	store := newProviderDB(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{key("o1")}},
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
	store := newProviderDB(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{key("o1")}},
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
