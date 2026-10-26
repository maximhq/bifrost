package openai

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToOpenAIChatRequest_ToolNormalization(t *testing.T) {
	// Create tool parameters with keys in non-alphabetical order:
	// "required" before "properties" before "type" — Normalized() should reorder to
	// type → description → properties → required, then alphabetical.
	unsortedParams := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("zebra", map[string]interface{}{"type": "string"}),
			schemas.KV("alpha", map[string]interface{}{"type": "number"}),
		),
		Required: []string{"zebra"},
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{
					Type: "function",
					Function: &schemas.ChatToolFunction{
						Name:       "test_func",
						Parameters: unsortedParams,
					},
				},
				{
					Type:     "function",
					Function: &schemas.ChatToolFunction{Name: "no_params_func"},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify parameters are normalized: Properties keys should preserve original order
	// (user-defined property names are kept in client order for LLM generation quality)
	normalizedParams := result.ChatParameters.Tools[0].Function.Parameters
	if normalizedParams == nil {
		t.Fatal("expected normalized parameters to be non-nil")
	}
	keys := normalizedParams.Properties.Keys()
	if len(keys) != 2 || keys[0] != "zebra" || keys[1] != "alpha" {
		t.Errorf("expected Properties keys preserved as [zebra, alpha], got %v", keys)
	}

	// Verify tool without parameters is unaffected
	if result.ChatParameters.Tools[1].Function.Parameters != nil {
		t.Error("expected nil parameters for tool without parameters")
	}

	// Verify original bifrostReq.Params.Tools was NOT mutated
	origKeys := bifrostReq.Params.Tools[0].Function.Parameters.Properties.Keys()
	if len(origKeys) != 2 || origKeys[0] != "zebra" || origKeys[1] != "alpha" {
		t.Errorf("original parameters were mutated: expected [zebra, alpha], got %v", origKeys)
	}

	// Verify the Function pointer is a different object (deep copy)
	if result.ChatParameters.Tools[0].Function == bifrostReq.Params.Tools[0].Function {
		t.Error("expected Function pointer to be a copy, not the original")
	}
}

// TestToOpenAIChatRequest_InvalidatesStaleSerializedCache guards the fix for a
// shared MCP tool carrying a precomputed serialized cache. Those tools are
// injected into the request by value, so the cache rides along with the copy.
// ToOpenAIChatRequest replaces Function.Parameters with the normalized form; if
// it left the stale cache in place, ChatTool.MarshalJSON would return the
// pre-normalized bytes and silently defeat the normalization for exactly the
// tools it is meant to help.
func TestToOpenAIChatRequest_InvalidatesStaleSerializedCache(t *testing.T) {
	// Items is a structural schema field Normalized() canonicalizes (unlike
	// Properties, whose order is preserved). Its keys are inserted out of the
	// canonical order (type, description, then alphabetical) so Normalized()
	// reorders them, making the cached (un-normalized) bytes differ from the
	// normalized output.
	params := &schemas.ToolFunctionParameters{
		Type: "array",
		Items: schemas.NewOrderedMapFromPairs(
			schemas.KV("enum", []interface{}{"celsius", "fahrenheit"}),
			schemas.KV("description", "A unit"),
			schemas.KV("type", "string"),
		),
	}

	tool := schemas.ChatTool{
		Type:     "function",
		Function: &schemas.ChatToolFunction{Name: "get_weather", Parameters: params},
	}
	// Simulate the MCP source caching the pre-normalized bytes.
	require.NoError(t, tool.EnsureSerialized())
	staleJSON, err := schemas.Marshal(tool)
	require.NoError(t, err)

	// Expected wire form: the same tool normalized, with no cache.
	expTool := schemas.ChatTool{
		Type:     "function",
		Function: &schemas.ChatToolFunction{Name: "get_weather", Parameters: params.Normalized()},
	}
	expJSON, err := schemas.Marshal(expTool)
	require.NoError(t, err)

	// Setup guard: the case is only meaningful if normalization changes the bytes.
	require.NotEqual(t, string(staleJSON), string(expJSON),
		"test setup: normalized form must differ from the cached form")

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params:   &schemas.ChatParameters{Tools: []schemas.ChatTool{tool}},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, bifrostReq)
	require.NotNil(t, result)

	gotJSON, err := schemas.Marshal(result.ChatParameters.Tools[0])
	require.NoError(t, err)

	require.Equal(t, string(expJSON), string(gotJSON),
		"converted tool must marshal from normalized params, not the stale serialized cache")
	require.NotEqual(t, string(staleJSON), string(gotJSON),
		"converted tool must not emit the pre-normalized cached bytes")
}

// TestToOpenAIChatRequest_DisableStoreContextKey pins that the disable-store context flag forces
// store=false on the converted request, including a passthrough extra_params store, without touching
// the caller's request that fallbacks to other providers share.
func TestToOpenAIChatRequest_DisableStoreContextKey(t *testing.T) {
	newReq := func() *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			Params:   &schemas.ChatParameters{Store: schemas.Ptr(true), ExtraParams: map[string]interface{}{"store": true}},
		}
	}

	t.Run("flag_set_forces_store_false", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyDisableStore, true)
		req := newReq()
		out := ToOpenAIChatRequest(ctx, req)
		require.False(t, *out.Store)
		require.Equal(t, false, out.ExtraParams["store"])
		require.True(t, *req.Params.Store)
		require.Equal(t, true, req.Params.ExtraParams["store"])
	})

	t.Run("flag_set_without_params", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyDisableStore, true)
		req := newReq()
		req.Params = nil
		require.False(t, *ToOpenAIChatRequest(ctx, req).Store)
	})

	t.Run("flag_unset_keeps_store", func(t *testing.T) {
		out := ToOpenAIChatRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), newReq())
		require.True(t, *out.Store)
		require.Equal(t, true, out.ExtraParams["store"])
	})
}

func TestToOpenAIChatRequest_PreservesN(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4.1",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			N: schemas.Ptr(2),
		},
	}

	out := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), req)
	if out == nil {
		t.Fatal("expected request")
	}
	if out.N == nil || *out.N != 2 {
		t.Fatalf("expected n=2, got %#v", out.N)
	}
}

func TestToOpenAIChatRequest_NormalizesReasoningEffort(t *testing.T) {
	// DeepSeek is configured as a custom OpenAI-compatible provider, which registers
	// itself so ParseModelString can strip its prefix from "deepseek/deepseek-v4-pro".
	schemas.RegisterKnownProvider(schemas.ModelProvider("deepseek"))
	defer schemas.UnregisterKnownProvider(schemas.ModelProvider("deepseek"))
	// GLM-5.2 (Z.ai) is also a custom OpenAI-compatible provider.
	schemas.RegisterKnownProvider(schemas.ModelProvider("zai"))
	defer schemas.UnregisterKnownProvider(schemas.ModelProvider("zai"))

	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		effort   string
		expected string
	}{
		{
			name:     "preserves xhigh for gpt-5.4",
			model:    "gpt-5.4",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.2",
			model:    "gpt-5.2",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.3 codex",
			model:    "gpt-5.3-codex",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.5",
			model:    "gpt-5.5",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "maps xhigh to high for gpt-5.1",
			model:    "gpt-5.1",
			effort:   "xhigh",
			expected: "high",
		},
		{
			name:     "maps xhigh to high for gpt-5-pro",
			model:    "gpt-5-pro",
			effort:   "xhigh",
			expected: "high",
		},
		{
			name:     "maps minimal to low for gpt-5.4",
			model:    "gpt-5.4",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "preserves minimal for gpt-5",
			model:    "gpt-5",
			effort:   "minimal",
			expected: "minimal",
		},
		{
			name:     "preserves minimal for gpt-5-mini",
			model:    "gpt-5-mini",
			effort:   "minimal",
			expected: "minimal",
		},
		{
			name:     "preserves minimal for gpt-5-nano",
			model:    "gpt-5-nano",
			effort:   "minimal",
			expected: "minimal",
		},
		{
			name:     "maps minimal to low for gpt-5.2",
			model:    "gpt-5.2",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for gpt-5.6",
			model:    "gpt-5.6",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for o3",
			model:    "o3",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for o1",
			model:    "o1",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for o4",
			model:    "o4",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for gpt-oss",
			model:    "gpt-oss",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps max to xhigh for xhigh-capable model",
			model:    "gpt-5.4",
			effort:   "max",
			expected: "xhigh",
		},
		{
			name:     "maps max to high for model without xhigh",
			model:    "gpt-5.1",
			effort:   "max",
			expected: "high",
		},
		{
			name:     "maps none to low for gpt-6-astra",
			model:    "gpt-6-astra",
			effort:   "none",
			expected: "low",
		},
		{
			name:     "preserves none for gpt-6-sol",
			model:    "gpt-6-sol",
			effort:   "none",
			expected: "none",
		},
		{
			name:     "preserves none for gpt-6-luna",
			model:    "gpt-6-luna",
			effort:   "none",
			expected: "none",
		},
		{
			name:     "maps none to minimal for gpt-5-mini",
			model:    "gpt-5-mini",
			effort:   "none",
			expected: "minimal",
		},
		{
			name:     "maps none to low for o4",
			model:    "o4",
			effort:   "none",
			expected: "low",
		},
		{
			name:     "preserves none for gpt-5.4",
			model:    "gpt-5.4",
			effort:   "none",
			expected: "none",
		},
		{
			name:     "preserves max for deepseek-v4-pro",
			provider: schemas.ModelProvider("deepseek"),
			model:    "deepseek-v4-pro",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for deepseek-v4-flash",
			provider: schemas.ModelProvider("deepseek"),
			model:    "deepseek-v4-flash",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for provider-prefixed deepseek-v4",
			provider: schemas.ModelProvider("deepseek"),
			model:    "deepseek/deepseek-v4-pro",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for glm-5.2",
			provider: schemas.ModelProvider("zai"),
			model:    "glm-5.2",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for provider-prefixed glm-5.2",
			provider: schemas.ModelProvider("zai"),
			model:    "zai/glm-5.2",
			effort:   "max",
			expected: "max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider
			if provider == "" {
				provider = schemas.OpenAI
			}
			req := &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    tt.model,
				Input: []schemas.ChatMessage{{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("hello"),
					},
				}},
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort:    schemas.Ptr(tt.effort),
						MaxTokens: schemas.Ptr(1024),
					},
				},
			}

			out := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), req)
			if out == nil {
				t.Fatal("expected OpenAI chat request")
			}
			if out.Reasoning == nil || out.Reasoning.Effort == nil {
				t.Fatal("expected reasoning effort to be set")
			}
			if got := *out.Reasoning.Effort; got != tt.expected {
				t.Fatalf("expected reasoning effort %q, got %q", tt.expected, got)
			}
			if out.Reasoning.MaxTokens != nil {
				t.Fatalf("expected reasoning max_tokens to be cleared, got %d", *out.Reasoning.MaxTokens)
			}
		})
	}
}

// Vertex Model Garden MaaS models (gpt-oss, Qwen3, kimi-k2-thinking, minimax-m2)
// reject reasoning_effort "none"; only minimal/low/medium/high are accepted. The
// Vertex case should drop a "none" effort for these models while preserving it for
// Mistral on Vertex (which does accept "none").
func TestToOpenAIChatRequest_VertexEndpoint(t *testing.T) {
	for _, model := range []string{"custom-qwen", "mistral-custom"} {
		t.Run(model, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Config: &schemas.AliasConfig{VertexAliasCfg: &schemas.VertexAliasCfg{
				VertexEndpoint: &schemas.VertexEndpoint{EndpointID: "2222", Format: schemas.VertexEndpointOpenAIChat},
			}}})
			request := &schemas.BifrostChatRequest{
				Provider: schemas.Vertex, Model: model,
				Input:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}},
				Params: &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: new("none")}},
			}
			result := ToOpenAIChatRequest(ctx, request)
			require.NotNil(t, result)
			require.Equal(t, model, result.Model)
			require.NotNil(t, result.Reasoning)
			require.Equal(t, "none", *result.Reasoning.Effort)
		})
	}
}

func TestToOpenAIChatRequest_VertexDropsNoneReasoningEffort(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		keepsEffort bool
	}{
		{
			name:        "MaaS model drops none effort",
			model:       "moonshotai/kimi-k2-thinking-maas",
			keepsEffort: false,
		},
		{
			name:        "minimax MaaS model drops none effort",
			model:       "minimaxai/minimax-m2-maas",
			keepsEffort: false,
		},
		{
			name:        "Mistral on Vertex keeps none effort",
			model:       "mistral-large",
			keepsEffort: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &schemas.BifrostChatRequest{
				Provider: schemas.Vertex,
				Model:    tt.model,
				Input: []schemas.ChatMessage{{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("hello"),
					},
				}},
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("none"),
					},
				},
			}

			out := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), req)
			if out == nil {
				t.Fatal("expected OpenAI chat request")
			}

			if tt.keepsEffort {
				if out.Reasoning == nil || out.Reasoning.Effort == nil || *out.Reasoning.Effort != "none" {
					t.Fatalf("expected reasoning effort to be preserved as \"none\", got %+v", out.Reasoning)
				}
				return
			}

			// Effort must be dropped so reasoning_effort is omitted from the payload.
			if out.Reasoning != nil && out.Reasoning.Effort != nil {
				t.Fatalf("expected reasoning effort to be dropped, got %q", *out.Reasoning.Effort)
			}

			// Verify the marshalled body does not contain reasoning_effort.
			body, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("failed to marshal request: %v", err)
			}
			if strings.Contains(string(body), "reasoning_effort") {
				t.Fatalf("expected marshalled body to omit reasoning_effort, got %s", string(body))
			}
		})
	}
}

