package governance

import (
	"context"
	"encoding/json"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/require"
)

// TestHTTPTransportPreHook_VirtualKeyReplicateRefinesNestedModel verifies that
// virtual-key provider pinning rewrites the request model to Replicate's nested provider slug.
func TestHTTPTransportPreHook_VirtualKeyReplicateRefinesNestedModel(t *testing.T) {
	logger := NewMockLogger()
	mc := modelcatalog.NewTestCatalog(map[string]string{
		"openai/gpt-5-nano": "gpt-5-nano",
	})
	mc.UpsertModelDataForProvider(schemas.Replicate, &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "replicate/openai/gpt-5-nano"},
		},
	}, nil)

	virtualKey := buildVirtualKeyWithProviders(
		"vk1",
		"sk-bf-test",
		"replicate-only",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("replicate", []string{"*"}),
		},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, mc)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, mc, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"gpt-5-nano","messages":[{"role":"user","content":"Hello!"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	var payload struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &payload))
	require.Equal(t, "replicate/openai/gpt-5-nano", payload.Model)
}

func TestHTTPTransportPreHook_ModelOnlyVirtualKeySetsAvailableProviders(t *testing.T) {
	logger := NewMockLogger()

	openAIConfig := buildProviderConfig("openai", []string{"gpt-4o"})
	openAIConfig.Weight = nil
	anthropicConfig := buildProviderConfig("anthropic", []string{"claude-3-5-sonnet"})
	anthropicConfig.Weight = nil

	virtualKey := buildVirtualKeyWithProviders(
		"vk-constraint",
		"sk-bf-constraint-test",
		"provider-constraint-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			openAIConfig,
			anthropicConfig,
		},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-constraint-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hello!"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	allowedProviders, ok := bfCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	require.True(t, ok, "provider constraint should be set")
	require.Equal(t, []schemas.ModelProvider{schemas.OpenAI}, allowedProviders)
}

func TestHTTPTransportPreHook_ModelOnlyVirtualKeySetsEmptyAvailableProvidersWhenNoProviderAllowsModel(t *testing.T) {
	logger := NewMockLogger()

	virtualKey := buildVirtualKeyWithProviders(
		"vk-empty-constraint",
		"sk-bf-empty-constraint-test",
		"empty-provider-constraint-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("openai", []string{"gpt-4o"}),
		},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-empty-constraint-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"Hello!"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	allowedProviders, ok := bfCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	require.True(t, ok, "provider constraint should be set")
	require.Empty(t, allowedProviders)
}

// TestHTTPTransportPreHook_WildcardKeepsCatalogOpaqueProvider_VLLM verifies that a VK with a
// wildcard ("*") allow-list on a catalog-opaque provider (here vLLM, whose self-hosted models
// are never in the bundled catalog) keeps that provider in BifrostContextKeyAvailableProviders
// for a bare, uncatalogued model. Before the fix, loadBalanceProvider gates the provider on the
// catalog (GetProvidersForModel is empty), drops it, and publishes an empty provider set —
// dead-ending the request (issue #4122 / #3282).
func TestHTTPTransportPreHook_WildcardKeepsCatalogOpaqueProvider_VLLM(t *testing.T) {
	logger := NewMockLogger()

	// Catalog knows a first-party model but has NO model list for vLLM (self-hosted).
	mc := modelcatalog.NewTestCatalog(map[string]string{"openai/gpt-4o": "gpt-4o"})
	mc.UpsertModelDataForProvider(schemas.OpenAI,
		&schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "openai/gpt-4o"}}}, nil)

	// inMemoryStore must be non-nil so loadBalanceProvider takes the catalog branch.
	inMem := &mockInMemoryStore{
		configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.VLLM: {}, // native keyless: no CustomProviderConfig
		},
	}

	virtualKey := buildVirtualKeyWithProviders(
		"vk-vllm",
		"sk-bf-vllm-test",
		"vllm-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("vllm", []string{"*"}),
		},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, mc)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, mc, nil, inMem)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-vllm-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"my-self-hosted-llama","messages":[{"role":"user","content":"Hi"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	allowedProviders, ok := bfCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	require.True(t, ok, "available providers should be set")
	// PRE-PATCH: catalog has no vLLM models -> provider excluded -> [] -> FAILS.
	// POST-PATCH: wildcard + catalog-opaque -> kept -> [vllm].
	require.Equal(t, []schemas.ModelProvider{schemas.VLLM}, allowedProviders)
}

// TestHTTPTransportPreHook_MixedOpaqueAndCatalogProvider_GPT4o shows what lands in
// BifrostContextKeyAvailableProviders when a VK has catalog-known providers (openai, anthropic,
// vertex) AND a catalog-opaque vLLM (no list-models) — all under wildcard allow-lists — and the
// model is gpt-4o. Only openai (which serves gpt-4o per the catalog) and vLLM (a wildcard
// catch-all) should be available; anthropic and vertex are catalog-known but do not serve gpt-4o.
func TestHTTPTransportPreHook_MixedOpaqueAndCatalogProvider_GPT4o(t *testing.T) {
	logger := NewMockLogger()

	// Catalog knows openai/gpt-4o, anthropic/claude-3-5-sonnet, vertex/gemini-1.5-pro.
	// It has NO model list for vLLM (opaque).
	mc := modelcatalog.NewTestCatalog(map[string]string{"openai/gpt-4o": "gpt-4o"})
	mc.UpsertModelDataForProvider(schemas.OpenAI,
		&schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "openai/gpt-4o"}}}, nil)
	mc.UpsertModelDataForProvider(schemas.Anthropic,
		&schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "anthropic/claude-3-5-sonnet"}}}, nil)
	mc.UpsertModelDataForProvider(schemas.Vertex,
		&schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "vertex/gemini-1.5-pro"}}}, nil)

	inMem := &mockInMemoryStore{
		configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI:    {},
			schemas.Anthropic: {},
			schemas.Vertex:    {},
			schemas.VLLM:      {}, // opaque: no CustomProviderConfig, no catalog models
		},
	}

	virtualKey := buildVirtualKeyWithProviders(
		"vk-mixed",
		"sk-bf-mixed-test",
		"mixed-providers-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("openai", []string{"*"}),
			buildProviderConfig("anthropic", []string{"*"}),
			buildProviderConfig("vertex", []string{"*"}),
			buildProviderConfig("vllm", []string{"*"}),
		},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, mc)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, mc, nil, inMem)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-mixed-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	allowedProviders, ok := bfCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	require.True(t, ok, "available providers should be set")
	t.Logf("AvailableProviders for gpt-4o (VK = openai + vllm-opaque, both wildcard): %v", allowedProviders)

	// Both compete: openai matches the catalog for gpt-4o; vLLM is a wildcard catch-all.
	require.ElementsMatch(t, []schemas.ModelProvider{schemas.OpenAI, schemas.VLLM}, allowedProviders)
}

