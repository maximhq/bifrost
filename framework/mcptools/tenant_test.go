package mcptools

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func ptr(s string) *string { return &s }

// teamCtx is a request governance resolved to a virtual key in team: the same
// context value governance stamps on a real request.
func teamCtx(team string) context.Context {
	return context.WithValue(context.Background(), schemas.BifrostContextKeyGovernanceTeamID, team)
}

func customerCtx(customer string) context.Context {
	return context.WithValue(context.Background(), schemas.BifrostContextKeyGovernanceCustomerID, customer)
}

// twoTenants is a deployment with team-a and team-b under different customers,
// each with a key, a budget, a rate limit and a model config.
func twoTenants() *fakeGovernanceReader {
	return &fakeGovernanceReader{
		customers: map[string]*tables.TableCustomer{
			"cust-a": {ID: "cust-a", Name: "a"},
			"cust-b": {ID: "cust-b", Name: "b"},
		},
		teams: map[string]*tables.TableTeam{
			"team-a":  {ID: "team-a", Name: "a", CustomerID: ptr("cust-a"), RateLimitID: ptr("rl-team-a")},
			"team-a2": {ID: "team-a2", Name: "a2", CustomerID: ptr("cust-a")},
			"team-b":  {ID: "team-b", Name: "b", CustomerID: ptr("cust-b"), RateLimitID: ptr("rl-team-b")},
		},
		byID: map[string]*tables.TableVirtualKey{
			"vk-a":  {ID: "vk-a", Name: "a", TeamID: ptr("team-a")},
			"vk-a2": {ID: "vk-a2", Name: "a2", TeamID: ptr("team-a2")},
			"vk-b":  {ID: "vk-b", Name: "b", TeamID: ptr("team-b")},
		},
		budgets: map[string]*tables.TableBudget{
			"budget-a":  {ID: "budget-a", MaxLimit: 10, ResetDuration: "1d", TeamID: ptr("team-a")},
			"budget-b":  {ID: "budget-b", MaxLimit: 10, ResetDuration: "1d", TeamID: ptr("team-b")},
			"budget-mc": {ID: "budget-mc", MaxLimit: 10, ResetDuration: "1d", ModelConfigID: ptr("mc-b")},
		},
		rateLimits: map[string]*tables.TableRateLimit{
			"rl-team-a": {ID: "rl-team-a"},
			"rl-team-b": {ID: "rl-team-b"},
		},
		modelConfigs: map[string]*tables.TableModelConfig{
			"mc-a": {ID: "mc-a", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeVirtualKey, ScopeID: ptr("vk-a")},
			"mc-b": {ID: "mc-b", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeVirtualKey, ScopeID: ptr("vk-b")},
		},
	}
}

