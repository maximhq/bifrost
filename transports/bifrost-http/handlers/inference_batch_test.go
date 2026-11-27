package handlers

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
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

// multipartFileUploadCtx builds a POST /v1/files?provider=openai request with the given form fields and a small file part.
func multipartFileUploadCtx(t *testing.T, fields [][2]string) *fasthttp.RequestCtx {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, f := range fields {
		if err := writer.WriteField(f[0], f[1]); err != nil {
			t.Fatalf("write field %s: %v", f[0], err)
		}
	}
	part, err := writer.CreateFormFile("file", "doc.pdf")
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write([]byte("%PDF-1.4 fake")); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/files?provider=openai")
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

// The native route must read expires_after[anchor] and expires_after[seconds] the way the /openai route does.
// Before, they fell into extra params, which the OpenAI provider never sends, so the file got no expiry.
func TestPrepareFileUploadRequest_ExpiresAfter(t *testing.T) {
	req, _, err := prepareFileUploadRequest(multipartFileUploadCtx(t, [][2]string{
		{"purpose", "user_data"},
		{"expires_after[anchor]", "created_at"},
		{"expires_after[seconds]", "3600"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ExpiresAfter == nil || req.ExpiresAfter.Anchor != "created_at" || req.ExpiresAfter.Seconds != 3600 {
		t.Fatalf("expires_after = %+v, want anchor created_at seconds 3600", req.ExpiresAfter)
	}
	for _, k := range []string{"expires_after[anchor]", "expires_after[seconds]"} {
		if _, leaked := req.ExtraParams[k]; leaked {
			t.Fatalf("%s leaked into extra params: %+v", k, req.ExtraParams)
		}
	}
}

func TestPrepareFileUploadRequest_NoExpiresAfter(t *testing.T) {
	req, _, err := prepareFileUploadRequest(multipartFileUploadCtx(t, [][2]string{{"purpose", "user_data"}}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ExpiresAfter != nil {
		t.Fatalf("expires_after = %+v, want nil", req.ExpiresAfter)
	}
}

func TestPrepareFileUploadRequest_InvalidExpiresAfterSeconds(t *testing.T) {
	_, status, err := prepareFileUploadRequest(multipartFileUploadCtx(t, [][2]string{
		{"purpose", "user_data"},
		{"expires_after[seconds]", "soon"},
	}))
	if err == nil || status != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, err = %v; want 400 for a non-numeric expires_after[seconds]", status, err)
	}
}