// TestToOpenAIChatRequest_StripsUnsupportedSamplingParams pins that sampling fields
// OpenAI rejects at the effective reasoning effort are dropped for OpenAI and Azure.
func TestToOpenAIChatRequest_StripsUnsupportedSamplingParams(t *testing.T) {
	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		effort   string
		stripped bool
	}{
		{name: "gpt-6-astra always strips", model: "gpt-6-astra", stripped: true},
		{name: "azure gpt-6-astra always strips", provider: schemas.Azure, model: "gpt-6-astra", effort: "low", stripped: true},
		{name: "o3 strips", model: "o3", effort: "high", stripped: true},
		{name: "gpt-5.5 omitted effort defaults to medium", model: "gpt-5.5", stripped: true},
		{name: "gpt-5.4 omitted effort defaults to none", model: "gpt-5.4", stripped: false},
		{name: "gpt-5.6 keeps while effort none", model: "gpt-5.6", effort: "none", stripped: false},
		{name: "gpt-5.6 strips once reasoning is on", model: "gpt-5.6", effort: "low", stripped: true},
		{name: "non-reasoning model keeps", model: "gpt-4o", stripped: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider
			if provider == "" {
				provider = schemas.OpenAI
			}
			params := &schemas.ChatParameters{
				Temperature: schemas.Ptr(0.2),
				TopP:        schemas.Ptr(0.9),
				LogProbs:    new(true),
				TopLogProbs: schemas.Ptr(3),
			}
			if tt.effort != "" {
				params.Reasoning = &schemas.ChatReasoning{Effort: schemas.Ptr(tt.effort)}
			}
			out := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    tt.model,
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
				Params: params,
			})
			require.NotNil(t, out)
			if tt.stripped {
				require.Nil(t, out.Temperature)
				require.Nil(t, out.TopP)
				require.Nil(t, out.LogProbs)
				require.Nil(t, out.TopLogProbs)
			} else {
				require.NotNil(t, out.Temperature)
				require.NotNil(t, out.TopP)
				require.NotNil(t, out.LogProbs)
				require.NotNil(t, out.TopLogProbs)
			}
			require.NotNil(t, params.Temperature, "caller's params must not be mutated")
		})
	}
}

func TestOpenAIChatRequest_FilterOpenAISpecificParameters_NormalizesReasoningEffort(t *testing.T) {
	// Register the custom "deepseek" provider so ParseModelString strips its prefix.
	schemas.RegisterKnownProvider(schemas.ModelProvider("deepseek"))
	defer schemas.UnregisterKnownProvider(schemas.ModelProvider("deepseek"))
	// GLM-5.2 (Z.ai) is also a custom OpenAI-compatible provider.
	schemas.RegisterKnownProvider(schemas.ModelProvider("zai"))
	defer schemas.UnregisterKnownProvider(schemas.ModelProvider("zai"))

	tests := []struct {
		name     string
		model    string
		effort   string
		expected string
	}{
		{
			name:     "preserves xhigh for gpt-5.4",
			model:    "gpt-5.4",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.2 pro",
			model:    "gpt-5.2-pro",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.2 codex",
			model:    "gpt-5.2-codex",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.5",
			model:    "gpt-5.5",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "maps xhigh to high for gpt-5.1",
			model:    "gpt-5.1",
			effort:   "xhigh",
			expected: "high",
		},
		{
			name:     "maps xhigh to high for gpt-5-pro",
			model:    "gpt-5-pro",
			effort:   "xhigh",
			expected: "high",
		},
		{
			name:     "maps minimal to low for gpt-5.4",
			model:    "gpt-5.4",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "preserves minimal for gpt-5",
			model:    "gpt-5",
			effort:   "minimal",
			expected: "minimal",
		},
		{
			name:     "preserves minimal for gpt-5-mini",
			model:    "gpt-5-mini",
			effort:   "minimal",
			expected: "minimal",
		},
		{
			name:     "preserves minimal for gpt-5-nano",
			model:    "gpt-5-nano",
			effort:   "minimal",
			expected: "minimal",
		},
		{
			name:     "maps minimal to low for gpt-5.2",
			model:    "gpt-5.2",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for gpt-5.6",
			model:    "gpt-5.6",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for o3",
			model:    "o3",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for o1",
			model:    "o1",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for o4",
			model:    "o4",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps minimal to low for gpt-oss",
			model:    "gpt-oss",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps max to xhigh for xhigh-capable model",
			model:    "gpt-5.4",
			effort:   "max",
			expected: "xhigh",
		},
		{
			name:     "maps max to high for model without xhigh",
			model:    "gpt-5.1",
			effort:   "max",
			expected: "high",
		},
		{
			name:     "preserves max for deepseek-v4-pro",
			model:    "deepseek-v4-pro",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for deepseek-v4-flash",
			model:    "deepseek-v4-flash",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for provider-prefixed deepseek-v4",
			model:    "deepseek/deepseek-v4-pro",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for glm-5.2",
			model:    "glm-5.2",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for provider-prefixed glm-5.2",
			model:    "zai/glm-5.2",
			effort:   "max",
			expected: "max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &OpenAIChatRequest{
				Model: tt.model,
				ChatParameters: schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort:    schemas.Ptr(tt.effort),
						MaxTokens: schemas.Ptr(1024),
					},
				},
			}

			req.filterOpenAISpecificParameters(schemas.ResolveModelCaps(schemas.OpenAI, req.Model))

			if req.Reasoning == nil || req.Reasoning.Effort == nil {
				t.Fatal("expected reasoning effort to be set")
			}
			if got := *req.Reasoning.Effort; got != tt.expected {
				t.Fatalf("expected reasoning effort %q, got %q", tt.expected, got)
			}
			if req.Reasoning.MaxTokens != nil {
				t.Fatalf("expected reasoning max_tokens to be cleared, got %d", *req.Reasoning.MaxTokens)
			}
		})
	}
}

func TestToOpenAIChatRequest_PreservesPropertyOrder(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("reasoning", map[string]interface{}{"type": "string", "description": "Step by step"}),
			schemas.KV("answer", map[string]interface{}{"type": "string", "description": "Final answer"}),
			schemas.KV("confidence", map[string]interface{}{"type": "number", "description": "Score"}),
		),
		Required: []string{"reasoning", "answer"},
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type:     "function",
				Function: &schemas.ChatToolFunction{Name: "test_func", Parameters: params},
			}},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, bifrostReq)

	// CoT: property order preserved
	normalizedParams := result.ChatParameters.Tools[0].Function.Parameters
	keys := normalizedParams.Properties.Keys()
	if len(keys) != 3 || keys[0] != "reasoning" || keys[1] != "answer" || keys[2] != "confidence" {
		t.Errorf("expected property order [reasoning, answer, confidence], got %v", keys)
	}
}

func TestToOpenAIChatRequest_PreservesExplicitEmptyToolParameters(t *testing.T) {
	var tool schemas.ChatTool
	err := json.Unmarshal([]byte(`{"type":"function","function":{"name":"empty_schema","parameters":{},"strict":false}}`), &tool)
	if err != nil {
		t.Fatalf("failed to unmarshal tool: %v", err)
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{tool},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	params := result.ChatParameters.Tools[0].Function.Parameters
	if params == nil {
		t.Fatal("expected tool parameters to be preserved")
	}

	marshaled, err := schemas.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal parameters: %v", err)
	}
	if string(marshaled) != `{}` {
		t.Fatalf("expected parameters to remain {}, got %s", marshaled)
	}
}

func TestToOpenAIChatRequest_CachingDeterminism(t *testing.T) {
	// Same properties, different structural key orders within property definitions
	makeReq := func(props *schemas.OrderedMap) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{{
					Type: "function",
					Function: &schemas.ChatToolFunction{
						Name:       "test",
						Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: props},
					},
				}},
			},
		}
	}

	// Version A: type before description
	propsA := schemas.NewOrderedMapFromPairs(
		schemas.KV("reasoning", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "Step by step"),
		)),
		schemas.KV("answer", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "Final answer"),
		)),
	)

	// Version B: description before type (different structural order)
	propsB := schemas.NewOrderedMapFromPairs(
		schemas.KV("reasoning", schemas.NewOrderedMapFromPairs(
			schemas.KV("description", "Step by step"),
			schemas.KV("type", "string"),
		)),
		schemas.KV("answer", schemas.NewOrderedMapFromPairs(
			schemas.KV("description", "Final answer"),
			schemas.KV("type", "string"),
		)),
	)

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	resultA := ToOpenAIChatRequest(ctx, makeReq(propsA))
	resultB := ToOpenAIChatRequest(ctx, makeReq(propsB))

	jsonA, err := schemas.Marshal(resultA.ChatParameters.Tools[0].Function.Parameters)
	if err != nil {
		t.Fatalf("failed to marshal params A: %v", err)
	}
	jsonB, err := schemas.Marshal(resultB.ChatParameters.Tools[0].Function.Parameters)
	if err != nil {
		t.Fatalf("failed to marshal params B: %v", err)
	}

	if string(jsonA) != string(jsonB) {
		t.Errorf("caching broken: same schema produced different JSON\nA: %s\nB: %s", jsonA, jsonB)
	}
}

func TestToOpenAIChatRequest_PromptCacheOptions(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	mode := "explicit"
	ttl := "30m"
	userContent := "hello"
	mkReq := func(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    model,
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &userContent},
			}},
			Params: &schemas.ChatParameters{
				PromptCacheOptions: &schemas.PromptCacheOptions{Mode: &mode, TTL: &ttl},
			},
		}
	}

	// OpenAI keeps the OpenAI-native field.
	openai := ToOpenAIChatRequest(ctx, mkReq(schemas.OpenAI, "gpt-5.6"))
	if openai == nil || openai.ChatParameters.PromptCacheOptions == nil {
		t.Fatal("expected prompt_cache_options preserved for OpenAI")
	}
	if *openai.ChatParameters.PromptCacheOptions.Mode != mode || *openai.ChatParameters.PromptCacheOptions.TTL != ttl {
		t.Fatalf("unexpected options: %#v", openai.ChatParameters.PromptCacheOptions)
	}

	// A non-OpenAI OpenAI-compatible provider strips it.
	fw := ToOpenAIChatRequest(ctx, mkReq(schemas.Fireworks, "accounts/fireworks/models/deepseek-v3p2"))
	if fw == nil || fw.ChatParameters.PromptCacheOptions != nil {
		t.Fatalf("expected prompt_cache_options stripped for Fireworks, got %#v", fw.ChatParameters.PromptCacheOptions)
	}
}

func TestToOpenAIChatRequest_FireworksPreservesReasoningAndCacheIsolation(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	cacheKey := "cache-key-1"
	reasoning := "step by step"
	predictionContent := "fireworks ok"
	userContent := "Reply with exactly: fireworks ok"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Fireworks,
		Model:    "accounts/fireworks/models/deepseek-v3p2",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &userContent,
				},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: &predictionContent,
				},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &reasoning,
				},
			},
		},
		Params: &schemas.ChatParameters{
			PromptCacheKey: &cacheKey,
			Prediction: &schemas.ChatPrediction{
				Type:    "content",
				Content: predictionContent,
			},
		},
	}

	result := ToOpenAIChatRequest(ctx, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.PromptCacheIsolationKey == nil || *result.PromptCacheIsolationKey != cacheKey {
		t.Fatalf("expected prompt_cache_isolation_key %q, got %v", cacheKey, result.PromptCacheIsolationKey)
	}
	if result.PromptCacheKey != nil {
		t.Fatalf("expected prompt_cache_key to be stripped, got %v", *result.PromptCacheKey)
	}
	if result.Prediction == nil || result.Prediction.Content != predictionContent {
		t.Fatalf("expected prediction to be preserved, got %#v", result.Prediction)
	}
	if len(result.Messages) != 2 || result.Messages[1].OpenAIChatAssistantMessage == nil {
		t.Fatalf("expected assistant message with OpenAI assistant payload, got %#v", result.Messages)
	}
	if result.Messages[1].OpenAIChatAssistantMessage.Reasoning == nil || *result.Messages[1].OpenAIChatAssistantMessage.Reasoning != reasoning {
		t.Fatalf("expected assistant reasoning_content %q, got %#v", reasoning, result.Messages[1].OpenAIChatAssistantMessage)
	}

	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToOpenAIChatRequest(ctx, bifrostReq), nil
		},
	)
	if bifrostErr != nil {
		t.Fatalf("failed to build request body: %v", bifrostErr.Error.Message)
	}

	var jsonMap map[string]interface{}
	if err := sonic.Unmarshal(wireBody, &jsonMap); err != nil {
		t.Fatalf("failed to parse marshaled request body: %v", err)
	}
	if got, ok := jsonMap["prompt_cache_isolation_key"].(string); !ok || got != cacheKey {
		t.Fatalf("expected prompt_cache_isolation_key %q in wire payload, got %#v", cacheKey, jsonMap["prompt_cache_isolation_key"])
	}
	if _, ok := jsonMap["prompt_cache_key"]; ok {
		t.Fatalf("expected prompt_cache_key to be absent from wire payload, got %#v", jsonMap["prompt_cache_key"])
	}

	messages, ok := jsonMap["messages"].([]interface{})
	if !ok || len(messages) != 2 {
		t.Fatalf("expected 2 messages in wire payload, got %#v", jsonMap["messages"])
	}
	assistantMessage, ok := messages[1].(map[string]interface{})
	if !ok {
		t.Fatalf("expected assistant message object, got %#v", messages[1])
	}
	if got, ok := assistantMessage["reasoning_content"].(string); !ok || got != reasoning {
		t.Fatalf("expected reasoning_content %q in assistant payload, got %#v", reasoning, assistantMessage["reasoning_content"])
	}
}