// A key scoped to team-a changes team-a's rows and nothing of team-b's, even
// when the tool is granted to it - the ownership the store does not check.
func TestTeamScopedWritesStayInsideTheTeam(t *testing.T) {
	ctx := teamCtx("team-a")
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"update_budget", map[string]any{"budget_id": "budget-b", "max_limit": 99.0}},
		{"update_budget", map[string]any{"budget_id": "budget-mc", "max_limit": 99.0}},
		{"create_budget", map[string]any{"max_limit": 5.0, "reset_duration": "1d", "team_id": "team-b"}},
		{"create_budget", map[string]any{"max_limit": 5.0, "reset_duration": "1d", "customer_id": "cust-a"}},
		{"update_virtual_key", map[string]any{"virtual_key_id": "vk-b", "name": "mine"}},
		{"update_virtual_key", map[string]any{"virtual_key_id": "vk-a", "team_id": "team-b"}},
		{"deactivate_virtual_key", map[string]any{"virtual_key_id": "vk-b"}},
		{"rotate_virtual_key", map[string]any{"virtual_key_id": "vk-b"}},
		{"create_virtual_key", map[string]any{"name": "x", "team_id": "team-b"}},
		{"create_virtual_key", map[string]any{"name": "x", "customer_id": "cust-a"}},
		{"create_rate_limit", map[string]any{"owner_type": "team", "owner_id": "team-b", "request_max_limit": 1.0, "request_reset_duration": "1h"}},
		{"create_rate_limit", map[string]any{"owner_type": "virtual_key", "owner_id": "vk-b", "request_max_limit": 1.0, "request_reset_duration": "1h"}},
		{"update_rate_limit", map[string]any{"rate_limit_id": "rl-team-b", "request_max_limit": 1.0, "request_reset_duration": "1h"}},
		{"create_model_config", map[string]any{"model_name": "gpt-4o"}},
		{"create_model_config", map[string]any{"model_name": "gpt-4o", "scope": "virtual_key", "scope_id": "vk-b"}},
		{"update_model_config", map[string]any{"model_config_id": "mc-b", "model_name": "gpt-5"}},
		{"create_team", map[string]any{"name": "sibling"}},
	}
	for _, tc := range cases {
		fake := twoTenants()
		_, err := runToolCtx(t, ctx, tc.tool, &Deps{Governance: fake}, tc.args)
		require.Error(t, err, "%s %v", tc.tool, tc.args)
		require.NotContains(t, err.Error(), "failed", "%s should be refused, not fail", tc.tool)
		require.Equal(t, 10.0, fake.budgets["budget-b"].MaxLimit, tc.tool)
		require.Equal(t, "b", fake.byID["vk-b"].Name, tc.tool)
		require.Equal(t, "team-a", *fake.byID["vk-a"].TeamID, tc.tool)
	}
}

