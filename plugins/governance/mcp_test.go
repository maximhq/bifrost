package governance

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolsByClient(permits []schemas.MCPPermit) map[string][]string {
	m := make(map[string][]string, len(permits))
	for _, p := range permits {
		m[p.Client] = p.Tools
	}
	return m
}

// vmcp builds a virtual MCP definition with the given name, enabled flag and tool specs.
func vmcp(name string, enabled bool, specs ...configstoreTables.MCPToolSpec) configstoreTables.TableVirtualMCP {
	return configstoreTables.TableVirtualMCP{Name: name, Enabled: enabled, ParsedTools: specs}
}

// spec builds a virtual MCP tool spec naming one client and its tools.
func spec(clientID string, tools ...string) configstoreTables.MCPToolSpec {
	return configstoreTables.MCPToolSpec{MCPClientID: clientID, ToolNames: tools}
}

// permitFor resolves vk's permit with the given Virtual MCPs assigned to it, the given client-name
// resolution, and the given allowed-by-default clients. The definitions are written as rows (each
// under its own name, since names are unique) and the key's record names them.
func permitFor(t testing.TB, vk *configstoreTables.TableVirtualKey, assigned []configstoreTables.TableVirtualMCP, names, allowedByDefault map[string]string) *grant.Permit {
	t.Helper()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, &liveFixture{clientNames: names, allowedByDefaultClients: allowedByDefault})
	var ids []uint
	for i := range assigned {
		def := assigned[i]
		def.Name = fmt.Sprintf("%s-%d", def.Name, i)
		def.EndpointSlug = ""
		ids = append(ids, h.putVirtualMCP(&def))
	}
	rec := virtualKeyRecord(h.live, vk, ids...)
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	return gs.permitForVirtualKey(emptyCtx(), rec)
}

// TestPermitForVirtualKey_VirtualMCPsMirrorConfigs pins that an assigned Virtual MCP grants exactly
// like the key's own MCP config: it owns the clients it names, unions per client, and blocks the
// allowed-by-default fallback for them.
func TestPermitForVirtualKey_VirtualMCPsMirrorConfigs(t *testing.T) {
	names := map[string]string{"cA": "alpha", "cB": "beta"}

	t.Run("a virtual MCP adds a client the key did not configure", func(t *testing.T) {
		vk := buildVKWithMCPConfigs("cA", "alpha", []string{"x"}) // vk.ID == "vk-1"
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB", "read"))}, names, nil)
		assert.Equal(t, map[string][]string{"cA": {"x"}, "cB": {"read"}}, toolsByClient(permit.MCPPermits()),
			"the direct config must survive and the virtual MCP must be added")
	})

	t.Run("a virtual MCP on the key's own client unions the tools", func(t *testing.T) {
		vk := buildVKWithMCPConfigs("cA", "alpha", []string{"x"})
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cA", "y"))}, names, nil)
		assert.Equal(t, map[string][]string{"cA": {"x", "y"}}, toolsByClient(permit.MCPPermits()))
	})

	t.Run("a wildcard tool name grants the whole client", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB", grant.Wildcard))}, names, nil)
		assert.Equal(t, map[string][]string{"cB": {grant.Wildcard}}, toolsByClient(permit.MCPPermits()))
	})

	t.Run("empty tool names grant nothing (deny-by-default)", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB"))}, names, nil)
		assert.Empty(t, permit.MCPPermits())
	})

	t.Run("a disabled virtual MCP contributes nothing", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", false, spec("cB", "read"))}, names, nil)
		assert.Empty(t, permit.MCPPermits())
	})

	t.Run("a virtual MCP client with no configured name is dropped", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cX", "read"))}, names, nil)
		assert.Empty(t, permit.MCPPermits())
	})

	t.Run("like a config, a virtual MCP client blocks the allowed-by-default wildcard", func(t *testing.T) {
		// cB is open to every key by default, but the virtual MCP names only "read", so the key gets
		// exactly that, not the wildcard — the same way an explicit config scopes an open client.
		vk := buildVKNoMCPConfigs()
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB", "read"))}, names, map[string]string{"cB": "beta"})
		assert.Equal(t, map[string][]string{"cB": {"read"}}, toolsByClient(permit.MCPPermits()),
			"an assigned virtual MCP scopes an otherwise-open client, mirroring an explicit config")
	})

	t.Run("an empty tool list denies even an allowed-by-default client", func(t *testing.T) {
		// cB is open to every key by default. An empty-tool spec must still scope it to nothing, not
		// let the allowed-by-default fallback reopen it — the spec has to register cB as configured.
		vk := buildVKNoMCPConfigs()
		permit := permitFor(t, vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB"))}, names, map[string]string{"cB": "beta"})
		assert.Empty(t, permit.MCPPermits(), "an empty allow-list blocks the allowed-by-default wildcard")
	})
}