func TestToOpenAIChatRequest_StripsAssistantReasoningContentForCompatibleProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
	}{
		{name: "deepseek", provider: schemas.DeepSeek, model: "deepseek-v4-pro"},
		{name: "mistral", provider: schemas.Mistral, model: "zai-glm-5-3"},
		{name: "vertex mistral", provider: schemas.Vertex, model: "mistral-medium-3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
			defer cancel()

			reasoning := "step by step"
			assistantContent := "The weather in Paris is mild today."
			userContent := "What is the weather in Paris?"

			bifrostReq := &schemas.BifrostChatRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input: []schemas.ChatMessage{
					{
						Role:    schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{ContentStr: &userContent},
					},
					{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: &assistantContent},
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							Reasoning: &reasoning,
						},
					},
				},
			}

			result := ToOpenAIChatRequest(ctx, bifrostReq)
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if len(result.Messages) != 2 || result.Messages[1].OpenAIChatAssistantMessage == nil {
				t.Fatalf("expected assistant message with OpenAI assistant payload, got %#v", result.Messages)
			}
			if result.Messages[1].OpenAIChatAssistantMessage.Reasoning != nil {
				t.Fatalf("expected assistant reasoning_content to be stripped for %s, got %#v", tt.provider, result.Messages[1].OpenAIChatAssistantMessage.Reasoning)
			}

			ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
			wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
				ctx,
				bifrostReq,
				func() (providerUtils.RequestBodyWithExtraParams, error) {
					return ToOpenAIChatRequest(ctx, bifrostReq), nil
				},
			)
			if bifrostErr != nil {
				t.Fatalf("failed to build request body: %v", bifrostErr.Error.Message)
			}

			var jsonMap map[string]any
			if err := sonic.Unmarshal(wireBody, &jsonMap); err != nil {
				t.Fatalf("failed to parse marshaled request body: %v", err)
			}

			messages, ok := jsonMap["messages"].([]any)
			if !ok || len(messages) != 2 {
				t.Fatalf("expected 2 messages in wire payload, got %#v", jsonMap["messages"])
			}
			assistantMessage, ok := messages[1].(map[string]any)
			if !ok {
				t.Fatalf("expected assistant message object, got %#v", messages[1])
			}
			if _, ok := assistantMessage["reasoning_content"]; ok {
				t.Fatalf("expected reasoning_content to be absent from %s assistant payload, got %#v", tt.provider, assistantMessage["reasoning_content"])
			}
		})
	}
}

// Groq rejects reasoning_content on assistant messages ("property 'reasoning_content'
// is unsupported") but accepts the OpenRouter-style "reasoning" spelling, so replayed
// reasoning has to move to that key rather than be dropped.
func TestToOpenAIChatRequest_MovesAssistantReasoningToAliasForGroq(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider schemas.ModelProvider
		model    string
	}{
		{name: "groq", provider: schemas.Groq, model: "qwen/qwen3.8-27b"},
		{name: "cerebras", provider: schemas.Cerebras, model: "gpt-oss-120b"},
	} {
		t.Run(tt.name, func(t *testing.T) { assertMovesAssistantReasoningToAlias(t, tt.provider, tt.model) })
	}
}

func assertMovesAssistantReasoningToAlias(t *testing.T, provider schemas.ModelProvider, model string) {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	reasoning := "step by step"
	assistantContent := "The weather in Paris is mild today."
	userContent := "What is the weather in Paris?"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &userContent},
			},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &assistantContent},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &reasoning,
				},
			},
		},
	}

	result := ToOpenAIChatRequest(ctx, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Messages) != 2 || result.Messages[1].OpenAIChatAssistantMessage == nil {
		t.Fatalf("expected assistant message with OpenAI assistant payload, got %#v", result.Messages)
	}
	assistant := result.Messages[1].OpenAIChatAssistantMessage
	if assistant.Reasoning != nil {
		t.Fatalf("expected reasoning_content to be cleared, got %#v", assistant.Reasoning)
	}
	if assistant.ReasoningAlias == nil || *assistant.ReasoningAlias != reasoning {
		t.Fatalf("expected reasoning alias %q, got %#v", reasoning, assistant.ReasoningAlias)
	}

	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToOpenAIChatRequest(ctx, bifrostReq), nil
		},
	)
	if bifrostErr != nil {
		t.Fatalf("failed to build request body: %v", bifrostErr.Error.Message)
	}

	var jsonMap map[string]any
	if err := sonic.Unmarshal(wireBody, &jsonMap); err != nil {
		t.Fatalf("failed to parse marshaled request body: %v", err)
	}
	messages, ok := jsonMap["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("expected 2 messages in wire payload, got %#v", jsonMap["messages"])
	}
	assistantMessage, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("expected assistant message object, got %#v", messages[1])
	}
	if _, ok := assistantMessage["reasoning_content"]; ok {
		t.Fatalf("expected reasoning_content to be absent from %s assistant payload, got %#v", provider, assistantMessage["reasoning_content"])
	}
	if got, ok := assistantMessage["reasoning"].(string); !ok || got != reasoning {
		t.Fatalf("expected reasoning %q in %s assistant payload, got %#v", reasoning, provider, assistantMessage["reasoning"])
	}
}

// TestToOpenAIChatRequest_AnnotationsNotInWirePayload verifies that MCPToolAnnotations
// (stored on ChatTool with json:"-") are never included in the JSON body sent to OpenAI.
func TestToOpenAIChatRequest_AnnotationsNotInWirePayload(t *testing.T) {
	readOnly := true

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "read_file",
						Description: schemas.Ptr("Read a file"),
						Parameters: &schemas.ToolFunctionParameters{
							Type: "object",
							Properties: schemas.NewOrderedMapFromPairs(
								schemas.KV("path", map[string]interface{}{"type": "string"}),
							),
							Required: []string{"path"},
						},
					},
					Annotations: &schemas.MCPToolAnnotations{
						Title:        "File Reader",
						ReadOnlyHint: &readOnly,
					},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	result := ToOpenAIChatRequest(ctx, bifrostReq)
	require.NotNil(t, result)

	wireBody, err := json.Marshal(result)
	require.NoError(t, err)
	s := string(wireBody)

	// Annotations must be absent from the wire payload
	if strings.Contains(s, "annotations") {
		t.Errorf("annotations field leaked into OpenAI wire payload: %s", s)
	}
	if strings.Contains(s, "readOnlyHint") {
		t.Errorf("readOnlyHint leaked into OpenAI wire payload: %s", s)
	}
	if strings.Contains(s, "File Reader") {
		t.Errorf("annotation title leaked into OpenAI wire payload: %s", s)
	}

	// The function definition must still be intact
	if !strings.Contains(s, "read_file") {
		t.Errorf("function name missing from OpenAI wire payload: %s", s)
	}
}

