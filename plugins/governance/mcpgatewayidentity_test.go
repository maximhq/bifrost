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

// The /mcp handler settles what a verified token stood for onto the grant before admission: a
// vk-mode token as the key it names, a session-mode token as nothing presented. Admission resolves
// those exactly as it resolves a key from a header and a request with no credential.
func TestMCPGatewayAdmissionOfTokenIdentities(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"read_file"})
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &mockInMemoryStore{})
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, local, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

	admit := func(ctx *schemas.BifrostContext) *schemas.BifrostError {
		_, refused := plugin.Evaluate(ctx, &EvaluationRequest{RequestType: schemas.MCPToolExecutionRequest})
		return refused
	}

	t.Run("a vk-mode token settled as its key resolves the key's permit", func(t *testing.T) {
		ctx := emptyCtx()
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, mcpTestVKValue)
		ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, mcpTestVKValue), nil, nil, nil, nil, nil, nil))

		require.Nil(t, admit(ctx))
		require.NotNil(t, ctx.Grant().Access())
		assert.Equal(t, []string{"sentry-read_file"}, ctx.Grant().Access().MCPToolIncludeList())
		assert.Equal(t, vk.ID, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID))
	})

	t.Run("a token settled as something other than a key does not present the key on the context", func(t *testing.T) {
		ctx := emptyCtx()
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, mcpTestVKValue)
		ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialMCPToken, vk.ID), nil, nil, nil, nil, nil, nil))

		refused := admit(ctx)
		require.NotNil(t, refused)
		require.NotNil(t, refused.StatusCode)
		assert.Equal(t, 401, *refused.StatusCode)
	})

	t.Run("a session-mode token settled as nothing presented is admitted unrestricted", func(t *testing.T) {
		ctx := emptyCtx()
		ctx.SetValue(schemas.BifrostContextKeyMCPSessionID, "sess-1")

		require.Nil(t, admit(ctx))
		assert.Nil(t, ctx.Grant().Access())
	})
}
