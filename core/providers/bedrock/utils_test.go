package bedrock

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func headerMetadataCtx(headers map[string]string) *schemas.BifrostContext {
	ctx := testBedrockCtx()
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	return ctx
}

func TestApplyHeaderRequestMetadata(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		existing map[string]string
		want     map[string]string
	}{
		{
			name: "copies prefixed headers only",
			headers: map[string]string{
				"x-bf-bedrock-metadata-tenant_slug": "acme",
				"x-bf-bedrock-metadata-trace-id":    "abc123",
				"x-bf-lh-user-id":                   "user-1",
			},
			want: map[string]string{"tenant_slug": "acme", "trace-id": "abc123"},
		},
		{
			name:     "extra_params value wins",
			headers:  map[string]string{"x-bf-bedrock-metadata-tenant_slug": "header"},
			existing: map[string]string{"tenant_slug": "body"},
			want:     map[string]string{"tenant_slug": "body"},
		},
		{
			name: "skips values bedrock would reject",
			headers: map[string]string{
				"x-bf-bedrock-metadata-user": "auth0|123",
				"x-bf-bedrock-metadata-ok":   "fine",
			},
			want: map[string]string{"ok": "fine"},
		},
		{
			name: "no headers leaves metadata unset",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &BedrockConverseRequest{RequestMetadata: tt.existing}
			applyHeaderRequestMetadata(headerMetadataCtx(tt.headers), req)
			assert.Equal(t, tt.want, req.RequestMetadata)
		})
	}
}

func TestApplyHeaderRequestMetadataCapsEntries(t *testing.T) {
	headers := make(map[string]string)
	for i := range 20 {
		headers[fmt.Sprintf("x-bf-bedrock-metadata-k%02d", i)] = "v"
	}
	req := &BedrockConverseRequest{}
	applyHeaderRequestMetadata(headerMetadataCtx(headers), req)
	want := make(map[string]string, maxRequestMetadataEntries)
	for i := range maxRequestMetadataEntries {
		want[fmt.Sprintf("k%02d", i)] = "v"
	}
	assert.Equal(t, want, req.RequestMetadata)
}

func TestApplyHeaderRequestMetadataLeavesBodyMapAlone(t *testing.T) {
	body := map[string]string{"tenant_slug": "body"}
	for _, trace := range []string{"first", "second"} {
		req := &BedrockConverseRequest{RequestMetadata: body}
		applyHeaderRequestMetadata(headerMetadataCtx(map[string]string{"x-bf-bedrock-metadata-trace_id": trace}), req)
		assert.Equal(t, map[string]string{"tenant_slug": "body", "trace_id": trace}, req.RequestMetadata)
	}
	assert.Equal(t, map[string]string{"tenant_slug": "body"}, body)
}

// TestConverseRequestsCarryHeaderMetadata pins the wiring in each Converse request
// builder, and that CountTokens, whose Converse input has no requestMetadata, stays clean.
func TestConverseRequestsCarryHeaderMetadata(t *testing.T) {
	var mu sync.Mutex
	var sent []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"captured"}`))
	}))
	defer ts.Close()

	provider := newTestProviderWithServer(t, ts)
	key := testBedrockKey()
	drain := func(stream chan *schemas.BifrostStreamChunk, err *schemas.BifrostError) {
		if err == nil {
			for range stream {
			}
		}
	}
	calls := []struct {
		name string
		call func(ctx *schemas.BifrostContext)
		path func(wire map[string]any) map[string]any
	}{
		{"ChatCompletion", func(ctx *schemas.BifrostContext) { _, _ = provider.ChatCompletion(ctx, key, testChatRequest()) }, nil},
		{"ChatCompletionStream", func(ctx *schemas.BifrostContext) {
			drain(provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, testChatRequest()))
		}, nil},
		{"Responses", func(ctx *schemas.BifrostContext) { _, _ = provider.Responses(ctx, key, testResponsesRequest()) }, nil},
		{"ResponsesStream", func(ctx *schemas.BifrostContext) {
			drain(provider.ResponsesStream(ctx, noopPostHookRunner, nil, key, testResponsesRequest()))
		}, nil},
		{"CountTokens", func(ctx *schemas.BifrostContext) { _, _ = provider.CountTokens(ctx, key, testResponsesRequest()) },
			func(wire map[string]any) map[string]any {
				return wire["input"].(map[string]any)["converse"].(map[string]any)
			}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			sent = nil
			mu.Unlock()
			tc.call(headerMetadataCtx(map[string]string{"x-bf-bedrock-metadata-tenant_slug": "acme"}))

			mu.Lock()
			defer mu.Unlock()
			var wire map[string]any
			require.NoError(t, json.Unmarshal(sent, &wire), "body sent: %s", sent)
			if tc.path != nil {
				assert.NotContains(t, tc.path(wire), "requestMetadata")
				return
			}
			assert.Equal(t, map[string]any{"tenant_slug": "acme"}, wire["requestMetadata"])
		})
	}
}

func BenchmarkConverseRequestHeaderMetadata(b *testing.B) {
	headers := map[string]string{
		"content-type":        "application/json",
		"accept":              "application/json",
		"authorization":       "Bearer sk-test",
		"user-agent":          "benchmark",
		"x-bf-lh-tenant-slug": "acme",
		"x-bf-lh-workload":    "chat",
		"x-bf-lh-trace-id":    "abc123",
		"x-request-id":        "req-1",
	}
	tagged := maps.Clone(headers)
	tagged["x-bf-bedrock-metadata-tenant_slug"] = "acme"
	tagged["x-bf-bedrock-metadata-workload"] = "chat"
	tagged["x-bf-bedrock-metadata-trace_id"] = "abc123"

	for _, bc := range []struct {
		name    string
		headers map[string]string
		apply   bool
	}{
		{"builder only (before)", headers, false},
		{"no metadata headers", headers, true},
		{"three metadata headers", tagged, true},
	} {
		b.Run(bc.name, func(b *testing.B) {
			ctx := headerMetadataCtx(bc.headers)
			req := testChatRequest()
			b.ReportAllocs()
			for b.Loop() {
				bedrockReq, err := ToBedrockChatCompletionRequest(ctx, req)
				if bc.apply {
					_, err = withHeaderRequestMetadata(ctx, bedrockReq, err)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
