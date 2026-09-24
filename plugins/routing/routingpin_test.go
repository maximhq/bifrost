package routing

import (
	"context"
	"strings"
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

// TestApplyRoutingRules_FallbackKeyPinReachesRequest covers the fallback pin, which travels on the request's fallback list because core clears the context per attempt.
func TestApplyRoutingRules_FallbackKeyPinReachesRequest(t *testing.T) {
	const fallbackKeyID = "fallback-key-xyz-789"

	store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "fb-pin-1",
		Name:          "Pinned Fallback Rule",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("azure"), Model: bifrost.Ptr("gpt-4-turbo"), Weight: 1.0},
		},
		ParsedFallbacks: []configstoreTables.RoutingFallback{
			{Fallback: schemas.Fallback{Provider: "vertex", Model: "gemini-2.5-pro", KeyID: fallbackKeyID}},
			{Fallback: schemas.Fallback{Provider: "anthropic"}},
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

	fallbacks := req.ChatRequest.Fallbacks
	require.Len(t, fallbacks, 2)
	assert.Equal(t, schemas.ModelProvider("vertex"), fallbacks[0].Provider)
	assert.Equal(t, fallbackKeyID, fallbacks[0].KeyID,
		"a routing rule's fallback key_id must reach schemas.Fallback.KeyID, which core re-pins per attempt")

	// An unpinned fallback inherits the incoming model and stays load-balanced.
	assert.Equal(t, schemas.ModelProvider("anthropic"), fallbacks[1].Provider)
	assert.Equal(t, "gpt-4o", fallbacks[1].Model)
	assert.Empty(t, fallbacks[1].KeyID)
}

// TestApplyRoutingRules_CustomProviderFallbackSurvivesRestart covers #7538. At boot the routing
// plugin loads rules from the config store before bifrost.Init registers custom providers, so a
// stored "backup/m" fallback is decoded while "backup" is still unknown. The fallback must still
// route to the custom provider once it is registered, as it did before fallbacks gained key pins.
func TestApplyRoutingRules_CustomProviderFallbackSurvivesRestart(t *testing.T) {
	const customProvider = schemas.ModelProvider("custom-backup-7538")
	schemas.UnregisterKnownProvider(customProvider)
	t.Cleanup(func() { schemas.UnregisterKnownProvider(customProvider) })

	// Decode the rule the way the config store does at boot: AfterFind on the stored JSON column,
	// while the custom provider is not yet registered.
	stored := &configstoreTables.TableRoutingRule{
		ID:            "custom-fb-1",
		Name:          "Custom Provider Fallback Rule",
		CelExpression: "model == 'm'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai"), Weight: 1.0},
		},
		Fallbacks: bifrost.Ptr(`["` + string(customProvider) + `/m"]`),
		Enabled:   bifrost.Ptr(true),
		Scope:     "global",
		Priority:  0,
	}
	require.NoError(t, stored.AfterFind(nil))

	store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), stored))
	plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, NewMockGovernance())
	require.NoError(t, err)

	// bifrost.Init now registers the custom provider, after the rules were loaded.
	schemas.RegisterKnownProvider(customProvider)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m"},
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())

	decision, err := plugin.applyRoutingRules(ctx, req, rules.GovernanceScope{})
	require.NoError(t, err)
	require.NotNil(t, decision)

	require.Len(t, req.ChatRequest.Fallbacks, 1,
		"a stored fallback naming a custom provider must not be dropped when rules load before the provider is registered")
	assert.Equal(t, customProvider, req.ChatRequest.Fallbacks[0].Provider)
	assert.Equal(t, "m", req.ChatRequest.Fallbacks[0].Model)
}

// TestApplyRoutingRules_TTFTTimeoutReachesContextForStreamsOnly: a matched
// rule's ttft_timeout_ms lands on BifrostContextKeyStreamFirstTokenTimeout,
// under the same restricted-write block core installs around PreRequestHook,
// and only for streaming request types.
func TestApplyRoutingRules_TTFTTimeoutReachesContextForStreamsOnly(t *testing.T) {
	store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "ttft-1",
		Name:          "TTFT Rule",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4o"), Weight: 1.0},
		},
		ParsedFallbacks: []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet-4-5"}}},
		TTFTTimeoutMs:   new(1500),
		Enabled:         bifrost.Ptr(true),
		Scope:           "global",
	}))
	plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, NewMockGovernance())
	require.NoError(t, err)

	for _, tc := range []struct {
		requestType schemas.RequestType
		want        time.Duration
	}{
		{schemas.ChatCompletionStreamRequest, 1500 * time.Millisecond},
		{schemas.ResponsesStreamRequest, 1500 * time.Millisecond},
		{schemas.ChatCompletionRequest, 0},
	} {
		t.Run(string(tc.requestType), func(t *testing.T) {
			req := &schemas.BifrostRequest{RequestType: tc.requestType}
			if tc.requestType == schemas.ResponsesStreamRequest {
				req.ResponsesRequest = &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}
			} else {
				req.ChatRequest = &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}
			}
			root := schemas.NewBifrostContext(context.Background(), time.Now())
			root.BlockRestrictedWrites()
			pluginName := PluginName
			scoped := root.WithPluginScope(&pluginName)

			decision, err := plugin.applyRoutingRules(scoped, req, rules.GovernanceScope{})
			require.NoError(t, err)
			require.NotNil(t, decision)
			got, _ := root.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout).(time.Duration)
			assert.Equal(t, tc.want, got)
		})
	}

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