// TestLoadVirtualMCPs_RoundTrip exercises the startup load against a real sqlite store (so the
// add_virtual_mcp_tables migration runs): every definition cached by id, every assignment cached per
// VK, ParsedTools decoded, and the enabled filter applied at resolution rather than at load.
func TestLoadVirtualMCPs_RoundTrip(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()

	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/vmcp.db"},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cs.Close(ctx)) })

	// A virtual key to hang the assignment off of.
	require.NoError(t, cs.DB().WithContext(ctx).Create(buildVirtualKey("vk-1", "sk-bf-vmcp", "VK", true)).Error)

	enabled := &configstoreTables.TableVirtualMCP{
		Name: "eng", EndpointSlug: "eng", Enabled: true,
		ParsedTools: []configstoreTables.MCPToolSpec{spec("cB", "read")},
	}
	require.NoError(t, cs.DB().WithContext(ctx).Create(enabled).Error)
	disabled := &configstoreTables.TableVirtualMCP{
		Name: "old", EndpointSlug: "old", Enabled: true,
		ParsedTools: []configstoreTables.MCPToolSpec{spec("cC")},
	}
	require.NoError(t, cs.DB().WithContext(ctx).Create(disabled).Error)
	// Enabled has a default:true, so a false zero-value on Create is ignored; force it off explicitly.
	require.NoError(t, cs.DB().WithContext(ctx).Model(disabled).Update("enabled", false).Error)
	for _, id := range []uint{enabled.ID, disabled.ID} {
		require.NoError(t, cs.DB().WithContext(ctx).Create(&configstoreTables.TableVirtualKeyVirtualMCP{
			VirtualMCPID: id, VirtualKeyID: "vk-1",
		}).Error)
	}

	h, err := openLiveHarness(t, cs, nil, nil)
	require.NoError(t, err)
	gs := h.mustGovernanceStore(logger, nil)

	ids := storedGrantSet(t, gs, "vk-1").VirtualMCPIDs
	require.Len(t, ids, 2, "both assignments load; enabled is filtered at resolution, not at load")

	// Resolution reads each assigned definition and skips the disabled one.
	var resolved []configstoreTables.TableVirtualMCP
	for _, id := range ids {
		if def := gs.virtualMCPByID(id); def != nil && def.Enabled {
			resolved = append(resolved, *def)
		}
	}
	require.Len(t, resolved, 1, "only the enabled virtual MCP resolves")
	assert.Equal(t, "eng", resolved[0].Name)
	require.Len(t, resolved[0].ParsedTools, 1, "ParsedTools must be decoded by AfterFind")
	assert.Equal(t, "cB", resolved[0].ParsedTools[0].MCPClientID)
	assert.Equal(t, []string{"read"}, resolved[0].ParsedTools[0].ToolNames)

	// The disabled definition is still cached, so enabling it later is one surgical Store.
	require.NotNil(t, gs.virtualMCPByID(disabled.ID))
	assert.False(t, gs.virtualMCPByID(disabled.ID).Enabled)
}

