package handlers

import (
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func jsonToolsCtx(path, body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI(path)
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBody([]byte(body))
	return ctx
}

// The DeepSeek tools contract carries no model, so a caller who omits it must still reach
// DeepSeek rather than fail with an empty provider.
func TestPrepareSearchRequest_DefaultsToDeepSeekWithoutAModel(t *testing.T) {
	req, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search", `{"query":"bifrost gateway"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.DeepSeek {
		t.Fatalf("provider = %q, want deepseek", req.Provider)
	}
	if req.Model != "" {
		t.Fatalf("model = %q, want empty: search carries no model", req.Model)
	}
}

func TestPrepareSearchRequest_CarriesOptionalFields(t *testing.T) {
	req, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search",
		`{"model":"deepseek/deepseek-chat","query":"lang:en after:2026-01-01 go generics","count":3,"ori_question":"how do generics work","localization":"203.0.113.7","need_content":true}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.DeepSeek {
		t.Fatalf("provider = %q, want deepseek", req.Provider)
	}
	if req.Model != "deepseek-chat" {
		t.Fatalf("model = %q, want the routing model without its provider prefix", req.Model)
	}
	if req.Count == nil || *req.Count != 3 {
		t.Fatalf("count = %v, want 3", req.Count)
	}
	if req.OriQuestion == nil || *req.OriQuestion != "how do generics work" {
		t.Fatalf("ori_question = %v", req.OriQuestion)
	}
	if req.Localization == nil || *req.Localization != "203.0.113.7" {
		t.Fatalf("localization = %v", req.Localization)
	}
	if req.NeedContent == nil || !*req.NeedContent {
		t.Fatalf("need_content = %v, want true", req.NeedContent)
	}
}

func TestPrepareSearchRequest_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing query", body: `{}`},
		{name: "blank query", body: `{"query":"   "}`},
		{name: "query too long", body: `{"query":"` + strings.Repeat("a", 129) + `"}`},
		{name: "count below range", body: `{"query":"q","count":0}`},
		{name: "count above range", body: `{"query":"q","count":51}`},
		{name: "blank ori_question", body: `{"query":"q","ori_question":"  "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search", tc.body), nil); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestPrepareSearchRequest_AcceptsMaximumQueryLength(t *testing.T) {
	body := `{"query":"` + strings.Repeat("a", 128) + `"}`
	if _, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search", body), nil); err != nil {
		t.Fatalf("128 characters is the documented maximum: %v", err)
	}
}

func TestPrepareFetchRequest_CarriesURL(t *testing.T) {
	req, err := prepareFetchRequest(jsonToolsCtx("/v1/tools/fetch", `{"url":"https://example.com/page"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.DeepSeek {
		t.Fatalf("provider = %q, want deepseek", req.Provider)
	}
	if req.URL != "https://example.com/page" {
		t.Fatalf("url = %q", req.URL)
	}
}

// Fetch runs on DeepSeek's servers, so the gateway validates only what the documented
// contract requires: an absolute http(s) URL with a host and no embedded credentials.
func TestPrepareFetchRequest_RejectsInvalidURLs(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "empty", url: ``},
		{name: "no scheme", url: `example.com/page`},
		{name: "file scheme", url: `file:///etc/passwd`},
		{name: "no host", url: `https://`},
		{name: "credentials", url: `https://user:pass@example.com/page`},
		{name: "too long", url: "https://example.com/" + strings.Repeat("a", 8192)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"url":"` + tc.url + `"}`
			if _, err := prepareFetchRequest(jsonToolsCtx("/v1/tools/fetch", body), nil); err == nil {
				t.Fatalf("expected %q to be rejected", tc.url)
			}
		})
	}
}