func TestApplyXAICompatibility(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		request  *OpenAIChatRequest
		validate func(t *testing.T, req *OpenAIChatRequest)
	}{
		{
			name:  "grok-3: preserves frequency_penalty and stop, clears presence_penalty and reasoning_effort",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// frequency_penalty should be preserved
				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}

				// stop should be preserved
				if len(req.Stop) != 1 || req.Stop[0] != "STOP" {
					t.Errorf("Expected Stop to be preserved as ['STOP'], got %v", req.Stop)
				}

				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// reasoning_effort should be cleared for non-mini grok-3
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil) for grok-3, got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-3-mini: clears all penalties and stop, preserves reasoning_effort",
			model: "grok-3-mini",
			request: &OpenAIChatRequest{
				Model:    "grok-3-mini",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("medium"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared for grok-3-mini
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil) for grok-3-mini, got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared for grok-3-mini
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil) for grok-3-mini, got %v", req.Stop)
				}

				// reasoning_effort should be preserved for grok-3-mini
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved for grok-3-mini")
				}
				if *req.Reasoning.Effort != "medium" {
					t.Errorf("Expected Reasoning.Effort to be 'medium', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4: clears all penalties, stop, and reasoning_effort",
			model: "grok-4",
			request: &OpenAIChatRequest{
				Model:    "grok-4",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared for grok-4
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil) for grok-4, got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared for grok-4
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil) for grok-4, got %v", req.Stop)
				}

				// reasoning_effort should be cleared for grok-4
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil) for grok-4, got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4-fast-reasoning: clears all penalties, stop, and reasoning_effort",
			model: "grok-4-fast-reasoning",
			request: &OpenAIChatRequest{
				Model:    "grok-4-fast-reasoning",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP", "END"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil), got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil), got %v", req.Stop)
				}

				// reasoning_effort should be cleared
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil), got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-code-fast-1: clears all penalties, stop, and reasoning_effort",
			model: "grok-code-fast-1",
			request: &OpenAIChatRequest{
				Model:    "grok-code-fast-1",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.2),
					PresencePenalty:  schemas.Ptr(0.1),
					Stop:             []string{"END"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("low"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil), got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil), got %v", req.Stop)
				}

				// reasoning_effort should be cleared
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil), got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "non-reasoning grok model: no changes applied",
			model: "grok-2-latest",
			request: &OpenAIChatRequest{
				Model:    "grok-2-latest",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// All parameters should be preserved for non-reasoning models
				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}

				if req.PresencePenalty == nil || *req.PresencePenalty != 0.3 {
					t.Errorf("Expected PresencePenalty to be preserved at 0.3, got %v", req.PresencePenalty)
				}

				if len(req.Stop) != 1 || req.Stop[0] != "STOP" {
					t.Errorf("Expected Stop to be preserved as ['STOP'], got %v", req.Stop)
				}

				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved")
				}
				if *req.Reasoning.Effort != "high" {
					t.Errorf("Expected Reasoning.Effort to be 'high', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-3: handles nil reasoning gracefully",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning:        nil,
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Should handle nil reasoning without panicking
				if req.Reasoning != nil {
					t.Errorf("Expected Reasoning to remain nil, got %v", req.Reasoning)
				}

				// Other parameters should still be processed
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}
			},
		},
		{
			name:  "grok-4.6: preserves reasoning_effort xhigh",
			model: "grok-4.6",
			request: &OpenAIChatRequest{
				Model:    "grok-4.6",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("xhigh"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved for grok-4.6")
				}
				if *req.Reasoning.Effort != "xhigh" {
					t.Errorf("Expected Reasoning.Effort 'xhigh', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4.5: preserves reasoning_effort",
			model: "grok-4.5",
			request: &OpenAIChatRequest{
				Model:    "grok-4.5",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("low"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved for grok-4.5")
				}
				if *req.Reasoning.Effort != "low" {
					t.Errorf("Expected Reasoning.Effort 'low', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4.3: preserves reasoning_effort",
			model: "grok-4.3",
			request: &OpenAIChatRequest{
				Model:    "grok-4.3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("medium"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved for grok-4.3")
				}
				if *req.Reasoning.Effort != "medium" {
					t.Errorf("Expected Reasoning.Effort 'medium', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4.20-multi-agent-0309: preserves reasoning_effort (agent count)",
			model: "grok-4.20-multi-agent-0309",
			request: &OpenAIChatRequest{
				Model:    "grok-4.20-multi-agent-0309",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved for grok-4.20-multi-agent")
				}
				if *req.Reasoning.Effort != "high" {
					t.Errorf("Expected Reasoning.Effort 'high', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4-0709: dated grok-4 alias still clears reasoning_effort",
			model: "grok-4-0709",
			request: &OpenAIChatRequest{
				Model:    "grok-4-0709",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort cleared for grok-4-0709, got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-3: preserves other parameters like temperature",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Temperature:      schemas.Ptr(0.8),
					TopP:             schemas.Ptr(0.9),
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Unrelated parameters should be preserved
				if req.Temperature == nil || *req.Temperature != 0.8 {
					t.Errorf("Expected Temperature to be preserved at 0.8, got %v", req.Temperature)
				}

				if req.TopP == nil || *req.TopP != 0.9 {
					t.Errorf("Expected TopP to be preserved at 0.9, got %v", req.TopP)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply the compatibility function
			tt.request.applyXAICompatibility(schemas.ResolveModelCaps(schemas.XAI, tt.model))

			// Validate the results
			tt.validate(t, tt.request)
		})
	}
}

// TestToOpenAIChatRequest_CacheControl_OpenRouterOnly verifies that
// Anthropic-style cache_control breakpoints on message content blocks and on
// tools survive marshalling only when the originating provider is OpenRouter
// (which forwards them to the underlying Claude/Gemini model). For OpenAI and
// other OpenAI-format providers, cache_control is still stripped.
func TestToOpenAIChatRequest_CacheControl_OpenRouterOnly(t *testing.T) {
	makeReq := func(provider schemas.ModelProvider) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    "anthropic/claude-opus-4",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleSystem,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type:         schemas.ChatContentBlockTypeText,
								Text:         schemas.Ptr("long cacheable system prompt"),
								CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
							},
						},
					},
				},
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
			},
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{
					{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:        "lookup",
							Description: schemas.Ptr("lookup something"),
							Parameters: &schemas.ToolFunctionParameters{
								Type: "object",
								Properties: schemas.NewOrderedMapFromPairs(
									schemas.KV("q", map[string]interface{}{"type": "string"}),
								),
							},
						},
						CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		provider schemas.ModelProvider
		wantKept bool
	}{
		{name: "openrouter preserves cache_control", provider: schemas.OpenRouter, wantKept: true},
		{name: "openai strips cache_control", provider: schemas.OpenAI, wantKept: false},
		{name: "gemini strips cache_control", provider: schemas.Gemini, wantKept: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
			defer cancel()

			result := ToOpenAIChatRequest(ctx, makeReq(tt.provider))
			require.NotNil(t, result)

			wireBody, err := json.Marshal(result)
			require.NoError(t, err)
			s := string(wireBody)

			if tt.wantKept {
				require.Contains(t, s, "cache_control", "cache_control must be preserved for OpenRouter: %s", s)
				// Both the content-block breakpoint and the tool breakpoint must survive.
				require.Equal(t, 2, strings.Count(s, "cache_control"), "expected cache_control on both content block and tool: %s", s)
			} else {
				require.NotContains(t, s, "cache_control", "cache_control must be stripped for %s: %s", tt.provider, s)
			}

			// The tool identity must always survive regardless of stripping.
			require.Contains(t, s, "lookup")
		})
	}
}

// TestOpenAIInbound_ServerToolNameSurvives is a diagnostic probe for the Bedrock
// managed-tool harness 400s. It replicates the transport inbound path
// (sonic.Unmarshal of the raw body into *OpenAIChatRequest, then
// ToBifrostChatRequest) and asserts the top-level server-tool name survives.
func TestOpenAIInbound_ServerToolNameSurvives(t *testing.T) {
	body := `{"model":"bedrock/global.anthropic.claude-sonnet-4-6","max_tokens":8000,"tools":[{"type":"bash_20250124","name":"bash"}],"messages":[{"role":"user","content":"Run ls"}]}`

	var req OpenAIChatRequest
	if err := sonic.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	t.Logf("after Unmarshal: tools=%+v", req.ChatParameters.Tools)
	if len(req.ChatParameters.Tools) != 1 || req.ChatParameters.Tools[0].Name != "bash" {
		t.Fatalf("PARSE dropped name: %+v", req.ChatParameters.Tools)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifReq := req.ToBifrostChatRequest(ctx)
	if bifReq.Params == nil || len(bifReq.Params.Tools) != 1 || bifReq.Params.Tools[0].Name != "bash" {
		t.Fatalf("ToBifrostChatRequest dropped name: %+v", bifReq.Params)
	}
	if bifReq.Params.MaxCompletionTokens == nil || *bifReq.Params.MaxCompletionTokens != 8000 {
		t.Fatalf("ToBifrostChatRequest did not map max_tokens to max_completion_tokens: %+v", bifReq.Params.MaxCompletionTokens)
	}
}

func TestOpenAIInbound_MaxCompletionTokensTakesPriorityOverMaxTokens(t *testing.T) {
	body := `{"model":"bedrock/global.anthropic.claude-sonnet-4-6","max_tokens":100,"max_completion_tokens":200,"messages":[{"role":"user","content":"Run ls"}]}`

	var req OpenAIChatRequest
	if err := sonic.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifReq := req.ToBifrostChatRequest(ctx)
	if bifReq.Params == nil || bifReq.Params.MaxCompletionTokens == nil {
		t.Fatalf("ToBifrostChatRequest dropped max_completion_tokens: %+v", bifReq.Params)
	}
	if *bifReq.Params.MaxCompletionTokens != 200 {
		t.Fatalf("max_completion_tokens should take priority over max_tokens, got %d", *bifReq.Params.MaxCompletionTokens)
	}
}

func TestToOpenAIChatRequest_LegacyMaxTokensProvidersUseMaxTokensOnWire(t *testing.T) {
	tests := []struct {
		name     string
		provider schemas.ModelProvider
	}{
		{name: "Go", provider: schemas.OpencodeGo},
		{name: "Zen", provider: schemas.OpencodeZen},
		{name: "Ollama", provider: schemas.Ollama},
		{name: "DeepSeek", provider: schemas.DeepSeek},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: tt.provider,
				Model:    "glm-5.3",
				Input: []schemas.ChatMessage{{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("hello"),
					},
				}},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: schemas.Ptr(512),
				},
			}

			converted := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), bifrostReq)
			require.NotNil(t, converted)

			wireJSON, err := json.Marshal(converted)
			require.NoError(t, err)

			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(wireJSON, &wire))
			require.Contains(t, wire, "max_tokens")
			var maxTokens int
			require.NoError(t, json.Unmarshal(wire["max_tokens"], &maxTokens))
			require.Equal(t, 512, maxTokens)
			require.NotContains(t, wire, "max_completion_tokens")
		})
	}
}

// When a conversation switches from Gemini to OpenAI, Gemini's thoughtSignature is
// embedded in the tool call_id as "<baseID>_ts_<sig>" and can exceed OpenAI's 64-char
// limit. The chat converter must strip it to the base ID on the wire while leaving the
// caller's input intact (so a later Gemini turn can still recover the signature).
func TestToOpenAIChatRequest_ToolCallCacheControl_OpenRouterOnly(t *testing.T) {
	// The tool_calls marker follows the same rule as block and tool markers: forwarded to
	// OpenRouter, which relays it to Claude, and stripped for every other OpenAI-shaped target.
	makeReq := func(provider schemas.ModelProvider) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    "anthropic/claude-sonnet-4-6",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("weather in paris?")}},
				{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID: schemas.Ptr("call_paris"), Type: schemas.Ptr(string(schemas.ChatToolTypeFunction)),
						Function:     schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Paris"}`},
						CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
					}},
				}},
				{
					Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_paris")},
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("18C")},
				},
			},
		}
	}
	for _, tt := range []struct {
		provider schemas.ModelProvider
		wantKept bool
	}{{schemas.OpenRouter, true}, {schemas.OpenAI, false}, {schemas.Groq, false}} {
		t.Run(string(tt.provider), func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
			defer cancel()
			req := makeReq(tt.provider)
			wire, err := json.Marshal(ToOpenAIChatRequest(ctx, req))
			require.NoError(t, err)
			if tt.wantKept {
				require.Equal(t, 1, strings.Count(string(wire), "cache_control"), "OpenRouter must keep the tool_calls marker: %s", wire)
			} else {
				require.NotContains(t, string(wire), "cache_control", "%s must not receive the tool_calls marker: %s", tt.provider, wire)
			}
			require.Contains(t, string(wire), `"call_paris"`)
			require.NotNil(t, req.Input[1].ChatAssistantMessage.ToolCalls[0].CacheControl, "strip must copy, not mutate the caller's request")
		})
	}
}

func TestToOpenAIChatRequest_OpenRouterClampsCacheControlToFour(t *testing.T) {
	cc := func() *schemas.CacheControl { return &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral} }
	// Five markers in render order: tool definition, system block, user block, tool call, tool result.
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenRouter,
		Model:    "anthropic/claude-sonnet-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("You are a weather assistant."), CacheControl: cc()},
			}}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("What is the weather in Paris?"), CacheControl: cc()},
			}}},
			{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID: schemas.Ptr("toolu_paris"), Type: schemas.Ptr(string(schemas.ChatToolTypeFunction)),
					Function:     schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Paris"}`},
					CacheControl: cc(),
				}},
			}},
			{
				Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_paris")},
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("18C, light rain"), CacheControl: cc()},
				}},
			},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Thanks. Is that warm?")}},
		},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
			Type:         schemas.ChatToolTypeFunction,
			Function:     &schemas.ChatToolFunction{Name: "get_weather", Parameters: &schemas.ToolFunctionParameters{Type: "object"}},
			CacheControl: cc(),
		}}},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, req)
	require.NotNil(t, result)

	wireBody, err := json.Marshal(result)
	require.NoError(t, err)
	wire := string(wireBody)
	require.Equal(t, 4, strings.Count(wire, "cache_control"), "OpenRouter relays markers to Claude, which caps them at 4: %s", wire)

	// The earliest marker in render order (the tool definition) is the one dropped;
	// the later ones, which anchor longer prefixes, all survive.
	require.Nil(t, result.ChatParameters.Tools[0].CacheControl, "tool-definition marker must be the one dropped")
	require.NotNil(t, result.Messages[0].Content.ContentBlocks[0].CacheControl, "system marker must survive")
	require.NotNil(t, result.Messages[1].Content.ContentBlocks[0].CacheControl, "user marker must survive")
	require.NotNil(t, result.Messages[2].OpenAIChatAssistantMessage.ToolCalls[0].CacheControl, "tool_calls marker must survive")
	require.NotNil(t, result.Messages[3].Content.ContentBlocks[0].CacheControl, "tool result marker must survive")

	// The caller's request is shared across retries and fallbacks and must not be mutated.
	require.NotNil(t, req.Params.Tools[0].CacheControl, "clamp must copy, not mutate the caller's tools")
	require.NotNil(t, req.Input[0].Content.ContentBlocks[0].CacheControl)
	require.NotNil(t, req.Input[2].ChatAssistantMessage.ToolCalls[0].CacheControl)

	// A non-Claude upstream on OpenRouter ignores the markers, so all five pass through.
	req.Model = "google/gemini-2.5-pro"
	result = ToOpenAIChatRequest(ctx, req)
	wireBody, err = json.Marshal(result)
	require.NoError(t, err)
	require.Equal(t, 5, strings.Count(string(wireBody), "cache_control"), "clamp must be scoped to Claude upstreams: %s", wireBody)

	// Four markers or fewer pass through untouched on Claude too.
	req.Model = "anthropic/claude-sonnet-5"
	req.Params.Tools[0].CacheControl = nil
	result = ToOpenAIChatRequest(ctx, req)
	wireBody, err = json.Marshal(result)
	require.NoError(t, err)
	require.Equal(t, 4, strings.Count(string(wireBody), "cache_control"))
}

func TestToOpenAIChatRequest_StripsThoughtSignatureFromToolCallIDs(t *testing.T) {
	embeddedID := "search" + providerUtils.ThoughtSignatureSeparator + strings.Repeat("A", 6000)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:   schemas.Ptr(embeddedID),
						Type: schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("search"),
							Arguments: "{}",
						},
					}},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(embeddedID)},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("result")},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, req)
	require.NotNil(t, result)

	gotCallID := *result.Messages[0].OpenAIChatAssistantMessage.ToolCalls[0].ID
	gotToolCallID := *result.Messages[1].ChatToolMessage.ToolCallID

	if gotCallID != "search" {
		t.Errorf("assistant tool call ID: got %q, want %q", gotCallID, "search")
	}
	if len(gotCallID) > 64 {
		t.Errorf("assistant tool call ID exceeds OpenAI's 64-char limit: %d chars", len(gotCallID))
	}
	if gotToolCallID != gotCallID {
		t.Errorf("tool result ID %q must match assistant call ID %q", gotToolCallID, gotCallID)
	}

	// The caller's history must be untouched.
	if *req.Input[0].ChatAssistantMessage.ToolCalls[0].ID != embeddedID {
		t.Error("original assistant tool call ID was mutated")
	}
	if *req.Input[1].ChatToolMessage.ToolCallID != embeddedID {
		t.Error("original tool result tool_call_id was mutated")
	}
}

