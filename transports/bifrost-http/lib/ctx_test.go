package lib

import (
	"context"
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestParseSessionIDFromBaggage(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "single member", header: "session-id=abc", want: "abc"},
		{name: "multiple members", header: "foo=bar, session-id=abc, baz=qux", want: "abc"},
		{name: "member with properties", header: "session-id=abc;ttl=60", want: "abc"},
		{name: "spaces preserved around parsing", header: " foo=bar , session-id = abc123 ;ttl=60 ", want: "abc123"},
		{name: "missing member", header: "foo=bar", want: ""},
		{name: "malformed ignored", header: "session-id, foo=bar", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseSessionIDFromBaggage(tt.header); got != tt.want {
				t.Fatalf("ParseSessionIDFromBaggage(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestConvertToBifrostContext_ReusesSharedContext(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	base := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	base.SetValue(schemas.BifrostContextKeyRequestID, "req-shared")
	ctx.SetUserValue(FastHTTPUserValueBifrostContext, base)

	converted, cancel := ConvertToBifrostContext(ctx, false, nil, schemas.WhiteList{})
	defer cancel()

	if converted == nil {
		t.Fatal("expected non-nil converted context")
	}
	if got, _ := converted.Value(schemas.BifrostContextKeyRequestID).(string); got != "req-shared" {
		t.Fatalf("expected converted context to preserve parent values, got request-id=%q", got)
	}
	if stored, ok := ctx.UserValue(FastHTTPUserValueBifrostContext).(*schemas.BifrostContext); !ok || stored == nil {
		t.Fatal("expected shared context pointer to be stored on fasthttp user values")
	}
	if ctx.UserValue(FastHTTPUserValueBifrostCancel) == nil {
		t.Fatal("expected shared cancel function to be stored on fasthttp user values")
	}
}

func TestConvertToBifrostContext_SecondCallReturnsSameSharedContext(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	first, cancelFirst := ConvertToBifrostContext(ctx, false, nil, schemas.WhiteList{})
	defer cancelFirst()
	if first == nil {
		t.Fatal("expected first context to be non-nil")
	}

	second, cancelSecond := ConvertToBifrostContext(ctx, false, nil, schemas.WhiteList{})
	defer cancelSecond()
	if second == nil {
		t.Fatal("expected second context to be non-nil")
	}
	if first != second {
		t.Fatal("expected ConvertToBifrostContext to reuse the shared context on repeated calls")
	}
}

// TestConvertToBifrostContext_StarAllowlistSecurityHeadersBlocked verifies that
// even with a "*" allowlist (allow all), the hardcoded security denylist in
// ConvertToBifrostContext still blocks security-sensitive headers.
func TestConvertToBifrostContext_StarAllowlistSecurityHeadersBlocked(t *testing.T) {
	matcher := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"*"},
	})

	ctx := &fasthttp.RequestCtx{}
	// x-bf-eh-* prefixed headers
	ctx.Request.Header.Set("x-bf-eh-custom-header", "allowed-value")
	ctx.Request.Header.Set("x-bf-eh-cookie", "should-be-blocked")
	ctx.Request.Header.Set("x-bf-eh-x-api-key", "should-be-blocked")
	ctx.Request.Header.Set("x-bf-eh-host", "should-be-blocked")
	ctx.Request.Header.Set("x-bf-eh-connection", "should-be-blocked")
	ctx.Request.Header.Set("x-bf-eh-proxy-authorization", "should-be-blocked")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, matcher, schemas.WhiteList{})
	defer cancel()

	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)

	// custom-header should be forwarded
	if _, ok := extraHeaders["custom-header"]; !ok {
		t.Error("expected custom-header to be forwarded via x-bf-eh- prefix")
	}

	// Security headers should be blocked even with * allowlist
	securityHeaders := []string{"cookie", "x-api-key", "host", "connection", "proxy-authorization"}
	for _, h := range securityHeaders {
		if _, ok := extraHeaders[h]; ok {
			t.Errorf("expected security header %q to be blocked even with * allowlist", h)
		}
	}
}

// TestConvertToBifrostContext_StarAllowlistDirectForwardingSecurityBlocked verifies
// that direct header forwarding with "*" allowlist forwards non-security headers
// but still blocks security headers.
func TestConvertToBifrostContext_StarAllowlistDirectForwardingSecurityBlocked(t *testing.T) {
	matcher := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"*"},
	})

	ctx := &fasthttp.RequestCtx{}
	// Direct headers (not prefixed with x-bf-eh-)
	ctx.Request.Header.Set("custom-header", "allowed-value")
	ctx.Request.Header.Set("anthropic-beta", "some-beta-feature")
	// Security headers sent directly — should be blocked
	ctx.Request.Header.Set("proxy-authorization", "should-be-blocked")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, matcher, schemas.WhiteList{})
	defer cancel()

	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)

	// Direct non-security headers should be forwarded when allowlist has *
	if _, ok := extraHeaders["custom-header"]; !ok {
		t.Error("expected custom-header to be forwarded directly")
	}
	if _, ok := extraHeaders["anthropic-beta"]; !ok {
		t.Error("expected anthropic-beta to be forwarded directly")
	}

	// Security headers should still be blocked in direct forwarding path
	directSecurityHeaders := []string{"proxy-authorization", "cookie", "host", "connection"}
	for _, h := range directSecurityHeaders {
		if _, ok := extraHeaders[h]; ok {
			t.Errorf("expected security header %q to be blocked in direct forwarding even with * allowlist", h)
		}
	}
}

