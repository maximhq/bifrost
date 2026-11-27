package routing

import (
	"context"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinRouterID is the conditional router the tests of this file route through.
const pinRouterID = "pin"

// pinRouterPlugin builds a routing plugin with one conditional router, named name, that sends the
// requests condition holds for to target, with fallbacks as the router's fallback chain.
func pinRouterPlugin(t *testing.T, governance *MockGovernance, name, condition string, target configstoreTables.TableModelRouterTarget, fallbacks ...configstoreTables.RoutingFallback) (*RoutingPlugin, *rules.TestRules) {
	t.Helper()
	ctx := context.Background()
	store, err := rules.NewTestRules(ctx, t.TempDir(), rules.NewMockLogger())
	require.NoError(t, err)
	t.Cleanup(store.Close)
	plugin, err := InitFromStore(ctx, nil, rules.NewMockLogger(), nil, store.Store, governance)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	require.NoError(t, store.SaveModelRouter(ctx, &configstore.ModelRouter{
		ID: pinRouterID, Name: name, Mode: configstoreTables.ModelRouterModeConditional,
		Targets: []configstoreTables.TableModelRouterTarget{target}, Fallbacks: fallbacks,
		Routing: &configstore.ModelRouterRouting{Enabled: true, Scope: "global", CelExpression: condition},
	}))
	servePinRouter(t, plugin, store)
	return plugin, store
}

// servePinRouter serves the router as the live store holds it, as the server does after a save.
func servePinRouter(t *testing.T, plugin *RoutingPlugin, store *rules.TestRules) {
	t.Helper()
	record, ok := store.Live().Collections.ModelRouters.Get(pinRouterID)
	require.True(t, ok)
	require.NoError(t, plugin.ReloadModelRouter(&record.Router))
}

// storePinRouterFallbacks writes the router's fallback column as given and reads the router back,
// the way a gateway reads it at boot. It bypasses the save path's checks, so a test can store
// what a rule that became this router left there.
func storePinRouterFallbacks(t *testing.T, plugin *RoutingPlugin, store *rules.TestRules, stored *string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.ConfigStore().DB().WithContext(ctx).Model(&configstoreTables.TableModelRouter{}).
		Where("id = ?", pinRouterID).Update("fallbacks", stored).Error)
	require.NoError(t, store.Live().Refresh(ctx, []livestore.Ref{{Kind: livestore.KindModelRouter, ID: pinRouterID}}))
	servePinRouter(t, plugin, store)
}

// restrictedContext returns a request context as core hands it to a PreRequestHook: writes to
// reserved keys are blocked and the plugin writes through its own scope (core/bifrost.go:
// ctx.BlockRestrictedWrites + ctx.WithPluginScope). root is what the rest of the request reads.
func restrictedContext() (root, scoped *schemas.BifrostContext) {
	root = schemas.NewBifrostContext(context.Background(), time.Now())
	root.BlockRestrictedWrites()
	pluginName := PluginName
	return root, root.WithPluginScope(&pluginName)
}

// TestRouterTarget_PinnedKeyReachesContext exercises the real propagation path of a target's key
// pin, under the restricted-write block core installs around every PreRequestHook. The pin lands
// on the dedicated, non-reserved BifrostContextKeyRoutingPinnedAPIKeyID (a write to the reserved
// BifrostContextKeyAPIKeyID would be silently dropped during this phase). Key selection
// (selectKeyFromProviderForModelWithPool) reads the pinned key back from this context.
func TestRouterTarget_PinnedKeyReachesContext(t *testing.T) {
	const pinnedKeyID = "pinned-key-abc-123"
	plugin, _ := pinRouterPlugin(t, NewMockGovernance(), "Pinned Key Router", "model == 'gpt-4o'", configstoreTables.TableModelRouterTarget{
		Provider: bifrost.Ptr("azure"), Model: bifrost.Ptr("gpt-4-turbo"), KeyID: bifrost.Ptr(pinnedKeyID), Weight: 1,
	})

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
	}
	root, scoped := restrictedContext()
	require.NoError(t, plugin.PreRequestHook(scoped, req))

	provider, model, _ := req.GetRequestFields()
	assert.Equal(t, schemas.Azure, provider)
	assert.Equal(t, "gpt-4-turbo", model)
	// The pinned key_id must be readable from the root context that key selection consults.
	ctxKeyID, _ := root.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string)
	assert.Equal(t, pinnedKeyID, ctxKeyID,
		"a router target's pinned key_id must reach BifrostContextKeyRoutingPinnedAPIKeyID that selectKeyFromProviderForModelWithPool reads")
}

