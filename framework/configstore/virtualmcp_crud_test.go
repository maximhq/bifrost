package configstore

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupVirtualMCPTestStore(t *testing.T) *RDBConfigStore {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableVirtualMCP{}, &tables.TableVirtualKeyVirtualMCP{}))
	return store
}

func vmcpSpec(clientID string, tools ...string) tables.MCPToolSpec {
	return tables.MCPToolSpec{MCPClientID: clientID, ToolNames: tools}
}

func TestVirtualMCP_CreateSlugFromName(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	def := &tables.TableVirtualMCP{Name: "Machine Learning Team", Enabled: true, ParsedTools: []tables.MCPToolSpec{vmcpSpec("sentry", "list-issues")}}
	require.NoError(t, store.CreateVirtualMCP(ctx, def))
	assert.Equal(t, "machine-learning-team", def.EndpointSlug)

	got, err := store.GetVirtualMCPByID(ctx, def.ID)
	require.NoError(t, err)
	require.Len(t, got.ParsedTools, 1, "ParsedTools decoded by AfterFind")
	assert.Equal(t, "sentry", got.ParsedTools[0].MCPClientID)
	assert.Equal(t, []string{"list-issues"}, got.ParsedTools[0].ToolNames)
}

func TestVirtualMCP_RejectsExactDuplicateName(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	// Distinct endpoints, same name: the table's own unique index on name rejects the second.
	require.NoError(t, store.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "Engineering", EndpointSlug: "eng-a", Enabled: true}))

	err := store.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "Engineering", EndpointSlug: "eng-b", Enabled: true})
	assert.Error(t, err, "the pre-existing name unique index still rejects an exact duplicate name")
	assert.NotErrorIs(t, err, ErrVirtualMCPEndpointExists, "a name collision must not be mislabeled as an endpoint conflict")
}

func TestVirtualMCP_CaseVariantNamesCoexistWithDistinctEndpoints(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	// Name uniqueness is exact (not case-insensitive): case variants may coexist, as long as their
	// endpoints differ. Endpoints are the real constraint.
	require.NoError(t, store.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "Engineering", EndpointSlug: "eng-a", Enabled: true}))
	require.NoError(t, store.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "engineering", EndpointSlug: "eng-b", Enabled: true}))
}

func TestVirtualMCP_RejectsDuplicateEndpoint(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	// Two distinct names that slugify to the same endpoint.
	require.NoError(t, store.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "Support (Legacy)", Enabled: true}))

	err := store.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: "Support - Legacy", Enabled: true})
	assert.ErrorIs(t, err, ErrVirtualMCPEndpointExists, "distinct names may still collide on endpoint, and that is rejected")
}

func TestVirtualMCP_CreateHonorsDisabled(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	def := &tables.TableVirtualMCP{Name: "off", Enabled: false}
	require.NoError(t, store.CreateVirtualMCP(ctx, def))

	got, err := store.GetVirtualMCPByID(ctx, def.ID)
	require.NoError(t, err)
	assert.False(t, got.Enabled, "a disabled create must not be flipped to true by the column default")
}

func TestVirtualMCP_UpdateKeepsSlugImmutable(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	def := &tables.TableVirtualMCP{Name: "Engineering", Enabled: true}
	require.NoError(t, store.CreateVirtualMCP(ctx, def))
	originalSlug := def.EndpointSlug

	def.Name = "Platform"
	def.EndpointSlug = "platform" // must be ignored
	def.Enabled = false
	require.NoError(t, store.UpdateVirtualMCP(ctx, def))

	got, err := store.GetVirtualMCPByID(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, "Platform", got.Name)
	assert.Equal(t, originalSlug, got.EndpointSlug, "endpoint_slug is immutable")
	assert.False(t, got.Enabled)
}

func TestVirtualMCP_AttachDetachIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	def := &tables.TableVirtualMCP{Name: "eng", Enabled: true}
	require.NoError(t, store.CreateVirtualMCP(ctx, def))

	require.NoError(t, store.AttachVirtualMCPToVirtualKey(ctx, def.ID, "vk-1"))
	require.NoError(t, store.AttachVirtualMCPToVirtualKey(ctx, def.ID, "vk-1")) // idempotent
	require.NoError(t, store.AttachVirtualMCPToVirtualKey(ctx, def.ID, "vk-2"))

	ids, err := store.GetVirtualKeyIDsForVirtualMCP(ctx, def.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"vk-1", "vk-2"}, ids)

	require.NoError(t, store.DetachVirtualMCPFromVirtualKey(ctx, def.ID, "vk-1"))
	ids, err = store.GetVirtualKeyIDsForVirtualMCP(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"vk-2"}, ids)
}

func TestVirtualMCP_DeleteRemovesAssignments(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	def := &tables.TableVirtualMCP{Name: "eng", Enabled: true}
	require.NoError(t, store.CreateVirtualMCP(ctx, def))
	require.NoError(t, store.AttachVirtualMCPToVirtualKey(ctx, def.ID, "vk-1"))

	require.NoError(t, store.DeleteVirtualMCP(ctx, def.ID))

	_, err := store.GetVirtualMCPByID(ctx, def.ID)
	assert.True(t, errors.Is(err, ErrNotFound))
	ids, err := store.GetVirtualKeyIDsForVirtualMCP(ctx, def.ID)
	require.NoError(t, err)
	assert.Empty(t, ids, "assignments are removed with the definition")
}

func TestVirtualMCP_Paginated(t *testing.T) {
	ctx := context.Background()
	store := setupVirtualMCPTestStore(t)

	for _, name := range []string{"Engineering", "Research", "Support"} {
		require.NoError(t, store.CreateVirtualMCP(ctx, &tables.TableVirtualMCP{Name: name, Enabled: true}))
	}

	all, total, err := store.GetVirtualMCPsPaginated(ctx, VirtualMCPsQueryParams{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, all, 3)

	found, total, err := store.GetVirtualMCPsPaginated(ctx, VirtualMCPsQueryParams{Limit: 10, Search: "sea"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, found, 1)
	assert.Equal(t, "Research", found[0].Name)
}