// TestConvertToBifrostContext_PrefixWildcardDirectForwarding verifies that
// prefix wildcard patterns like "anthropic-*" work for direct header forwarding
// (without x-bf-eh- prefix).
func TestConvertToBifrostContext_PrefixWildcardDirectForwarding(t *testing.T) {
	matcher := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"anthropic-*"},
	})

	ctx := &fasthttp.RequestCtx{}
	// Direct headers matching the wildcard pattern
	ctx.Request.Header.Set("anthropic-beta", "beta-value")
	ctx.Request.Header.Set("anthropic-version", "2024-01-01")
	// Header not matching the pattern
	ctx.Request.Header.Set("openai-version", "should-not-forward")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, matcher, schemas.WhiteList{})
	defer cancel()

	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)

	if _, ok := extraHeaders["anthropic-beta"]; !ok {
		t.Error("expected anthropic-beta to be forwarded directly via wildcard allowlist")
	}
	if _, ok := extraHeaders["anthropic-version"]; !ok {
		t.Error("expected anthropic-version to be forwarded directly via wildcard allowlist")
	}
	if _, ok := extraHeaders["openai-version"]; ok {
		t.Error("expected openai-version to NOT be forwarded (doesn't match anthropic-*)")
	}
}

// TestConvertToBifrostContext_WildcardAllowlistFiltering verifies wildcard patterns
// correctly filter headers via the x-bf-eh- prefix path.
func TestConvertToBifrostContext_WildcardAllowlistFiltering(t *testing.T) {
	matcher := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"anthropic-*"},
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-eh-anthropic-beta", "beta-value")
	ctx.Request.Header.Set("x-bf-eh-anthropic-version", "2024-01-01")
	ctx.Request.Header.Set("x-bf-eh-openai-version", "should-be-blocked")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, matcher, schemas.WhiteList{})
	defer cancel()

	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)

	if _, ok := extraHeaders["anthropic-beta"]; !ok {
		t.Error("expected anthropic-beta to be forwarded")
	}
	if _, ok := extraHeaders["anthropic-version"]; !ok {
		t.Error("expected anthropic-version to be forwarded")
	}
	if _, ok := extraHeaders["openai-version"]; ok {
		t.Error("expected openai-version to be blocked (not matching anthropic-*)")
	}
}

// TestConvertToBifrostContext_WildcardDenylistBlocking verifies wildcard denylist
// patterns block matching headers.
func TestConvertToBifrostContext_WildcardDenylistBlocking(t *testing.T) {
	matcher := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Denylist: []string{"x-internal-*"},
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-eh-x-internal-id", "blocked-value")
	ctx.Request.Header.Set("x-bf-eh-x-internal-secret", "blocked-value")
	ctx.Request.Header.Set("x-bf-eh-custom-header", "allowed-value")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, matcher, schemas.WhiteList{})
	defer cancel()

	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)

	if _, ok := extraHeaders["x-internal-id"]; ok {
		t.Error("expected x-internal-id to be blocked by denylist")
	}
	if _, ok := extraHeaders["x-internal-secret"]; ok {
		t.Error("expected x-internal-secret to be blocked by denylist")
	}
	if _, ok := extraHeaders["custom-header"]; !ok {
		t.Error("expected custom-header to be forwarded")
	}
}

// TestConvertToBifrostContext_NilMatcher verifies nil matcher allows all headers.
func TestConvertToBifrostContext_NilMatcher(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-eh-custom-header", "allowed-value")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, nil, schemas.WhiteList{})
	defer cancel()

	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)

	if _, ok := extraHeaders["custom-header"]; !ok {
		t.Error("expected custom-header to be forwarded with nil matcher")
	}
}

func TestConvertToBifrostContext_BaggageSessionIDSetsGrouping(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("baggage", "foo=bar, session-id=rt-123, baz=qux")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, nil, schemas.WhiteList{})
	defer cancel()

	if got, _ := bifrostCtx.Value(schemas.BifrostContextKeyParentRequestID).(string); got != "rt-123" {
		t.Fatalf("parent request id = %q, want %q", got, "rt-123")
	}
}

func TestConvertToBifrostContext_EmptyBaggageSessionIDIgnored(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("baggage", "session-id=   ")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, false, nil, schemas.WhiteList{})
	defer cancel()

	if got := bifrostCtx.Value(schemas.BifrostContextKeyParentRequestID); got != nil {
		t.Fatalf("parent request id should be unset, got %#v", got)
	}
}
