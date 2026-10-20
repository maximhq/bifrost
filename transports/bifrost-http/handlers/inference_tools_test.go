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
		`{"model":"deepseek/deepseek-chat","query":"go generics","max_results":3,"include_content":true,"include_domains":["go.dev"],"exclude_domains":["example.com"],"published_after":"2026-01-01","published_before":"2026-10-01T00:00:00Z"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.DeepSeek {
		t.Fatalf("provider = %q, want deepseek", req.Provider)
	}
	if req.Model != "deepseek-chat" {
		t.Fatalf("model = %q, want the routing model without its provider prefix", req.Model)
	}
	if req.MaxResults == nil || *req.MaxResults != 3 {
		t.Fatalf("max_results = %v, want 3", req.MaxResults)
	}
	if req.IncludeContent == nil || !*req.IncludeContent {
		t.Fatalf("include_content = %v, want true", req.IncludeContent)
	}
	if len(req.IncludeDomains) != 1 || req.IncludeDomains[0] != "go.dev" {
		t.Fatalf("include_domains = %v", req.IncludeDomains)
	}
	if len(req.ExcludeDomains) != 1 || req.ExcludeDomains[0] != "example.com" {
		t.Fatalf("exclude_domains = %v", req.ExcludeDomains)
	}
	if req.PublishedAfter == nil || *req.PublishedAfter != "2026-01-01" {
		t.Fatalf("published_after = %v", req.PublishedAfter)
	}
	if req.PublishedBefore == nil || *req.PublishedBefore != "2026-10-01T00:00:00Z" {
		t.Fatalf("published_before = %v", req.PublishedBefore)
	}
}

// A provider's own options are not part of the neutral shape; they reach the provider in
// ExtraParams, which is where DeepSeek reads ori_question and localization from.
func TestPrepareSearchRequest_CarriesProviderOptionsInExtraParams(t *testing.T) {
	req, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search",
		`{"query":"q","ori_question":"how do generics work","localization":"203.0.113.7"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ExtraParams["ori_question"] != "how do generics work" {
		t.Fatalf("ori_question = %v, want it in ExtraParams", req.ExtraParams["ori_question"])
	}
	if req.ExtraParams["localization"] != "203.0.113.7" {
		t.Fatalf("localization = %v, want it in ExtraParams", req.ExtraParams["localization"])
	}
}

func TestPrepareSearchRequest_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing query", body: `{}`},
		{name: "blank query", body: `{"query":"   "}`},
		{name: "published_after not a date", body: `{"query":"q","published_after":"last week"}`},
		{name: "published_before not a date", body: `{"query":"q","published_before":"2026-13-01"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search", tc.body), nil); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestPrepareSearchRequest_SendsTrimmedQuery(t *testing.T) {
	req, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search", `{"query":"  bifrost gateway  "}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Query != "bifrost gateway" {
		t.Fatalf("query = %q, want the trimmed query", req.Query)
	}
}

// Query length and result bounds differ by provider, so the edge leaves them to the provider:
// a 129-character query is DeepSeek's to refuse, and another provider accepts it.
func TestPrepareSearchRequest_LeavesProviderBoundsToTheProvider(t *testing.T) {
	body := `{"query":"` + strings.Repeat("a", 129) + `","max_results":100}`
	if _, err := prepareSearchRequest(jsonToolsCtx("/v1/tools/search", body), nil); err != nil {
		t.Fatalf("provider-specific bounds must not be enforced at the edge: %v", err)
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
