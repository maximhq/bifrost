package routing

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyRoutingRules_PinnedKeyReachesContext exercises the real propagation path through
// applyRoutingRules, under the same restricted-write block that core's RunPreRequestHooks
// installs around every PreRequestHook (core/bifrost.go: ctx.BlockRestrictedWrites +
// ctx.WithPluginScope). This is what production actually does: the routing pin lands on the
// dedicated, non-reserved BifrostContextKeyRoutingPinnedAPIKeyID (a write to the reserved
// BifrostContextKeyAPIKeyID would be silently dropped during this phase). Key selection
// (selectKeyFromProviderForModelWithPool) reads the pinned key back from this context.
func TestApplyRoutingRules_PinnedKeyReachesContext(t *testing.T) {
	const pinnedKeyID = "pinned-key-abc-123"

	store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "pin-1",
		Name:          "Pinned Key Rule",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{
				Provider: bifrost.Ptr("azure"),
				Model:    bifrost.Ptr("gpt-4-turbo"),
				KeyID:    bifrost.Ptr(pinnedKeyID),
				Weight:   1.0,
			},
		},
		Enabled:  bifrost.Ptr(true),
		Scope:    "global",
		Priority: 0,
	}))

	plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, rules.NewMockGovernanceStore())
	require.NoError(t, err)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
	}

	root := schemas.NewBifrostContext(context.Background(), time.Now())
	root.BlockRestrictedWrites()
	pluginName := PluginName
	scoped := root.WithPluginScope(&pluginName)

	decision, err := plugin.applyRoutingRules(scoped, req, nil)
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, pinnedKeyID, decision.KeyID)

	// The pinned key_id must be readable from the root context that key selection consults.
	ctxKeyID, _ := root.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string)
	assert.Equal(t, pinnedKeyID, ctxKeyID,
		"routing-rule pinned key_id must reach BifrostContextKeyRoutingPinnedAPIKeyID that selectKeyFromProviderForModelWithPool reads")
}