func TestTeamScopedWritesWorkOnTheirOwnRows(t *testing.T) {
	ctx := teamCtx("team-a")
	fake := twoTenants()
	deps := &Deps{Governance: fake}

	_, err := runToolCtx(t, ctx, "update_budget", deps, map[string]any{"budget_id": "budget-a", "max_limit": 42.0})
	require.NoError(t, err)
	require.Equal(t, 42.0, fake.budgets["budget-a"].MaxLimit)

	_, err = runToolCtx(t, ctx, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-a", "name": "renamed"})
	require.NoError(t, err)
	require.Equal(t, "renamed", fake.byID["vk-a"].Name)

	_, err = runToolCtx(t, ctx, "update_rate_limit", deps, map[string]any{"rate_limit_id": "rl-team-a", "request_max_limit": 5.0, "request_reset_duration": "1h"})
	require.NoError(t, err)

	_, err = runToolCtx(t, ctx, "update_model_config", deps, map[string]any{"model_config_id": "mc-a", "model_name": "gpt-5"})
	require.NoError(t, err)

	_, err = runToolCtx(t, ctx, "create_model_config", deps, map[string]any{"model_name": "o3", "scope": "virtual_key", "scope_id": "vk-a"})
	require.NoError(t, err)

	// With no team or customer named, a new key lands in the caller's team,
	// never outside every tenant.
	result, err := runToolCtx(t, ctx, "create_virtual_key", deps, map[string]any{"name": "new"})
	require.NoError(t, err)
	require.Equal(t, "team-a", *fake.createdVK.TeamID)
	require.Equal(t, "team-a", result.(map[string]any)["team_id"])
}

// A customer key reaches every team under its customer, and no further.
func TestCustomerScopedWritesCoverItsTeams(t *testing.T) {
	ctx := customerCtx("cust-a")
	fake := twoTenants()
	deps := &Deps{Governance: fake}

	_, err := runToolCtx(t, ctx, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-a2", "team_id": "team-a"})
	require.NoError(t, err)
	_, err = runToolCtx(t, ctx, "create_budget", deps, map[string]any{"max_limit": 5.0, "reset_duration": "1d", "customer_id": "cust-a"})
	require.NoError(t, err)

	_, err = runToolCtx(t, ctx, "create_team", deps, map[string]any{"name": "new-team"})
	require.NoError(t, err)
	for _, team := range fake.teams {
		if team.Name == "new-team" {
			require.Equal(t, "cust-a", *team.CustomerID, "a new team defaults to the caller's customer")
		}
	}

	_, err = runToolCtx(t, ctx, "update_virtual_key", deps, map[string]any{"virtual_key_id": "vk-b", "name": "x"})
	require.ErrorContains(t, err, "outside this virtual key's customer")
	_, err = runToolCtx(t, ctx, "create_team", deps, map[string]any{"name": "t", "customer_id": "cust-b"})
	require.ErrorContains(t, err, "outside")
	_, err = runToolCtx(t, ctx, "update_rate_limit", deps, map[string]any{"rate_limit_id": "rl-team-b", "request_max_limit": 5.0, "request_reset_duration": "1h"})
	require.ErrorContains(t, err, "outside")
	_, err = runToolCtx(t, ctx, "update_rate_limit", deps, map[string]any{"rate_limit_id": "rl-team-a", "request_max_limit": 5.0, "request_reset_duration": "1h"})
	require.NoError(t, err, "a team under the caller's customer is inside its tenant")
}

// Every tool not marked tenantScoped reads or changes deployment-wide state, so
// a scoped key is refused before it runs - even with the tool granted.
func TestDeploymentWideToolsRefuseScopedKeys(t *testing.T) {
	governed := schemas.NewBifrostContext(teamCtx("team-a"), schemas.NoDeadline)
	governed.SetGrant(&governedGrant{access: grantedAccess{}})
	refused := 0
	for _, tool := range buildTools() {
		if tool.tenantScoped {
			continue
		}
		refused++
		result := runToolViaHandlerCtx(t, governed, tool.name, &Deps{}, map[string]any{})
		require.True(t, result.IsError, tool.name)
		require.Contains(t, result.Content[0].(mcp.TextContent).Text, "deployment-wide", tool.name)
	}
	// Reads are covered too: provider, plugin and webhook config is not a
	// team's to see.
	for _, name := range []string{"list_providers", "list_provider_keys", "list_plugins", "list_webhooks", "get_warp_config", "list_mcp_clients"} {
		tool, ok := toolByName(buildTools(), name)
		require.True(t, ok, name)
		require.False(t, tool.tenantScoped, name)
	}
	require.Greater(t, refused, 30)
}

// A scope that is not one team or customer leaves nothing to check ownership
// against, so every write fails closed.
func TestUnknownScopeRefusesWrites(t *testing.T) {
	scope := func(db *gorm.DB) *gorm.DB { return db }
	ctx := queryscope.WithQueryScope(context.Background(), scope)
	_, err := runToolCtx(t, ctx, "update_budget", &Deps{Governance: twoTenants()}, map[string]any{"budget_id": "budget-a", "max_limit": 1.0})
	require.ErrorIs(t, err, errUnknownTenant)

	multi := context.WithValue(context.Background(), schemas.BifrostContextKeyGovernanceTeamIDs, []string{"team-a", "team-b"})
	_, err = runToolCtx(t, multi, "create_virtual_key", &Deps{Governance: twoTenants()}, map[string]any{"name": "x"})
	require.ErrorIs(t, err, errUnknownTenant)
}

// An unscoped caller - a key with no team or customer, or an admin - is not
// narrowed at all.
func TestUnscopedCallerWritesAnywhere(t *testing.T) {
	fake := twoTenants()
	_, err := runToolCtx(t, context.Background(), "update_budget", &Deps{Governance: fake}, map[string]any{"budget_id": "budget-b", "max_limit": 7.0})
	require.NoError(t, err)
	require.Equal(t, 7.0, fake.budgets["budget-b"].MaxLimit)
}

func names(rows []map[string]any, key string) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row[key].(string))
	}
	return out
}