// TestVirtualMCPInMemoryCRUD covers how definition and assignment writes reach the live store: a
// definition edit ripples through the shared record, and an assignment change re-reads one key.
func TestVirtualMCPInMemoryCRUD(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{
			*buildVirtualKey("vk-1", "sk-bf-vmcp-1", "VK 1", true),
			*buildVirtualKey("vk-2", "sk-bf-vmcp-2", "VK 2", true),
		},
	}, nil)
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	assigned := func(vkID string) []uint { return storedGrantSet(t, gs, vkID).VirtualMCPIDs }

	// Create stores a copy: mutating the caller's struct (including its tool slices) afterwards does
	// not change the record.
	def := &configstoreTables.TableVirtualMCP{
		ID: 7, Name: "eng", Enabled: true,
		ParsedTools: []configstoreTables.MCPToolSpec{{MCPClientID: "c", ToolNames: []string{"read"}}},
	}
	h.putVirtualMCP(def)
	def.Name = "mutated"
	def.ParsedTools[0].MCPClientID = "mutated"
	def.ParsedTools[0].ToolNames[0] = "mutated"
	cached := gs.virtualMCPByID(7)
	require.NotNil(t, cached)
	assert.Equal(t, "eng", cached.Name, "the record holds a copy, not the caller's struct")
	require.Len(t, cached.ParsedTools, 1)
	assert.Equal(t, "c", cached.ParsedTools[0].MCPClientID, "the record's tool spec is independent of the caller")
	assert.Equal(t, []string{"read"}, cached.ParsedTools[0].ToolNames, "the record's tool names are its own")
	h.putVirtualMCP(&configstoreTables.TableVirtualMCP{ID: 3, Name: "three", Enabled: true})
	h.putVirtualMCP(&configstoreTables.TableVirtualMCP{ID: 9, Name: "nine", Enabled: true})

	// Attach records the id and is idempotent.
	h.attachVirtualMCP("vk-1", 7)
	h.attachVirtualMCP("vk-1", 7)
	assert.Equal(t, []uint{7}, assigned("vk-1"))

	// Attach keeps the id list sorted, so resolution order (and the permit) is deterministic.
	h.attachVirtualMCP("vk-1", 3)
	assert.Equal(t, []uint{3, 7}, assigned("vk-1"))
	h.detachVirtualMCP("vk-1", 3)

	// Update replaces the shared definition; every assigned VK sees the new one via resolution.
	h.putVirtualMCP(&configstoreTables.TableVirtualMCP{ID: 7, Name: "eng-2", Enabled: false})
	assert.Equal(t, "eng-2", gs.virtualMCPByID(7).Name)
	assert.False(t, gs.virtualMCPByID(7).Enabled)

	// Detach removes just that id; emptying the list leaves the key with none.
	h.attachVirtualMCP("vk-1", 9)
	h.detachVirtualMCP("vk-1", 7)
	assert.Equal(t, []uint{9}, assigned("vk-1"))
	h.detachVirtualMCP("vk-1", 9)
	assert.Empty(t, assigned("vk-1"))

	// A key record not yet refreshed after its definition went still names the id, which resolves to
	// nil, so it is simply skipped. The record alone is dropped here; deleting the row would also
	// delete the assignment and refresh the key.
	h.attachVirtualMCP("vk-2", 7)
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualMCP, ID: registry.VirtualMCPKey(7), Remove: true})
	assert.Nil(t, gs.virtualMCPByID(7))
	assert.Equal(t, []uint{7}, assigned("vk-2"), "stale id remains; resolution skips it via the nil lookup")

	// Deleting the definition deletes its assignments and refreshes the keys that held it.
	h.putVirtualMCP(&configstoreTables.TableVirtualMCP{ID: 7, Name: "eng-3", Enabled: true})
	h.deleteVirtualMCP(7)
	assert.Nil(t, gs.virtualMCPByID(7))
	assert.Empty(t, assigned("vk-2"), "the key is re-read without the deleted definition")
}

// TestSetVirtualMCPAssignmentsInMemory pins that a key's assignments can be replaced in one write: the
// new set lands sorted and independent of the caller's slice, an empty set leaves the key with none,
// and other keys are untouched.
func TestSetVirtualMCPAssignmentsInMemory(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{
			*buildVirtualKey("vk-1", "sk-bf-vmcp-1", "VK 1", true),
			*buildVirtualKey("vk-2", "sk-bf-vmcp-2", "VK 2", true),
		},
	}, nil)
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	assigned := func(vkID string) []uint { return storedGrantSet(t, gs, vkID).VirtualMCPIDs }
	for _, id := range []uint{1, 2, 3, 9} {
		h.putVirtualMCP(&configstoreTables.TableVirtualMCP{ID: id, Name: fmt.Sprintf("vmcp-%d", id), Enabled: true})
	}
	h.attachVirtualMCP("vk-1", 1)
	h.attachVirtualMCP("vk-1", 2)
	h.attachVirtualMCP("vk-2", 2)

	ids := []uint{9, 3}
	h.setVirtualMCPAssignments("vk-1", ids...)
	ids[0] = 42
	assert.Equal(t, []uint{3, 9}, assigned("vk-1"), "replaced, sorted, and copied from the caller")
	assert.Equal(t, []uint{2}, assigned("vk-2"), "other keys are untouched")

	h.setVirtualMCPAssignments("vk-1")
	assert.Empty(t, assigned("vk-1"), "an empty set leaves the key with none")
}

