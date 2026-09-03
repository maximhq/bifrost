package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateMCPClientConfig_WritesBackDerivedSlug pins that CreateMCPClientConfig sets the derived slug
// on the caller's config, so the in-memory registration (which reuses that pointer) serves /mcp/<slug>
// immediately rather than only after a restart.
func TestCreateMCPClientConfig_WritesBackDerivedSlug(t *testing.T) {
	s := setupRDBTestStore(t)
	cfg := &schemas.MCPClientConfig{
		ID: "c1", Name: "My Client", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}
	require.NoError(t, s.CreateMCPClientConfig(context.Background(), cfg))
	assert.Equal(t, "my-client", cfg.EndpointSlug, "the derived slug is written back to the caller's config")
}

// TestCreateMCPClientConfig_RejectsUnderivableSlug pins that a name slugifying to empty (with no
// endpoint_slug) is rejected with a typed error, so handlers can map invalid input to a 400.
func TestCreateMCPClientConfig_RejectsUnderivableSlug(t *testing.T) {
	s := setupRDBTestStore(t)
	err := s.CreateMCPClientConfig(context.Background(), &schemas.MCPClientConfig{
		ID: "c1", Name: "***",
		ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://x.invalid/mcp"),
	})
	assert.ErrorIs(t, err, ErrMCPEndpointSlugInvalid)
}

// TestCreateVirtualMCP_RejectsUnderivableSlug pins the same typed-error contract on the Virtual MCP
// side: a name slugifying to empty (with no endpoint_slug) returns ErrMCPEndpointSlugInvalid so
// handlers can map it to a 400.
func TestCreateVirtualMCP_RejectsUnderivableSlug(t *testing.T) {
	s := setupRDBTestStore(t)
	err := s.CreateVirtualMCP(context.Background(), &tables.TableVirtualMCP{Name: "***", Enabled: true})
	assert.ErrorIs(t, err, ErrMCPEndpointSlugInvalid)
}

// TestCreateMCPClientConfig_RejectsSlugTakenByVirtualMCP pins the cross-entity rule from the client
// side: a client cannot take an endpoint slug a Virtual MCP already serves (shared /mcp/<slug> space).
func TestCreateMCPClientConfig_RejectsSlugTakenByVirtualMCP(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "Shared", EndpointSlug: "shared", Enabled: true}))

	err := s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "c1", Name: "Shared Client", EndpointSlug: "shared",
		ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://x.invalid/mcp"),
	})
	assert.ErrorIs(t, err, ErrMCPEndpointSlugExists)
}

// TestCreateVirtualMCP_RejectsSlugTakenByMCPClient pins the same rule from the Virtual MCP side: a
// Virtual MCP cannot take an endpoint slug an MCP client already serves.
func TestCreateVirtualMCP_RejectsSlugTakenByMCPClient(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "c1", Name: "Alpha", EndpointSlug: "alpha",
		ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://x.invalid/mcp"),
	}))

	err := s.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "Alpha Group", EndpointSlug: "alpha", Enabled: true})
	assert.ErrorIs(t, err, ErrMCPEndpointSlugExists)
}

// TestBackfillMCPClientEndpointSlugs_FromName pins that a slug-less client gets a slug derived from
// its name.
func TestBackfillMCPClientEndpointSlugs_FromName(t *testing.T) {
	db := setupRDBTestStore(t).DB().WithContext(context.Background())
	require.NoError(t, db.Create(&tables.TableMCPClient{ClientID: "c1", Name: "My Client", ConnectionType: "stdio"}).Error)

	require.NoError(t, backfillMCPClientEndpointSlugs(db))

	var got tables.TableMCPClient
	require.NoError(t, db.Where("client_id = ?", "c1").First(&got).Error)
	assert.Equal(t, "my-client", got.EndpointSlug)
}

// TestBackfillMCPClientEndpointSlugs_SeedsFromVirtualMCPs pins the cross-entity rule: a client whose
// name slugifies to a slug a Virtual MCP already owns is suffixed, since both share /mcp/<slug>.
func TestBackfillMCPClientEndpointSlugs_SeedsFromVirtualMCPs(t *testing.T) {
	db := setupRDBTestStore(t).DB().WithContext(context.Background())
	require.NoError(t, db.Create(&tables.TableVirtualMCP{Name: "Shared Tool", EndpointSlug: "shared-tool", Enabled: true}).Error)
	require.NoError(t, db.Create(&tables.TableMCPClient{ClientID: "c1", Name: "Shared Tool", ConnectionType: "stdio"}).Error)

	require.NoError(t, backfillMCPClientEndpointSlugs(db))

	var got tables.TableMCPClient
	require.NoError(t, db.Where("client_id = ?", "c1").First(&got).Error)
	assert.NotEmpty(t, got.EndpointSlug, "backfill assigns a slug")
	assert.NotEqual(t, "shared-tool", got.EndpointSlug, "must not collide with the Virtual MCP's slug")
}