// A team key's reads show its own team's governance rows and nothing else -
// including the rows the budget, rate-limit and model-config stores return
// with no row filter at all.
func TestTeamScopedReadsSeeOnlyTheTeam(t *testing.T) {
	ctx := teamCtx("team-a")
	deps := &Deps{Governance: twoTenants()}
	run := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := runToolCtx(t, ctx, name, deps, args)
		require.NoError(t, err, name)
		return result.(map[string]any)
	}

	require.ElementsMatch(t, []string{"vk-a"}, names(run("list_virtual_keys", map[string]any{})["virtual_keys"].([]map[string]any), "id"))
	require.ElementsMatch(t, []string{"team-a"}, names(run("list_teams", map[string]any{})["teams"].([]map[string]any), "id"))
	require.Empty(t, run("list_customers", map[string]any{})["customers"], "a team key owns no customer")
	require.ElementsMatch(t, []string{"budget-a"}, names(run("list_budgets", map[string]any{})["budgets"].([]map[string]any), "id"))
	require.ElementsMatch(t, []string{"rl-team-a"}, names(run("list_rate_limits", map[string]any{})["rate_limits"].([]map[string]any), "id"))
	require.ElementsMatch(t, []string{"mc-a"}, names(run("list_model_configs", map[string]any{})["model_configs"].([]map[string]any), "id"))

	// Another tenant's row reads exactly like a missing one.
	for _, tc := range []struct{ tool, arg, id, want string }{
		{"describe_virtual_key", "virtual_key_id", "vk-b", `no virtual key with id "vk-b"`},
		{"describe_team", "team_id", "team-b", `no team with id "team-b"`},
		{"describe_customer", "customer_id", "cust-a", `no customer with id "cust-a"`},
		{"describe_budget", "budget_id", "budget-b", `no budget with id "budget-b"`},
		{"describe_rate_limit", "rate_limit_id", "rl-team-b", `no rate limit with id "rl-team-b"`},
		{"describe_model_config", "model_config_id", "mc-b", `no model config with id "mc-b"`},
	} {
		_, err := runToolCtx(t, ctx, tc.tool, deps, map[string]any{tc.arg: tc.id})
		require.ErrorContains(t, err, tc.want, tc.tool)
	}
	for _, tc := range []struct{ tool, arg, id string }{
		{"describe_virtual_key", "virtual_key_id", "vk-a"},
		{"describe_team", "team_id", "team-a"},
		{"describe_budget", "budget_id", "budget-a"},
		{"describe_rate_limit", "rate_limit_id", "rl-team-a"},
		{"describe_model_config", "model_config_id", "mc-a"},
	} {
		_, err := runToolCtx(t, ctx, tc.tool, deps, map[string]any{tc.arg: tc.id})
		require.NoError(t, err, tc.tool)
	}
}

// A customer key reads every team under its customer and its own customer row.
func TestCustomerScopedReadsCoverItsTeams(t *testing.T) {
	ctx := customerCtx("cust-a")
	deps := &Deps{Governance: twoTenants()}
	result, err := runToolCtx(t, ctx, "list_virtual_keys", deps, map[string]any{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"vk-a", "vk-a2"}, names(result.(map[string]any)["virtual_keys"].([]map[string]any), "id"))
	result, err = runToolCtx(t, ctx, "list_customers", deps, map[string]any{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"cust-a"}, names(result.(map[string]any)["customers"].([]map[string]any), "id"))
}

// A scope that is not one team or customer keeps the store's own row filter
// where the store has one (virtual keys), and is refused where it has none.
func TestUnknownScopeReads(t *testing.T) {
	scope := func(db *gorm.DB) *gorm.DB { return db }
	ctx := queryscope.WithQueryScope(context.Background(), scope)
	fake := twoTenants()
	_, err := runToolCtx(t, ctx, "describe_virtual_key", &Deps{Governance: fake}, map[string]any{"virtual_key_id": "vk-b"})
	require.NoError(t, err)
	require.NotNil(t, queryscope.FromContext(fake.sawContext), "the store still receives the request's scope")
	_, err = runToolCtx(t, ctx, "list_budgets", &Deps{Governance: fake}, map[string]any{})
	require.ErrorIs(t, err, errUnknownTenant)
}