// TestPostMCPHook_RecordsAccountedLimitIDs verifies a tool call leaves behind the
// budgets and rate limits it was billed against, the way PostLLMHook does for
// inference. The logging plugin reads these keys when it completes the tool log,
// so without them an MCP row cannot say what funded it.
func TestPostMCPHook_RecordsAccountedLimitIDs(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("vk-rl", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, store.live)
	require.NoError(t, err)
	defer plugin.Cleanup()

	ctx := resolverCtx(store, "sk-bf-test")
	settled, settleErr := resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
	require.NoError(t, settleErr)
	require.NotNil(t, settled)

	_, _, err = plugin.PostMCPHook(ctx, &schemas.BifrostMCPResponse{
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeExecuteTool,
			ClientName:     "client",
			ToolName:       "tool",
		},
	}, nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	rateLimitIDs, _ := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
	assert.Contains(t, rateLimitIDs, "vk-rl")
}

// A prompt or resource read is accounted against the caller's limits like a tool call.
func TestPostMCPHook_AccountsPromptAndResourceReads(t *testing.T) {
	for _, requestType := range []schemas.MCPRequestType{schemas.MCPRequestTypeGetPrompt, schemas.MCPRequestTypeReadResource} {
		t.Run(string(requestType), func(t *testing.T) {
			logger := NewMockLogger()
			rateLimit := buildRateLimitWithUsage("vk-rl", 10000, 0, 1000, 0)
			vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
			store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
				RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
			}, nil, nil)
			require.NoError(t, err)
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, store.live)
			require.NoError(t, err)
			defer plugin.Cleanup()

			ctx := resolverCtx(store, "sk-bf-test")
			_, settleErr := resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
			require.NoError(t, settleErr)
			_, _, err = plugin.PostMCPHook(ctx, &schemas.BifrostMCPResponse{
				ExtraFields: schemas.BifrostMCPResponseExtraFields{MCPRequestType: requestType, ClientName: "client"},
			}, nil)
			require.NoError(t, err)
			plugin.wg.Wait()

			rateLimitIDs, _ := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
			assert.Contains(t, rateLimitIDs, "vk-rl")
		})
	}
}

// scopedBudgetStore builds a store whose virtual key carries one budget of each scope on its
// all-models model config, which is where a key's own budgets live. usage sets each budget's
// current spend against a limit of 10.
func scopedBudgetStore(t *testing.T, usage map[configstoreTables.LimitScope]float64) (*LocalGovernanceStore, *GovernancePlugin) {
	t.Helper()
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	mc := buildVKScopedModelConfig("mc-vk", "*", nil, vk.ID, nil, nil)
	var budgets []configstoreTables.TableBudget
	for _, scope := range []configstoreTables.LimitScope{configstoreTables.LimitScopeShared, configstoreTables.LimitScopeModel, configstoreTables.LimitScopeMCP} {
		b := buildBudgetWithUsage("budget-"+string(scope), 10, usage[scope], "1d")
		b.Scope = scope
		b.ModelConfigID = &mc.ID
		budgets = append(budgets, *b)
	}
	mc.Budgets = budgets
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      budgets,
	}, nil, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, store.live)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })
	return store, plugin
}