// fallbackMatrixPlugin loads one global rule matching model "m" whose fallbacks are decoded from
// storedFallbacks the way the config store does at boot (AfterFind on the JSON column).
func fallbackMatrixPlugin(t *testing.T, storedFallbacks *string) *RoutingPlugin {
	t.Helper()
	stored := &configstoreTables.TableRoutingRule{
		ID:            "fb-matrix",
		Name:          "Fallback Matrix Rule",
		CelExpression: "model == 'm'",
		Targets:       []configstoreTables.TableRoutingTarget{{Provider: bifrost.Ptr("openai"), Weight: 1.0}},
		Fallbacks:     storedFallbacks,
		Enabled:       bifrost.Ptr(true),
		Scope:         "global",
	}
	require.NoError(t, stored.AfterFind(nil))
	store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), stored))
	plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, NewMockGovernance())
	require.NoError(t, err)
	return plugin
}

// TestApplyRoutingRules_FallbackMatrix pins how every stored fallback form reaches the request,
// for a standard provider, a custom provider registered before the rules load, and a custom
// provider registered only after they load (the boot order behind #7538).
func TestApplyRoutingRules_FallbackMatrix(t *testing.T) {
	providers := []struct {
		name          string
		provider      schemas.ModelProvider
		registerAfter bool // register after the rule is decoded, as bifrost.Init does at boot
	}{
		{name: "standard", provider: schemas.Anthropic},
		{name: "custom registered before load", provider: "custom-fb-before"},
		{name: "custom registered after load", provider: "custom-fb-after", registerAfter: true},
	}
	cases := []struct {
		name   string
		stored func(p string) string // JSON column content
		want   func(p schemas.ModelProvider) []schemas.Fallback
	}{
		{
			name:   "legacy provider/model",
			stored: func(p string) string { return `["` + p + `/fb-model"]` },
			want: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "fb-model"}}
			},
		},
		{
			name:   "legacy provider/ uses the incoming model",
			stored: func(p string) string { return `["` + p + `/"]` },
			want:   func(p schemas.ModelProvider) []schemas.Fallback { return []schemas.Fallback{{Provider: p, Model: "m"}} },
		},
		{
			name:   "legacy model containing slashes",
			stored: func(p string) string { return `["` + p + `/meta-llama/Llama-3.1-8B"]` },
			want: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "meta-llama/Llama-3.1-8B"}}
			},
		},
		{
			name:   "pinned object",
			stored: func(p string) string { return `[{"provider":"` + p + `","model":"fb-model","key_id":"k-1"}]` },
			want: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "fb-model", KeyID: "k-1"}}
			},
		},
		{
			name:   "pinned object without a model uses the incoming model",
			stored: func(p string) string { return `[{"provider":"` + p + `","model":"","key_id":"k-1"}]` },
			want: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "m", KeyID: "k-1"}}
			},
		},
		{
			name: "mixed list keeps order and skips entries without a known provider",
			stored: func(p string) string {
				return `["unknown-prefix/fb-model",{"provider":"` + p + `","model":"a","key_id":"k-1"},"bare-model","` + p + `/b","openai/gpt-4o"]`
			},
			want: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "a", KeyID: "k-1"}, {Provider: p, Model: "b"}, {Provider: schemas.OpenAI, Model: "gpt-4o"}}
			},
		},
	}
	for _, prov := range providers {
		for _, tc := range cases {
			t.Run(prov.name+"/"+tc.name, func(t *testing.T) {
				if prov.provider != schemas.Anthropic {
					schemas.UnregisterKnownProvider(prov.provider)
					t.Cleanup(func() { schemas.UnregisterKnownProvider(prov.provider) })
					if !prov.registerAfter {
						schemas.RegisterKnownProvider(prov.provider)
					}
				}
				plugin := fallbackMatrixPlugin(t, bifrost.Ptr(tc.stored(string(prov.provider))))
				if prov.registerAfter {
					schemas.RegisterKnownProvider(prov.provider)
				}

				req := &schemas.BifrostRequest{
					RequestType: schemas.ChatCompletionRequest,
					ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m"},
				}
				decision, err := plugin.applyRoutingRules(schemas.NewBifrostContext(context.Background(), time.Now()), req, rules.GovernanceScope{})
				require.NoError(t, err)
				require.NotNil(t, decision)
				assert.Equal(t, tc.want(prov.provider), req.ChatRequest.Fallbacks)
			})
		}
	}
}