// TestHTTPTransportPreHook_VKExcludesUnlistedProviderEvenIfItServesModel shows that VK scoping
// wins: even when the catalog says BOTH openai and vertex serve gpt-4o, a VK granting access to
// only openai + vLLM yields exactly [openai, vllm] — vertex is never a candidate because it is
// not in the VK's provider configs.
func TestHTTPTransportPreHook_VKExcludesUnlistedProviderEvenIfItServesModel(t *testing.T) {
	logger := NewMockLogger()

	// Catalog: BOTH openai and vertex serve gpt-4o. vLLM has no catalog models (opaque).
	mc := modelcatalog.NewTestCatalog(map[string]string{"openai/gpt-4o": "gpt-4o"})
	mc.UpsertModelDataForProvider(schemas.OpenAI,
		&schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "openai/gpt-4o"}}}, nil)
	mc.UpsertModelDataForProvider(schemas.Vertex,
		&schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "vertex/gpt-4o"}}}, nil)

	inMem := &mockInMemoryStore{
		configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
			schemas.Vertex: {},
			schemas.VLLM:   {}, // opaque
		},
	}

	// VK grants access to ONLY openai and vLLM — NOT vertex, even though vertex serves gpt-4o.
	virtualKey := buildVirtualKeyWithProviders(
		"vk-scoped",
		"sk-bf-scoped-test",
		"openai-vllm-only-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("openai", []string{"*"}),
			buildProviderConfig("vllm", []string{"*"}),
		},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, mc)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, mc, nil, inMem)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-scoped-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	allowedProviders, ok := bfCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	require.True(t, ok, "available providers should be set")
	t.Logf("AvailableProviders for gpt-4o (catalog: openai+vertex serve it; VK = openai + vllm only): %v", allowedProviders)

	// Vertex serves gpt-4o per the catalog but is NOT in the VK, so it must be absent.
	require.ElementsMatch(t, []schemas.ModelProvider{schemas.OpenAI, schemas.VLLM}, allowedProviders)
}

