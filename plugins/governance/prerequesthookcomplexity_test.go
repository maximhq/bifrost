package governance

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestPreRequestHook_ComplexityAnalyzerFeedsCELVariable(t *testing.T) {
	logger := NewMockLogger()
	provider := "openai"
	model := "gpt-4o-mini"

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			RoutingRules: []configstoreTables.TableRoutingRule{
				{
					ID:            "rule-1",
					Name:          "Complexity Available",
					CelExpression: `complexity_tier != ""`,
					Targets: []configstoreTables.TableRoutingTarget{
						{Provider: &provider, Model: &model, Weight: 1.0},
					},
					Enabled:  schemas.Ptr(true),
					Scope:    "global",
					Priority: 0,
				},
			},
		},
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: complexityChatString("What is a vector database?"),
				},
			},
		},
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	engines, ok := bfCtx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	require.True(t, ok, "routing engines used should be tracked")
	require.Contains(t, engines, schemas.RoutingEngineRoutingRule)

	providerOut, modelOut, _ := req.GetRequestFields()
	require.Equal(t, schemas.OpenAI, providerOut)
	require.Equal(t, "gpt-4o-mini", modelOut)
}

func TestPreRequestHook_ComplexitySkippedWhenNoRulesReferenceIt(t *testing.T) {
	logger := NewMockLogger()
	provider := "openai"
	model := "gpt-4o-mini"

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			RoutingRules: []configstoreTables.TableRoutingRule{
				{
					ID:            "rule-1",
					Name:          "Always match",
					CelExpression: "true",
					Targets: []configstoreTables.TableRoutingTarget{
						{Provider: &provider, Model: &model, Weight: 1.0},
					},
					Enabled:  schemas.Ptr(true),
					Scope:    "global",
					Priority: 0,
				},
			},
		},
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: complexityChatString("Hello"),
				},
			},
		},
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	logs := bfCtx.GetRoutingEngineLogs()
	for _, entry := range logs {
		if entry.Engine == schemas.RoutingEngineRoutingRule && strings.Contains(entry.Message, "Complexity") {
			t.Fatalf("expected no complexity logs when no rules reference complexity_tier, got: %s", entry.Message)
		}
	}
}