// A short call id that merely contains "_ts_" (e.g. two distinct raw upstream ids) must be
// left intact: stripping only kicks in above OpenAI's 64-char limit, so distinct ids never
// collapse into one.
func TestToOpenAIChatRequest_PreservesShortToolCallIDsContainingSeparator(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							ID:       schemas.Ptr("search_ts_a"),
							Type:     schemas.Ptr("function"),
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("search"), Arguments: "{}"},
						},
						{
							ID:       schemas.Ptr("search_ts_b"),
							Type:     schemas.Ptr("function"),
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("search"), Arguments: "{}"},
						},
					},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("search_ts_a")},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("r")},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, req)
	require.NotNil(t, result)

	got := result.Messages[0].OpenAIChatAssistantMessage.ToolCalls
	if *got[0].ID != "search_ts_a" || *got[1].ID != "search_ts_b" {
		t.Errorf("distinct short ids must be preserved, got %q and %q", *got[0].ID, *got[1].ID)
	}
	if *result.Messages[1].ChatToolMessage.ToolCallID != "search_ts_a" {
		t.Errorf("short tool_call_id must be preserved, got %q", *result.Messages[1].ChatToolMessage.ToolCallID)
	}
}

func TestOpenAIChatRequest_StripsCitationTextFromAnnotations(t *testing.T) {
	req := &OpenAIChatRequest{
		Model:    "gpt-4o",
		Provider: schemas.OpenAI,
		Messages: []OpenAIMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("who won?")}},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Spain won Euro 2024.")},
				OpenAIChatAssistantMessage: &OpenAIChatAssistantMessage{
					Annotations: []schemas.ChatAssistantMessageAnnotation{
						{
							Type: "url_citation",
							URLCitation: schemas.ChatAssistantMessageAnnotationCitation{
								StartIndex: 0,
								EndIndex:   20,
								Title:      "uefa.com",
								URL:        schemas.Ptr("https://example.com/spain"),
								Text:       schemas.Ptr("Spain won Euro 2024."),
							},
						},
					},
				},
			},
		},
	}

	wireBody, err := json.Marshal(req)
	require.NoError(t, err)
	s := string(wireBody)

	require.Contains(t, s, `"url":"https://example.com/spain"`, "annotation url must survive")
	require.Contains(t, s, `"title":"uefa.com"`, "annotation title must survive")
	require.NotContains(t, s, `"text"`, "Bifrost-extension citation text must not reach the OpenAI wire")

	// Original request must not be mutated
	require.NotNil(t, req.Messages[1].OpenAIChatAssistantMessage.Annotations[0].URLCitation.Text)
}

func TestOpenAIChatRequest_StripsWebSearchOptionsFilters(t *testing.T) {
	req := &OpenAIChatRequest{
		Model:    "gpt-4o-search-preview",
		Provider: schemas.OpenAI,
		Messages: []OpenAIMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("who won?")}},
		},
		ChatParameters: schemas.ChatParameters{
			WebSearchOptions: &schemas.ChatWebSearchOptions{
				SearchContextSize: schemas.Ptr("high"),
				Filters: &schemas.ChatWebSearchOptionsFilters{
					BlockedDomains: []string{"pinterest.com"},
				},
			},
		},
	}

	wireBody, err := json.Marshal(req)
	require.NoError(t, err)
	s := string(wireBody)

	require.Contains(t, s, `"web_search_options"`, "web_search_options must survive for OpenAI")
	require.Contains(t, s, `"search_context_size":"high"`, "native fields must survive")
	require.NotContains(t, s, `"filters"`, "Bifrost-extension filters must not reach the OpenAI wire")
	require.NotContains(t, s, "pinterest.com", "filter contents must not reach the OpenAI wire")

	// Original request must not be mutated
	require.NotNil(t, req.ChatParameters.WebSearchOptions.Filters)
}

// TestToOpenAIChatRequest_PreservesDeepSeekToolCallReasoningContent covers the outbound
// half of issue #5887. DeepSeek requires reasoning_content to be replayed on assistant
// tool_call turns (400 without it) while rejecting it on ordinary assistant turns, so the
// strip must be selective rather than unconditional.
func TestToOpenAIChatRequest_PreservesDeepSeekToolCallReasoningContent(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	toolCallReasoning := "the user wants the time, call the tool"
	plainReasoning := "no tool needed here"
	plainAnswer := "It is 12:00 UTC."
	toolCallID := "call_1"
	toolName := "get_current_time"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &toolCallReasoning,
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:       &toolCallID,
						Function: schemas.ChatAssistantMessageToolCallFunction{Name: &toolName, Arguments: `{}`},
					}},
				},
			},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &plainAnswer},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &plainReasoning,
				},
			},
		},
	}

	result := ToOpenAIChatRequest(ctx, bifrostReq)
	require.NotNil(t, result)
	require.Len(t, result.Messages, 2)

	require.NotNil(t, result.Messages[0].OpenAIChatAssistantMessage)
	require.NotNil(t, result.Messages[0].OpenAIChatAssistantMessage.Reasoning,
		"reasoning_content must be preserved on a DeepSeek assistant tool_call turn")
	require.Equal(t, toolCallReasoning, *result.Messages[0].OpenAIChatAssistantMessage.Reasoning)

	require.NotNil(t, result.Messages[1].OpenAIChatAssistantMessage)
	require.Nil(t, result.Messages[1].OpenAIChatAssistantMessage.Reasoning,
		"reasoning_content must still be stripped from a DeepSeek assistant turn without tool calls")
}