// TestHTTPTransportPreHook_WildcardOpaqueProviderRespectsBlacklist guards the ordering in
// loadBalanceProvider: the blacklist pre-pass must exclude a provider before the wildcard +
// catalog-opaque shortcut applies, so a blacklisted model on an opaque provider is dropped
// from BifrostContextKeyAvailableProviders even under a ["*"] allow-list.
func TestHTTPTransportPreHook_WildcardOpaqueProviderRespectsBlacklist(t *testing.T) {
	logger := NewMockLogger()

	mc := modelcatalog.NewTestCatalog(nil) // no vLLM models -> opaque

	inMem := &mockInMemoryStore{
		configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.VLLM: {},
		},
	}

	vllmConfig := buildProviderConfig("vllm", []string{"*"})
	vllmConfig.BlacklistedModels = schemas.BlackList{"my-self-hosted-llama"}

	virtualKey := buildVirtualKeyWithProviders(
		"vk-vllm-bl",
		"sk-bf-vllm-bl-test",
		"vllm-bl-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{vllmConfig},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, mc)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, mc, nil, inMem)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-vllm-bl-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"my-self-hosted-llama","messages":[{"role":"user","content":"Hi"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	allowedProviders, ok := bfCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	require.True(t, ok, "available providers should be set")
	// Blacklisted model is excluded even though the provider is catalog-opaque under ["*"].
	require.Empty(t, allowedProviders)
}

// TestHTTPTransportPreHook_GenAIRoutingRulePreservesTarget verifies that when a routing rule
// matches on the /genai path, governance load balancing does not override the routing-rule target
// with a provider from the VK pool (regression test for issue #2516).
func TestHTTPTransportPreHook_GenAIRoutingRulePreservesTarget(t *testing.T) {
	logger := NewMockLogger()

	routingRule := configstoreTables.TableRoutingRule{
		ID:            "rule-genai-1",
		Name:          "genai-repro-rule",
		Enabled:       bifrost.Ptr(true),
		CelExpression: `model == "probe-genai-model" && provider == ""`,
		Targets: []configstoreTables.TableRoutingTarget{
			{
				RuleID:   "rule-genai-1",
				Provider: bifrost.Ptr("repro-openai-a"),
				Model:    bifrost.Ptr("error-test"),
				Weight:   1.0,
			},
		},
		Scope:    "global",
		Priority: 1,
	}

	// VK with repro-openai-b at weight=1 — this is what governance LB would wrongly select without the fix
	virtualKey := buildVirtualKeyWithProviders(
		"vk-genai",
		"sk-bf-genai-test",
		"genai-repro-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("repro-openai-b", []string{"*"}),
		},
	)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*virtualKey},
		RoutingRules: []configstoreTables.TableRoutingRule{routingRule},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/genai/v1beta/models/probe-genai-model:generateContent"
	req.PathParams["model"] = "probe-genai-model:generateContent"
	req.Headers["Authorization"] = "Bearer sk-bf-genai-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	// Routing rule matched and set context model to "repro-openai-a/error-test:generateContent".
	// Governance LB must NOT override this with "repro-openai-b/probe-genai-model:generateContent".
	ctxModel, ok := bfCtx.Value("model").(string)
	require.True(t, ok, "context model should be set")
	require.Equal(t, "repro-openai-a/error-test:generateContent", ctxModel)
}

// TestHTTPTransportPreHook_GenAIRoutingRulePreservesTarget_WithStore is a production-like variant
// of TestHTTPTransportPreHook_GenAIRoutingRulePreservesTarget that passes a non-nil inMemoryStore
// containing the routing-rule provider, confirming the fix holds when p.inMemoryStore != nil
// and the provider IS present in GetConfiguredProviders (the normal production code path).
func TestHTTPTransportPreHook_GenAIRoutingRulePreservesTarget_WithStore(t *testing.T) {
	logger := NewMockLogger()

	routingRule := configstoreTables.TableRoutingRule{
		ID:            "rule-genai-ws-1",
		Name:          "genai-repro-rule-with-store",
		Enabled:       bifrost.Ptr(true),
		CelExpression: `model == "probe-genai-model" && provider == ""`,
		Targets: []configstoreTables.TableRoutingTarget{
			{
				RuleID:   "rule-genai-ws-1",
				Provider: bifrost.Ptr("repro-openai-a"),
				Model:    bifrost.Ptr("error-test"),
				Weight:   1.0,
			},
		},
		Scope:    "global",
		Priority: 1,
	}

	virtualKey := buildVirtualKeyWithProviders(
		"vk-genai-ws",
		"sk-bf-genai-ws-test",
		"genai-repro-vk-with-store",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("repro-openai-b", []string{"*"}),
		},
	)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*virtualKey},
		RoutingRules: []configstoreTables.TableRoutingRule{routingRule},
	}, nil)
	require.NoError(t, err)

	// Register the fake provider so ParseModelString can split "repro-openai-a/model"
	// the same way it would for a real provider in production.
	schemas.RegisterKnownProvider("repro-openai-a")
	t.Cleanup(func() { schemas.UnregisterKnownProvider("repro-openai-a") })

	// Use a non-nil inMemoryStore that recognises the routing-rule provider,
	// mirroring production where configured providers are always registered in the store.
	inMemStore := &mockInMemoryStore{
		configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
			"repro-openai-a": {},
		},
	}

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, inMemStore)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/genai/v1beta/models/probe-genai-model:generateContent"
	req.PathParams["model"] = "probe-genai-model:generateContent"
	req.Headers["Authorization"] = "Bearer sk-bf-genai-ws-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	ctxModel, ok := bfCtx.Value("model").(string)
	require.True(t, ok, "context model should be set")
	require.Equal(t, "repro-openai-a/error-test:generateContent", ctxModel)
}

