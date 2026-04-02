package governance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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

func TestHTTPTransportPreHook_ComplexityRouterWithoutRoutingRules(t *testing.T) {
	logger := NewMockLogger()

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			ComplexityRouter: &configstore.ComplexityRouterConfig{
				Enabled: true,
				Models: map[string]string{
					"SIMPLE":    "openai/gpt-4o-mini",
					"MEDIUM":    "openai/gpt-4o",
					"COMPLEX":   "openai/gpt-4o",
					"REASONING": "openai/gpt-4o",
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

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"What is a vector database?"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ChatCompletionRequest)

	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	var payload struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &payload))
	require.Equal(t, "openai/gpt-4o-mini", payload.Model)
}

func TestInitFromStore_UsesStoreComplexityRouterConfig(t *testing.T) {
	logger := NewMockLogger()

	store, err := NewLocalGovernanceStore(
		context.Background(),
		logger,
		nil,
		&configstore.GovernanceConfig{
			ComplexityRouter: &configstore.ComplexityRouterConfig{
				Enabled: true,
				Models: map[string]string{
					"SIMPLE":    "openai/gpt-4o-mini",
					"MEDIUM":    "openai/gpt-4o",
					"COMPLEX":   "openai/gpt-4o",
					"REASONING": "openai/gpt-4o",
				},
			},
		},
		nil,
	)
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
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"What is a vector database?"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ChatCompletionRequest)

	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	var payload struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &payload))
	require.Equal(t, "openai/gpt-4o-mini", payload.Model)
}

func TestHTTPTransportPreHook_ComplexityAnalyzerFeedsCELWhenAutoRouterDisabled(t *testing.T) {
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
					CelExpression: "complexity.available == true",
					Targets: []configstoreTables.TableRoutingTarget{
						{Provider: &provider, Model: &model, Weight: 1.0},
					},
					Enabled:  true,
					Scope:    "global",
					Priority: 0,
				},
			},
			ComplexityRouter: &configstore.ComplexityRouterConfig{
				Enabled: false,
				Models: map[string]string{
					"SIMPLE":    "openai/gpt-4o-mini",
					"MEDIUM":    "openai/gpt-4o",
					"COMPLEX":   "openai/gpt-4o",
					"REASONING": "openai/gpt-4o",
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

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"What is a vector database?"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ChatCompletionRequest)

	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	var payload struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &payload))
	require.Equal(t, "openai/gpt-4o-mini", payload.Model)
}

func TestHTTPTransportPreHook_LargePayloadEvaluatesComplexityWhenConfigured(t *testing.T) {
	logger := NewMockLogger()

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			ComplexityRouter: &configstore.ComplexityRouterConfig{
				Enabled: true,
				Models: map[string]string{
					"SIMPLE":    "openai/gpt-4o-mini",
					"MEDIUM":    "openai/gpt-4o",
					"COMPLEX":   "openai/gpt-4o",
					"REASONING": "openai/gpt-4o",
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

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Content-Type"] = "application/json"

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ChatCompletionRequest)
	bfCtx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	bfCtx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, &schemas.LargePayloadMetadata{
		Model: "openai/gpt-4o",
	})

	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	logs := bfCtx.GetRoutingEngineLogs()
	require.NotEmpty(t, logs)

	foundComplexitySkip := false
	for _, entry := range logs {
		if entry.Engine == schemas.RoutingEngineComplexityRouter &&
			strings.Contains(entry.Message, "Complexity analysis skipped") {
			foundComplexitySkip = true
			break
		}
	}
	require.True(t, foundComplexitySkip, "expected complexity router logs for large payload path")
}
