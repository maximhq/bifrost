package compat

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func newTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func newResponsesRequest(provider schemas.ModelProvider, model string, params *schemas.ResponsesParameters) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType:      schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: provider, Model: model, Params: params},
	}
}

// TestDropUnsupportedParams_MaxOutputTokens verifies that the Responses-API
// max_output_tokens cap is preserved whenever the (chat-named) model parameter
// catalog authorizes the token cap under any spelling
// (max_output_tokens / max_tokens / max_completion_tokens), and dropped only
// when none of them is supported.
func TestDropUnsupportedParams_MaxOutputTokens(t *testing.T) {
	tests := []struct {
		name          string
		provider      schemas.ModelProvider
		model         string
		supported     []string
		wantPreserved bool
	}{
		{
			name:          "openai gpt-4o-mini lists chat max_tokens only",
			provider:      schemas.OpenAI,
			model:         "gpt-4o-mini",
			supported:     []string{"temperature", "top_p", "max_tokens", "stop", "tools"},
			wantPreserved: true,
		},
		{
			name:          "openai o4-mini lists max_completion_tokens",
			provider:      schemas.OpenAI,
			model:         "o4-mini",
			supported:     []string{"temperature", "max_completion_tokens"},
			wantPreserved: true,
		},
		{
			name:          "model lists max_output_tokens explicitly",
			provider:      schemas.OpenAI,
			model:         "gpt-oss-120b",
			supported:     []string{"temperature", "max_output_tokens"},
			wantPreserved: true,
		},
		{
			name:          "anthropic claude lists max_tokens",
			provider:      schemas.Anthropic,
			model:         "claude-sonnet-4",
			supported:     []string{"temperature", "max_tokens", "top_p"},
			wantPreserved: true,
		},
		{
			name:          "vertex gemini lists max_tokens",
			provider:      schemas.Vertex,
			model:         "gemini-2.5-pro",
			supported:     []string{"temperature", "max_tokens"},
			wantPreserved: true,
		},
		{
			name:          "no token cap supported drops max_output_tokens",
			provider:      schemas.OpenAI,
			model:         "no-token-cap-model",
			supported:     []string{"temperature", "top_p"},
			wantPreserved: false,
		},
		{
			name:          "unknown model with empty catalog drops max_output_tokens",
			provider:      schemas.OpenAI,
			model:         "unknown-model",
			supported:     []string{},
			wantPreserved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newResponsesRequest(tt.provider, tt.model, &schemas.ResponsesParameters{
				MaxOutputTokens: schemas.Ptr(16),
				Temperature:     schemas.Ptr(0.5),
			})

			dropped := dropUnsupportedParams(newTestContext(), req, tt.supported)
			got := req.ResponsesRequest.Params.MaxOutputTokens

			if tt.wantPreserved {
				if got == nil {
					t.Fatalf("max_output_tokens = dropped, want preserved (supported=%v)", tt.supported)
				}
				if *got != 16 {
					t.Errorf("max_output_tokens = %d, want 16", *got)
				}
				if slices.Contains(dropped, "max_output_tokens") {
					t.Errorf("max_output_tokens reported in dropped=%v, want absent", dropped)
				}
				return
			}

			if got != nil {
				t.Fatalf("max_output_tokens = %d, want dropped (supported=%v)", *got, tt.supported)
			}
			if !slices.Contains(dropped, "max_output_tokens") {
				t.Errorf("max_output_tokens not reported in dropped=%v, want present", dropped)
			}
		})
	}
}

// TestDropUnsupportedParams_MaxOutputTokensSurgical verifies the fix is
// surgical: within one Responses request, max_output_tokens is preserved (chat
// token cap supported) while an unsupported sibling (top_p) is still dropped.
func TestDropUnsupportedParams_MaxOutputTokensSurgical(t *testing.T) {
	req := newResponsesRequest(schemas.OpenAI, "gpt-4o-mini", &schemas.ResponsesParameters{
		MaxOutputTokens: schemas.Ptr(16),
		TopP:            schemas.Ptr(0.9),
		Temperature:     schemas.Ptr(0.5),
	})

	dropped := dropUnsupportedParams(newTestContext(), req, []string{"max_tokens", "temperature"})

	p := req.ResponsesRequest.Params
	if p.MaxOutputTokens == nil {
		t.Errorf("max_output_tokens = dropped, want preserved")
	}
	if p.TopP != nil {
		t.Errorf("top_p = preserved, want dropped")
	}
	if p.Temperature == nil {
		t.Errorf("temperature = dropped, want preserved")
	}
	if !slices.Contains(dropped, "top_p") {
		t.Errorf("top_p not reported in dropped=%v, want present", dropped)
	}
}