func settledBudgetIDs(t *testing.T, limits schemas.Limits) []string {
	t.Helper()
	require.NotNil(t, limits)
	ids := make([]string, 0)
	for _, limit := range limits.Budgets() {
		ids = append(ids, limit.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestResolveLimits_AppliesBudgetScope pins that the limits settled for an attempt follow budget
// scope: an MCP tool execution answers to "shared" and "mcp" budgets, a model request to "shared" and
// "model" ones. Settling a tool call between two model requests on one grant, as the agent loop
// does, leaves the next model request with the model set again.
func TestResolveLimits_AppliesBudgetScope(t *testing.T) {
	store, _ := scopedBudgetStore(t, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	model, err := resolveLimits(ctx, store, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, []string{"budget-model", "budget-shared"}, settledBudgetIDs(t, model))

	mcp, err := resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"budget-mcp", "budget-shared"}, settledBudgetIDs(t, mcp))
	assert.Equal(t, []string{"budget-mcp", "budget-shared"}, settledBudgetIDs(t, ctx.Grant().Limits()))

	again, err := resolveLimits(ctx, store, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, []string{"budget-model", "budget-shared"}, settledBudgetIDs(t, again))
}

// holderScopedBudgetStore builds a store whose virtual key answers to one holder carrying one budget
// of each scope, each with a limit of 10 and the given current spend: the key itself ("virtual_key",
// on its all-models model config, where a key's own budgets live), or the team or customer it belongs
// to ("team", "customer"). The key allows every OpenAI model and has no other budgets.
func holderScopedBudgetStore(t *testing.T, holder string, usage map[configstoreTables.LimitScope]float64) (*LocalGovernanceStore, *GovernancePlugin) {
	t.Helper()
	logger := NewMockLogger()
	var budgets []configstoreTables.TableBudget
	for _, scope := range []configstoreTables.LimitScope{configstoreTables.LimitScopeShared, configstoreTables.LimitScopeModel, configstoreTables.LimitScopeMCP} {
		b := buildBudgetWithUsage(holder+"-budget-"+string(scope), 10, usage[scope], "1d")
		b.Scope = scope
		budgets = append(budgets, *b)
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai", []string{"*"})})
	gov := &configstore.GovernanceConfig{Budgets: budgets}
	switch holder {
	case "virtual_key":
		mc := buildVKScopedModelConfig("mc-vk", "*", nil, vk.ID, nil, nil)
		for i := range budgets {
			budgets[i].ModelConfigID = &mc.ID
		}
		mc.Budgets = budgets
		gov.ModelConfigs = []configstoreTables.TableModelConfig{*mc}
	case "team":
		team := buildTeam("team1", "Team 1", nil)
		for i := range budgets {
			budgets[i].TeamID = &team.ID
		}
		team.Budgets = budgets
		vk.TeamID = &team.ID
		vk.Team = team
		gov.Teams = []configstoreTables.TableTeam{*team}
	case "customer":
		customer := buildCustomer("customer1", "Customer 1", nil)
		for i := range budgets {
			budgets[i].CustomerID = &customer.ID
		}
		customer.Budgets = budgets
		vk.CustomerID = &customer.ID
		vk.Customer = customer
		gov.Customers = []configstoreTables.TableCustomer{*customer}
	default:
		t.Fatalf("unknown holder %q", holder)
	}
	gov.VirtualKeys = []configstoreTables.TableVirtualKey{*vk}
	store, err := newTestGovernanceStore(t, logger, gov, nil, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, store.live)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })
	return store, plugin
}

// TestBudgetScope_AppliesToEveryHolder pins that budget scope is honoured on every holder a key
// answers to, its own budgets and those it inherits from its team or customer: a tool execution is
// checked and charged against the holder's shared and "mcp" budgets and never its "model" one, and a
// model request against its shared and "model" ones.
func TestBudgetScope_AppliesToEveryHolder(t *testing.T) {
	for _, holder := range []string{"virtual_key", "team", "customer"} {
		budgetID := func(scope configstoreTables.LimitScope) string { return holder + "-budget-" + string(scope) }
		sharedBudget, modelBudget, mcpBudget := budgetID(configstoreTables.LimitScopeShared), budgetID(configstoreTables.LimitScopeModel), budgetID(configstoreTables.LimitScopeMCP)

		t.Run(holder+" settles by request kind", func(t *testing.T) {
			store, _ := holderScopedBudgetStore(t, holder, nil)
			ctx := resolverCtx(store, "sk-bf-test")
			mcp, err := resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
			require.NoError(t, err)
			assert.Equal(t, []string{mcpBudget, sharedBudget}, settledBudgetIDs(t, mcp))
			model, err := resolveLimits(ctx, store, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
			require.NoError(t, err)
			assert.Equal(t, []string{modelBudget, sharedBudget}, settledBudgetIDs(t, model))
		})

		t.Run(holder+" tool execution is charged to the shared and mcp budgets only", func(t *testing.T) {
			store, plugin := holderScopedBudgetStore(t, holder, nil)
			// A priced tool, so the charge itself is visible and not only the budgets it reaches.
			require.NoError(t, harnessDB(t, store).Create(&configstoreTables.TableMCPClient{
				ClientID: "client", Name: "client", ConnectionType: string(schemas.MCPConnectionTypeHTTP),
				ToolPricing: map[string]float64{"tool": 0.25},
			}).Error)
			require.NoError(t, store.live.Refresh(context.Background(), []livestore.Ref{{Kind: livestore.KindMCPClient, ID: "client"}}))
			catalog, err := mcpcatalog.Init(context.Background(), store.live, NewMockLogger())
			require.NoError(t, err)
			plugin.mcpCatalog = catalog
			ctx := resolverCtx(store, "sk-bf-test")
			_, err = resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
			require.NoError(t, err)
			_, _, err = plugin.PostMCPHook(ctx, &schemas.BifrostMCPResponse{
				ExtraFields: schemas.BifrostMCPResponseExtraFields{MCPRequestType: schemas.MCPRequestTypeExecuteTool, ClientName: "client", ToolName: "tool"},
			}, nil)
			require.NoError(t, err)
			plugin.wg.Wait()
			budgetIDs, _ := ctx.Value(schemas.BifrostContextKeyGovernanceBudgetIDs).([]string)
			slices.Sort(budgetIDs)
			assert.Equal(t, []string{mcpBudget, sharedBudget}, budgetIDs)
			usageOf := func(id string) float64 {
				budget := store.LoadBudget(context.Background(), id)
				require.NotNil(t, budget, id)
				return budget.CurrentUsage
			}
			assert.InDelta(t, 0.25, usageOf(mcpBudget), 1e-9)
			assert.InDelta(t, 0.25, usageOf(sharedBudget), 1e-9)
			assert.Zero(t, usageOf(modelBudget))
		})

		checks := []struct {
			name        string
			exhausted   configstoreTables.LimitScope
			requestType schemas.RequestType
			allowed     bool
		}{
			{"exhausted model budget does not stop a tool execution", configstoreTables.LimitScopeModel, schemas.MCPToolExecutionRequest, true},
			{"exhausted mcp budget stops a tool execution", configstoreTables.LimitScopeMCP, schemas.MCPToolExecutionRequest, false},
			{"exhausted shared budget stops a tool execution", configstoreTables.LimitScopeShared, schemas.MCPToolExecutionRequest, false},
			{"exhausted mcp budget does not stop a model request", configstoreTables.LimitScopeMCP, schemas.ChatCompletionRequest, true},
			{"exhausted model budget stops a model request", configstoreTables.LimitScopeModel, schemas.ChatCompletionRequest, false},
			{"exhausted shared budget stops a model request", configstoreTables.LimitScopeShared, schemas.ChatCompletionRequest, false},
		}
		for _, tc := range checks {
			t.Run(holder+" "+tc.name, func(t *testing.T) {
				store, plugin := holderScopedBudgetStore(t, holder, map[configstoreTables.LimitScope]float64{tc.exhausted: 10})
				ctx := resolverCtx(store, "sk-bf-test")
				request := &EvaluationRequest{RequestType: tc.requestType}
				if tc.requestType != schemas.MCPToolExecutionRequest {
					request.Provider = schemas.OpenAI
					request.Model = "gpt-4o"
				}
				result, bifrostErr := plugin.Evaluate(ctx, request)
				if tc.allowed {
					require.Nil(t, bifrostErr, "result=%+v", result)
					return
				}
				require.NotNil(t, bifrostErr)
				assert.Equal(t, DecisionBudgetExceeded, result.Decision)
			})
		}
	}
}

// scopedBudgets builds one budget of each scope, named prefix-<scope>, owned by the model config ownerID
// (or by nothing when ownerID is empty).
func scopedBudgets(prefix, ownerID string, scopes ...configstoreTables.LimitScope) []configstoreTables.TableBudget {
	budgets := make([]configstoreTables.TableBudget, 0, len(scopes))
	for _, scope := range scopes {
		b := buildBudget(prefix+"-"+string(scope), 10, "1d")
		b.Scope = scope
		if ownerID != "" {
			b.ModelConfigID = &ownerID
		}
		budgets = append(budgets, *b)
	}
	return budgets
}

// holderLevelStore builds a store with every kind of holder an MCP tool execution can answer to:
// the deployment's and a virtual key's holder-level model configs (model "*", no provider), each with
// one budget of every scope and a rate limit, and the team and customer above the key, each with a
// model and an mcp budget. Beside them sit narrower model configs (a model, and all models on one
// provider) carrying shared budgets, which model traffic answers to and MCP traffic never reaches.
func holderLevelStore(tb testing.TB) *LocalGovernanceStore {
	tb.Helper()
	all := []configstoreTables.LimitScope{configstoreTables.LimitScopeShared, configstoreTables.LimitScopeModel, configstoreTables.LimitScopeMCP}
	split := []configstoreTables.LimitScope{configstoreTables.LimitScopeModel, configstoreTables.LimitScopeMCP}
	openai := "openai"

	globalRL := buildRateLimit("global-rl", 1_000_000, 1000)
	globalMC := buildModelConfig("mc-global", "*", nil, nil, globalRL)
	globalMC.Budgets = scopedBudgets("global", globalMC.ID, all...)
	globalModelMC := buildModelConfig("mc-global-gpt-4o", "gpt-4o", nil, nil, nil)
	globalModelMC.Budgets = scopedBudgets("global-gpt-4o", globalModelMC.ID, configstoreTables.LimitScopeShared)

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai", []string{"*"})})
	vkRL := buildRateLimit("vk-rl", 1_000_000, 1000)
	vkMC := buildVKScopedModelConfig("mc-vk", "*", nil, vk.ID, nil, vkRL)
	vkMC.Budgets = scopedBudgets("vk", vkMC.ID, all...)
	vkProviderMC := buildVKScopedModelConfig("mc-vk-openai", "*", &openai, vk.ID, nil, nil)
	vkProviderMC.Budgets = scopedBudgets("vk-openai", vkProviderMC.ID, configstoreTables.LimitScopeShared)

	customer := buildCustomer("customer1", "Customer 1", nil)
	customer.Budgets = scopedBudgets("customer", "", split...)
	for i := range customer.Budgets {
		customer.Budgets[i].CustomerID = &customer.ID
	}
	team := buildTeam("team1", "Team 1", nil)
	team.CustomerID = &customer.ID
	team.Budgets = scopedBudgets("team", "", split...)
	for i := range team.Budgets {
		team.Budgets[i].TeamID = &team.ID
	}
	vk.TeamID = &team.ID
	vk.Team = team

	var budgets []configstoreTables.TableBudget
	for _, set := range [][]configstoreTables.TableBudget{globalMC.Budgets, globalModelMC.Budgets, vkMC.Budgets, vkProviderMC.Budgets, team.Budgets, customer.Budgets} {
		budgets = append(budgets, set...)
	}
	store, err := newTestGovernanceStore(tb, NewMockLogger(), &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		Teams:        []configstoreTables.TableTeam{*team},
		Customers:    []configstoreTables.TableCustomer{*customer},
		ModelConfigs: []configstoreTables.TableModelConfig{*globalMC, *globalModelMC, *vkMC, *vkProviderMC},
		Budgets:      budgets,
		RateLimits:   []configstoreTables.TableRateLimit{*globalRL, *vkRL},
	}, nil, nil)
	require.NoError(tb, err)
	return store
}

func settledRateLimitIDs(t *testing.T, limits schemas.Limits) []string {
	t.Helper()
	require.NotNil(t, limits)
	ids := make([]string, 0)
	for _, limit := range limits.RateLimits() {
		ids = append(ids, limit.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestResolveLimits_MCPAnswersToHolderLevelLimits pins what an MCP tool execution answers to: the
// deployment's and the key's holder-level model configs, read directly, and the key's team and
// customer, each charged by its shared and mcp lines and never its model ones. The narrower model
// configs are model traffic's alone, and a model request still answers to every tier it did before.
func TestResolveLimits_MCPAnswersToHolderLevelLimits(t *testing.T) {
	store := holderLevelStore(t)
	ctx := resolverCtx(store, "sk-bf-test")

	mcp, err := resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"customer-mcp", "global-mcp", "global-shared", "team-mcp", "vk-mcp", "vk-shared"}, settledBudgetIDs(t, mcp))
	assert.Equal(t, []string{"global-rl", "vk-rl"}, settledRateLimitIDs(t, mcp))

	model, err := resolveLimits(ctx, store, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, []string{"customer-model", "global-gpt-4o-shared", "global-model", "global-shared", "team-model", "vk-model", "vk-openai-shared", "vk-shared"}, settledBudgetIDs(t, model))
	assert.Equal(t, []string{"global-rl", "vk-rl"}, settledRateLimitIDs(t, model))
}

// TestResolveLimits_MCPKeepsTheDeploymentLimitsWithoutAnAccessGrant: a call that carries no virtual key
// and so no access grant still answers to the deployment's holder-level limits, and an exhausted
// global MCP budget refuses it.
func TestResolveLimits_MCPKeepsTheDeploymentLimitsWithoutAnAccessGrant(t *testing.T) {
	globalMC := buildModelConfig("mc-global", "*", nil, nil, nil)
	spent := buildBudgetWithUsage("global-mcp", 10, 10, "1d")
	spent.Scope = configstoreTables.LimitScopeMCP
	spent.ModelConfigID = &globalMC.ID
	globalMC.Budgets = []configstoreTables.TableBudget{*spent}
	store, err := newTestGovernanceStore(t, NewMockLogger(), &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*globalMC},
		Budgets:      []configstoreTables.TableBudget{*spent},
	}, nil, nil)
	require.NoError(t, err)
	ctx := resolverCtx(store, "")
	require.Nil(t, ctx.Grant().Access(), "the call has no access grant")

	mcp, err := resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"global-mcp"}, settledBudgetIDs(t, mcp))

	decision, _ := store.CheckBudgets(context.Background(), mcp.Budgets(), nil)
	assert.True(t, isBudgetViolation(decision), "the exhausted global MCP budget refuses the call")
}

