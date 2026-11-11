package governance

import (
	"context"
	"math/rand"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
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

func vmcp(name string, enabled bool, specs ...configstoreTables.MCPToolSpec) configstoreTables.TableVirtualMCP {
	return configstoreTables.TableVirtualMCP{Name: name, Enabled: enabled, ParsedTools: specs}
}

func spec(clientID string, tools ...string) configstoreTables.MCPToolSpec {
	return configstoreTables.MCPToolSpec{MCPClientID: clientID, ToolNames: tools}
}

// permitFor resolves vk's permit with the given assigned Virtual MCPs cached, the given client-name
// resolution, and the given allowed-by-default clients.
func permitFor(vk *configstoreTables.TableVirtualKey, assigned []configstoreTables.TableVirtualMCP, names, allowedByDefault map[string]string) *grant.Permit {
	gs := &LocalGovernanceStore{
		inMemoryStore: &mockInMemoryStore{clientNames: names, allowedByDefaultClients: allowedByDefault},
		logger:        NewMockLogger(),
	}
	if len(assigned) > 0 {
		ids := make([]uint, len(assigned))
		for i := range assigned {
			def := assigned[i]
			def.ID = uint(i + 1) // distinct ids; the definitions cache is keyed by id
			gs.virtualMCPs.Store(def.ID, &def)
			ids[i] = def.ID
		}
		gs.virtualMCPIDsByVK.Store(vk.ID, ids)
	}
	return gs.permitForVirtualKey(emptyCtx(), vk)
}

// TestPermitForVirtualKey_VirtualMCPsMirrorConfigs pins that an assigned Virtual MCP grants exactly
// like the key's own MCP config: it owns the clients it names, unions per client, and blocks the
// allowed-by-default fallback for them.
func TestPermitForVirtualKey_VirtualMCPsMirrorConfigs(t *testing.T) {
	names := map[string]string{"cA": "alpha", "cB": "beta"}

	t.Run("a virtual MCP adds a client the key did not configure", func(t *testing.T) {
		vk := buildVKWithMCPConfigs("cA", "alpha", []string{"x"}) // vk.ID == "vk-1"
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB", "read"))}, names, nil)
		assert.Equal(t, map[string][]string{"cA": {"x"}, "cB": {"read"}}, toolsByClient(permit.MCPPermits()),
			"the direct config must survive and the virtual MCP must be added")
	})

	t.Run("a virtual MCP on the key's own client unions the tools", func(t *testing.T) {
		vk := buildVKWithMCPConfigs("cA", "alpha", []string{"x"})
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cA", "y"))}, names, nil)
		assert.Equal(t, map[string][]string{"cA": {"x", "y"}}, toolsByClient(permit.MCPPermits()))
	})

	t.Run("a wildcard tool name grants the whole client", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB", grant.Wildcard))}, names, nil)
		assert.Equal(t, map[string][]string{"cB": {grant.Wildcard}}, toolsByClient(permit.MCPPermits()))
	})

	t.Run("empty tool names grant nothing (deny-by-default)", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB"))}, names, nil)
		assert.Empty(t, permit.MCPPermits())
	})

	t.Run("a disabled virtual MCP contributes nothing", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", false, spec("cB", "read"))}, names, nil)
		assert.Empty(t, permit.MCPPermits())
	})

	t.Run("a virtual MCP client with no configured name is dropped", func(t *testing.T) {
		vk := buildVKNoMCPConfigs()
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cX", "read"))}, names, nil)
		assert.Empty(t, permit.MCPPermits())
	})

	t.Run("like a config, a virtual MCP client blocks the allowed-by-default wildcard", func(t *testing.T) {
		// cB is open to every key by default, but the virtual MCP names only "read", so the key gets
		// exactly that, not the wildcard — the same way an explicit config scopes an open client.
		vk := buildVKNoMCPConfigs()
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB", "read"))}, names, map[string]string{"cB": "beta"})
		assert.Equal(t, map[string][]string{"cB": {"read"}}, toolsByClient(permit.MCPPermits()),
			"an assigned virtual MCP scopes an otherwise-open client, mirroring an explicit config")
	})

	t.Run("an empty tool list denies even an allowed-by-default client", func(t *testing.T) {
		// cB is open to every key by default. An empty-tool spec must still scope it to nothing, not
		// let the allowed-by-default fallback reopen it — the spec has to register cB as configured.
		vk := buildVKNoMCPConfigs()
		permit := permitFor(vk, []configstoreTables.TableVirtualMCP{vmcp("g", true, spec("cB"))}, names, map[string]string{"cB": "beta"})
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

	gs := &LocalGovernanceStore{configStore: cs, logger: logger}
	require.NoError(t, gs.loadVirtualMCPs(ctx))

	ids := gs.assignedVirtualMCPIDs("vk-1")
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

// TestVirtualMCPInMemoryCRUD covers the surgical cache updates that replace a full reload on writes:
// a definition edit ripples through the shared cache, and attach/detach touch one VK's id slice.
func TestVirtualMCPInMemoryCRUD(t *testing.T) {
	gs := &LocalGovernanceStore{logger: NewMockLogger()}

	// Create caches a copy: mutating the caller's struct (including its tool slices) afterwards does
	// not change the cache.
	def := &configstoreTables.TableVirtualMCP{
		ID: 7, Name: "eng", Enabled: true,
		ParsedTools: []configstoreTables.MCPToolSpec{{MCPClientID: "c", ToolNames: []string{"read"}}},
	}
	gs.CreateVirtualMCPInMemory(def)
	def.Name = "mutated"
	def.ParsedTools[0].MCPClientID = "mutated"
	def.ParsedTools[0].ToolNames[0] = "mutated"
	cached := gs.virtualMCPByID(7)
	require.NotNil(t, cached)
	assert.Equal(t, "eng", cached.Name, "the cache holds a copy, not the caller's struct")
	require.Len(t, cached.ParsedTools, 1)
	assert.Equal(t, "c", cached.ParsedTools[0].MCPClientID, "cached tool spec is independent of the caller")
	assert.Equal(t, []string{"read"}, cached.ParsedTools[0].ToolNames, "cached tool names are deep-copied")

	// Attach records the id and is idempotent.
	gs.AttachVirtualMCPInMemory("vk-1", 7)
	gs.AttachVirtualMCPInMemory("vk-1", 7)
	assert.Equal(t, []uint{7}, gs.assignedVirtualMCPIDs("vk-1"))

	// Attach keeps the id list sorted, so resolution order (and the permit) is deterministic.
	gs.AttachVirtualMCPInMemory("vk-1", 3)
	assert.Equal(t, []uint{3, 7}, gs.assignedVirtualMCPIDs("vk-1"))
	gs.DetachVirtualMCPInMemory("vk-1", 3)

	// Update replaces the shared definition; every assigned VK sees the new one via resolution.
	gs.UpdateVirtualMCPInMemory(&configstoreTables.TableVirtualMCP{ID: 7, Name: "eng-2", Enabled: false})
	assert.Equal(t, "eng-2", gs.virtualMCPByID(7).Name)
	assert.False(t, gs.virtualMCPByID(7).Enabled)

	// Detach removes just that id; emptying the slice drops the VK key entirely.
	gs.AttachVirtualMCPInMemory("vk-1", 9)
	gs.DetachVirtualMCPInMemory("vk-1", 7)
	assert.Equal(t, []uint{9}, gs.assignedVirtualMCPIDs("vk-1"))
	gs.DetachVirtualMCPInMemory("vk-1", 9)
	assert.Nil(t, gs.assignedVirtualMCPIDs("vk-1"))

	// Delete drops the definition; a lingering assignment id resolves to nil, so it is simply skipped.
	gs.AttachVirtualMCPInMemory("vk-2", 7)
	gs.DeleteVirtualMCPInMemory(7)
	assert.Nil(t, gs.virtualMCPByID(7))
	assert.Equal(t, []uint{7}, gs.assignedVirtualMCPIDs("vk-2"), "stale id remains; resolution skips it via the nil lookup")
}

// TestSetVirtualMCPAssignmentsInMemory pins that a key's assignments can be replaced in one write, the
// way a key reload converges the index on what the database holds: the new set lands sorted and
// independent of the caller's slice, an empty set drops the key, and other keys are untouched.
func TestSetVirtualMCPAssignmentsInMemory(t *testing.T) {
	gs := &LocalGovernanceStore{logger: NewMockLogger()}
	gs.AttachVirtualMCPInMemory("vk-1", 1)
	gs.AttachVirtualMCPInMemory("vk-1", 2)
	gs.AttachVirtualMCPInMemory("vk-2", 2)

	ids := []uint{9, 3}
	gs.SetVirtualMCPAssignmentsInMemory("vk-1", ids)
	ids[0] = 42
	assert.Equal(t, []uint{3, 9}, gs.assignedVirtualMCPIDs("vk-1"), "replaced, sorted, and copied from the caller")
	got := gs.VirtualMCPAssignmentsInMemory("vk-1")
	assert.Equal(t, []uint{3, 9}, got)
	got[0] = 99
	assert.Equal(t, []uint{3, 9}, gs.assignedVirtualMCPIDs("vk-1"), "the reader hands out a copy")
	assert.Equal(t, []uint{2}, gs.assignedVirtualMCPIDs("vk-2"), "other keys are untouched")

	gs.SetVirtualMCPAssignmentsInMemory("vk-1", nil)
	assert.Nil(t, gs.assignedVirtualMCPIDs("vk-1"), "an empty set drops the key")
	_, present := gs.virtualMCPIDsByVK.Load("vk-1")
	assert.False(t, present)
}

// TestSetVirtualMCPAssignmentsMatchesAttachDetachConvergence pins that replacing a key's set in one
// write leaves the index exactly where the enterprise key reload used to leave it: walking every
// Virtual MCP that exists, attaching the ones the database assigns the key and detaching the rest.
// Randomized over the starting index, the database's assignments and the other keys present.
func TestSetVirtualMCPAssignmentsMatchesAttachDetachConvergence(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	all := []uint{1, 2, 3, 4, 5, 6, 7, 8}
	subset := func() []uint {
		var out []uint
		for _, id := range all {
			if rng.Intn(2) == 0 {
				out = append(out, id)
			}
		}
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}
	keys := []string{"vk-1", "vk-2", "vk-3"}
	for iter := 0; iter < 2000; iter++ {
		before := map[string][]uint{}
		for _, k := range keys {
			before[k] = subset()
		}
		assigned := subset()
		if rng.Intn(5) == 0 {
			assigned = nil // the database assigns nothing (or the key is profile-managed)
		}

		old := &LocalGovernanceStore{logger: NewMockLogger()}
		replaced := &LocalGovernanceStore{logger: NewMockLogger()}
		for _, k := range keys {
			for _, id := range before[k] {
				old.AttachVirtualMCPInMemory(k, id)
				replaced.AttachVirtualMCPInMemory(k, id)
			}
		}

		// The previous convergence, verbatim.
		for _, id := range all {
			if slices.Contains(assigned, id) {
				old.AttachVirtualMCPInMemory("vk-1", id)
			} else {
				old.DetachVirtualMCPInMemory("vk-1", id)
			}
		}
		replaced.SetVirtualMCPAssignmentsInMemory("vk-1", assigned)

		for _, k := range keys {
			require.Equal(t, old.assignedVirtualMCPIDs(k), replaced.assignedVirtualMCPIDs(k),
				"iteration %d key %s: before %v, database %v", iter, k, before[k], assigned)
			_, oldPresent := old.virtualMCPIDsByVK.Load(k)
			_, newPresent := replaced.virtualMCPIDsByVK.Load(k)
			require.Equal(t, oldPresent, newPresent, "iteration %d key %s: an emptied key is dropped either way", iter, k)
		}
	}
}

// TestPostMCPHook_RecordsAccountedLimitIDs verifies a tool call leaves behind the
// budgets and rate limits it was billed against, the way PostLLMHook does for
// inference. The logging plugin reads these keys when it completes the tool log,
// so without them an MCP row cannot say what funded it.
func TestPostMCPHook_RecordsAccountedLimitIDs(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("vk-rl", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
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
			store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
				RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
			}, nil, nil)
			require.NoError(t, err)
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      budgets,
	}, nil, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, gov, nil, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
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
			catalog, err := mcpcatalog.Init(context.Background(), &mcpcatalog.Config{PricingData: mcpcatalog.MCPPricingData{
				"client/tool": {Server: "client", ToolName: "tool", CostPerExecution: 0.25},
			}}, NewMockLogger())
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
				raw, ok := store.budgets.Load(id)
				require.True(t, ok, id)
				return raw.(*configstoreTables.TableBudget).CurrentUsage
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
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{
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

// kindRecordingStore records the request kind resolveLimits tells its store.
type kindRecordingStore struct {
	*LocalGovernanceStore
	sawMCP []bool
}

func (s *kindRecordingStore) GatherLimitsForRequestKind(ctx *schemas.BifrostContext, access schemas.Access, provider schemas.ModelProvider, model string, isMCP bool) ([]schemas.Limit, []schemas.Limit, error) {
	s.sawMCP = append(s.sawMCP, isMCP)
	return s.LocalGovernanceStore.GatherLimitsForRequestKind(ctx, access, provider, model, isMCP)
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

	assert.Equal(t, []bool{false, true}, store.sawMCP)
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
		_ = BudgetsForRequestKind(ctx, store, budgets, false)
	})
	assert.Zero(t, allocs)
	kept := BudgetsForRequestKind(ctx, store, budgets, true)
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
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
			_ = BudgetsForRequestKind(ctx, store, limits, false)
		}
	})
}