// TestDropUnsupportedParams_ChatMaxCompletionTokensUnchanged guards the
// pre-existing chat-branch behaviour that the Responses fix mirrors.
func TestDropUnsupportedParams_ChatMaxCompletionTokensUnchanged(t *testing.T) {
	newChat := func() *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Params:   &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(16)},
			},
		}
	}

	keep := newChat()
	dropUnsupportedParams(newTestContext(), keep, []string{"max_tokens"})
	if keep.ChatRequest.Params.MaxCompletionTokens == nil {
		t.Errorf("max_completion_tokens = dropped, want preserved (max_tokens supported)")
	}

	drop := newChat()
	dropUnsupportedParams(newTestContext(), drop, []string{"temperature"})
	if drop.ChatRequest.Params.MaxCompletionTokens != nil {
		t.Errorf("max_completion_tokens = preserved, want dropped (no token cap supported)")
	}
}

// TestDropUnsupportedParams_ServiceTier pins service_tier passthrough on both
// request paths. A dropped service_tier is invisible to the caller - the
// provider simply serves the request at its default tier and bills it as such -
// so this guards that the param survives whenever the catalog allowlists it.
func TestDropUnsupportedParams_ServiceTier(t *testing.T) {
	tests := []struct {
		name          string
		supported     []string
		wantPreserved bool
	}{
		{
			name:          "catalog lists service_tier",
			supported:     []string{"temperature", "service_tier"},
			wantPreserved: true,
		},
		{
			name:          "catalog omits service_tier",
			supported:     []string{"temperature"},
			wantPreserved: false,
		},
		{
			name:          "empty catalog",
			supported:     []string{},
			wantPreserved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/chat", func(t *testing.T) {
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-5.4",
					Params: &schemas.ChatParameters{
						ServiceTier: schemas.Ptr(schemas.BifrostServiceTierPriority),
						Temperature: schemas.Ptr(0.5),
					},
				},
			}

			dropped := dropUnsupportedParams(newTestContext(), req, tt.supported)
			assertServiceTier(t, req.ChatRequest.Params.ServiceTier, dropped, tt.wantPreserved, tt.supported)
		})

		t.Run(tt.name+"/responses", func(t *testing.T) {
			req := newResponsesRequest(schemas.OpenAI, "gpt-5.4", &schemas.ResponsesParameters{
				ServiceTier: schemas.Ptr(schemas.BifrostServiceTierPriority),
				Temperature: schemas.Ptr(0.5),
			})

			dropped := dropUnsupportedParams(newTestContext(), req, tt.supported)
			assertServiceTier(t, req.ResponsesRequest.Params.ServiceTier, dropped, tt.wantPreserved, tt.supported)
		})
	}
}

func assertServiceTier(t *testing.T, got *schemas.BifrostServiceTier, dropped []string, wantPreserved bool, supported []string) {
	t.Helper()

	if wantPreserved {
		if got == nil {
			t.Fatalf("service_tier = dropped, want preserved (supported=%v)", supported)
		}
		if *got != schemas.BifrostServiceTierPriority {
			t.Errorf("service_tier = %q, want %q", *got, schemas.BifrostServiceTierPriority)
		}
		if slices.Contains(dropped, "service_tier") {
			t.Errorf("service_tier reported in dropped=%v, want absent", dropped)
		}
		return
	}

	if got != nil {
		t.Fatalf("service_tier = %q, want dropped (supported=%v)", *got, supported)
	}
	if !slices.Contains(dropped, "service_tier") {
		t.Errorf("service_tier not reported in dropped=%v, want present", dropped)
	}
}

func TestDropUnsupportedParams_ChatReasoningWithUnsupportedTools(t *testing.T) {
	newChat := func() *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "reasoning-no-tools-model",
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{},
					Tools: []schemas.ChatTool{{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:        "get_weather",
							Description: schemas.Ptr("Returns weather"),
						},
					}},
				},
			},
		}
	}

	preserveReasoning := newChat()
	dropped := dropUnsupportedParams(newTestContext(), preserveReasoning, []string{"reasoning"})
	if preserveReasoning.ChatRequest.Params.Reasoning == nil {
		t.Fatalf("reasoning = dropped, want preserved when tools are also dropped")
	}
	if preserveReasoning.ChatRequest.Params.Tools != nil {
		t.Fatalf("tools = preserved, want dropped")
	}
	if slices.Contains(dropped, "reasoning") {
		t.Errorf("reasoning reported in dropped=%v, want absent", dropped)
	}
	if !slices.Contains(dropped, "tools") {
		t.Errorf("tools not reported in dropped=%v, want present", dropped)
	}

	dropReasoning := newChat()
	dropped = dropUnsupportedParams(newTestContext(), dropReasoning, []string{"reasoning", "tools", "supports_none_reasoning_effort"})
	if dropReasoning.ChatRequest.Params.Reasoning == nil {
		t.Fatalf("reasoning = nil, want forced to effort=none when tools survive without reasoning_with_tool_calls")
	}
	if got := dropReasoning.ChatRequest.Params.Reasoning.Effort; got == nil || *got != "none" {
		t.Fatalf("reasoning.effort = %v, want \"none\"", got)
	}
	if dropReasoning.ChatRequest.Params.Tools == nil {
		t.Fatalf("tools = dropped, want preserved")
	}
	if !slices.Contains(dropped, "reasoning") {
		t.Errorf("reasoning not reported in dropped=%v, want present", dropped)
	}

	dropReasoningNoNoneSupport := newChat()
	dropped = dropUnsupportedParams(newTestContext(), dropReasoningNoNoneSupport, []string{"reasoning", "tools"})
	if dropReasoningNoNoneSupport.ChatRequest.Params.Reasoning != nil {
		t.Fatalf("reasoning = %v, want dropped to nil when model doesn't support effort=none", dropReasoningNoNoneSupport.ChatRequest.Params.Reasoning)
	}
	if !slices.Contains(dropped, "reasoning") {
		t.Errorf("reasoning not reported in dropped=%v, want present", dropped)
	}
}

