package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestValidateToolSpecs_NamesAClientByID pins that a Virtual MCP tool spec must name its client by
// client_id. A client's name passed validation, but grants are resolved by id, so a Virtual MCP that
// named alpha by name was saved, granted nothing, and its endpoint listed nothing.
func TestValidateToolSpecs_NamesAClientByID(t *testing.T) {
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(&lib.Config{LiveStore: liveStoreForTest(t)}),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	// A disabled client registers without connecting to anything.
	alpha := &schemas.MCPClientConfig{
		ID:               "client-alpha",
		Name:             "alpha",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
		Disabled:         true,
	}
	manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{}, mcpClientsAccount{clients: []*schemas.MCPClientConfig{alpha}}, nil, &mockLogger{}, nil)
	require.NoError(t, manager.AddClient(context.Background(), alpha.ID))
	client.SetMCPManager(manager)
	h := &VirtualMCPHandler{client: client, logger: &mockLogger{}}

	assert.Equal(t, "", h.validateToolSpecs([]configstoreTables.MCPToolSpec{{MCPClientID: "client-alpha", ToolNames: []string{"echo"}}}))
	assert.Equal(t, "invalid MCP client IDs: alpha", h.validateToolSpecs([]configstoreTables.MCPToolSpec{{MCPClientID: "alpha", ToolNames: []string{"echo"}}}))
}

// TestVirtualMCPStoreError pins how the store's caller errors answer: a taken slug is a 409, and a name
// with no usable slug and no endpoint_slug is a 400. The latter came back as a 500.
func TestVirtualMCPStoreError(t *testing.T) {
	code, msg, ok := virtualMCPStoreError(configstore.ErrMCPEndpointSlugExists)
	assert.True(t, ok)
	assert.Equal(t, fasthttp.StatusConflict, code)
	assert.Equal(t, configstore.ErrMCPEndpointSlugExists.Error(), msg)

	code, msg, ok = virtualMCPStoreError(configstore.ErrMCPEndpointSlugInvalid)
	assert.True(t, ok)
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.Equal(t, configstore.ErrMCPEndpointSlugInvalid.Error(), msg)

	_, _, ok = virtualMCPStoreError(errors.New("disk on fire"))
	assert.False(t, ok, "anything else stays a server error")
}

// TestListVirtualMCPs_FiltersByMCPClientID pins the mcp_client_id query parameter the MCP server sheet
// uses for its reverse lookup. Without it the sheet scanned one capped page client-side, so a Virtual
// MCP past the first 100 read as "not part of any virtual MCP" (#8645). total_count counts only the
// matches, and a match older than the requested page is still returned.
func TestListVirtualMCPs_FiltersByMCPClientID(t *testing.T) {
	store := setupPricingOverrideHandlerStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateVirtualMCP(ctx, &configstoreTables.TableVirtualMCP{
		Name: "Bundles target", Enabled: true, ParsedTools: []configstoreTables.MCPToolSpec{{MCPClientID: "target", ToolNames: []string{"*"}}},
	}))
	for _, name := range []string{"Filler one", "Filler two", "Filler three"} {
		require.NoError(t, store.CreateVirtualMCP(ctx, &configstoreTables.TableVirtualMCP{
			Name: name, Enabled: true, ParsedTools: []configstoreTables.MCPToolSpec{{MCPClientID: "other", ToolNames: []string{"*"}}},
		}))
	}
	h := &VirtualMCPHandler{store: &lib.Config{ConfigStore: store}, logger: &mockLogger{}}

	list := func(query string) map[string]any {
		t.Helper()
		reqCtx := newTestRequestCtx("")
		reqCtx.Request.SetRequestURI("/api/mcp/virtual-mcps?" + query)
		h.listVirtualMCPs(reqCtx)
		require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))
		var body map[string]any
		require.NoError(t, json.Unmarshal(reqCtx.Response.Body(), &body))
		return body
	}

	body := list("limit=2&mcp_client_id=target")
	assert.Equal(t, float64(1), body["total_count"])
	vmcps, ok := body["virtual_mcps"].([]any)
	require.True(t, ok)
	require.Len(t, vmcps, 1)
	assert.Equal(t, "Bundles target", vmcps[0].(map[string]any)["name"])

	body = list("limit=2&mcp_client_id=missing")
	assert.Equal(t, float64(0), body["total_count"])
	assert.Empty(t, body["virtual_mcps"])

	body = list("limit=2")
	assert.Equal(t, float64(4), body["total_count"], "without the parameter every Virtual MCP is listed")
}

// failingAssignmentGovernanceManager is a governance manager whose Virtual MCP assignment reload
// fails. It records whether each reload it was asked for was an attach or a detach.
type failingAssignmentGovernanceManager struct {
	GovernanceManager
	reloads []bool
}

// ReloadVirtualMCPAssignment records the reload and fails it.
func (m *failingAssignmentGovernanceManager) ReloadVirtualMCPAssignment(_ context.Context, _ string, _ uint, attached bool) error {
	m.reloads = append(m.reloads, attached)
	return errors.New("reload failed")
}

// TestVirtualMCPAssignmentReloadFailureAnswers500 checks that attaching or detaching a Virtual MCP
// answers 500 when the key's in-memory state fails to reload, as other writes do, rather than
// reporting success while the key keeps its previous assignment. The write itself stands.
func TestVirtualMCPAssignmentReloadFailureAnswers500(t *testing.T) {
	store := setupPricingOverrideHandlerStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{
		ID: "vk-1", Name: "vk-1", Value: *schemas.NewSecretVar("sk-bf-vk-1"), IsActive: new(true),
	}))
	def := &configstoreTables.TableVirtualMCP{
		Name: "Bundle", Enabled: true, ParsedTools: []configstoreTables.MCPToolSpec{{MCPClientID: "client", ToolNames: []string{"*"}}},
	}
	require.NoError(t, store.CreateVirtualMCP(ctx, def))
	manager := &failingAssignmentGovernanceManager{}
	h := &VirtualMCPHandler{store: &lib.Config{ConfigStore: store}, governanceManager: manager, logger: &mockLogger{}}

	call := func(handle func(*fasthttp.RequestCtx)) *fasthttp.RequestCtx {
		t.Helper()
		reqCtx := newTestRequestCtx("")
		reqCtx.SetUserValue("id", strconv.FormatUint(uint64(def.ID), 10))
		reqCtx.SetUserValue("vkId", "vk-1")
		handle(reqCtx)
		return reqCtx
	}

	attach := call(h.attachVirtualKey)
	assert.Equal(t, fasthttp.StatusInternalServerError, attach.Response.StatusCode(), string(attach.Response.Body()))
	assert.Contains(t, string(attach.Response.Body()), "Virtual MCP assignment attached in database but failed to reload in-memory state")
	ids, err := store.GetVirtualMCPIDsForVirtualKey(ctx, "vk-1")
	require.NoError(t, err)
	assert.Equal(t, []uint{def.ID}, ids, "the attachment stands")

	detach := call(h.detachVirtualKey)
	assert.Equal(t, fasthttp.StatusInternalServerError, detach.Response.StatusCode(), string(detach.Response.Body()))
	assert.Contains(t, string(detach.Response.Body()), "Virtual MCP assignment detached in database but failed to reload in-memory state")
	ids, err = store.GetVirtualMCPIDsForVirtualKey(ctx, "vk-1")
	require.NoError(t, err)
	assert.Empty(t, ids, "the detachment stands")

	assert.Equal(t, []bool{true, false}, manager.reloads)
}
