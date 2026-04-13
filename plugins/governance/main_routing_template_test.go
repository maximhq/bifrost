package governance

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyRoutingRules_TargetModelTemplate(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	engine, err := NewRoutingEngine(store, logger)
	require.NoError(t, err)

	plugin := &GovernancePlugin{
		store:  store,
		engine: engine,
		logger: logger,
	}

	// Uses CEL string expression for model transformation
	rule := &configstoreTables.TableRoutingRule{
		ID:            "cel-apply-1",
		Name:          "CEL Model Transform Apply Rule",
		CelExpression: "true",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openrouter"), Model: bifrost.Ptr(`"anthropic/" + model`), Weight: 1.0},
		},
		Enabled:  true,
		Scope:    "global",
		Priority: 0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule))

	body := map[string]any{"model": "claude-sonnet-4-5"}
	req := &schemas.HTTPRequest{Path: "/v1/chat/completions", Headers: map[string]string{}, Query: map[string]string{}}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())

	updatedBody, decision, err := plugin.applyRoutingRules(ctx, req, body, nil)
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "openrouter", decision.Provider)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", decision.Model)
	assert.Equal(t, "openrouter/anthropic/claude-sonnet-4-5", updatedBody["model"])
}

func TestApplyRoutingRules_StaticTargetUnchanged(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	engine, err := NewRoutingEngine(store, logger)
	require.NoError(t, err)

	plugin := &GovernancePlugin{
		store:  store,
		engine: engine,
		logger: logger,
	}

	rule := &configstoreTables.TableRoutingRule{
		ID:            "static-apply-1",
		Name:          "Static Apply Rule",
		CelExpression: "true",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openrouter"), Model: bifrost.Ptr("anthropic/claude-sonnet-4-5"), Weight: 1.0},
		},
		Enabled:  true,
		Scope:    "global",
		Priority: 0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule))

	body := map[string]any{"model": "claude-sonnet-4-5"}
	req := &schemas.HTTPRequest{Path: "/v1/chat/completions", Headers: map[string]string{}, Query: map[string]string{}}
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())

	updatedBody, decision, err := plugin.applyRoutingRules(ctx, req, body, nil)
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "openrouter", decision.Provider)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", decision.Model)
	assert.Equal(t, "openrouter/anthropic/claude-sonnet-4-5", updatedBody["model"])
}
