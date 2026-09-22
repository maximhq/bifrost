package handlers

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func TestPrepareChatCompletionRequestStop(t *testing.T) {
	cases := []struct {
		name    string
		field   string
		want    []string
		wantErr bool
	}{
		{name: "absent"},
		{name: "null", field: `,"stop":null`},
		{name: "empty string", field: `,"stop":""`, want: []string{""}},
		{name: "string", field: `,"stop":"<END>"`, want: []string{"<END>"}},
		{name: "empty array", field: `,"stop":[]`, want: []string{}},
		{name: "array", field: `,"stop":["<END>","\n"]`, want: []string{"<END>", "\n"}},
		{name: "number", field: `,"stop":1`, wantErr: true},
		{name: "mixed array", field: `,"stop":["end",1]`, wantErr: true},
	}
	for _, tc := range cases {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, streaming), func(t *testing.T) {
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.Header.SetMethod("POST")
				ctx.Request.SetRequestURI("/v1/chat/completions")
				ctx.Request.Header.SetContentType("application/json")
				ctx.Request.SetBodyString(fmt.Sprintf(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":%t,"provider_option":true%s}`, streaming, tc.field))
				raw, req, err := prepareChatCompletionRequest(ctx, nil)
				if tc.wantErr {
					if err == nil || err.Error() != "Invalid request payload" {
						t.Fatalf("expected payload error, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("prepare request: %v", err)
				}
				if req.Provider != schemas.OpenAI || req.Model != "gpt-4o-mini" || raw.Stream == nil || *raw.Stream != streaming {
					t.Fatalf("request fields lost: %+v, stream=%v", req, raw.Stream)
				}
				if !reflect.DeepEqual(req.Params.Stop, tc.want) {
					t.Fatalf("stop = %#v, want %#v", req.Params.Stop, tc.want)
				}
				if _, exists := req.Params.ExtraParams["stop"]; exists {
					t.Fatal("stop duplicated into ExtraParams")
				}
				if req.Params.ExtraParams["provider_option"] != true {
					t.Fatal("unknown provider parameter lost")
				}
			})
		}
	}
}

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