// TestConvertOpenAIMessagesToBifrostMessages_NormalizesReasoningSpellings covers the inbound
// half of issue #5887: callers replay assistant reasoning as reasoning_content, reasoning,
// or reasoning_details, and all three must reach the Bifrost schema so provider logic that
// gates on replayed reasoning behaves the same either way.
func TestConvertOpenAIMessagesToBifrostMessages_NormalizesReasoningSpellings(t *testing.T) {
	reasoning := "step by step"

	tests := []struct {
		name    string
		payload string
	}{
		{name: "reasoning_content", payload: `{"role":"assistant","reasoning_content":"step by step"}`},
		{name: "reasoning", payload: `{"role":"assistant","reasoning":"step by step"}`},
		{name: "reasoning_details", payload: `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","text":"step by step"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg OpenAIMessage
			require.NoError(t, sonic.Unmarshal([]byte(tt.payload), &msg))

			converted := ConvertOpenAIMessagesToBifrostMessages([]OpenAIMessage{msg})
			require.Len(t, converted, 1)
			require.NotNil(t, converted[0].ChatAssistantMessage)
			require.NotNil(t, converted[0].ChatAssistantMessage.Reasoning,
				"replayed reasoning must reach the Bifrost schema regardless of spelling")
			require.Equal(t, reasoning, *converted[0].ChatAssistantMessage.Reasoning)
		})
	}
}

// TestToOpenAIChatRequest_DoesNotEmitInboundReasoningAliases pins the inbound-only contract
// on OpenAIChatAssistantMessage: the "reasoning" and "reasoning_details" aliases exist to
// parse requests and must never appear on the outbound wire for any provider.
func TestToOpenAIChatRequest_DoesNotEmitInboundReasoningAliases(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	reasoning := "step by step"
	answer := "It is 12:00 UTC."

	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.DeepSeek, schemas.XAI} {
		t.Run(string(provider), func(t *testing.T) {
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    "some-model",
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{ContentStr: &answer},
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						Reasoning: &reasoning,
						ReasoningDetails: []schemas.ChatReasoningDetails{{
							Index: 0,
							Type:  schemas.BifrostReasoningDetailsTypeText,
							Text:  &reasoning,
						}},
					},
				}},
			}

			wireBody, err := sonic.Marshal(ToOpenAIChatRequest(ctx, bifrostReq))
			require.NoError(t, err)

			var jsonMap map[string]any
			require.NoError(t, sonic.Unmarshal(wireBody, &jsonMap))
			messages, ok := jsonMap["messages"].([]any)
			require.True(t, ok)
			require.Len(t, messages, 1)
			assistantMessage, ok := messages[0].(map[string]any)
			require.True(t, ok)

			require.NotContains(t, assistantMessage, "reasoning",
				"the inbound-only reasoning alias must never be marshalled outbound")
			require.NotContains(t, assistantMessage, "reasoning_details",
				"the inbound-only reasoning_details alias must never be marshalled outbound")
		})
	}
}

// TestXAIReasoningEffortEndToEnd pins reasoning_effort through the FULL conversion
// pipeline, not just applyXAICompatibility. filterOpenAISpecificParameters runs
// normalizeReasoningEffort BEFORE the xAI compat pass, so a value can be rewritten
// upstream of the deny-list check - which is exactly how "xhigh" was silently
// downgraded to "high" for grok-4.6 even after the deny-list was fixed.
func TestXAIReasoningEffortEndToEnd(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		effort   string
		expected *string // nil => expect the field stripped
	}{
		{"grok-4.6 keeps xhigh", "grok-4.6", "xhigh", schemas.Ptr("xhigh")},
		{"grok-4.6 maps max to xhigh", "grok-4.6", "max", schemas.Ptr("xhigh")},
		{"grok-4.6 keeps low", "grok-4.6", "low", schemas.Ptr("low")},
		{"grok-4.5 downgrades xhigh to high", "grok-4.5", "xhigh", schemas.Ptr("high")},
		{"grok-4.5 keeps medium", "grok-4.5", "medium", schemas.Ptr("medium")},
		{"grok-4.20-multi-agent keeps xhigh", "grok-4.20-multi-agent-0309", "xhigh", schemas.Ptr("xhigh")},
		{"grok-4 strips effort", "grok-4-0709", "high", nil},
		{"grok-code-fast-1 strips effort", "grok-code-fast-1", "high", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: schemas.XAI,
				Model:    tt.model,
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
				},
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr(tt.effort)},
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
			defer cancel()

			result := ToOpenAIChatRequest(ctx, bifrostReq)
			require.NotNil(t, result)

			if tt.expected == nil {
				if result.Reasoning != nil && result.Reasoning.Effort != nil {
					t.Fatalf("expected effort stripped for %s, got %q", tt.model, *result.Reasoning.Effort)
				}
				return
			}
			require.NotNil(t, result.Reasoning, "reasoning missing for %s", tt.model)
			require.NotNil(t, result.Reasoning.Effort, "effort stripped for %s", tt.model)
			if *result.Reasoning.Effort != *tt.expected {
				t.Errorf("model %s effort %q: expected %q, got %q",
					tt.model, tt.effort, *tt.expected, *result.Reasoning.Effort)
			}
		})
	}
}

// installCapabilityRecord points the capability resolver at a single model for
// the duration of the test, so a gate can be driven from the datasheet side.
func installCapabilityRecord(t *testing.T, model string, record *schemas.ModelCapabilities) {
	t.Helper()
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if m != model {
			return nil
		}
		return record
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

// TestOpenAICompatFiltersReadDatasheet covers the datasheet side of the
// openai-compat parameter filters. The name-based fallbacks are covered by
// TestApplyXAICompatibility; these pin that an unsupported_fields entry
// overrides them, and that unlisted fields keep the fallback.
func TestOpenAICompatFiltersReadDatasheet(t *testing.T) {
	t.Run("compat_filter_strips_when_datasheet_is_silent", func(t *testing.T) {
		req := &OpenAIChatRequest{ChatParameters: schemas.ChatParameters{
			Prediction: &schemas.ChatPrediction{Type: "content"},
			Store:      new(true),
			Verbosity:  schemas.Ptr("low"),
		}}
		req.filterOpenAISpecificParameters(schemas.ResolveModelCaps(schemas.Cerebras, "some-compat-model"))
		require.Nil(t, req.Prediction)
		require.Nil(t, req.Store)
		require.Nil(t, req.Verbosity)
	})

	t.Run("compat_filter_honours_explicit_opt_in", func(t *testing.T) {
		const model = "fireworks-predictive"
		installCapabilityRecord(t, model, &schemas.ModelCapabilities{
			UnsupportedFields: map[string]bool{schemas.FieldPrediction: false},
		})

		req := &OpenAIChatRequest{ChatParameters: schemas.ChatParameters{
			Prediction: &schemas.ChatPrediction{Type: "content"},
			Store:      new(true),
		}}
		req.filterOpenAISpecificParameters(schemas.ResolveModelCaps(schemas.Fireworks, model))
		require.NotNil(t, req.Prediction, "an explicit false must survive the compat filter")
		require.Nil(t, req.Store, "fields the row omits keep the default strip")
	})

	t.Run("xai_gate_honours_explicit_opt_in", func(t *testing.T) {
		const model = "grok-4"
		installCapabilityRecord(t, model, &schemas.ModelCapabilities{
			UnsupportedFields: map[string]bool{schemas.FieldPresencePenalty: false},
		})

		req := &OpenAIChatRequest{ChatParameters: schemas.ChatParameters{
			PresencePenalty:  schemas.Ptr(0.5),
			FrequencyPenalty: schemas.Ptr(0.5),
		}}
		req.applyXAICompatibility(schemas.ResolveModelCaps(schemas.XAI, model))
		require.NotNil(t, req.PresencePenalty, "an explicit false must beat the grok name check")
		require.Nil(t, req.FrequencyPenalty, "fields the row omits keep the grok name-based default")
	})
}

// reasoning.type is an Anthropic thinking type; it must never reach the wire of
// OpenAI or the OpenAI-compatible providers that share this converter.
func TestToOpenAIChatRequest_DoesNotEmitReasoningType(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Azure, schemas.Groq, schemas.OpenRouter, schemas.XAI, schemas.Cerebras, schemas.Ollama} {
		req := &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    "gpt-5",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
			}},
			Params: &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{
				Type:   schemas.Ptr("between_tools"),
				Effort: schemas.Ptr("medium"),
			}},
		}
		out := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), req)
		require.NotNil(t, out, "%s: expected request", provider)
		body, err := sonic.Marshal(out)
		require.NoError(t, err)
		require.NotContains(t, string(body), "between_tools", "%s: reasoning.type leaked: %s", provider, body)
	}
}

// TestToOpenAIChatRequest_GPT56CacheBreakpoint is the Chat Completions counterpart
// of TestToOpenAIResponsesRequest_GPT56CacheBreakpoint. gpt-5.6 and later take an
// explicit boundary as prompt_cache_breakpoint on a text part; a cache_control
// marker sent in Chat format (LiteLLM-style clients) used to be stripped with
// nothing put in its place, so the request fell back to implicit caching.
func TestToOpenAIChatRequest_GPT56CacheBreakpoint(t *testing.T) {
	ephemeral := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
	mkReq := func(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    model,
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("REUSABLE_PREFIX"), CacheControl: ephemeral},
				}}},
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeImage, CacheControl: ephemeral, ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.com/a.png"}},
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("Call echo.")},
				}}},
				{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID: schemas.Ptr("call_1"), Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("echo"), Arguments: `{"text":"hello"}`},
				}}}},
				{
					Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")},
					Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
						{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("hello"), CacheControl: ephemeral},
					}},
				},
			},
			Params: &schemas.ChatParameters{},
		}
	}
	marshal := func(t *testing.T, req *schemas.BifrostChatRequest) (map[string]any, string) {
		t.Helper()
		ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
		defer cancel()
		out := ToOpenAIChatRequest(ctx, req)
		require.NotNil(t, out)
		b, err := out.MarshalJSON()
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, sonic.Unmarshal(b, &m), string(b))
		return m, string(b)
	}
	part := func(t *testing.T, m map[string]any, msg, idx int, raw string) map[string]any {
		t.Helper()
		msgs, _ := m["messages"].([]any)
		require.Greater(t, len(msgs), msg, raw)
		mm, _ := msgs[msg].(map[string]any)
		parts, ok := mm["content"].([]any)
		require.True(t, ok && idx < len(parts), "message %d content must be a part array; raw=%s", msg, raw)
		p, _ := parts[idx].(map[string]any)
		return p
	}

	for _, tc := range []struct {
		name     string
		provider schemas.ModelProvider
		model    string
	}{
		{"openai gpt-5.6", schemas.OpenAI, "gpt-5.6-sol"},
		{"openai gpt-6", schemas.OpenAI, "gpt-6-sol"},
		{"azure gpt-6.1", schemas.Azure, "gpt-6.1-luna"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := mkReq(tc.provider, tc.model)
			m, raw := marshal(t, req)
			for _, loc := range []struct {
				msg, idx int
				what     string
			}{{0, 0, "system text"}, {3, 0, "tool message text"}} {
				p := part(t, m, loc.msg, loc.idx, raw)
				bp, ok := p["prompt_cache_breakpoint"].(map[string]any)
				require.Truef(t, ok, "%s must carry prompt_cache_breakpoint; raw=%s", loc.what, raw)
				require.Equalf(t, "explicit", bp["mode"], "%s mode; raw=%s", loc.what, raw)
				_, present := p["cache_control"]
				require.Falsef(t, present, "cache_control must not reach OpenAI on %s; raw=%s", loc.what, raw)
			}
			img := part(t, m, 1, 0, raw)
			_, present := img["prompt_cache_breakpoint"]
			require.Falsef(t, present, "Chat image parts have no verified breakpoint field; none may be synthesized; raw=%s", raw)
			_, present = img["cache_control"]
			require.Falsef(t, present, "cache_control must be stripped from the image part; raw=%s", raw)
			_, present = part(t, m, 1, 1, raw)["prompt_cache_breakpoint"]
			require.Falsef(t, present, "unmarked text part must not receive a breakpoint; raw=%s", raw)

			// Explicit-only mode drops the implicit lookback over earlier message
			// endings, so a rolling per-turn marker would never be read back.
			_, present = m["prompt_cache_options"]
			require.Falsef(t, present, "a translated breakpoint must not switch the request to explicit mode; raw=%s", raw)

			// Copy-on-write: the caller's input must still carry its own markers only.
			require.Nil(t, req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint, "caller's input was mutated")
			require.NotNil(t, req.Input[0].Content.ContentBlocks[0].CacheControl, "caller's marker was cleared")
		})
	}

	t.Run("caller's prompt_cache_options wins", func(t *testing.T) {
		req := mkReq(schemas.OpenAI, "gpt-5.6-sol")
		req.Params.PromptCacheOptions = &schemas.PromptCacheOptions{Mode: schemas.Ptr("implicit")}
		m, raw := marshal(t, req)
		opts, _ := m["prompt_cache_options"].(map[string]any)
		require.Equalf(t, "implicit", opts["mode"], "raw=%s", raw)
	})

	t.Run("pre-5.6 model strips and stays implicit", func(t *testing.T) {
		m, raw := marshal(t, mkReq(schemas.OpenAI, "gpt-4o"))
		_, present := part(t, m, 0, 0, raw)["prompt_cache_breakpoint"]
		require.Falsef(t, present, "gpt-4o predates prompt_cache_breakpoint; raw=%s", raw)
		_, present = part(t, m, 0, 0, raw)["cache_control"]
		require.Falsef(t, present, "cache_control must still be stripped; raw=%s", raw)
		_, present = m["prompt_cache_options"]
		require.Falsef(t, present, "gpt-4o must not gain prompt_cache_options; raw=%s", raw)
	})

	t.Run("openrouter keeps cache_control verbatim and gains no breakpoint", func(t *testing.T) {
		m, raw := marshal(t, mkReq(schemas.OpenRouter, "anthropic/claude-sonnet-4.6"))
		p := part(t, m, 0, 0, raw)
		_, present := p["prompt_cache_breakpoint"]
		require.Falsef(t, present, "raw=%s", raw)
		_, present = p["cache_control"]
		require.Truef(t, present, "OpenRouter forwards cache_control itself; raw=%s", raw)
		_, present = m["prompt_cache_options"]
		require.Falsef(t, present, "raw=%s", raw)
	})

	overMarked := func() *schemas.BifrostChatRequest {
		req := mkReq(schemas.OpenAI, "gpt-5.6-sol")
		var extra []schemas.ChatMessage
		for i := 0; i < 4; i++ {
			extra = append(extra, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("turn"), CacheControl: ephemeral},
			}}})
		}
		req.Input = append(req.Input, extra...) // 6 markers in total: system, tool, 4 turns
		return req
	}
	countBreakpoints := func(m map[string]any) int {
		count := 0
		msgs, _ := m["messages"].([]any)
		for _, mm := range msgs {
			parts, _ := mm.(map[string]any)["content"].([]any)
			for _, pp := range parts {
				if _, ok := pp.(map[string]any)["prompt_cache_breakpoint"]; ok {
					count++
				}
			}
		}
		return count
	}

	t.Run("implicit mode keeps the latest three markers", func(t *testing.T) {
		// The implicit breakpoint takes one of the four cache-write slots.
		m, raw := marshal(t, overMarked())
		require.Equalf(t, 3, countBreakpoints(m), "raw=%s", raw)
		_, present := part(t, m, 0, 0, raw)["prompt_cache_breakpoint"]
		require.Falsef(t, present, "the earliest marker (system) must be the one dropped; raw=%s", raw)
	})

	t.Run("caller explicit mode keeps the latest four markers", func(t *testing.T) {
		req := overMarked()
		req.Params.PromptCacheOptions = &schemas.PromptCacheOptions{Mode: schemas.Ptr("explicit")}
		m, raw := marshal(t, req)
		require.Equalf(t, 4, countBreakpoints(m), "raw=%s", raw)
		opts, _ := m["prompt_cache_options"].(map[string]any)
		require.Equalf(t, "explicit", opts["mode"], "raw=%s", raw)
	})

	t.Run("caller options without a mode keep the latest three markers", func(t *testing.T) {
		req := overMarked()
		req.Params.PromptCacheOptions = &schemas.PromptCacheOptions{TTL: schemas.Ptr("30m")}
		m, raw := marshal(t, req)
		require.Equalf(t, 3, countBreakpoints(m), "raw=%s", raw)
	})

	t.Run("passthrough explicit mode keeps the latest four markers", func(t *testing.T) {
		// Passthrough extra params are merged into the wire body after serialization,
		// so this prompt_cache_options is what the provider sees.
		req := overMarked()
		req.Params.ExtraParams = map[string]interface{}{
			"prompt_cache_options": map[string]interface{}{"mode": "explicit"},
		}
		ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
		defer cancel()
		ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
		out := ToOpenAIChatRequest(ctx, req)
		require.NotNil(t, out)
		b, err := out.MarshalJSON()
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, sonic.Unmarshal(b, &m), string(b))
		require.Equalf(t, 4, countBreakpoints(m), "raw=%s", string(b))
	})
}

// The /openai integration keeps OpenAI's chat shape, which has no reasoning.summary:
// it must not reach the Responses request unless sent via passthrough extra_params.
func TestOpenAIInbound_ReasoningSummaryIsNotForwarded(t *testing.T) {
	body := `{"model":"openai/gpt-6-astra","reasoning":{"effort":"high","summary":"auto"},"messages":[{"role":"user","content":"hi"}]}`

	var req OpenAIChatRequest
	if err := sonic.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	responses := req.ToBifrostChatRequest(ctx).ToResponsesRequest()
	if responses.Params == nil || responses.Params.Reasoning == nil {
		t.Fatalf("reasoning.effort should still be forwarded: %+v", responses.Params)
	}
	if responses.Params.Reasoning.Effort == nil || *responses.Params.Reasoning.Effort != "high" {
		t.Fatalf("reasoning.effort = %v, want high", responses.Params.Reasoning.Effort)
	}
	if responses.Params.Reasoning.Summary != nil {
		t.Fatalf("reasoning.summary leaked through the openai integration: %q", *responses.Params.Reasoning.Summary)
	}
}

// oauthTokenStub answers every token request with one fixed bearer and counts hits.
func oauthTokenStub(t testing.TB) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"minted-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func oauthTestProvider(t testing.TB) *OpenAIProvider {
	t.Helper()
	return NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "https://llm.example", DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
	}, nil)
}

func oauthKey(name, tokenURL string) schemas.Key {
	return schemas.Key{Name: name, Models: []string{"*"}, OAuthKeyConfig: &schemas.OAuthKeyConfig{
		GrantType:    schemas.OAuthGrantClientCredentials,
		TokenURL:     *schemas.NewSecretVar(tokenURL),
		ClientID:     schemas.NewSecretVar(name + "-id"),
		ClientSecret: schemas.NewSecretVar(name + "-secret"),
	}}
}

// TestFileListCursorSelectsTheKeyByPosition pins that a serial pagination cursor keeps
// addressing the key it was issued for. The cursor stores a position, so a failed mint on an
// earlier key must not shift the later keys: the cursor for key 2 queries key 2.
func TestFileListCursorSelectsTheKeyByPosition(t *testing.T) {
	stub, _ := oauthTokenStub(t)
	var authSeen atomic.Value
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"file-1","object":"file","bytes":1,"created_at":1,"filename":"a.txt","purpose":"assistants"}],"has_more":false}`))
	}))
	t.Cleanup(files.Close)
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: files.URL, DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
	}, nil)
	keys := []schemas.Key{
		oauthKey("down", "http://127.0.0.1:1/token"),
		oauthKey("up", stub.URL+"/token"),
	}
	cursor := schemas.EncodeSerialCursor(schemas.NewSerialCursor(1, ""))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{After: &cursor})
	if bErr != nil {
		t.Fatalf("the cursor names the working key, so the list must succeed: %v", bErr.Error.Message)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "file-1" {
		t.Fatalf("data = %+v, want the second key's one file", resp.Data)
	}
	if got, _ := authSeen.Load().(string); got != "Bearer minted-token" {
		t.Fatalf("Authorization = %q, want the second key's minted bearer", got)
	}

	// The cursor for the failed key surfaces that key's error instead of silently serving a
	// different key's files.
	cursor = schemas.EncodeSerialCursor(schemas.NewSerialCursor(0, ""))
	_, bErr = provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{After: &cursor})
	if bErr == nil {
		t.Fatal("the cursor for the key whose mint fails must return that failure")
	}
}

