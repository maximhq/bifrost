package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
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
