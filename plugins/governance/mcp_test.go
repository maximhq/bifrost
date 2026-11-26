package governance

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
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
	settled, settleErr := resolveLimits(ctx, store, "", "")
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
			_, settleErr := resolveLimits(ctx, store, "", "")
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