// TestDecisionMintsOAuthBearer pins that the native decisions path resolves an OAuth key: the
// single-key method must carry the minted bearer, not the key's empty static value.
func TestDecisionMintsOAuthBearer(t *testing.T) {
	stub, _ := oauthTokenStub(t)
	var authSeen atomic.Value
	decisions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"dec_1","model":"gpt-6-luna","answers":[{"type":"predicate","probability":0.9}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(decisions.Close)
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: decisions.URL, DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
	}, nil)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.Decision(ctx, oauthKey("dec", stub.URL+"/token"), &schemas.BifrostDecisionRequest{
		Provider:  schemas.OpenAI,
		Model:     "gpt-6-luna",
		Input:     schemas.DecisionInput{Text: schemas.Ptr("this is unacceptable")},
		Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("angry")}},
	})
	if bErr != nil {
		t.Fatalf("decision failed: %v", bErr.Error.Message)
	}
	if len(resp.Answers) != 1 {
		t.Fatalf("answers = %+v, want one", resp.Answers)
	}
	if got, _ := authSeen.Load().(string); got != "Bearer minted-token" {
		t.Fatalf("Authorization = %q, want the minted bearer", got)
	}

	// A key whose token endpoint is down fails the request with the mint error, not a bare 401.
	_, bErr = provider.Decision(ctx, oauthKey("down", "http://127.0.0.1:1/token"), &schemas.BifrostDecisionRequest{
		Provider:  schemas.OpenAI,
		Model:     "gpt-6-luna",
		Input:     schemas.DecisionInput{Text: schemas.Ptr("x")},
		Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("angry")}},
	})
	if bErr == nil || !strings.Contains(bErr.Error.Message, "oauth token") {
		t.Fatalf("want the token mint failure, got %+v", bErr)
	}
}

// TestWebSocketHeadersMintOAuthBearer pins that the native WebSocket path carries the minted
// bearer for an OAuth key instead of dialing with an empty Authorization header.
func TestWebSocketHeadersMintOAuthBearer(t *testing.T) {
	stub, hits := oauthTokenStub(t)
	provider := oauthTestProvider(t)
	key := oauthKey("ws", stub.URL+"/token")

	headers := provider.WebSocketHeaders(key)
	if headers["Authorization"] != "Bearer minted-token" {
		t.Fatalf("Authorization = %q, want the minted bearer", headers["Authorization"])
	}
	headers = provider.WebSocketHeaders(key)
	if headers["Authorization"] != "Bearer minted-token" || hits.Load() != 1 {
		t.Fatalf("second call: Authorization = %q, mints = %d, want the cached bearer and one mint", headers["Authorization"], hits.Load())
	}

	// A key whose endpoint is down keeps today's behaviour: no Authorization header, so the
	// transport falls back to the HTTP bridge.
	headers = provider.WebSocketHeaders(oauthKey("down", "http://127.0.0.1:1/token"))
	if _, ok := headers["Authorization"]; ok {
		t.Fatalf("a failed mint must not send an Authorization header, got %q", headers["Authorization"])
	}
	if provider.WebSocketHeaders(schemas.Key{Value: *schemas.NewSecretVar("sk")})["Authorization"] != "Bearer sk" {
		t.Fatal("a static key is unchanged")
	}
}

// BenchmarkResolveKeyOAuthWarm is the per-request cost of an OAuth key once its token is
// cached: the cache-key hash, the warm cache lookup and the SecretVar copy. The token is
// minted once before the timer.
func BenchmarkResolveKeyOAuthWarm(b *testing.B) {
	stub, _ := oauthTokenStub(b)
	provider := oauthTestProvider(b)
	key := oauthKey("bench", stub.URL+"/token")
	if _, bErr := provider.resolveKey(context.Background(), key); bErr != nil {
		b.Fatal(bErr.Error.Message)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, bErr := provider.resolveKey(context.Background(), key); bErr != nil {
			b.Fatal(bErr.Error.Message)
		}
	}
}

// BenchmarkResolveKeyStatic is the cost every OpenAI request now pays at method entry when
// the key carries a value: it must stay at two comparisons and zero allocations.
func BenchmarkResolveKeyStatic(b *testing.B) {
	provider := &OpenAIProvider{}
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-static")}
	b.ReportAllocs()
	for range b.N {
		if _, bErr := provider.resolveKey(context.Background(), key); bErr != nil {
			b.Fatal(bErr)
		}
	}
}

// deepSeekJSONSchemaBody is a chat/completions body carrying a json_schema
// response_format and a system prompt of its own, as a client sends it.
const deepSeekJSONSchemaBody = `{"model":"deepseek-v4-flash","messages":[{"role":"system","content":"You extract people."},{"role":"user","content":"Ada is 36."}],"response_format":{"type":"json_schema","json_schema":{"name":"person","schema":{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"],"additionalProperties":false},"strict":true}}}`

func deepSeekJSONSchemaRequest(t testing.TB, body string, provider schemas.ModelProvider) (*schemas.BifrostContext, *schemas.BifrostChatRequest) {
	t.Helper()
	var inbound OpenAIChatRequest
	require.NoError(t, schemas.Unmarshal([]byte(body), &inbound))
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := inbound.ToBifrostChatRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = provider
	return ctx, bifrostReq
}

// TestToOpenAIChatRequest_DeepSeekDowngradesJSONSchema pins that a json_schema
// response_format reaches DeepSeek as json_object, which is the only structured
// mode its chat endpoint accepts, with the schema carried as an instruction.
func TestToOpenAIChatRequest_DeepSeekDowngradesJSONSchema(t *testing.T) {
	ctx, bifrostReq := deepSeekJSONSchemaRequest(t, deepSeekJSONSchemaBody, schemas.DeepSeek)
	originalFormat := bifrostReq.Params.ResponseFormat
	originalFormatValue := *originalFormat

	out := ToOpenAIChatRequest(ctx, bifrostReq)
	require.NotNil(t, out)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	require.Equal(t, "json_object", gjsonString(raw, "response_format.type"), "wire: %s", raw)
	require.False(t, gjsonExists(raw, "response_format.json_schema"), "json_schema must not reach DeepSeek: %s", raw)

	require.Len(t, out.Messages, 3, "schema instruction must be added as one system message")
	require.Equal(t, schemas.ChatMessageRoleSystem, out.Messages[0].Role)
	require.Equal(t, "You extract people.", *out.Messages[0].Content.ContentStr, "client system prompt stays first")
	require.Equal(t, schemas.ChatMessageRoleSystem, out.Messages[1].Role)
	require.NotNil(t, out.Messages[1].Content)
	require.NotNil(t, out.Messages[1].Content.ContentStr)
	instruction := *out.Messages[1].Content.ContentStr
	require.Contains(t, instruction, "json", "DeepSeek json_object requires the word json in the prompt")
	require.Contains(t, instruction, `{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"],"additionalProperties":false}`,
		"schema must be carried verbatim")
	require.Contains(t, instruction, "person", "schema name is carried")
	require.Equal(t, schemas.ChatMessageRoleUser, out.Messages[2].Role)

	// The caller's request is shared with fallbacks and post-hooks.
	require.Same(t, originalFormat, bifrostReq.Params.ResponseFormat)
	require.Equal(t, originalFormatValue, *bifrostReq.Params.ResponseFormat)
	require.Len(t, bifrostReq.Input, 2)
}

// TestToOpenAIChatRequest_DeepSeekJSONSchemaWithoutSystemPrompt covers the
// common shape with no client system prompt: the instruction leads.
func TestToOpenAIChatRequest_DeepSeekJSONSchemaWithoutSystemPrompt(t *testing.T) {
	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"Ada is 36."}],"response_format":{"type":"json_schema","json_schema":{"name":"person","schema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}}`
	ctx, bifrostReq := deepSeekJSONSchemaRequest(t, body, schemas.DeepSeek)

	out := ToOpenAIChatRequest(ctx, bifrostReq)
	require.NotNil(t, out)
	require.Len(t, out.Messages, 2)
	require.Equal(t, schemas.ChatMessageRoleSystem, out.Messages[0].Role)
	require.Contains(t, *out.Messages[0].Content.ContentStr, `"required":["name"]`)
	require.Equal(t, schemas.ChatMessageRoleUser, out.Messages[1].Role)
}

// TestToOpenAIChatRequest_DeepSeekResponsesTextFormatDowngraded covers the
// Responses route: DeepSeek serves /v1/responses through ToChatRequest, where
// text.format arrives as an in-memory response_format rather than raw bytes.
func TestToOpenAIChatRequest_DeepSeekResponsesTextFormatDowngraded(t *testing.T) {
	var inbound OpenAIResponsesRequest
	body := `{"model":"deepseek-v4-flash","input":"Ada is 36.","text":{"format":{"type":"json_schema","name":"person","schema":{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"]}}}}`
	require.NoError(t, schemas.Unmarshal([]byte(body), &inbound))
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	responsesReq := inbound.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, responsesReq)
	responsesReq.Provider = schemas.DeepSeek

	out := ToOpenAIChatRequest(ctx, responsesReq.ToChatRequest())
	require.NotNil(t, out)
	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	require.Equal(t, "json_object", gjsonString(raw, "response_format.type"), "wire: %s", raw)
	require.False(t, gjsonExists(raw, "response_format.json_schema"), "wire: %s", raw)
	require.Equal(t, schemas.ChatMessageRoleSystem, out.Messages[0].Role)
	instruction := *out.Messages[0].Content.ContentStr
	require.Contains(t, instruction, "json")
	require.Contains(t, instruction, `"required":["name","age"]`)
}

