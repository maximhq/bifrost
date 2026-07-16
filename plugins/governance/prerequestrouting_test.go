package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPreRequestRoutingPlugin(t *testing.T, vk *configstoreTables.TableVirtualKey) *GovernancePlugin {
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

// TestRunPreRequestRouting_ExplicitProviderPrefixSkipsLoadBalancing covers the
// large-payload path: metadata.Model arrives provider-prefixed and unparsed, and
// the explicit prefix must win over VK load balancing even when multiple weighted
// providers allow the model.
func TestRunPreRequestRouting_ExplicitProviderPrefixSkipsLoadBalancing(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
		buildProviderConfig("anthropic", []string{"*"}),
	})
	p := newPreRequestRoutingPlugin(t, vk)

	for range 20 {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		got, err := p.runPreRequestRouting(ctx, vk, false, "openai/gpt-4o", schemas.ChatCompletionRequest)
		require.NoError(t, err)
		assert.Equal(t, "openai/gpt-4o", got)
	}
}

// TestRunPreRequestRouting_UnprefixedModelLoadBalances verifies that a bare model
// string still goes through VK load balancing and comes back provider-prefixed.
func TestRunPreRequestRouting_UnprefixedModelLoadBalances(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	})
	p := newPreRequestRoutingPlugin(t, vk)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	got, err := p.runPreRequestRouting(ctx, vk, false, "gpt-4o", schemas.ChatCompletionRequest)
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-4o", got)
}

// TestRunPreRequestRouting_UnknownPrefixIsTreatedAsModelNamespace verifies that a
// "/" prefix that is not a known provider (e.g. a HuggingFace-style namespace) is
// kept as part of the model name and load balancing still applies.
func TestRunPreRequestRouting_UnknownPrefixIsTreatedAsModelNamespace(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-lb", "LB VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("groq", []string{"*"}),
	})
	p := newPreRequestRoutingPlugin(t, vk)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	got, err := p.runPreRequestRouting(ctx, vk, false, "meta-llama/llama-3.1-8b-instant", schemas.ChatCompletionRequest)
	require.NoError(t, err)
	assert.Equal(t, "groq/meta-llama/llama-3.1-8b-instant", got)
}
