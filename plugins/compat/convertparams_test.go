package compat

import (
	"fmt"
	"slices"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

func intValue(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestConvertUnsupportedParamValues_ClampsTokenCap pins the clamp for each
// request shape: a cap above the model limit is lowered to the limit, and a cap
// at or below it, or an absent one, is left alone.
func TestConvertUnsupportedParamValues_ClampsTokenCap(t *testing.T) {
	const limit = 65536

	tests := []struct {
		name        string
		requested   *int
		want        *int
		wantChanges []string
	}{
		{name: "above limit is clamped", requested: new(1000000), want: new(limit), wantChanges: []string{"%s: 1000000 -> 65536"}},
		{name: "at limit is untouched", requested: new(limit), want: new(limit)},
		{name: "below limit is untouched", requested: new(1024), want: new(1024)},
		{name: "absent stays absent", requested: nil, want: nil},
	}

	shapes := []struct {
		param string
		build func(v *int) *schemas.BifrostRequest
		get   func(r *schemas.BifrostRequest) *int
	}{
		{
			param: "max_completion_tokens",
			build: func(v *int) *schemas.BifrostRequest {
				return &schemas.BifrostRequest{
					RequestType: schemas.ChatCompletionRequest,
					ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.Gemini, Model: "m", Params: &schemas.ChatParameters{MaxCompletionTokens: v}},
				}
			},
			get: func(r *schemas.BifrostRequest) *int { return r.ChatRequest.Params.MaxCompletionTokens },
		},
		{
			param: "max_output_tokens",
			build: func(v *int) *schemas.BifrostRequest {
				return newResponsesRequest(schemas.Gemini, "m", &schemas.ResponsesParameters{MaxOutputTokens: v})
			},
			get: func(r *schemas.BifrostRequest) *int { return r.ResponsesRequest.Params.MaxOutputTokens },
		},
		{
			param: "max_tokens",
			build: func(v *int) *schemas.BifrostRequest {
				return &schemas.BifrostRequest{
					RequestType:           schemas.TextCompletionRequest,
					TextCompletionRequest: &schemas.BifrostTextCompletionRequest{Provider: schemas.Gemini, Model: "m", Params: &schemas.TextCompletionParameters{MaxTokens: v}},
				}
			},
			get: func(r *schemas.BifrostRequest) *int { return r.TextCompletionRequest.Params.MaxTokens },
		},
	}

	for _, shape := range shapes {
		for _, tt := range tests {
			t.Run(shape.param+"/"+tt.name, func(t *testing.T) {
				// Each subtest gets its own pointer: the table is shared across
				// shapes, and a clamp on one must not leak into the next.
				var requested *int
				if tt.requested != nil {
					requested = new(*tt.requested)
				}
				req := shape.build(requested)
				changes := convertUnsupportedParamValues(req, limit)

				if got := shape.get(req); intValue(got) != intValue(tt.want) {
					t.Errorf("%s = %v, want %v", shape.param, intValue(got), intValue(tt.want))
				}
				var want []string
				for _, c := range tt.wantChanges {
					want = append(want, fmt.Sprintf(c, shape.param))
				}
				if !slices.Equal(changes, want) {
					t.Errorf("changes = %v, want %v", changes, want)
				}
			})
		}
	}
}

// TestConvertUnsupportedParamValues_ReasoningBudgetStaysBelowCap guards the
// thinking budget. Anthropic rejects budget_tokens >= max_tokens, so lowering
// only the outer cap would swap one upstream 400 for another.
func TestConvertUnsupportedParamValues_ReasoningBudgetStaysBelowCap(t *testing.T) {
	const limit = 64000

	t.Run("chat budget above new cap is lowered", func(t *testing.T) {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Model: "m", Params: &schemas.ChatParameters{
				MaxCompletionTokens: new(200000),
				Reasoning:           &schemas.ChatReasoning{MaxTokens: new(100000)},
			}},
		}
		changes := convertUnsupportedParamValues(req, limit)
		if got := intValue(req.ChatRequest.Params.Reasoning.MaxTokens); got != limit-1 {
			t.Errorf("reasoning.max_tokens = %v, want %d", got, limit-1)
		}
		if !slices.Contains(changes, "reasoning.max_tokens: 100000 -> 63999") {
			t.Errorf("changes = %v, want the reasoning budget reported", changes)
		}
	})

	t.Run("responses budget above new cap is lowered", func(t *testing.T) {
		req := newResponsesRequest(schemas.Anthropic, "m", &schemas.ResponsesParameters{
			MaxOutputTokens: new(200000),
			Reasoning:       &schemas.ResponsesParametersReasoning{MaxTokens: new(64000)},
		})
		convertUnsupportedParamValues(req, limit)
		if got := intValue(req.ResponsesRequest.Params.Reasoning.MaxTokens); got != limit-1 {
			t.Errorf("reasoning.max_tokens = %v, want %d", got, limit-1)
		}
	})

	t.Run("budget below new cap is untouched", func(t *testing.T) {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Model: "m", Params: &schemas.ChatParameters{
				MaxCompletionTokens: new(200000),
				Reasoning:           &schemas.ChatReasoning{MaxTokens: new(8000)},
			}},
		}
		convertUnsupportedParamValues(req, limit)
		if got := intValue(req.ChatRequest.Params.Reasoning.MaxTokens); got != 8000 {
			t.Errorf("reasoning.max_tokens = %v, want 8000", got)
		}
	})

	t.Run("budget is untouched when the cap was not clamped", func(t *testing.T) {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Model: "m", Params: &schemas.ChatParameters{
				MaxCompletionTokens: new(16000),
				Reasoning:           &schemas.ChatReasoning{MaxTokens: new(32000)},
			}},
		}
		if changes := convertUnsupportedParamValues(req, limit); len(changes) != 0 {
			t.Errorf("changes = %v, want none - the caller's own cap/budget pairing is not ours to fix", changes)
		}
	})
}

