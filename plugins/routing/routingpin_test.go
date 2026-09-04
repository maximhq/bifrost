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

	plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, NewMockGovernance())
	require.NoError(t, err)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
	}

	root := schemas.NewBifrostContext(context.Background(), time.Now())
	root.BlockRestrictedWrites()
	pluginName := PluginName
	scoped := root.WithPluginScope(&pluginName)

	decision, err := plugin.applyRoutingRules(scoped, req, rules.GovernanceScope{})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, pinnedKeyID, decision.KeyID)

	// The pinned key_id must be readable from the root context that key selection consults.
	ctxKeyID, _ := root.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string)
	assert.Equal(t, pinnedKeyID, ctxKeyID,
		"routing-rule pinned key_id must reach BifrostContextKeyRoutingPinnedAPIKeyID that selectKeyFromProviderForModelWithPool reads")
}

// TestPreRequestHook_MaterializesVirtualKeyRoutingAfterRules pins the ordering this plugin
// exists to guarantee: a matched rule rewrites the model, and both the provider allowlist and
// the load balancer must then run against the rewritten model, not the one the caller sent.
// Publishing the allowlist for the incoming model would prune providers the routed model is
// actually allowed on.
func TestPreRequestHook_MaterializesVirtualKeyRoutingAfterRules(t *testing.T) {
	store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "rewrite-1",
		Name:          "Rewrite Model",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("anthropic"), Model: bifrost.Ptr("claude-sonnet-4"), Weight: 1.0},
		},
		Enabled:  bifrost.Ptr(true),
		Scope:    "global",
		Priority: 0,
	}))

	vk := &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "vk", Value: *schemas.NewSecretVar("sk-bf-test"), IsActive: bifrost.Ptr(true)}
	governance := NewMockGovernance()
	governance.VirtualKeys["sk-bf-test"] = vk

	plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, governance)
	require.NoError(t, err)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")

	require.NoError(t, plugin.PreRequestHook(ctx, req))

	_, model, _ := req.GetRequestFields()
	assert.Equal(t, "claude-sonnet-4", model, "rule should have rewritten the model")
	assert.Equal(t, []string{"claude-sonnet-4"}, governance.AllowlistModels,
		"allowlist must be published for the routed model, not the incoming one")
	assert.Equal(t, []string{"claude-sonnet-4"}, governance.LoadBalancedModels,
		"load balancing must run after rules, on the routed model")
}

// TestPreRequestHook_LargePayloadPublishesAllowlistBeforeRefinement covers the large-payload
// path, where the model is carried on LargePayloadMetadata instead of the request body. The
// allowlist must be computed from the model the caller asked for (post-rule, pre-load-balance),
// because a virtual key's allowed/blacklisted model patterns are written against caller-facing
// names. Load balancing may rewrite the model to a provider-specific one, and an allowlist
// derived from that name can come back empty, which downstream layers treat as "no provider
// permitted".
func TestPreRequestHook_LargePayloadPublishesAllowlistBeforeRefinement(t *testing.T) {
	store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "rewrite-lp",
		Name:          "Rewrite Model",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Model: bifrost.Ptr("gpt-4o-mini"), Weight: 1.0},
		},
		Enabled:  bifrost.Ptr(true),
		Scope:    "global",
		Priority: 0,
	}))

	vk := &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "vk", Value: *schemas.NewSecretVar("sk-bf-test"), IsActive: bifrost.Ptr(true)}
	governance := NewMockGovernance()
	governance.VirtualKeys["sk-bf-test"] = vk
	// Stand in for the real load balancer picking a provider and refining the model to that
	// provider's deployment name.
	governance.OnLoadBalance = func(req *schemas.BifrostRequest) {
		req.SetProvider(schemas.Azure)
		req.SetModel("azure-gpt-4o-mini-deployment")
	}

	plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, governance)
	require.NoError(t, err)

	metadata := &schemas.LargePayloadMetadata{Model: "gpt-4o"}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{},
	}
	require.NoError(t, plugin.PreRequestHook(ctx, req))

	assert.Equal(t, []string{"gpt-4o-mini"}, governance.AllowlistModels,
		"allowlist must be published for the routed model, before load balancing refines it")
	assert.Equal(t, []string{"gpt-4o-mini"}, governance.LoadBalancedModels,
		"load balancing must run on the routed model")
	assert.Equal(t, "azure/azure-gpt-4o-mini-deployment", metadata.Model,
		"the refined provider/model must be written back to the streamed metadata")
}
