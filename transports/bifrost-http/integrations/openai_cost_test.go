package integrations

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Transport coverage focuses on dispatch and coexistence with cost/details.
func TestOpenAICompletionWirePreservesReportedTokenCounts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage schemas.BifrostLLMUsage
		want  string
	}{
		{"zero", schemas.BifrostLLMUsage{PromptTokensExplicitZero: true, CompletionTokensExplicitZero: true}, `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`},
		{"partial", schemas.BifrostLLMUsage{PromptTokensExplicitZero: true, TotalTokens: 12}, `{"prompt_tokens":0,"total_tokens":12}`},
	} {
		for _, withCost := range []bool{false, true} {
			usage := tc.usage.DeepCopy()
			usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{ReasoningTokens: 1}
			want := tc.want[:len(tc.want)-1] + `,"completion_tokens_details":{"reasoning_tokens":1}`
			variant := "/without cost"
			if withCost {
				variant = "/with cost"
				usage.Cost = &schemas.BifrostCost{TotalCost: 0.25}
				want += `,"cost":0.25`
			}
			want += `}`
			before := usage.DeepCopy()
			for name, response := range map[string]any{
				"chat": &schemas.BifrostChatResponse{ID: "usage-test", Usage: usage},
				"text": &schemas.BifrostTextCompletionResponse{ID: "usage-test", Usage: usage},
			} {
				t.Run(name+"/"+tc.name+variant, func(t *testing.T) {
					wire, err := schemas.MarshalSorted(openAIWireCostResponse(response))
					require.NoError(t, err)
					var body struct {
						ID    string
						Usage json.RawMessage
					}
					require.NoError(t, json.Unmarshal(wire, &body))
					require.Equal(t, "usage-test", body.ID)
					require.JSONEq(t, want, string(body.Usage))
				})
			}
			require.Equal(t, before, usage, "serialization must not mutate shared usage")
		}
	}
}

// TestOpenAIWireCostResponse verifies 2A: the OpenAI-compatible wire renders
// usage.cost as the bare total (float), the legacy shape SDKs accept, instead of
// the nested BifrostCost object. The rewrite wraps rather than mutates, so the
// shared response is left intact.
func TestOpenAIWireCostResponse(t *testing.T) {
	t.Run("chat renders cost as float total without mutating the shared response", func(t *testing.T) {
		cost := &schemas.BifrostCost{InputCost: 1, OutputCost: 2, TotalCost: 3}
		usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, Cost: cost}
		resp := &schemas.BifrostChatResponse{Usage: usage}

		out := openAIWireCostResponse(resp)
		if any(out) == any(resp) {
			t.Fatal("expected a wire wrapper, got the shared response")
		}
		js, err := sonic.Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(js)
		if !strings.Contains(s, `"cost":3`) {
			t.Errorf("wire cost is not the bare total float: %s", s)
		}
		if strings.Contains(s, "total_cost") || strings.Contains(s, "input_cost") {
			t.Errorf("wire still carries the nested cost object: %s", s)
		}
		if !strings.Contains(s, `"total_tokens":15`) {
			t.Errorf("usage fields lost in wrapper: %s", s)
		}

		// The shared response keeps its full breakdown object.
		if usage.Cost != cost {
			t.Error("shared response usage.cost was mutated")
		}
	})

	t.Run("responses renders cost as float total", func(t *testing.T) {
		resp := &schemas.BifrostResponsesResponse{
			Usage: &schemas.ResponsesResponseUsage{TotalTokens: 15, Cost: &schemas.BifrostCost{TotalCost: 3}},
		}
		js, err := sonic.Marshal(openAIWireCostResponse(resp))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if s := string(js); !strings.Contains(s, `"cost":3`) || strings.Contains(s, "total_cost") {
			t.Errorf("responses wire cost not flattened to float: %s", s)
		}
		if resp.Usage.Cost == nil {
			t.Error("shared responses usage.cost was mutated")
		}
	})

	t.Run("responses keeps tool_usage top-level beside the flattened cost", func(t *testing.T) {
		resp := &schemas.BifrostResponsesResponse{
			Usage: &schemas.ResponsesResponseUsage{
				TotalTokens: 15,
				Cost:        &schemas.BifrostCost{TotalCost: 3},
				ToolUsage:   &schemas.ToolUsage{WebSearch: &schemas.WebSearchToolUsage{NumRequests: 2}},
			},
		}
		js, err := sonic.Marshal(openAIWireCostResponse(resp))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var wire struct {
			ToolUsage *schemas.ToolUsage     `json:"tool_usage"`
			Usage     map[string]interface{} `json:"usage"`
		}
		if err := sonic.Unmarshal(js, &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if wire.ToolUsage == nil || wire.ToolUsage.WebSearch == nil || wire.ToolUsage.WebSearch.NumRequests != 2 {
			t.Errorf("top-level tool_usage missing: %s", js)
		}
		if _, ok := wire.Usage["tool_usage"]; ok {
			t.Errorf("usage must not carry tool_usage: %s", js)
		}
		if wire.Usage["cost"] != float64(3) {
			t.Errorf("responses wire cost not flattened to float: %s", js)
		}
	})

	t.Run("image renders cost as float total", func(t *testing.T) {
		resp := &schemas.BifrostImageGenerationResponse{
			Usage: &schemas.ImageUsage{Cost: &schemas.BifrostCost{TotalCost: 3}},
		}
		js, err := sonic.Marshal(openAIWireCostResponse(resp))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if s := string(js); !strings.Contains(s, `"cost":3`) || strings.Contains(s, "total_cost") {
			t.Errorf("image wire cost not flattened to float: %s", s)
		}
		if resp.Usage.Cost == nil {
			t.Error("shared image usage.cost was mutated")
		}
	})

	t.Run("chat keeps generated images beside the flattened cost", func(t *testing.T) {
		const images = `"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]`
		var resp schemas.BifrostChatResponse
		upstream := `{"id":"gen-1","object":"chat.completion","created":1,"model":"google/gemini-2.5-flash-image",` +
			`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Here you go",` + images + `}}]}`
		if err := schemas.Unmarshal([]byte(upstream), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		resp.Usage = &schemas.BifrostLLMUsage{TotalTokens: 15, Cost: &schemas.BifrostCost{TotalCost: 3}}
		js, err := sonic.Marshal(openAIWireCostResponse(&resp))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if s := string(js); !strings.Contains(s, images) || !strings.Contains(s, `"content":"Here you go"`) {
			t.Errorf("drop-in chat wire lost the generated image: %s", s)
		}
	})

	t.Run("chat without cost and with nonzero counts passes through the same pointer", func(t *testing.T) {
		resp := &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
		if out := openAIWireCostResponse(resp); any(out) != any(resp) {
			t.Error("cost-free response should pass through unchanged")
		}
	})

	t.Run("raw upstream payload passes through unchanged", func(t *testing.T) {
		raw := "raw-openai-json"
		if out := openAIWireCostResponse(raw); out != raw {
			t.Errorf("raw passthrough altered: %v", out)
		}
	})
}