// TestApplyRoutingRules_FallbacksReachResponsesRequests covers a non-chat request type, since the
// plugin writes fallbacks through BifrostRequest.SetFallbacks for every type.
func TestApplyRoutingRules_FallbacksReachResponsesRequests(t *testing.T) {
	plugin := fallbackMatrixPlugin(t, bifrost.Ptr(`["anthropic/fb-model",{"provider":"vertex","key_id":"k-1"}]`))
	req := &schemas.BifrostRequest{
		RequestType:      schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m"},
	}
	decision, err := plugin.applyRoutingRules(schemas.NewBifrostContext(context.Background(), time.Now()), req, rules.GovernanceScope{})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, []schemas.Fallback{
		{Provider: schemas.Anthropic, Model: "fb-model"},
		{Provider: schemas.Vertex, Model: "m", KeyID: "k-1"},
	}, req.ResponsesRequest.Fallbacks)
}

// TestApplyRoutingRules_RuleFallbacksVersusCallerFallbacks pins how a matched rule treats the
// fallbacks the caller sent: a rule without fallbacks leaves them alone, a rule with fallbacks
// replaces them, and a rule whose every fallback is dropped still replaces them with an empty list.
func TestApplyRoutingRules_RuleFallbacksVersusCallerFallbacks(t *testing.T) {
	callerFallbacks := []schemas.Fallback{{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}
	cases := []struct {
		name   string
		stored *string
		want   []schemas.Fallback
	}{
		{name: "rule without fallbacks keeps the caller's", stored: nil, want: callerFallbacks},
		{name: "rule with an empty list keeps the caller's", stored: bifrost.Ptr(`[]`), want: callerFallbacks},
		{name: "rule fallbacks replace the caller's", stored: bifrost.Ptr(`["anthropic/fb-model"]`), want: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "fb-model"}}},
		{name: "rule whose fallbacks all drop clears the caller's", stored: bifrost.Ptr(`["unknown-prefix/fb-model"]`), want: []schemas.Fallback{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plugin := fallbackMatrixPlugin(t, tc.stored)
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m", Fallbacks: append([]schemas.Fallback(nil), callerFallbacks...)},
			}
			decision, err := plugin.applyRoutingRules(schemas.NewBifrostContext(context.Background(), time.Now()), req, rules.GovernanceScope{})
			require.NoError(t, err)
			require.NotNil(t, decision)
			assert.Equal(t, tc.want, req.ChatRequest.Fallbacks)
		})
	}
}

// TestApplyRoutingRules_SkippedFallbackIsLogged pins that a rule fallback dropped for naming no
// known provider is reported on the request's routing log, naming the rule and the entry as
// configured; fallbacks that resolve produce no warning.
func TestApplyRoutingRules_SkippedFallbackIsLogged(t *testing.T) {
	cases := []struct {
		name        string
		stored      string
		wantSkipped []string
		wantKept    []schemas.Fallback
	}{
		{
			name:        "unknown prefix and bare model are reported",
			stored:      `["unknown-prefix/m","anthropic/claude-sonnet-4","bare-model"]`,
			wantSkipped: []string{`"unknown-prefix/m"`, `"bare-model"`},
			wantKept:    []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet-4"}},
		},
		{
			name:     "resolvable fallbacks are not reported",
			stored:   `["anthropic/claude-sonnet-4",{"provider":"vertex","key_id":"k-1"}]`,
			wantKept: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet-4"}, {Provider: schemas.Vertex, Model: "m", KeyID: "k-1"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := &configstoreTables.TableRoutingRule{
				ID:            "fb-skip-log",
				Name:          "Skip Log Rule",
				CelExpression: "model == 'm'",
				Targets:       []configstoreTables.TableRoutingTarget{{Provider: bifrost.Ptr("openai"), Weight: 1.0}},
				Fallbacks:     bifrost.Ptr(tc.stored),
				Enabled:       bifrost.Ptr(true),
				Scope:         "global",
			}
			require.NoError(t, stored.AfterFind(nil))
			store, err := rules.NewLocalStore(context.Background(), rules.NewMockLogger(), nil)
			require.NoError(t, err)
			require.NoError(t, store.UpsertRule(context.Background(), stored))
			plugin, err := InitFromStore(context.Background(), nil, rules.NewMockLogger(), nil, store, NewMockGovernance())
			require.NoError(t, err)

			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m"},
			}
			ctx := schemas.NewBifrostContext(context.Background(), time.Now())
			decision, err := plugin.applyRoutingRules(ctx, req, rules.GovernanceScope{})
			require.NoError(t, err)
			require.NotNil(t, decision)
			assert.Equal(t, tc.wantKept, req.ChatRequest.Fallbacks)

			var routingWarnings []string
			for _, entry := range ctx.GetRoutingEngineLogs() {
				if entry.Level == schemas.LogLevelWarn && strings.Contains(entry.Message, "fallback") {
					routingWarnings = append(routingWarnings, entry.Message)
				}
			}
			require.Len(t, routingWarnings, len(tc.wantSkipped), "routing log warnings: %v", routingWarnings)
			for i, entry := range tc.wantSkipped {
				assert.Contains(t, routingWarnings[i], "Skip Log Rule", "warning must name the rule")
				assert.Contains(t, routingWarnings[i], entry, "warning must name the skipped entry as configured")
			}
		})
	}
}
