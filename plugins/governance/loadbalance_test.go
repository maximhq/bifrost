package governance

import (
	"context"
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
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
	vkValue := "sk-bf-lb"
	vk, _ := p.store.GetVirtualKey(ctx, vkValue)
	if err := p.LoadBalanceProvider(ctx, req, vk); err != nil {
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

	got, err := loadBalance(t, p, ctx, "meta-llama/llama-3.1-8b-instant")
	require.NoError(t, err)
	assert.Equal(t, "groq/meta-llama/llama-3.1-8b-instant", got)
}

func TestSelectWeightedProviderConfigIgnoresUnusableWeights(t *testing.T) {
	configs := []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "zero", Weight: schemas.Ptr(0.0)},
		{Provider: "negative", Weight: schemas.Ptr(-1.0)},
		{Provider: "nan", Weight: schemas.Ptr(math.NaN())},
		{Provider: "infinity", Weight: schemas.Ptr(math.Inf(1))},
		{Provider: "positive", Weight: schemas.Ptr(1.0)},
	}

	selected, eligible, ok := selectWeightedProviderConfigAt(configs, 0.5)
	require.True(t, ok)
	assert.Equal(t, "positive", selected.Provider)
	require.Len(t, eligible, 1)
	assert.Equal(t, "positive", eligible[0].Provider)
}

func TestSelectWeightedProviderConfigRejectsOnlyUnusableWeights(t *testing.T) {
	configs := []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "unset"},
		{Provider: "zero", Weight: schemas.Ptr(0.0)},
		{Provider: "nan", Weight: schemas.Ptr(math.NaN())},
	}

	_, eligible, ok := selectWeightedProviderConfigAt(configs, 0.5)
	assert.False(t, ok)
	assert.Empty(t, eligible)
}

func TestSelectWeightedProviderConfigPreservesTinyWeights(t *testing.T) {
	configs := []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "normal", Weight: schemas.Ptr(1.0)},
		{Provider: "tiny", Weight: schemas.Ptr(0.001)},
	}

	selected, _, ok := selectWeightedProviderConfigAt(configs, math.Nextafter(1, 0))
	require.True(t, ok)
	assert.Equal(t, "tiny", selected.Provider)
}

func TestSelectWeightedProviderConfigHandlesVeryLargeWeights(t *testing.T) {
	configs := []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "first", Weight: schemas.Ptr(math.MaxFloat64)},
		{Provider: "second", Weight: schemas.Ptr(math.MaxFloat64)},
	}

	selected, _, ok := selectWeightedProviderConfigAt(configs, 0.75)
	require.True(t, ok)
	assert.Equal(t, "second", selected.Provider)
}
