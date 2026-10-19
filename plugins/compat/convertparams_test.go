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

// newConvertTestPlugin builds a plugin whose catalog holds pricing rows in memory
// and no capability records, which is what a deployment without a config store
// sees. Anthropic has one key aliasing team-claude to Haiku.
func newConvertTestPlugin(t *testing.T, cfg Config) *CompatPlugin {
	t.Helper()
	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "gemini-test-flash", Provider: string(schemas.Gemini), Mode: "chat", MaxOutputTokens: new(65536)},
		{Model: "gemini-nocap-flash", Provider: string(schemas.Gemini), Mode: "chat"},
		{Model: "gpt-4o", Provider: string(schemas.OpenAI), Mode: "chat", MaxOutputTokens: new(16384)},
	})
	mc := modelcatalog.NewTestCatalogWithDatasheet(ds)
	mc.SetKeyConfigForProvider(schemas.Anthropic, []schemas.Key{{
		ID:      "anthropic-key",
		Aliases: schemas.KeyAliases{"team-claude": {ModelID: "claude-haiku-4-5-20251001"}},
	}})
	p, err := Init(cfg, bifrost.NewNoOpLogger(), mc)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func newChatRequest(provider schemas.ModelProvider, model string, params *schemas.ChatParameters) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: provider, Model: model, Params: params},
	}
}

