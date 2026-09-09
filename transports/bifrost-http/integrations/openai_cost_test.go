package integrations

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

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

	t.Run("chat without cost passes through the same pointer", func(t *testing.T) {
		resp := &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{TotalTokens: 15}}
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
