package compat

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// lunaFixtureURL points at a local single-row snapshot of the public
// model-parameters datasheet: openai/gpt-6-luna as shipped (logprobs and
// top_logprobs listed only under conditionally_unsupported_fields, issue
// #8848) plus a variant that rejects them outright, so both drop paths are
// exercised against real rows.
func lunaFixtureURL(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "gpt-6-luna-model-parameters.json"))
	if err != nil {
		t.Fatalf("failed to resolve fixture path: %v", err)
	}
	return "file://" + abs
}

// newLunaFixturePlugin builds a drop-params-enabled plugin whose catalog is
// loaded from the fixture through the real sync path, so the allowlist is
// derived from the rows exactly as a deployment's would be.
func newLunaFixturePlugin(t *testing.T) *CompatPlugin {
	t.Helper()
	ds := datasheet.New(nil, bifrost.NewNoOpLogger(), datasheet.Config{
		URL:                lunaFixtureURL(t),
		ModelParametersURL: lunaFixtureURL(t),
	})
	if err := ds.LoadModelParamsFromURLIntoMemory(context.Background()); err != nil {
		t.Fatalf("LoadModelParamsFromURLIntoMemory: %v", err)
	}
	p, err := Init(Config{ShouldDropParams: true}, bifrost.NewNoOpLogger(), modelcatalog.NewTestCatalogWithDatasheet(ds))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// newLunaChatRequest mirrors the issue #8848 reproduction: a chat completion
// for openai/gpt-6-luna carrying logprobs/top_logprobs beside reasoning_effort.
func newLunaChatRequest(model string, effort string) *schemas.BifrostRequest {
	params := &schemas.ChatParameters{
		LogProbs:            schemas.Ptr(true),
		TopLogProbs:         schemas.Ptr(5),
		MaxCompletionTokens: schemas.Ptr(4),
	}
	if effort != "" {
		params.Reasoning = &schemas.ChatReasoning{Effort: schemas.Ptr(effort)}
	}
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Reply with one digit 1-6.")},
			}},
			Params: params,
		},
	}
}

// runCompatRound drives a request through PreLLMHook/PostLLMHook and returns
// the caller-facing params plus the reported dropped list.
func runCompatRound(t *testing.T, p *CompatPlugin, req *schemas.BifrostRequest) (*schemas.ChatParameters, []string) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	modified, _, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	if _, _, err := p.PostLLMHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	return modified.ChatRequest.Params, resp.ChatResponse.ExtraFields.DroppedCompatPluginParams
}

// TestPreLLMHook_KeepsLogprobsMarkedConditionallyUnsupported reproduces issue
// #8848: the gpt-6-luna datasheet row gates logprobs/top_logprobs behind
// "when_effort_none" instead of listing them as model parameters, and the
// derived allowlist therefore made the compat plugin drop them from every
// request — even at reasoning_effort "none", where OpenAI returns logprobs.
// The allowlist must carry conditionally-gated fields; the provider request
// shaper is what enforces the condition per request.
func TestPreLLMHook_KeepsLogprobsMarkedConditionallyUnsupported(t *testing.T) {
	p := newLunaFixturePlugin(t)

	params, dropped := runCompatRound(t, p, newLunaChatRequest("gpt-6-luna", "none"))
	if params.LogProbs == nil || !*params.LogProbs {
		t.Errorf("logprobs = dropped, want preserved at reasoning_effort \"none\" (reported dropped: %v)", dropped)
	}
	if params.TopLogProbs == nil || *params.TopLogProbs != 5 {
		t.Errorf("top_logprobs = dropped, want preserved at reasoning_effort \"none\" (reported dropped: %v)", dropped)
	}
	for _, name := range []string{"logprobs", "top_logprobs"} {
		if slices.Contains(dropped, name) {
			t.Errorf("dropped_compat_plugin_params contains %q, want absent", name)
		}
	}
}

// TestPreLLMHook_DropsLogprobsUnsupportedOutright guards the failure path: a
// row that rejects logprobs/top_logprobs outright (unsupported_fields, not the
// conditional label) must still have them dropped and reported.
func TestPreLLMHook_DropsLogprobsUnsupportedOutright(t *testing.T) {
	p := newLunaFixturePlugin(t)

	params, dropped := runCompatRound(t, p, newLunaChatRequest("gpt-6-luna-locked", "none"))
	if params.LogProbs != nil {
		t.Errorf("logprobs preserved for a model that rejects it outright, want dropped (reported: %v)", dropped)
	}
	if params.TopLogProbs != nil {
		t.Errorf("top_logprobs preserved for a model that rejects it outright, want dropped (reported: %v)", dropped)
	}
	for _, name := range []string{"logprobs", "top_logprobs"} {
		if !slices.Contains(dropped, name) {
			t.Errorf("dropped_compat_plugin_params = %v, want it to contain %q", dropped, name)
		}
	}
}
