package governance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLoadBalanceTestPlugin(t *testing.T, vk *configstoreTables.TableVirtualKey) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)
	return &GovernancePlugin{
		logger:   logger,
		store:    store,
		resolver: NewBudgetResolver(store, nil, logger, nil),
	}
}

// loadBalance runs LoadBalanceProvider over a model string the way the routing plugin does for
// large-payload requests, and returns the resolved model (provider-prefixed when a provider
// was selected, plain model otherwise).
func loadBalance(t *testing.T, p *GovernancePlugin, ctx *schemas.BifrostContext, modelIn string) (string, error) {
	t.Helper()
	providerIn, parsedModel := schemas.ParseModelString(modelIn, "")
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: providerIn, Model: parsedModel},
	}
	if err := p.LoadBalanceProvider(ctx, req); err != nil {
		return modelIn, err
	}
	provider, model, _ := req.GetRequestFields()
	if provider != "" {
		return string(provider) + "/" + model, nil
	}
	return model, nil
}

// TestLoadBalanceProvider_ExplicitProviderPrefixSkipsLoadBalancing covers the
// large-payload path, where metadata.Model arrives provider-prefixed and unparsed, and
// the explicit prefix must win over VK load balancing even when multiple weighted
// providers allow the model.
func TestLoadBalanceProvider_ExplicitProviderPrefixSkipsLoadBalancing(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
		buildProviderConfig("anthropic", []string{"*"}),
	})
	p := newLoadBalanceTestPlugin(t, vk)

	for range 20 {
		ctx := presentCtx("sk-bf-lb")
		got, err := loadBalance(t, p, ctx, "openai/gpt-4o")
		require.NoError(t, err)
		assert.Equal(t, "openai/gpt-4o", got)
	}
}

// TestLoadBalanceProvider_UnprefixedModelLoadBalances verifies that a bare model
// string still goes through VK load balancing and comes back provider-prefixed.
func TestLoadBalanceProvider_UnprefixedModelLoadBalances(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	})
	p := newLoadBalanceTestPlugin(t, vk)
	ctx := presentCtx("sk-bf-lb")

	got, err := loadBalance(t, p, ctx, "gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-4o", got)
}

// TestLoadBalanceProvider_UnknownPrefixIsTreatedAsModelNamespace verifies that a
// "/" prefix that is not a known provider (e.g. a HuggingFace-style namespace) is
// kept as part of the model name and load balancing still applies.
func TestLoadBalanceProvider_UnknownPrefixIsTreatedAsModelNamespace(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("groq", []string{"*"}),
	})
	p := newLoadBalanceTestPlugin(t, vk)
	ctx := presentCtx("sk-bf-lb")

	got, err := loadBalance(t, p, ctx, "meta-llama/llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "groq/meta-llama/llama-3.1-8b-instant", got)
}

// ---------------------------------------------------------------------------
// what load balancing takes from the request's access
// ---------------------------------------------------------------------------

// newLoadBalanceTestPluginWithStore builds a load-balancing plugin over a store that composes
// further permits onto every request, standing in for a deployment that resolves permit holders
// beyond the presented key.
func newLoadBalanceTestPluginWithStore(t *testing.T, vk *configstoreTables.TableVirtualKey, wrap *permitStore) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &mockInMemoryStore{})
	require.NoError(t, err)
	wrap.GovernanceStore = local
	return &GovernancePlugin{
		logger:   logger,
		store:    wrap,
		resolver: NewBudgetResolver(wrap, nil, logger, nil),
	}
}

func lbCtx() *schemas.BifrostContext {
	ctx := presentCtx("sk-bf-lb")
	return ctx
}

// A provider the request holds only through a composed permit is a candidate, and carries that
// permit's weight: the whole point of composing permits onto a request rather than beside it.
func TestLoadBalanceProvider_ComposedProviderParticipates(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", nil)
	contributed := grant.NewPermit("other", "o1", "Pool", true, false, []schemas.ProviderPermit{{
		Provider:      "groq",
		AllowedModels: []string{"*"},
		KeyIDs:        []string{"key-shared"},
		Weight:        schemas.Ptr(2.0),
	}}, nil)
	p := newLoadBalanceTestPluginWithStore(t, vk, &permitStore{
		scoping: contributed,
		mode:    grant.Union,
	})

	got, err := loadBalance(t, p, lbCtx(), "llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "groq/llama-3.1-8b-instant", got, "the composed provider serves the request")
}

// Under intersect, a provider the composed permit does not permit is never a candidate, however
// the key is configured.
func TestLoadBalanceProvider_IntersectRemovedProviderIsNeverACandidate(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("groq", []string{"*"}),
	})
	narrow := permitWithProviders("other", "o1", "Narrow", "openai")
	p := newLoadBalanceTestPluginWithStore(t, vk, &permitStore{
		scoping: narrow,
		mode:    grant.Intersect,
	})

	got, err := loadBalance(t, p, lbCtx(), "llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "llama-3.1-8b-instant", got, "no provider is eligible, so the model is untouched")
}