// TestConvertUnsupportedParamValues_FitsEachShape pins the cap for each request
// shape against the catalog's in-memory limit: gpt-4o has no capability record
// here, so 1000000 must still come down to the pricing data's 16384.
func TestConvertUnsupportedParamValues_FitsEachShape(t *testing.T) {
	const limit = 16384

	tests := []struct {
		name        string
		requested   *int
		want        *int
		wantChanges []string
	}{
		{name: "above limit is lowered", requested: new(1000000), want: new(limit), wantChanges: []string{"%s: 1000000 -> 16384"}},
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
				return newChatRequest(schemas.OpenAI, "gpt-4o", &schemas.ChatParameters{MaxCompletionTokens: v})
			},
			get: func(r *schemas.BifrostRequest) *int { return r.ChatRequest.Params.MaxCompletionTokens },
		},
		{
			param: "max_output_tokens",
			build: func(v *int) *schemas.BifrostRequest {
				return newResponsesRequest(schemas.OpenAI, "gpt-4o", &schemas.ResponsesParameters{MaxOutputTokens: v})
			},
			get: func(r *schemas.BifrostRequest) *int { return r.ResponsesRequest.Params.MaxOutputTokens },
		},
		{
			param: "max_tokens",
			build: func(v *int) *schemas.BifrostRequest {
				return &schemas.BifrostRequest{
					RequestType:           schemas.TextCompletionRequest,
					TextCompletionRequest: &schemas.BifrostTextCompletionRequest{Provider: schemas.OpenAI, Model: "gpt-4o", Params: &schemas.TextCompletionParameters{MaxTokens: v}},
				}
			},
			get: func(r *schemas.BifrostRequest) *int { return r.TextCompletionRequest.Params.MaxTokens },
		},
	}

	p := newConvertTestPlugin(t, Config{ShouldConvertParams: true})
	for _, shape := range shapes {
		for _, tt := range tests {
			t.Run(shape.param+"/"+tt.name, func(t *testing.T) {
				// Each subtest gets its own pointer: the table is shared across
				// shapes, and a fit on one must not leak into the next.
				var requested *int
				if tt.requested != nil {
					requested = new(*tt.requested)
				}
				req := shape.build(requested)
				changes := p.convertUnsupportedParamValues(req, schemas.OpenAI, "gpt-4o")

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

// TestConvertUnsupportedParamValues_FitsReasoningBudget pins the budget wiring on
// both shapes: scaled with a lowered cap, kept under it, and left alone when the
// caller's own cap stands.
func TestConvertUnsupportedParamValues_FitsReasoningBudget(t *testing.T) {
	p := newConvertTestPlugin(t, Config{ShouldConvertParams: true})

	t.Run("alias resolves to Claude and the budget scales with the cap", func(t *testing.T) {
		req := newChatRequest(schemas.Anthropic, "team-claude", &schemas.ChatParameters{
			MaxCompletionTokens: new(128000),
			Reasoning:           &schemas.ChatReasoning{MaxTokens: new(100000)},
		})
		changes := p.convertUnsupportedParamValues(req, schemas.Anthropic, "team-claude")
		params := req.ChatRequest.Params
		if intValue(params.MaxCompletionTokens) != 64000 || intValue(params.Reasoning.MaxTokens) != 50000 {
			t.Errorf("max = %v, budget = %v, want 64000 and 50000", intValue(params.MaxCompletionTokens), intValue(params.Reasoning.MaxTokens))
		}
		want := []string{"max_completion_tokens: 128000 -> 64000", "reasoning.max_tokens: 100000 -> 50000"}
		if !slices.Equal(changes, want) {
			t.Errorf("changes = %v, want %v", changes, want)
		}
	})

	t.Run("responses budget ends under the lowered cap", func(t *testing.T) {
		req := newResponsesRequest(schemas.Anthropic, "claude-haiku-4-5-20251001", &schemas.ResponsesParameters{
			MaxOutputTokens: new(200000),
			Reasoning:       &schemas.ResponsesParametersReasoning{MaxTokens: new(200000)},
		})
		p.convertUnsupportedParamValues(req, schemas.Anthropic, "claude-haiku-4-5-20251001")
		params := req.ResponsesRequest.Params
		if intValue(params.MaxOutputTokens) != 64000 || intValue(params.Reasoning.MaxTokens) != 63999 {
			t.Errorf("max = %v, budget = %v, want 64000 and 63999", intValue(params.MaxOutputTokens), intValue(params.Reasoning.MaxTokens))
		}
	})

	t.Run("caller's own cap and budget pairing is left alone", func(t *testing.T) {
		req := newChatRequest(schemas.OpenAI, "gpt-4o", &schemas.ChatParameters{
			MaxCompletionTokens: new(16000),
			Reasoning:           &schemas.ChatReasoning{MaxTokens: new(32000)},
		})
		if changes := p.convertUnsupportedParamValues(req, schemas.OpenAI, "gpt-4o"); len(changes) != 0 {
			t.Errorf("changes = %v, want none", changes)
		}
	})
}

// TestPluginConvertParamsFitsMaxOutputTokens is the user-reported case:
// max_output_tokens far above the model limit goes out unchanged and the
// provider rejects it. With should_convert_params on it is fitted on a copy,
// so the caller's request is left as sent.
func TestPluginConvertParamsFitsMaxOutputTokens(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		override   *bool
		model      string
		want       int
		wantBudget int
	}{
		{name: "config on fits", cfg: Config{ShouldConvertParams: true}, model: "gemini-test-flash", want: 65536, wantBudget: 6553},
		{name: "header override on fits", cfg: Config{}, override: new(true), model: "gemini-test-flash", want: 65536, wantBudget: 6553},
		{name: "off leaves the values", cfg: Config{}, model: "gemini-test-flash", want: 1000000, wantBudget: 100000},
		{name: "no catalog limit leaves the values", cfg: Config{ShouldConvertParams: true}, model: "gemini-nocap-flash", want: 1000000, wantBudget: 100000},
		{name: "unknown model leaves the values", cfg: Config{ShouldConvertParams: true}, model: "not-in-catalog", want: 1000000, wantBudget: 100000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newConvertTestPlugin(t, tt.cfg)
			ctx := newTestContext()
			if tt.override != nil {
				ctx.SetValue(schemas.BifrostContextKeyCompatShouldConvertParams, *tt.override)
			}
			orig := newResponsesRequest(schemas.Gemini, tt.model, &schemas.ResponsesParameters{
				MaxOutputTokens: new(1000000),
				Reasoning:       &schemas.ResponsesParametersReasoning{MaxTokens: new(100000)},
			})

			got, _, err := p.PreLLMHook(ctx, orig)
			if err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}
			if v := intValue(got.ResponsesRequest.Params.MaxOutputTokens); v != tt.want {
				t.Errorf("max_output_tokens = %v, want %d", v, tt.want)
			}
			if v := intValue(got.ResponsesRequest.Params.Reasoning.MaxTokens); v != tt.wantBudget {
				t.Errorf("reasoning.max_tokens = %v, want %d", v, tt.wantBudget)
			}
			if params := orig.ResponsesRequest.Params; intValue(params.MaxOutputTokens) != 1000000 || intValue(params.Reasoning.MaxTokens) != 100000 {
				t.Errorf("caller's request was mutated: max = %v, budget = %v", intValue(params.MaxOutputTokens), intValue(params.Reasoning.MaxTokens))
			}
		})
	}
}

// TestPluginConvertParamsFitsEachFallbackAttempt pins fallbacks: core builds each
// fallback as a shallow copy of the caller's request (sharing Params) and reruns
// the hook, so the fallback must be fitted from the caller's values, not the
// primary's fitted ones.
func TestPluginConvertParamsFitsEachFallbackAttempt(t *testing.T) {
	p := newConvertTestPlugin(t, Config{ShouldConvertParams: true})
	orig := newChatRequest(schemas.OpenAI, "gpt-4o", &schemas.ChatParameters{
		MaxCompletionTokens: new(128000),
		Reasoning:           &schemas.ChatReasoning{MaxTokens: new(100000)},
	})

	primary, _, err := p.PreLLMHook(newTestContext(), orig)
	if err != nil {
		t.Fatalf("primary PreLLMHook: %v", err)
	}
	// 100000 scaled by 16384/128000.
	if params := primary.ChatRequest.Params; intValue(params.MaxCompletionTokens) != 16384 || intValue(params.Reasoning.MaxTokens) != 12800 {
		t.Errorf("primary: max = %v, budget = %v, want 16384 and 12800", intValue(params.MaxCompletionTokens), intValue(params.Reasoning.MaxTokens))
	}

	// The fallback allows more output than the primary, so a leaked primary fit would show.
	fallbackChat := *orig.ChatRequest
	fallbackChat.Provider, fallbackChat.Model = schemas.Anthropic, "claude-haiku-4-5-20251001"
	fallback := *orig
	fallback.ChatRequest = &fallbackChat
	got, _, err := p.PreLLMHook(newTestContext(), &fallback)
	if err != nil {
		t.Fatalf("fallback PreLLMHook: %v", err)
	}
	if params := got.ChatRequest.Params; intValue(params.MaxCompletionTokens) != 64000 || intValue(params.Reasoning.MaxTokens) != 50000 {
		t.Errorf("fallback: max = %v, budget = %v, want 64000 and 50000", intValue(params.MaxCompletionTokens), intValue(params.Reasoning.MaxTokens))
	}
}
