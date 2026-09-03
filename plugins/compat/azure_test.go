package compat

import (
	"slices"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// newDeepSeekResponsesRequest builds a Responses request carrying reasoning.effort.
func newDeepSeekResponsesRequest(provider schemas.ModelProvider, model string, requestType schemas.RequestType) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: requestType,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: provider,
			Model:    model,
			Params: &schemas.ResponsesParameters{
				Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high")},
			},
		},
	}
}

// TestAzureDeepSeekResponsesRouting covers the split between Azure's two DeepSeek
// endpoints: /v1/responses has web search but rejects reasoning.effort, and
// /v1/chat/completions is the reverse. Coding harnesses are routed to chat with
// their reasoning intact; every other caller keeps Responses minus reasoning.
func TestAzureDeepSeekResponsesRouting(t *testing.T) {
	const model = "DeepSeek-V3.1"

	tests := []struct {
		name            string
		provider        schemas.ModelProvider
		model           string
		requestType     schemas.RequestType
		userAgent       string
		wantConverted   bool
		wantReasoning   bool
		wantDropListHas bool
	}{
		{
			name:          "claude code is routed through chat completions with reasoning intact",
			provider:      schemas.Azure,
			model:         model,
			requestType:   schemas.ResponsesRequest,
			userAgent:     "claude-cli/2.1.168 (external, cli)",
			wantConverted: true,
			wantReasoning: true,
		},
		{
			name:          "codex streaming is routed through chat completions",
			provider:      schemas.Azure,
			model:         model,
			requestType:   schemas.ResponsesStreamRequest,
			userAgent:     "codex-cli/0.5.0",
			wantConverted: true,
			wantReasoning: true,
		},
		{
			name:            "other callers keep responses and lose the reasoning azure rejects",
			provider:        schemas.Azure,
			model:           model,
			requestType:     schemas.ResponsesRequest,
			userAgent:       "python-httpx/0.27.0",
			wantConverted:   false,
			wantReasoning:   false,
			wantDropListHas: true,
		},
		{
			name:          "non-deepseek azure model is untouched",
			provider:      schemas.Azure,
			model:         "gpt-4o",
			requestType:   schemas.ResponsesRequest,
			userAgent:     "claude-cli/2.1.168 (external, cli)",
			wantConverted: false,
			wantReasoning: true,
		},
		{
			name:          "deepseek on its own provider is untouched",
			provider:      schemas.DeepSeek,
			model:         "deepseek-reasoner",
			requestType:   schemas.ResponsesRequest,
			userAgent:     "claude-cli/2.1.168 (external, cli)",
			wantConverted: false,
			wantReasoning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestPlugin(t, map[string][]string{tt.model: {"reasoning", "temperature"}})
			ctx := newTestContext()
			ctx.SetValue(schemas.BifrostContextKeyUserAgent, tt.userAgent)

			got, _, err := p.PreLLMHook(ctx, newDeepSeekResponsesRequest(tt.provider, tt.model, tt.requestType))
			if err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}

			changeType, converted := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType)
			converted = converted && changeType == schemas.ChatCompletionRequest
			if converted != tt.wantConverted {
				t.Errorf("converted to chat completions = %v, want %v", converted, tt.wantConverted)
			}

			hasReasoning := got.ResponsesRequest.Params.Reasoning != nil
			if hasReasoning != tt.wantReasoning {
				t.Errorf("reasoning preserved = %v, want %v", hasReasoning, tt.wantReasoning)
			}

			dropped, _ := ctx.Value(schemas.BifrostContextKeyCompatDroppedParams).([]string)
			if got := slices.Contains(dropped, "reasoning"); got != tt.wantDropListHas {
				t.Errorf("dropped params %v contains reasoning = %v, want %v", dropped, got, tt.wantDropListHas)
			}
		})
	}
}

func TestAzureDeepSeekResponsesRoutingDisabled(t *testing.T) {
	const model = "DeepSeek-V3.1"

	ds := datasheet.NewTestStore(nil)
	ds.SetSupportedParamsForTest(map[string][]string{model: {"reasoning", "temperature"}})
	p, err := Init(
		Config{ShouldDropParams: true, AzureDeepseek: false},
		bifrost.NewNoOpLogger(),
		modelcatalog.NewTestCatalogWithDatasheet(ds),
	)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx := newTestContext()
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, "claude-cli/2.1.168 (external, cli)")

	got, _, hookErr := p.PreLLMHook(ctx, newDeepSeekResponsesRequest(schemas.Azure, model, schemas.ResponsesRequest))
	if hookErr != nil {
		t.Fatalf("PreLLMHook: %v", hookErr)
	}

	if _, converted := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType); converted {
		t.Error("request was converted to chat completions with the toggle off")
	}
	if got.ResponsesRequest.Params.Reasoning != nil {
		t.Error("reasoning survived, but Azure's Responses endpoint rejects it")
	}
	dropped, _ := ctx.Value(schemas.BifrostContextKeyCompatDroppedParams).([]string)
	if !slices.Contains(dropped, "reasoning") {
		t.Errorf("dropped params %v does not contain reasoning", dropped)
	}
}

// The x-bf-compat header sets BifrostContextKeyCompatAzureDeepseek, which turns the
// conversion on for a single request even though the plugin config has it off.
func TestAzureDeepSeekResponsesRoutingHeaderOverride(t *testing.T) {
	const model = "DeepSeek-V3.1"

	ds := datasheet.NewTestStore(nil)
	ds.SetSupportedParamsForTest(map[string][]string{model: {"reasoning", "temperature"}})
	p, err := Init(
		Config{ShouldDropParams: true, AzureDeepseek: false},
		bifrost.NewNoOpLogger(),
		modelcatalog.NewTestCatalogWithDatasheet(ds),
	)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx := newTestContext()
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, "claude-cli/2.1.168 (external, cli)")
	ctx.SetValue(schemas.BifrostContextKeyCompatAzureDeepseek, true)

	if _, _, hookErr := p.PreLLMHook(ctx, newDeepSeekResponsesRequest(schemas.Azure, model, schemas.ResponsesRequest)); hookErr != nil {
		t.Fatalf("PreLLMHook: %v", hookErr)
	}

	changed, converted := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType)
	if !converted || changed != schemas.ChatCompletionRequest {
		t.Errorf("request was not converted to chat completions, got %v (set: %v)", changed, converted)
	}
}
