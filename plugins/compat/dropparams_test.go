package compat

import (
	"context"
	"slices"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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

// TestPreLLMHook_ShouldDropParams_MaxOutputTokens exercises the user-facing
// compat.should_drop_params toggle end to end through PreLLMHook and the model
// catalog: with dropping enabled and a chat-named catalog (max_tokens only, the
// real gpt-4o-mini case) the Responses token cap must survive, and with dropping
// disabled the request is left untouched.
func TestPreLLMHook_ShouldDropParams_MaxOutputTokens(t *testing.T) {
	mc := modelcatalog.NewTestCatalogWithParams(nil, map[string][]string{
		"gpt-4o-mini": {"temperature", "max_tokens"},
	})
	logger := bifrost.NewNoOpLogger()

	// top_p is absent from the catalog (only temperature + max_tokens). Each
	// subtest checks it too, so the drop path is proven to actually run:
	// dropped when dropping is enabled, kept when it is disabled.
	t.Run("should_drop_params=true keeps max_output_tokens, drops unsupported params", func(t *testing.T) {
		p, err := Init(Config{ShouldDropParams: true}, logger, mc)
		if err != nil {
			t.Fatalf("Init() error = %v", err)
		}
		req := newResponsesRequest(schemas.OpenAI, "gpt-4o-mini", &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(16),
			TopP:            schemas.Ptr(0.9),
		})
		out, _, err := p.PreLLMHook(newTestContext(), req)
		if err != nil {
			t.Fatalf("PreLLMHook() error = %v", err)
		}
		if out.ResponsesRequest.Params.MaxOutputTokens == nil {
			t.Errorf("max_output_tokens = dropped, want preserved")
		}
		// Confirms the drop branch actually executed (not silently skipped).
		if out.ResponsesRequest.Params.TopP != nil {
			t.Errorf("top_p = preserved, want dropped (unsupported, dropping enabled)")
		}
	})

	t.Run("should_drop_params=false leaves request untouched", func(t *testing.T) {
		p, err := Init(Config{ShouldDropParams: false}, logger, mc)
		if err != nil {
			t.Fatalf("Init() error = %v", err)
		}
		req := newResponsesRequest(schemas.OpenAI, "gpt-4o-mini", &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(16),
			TopP:            schemas.Ptr(0.9),
		})
		out, _, err := p.PreLLMHook(newTestContext(), req)
		if err != nil {
			t.Fatalf("PreLLMHook() error = %v", err)
		}
		// Dropping disabled: every param survives untouched, even unsupported ones.
		if out.ResponsesRequest.Params.MaxOutputTokens == nil {
			t.Errorf("max_output_tokens = dropped, want preserved (dropping disabled)")
		}
		if out.ResponsesRequest.Params.TopP == nil {
			t.Errorf("top_p = dropped, want preserved (dropping disabled)")
		}
	})
}
