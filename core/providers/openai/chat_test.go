package openai

import (
	"encoding/json"
	"strings"
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

// TestCustomProviderExtraParamsForwardedAutomatically verifies that custom
// OpenAI-compatible providers preserve provider-specific extra params.
func TestCustomProviderExtraParamsForwardedAutomatically(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIsCustomProvider, true)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.ModelProvider("custom-openai"),
		Model:    "deepseek-v4",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"thinking": false,
				"chat_template_kwargs": map[string]interface{}{
					"enable_thinking": false,
				},
			},
		},
	}

	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		req,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToOpenAIChatRequest(ctx, req), nil
		},
	)
	if bifrostErr != nil {
		t.Fatalf("failed to build request body: %v", bifrostErr.Error.Message)
	}

	var body map[string]interface{}
	if err := sonic.Unmarshal(wireBody, &body); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if body["thinking"] != false {
		t.Fatalf("expected thinking=false, got %v", body["thinking"])
	}

	rawKwargs, ok := body["chat_template_kwargs"]
	if !ok {
		t.Fatal("chat_template_kwargs missing from outgoing request body")
	}
	kwargs, ok := rawKwargs.(map[string]interface{})
	if !ok {
		t.Fatalf("expected chat_template_kwargs to be an object, got %T", rawKwargs)
	}
	if kwargs["enable_thinking"] != false {
		t.Fatalf("expected enable_thinking=false, got %v", kwargs["enable_thinking"])
	}
}

func TestStandardProviderExtraParamsNotForwardedAutomatically(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"thinking": true,
				"chat_template_kwargs": map[string]interface{}{
					"enable_thinking": true,
				},
			},
		},
	}

	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		req,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToOpenAIChatRequest(ctx, req), nil
		},
	)
	if bifrostErr != nil {
		t.Fatalf("failed to build request body: %v", bifrostErr.Error.Message)
	}

	var body map[string]interface{}
	if err := sonic.Unmarshal(wireBody, &body); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if _, ok := body["thinking"]; ok {
		t.Fatal("thinking must not be forwarded for a standard provider")
	}
	if _, ok := body["chat_template_kwargs"]; ok {
		t.Fatal("chat_template_kwargs must not be forwarded for a standard provider")
	}
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
			name:     "maps minimal to low",
			model:    "gpt-5.4",
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
			name:     "maps minimal to low",
			model:    "gpt-5.4",
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

			req.filterOpenAISpecificParameters(req.Model)

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
		{name: "cerebras", provider: schemas.Cerebras, model: "gpt-oss-120b"},
		{name: "deepseek", provider: schemas.DeepSeek, model: "deepseek-v4-pro"},
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
			tt.request.applyXAICompatibility(tt.model)

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

// When a conversation switches from Gemini to OpenAI, Gemini's thoughtSignature is
// embedded in the tool call_id as "<baseID>_ts_<sig>" and can exceed OpenAI's 64-char
// limit. The chat converter must strip it to the base ID on the wire while leaving the
// caller's input intact (so a later Gemini turn can still recover the signature).
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
