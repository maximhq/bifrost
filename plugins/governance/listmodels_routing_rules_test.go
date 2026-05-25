package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestFilterModelsForVirtualKeyAddsAllowedRoutingRuleModels(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKeyWithProviders("vk-1", "sk-bf-test", "test", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4o"}),
	})
	enabled := true
	provider := "openai"
	model := "gpt-4o"
	rule := configstoreTables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "route-deepseek-v4-pro",
		Enabled:       &enabled,
		CelExpression: `model == "deepseek-v4-pro" || model == "deepseek/deepseek-v4-pro"`,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &model, Weight: 1},
		},
		Scope: "global",
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		RoutingRules: []configstoreTables.TableRoutingRule{rule},
	}, nil)
	require.NoError(t, err)

	plugin := &GovernancePlugin{store: store, logger: logger}
	models := plugin.filterModelsForVirtualKey(context.Background(), []schemas.Model{
		{ID: "openai/gpt-4o"},
	}, "sk-bf-test", schemas.OpenAI)

	requireModelIDs(t, models, []string{
		"deepseek-v4-pro",
		"deepseek/deepseek-v4-pro",
		"openai/gpt-4o",
	})
}

func TestFilterModelsForVirtualKeySkipsRoutingRuleWithoutAllowedTarget(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKeyWithProviders("vk-1", "sk-bf-test", "test", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4o-mini"}),
	})
	enabled := true
	provider := "nahcrof"
	model := "deepseek-v4-pro-precision"
	rule := configstoreTables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "route-deepseek-v4-pro",
		Enabled:       &enabled,
		CelExpression: `model in ["deepseek-v4-pro", "deepseek/deepseek-v4-pro"]`,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &model, Weight: 1},
		},
		Scope: "global",
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		RoutingRules: []configstoreTables.TableRoutingRule{rule},
	}, nil)
	require.NoError(t, err)

	plugin := &GovernancePlugin{store: store, logger: logger}
	models := plugin.filterModelsForVirtualKey(context.Background(), []schemas.Model{
		{ID: "openai/gpt-4o-mini"},
	}, "sk-bf-test", schemas.OpenAI)

	requireModelIDs(t, models, []string{"openai/gpt-4o-mini"})
}

func TestFilterModelsForVirtualKeyAddsRoutingRuleModelsOnAllowedFallback(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKeyWithProviders("vk-1", "sk-bf-test", "test", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4o-mini"}),
	})
	enabled := true
	blockedProvider := "anthropic"
	blockedModel := "claude-sonnet-4-6"
	rule := configstoreTables.TableRoutingRule{
		ID:              "rule-1",
		Name:            "route-fallback-only",
		Enabled:         &enabled,
		CelExpression:   `model == "best-model"`,
		ParsedFallbacks: []string{"openai/gpt-4o-mini"},
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &blockedProvider, Model: &blockedModel, Weight: 1},
		},
		Scope: "global",
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		RoutingRules: []configstoreTables.TableRoutingRule{rule},
	}, nil)
	require.NoError(t, err)

	plugin := &GovernancePlugin{store: store, logger: logger}
	models := plugin.filterModelsForVirtualKey(context.Background(), []schemas.Model{
		{ID: "openai/gpt-4o-mini"},
	}, "sk-bf-test", schemas.OpenAI)

	requireModelIDs(t, models, []string{"best-model", "openai/gpt-4o-mini"})
}

func TestFilterModelsForVirtualKeyAddsRoutingRuleModelsOnlyOnFirstAllowedFallback(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKeyWithProviders("vk-1", "sk-bf-test", "test", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4o-mini"}),
		buildProviderConfig("anthropic", []string{"claude-sonnet-4-6"}),
	})
	enabled := true
	blockedProvider := "nahcrof"
	blockedModel := "deepseek-v4-pro-precision"
	rule := configstoreTables.TableRoutingRule{
		ID:              "rule-1",
		Name:            "route-fallback-order",
		Enabled:         &enabled,
		CelExpression:   `model == "best-model"`,
		ParsedFallbacks: []string{"openai/gpt-4o-mini", "anthropic/claude-sonnet-4-6"},
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &blockedProvider, Model: &blockedModel, Weight: 1},
		},
		Scope: "global",
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		RoutingRules: []configstoreTables.TableRoutingRule{rule},
	}, nil)
	require.NoError(t, err)

	plugin := &GovernancePlugin{store: store, logger: logger}
	openAIModels := plugin.filterModelsForVirtualKey(context.Background(), []schemas.Model{
		{ID: "openai/gpt-4o-mini"},
	}, "sk-bf-test", schemas.OpenAI)
	anthropicModels := plugin.filterModelsForVirtualKey(context.Background(), []schemas.Model{
		{ID: "anthropic/claude-sonnet-4-6"},
	}, "sk-bf-test", schemas.Anthropic)

	requireModelIDs(t, openAIModels, []string{"best-model", "openai/gpt-4o-mini"})
	requireModelIDs(t, anthropicModels, []string{"anthropic/claude-sonnet-4-6"})
}

func TestFilterModelsForVirtualKeyDoesNotLeakProviderlessTargetAcrossProviders(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKeyWithProviders("vk-1", "sk-bf-test", "test", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4o"}),
		buildProviderConfig("anthropic", []string{"claude-sonnet-4-6"}),
	})
	enabled := true
	model := "gpt-4o"
	rule := configstoreTables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "route-providerless",
		Enabled:       &enabled,
		CelExpression: `model == "best-model"`,
		Targets: []configstoreTables.TableRoutingTarget{
			{Model: &model, Weight: 1},
		},
		Scope: "global",
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		RoutingRules: []configstoreTables.TableRoutingRule{rule},
	}, nil)
	require.NoError(t, err)

	plugin := &GovernancePlugin{store: store, logger: logger}
	openAIModels := plugin.filterModelsForVirtualKey(context.Background(), []schemas.Model{
		{ID: "openai/gpt-4o"},
	}, "sk-bf-test", schemas.OpenAI)
	anthropicModels := plugin.filterModelsForVirtualKey(context.Background(), []schemas.Model{
		{ID: "anthropic/claude-sonnet-4-6"},
	}, "sk-bf-test", schemas.Anthropic)

	requireModelIDs(t, openAIModels, []string{"best-model", "openai/gpt-4o"})
	requireModelIDs(t, anthropicModels, []string{"anthropic/claude-sonnet-4-6"})
}

func requireModelIDs(t *testing.T, models []schemas.Model, expected []string) {
	t.Helper()
	actual := make([]string, 0, len(models))
	for _, model := range models {
		actual = append(actual, model.ID)
	}
	require.Equal(t, expected, actual)
}
