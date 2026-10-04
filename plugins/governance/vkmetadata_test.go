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

// Resolving access publishes the key's metadata for the logging plugin, as a copy: a request that
// changes its copy must not reach the key every other request shares. A key without metadata
// stamps nothing.
func TestResolveAccessStampsVirtualKeyMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]string
	}{
		{name: "no metadata stamps nothing", metadata: nil},
		{name: "metadata is stamped", metadata: map[string]string{"cost_center": "cc-42", "owner": "a@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vk := buildVKForMCPStamping(nil)
			vk.Metadata = tc.metadata

			logger := NewMockLogger()
			local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
			}, nil, &mockInMemoryStore{})
			require.NoError(t, err)
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, local, nil, nil, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

			ctx := emptyCtx()
			ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, mcpTestVKValue), nil, nil, nil, nil, nil, nil))
			_, err = plugin.ResolveAccess(ctx)
			require.NoError(t, err)
			require.Equal(t, vk.ID, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID), "the key resolved")

			stamped := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyMetadata)
			if tc.metadata == nil {
				assert.Nil(t, stamped, "a key without metadata must leave the context key absent")
				return
			}
			got, ok := stamped.(map[string]string)
			require.True(t, ok, "stamped metadata must be a map[string]string, got %T", stamped)
			assert.Equal(t, tc.metadata, got)

			got["cost_center"] = "tampered"
			stored, ok := local.GetVirtualKey(ctx, mcpTestVKValue)
			require.True(t, ok)
			assert.Equal(t, "cc-42", stored.Metadata["cost_center"], "the stamped map must be a copy, not the stored key's map")
		})
	}
}

// The connect path stamps the key by hand rather than through StampVirtualKeyScope, so it must
// carry the metadata on its own.
func TestPreMCPConnectionHook_StampsVirtualKeyMetadata(t *testing.T) {
	vk := buildVKForMCPStamping(nil)
	vk.Metadata = map[string]string{"cost_center": "cc-42"}
	plugin := newPluginForConnectionHook(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	})
	ctx := connectCtx(mcpTestVKValue)

	_, shortCircuit, err := plugin.PreMCPConnectionHook(ctx, connectReq("sentry"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, map[string]string{"cost_center": "cc-42"}, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyMetadata))
}