// /v1/mcp/tool/execute stamps no row filter; the tool handler derives one from
// the key's tenant, so log reads there are scoped like on /mcp. A filter a
// route already set is left alone.
func TestLogToolsGetTheTenantScopeOnEveryRoute(t *testing.T) {
	governed := schemas.NewBifrostContext(teamCtx("team-a"), schemas.NoDeadline)
	governed.SetGrant(&governedGrant{access: grantedAccess{}})
	logs := &fakeLogReader{}
	result := runToolViaHandlerCtx(t, governed, "query_mcp_logs", &Deps{LogManager: logs}, map[string]any{"filters": map[string]any{}})
	require.False(t, result.IsError, result.Content)
	scope := queryscope.FromContext(logs.sawContext)
	require.NotNil(t, scope, "a team key's log read must carry a row filter")
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{DryRun: true})
	require.NoError(t, err)
	sql := scope(db.Table("logs")).Find(&[]map[string]any{}).Statement.SQL.String()
	require.Contains(t, sql, "team_id = ?")

	marker := func(db *gorm.DB) *gorm.DB { return db.Where("route = ?", "set") }
	stamped := schemas.NewBifrostContext(queryscope.WithQueryScope(teamCtx("team-a"), marker), schemas.NoDeadline)
	stamped.SetGrant(&governedGrant{access: grantedAccess{}})
	logs = &fakeLogReader{}
	runToolViaHandlerCtx(t, stamped, "query_mcp_logs", &Deps{LogManager: logs}, map[string]any{"filters": map[string]any{}})
	sql = queryscope.FromContext(logs.sawContext)(db.Table("logs")).Find(&[]map[string]any{}).Statement.SQL.String()
	require.Contains(t, sql, "route = ?")
}

// semantic_search_logs does not use LogManager, but the searcher hydrates its
// hits from the log store on the ctx it is given, so it needs the tenant's row
// filter like any log read. On /v1/mcp/tool/execute the handler derives it; on
// /mcp the one the route stamped is kept.
func TestSemanticSearchGetsTheTenantScopeOnEveryRoute(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{DryRun: true})
	require.NoError(t, err)
	args := map[string]any{"query": "payment failures", "filters": map[string]any{}}

	governed := schemas.NewBifrostContext(teamCtx("team-a"), schemas.NoDeadline)
	governed.SetGrant(&governedGrant{access: grantedAccess{}})
	searcher := &fakeSemanticSearcher{}
	result := runToolViaHandlerCtx(t, governed, "semantic_search_logs", &Deps{Semantic: searcher}, args)
	require.False(t, result.IsError, result.Content)
	scope := queryscope.FromContext(searcher.sawContext)
	require.NotNil(t, scope, "a team key's semantic search must hydrate under a row filter")
	require.Contains(t, scope(db.Table("logs")).Find(&[]map[string]any{}).Statement.SQL.String(), "team_id = ?")

	marker := func(db *gorm.DB) *gorm.DB { return db.Where("route = ?", "set") }
	stamped := schemas.NewBifrostContext(queryscope.WithQueryScope(teamCtx("team-a"), marker), schemas.NoDeadline)
	stamped.SetGrant(&governedGrant{access: grantedAccess{}})
	searcher = &fakeSemanticSearcher{}
	result = runToolViaHandlerCtx(t, stamped, "semantic_search_logs", &Deps{Semantic: searcher}, args)
	require.False(t, result.IsError, result.Content)
	scope = queryscope.FromContext(searcher.sawContext)
	require.NotNil(t, scope, "the route's row filter must reach the searcher")
	require.Contains(t, scope(db.Table("logs")).Find(&[]map[string]any{}).Statement.SQL.String(), "route = ?")
}

// Governance tools for a known tenant read without the blind row filter - it
// has no team_id column to match on teams - and filter by ownership instead.
func TestGovernanceToolsReadWithoutTheRowFilter(t *testing.T) {
	marker := func(db *gorm.DB) *gorm.DB { return db.Where("team_id = ?", "team-a") }
	governed := schemas.NewBifrostContext(queryscope.WithQueryScope(teamCtx("team-a"), marker), schemas.NoDeadline)
	governed.SetGrant(&governedGrant{access: grantedAccess{}})
	fake := twoTenants()
	result := runToolViaHandlerCtx(t, governed, "describe_virtual_key", &Deps{Governance: fake}, map[string]any{"virtual_key_id": "vk-a"})
	require.False(t, result.IsError, result.Content)
	require.Nil(t, queryscope.FromContext(fake.sawContext))
}
