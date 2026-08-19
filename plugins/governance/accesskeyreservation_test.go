package governance

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Resolving a request's access happens inside this plugin's own hook, and core blocks writes to the
// keys it owns for as long as a hook batch is running. The answer lands anyway because the deployment
// named this plugin as that key's writer where it wired it, which is also what stops every other plugin
// in the same batch from rewriting what was resolved.
//
// The second case says what a deployment that names nobody gets, and it is worth knowing: not quiet
// ungoverned traffic, but a refusal on every request. The answer is dropped, the funnel reads the
// request back as one whose credential resolved to nothing, and turns it away — so a wiring mistake
// takes the deployment down rather than letting requests through uncharged.
func TestResolvedAccessIsRecordedOnlyByThePluginNamedForTheKey(t *testing.T) {
	vk := buildVKForMCPStamping(nil)

	// What core hands a plugin: the request, and a scope over it named after the plugin running.
	inHookAs := func(t *testing.T, plugin string) (*schemas.BifrostContext, *schemas.BifrostContext) {
		t.Helper()
		ctx := newPreRequestCtx(nil, nil)
		ctx.BlockRestrictedWrites()
		t.Cleanup(ctx.UnblockRestrictedWrites)
		name := plugin
		scoped := ctx.WithPluginScope(&name)
		t.Cleanup(scoped.ReleasePluginScope)
		return ctx, scoped
	}

	t.Run("named for the key, as a deployment names it", func(t *testing.T) {
		schemas.RegisterReservedKeyWriter(PluginName, schemas.BifrostContextKeyGovernanceEffectiveAccess)
		plugin := newAccessTestPlugin(t, vk, nil)
		ctx, scoped := inHookAs(t, PluginName)

		_, shortCircuit, err := plugin.PreLLMHook(scoped, newChatRequest())

		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		access := grants.EffectiveAccessFromContext(ctx)
		require.NotNil(t, access, "the hook resolved this request's access and could not record it")
		require.NotNil(t, access.Base())
		assert.Equal(t, "vk-mcp-stamp", access.Base().ID, "the answer on the request is the one this hook resolved")
		assert.True(t, access.IsProviderAllowed("openai"))
	})

	t.Run("unnamed, and every request is refused rather than quietly ungoverned", func(t *testing.T) {
		plugin := newAccessTestPlugin(t, vk, nil)
		ctx, scoped := inHookAs(t, "a-deployment-that-named-nobody")

		_, shortCircuit, err := plugin.PreLLMHook(scoped, newChatRequest())

		require.NoError(t, err)
		require.NotNil(t, shortCircuit, "a dropped answer must not read as a governed request")
		require.NotNil(t, shortCircuit.Error)
		require.NotNil(t, shortCircuit.Error.StatusCode)
		assert.Equal(t, 401, *shortCircuit.Error.StatusCode,
			"the funnel reads back nothing and refuses the credential it was handed")
		assert.Nil(t, grants.EffectiveAccessFromContext(ctx),
			"and nothing is left on the request for anything downstream to enforce or bill against")
	})
}