// kindRecordingStore records the traffic scope resolveLimits tells its store.
type kindRecordingStore struct {
	*LocalGovernanceStore
	sawTraffic []configstoreTables.LimitScope
}

func (s *kindRecordingStore) GatherLimitsForRequestKind(ctx *schemas.BifrostContext, access schemas.Access, provider schemas.ModelProvider, model string, traffic configstoreTables.LimitScope) ([]schemas.Limit, []schemas.Limit, error) {
	s.sawTraffic = append(s.sawTraffic, traffic)
	return s.LocalGovernanceStore.GatherLimitsForRequestKind(ctx, access, provider, model, traffic)
}

// TestResolveLimits_TellsKindAwareStoresTheRequestKind pins the hook: the store is asked with the
// attempt's kind, and what it returns is still filtered
// by scope, so a store that ignores the kind cannot leak an MCP-only budget onto model traffic.
func TestResolveLimits_TellsKindAwareStoresTheRequestKind(t *testing.T) {
	local, _ := scopedBudgetStore(t, nil)
	store := &kindRecordingStore{LocalGovernanceStore: local}
	ctx := resolverCtx(local, "sk-bf-test")

	model, err := resolveLimits(ctx, store, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
	require.NoError(t, err)
	mcp, err := resolveLimits(ctx, store, schemas.MCPToolExecutionRequest, "", "")
	require.NoError(t, err)

	assert.Equal(t, []configstoreTables.LimitScope{configstoreTables.LimitScopeModel, configstoreTables.LimitScopeMCP}, store.sawTraffic)
	assert.Equal(t, []string{"budget-model", "budget-shared"}, settledBudgetIDs(t, model))
	assert.Equal(t, []string{"budget-mcp", "budget-shared"}, settledBudgetIDs(t, mcp))
}

// TestBudgetsForRequestKind_NoAllocationWhenEveryBudgetApplies pins the common-case cost: when no
// budget is dropped the gathered slice is returned as is.
func TestBudgetsForRequestKind_NoAllocationWhenEveryBudgetApplies(t *testing.T) {
	store, _ := scopedBudgetStore(t, nil)
	ctx := context.Background()
	budgets := []schemas.Limit{{ID: "budget-shared"}, {ID: "budget-model"}, {ID: "unknown"}}
	allocs := testing.AllocsPerRun(100, func() {
		_ = BudgetsForRequestKind(ctx, store, budgets, configstoreTables.LimitScopeModel)
	})
	assert.Zero(t, allocs)
	kept := BudgetsForRequestKind(ctx, store, budgets, configstoreTables.LimitScopeMCP)
	assert.Equal(t, []schemas.Limit{{ID: "budget-shared"}, {ID: "unknown"}}, kept)
	assert.Equal(t, "budget-model", budgets[1].ID, "the input slice is never filtered in place")
}

func BenchmarkBudgetsForRequestKind(b *testing.B) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	mc := buildVKScopedModelConfig("mc-vk", "*", nil, vk.ID, nil, nil)
	budgets := []configstoreTables.TableBudget{*buildBudget("b-day", 10, "1d"), *buildBudget("b-month", 100, "1M")}
	for i := range budgets {
		budgets[i].ModelConfigID = &mc.ID
	}
	mc.Budgets = budgets
	store, err := newTestGovernanceStore(b, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk}, ModelConfigs: []configstoreTables.TableModelConfig{*mc}, Budgets: budgets,
	}, nil, nil)
	require.NoError(b, err)
	ctx := resolverCtx(store, "sk-bf-test")
	b.Run("resolveLimits", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = resolveLimits(ctx, store, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
		}
	})
	// The whole hierarchy an MCP tool execution answers to: the deployment's and the key's
	// holder-level lines, the key's team and customer, with narrower model configs beside them.
	hierarchy := holderLevelStore(b)
	hierarchyCtx := resolverCtx(hierarchy, "sk-bf-test")
	b.Run("resolveLimits/hierarchy/model", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = resolveLimits(hierarchyCtx, hierarchy, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o")
		}
	})
	b.Run("resolveLimits/hierarchy/mcp", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = resolveLimits(hierarchyCtx, hierarchy, schemas.MCPToolExecutionRequest, "", "")
		}
	})
	limits := []schemas.Limit{{ID: "b-day"}, {ID: "b-month"}}
	b.Run("filter", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = BudgetsForRequestKind(ctx, store, limits, configstoreTables.LimitScopeModel)
		}
	})
}