// TestRouterTarget_FallbackKeyPinReachesRequest covers the fallback pin, which travels on the request's fallback list because core clears the context per attempt.
func TestRouterTarget_FallbackKeyPinReachesRequest(t *testing.T) {
	const fallbackKeyID = "fallback-key-xyz-789"
	plugin, _ := pinRouterPlugin(t, NewMockGovernance(), "Pinned Fallback Router", "model == 'gpt-4o'", configstoreTables.TableModelRouterTarget{
		Provider: bifrost.Ptr("azure"), Model: bifrost.Ptr("gpt-4-turbo"), Weight: 1,
	},
		configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: "vertex", Model: "gemini-2.5-pro", KeyID: fallbackKeyID}},
		configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: "anthropic"}},
	)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
	}
	_, scoped := restrictedContext()
	require.NoError(t, plugin.PreRequestHook(scoped, req))

	fallbacks := req.ChatRequest.Fallbacks
	require.Len(t, fallbacks, 2)
	assert.Equal(t, schemas.ModelProvider("vertex"), fallbacks[0].Provider)
	assert.Equal(t, fallbackKeyID, fallbacks[0].KeyID,
		"a target's fallback key_id must reach schemas.Fallback.KeyID, which core re-pins per attempt")

	// An unpinned fallback inherits the incoming model and stays load-balanced.
	assert.Equal(t, schemas.ModelProvider("anthropic"), fallbacks[1].Provider)
	assert.Equal(t, "gpt-4o", fallbacks[1].Model)
	assert.Empty(t, fallbacks[1].KeyID)
}

// TestRouterTarget_CustomProviderFallbackSurvivesRestart covers #7538. At boot the routing plugin
// loads routers from the config store before bifrost.Init registers custom providers, so a stored
// "backup/m" fallback is decoded while "backup" is still unknown. The fallback must still route to
// the custom provider once it is registered.
func TestRouterTarget_CustomProviderFallbackSurvivesRestart(t *testing.T) {
	const customProvider = schemas.ModelProvider("custom-backup-7538")
	schemas.UnregisterKnownProvider(customProvider)
	t.Cleanup(func() { schemas.UnregisterKnownProvider(customProvider) })

	plugin, store := pinRouterPlugin(t, NewMockGovernance(), "Custom Provider Fallback Router", "model == 'm'", configstoreTables.TableModelRouterTarget{
		Provider: bifrost.Ptr("openai"), Weight: 1,
	})
	// The router is read the way the config store reads it at boot, while the custom provider is
	// not yet registered.
	storePinRouterFallbacks(t, plugin, store, bifrost.Ptr(`["`+string(customProvider)+`/m"]`))

	// bifrost.Init now registers the custom provider, after the routers were loaded.
	schemas.RegisterKnownProvider(customProvider)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m"},
	}
	require.NoError(t, plugin.PreRequestHook(schemas.NewBifrostContext(context.Background(), time.Now()), req))

	require.Len(t, req.ChatRequest.Fallbacks, 1,
		"a stored fallback naming a custom provider must not be dropped when routers load before the provider is registered")
	assert.Equal(t, customProvider, req.ChatRequest.Fallbacks[0].Provider)
	assert.Equal(t, "m", req.ChatRequest.Fallbacks[0].Model)
}