// A chat request that sets reasoning.mode on OpenAI/Azure is routed to the Responses
// API by core, where reasoning and tools coexist, so the chat-only tools rewrite must
// not erase it. Other providers keep the existing rewrite.
func TestDropUnsupportedParams_ChatReasoningModeSurvivesUnsupportedTools(t *testing.T) {
	newChat := func(provider schemas.ModelProvider) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    "reasoning-no-tools-model",
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{Effort: new("high"), Mode: new("pro")},
					Tools: []schemas.ChatTool{{
						Type:     schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{Name: "get_weather"},
					}},
				},
			},
		}
	}
	supported := []string{"reasoning", "tools", "supports_none_reasoning_effort"}

	// "my-openai" is a custom key: its base is only known at dispatch, so compat keeps
	// reasoning and lets core route it or report the drop.
	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Azure, "my-openai"} {
		req := newChat(provider)
		dropped := dropUnsupportedParams(newTestContext(), req, supported)
		reasoning := req.ChatRequest.Params.Reasoning
		if reasoning == nil || reasoning.Mode == nil || *reasoning.Mode != "pro" || reasoning.Effort == nil || *reasoning.Effort != "high" {
			t.Fatalf("%s: reasoning = %+v, want effort=high mode=pro preserved for Responses routing", provider, reasoning)
		}
		if slices.Contains(dropped, "reasoning") {
			t.Errorf("%s: reasoning reported in dropped=%v, want absent", provider, dropped)
		}
	}

	other := newChat(schemas.Groq)
	dropped := dropUnsupportedParams(newTestContext(), other, supported)
	if got := other.ChatRequest.Params.Reasoning; got == nil || got.Effort == nil || *got.Effort != "none" || got.Mode != nil {
		t.Fatalf("groq: reasoning = %+v, want forced to effort=none", got)
	}
	if !slices.Contains(dropped, "reasoning") {
		t.Errorf("groq: reasoning not reported in dropped=%v, want present", dropped)
	}
}

func TestDropUnsupportedParams_ChatReasoningNilForcedToNoneWithUnsupportedTools(t *testing.T) {
	newChatNoReasoning := func() *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "reasoning-no-tools-model",
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:        "get_weather",
							Description: schemas.Ptr("Returns weather"),
						},
					}},
				},
			},
		}
	}

	forceNone := newChatNoReasoning()
	dropped := dropUnsupportedParams(newTestContext(), forceNone, []string{"reasoning", "tools", "supports_none_reasoning_effort"})
	if forceNone.ChatRequest.Params.Reasoning == nil {
		t.Fatalf("reasoning = nil, want forced to effort=none when model reasons by default and doesn't support reasoning with tools")
	}
	if got := forceNone.ChatRequest.Params.Reasoning.Effort; got == nil || *got != "none" {
		t.Fatalf("reasoning.effort = %v, want \"none\"", got)
	}
	if forceNone.ChatRequest.Params.Tools == nil {
		t.Fatalf("tools = dropped, want preserved")
	}
	if !slices.Contains(dropped, "reasoning") {
		t.Errorf("reasoning not reported in dropped=%v, want present", dropped)
	}

	noNoneSupport := newChatNoReasoning()
	dropped = dropUnsupportedParams(newTestContext(), noNoneSupport, []string{"reasoning", "tools"})
	if noNoneSupport.ChatRequest.Params.Reasoning != nil {
		t.Fatalf("reasoning = %v, want left nil when model doesn't support effort=none", noNoneSupport.ChatRequest.Params.Reasoning)
	}
	if slices.Contains(dropped, "reasoning") {
		t.Errorf("reasoning reported in dropped=%v, want absent since it was never set", dropped)
	}

	reasoningWithToolsSupported := newChatNoReasoning()
	dropped = dropUnsupportedParams(newTestContext(), reasoningWithToolsSupported, []string{"reasoning", "tools", "reasoning_with_tool_calls", "supports_none_reasoning_effort"})
	if reasoningWithToolsSupported.ChatRequest.Params.Reasoning != nil {
		t.Fatalf("reasoning = %v, want left nil when reasoning_with_tool_calls is supported", reasoningWithToolsSupported.ChatRequest.Params.Reasoning)
	}
	if slices.Contains(dropped, "reasoning") {
		t.Errorf("reasoning reported in dropped=%v, want absent since it was never set", dropped)
	}
}