// A provider with no weight is not a candidate for selection: composing a permit onto a request
// must not promote an unweighted provider into load balancing.
func TestLoadBalanceProvider_UnweightedProviderIsNotSelected(t *testing.T) {
	unweighted := buildProviderConfig("groq", []string{"*"})
	unweighted.Weight = nil
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK",
		[]configstoreTables.TableVirtualKeyProviderConfig{unweighted})
	p := newLoadBalanceTestPlugin(t, vk)

	got, err := loadBalance(t, p, lbCtx(), "llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "llama-3.1-8b-instant", got)
}

// Two configs for one provider stay two candidates: there is no unique constraint on
// (key, provider), and collapsing them would silently change which weights are in play.
func TestLoadBalanceProvider_DuplicateProviderConfigsBothCount(t *testing.T) {
	first := buildProviderConfig("groq", []string{"*"})
	first.Weight = schemas.Ptr(1.0)
	second := buildProviderConfig("groq", []string{"*"})
	second.Weight = schemas.Ptr(1.0)
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK",
		[]configstoreTables.TableVirtualKeyProviderConfig{first, second})

	access := grant.NewAccess([]schemas.Permit{vkPermit(vk, nil)}, nil, "", nil)
	assert.Len(t, access.ProvidersForModel("llama-3.1-8b-instant"), 2)
}

// The allowlist is published from the request's access, and a request with no key publishes
// nothing at all: an empty list would mean no provider is permitted and fail-close the request.
func TestPublishRoutingAllowlist(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("groq", []string{"llama-3.1-8b-instant"}),
		buildProviderConfig("openai", []string{"gpt-4o"}),
	})

	t.Run("narrowed to the providers that serve the model", func(t *testing.T) {
		p := newLoadBalanceTestPlugin(t, vk)
		ctx := lbCtx()

		p.PublishRoutingAllowlist(ctx, "gpt-4o")

		allowed, ok := ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
		require.True(t, ok)
		assert.Equal(t, []schemas.ModelProvider{"openai"}, allowed)
	})

	t.Run("a request carrying no permits publishes nothing", func(t *testing.T) {
		p := newLoadBalanceTestPlugin(t, vk)
		// No key on the request, and a store that answers only for keys: nothing resolves.
		ctx := emptyCtx()

		p.PublishRoutingAllowlist(ctx, "gpt-4o")

		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders),
			"an empty allowlist would mean no provider is permitted")
	})

	// Publishing keys off what the request holds, not off how it authenticated: a store that
	// grants providers to a request presenting no key narrows routing the same way a key does.
	t.Run("permits held without a key are published", func(t *testing.T) {
		p := newLoadBalanceTestPluginWithStore(t, vk, &permitStore{
			baseOverride: permitWithProviders("other", "o1", "Holder", "anthropic"),
		})
		ctx := emptyCtx()

		p.PublishRoutingAllowlist(ctx, "")

		allowed, ok := ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
		require.True(t, ok)
		assert.Equal(t, []schemas.ModelProvider{"anthropic"}, allowed)
	})

	t.Run("a composed permit widens the allowlist under union", func(t *testing.T) {
		p := newLoadBalanceTestPluginWithStore(t, vk, &permitStore{
			scoping: permitWithProviders("other", "o1", "Pool", "anthropic"),
			mode:    grant.Union,
		})
		ctx := lbCtx()

		p.PublishRoutingAllowlist(ctx, "")

		allowed, _ := ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
		assert.ElementsMatch(t, []schemas.ModelProvider{"groq", "openai", "anthropic"}, allowed)
	})
}

// Load balancing follows what the request holds, not how it authenticated. A request that
// presents no key but is granted weighted providers is balanced across them, the same
// candidates, weights and fallbacks a key would have produced.
func TestLoadBalanceProvider_GrantsHeldWithoutAKeyAreBalanced(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", nil)
	held := grant.NewPermit("other", "o1", "Holder", true, false, []schemas.ProviderPermit{{
		Provider:      "groq",
		AllowedModels: []string{"*"},
		KeyIDs:        []string{"key-shared"},
		Weight:        schemas.Ptr(2.0),
	}}, nil)
	p := newLoadBalanceTestPluginWithStore(t, vk, &permitStore{baseOverride: held})

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Model: "llama-3.1-8b-instant"},
	}
	// No key on the request and none passed in: the permits are all there is to balance across.
	ctx := emptyCtx()

	require.NoError(t, p.LoadBalanceProvider(ctx, req))

	provider, model, _ := req.GetRequestFields()
	assert.Equal(t, schemas.ModelProvider("groq"), provider)
	assert.Equal(t, "llama-3.1-8b-instant", model)
}

