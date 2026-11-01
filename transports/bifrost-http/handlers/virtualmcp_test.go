package handlers

import (
	"context"
	"errors"
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
		Account: lib.NewBaseAccount(&lib.Config{}),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	// A disabled client registers without connecting to anything.
	manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &mockLogger{}, nil)
	require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:               "client-alpha",
		Name:             "alpha",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
		Disabled:         true,
	}))
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