// TestDropUnsupportedParams_ReasoningSummary pins the summary flags, with effort always
// kept: a detailed/concise summary the model does not support becomes "auto" when
// supports_reasoning_summary_auto holds and is dropped otherwise (Fireworks models on
// Azure Responses accept "auto" and 400 on "concise"/"detailed"), and an unsupported
// "auto" is dropped.
func TestDropUnsupportedParams_ReasoningSummary(t *testing.T) {
	all := []string{"reasoning", "reasoning_summary_detailed", "reasoning_summary_concise", "reasoning_summary_auto"}
	autoOnly := []string{"reasoning", "reasoning_summary_auto"}
	tests := []struct {
		name        string
		supported   []string
		summary     string
		want        string
		wantDropped bool
	}{
		{name: "all supported keeps detailed", supported: all, summary: "detailed", want: "detailed"},
		{name: "all supported keeps concise", supported: all, summary: "concise", want: "concise"},
		{name: "auto only converts detailed", supported: autoOnly, summary: "detailed", want: "auto"},
		{name: "auto only converts concise", supported: autoOnly, summary: "concise", want: "auto"},
		{name: "auto only keeps auto", supported: autoOnly, summary: "auto", want: "auto"},
		{name: "concise unsupported keeps detailed", supported: []string{"reasoning", "reasoning_summary_detailed", "reasoning_summary_auto"}, summary: "detailed", want: "detailed"},
		{name: "concise unsupported converts concise", supported: []string{"reasoning", "reasoning_summary_detailed", "reasoning_summary_auto"}, summary: "concise", want: "auto"},
		{name: "no auto drops detailed", supported: []string{"reasoning"}, summary: "detailed", wantDropped: true},
		{name: "no auto drops auto", supported: []string{"reasoning", "reasoning_summary_detailed", "reasoning_summary_concise"}, summary: "auto", wantDropped: true},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/responses", func(t *testing.T) {
			req := newResponsesRequest(schemas.Azure, "FW-GLM-5.3", &schemas.ResponsesParameters{
				Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high"), Summary: schemas.Ptr(tt.summary)},
			})

			dropped := dropUnsupportedParams(newTestContext(), req, tt.supported)
			reasoning := req.ResponsesRequest.Params.Reasoning
			if reasoning == nil || reasoning.Effort == nil || *reasoning.Effort != "high" {
				t.Fatalf("reasoning = %+v, want effort=high kept", reasoning)
			}
			assertReasoningSummary(t, reasoning.Summary, dropped, tt.want, tt.wantDropped)
		})

		t.Run(tt.name+"/chat", func(t *testing.T) {
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.Azure,
					Model:    "FW-GLM-5.3",
					Params: &schemas.ChatParameters{
						Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high"), Summary: schemas.Ptr(tt.summary)},
					},
				},
			}

			dropped := dropUnsupportedParams(newTestContext(), req, tt.supported)
			reasoning := req.ChatRequest.Params.Reasoning
			if reasoning == nil || reasoning.Effort == nil || *reasoning.Effort != "high" {
				t.Fatalf("reasoning = %+v, want effort=high kept", reasoning)
			}
			assertReasoningSummary(t, reasoning.Summary, dropped, tt.want, tt.wantDropped)
		})
	}
}

func assertReasoningSummary(t *testing.T, got *string, dropped []string, want string, wantDropped bool) {
	t.Helper()
	if wantDropped {
		if got != nil {
			t.Fatalf("reasoning.summary = %q, want dropped", *got)
		}
		if !slices.Contains(dropped, "reasoning.summary") {
			t.Errorf("reasoning.summary not reported in dropped=%v, want present", dropped)
		}
		return
	}
	if got == nil || *got != want {
		gotValue := "<nil>"
		if got != nil {
			gotValue = *got
		}
		t.Fatalf("reasoning.summary = %q, want %q", gotValue, want)
	}
	if slices.Contains(dropped, "reasoning.summary") {
		t.Errorf("reasoning.summary reported in dropped=%v, want absent", dropped)
	}
}