// And a request carrying nothing is left exactly as it arrived, which is what every pure
// key-based deployment sees for its key-less traffic.
func TestLoadBalanceProvider_NoGrantsLeavesTheRequestAlone(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	})
	p := newLoadBalanceTestPlugin(t, vk)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Model: "gpt-4o"},
	}
	ctx := emptyCtx()

	require.NoError(t, p.LoadBalanceProvider(ctx, req))

	provider, _, _ := req.GetRequestFields()
	assert.Empty(t, provider, "nothing was granted, so nothing was selected")

	// And the trail says so plainly. A request with no access enumerates no provider configs, which
	// is not a misconfiguration and must not read as one: no warning, and the reason it stopped is
	// named.
	var warnings, reasons []string
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if entry.Level == schemas.LogLevelWarn {
			warnings = append(warnings, entry.Message)
		}
		if strings.Contains(entry.Message, "carries no access") {
			reasons = append(reasons, entry.Message)
		}
	}
	assert.Empty(t, warnings, "key-less traffic is the ordinary case, not a warning")
	assert.NotEmpty(t, reasons, "the trail names why load balancing stopped")
}

// excludingStore refuses to fund the named provider, standing in for a deployment that pays for
// candidates from something the load balancer knows nothing about.
type excludingStore struct {
	GovernanceStore
	provider string
	decision Decision
}

func (s *excludingStore) CheckProviderCandidateExclusion(_ *schemas.BifrostContext, _ schemas.Access, candidate schemas.ProviderCandidate, _ string) (Decision, error) {
	if candidate.Provider == s.provider {
		return s.decision, nil
	}
	return DecisionAllow, nil
}

// Load balancing selects among the candidates the store agrees to fund. What a candidate may
// reach and what pays for it are separate questions, and only the second one is the store's, so a
// deployment can fund candidates from limits this algorithm never hears about.
func TestLoadBalanceProvider_StoreDecidesWhichCandidatesCanBeFunded(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
		buildProviderConfig("anthropic", []string{"*"}),
	})
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &mockInMemoryStore{})
	require.NoError(t, err)
	store := &excludingStore{GovernanceStore: local, provider: "openai", decision: DecisionBudgetExceeded}
	p := &GovernancePlugin{
		logger:   logger,
		store:    store,
		resolver: NewBudgetResolver(store, nil, logger, nil),
	}

	got, err := loadBalance(t, p, lbCtx(), "claude-3-5-sonnet")
	require.NoError(t, err)
	assert.Equal(t, "anthropic/claude-3-5-sonnet", got, "the unfunded candidate is not selected")
}

// A routing log is read by whoever made the request, so an excluded candidate is explained in plain
// words rather than by naming the decision that produced it. The store answers with a Decision because
// that is what its other checks answer with; the sentence is this plugin's to write.
func TestCandidateExclusionReason(t *testing.T) {
	assert.Equal(t, "rate limit violated", candidateExclusionReason(DecisionTokenLimited, nil))
	assert.Equal(t, "rate limit violated", candidateExclusionReason(DecisionRequestLimited, nil))
	assert.Equal(t, "budget limit violated", candidateExclusionReason(DecisionBudgetExceeded, nil))

	// A decision that names no limit still has to say something a reader can act on.
	assert.Equal(t, "governance would not fund it", candidateExclusionReason(DecisionAccessBlocked, nil))

	// An error carries its own words, which are better than any this could invent.
	assert.Equal(t, "budget 'team-daily' is exhausted",
		candidateExclusionReason(DecisionAllow, errors.New("budget 'team-daily' is exhausted")))
}

// A model budget scoped to one provider can tell candidates apart, so exclusion consults it and
// routes around exactly that provider. A model budget covering every provider cannot: it excludes
// nobody here, and running out of it is the funnel's refusal to state with a reason.
func TestCandidateExclusionConsultsProviderScopedModelConfigs(t *testing.T) {
	openai := "openai"
	spentOnOpenAI := buildBudgetWithUsage("mc-openai-b", 100.0, 150.0, "1h")
	spentEverywhere := buildBudgetWithUsage("mc-any-b", 100.0, 150.0, "1h")
	pairConfig := buildModelConfig("mc-openai", "gpt-5", &openai, spentOnOpenAI, nil)
	modelWideConfig := buildModelConfig("mc-any", "gpt-5", nil, spentEverywhere, nil)

	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*pairConfig, *modelWideConfig},
		Budgets:      []configstoreTables.TableBudget{*spentOnOpenAI, *spentEverywhere},
	}, nil, nil)
	require.NoError(t, err)

	permit := permitWithProviders(grant.PermitVirtualKey, "vk-1", "Key", "openai", "azure")
	access := grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)

	decision, exclusionErr := store.CheckProviderCandidateExclusion(emptyCtx(), access, schemas.ProviderCandidate{Provider: "openai"}, "gpt-5")
	require.Error(t, exclusionErr, "the exact-pair budget is spent, so openai cannot serve the model")
	assert.NotEqual(t, DecisionAllow, decision)

	decision, exclusionErr = store.CheckProviderCandidateExclusion(emptyCtx(), access, schemas.ProviderCandidate{Provider: "azure"}, "gpt-5")
	require.NoError(t, exclusionErr)
	assert.Equal(t, DecisionAllow, decision,
		"the spent budget covering every provider excludes no candidate; that refusal is the funnel's to state")
}
