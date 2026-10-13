package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TestResolveBatchProvider covers the three resolution paths introduced to make
// model optional on POST /v1/batches (OpenAI spec: model lives in the JSONL body).
func TestResolveBatchProvider(t *testing.T) {
	config := &lib.Config{}

	cases := []struct {
		name         string
		model        string
		header       string // x-model-provider; empty = unset
		query        string // ?provider=; empty = unset
		wantProvider string
		wantModel    string
		wantErrMsg   string // non-empty = error expected, substring match
	}{
		{
			name:         "model field: provider+model parsed",
			model:        "openai/gpt-4o-mini",
			wantProvider: "openai",
			wantModel:    "gpt-4o-mini",
		},
		{
			name:         "no model, x-model-provider header",
			header:       "openai",
			wantProvider: "openai",
			wantModel:    "",
		},
		{
			name:         "no model, ?provider= query param",
			query:        "anthropic",
			wantProvider: "anthropic",
			wantModel:    "",
		},
		{
			name:       "no model, no provider → error",
			wantErrMsg: "provider query parameter or x-model-provider header is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tc.header != "" {
				ctx.Request.Header.Set("x-model-provider", tc.header)
			}
			if tc.query != "" {
				ctx.QueryArgs().Set("provider", tc.query)
			}

			provider, modelName, err := resolveBatchProvider(ctx, config, tc.model)

			if tc.wantErrMsg != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrMsg)
				}
				if !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(provider) != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", provider, tc.wantProvider)
			}
			if modelName != tc.wantModel {
				t.Fatalf("modelName = %q, want %q", modelName, tc.wantModel)
			}
		})
	}
}

// A batch create names its work once: an uploaded file, a blob, or inline requests. No provider
// runs inline items beside a file (OpenAI, Azure and Bedrock submit the file and drop them,
// Gemini and Vertex refuse the mix), so the gateway refuses the mix before anything runs rather
// than let a client's inline items vanish.
func TestBatchCreateRefusesFileAndInlineRequestsTogether(t *testing.T) {
	h := &CompletionHandler{config: &lib.Config{}}
	cases := []struct {
		name string
		body string
		want int
	}{
		{"file beside inline requests", `{"model":"openai/gpt-4o-mini","input_file_id":"file-x","endpoint":"/v1/chat/completions","requests":[{"custom_id":"a","body":{"model":"gpt-4o-mini"}}]}`, fasthttp.StatusBadRequest},
		{"blob beside inline requests", `{"model":"azure/gpt-4o-mini","input_blob":"https://blob.example/in.jsonl","endpoint":"/v1/chat/completions","requests":[{"custom_id":"a","body":{"model":"gpt-4o-mini"}}]}`, fasthttp.StatusBadRequest},
		{"nothing named", `{"model":"openai/gpt-4o-mini","endpoint":"/v1/chat/completions"}`, fasthttp.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.SetBodyString(tc.body)

			h.batchCreate(ctx)

			if ctx.Response.StatusCode() != tc.want {
				t.Fatalf("status = %d, want %d: %s", ctx.Response.StatusCode(), tc.want, ctx.Response.Body())
			}
			if tc.name != "nothing named" && !strings.Contains(string(ctx.Response.Body()), "not both") {
				t.Fatalf("the refusal should say the inputs are exclusive: %s", ctx.Response.Body())
			}
		})
	}
}