// newConvertTestPlugin builds a plugin whose catalog knows one gemini row with
// a 65536 output-token limit.
func newConvertTestPlugin(t *testing.T, cfg Config) *CompatPlugin {
	t.Helper()
	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "gemini-test-flash", Provider: string(schemas.Gemini), Mode: "chat", MaxOutputTokens: new(65536)},
		{Model: "gemini-nocap-flash", Provider: string(schemas.Gemini), Mode: "chat"},
	})
	p, err := Init(cfg, bifrost.NewNoOpLogger(), modelcatalog.NewTestCatalogWithDatasheet(ds))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// TestPluginConvertParamsClampsMaxOutputTokens is the user-reported case:
// max_output_tokens far above the model limit goes out unchanged and the
// provider rejects it. With should_convert_params on it must be lowered to the
// catalog limit, on a copy so the caller's request is left as sent.
func TestPluginConvertParamsClampsMaxOutputTokens(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		override *bool
		model    string
		want     int
	}{
		{name: "config on clamps", cfg: Config{ShouldConvertParams: true}, model: "gemini-test-flash", want: 65536},
		{name: "header override on clamps", cfg: Config{}, override: new(true), model: "gemini-test-flash", want: 65536},
		{name: "off leaves the value", cfg: Config{}, model: "gemini-test-flash", want: 1000000},
		{name: "no catalog limit leaves the value", cfg: Config{ShouldConvertParams: true}, model: "gemini-nocap-flash", want: 1000000},
		{name: "unknown model leaves the value", cfg: Config{ShouldConvertParams: true}, model: "not-in-catalog", want: 1000000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newConvertTestPlugin(t, tt.cfg)
			ctx := newTestContext()
			if tt.override != nil {
				ctx.SetValue(schemas.BifrostContextKeyCompatShouldConvertParams, *tt.override)
			}
			orig := newResponsesRequest(schemas.Gemini, tt.model, &schemas.ResponsesParameters{MaxOutputTokens: new(1000000)})

			got, _, err := p.PreLLMHook(ctx, orig)
			if err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}
			if v := intValue(got.ResponsesRequest.Params.MaxOutputTokens); v != tt.want {
				t.Errorf("max_output_tokens = %v, want %d", v, tt.want)
			}
			if v := intValue(orig.ResponsesRequest.Params.MaxOutputTokens); v != 1000000 {
				t.Errorf("caller's request was mutated: max_output_tokens = %v, want 1000000", v)
			}
		})
	}
}
