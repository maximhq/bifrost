package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Issue #6779: the OpenAI provider with a custom base_url (Ollama, vLLM, llama.cpp,
// LM Studio, LiteLLM, ...) must forward the caller's reasoning block unchanged. The
// OpenAI model-name allowlist only describes api.openai.com; the self-hosted
// backend decides what it accepts.
func TestToOpenAIResponsesRequest_CustomBaseURLKeepsReasoning(t *testing.T) {
	// String literal on purpose: the test must compile against the current tree,
	// and pins the wire name the schemas constant must carry.
	const providerBaseURLKey = schemas.BifrostContextKey("bifrost-provider-base-url")

	newReq := func(model string) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Input: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("What is 2+2?")},
			}},
			Params: &schemas.ResponsesParameters{
				Reasoning: &schemas.ResponsesParametersReasoning{
					Effort:  schemas.Ptr(schemas.ReasoningEffortNone),
					Summary: schemas.Ptr("auto"),
				},
			},
		}
	}

	tests := []struct {
		name          string
		baseURL       string
		model         string
		wantReasoning bool
	}{
		{
			name:          "ollama base url keeps effort none for qwen3",
			baseURL:       "http://127.0.0.1:11434/v1",
			model:         "qwen3:8b",
			wantReasoning: true,
		},
		{
			name:          "vllm base url keeps effort none for deepseek",
			baseURL:       "http://vllm.internal:8000/v1",
			model:         "deepseek-r1",
			wantReasoning: true,
		},
		{
			name:          "hosted openai still strips reasoning for gpt-4o",
			baseURL:       "https://api.openai.com",
			model:         "gpt-4o",
			wantReasoning: false,
		},
		{
			name:          "no base url on context still strips reasoning for gpt-4o",
			baseURL:       "",
			model:         "gpt-4o",
			wantReasoning: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			if tt.baseURL != "" {
				ctx.SetValue(providerBaseURLKey, tt.baseURL)
			}

			req := ToOpenAIResponsesRequest(ctx, newReq(tt.model))
			require.NotNil(t, req)

			if !tt.wantReasoning {
				require.Nil(t, req.Reasoning, "reasoning must be stripped for non-reasoning model on api.openai.com")
				return
			}
			require.NotNil(t, req.Reasoning, "reasoning block was dropped before reaching the custom base_url backend")
			require.NotNil(t, req.Reasoning.Effort)
			require.Equal(t, schemas.ReasoningEffortNone, *req.Reasoning.Effort)
			require.NotNil(t, req.Reasoning.Summary)
			require.Equal(t, "auto", *req.Reasoning.Summary)
		})
	}
}
