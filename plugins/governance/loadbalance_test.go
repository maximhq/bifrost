package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLoadBalanceTestPlugin(t *testing.T, vk *configstoreTables.TableVirtualKey) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
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
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-lb")
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
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-lb")

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
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-lb")

	got, err := loadBalance(t, p, ctx, "meta-llama/llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "groq/meta-llama/llama-3.1-8b-instant", got)
}

// ---------------------------------------------------------------------------
// what load balancing takes from the request's access
// ---------------------------------------------------------------------------

// newLoadBalanceTestPluginWithStore builds a load-balancing plugin over a store that composes
// further grants onto every request, standing in for a deployment that resolves grant holders
// beyond the presented key.
func newLoadBalanceTestPluginWithStore(t *testing.T, vk *configstoreTables.TableVirtualKey, wrap *grantSetStore) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)
	local.inMemoryStore = &mockInMemoryStore{}
	wrap.GovernanceStore = local
	return &GovernancePlugin{
		logger:   logger,
		store:    wrap,
		resolver: NewBudgetResolver(wrap, nil, logger, nil),
	}
}

func lbCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-lb")
	return ctx
}

// A provider the request holds only through a composed grant is a candidate, and carries that
// grant's weight — the whole point of composing grants onto a request rather than beside it.
func TestLoadBalanceProvider_ComposedProviderParticipates(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", nil)
	contributed := &grants.Grant{
		Type: "other", ID: "o1", Name: "Pool",
		ProviderConfigGrants: []grants.ProviderConfigGrant{{
			Provider:      "groq",
			AllowedModels: schemas.WhiteList{"*"},
			KeyIDs:        schemas.WhiteList{"key-shared"},
			Weight:        schemas.Ptr(2.0),
		}},
	}
	p := newLoadBalanceTestPluginWithStore(t, vk, &grantSetStore{
		scoping: contributed,
		mode:    grants.GrantModeUnion,
	})

	got, err := loadBalance(t, p, lbCtx(), "llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "groq/llama-3.1-8b-instant", got, "the composed provider serves the request")
}

// Under intersect, a provider the composed grant does not permit is never a candidate, however
// the key is configured.
func TestLoadBalanceProvider_IntersectRemovedProviderIsNeverACandidate(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("groq", []string{"*"}),
	})
	narrow := grantWithProviders("other", "o1", "Narrow", "openai")
	p := newLoadBalanceTestPluginWithStore(t, vk, &grantSetStore{
		scoping: narrow,
		mode:    grants.GrantModeIntersect,
	})

	got, err := loadBalance(t, p, lbCtx(), "llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "llama-3.1-8b-instant", got, "no provider is eligible, so the model is untouched")
}

// A provider with no weight is not a candidate for selection: composing a grant onto a request
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

// Two configs for one provider stay two candidates — there is no unique constraint on
// (key, provider), and collapsing them would silently change which weights are in play.
func TestLoadBalanceProvider_DuplicateProviderConfigsBothCount(t *testing.T) {
	first := buildProviderConfig("groq", []string{"*"})
	first.Weight = schemas.Ptr(1.0)
	second := buildProviderConfig("groq", []string{"*"})
	second.Weight = schemas.Ptr(1.0)
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK",
		[]configstoreTables.TableVirtualKeyProviderConfig{first, second})

	access := grants.NewEffectiveAccess(vkGrant(vk, nil), nil, "", nil, nil)
	assert.Len(t, access.ProvidersFor("llama-3.1-8b-instant"), 2)
}

// The allowlist is published from the request's access, and a request with no key publishes
// nothing at all — an empty list would mean no provider is permitted and fail-close the request.
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

	t.Run("a request carrying no grants publishes nothing", func(t *testing.T) {
		p := newLoadBalanceTestPlugin(t, vk)
		// No key on the request, and a store that answers only for keys: nothing resolves.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		p.PublishRoutingAllowlist(ctx, "gpt-4o")

		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders),
			"an empty allowlist would mean no provider is permitted")
	})

	// Publishing keys off what the request holds, not off how it authenticated: a store that
	// grants providers to a request presenting no key narrows routing the same way a key does.
	t.Run("grants held without a key are published", func(t *testing.T) {
		p := newLoadBalanceTestPluginWithStore(t, vk, &grantSetStore{
			baseOverride: grantWithProviders("other", "o1", "Holder", "anthropic"),
		})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		p.PublishRoutingAllowlist(ctx, "")

		allowed, ok := ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
		require.True(t, ok)
		assert.Equal(t, []schemas.ModelProvider{"anthropic"}, allowed)
	})

	t.Run("a composed grant widens the allowlist under union", func(t *testing.T) {
		p := newLoadBalanceTestPluginWithStore(t, vk, &grantSetStore{
			scoping: grantWithProviders("other", "o1", "Pool", "anthropic"),
			mode:    grants.GrantModeUnion,
		})
		ctx := lbCtx()

		p.PublishRoutingAllowlist(ctx, "")

		allowed, _ := ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
		assert.ElementsMatch(t, []schemas.ModelProvider{"groq", "openai", "anthropic"}, allowed)
	})
}

// Load balancing follows what the request holds, not how it authenticated. A request that
// presents no key but is granted weighted providers is balanced across them — the same
// candidates, weights and fallbacks a key would have produced.
func TestLoadBalanceProvider_GrantsHeldWithoutAKeyAreBalanced(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", nil)
	held := &grants.Grant{
		Type: "other", ID: "o1", Name: "Holder",
		ProviderConfigGrants: []grants.ProviderConfigGrant{{
			Provider:      "groq",
			AllowedModels: schemas.WhiteList{"*"},
			KeyIDs:        schemas.WhiteList{"key-shared"},
			Weight:        schemas.Ptr(2.0),
		}},
	}
	p := newLoadBalanceTestPluginWithStore(t, vk, &grantSetStore{baseOverride: held})

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Model: "llama-3.1-8b-instant"},
	}
	// No key on the request and none passed in: the grants are all there is to balance across.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

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
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	require.NoError(t, p.LoadBalanceProvider(ctx, req))

	provider, _, _ := req.GetRequestFields()
	assert.Empty(t, provider, "nothing was granted, so nothing was selected")
}

// excludingStore refuses to fund the named provider, standing in for a deployment that pays for
// candidates from something the load balancer knows nothing about.
type excludingStore struct {
	GovernanceStore
	provider string
	decision Decision
}

func (s *excludingStore) CheckProviderCandidateExclusion(_ *schemas.BifrostContext, candidate grants.ProviderCandidate) (Decision, error) {
	if candidate.Provider == s.provider {
		return s.decision, nil
	}
	return DecisionAllow, nil
}

// Load balancing selects among the candidates the store agrees to fund. What a candidate may
// reach and what pays for it are separate questions, and only the second one is the store's — so a
// deployment can fund candidates from limits this algorithm never hears about.
func TestLoadBalanceProvider_StoreDecidesWhichCandidatesCanBeFunded(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
		buildProviderConfig("anthropic", []string{"*"}),
	})
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)
	local.inMemoryStore = &mockInMemoryStore{}
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