// TestHTTPTransportPreHook_GenAINoRoutingRuleStillLoadBalances verifies that when no routing rule
// matches on the /genai path, governance load balancing still selects a provider from the VK pool.
func TestHTTPTransportPreHook_GenAINoRoutingRuleStillLoadBalances(t *testing.T) {
	logger := NewMockLogger()

	// VK with repro-openai-b at weight=1 — LB should select this
	virtualKey := buildVirtualKeyWithProviders(
		"vk-genai-lb",
		"sk-bf-genai-lb-test",
		"genai-lb-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("repro-openai-b", []string{"*"}),
		},
	)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
		// No routing rules — governance LB should run normally
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/genai/v1beta/models/probe-genai-model:generateContent"
	req.PathParams["model"] = "probe-genai-model:generateContent"
	req.Headers["Authorization"] = "Bearer sk-bf-genai-lb-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	// No routing rule: governance LB must still run and select repro-openai-b from the VK pool
	ctxModel, ok := bfCtx.Value("model").(string)
	require.True(t, ok, "context model should be set by governance LB")
	require.Equal(t, "repro-openai-b/probe-genai-model:generateContent", ctxModel)
}

// TestHTTPTransportPreHook_BedrockRoutingRulePreservesTarget verifies that when a routing rule
// matches on the /bedrock path, governance load balancing does not override the routing-rule target
// (regression test mirroring the GenAI fix for the Bedrock integration).
func TestHTTPTransportPreHook_BedrockRoutingRulePreservesTarget(t *testing.T) {
	logger := NewMockLogger()

	routingRule := configstoreTables.TableRoutingRule{
		ID:            "rule-bedrock-1",
		Name:          "bedrock-repro-rule",
		Enabled:       bifrost.Ptr(true),
		CelExpression: `model == "probe-bedrock-model" && provider == ""`,
		Targets: []configstoreTables.TableRoutingTarget{
			{
				RuleID:   "rule-bedrock-1",
				Provider: bifrost.Ptr("repro-openai-a"),
				Model:    bifrost.Ptr("error-test"),
				Weight:   1.0,
			},
		},
		Scope:    "global",
		Priority: 1,
	}

	// VK with repro-openai-b at weight=1 — this is what governance LB would wrongly select without the fix
	virtualKey := buildVirtualKeyWithProviders(
		"vk-bedrock",
		"sk-bf-bedrock-test",
		"bedrock-repro-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("repro-openai-b", []string{"*"}),
		},
	)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*virtualKey},
		RoutingRules: []configstoreTables.TableRoutingRule{routingRule},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/bedrock/model/probe-bedrock-model/converse"
	req.PathParams["modelId"] = "probe-bedrock-model"
	req.Headers["Authorization"] = "Bearer sk-bf-bedrock-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	// Routing rule matched and set context modelId to "repro-openai-a/error-test".
	// Governance LB must NOT override this with "repro-openai-b/probe-bedrock-model".
	ctxModelID, ok := bfCtx.Value("modelId").(string)
	require.True(t, ok, "context modelId should be set")
	require.Equal(t, "repro-openai-a/error-test", ctxModelID)
}

// TestHTTPTransportPreHook_BedrockNoRoutingRuleStillLoadBalances verifies that when no routing rule
// matches on the /bedrock path, governance load balancing still selects a provider from the VK pool.
func TestHTTPTransportPreHook_BedrockNoRoutingRuleStillLoadBalances(t *testing.T) {
	logger := NewMockLogger()

	// VK with repro-openai-b at weight=1 — LB should select this
	virtualKey := buildVirtualKeyWithProviders(
		"vk-bedrock-lb",
		"sk-bf-bedrock-lb-test",
		"bedrock-lb-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("repro-openai-b", []string{"*"}),
		},
	)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
		// No routing rules — governance LB should run normally
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/bedrock/model/probe-bedrock-model/converse"
	req.PathParams["modelId"] = "probe-bedrock-model"
	req.Headers["Authorization"] = "Bearer sk-bf-bedrock-lb-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	// No routing rule: governance LB must still run and select repro-openai-b from the VK pool
	ctxModelID, ok := bfCtx.Value("modelId").(string)
	require.True(t, ok, "context modelId should be set by governance LB")
	require.Equal(t, "repro-openai-b/probe-bedrock-model", ctxModelID)
}