// TestToOpenAIChatRequest_JSONSchemaDowngradeScope pins what the DeepSeek
// downgrade must leave alone: json_object on DeepSeek, and json_schema on any
// provider that accepts it.
func TestToOpenAIChatRequest_JSONSchemaDowngradeScope(t *testing.T) {
	t.Run("deepseek json_object untouched", func(t *testing.T) {
		body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"Reply in json."}],"response_format":{"type":"json_object"}}`
		ctx, bifrostReq := deepSeekJSONSchemaRequest(t, body, schemas.DeepSeek)
		out := ToOpenAIChatRequest(ctx, bifrostReq)
		require.NotNil(t, out)
		require.Same(t, bifrostReq.Params.ResponseFormat, out.ResponseFormat)
		require.Len(t, out.Messages, 1)
	})
	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Groq, schemas.XAI} {
		t.Run(string(provider)+" json_schema untouched", func(t *testing.T) {
			ctx, bifrostReq := deepSeekJSONSchemaRequest(t, deepSeekJSONSchemaBody, provider)
			out := ToOpenAIChatRequest(ctx, bifrostReq)
			require.NotNil(t, out)
			require.Same(t, bifrostReq.Params.ResponseFormat, out.ResponseFormat)
			require.Len(t, out.Messages, 2)
		})
	}
	// A custom provider reports its own name, so the downgrade keys on the base provider.
	t.Run("custom provider on a deepseek base downgrades", func(t *testing.T) {
		ctx, bifrostReq := deepSeekJSONSchemaRequest(t, deepSeekJSONSchemaBody, schemas.ModelProvider("my-deepseek"))
		ctx.SetValue(schemas.BifrostContextKeyIsCustomProvider, true)
		ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.DeepSeek)
		out := ToOpenAIChatRequest(ctx, bifrostReq)
		require.NotNil(t, out)
		raw, err := providerUtils.MarshalSorted(out)
		require.NoError(t, err)
		require.Equal(t, "json_object", gjsonString(raw, "response_format.type"), "wire: %s", raw)
		require.False(t, gjsonExists(raw, "response_format.json_schema"), "json_schema must not reach a custom DeepSeek: %s", raw)
	})
	t.Run("custom provider on an openai base json_schema untouched", func(t *testing.T) {
		ctx, bifrostReq := deepSeekJSONSchemaRequest(t, deepSeekJSONSchemaBody, schemas.ModelProvider("my-openai"))
		ctx.SetValue(schemas.BifrostContextKeyIsCustomProvider, true)
		ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.OpenAI)
		out := ToOpenAIChatRequest(ctx, bifrostReq)
		require.NotNil(t, out)
		require.Same(t, bifrostReq.Params.ResponseFormat, out.ResponseFormat)
	})
}

func gjsonString(raw []byte, path string) string {
	return providerUtils.GetJSONField(raw, path).String()
}

func gjsonExists(raw []byte, path string) bool {
	return providerUtils.JSONFieldExists(raw, path)
}

// BenchmarkToOpenAIChatRequest_DeepSeekResponseFormat measures the DeepSeek
// conversion with no response_format (the common case, which must not pay for
// the downgrade) and with a json_schema response_format (which does).
// BenchmarkToOpenAIChatRequest_BytePlusReasoning measures the BytePlus compat case against the
// generic default branch it replaces, so the reasoning rewrite's cost is visible per request shape.
func BenchmarkToOpenAIChatRequest_BytePlusReasoning(b *testing.B) {
	for _, tc := range []struct {
		name     string
		provider schemas.ModelProvider
		effort   *string
	}{
		{"default_no_reasoning", schemas.Nebius, nil},
		{"byteplus_no_reasoning", schemas.BytePlus, nil},
		{"default_effort_low", schemas.Nebius, schemas.Ptr("low")},
		{"byteplus_effort_low", schemas.BytePlus, schemas.Ptr("low")},
		{"byteplus_effort_none", schemas.BytePlus, schemas.Ptr("none")},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: tc.provider,
				Model:    "seed-2-0-lite-260228",
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			}
			if tc.effort != nil {
				bifrostReq.Params = &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: tc.effort}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ToOpenAIChatRequest(ctx, bifrostReq)
			}
		})
	}
}

func BenchmarkToOpenAIChatRequest_DeepSeekResponseFormat(b *testing.B) {
	plainBody := `{"model":"deepseek-v4-flash","messages":[{"role":"system","content":"You extract people."},{"role":"user","content":"Ada is 36."}]}`
	for _, tc := range []struct{ name, body string }{
		{"no_response_format", plainBody},
		{"json_schema", deepSeekJSONSchemaBody},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx, bifrostReq := deepSeekJSONSchemaRequest(b, tc.body, schemas.DeepSeek)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ToOpenAIChatRequest(ctx, bifrostReq)
			}
		})
	}
}

// TestOpenAIChatRequestUnknownFields pins which top-level chat fields count as
// unknown: every field OpenAIChatRequest decodes (its own, ChatParameters', the
// reasoning_* shorthands, fallbacks and extra_params) is known, anything else
// such as Azure's data_sources is returned compacted, and a body with only
// known fields allocates nothing.
func TestOpenAIChatRequestUnknownFields(t *testing.T) {
	known := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":8,` +
		`"max_completion_tokens":8,"temperature":0.1,"top_p":1,"tools":[],"tool_choice":"auto","response_format":{"type":"text"},` +
		`"reasoning_effort":"low","reasoning_max_tokens":5,"reasoning_display":"summarized","reasoning":{"effort":"low"},` +
		`"prompt_cache_isolation_key":"k","fallbacks":["openai/gpt-4o"],"extra_params":{"a":1},"stream_options":{"include_usage":true},` +
		`"web_search_options":{},"user":"u","service_tier":"auto","context_management":{}}`)
	require.Nil(t, OpenAIChatRequestUnknownFields(known))
	require.Nil(t, OpenAIChatRequestUnknownFields(nil))
	allocs := testing.AllocsPerRun(100, func() { _ = OpenAIChatRequestUnknownFields(known) })
	require.Zero(t, allocs, "a body with only known fields must not allocate")

	body := []byte(`{"model":"gpt-4o","messages":[],"data_sources":[ {"type": "azure_search", "parameters": {"index_name": "idx"}} ],"x_new":7}`)
	got := OpenAIChatRequestUnknownFields(body)
	require.Len(t, got, 2)
	require.Equal(t, json.RawMessage(`[{"type":"azure_search","parameters":{"index_name":"idx"}}]`), got["data_sources"])
	require.Equal(t, json.RawMessage(`7`), got["x_new"])

	// The kept names and values must not alias the body, which fasthttp reuses.
	copy(body, strings.Repeat("x", len(body)))
	require.Equal(t, json.RawMessage(`7`), got["x_new"])
	require.Contains(t, got, "data_sources")

	// sonic matches field names case-insensitively, so these spellings are decoded into the
	// typed fields; forwarding them as well would send each one to the provider twice.
	folded := []byte(`{"MODEL":"gpt-4o","Messages":[{"role":"user","content":"hi"}],"Temperature":0.2,"Reasoning_Effort":"low","data_sources":[]}`)
	var decoded OpenAIChatRequest
	require.NoError(t, sonic.Unmarshal(folded, &decoded))
	require.Equal(t, "gpt-4o", decoded.Model, "precondition: sonic folds the case of MODEL")
	require.Len(t, decoded.Messages, 1, "precondition: sonic folds the case of Messages")
	got = OpenAIChatRequestUnknownFields(folded)
	require.Equal(t, []string{"data_sources"}, slices.Sorted(maps.Keys(got)), "only the field sonic does not decode is unknown")
	foldedKnown := []byte(`{"MODEL":"gpt-4o","Messages":[{"role":"user","content":"hi"}],"Temperature":0.2,"Reasoning_Effort":"low"}`)
	require.Nil(t, OpenAIChatRequestUnknownFields(foldedKnown))
	allocs = testing.AllocsPerRun(100, func() { _ = OpenAIChatRequestUnknownFields(foldedKnown) })
	require.Zero(t, allocs, "case-folded known fields must not allocate either")
}

// BenchmarkOpenAIChatRequestUnknownFields measures the top-level scan on a
// typical chat body with no unknown fields.
func BenchmarkOpenAIChatRequestUnknownFields(b *testing.B) {
	body := []byte(`{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"temperature":0.2,"max_tokens":64,"stream":true,"stream_options":{"include_usage":true}}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = OpenAIChatRequestUnknownFields(body)
	}
}

// TestToOpenAIChatRequest_MidConversationSystemFromDatasheet pins that a datasheet row with
// supports_mid_conversation_system_messages=false turns system/developer messages after the
// leading run into <system-reminder> user turns, and that a silent row leaves them alone.
func TestToOpenAIChatRequest_MidConversationSystemFromDatasheet(t *testing.T) {
	const model = "accounts/fireworks/models/qwen3p8-max"
	input := []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("be brief")}},
		{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("no emoji")}},
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("answer in french"), CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
		}}},
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("how are you")}},
	}
	newReq := func() *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{Provider: schemas.Fireworks, Model: model, Input: slices.Clone(input)}
	}

	t.Run("datasheet_false_inlines_mid_conversation_system", func(t *testing.T) {
		installCapabilityRecord(t, model, &schemas.ModelCapabilities{SupportsMidConversationSystem: schemas.Ptr(false)})
		req := newReq()
		out := ToOpenAIChatRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req)
		require.Len(t, out.Messages, 6)
		require.Equal(t, schemas.ChatMessageRoleSystem, out.Messages[0].Role)
		require.Equal(t, schemas.ChatMessageRoleDeveloper, out.Messages[1].Role)
		inlined := out.Messages[4]
		require.Equal(t, schemas.ChatMessageRoleUser, inlined.Role)
		require.Len(t, inlined.Content.ContentBlocks, 1)
		require.Equal(t, "<system-reminder>\nanswer in french\n</system-reminder>\n", *inlined.Content.ContentBlocks[0].Text)
		require.NotNil(t, inlined.Content.ContentBlocks[0].CacheControl)
		// The caller's input is not rewritten.
		require.Equal(t, schemas.ChatMessageRoleSystem, req.Input[4].Role)
		require.Equal(t, "answer in french", *req.Input[4].Content.ContentBlocks[0].Text)
	})

	t.Run("datasheet_silent_keeps_mid_conversation_system", func(t *testing.T) {
		installCapabilityRecord(t, model, nil)
		out := ToOpenAIChatRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), newReq())
		require.Equal(t, schemas.ChatMessageRoleSystem, out.Messages[4].Role)
	})

	// A custom provider has no row of its own, so it reads the base provider's row.
	t.Run("custom_provider_falls_back_to_base_row", func(t *testing.T) {
		installProviderCapabilityRecords(t, model, map[schemas.ModelProvider]*schemas.ModelCapabilities{
			schemas.Fireworks: {SupportsMidConversationSystem: schemas.Ptr(false)},
		})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Fireworks)
		req := newReq()
		req.Provider = "my-fireworks"
		out := ToOpenAIChatRequest(ctx, req)
		require.Equal(t, schemas.ChatMessageRoleUser, out.Messages[4].Role)
	})
}

// installProviderCapabilityRecords installs a capability resolver keyed by provider for one model.
func installProviderCapabilityRecords(t *testing.T, model string, records map[schemas.ModelProvider]*schemas.ModelCapabilities) {
	t.Helper()
	schemas.SetCapabilityResolver(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if m != model {
			return nil
		}
		return records[p]
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

// stubDeveloperRoleDatasheet marks glm-5 as rejecting the developer role and
// qwen3-dev as accepting it; every other model has no row.
func stubDeveloperRoleDatasheet(t *testing.T) {
	rows := map[string]bool{"glm-5": false, "qwen3-dev": true}
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		supported, ok := rows[model]
		if !ok {
			return nil
		}
		return &schemas.ModelCapabilities{SupportsDeveloperRole: schemas.Ptr(supported)}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

func developerChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Be brief.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
	}
}

func TestToOpenAIChatRequest_DeveloperRoleAsSystem(t *testing.T) {
	stubDeveloperRoleDatasheet(t)
	req := developerChatRequest(schemas.Fireworks, "glm-5")
	result := ToOpenAIChatRequest(nil, req)
	require.NotNil(t, result)
	require.Equal(t, schemas.ChatMessageRoleSystem, result.Messages[0].Role)
	require.Equal(t, schemas.ChatMessageRoleUser, result.Messages[1].Role)
	require.Equal(t, schemas.ChatMessageRoleDeveloper, req.Input[0].Role)

	result = ToOpenAIChatRequest(nil, developerChatRequest(schemas.Fireworks, "qwen3-dev"))
	require.Equal(t, schemas.ChatMessageRoleDeveloper, result.Messages[0].Role)
}

// TestToOpenAIChatRequest_DeveloperRoleAndMidConversationSystem pins that the two gates act
// independently and compose: developer unsupported sends every developer message as system,
// mid-conversation system unsupported turns every system/developer message after the leading
// run into a <system-reminder> user turn.
func TestToOpenAIChatRequest_DeveloperRoleAndMidConversationSystem(t *testing.T) {
	const model = "test-model"
	text := func(role schemas.ChatMessageRole, s string) schemas.ChatMessage {
		return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(s)}}
	}
	input := []schemas.ChatMessage{
		text(schemas.ChatMessageRoleDeveloper, "lead dev"),
		text(schemas.ChatMessageRoleSystem, "lead sys"),
		text(schemas.ChatMessageRoleUser, "hi"),
		text(schemas.ChatMessageRoleDeveloper, "mid dev"),
		text(schemas.ChatMessageRoleSystem, "mid sys"),
		text(schemas.ChatMessageRoleUser, "bye"),
	}
	const (
		dev  = schemas.ChatMessageRoleDeveloper
		sys  = schemas.ChatMessageRoleSystem
		user = schemas.ChatMessageRoleUser
	)
	tests := []struct {
		name           string
		developerRole  bool
		midConvSystem  bool
		wantRoles      []schemas.ChatMessageRole
		wantMidInlined bool
	}{
		{"both_supported", true, true, []schemas.ChatMessageRole{dev, sys, user, dev, sys, user}, false},
		{"developer_unsupported", false, true, []schemas.ChatMessageRole{sys, sys, user, sys, sys, user}, false},
		{"mid_conversation_unsupported", true, false, []schemas.ChatMessageRole{dev, sys, user, user, user, user}, true},
		{"both_unsupported", false, false, []schemas.ChatMessageRole{sys, sys, user, user, user, user}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installCapabilityRecord(t, model, &schemas.ModelCapabilities{
				SupportsDeveloperRole:         schemas.Ptr(tt.developerRole),
				SupportsMidConversationSystem: schemas.Ptr(tt.midConvSystem),
			})
			req := &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: model, Input: slices.Clone(input)}
			out := ToOpenAIChatRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req)
			require.Len(t, out.Messages, len(tt.wantRoles))
			for i, want := range tt.wantRoles {
				require.Equal(t, want, out.Messages[i].Role, "message %d", i)
			}
			require.Equal(t, "lead dev", *out.Messages[0].Content.ContentStr)
			if tt.wantMidInlined {
				require.Equal(t, "<system-reminder>\nmid dev\n</system-reminder>\n", *out.Messages[3].Content.ContentStr)
				require.Equal(t, "<system-reminder>\nmid sys\n</system-reminder>\n", *out.Messages[4].Content.ContentStr)
			} else {
				require.Equal(t, "mid dev", *out.Messages[3].Content.ContentStr)
			}
			require.Equal(t, dev, req.Input[0].Role)
			require.Equal(t, dev, req.Input[3].Role)
		})
	}
}

// TestInlineChatMidConversationSystem_NilContent pins that a mid-conversation system or developer
// message without content still becomes a user turn, while the leading run is left alone.
func TestInlineChatMidConversationSystem_NilContent(t *testing.T) {
	out := inlineChatMidConversationSystem([]OpenAIMessage{
		{Role: schemas.ChatMessageRoleSystem},
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		{Role: schemas.ChatMessageRoleDeveloper, Name: schemas.Ptr("ops")},
	})
	require.Equal(t, schemas.ChatMessageRoleSystem, out[0].Role)
	require.Equal(t, schemas.ChatMessageRoleUser, out[2].Role)
	require.Equal(t, "ops", *out[2].Name)
	require.Nil(t, out[2].Content)
}