// TestRouterTarget_TTFTTimeoutReachesContextForStreamsOnly: a chosen target's ttft_timeout_ms
// lands on BifrostContextKeyStreamFirstTokenTimeout, under the same restricted-write block core
// installs around PreRequestHook, and only for streaming request types.
func TestRouterTarget_TTFTTimeoutReachesContextForStreamsOnly(t *testing.T) {
	plugin, _ := pinRouterPlugin(t, NewMockGovernance(), "TTFT Router", "model == 'gpt-4o'", configstoreTables.TableModelRouterTarget{
		Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4o"), Weight: 1, TTFTTimeoutMs: new(1500),
	}, configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet-4-5"}})

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
			root, scoped := restrictedContext()
			require.NoError(t, plugin.PreRequestHook(scoped, req))
			got, _ := root.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout).(time.Duration)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestPreRequestHook_MaterializesVirtualKeyRoutingAfterRouters pins the ordering this plugin
// exists to guarantee: a router rewrites the model, and both the provider allowlist and the load
// balancer must then run against the rewritten model, not the one the caller sent. Publishing the
// allowlist for the incoming model would prune providers the routed model is actually allowed on.
func TestPreRequestHook_MaterializesVirtualKeyRoutingAfterRouters(t *testing.T) {
	vk := &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "vk", Value: *schemas.NewSecretVar("sk-bf-test"), IsActive: bifrost.Ptr(true)}
	governance := NewMockGovernance()
	governance.VirtualKeys["sk-bf-test"] = vk
	plugin, _ := pinRouterPlugin(t, governance, "Rewrite Model", "model == 'gpt-4o'", configstoreTables.TableModelRouterTarget{
		Provider: bifrost.Ptr("anthropic"), Model: bifrost.Ptr("claude-sonnet-4"), Weight: 1,
	})

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")

	require.NoError(t, plugin.PreRequestHook(ctx, req))

	_, model, _ := req.GetRequestFields()
	assert.Equal(t, "claude-sonnet-4", model, "the router should have rewritten the model")
	assert.Equal(t, []string{"claude-sonnet-4"}, governance.AllowlistModels,
		"allowlist must be published for the routed model, not the incoming one")
	assert.Equal(t, []string{"claude-sonnet-4"}, governance.LoadBalancedModels,
		"load balancing must run after the routers, on the routed model")
}

// TestPreRequestHook_LargePayloadPublishesAllowlistBeforeRefinement covers the large-payload
// path, where the model is carried on LargePayloadMetadata instead of the request body. The
// allowlist must be computed from the model the caller asked for (routed, before load balancing),
// because a virtual key's allowed/blacklisted model patterns are written against caller-facing
// names. Load balancing may rewrite the model to a provider-specific one, and an allowlist
// derived from that name can come back empty, which downstream layers treat as "no provider
// permitted".
func TestPreRequestHook_LargePayloadPublishesAllowlistBeforeRefinement(t *testing.T) {
	vk := &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "vk", Value: *schemas.NewSecretVar("sk-bf-test"), IsActive: bifrost.Ptr(true)}
	governance := NewMockGovernance()
	governance.VirtualKeys["sk-bf-test"] = vk
	// Stand in for the real load balancer picking a provider and refining the model to that
	// provider's deployment name.
	governance.OnLoadBalance = func(req *schemas.BifrostRequest) {
		req.SetProvider(schemas.Azure)
		req.SetModel("azure-gpt-4o-mini-deployment")
	}
	plugin, _ := pinRouterPlugin(t, governance, "Rewrite Model", "model == 'gpt-4o'", configstoreTables.TableModelRouterTarget{
		Model: bifrost.Ptr("gpt-4o-mini"), Weight: 1,
	})

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

// fallbackMatrixPlugin serves one global router matching model "m" whose fallbacks are
// decoded from storedFallbacks the way the config store does at boot.
func fallbackMatrixPlugin(t *testing.T, governance *MockGovernance, name string, storedFallbacks *string) *RoutingPlugin {
	t.Helper()
	plugin, store := pinRouterPlugin(t, governance, name, "model == 'm'", configstoreTables.TableModelRouterTarget{Provider: bifrost.Ptr("openai"), Weight: 1})
	storePinRouterFallbacks(t, plugin, store, storedFallbacks)
	return plugin
}

// TestRouterTarget_FallbackMatrix pins how every stored fallback form reaches the request, for a
// standard provider, a custom provider registered before the routers load, and a custom provider
// registered only after they load (the boot order behind #7538).
func TestRouterTarget_FallbackMatrix(t *testing.T) {
	providers := []struct {
		name          string
		provider      schemas.ModelProvider
		registerAfter bool // register after the router is decoded, as bifrost.Init does at boot
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
				plugin := fallbackMatrixPlugin(t, NewMockGovernance(), "Fallback Matrix Router", bifrost.Ptr(tc.stored(string(prov.provider))))
				if prov.registerAfter {
					schemas.RegisterKnownProvider(prov.provider)
				}

				req := &schemas.BifrostRequest{
					RequestType: schemas.ChatCompletionRequest,
					ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m"},
				}
				require.NoError(t, plugin.PreRequestHook(schemas.NewBifrostContext(context.Background(), time.Now()), req))
				assert.Equal(t, tc.want(prov.provider), req.ChatRequest.Fallbacks)
			})
		}
	}
}

// TestRouterTarget_FallbacksReachResponsesRequests covers a non-chat request type, since the
// plugin writes fallbacks through BifrostRequest.SetFallbacks for every type.
func TestRouterTarget_FallbacksReachResponsesRequests(t *testing.T) {
	plugin := fallbackMatrixPlugin(t, NewMockGovernance(), "Fallback Matrix Router", bifrost.Ptr(`["anthropic/fb-model",{"provider":"vertex","key_id":"k-1"}]`))
	req := &schemas.BifrostRequest{
		RequestType:      schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "m"},
	}
	require.NoError(t, plugin.PreRequestHook(schemas.NewBifrostContext(context.Background(), time.Now()), req))
	assert.Equal(t, []schemas.Fallback{
		{Provider: schemas.Anthropic, Model: "fb-model"},
		{Provider: schemas.Vertex, Model: "m", KeyID: "k-1"},
	}, req.ResponsesRequest.Fallbacks)
}

// TestRouterTarget_FallbacksVersusCallerFallbacks pins how a chosen target treats the fallbacks
// the caller sent: a target without fallbacks leaves them alone, a target with fallbacks replaces
// them, and a target whose every fallback is dropped still replaces them with an empty list.
func TestRouterTarget_FallbacksVersusCallerFallbacks(t *testing.T) {
	callerFallbacks := []schemas.Fallback{{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}
	cases := []struct {
		name   string
		stored *string
		want   []schemas.Fallback
	}{
		{name: "a target without fallbacks keeps the caller's", stored: nil, want: callerFallbacks},
		{name: "a target with an empty list keeps the caller's", stored: bifrost.Ptr(`[]`), want: callerFallbacks},
		{name: "router fallbacks replace the caller's", stored: bifrost.Ptr(`["anthropic/fb-model"]`), want: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "fb-model"}}},
		{name: "a router whose fallbacks all drop clears the caller's", stored: bifrost.Ptr(`["unknown-prefix/fb-model"]`), want: []schemas.Fallback{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plugin := fallbackMatrixPlugin(t, NewMockGovernance(), "Fallback Matrix Router", tc.stored)
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m", Fallbacks: append([]schemas.Fallback(nil), callerFallbacks...)},
			}
			require.NoError(t, plugin.PreRequestHook(schemas.NewBifrostContext(context.Background(), time.Now()), req))
			assert.Equal(t, tc.want, req.ChatRequest.Fallbacks)
		})
	}
}

// fallbackWarnings returns the routing log's warnings about fallbacks, in order.
func fallbackWarnings(ctx *schemas.BifrostContext) []string {
	var warnings []string
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if entry.Level == schemas.LogLevelWarn && strings.Contains(entry.Message, "fallback") {
			warnings = append(warnings, entry.Message)
		}
	}
	return warnings
}

// TestRouterTarget_SkippedFallbackIsLogged pins that a target fallback dropped for naming no known
// provider is reported on the request's routing log, naming the router and the entry as
// configured; fallbacks that resolve produce no warning.
func TestRouterTarget_SkippedFallbackIsLogged(t *testing.T) {
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
			plugin := fallbackMatrixPlugin(t, NewMockGovernance(), "Skip Log Router", bifrost.Ptr(tc.stored))
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "m"},
			}
			ctx := schemas.NewBifrostContext(context.Background(), time.Now())
			require.NoError(t, plugin.PreRequestHook(ctx, req))
			assert.Equal(t, tc.wantKept, req.ChatRequest.Fallbacks)

			warnings := fallbackWarnings(ctx)
			require.Len(t, warnings, len(tc.wantSkipped), "routing log warnings: %v", warnings)
			for i, entry := range tc.wantSkipped {
				assert.Contains(t, warnings[i], "Skip Log Router", "warning must name the router")
				assert.Contains(t, warnings[i], entry, "warning must name the skipped entry as configured")
			}
		})
	}
}

// TestPreRequestHook_FallbackOutsideAccessIsLogged pins #3261. A target fallback the request's own
// access does not allow is refused by governance before it reaches its provider, and once the chain
// runs out the caller is answered with the primary's error. The routing trail says so when the
// router takes the request, naming the router, the fallback and why, so a misconfigured router does
// not read as a fallback that silently never ran. The chain itself is left as the router wrote it,
// and a request carrying no access has nothing to report.
func TestPreRequestHook_FallbackOutsideAccessIsLogged(t *testing.T) {
	vkAccess := grant.NewAccess([]schemas.Permit{grant.NewPermit(grant.PermitVirtualKey, "vk-1", "vk", true, false, []schemas.ProviderPermit{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}, KeyIDs: schemas.WhiteList{"*"}},
		{Provider: "anthropic", AllowedModels: schemas.WhiteList{"claude-sonnet-4"}, KeyIDs: schemas.WhiteList{"*"}},
	}, nil)}, "", nil, grant.Union, nil)
	cases := []struct {
		name         string
		access       schemas.Access
		wantWarnings []string
	}{
		{
			name:   "fallbacks the access refuses are reported",
			access: vkAccess,
			wantWarnings: []string{
				"fallback anthropic/claude-opus-4 will be refused by governance: model claude-opus-4 is not allowed on anthropic",
				"fallback groq/llama-3.1-8b-instant will be refused by governance: provider groq is not allowed",
			},
		},
		{name: "a request with no access reports nothing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			governance := NewMockGovernance()
			governance.Access = tc.access
			plugin, _ := pinRouterPlugin(t, governance, "Access Log Router", "model == 'm'", configstoreTables.TableModelRouterTarget{
				Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-5.4-mini"), Weight: 1,
			},
				configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-sonnet-4"}},
				configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-opus-4"}},
				configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}},
			)

			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Model: "m"},
			}
			ctx := schemas.NewBifrostContext(context.Background(), time.Now())
			require.NoError(t, plugin.PreRequestHook(ctx, req))
			assert.Len(t, req.ChatRequest.Fallbacks, 3, "the chain stays as the router wrote it")

			warnings := fallbackWarnings(ctx)
			require.Len(t, warnings, len(tc.wantWarnings), "routing log warnings: %v", warnings)
			for i, want := range tc.wantWarnings {
				assert.Contains(t, warnings[i], "Model router 'Access Log Router'", "warning must name the router")
				assert.Contains(t, warnings[i], want)
			}
		})
	}
}

// TestPreRequestHook_LargePayloadDoesNotWarnAboutDroppedFallbacks pins that the large-payload
// path, which routes a synthetic request and copies back only its provider and model, does not
// warn about a target fallback the request will never carry: no chain is attached, so nothing
// will be refused.
func TestPreRequestHook_LargePayloadDoesNotWarnAboutDroppedFallbacks(t *testing.T) {
	governance := NewMockGovernance()
	governance.Access = grant.NewAccess([]schemas.Permit{grant.NewPermit(grant.PermitVirtualKey, "vk-1", "vk", true, false, []schemas.ProviderPermit{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}, KeyIDs: schemas.WhiteList{"*"}},
	}, nil)}, "", nil, grant.Union, nil)
	plugin, _ := pinRouterPlugin(t, governance, "Large Payload Router", "model == 'gpt-4o'", configstoreTables.TableModelRouterTarget{
		Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4o"), Weight: 1,
	}, configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}})

	metadata := &schemas.LargePayloadMetadata{Model: "gpt-4o"}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{},
	}
	require.NoError(t, plugin.PreRequestHook(ctx, req))
	require.Empty(t, req.ChatRequest.Fallbacks, "the large-payload path attaches no chain")

	for _, entry := range ctx.GetRoutingEngineLogs() {
		assert.NotContains(t, entry.Message, "will be refused by governance", "a fallback the request does not carry cannot be refused")
	}
}

// BenchmarkWarnFallbacksOutsideAccess measures the access check a router target with fallbacks
// runs, on a chain the access allows in full: the common case, which logs nothing.
func BenchmarkWarnFallbacksOutsideAccess(b *testing.B) {
	governance := NewMockGovernance()
	governance.Access = grant.NewAccess([]schemas.Permit{grant.NewPermit(grant.PermitVirtualKey, "vk-1", "vk", true, false, []schemas.ProviderPermit{
		{Provider: "anthropic", AllowedModels: schemas.WhiteList{"claude-sonnet-4"}, KeyIDs: schemas.WhiteList{"*"}},
		{Provider: "groq", AllowedModels: schemas.WhiteList{"llama-3.1-8b-instant"}, KeyIDs: schemas.WhiteList{"*"}},
	}, nil)}, "", nil, grant.Union, nil)
	plugin := &RoutingPlugin{governance: governance}
	fallbacks := []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet-4"}, {Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	b.ReportAllocs()
	for b.Loop() {
		plugin.warnFallbacksOutsideAccess(ctx, schemas.RoutingEngineModelRouter, "Model router", "router", fallbacks)
	}
}
