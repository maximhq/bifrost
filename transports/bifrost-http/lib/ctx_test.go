package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/valyala/fasthttp"
)

// testHandlerStore is a minimal HandlerStore for ctx tests.
type testHandlerStore struct {
	matcher         *HeaderMatcher
	allowDirectKeys bool
}

func (s testHandlerStore) GetHeaderMatcher() *HeaderMatcher                      { return s.matcher }
func (s testHandlerStore) GetProvidersForModel(_ string) []schemas.ModelProvider { return nil }
func (s testHandlerStore) GetStreamChunkInterceptor() StreamChunkInterceptor     { return nil }
func (s testHandlerStore) GetAsyncJobExecutor() *logstore.AsyncJobExecutor       { return nil }
func (s testHandlerStore) GetAsyncJobResultTTL() int                             { return 0 }
func (s testHandlerStore) GetKVStore() *kvstore.Store                            { return nil }
func (s testHandlerStore) GetMCPHeaderCombinedAllowlist() schemas.WhiteList {
	return schemas.WhiteList{}
}
func (s testHandlerStore) ShouldAllowPerRequestStorageOverride() bool      { return false }
func (s testHandlerStore) ShouldAllowPerRequestRawOverride() bool          { return false }
func (s testHandlerStore) ShouldAllowDirectKeys() bool                     { return s.allowDirectKeys }
func (s testHandlerStore) IsProviderConfigured(schemas.ModelProvider) bool { return false }
func (s testHandlerStore) GetMCPExternalServerURL() string                 { return "" }
func (s testHandlerStore) GetMCPExternalClientURL() string                 { return "" }

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

	converted, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
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

	first, cancelFirst := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancelFirst()
	if first == nil {
		t.Fatal("expected first context to be non-nil")
	}

	second, cancelSecond := ConvertToBifrostContext(ctx, testHandlerStore{})
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

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{matcher: matcher})
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

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{matcher: matcher})
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

// TestConvertToBifrostContext_AsyncWebhookHeaderSurvivesStarAllowlist verifies
// that the reserved x-bf-async-webhook header is captured into the context even
// when a "*" allowlist would otherwise forward-and-return it as a direct header,
// which would drop the endpoint name and run the async job without notifying.
func TestConvertToBifrostContext_AsyncWebhookHeaderSurvivesStarAllowlist(t *testing.T) {
	matcher := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"*"},
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-async-webhook", "receiver")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{matcher: matcher})
	defer cancel()

	if got, ok := bifrostCtx.Value(schemas.BifrostContextKeyAsyncWebhookEndpoint).(string); !ok || got != "receiver" {
		t.Errorf("expected async webhook endpoint %q to be captured under a * allowlist, got %q (present=%v)", "receiver", got, ok)
	}
	// The reserved header must not also leak into forwarded extra headers.
	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if _, ok := extraHeaders["x-bf-async-webhook"]; ok {
		t.Error("expected reserved x-bf-async-webhook to be consumed, not forwarded as an extra header")
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

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{matcher: matcher})
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

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{matcher: matcher})
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

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{matcher: matcher})
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

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)

	if _, ok := extraHeaders["custom-header"]; !ok {
		t.Error("expected custom-header to be forwarded with nil matcher")
	}
}

func TestConvertToBifrostContext_BaggageSessionIDSetsGrouping(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("baggage", "foo=bar, session-id=rt-123, baz=qux")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	if got, _ := bifrostCtx.Value(schemas.BifrostContextKeyParentRequestID).(string); got != "rt-123" {
		t.Fatalf("parent request id = %q, want %q", got, "rt-123")
	}
}

func TestConvertToBifrostContext_CompatHeaderForceReasoningOnlyToResponses(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"named feature", `["force_reasoning_only_models_to_responses"]`, true},
		{"true enables all", "true", true},
		{"star enables all", `["*"]`, true},
		{"other feature only", `["should_drop_params"]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.Set("x-bf-compat", tc.header)

			bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
			defer cancel()

			got, _ := bifrostCtx.Value(schemas.BifrostContextKeyCompatForceReasoningOnlyToResponses).(bool)
			if got != tc.want {
				t.Fatalf("force_reasoning_only_models_to_responses override = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConvertToBifrostContext_KeyPinHeaders pins how the caller's key pins reach core: x-bf-api-key
// names a key and x-bf-api-key-id identifies one, each trimmed, and a blank value pins nothing, so
// a client that always sends the header empty is not refused for a key that does not exist.
func TestConvertToBifrostContext_KeyPinHeaders(t *testing.T) {
	for _, tc := range []struct {
		name     string
		headers  map[string]string
		wantName any
		wantID   any
	}{
		{"a key name", map[string]string{"x-bf-api-key": " prod-key "}, "prod-key", nil},
		{"a key id", map[string]string{"x-bf-api-key-id": " key-uuid "}, nil, "key-uuid"},
		{"both", map[string]string{"x-bf-api-key": "prod-key", "x-bf-api-key-id": "key-uuid"}, "prod-key", "key-uuid"},
		{"blank values", map[string]string{"x-bf-api-key": "   ", "x-bf-api-key-id": ""}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for k, v := range tc.headers {
				ctx.Request.Header.Set(k, v)
			}
			bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
			defer cancel()
			if got := bifrostCtx.Value(schemas.BifrostContextKeyAPIKeyName); got != tc.wantName {
				t.Fatalf("key name pin = %#v, want %#v", got, tc.wantName)
			}
			if got := bifrostCtx.Value(schemas.BifrostContextKeyAPIKeyID); got != tc.wantID {
				t.Fatalf("key id pin = %#v, want %#v", got, tc.wantID)
			}
		})
	}
}

// TestConvertToBifrostContext_SessionTTLAndAffinityHeaders pins how the two per-request session
// headers are read. x-bf-session-ttl takes a Go duration or a whole number of seconds, and a value
// that is neither, or is not positive, leaves the default TTL in place. x-bf-session-affinity takes
// on/true/1 or off/false/0 in any case, and anything else is ignored rather than read as off.
func TestConvertToBifrostContext_SessionTTLAndAffinityHeaders(t *testing.T) {
	for _, tc := range []struct {
		name         string
		headers      map[string]string
		wantTTL      any
		wantAffinity any
	}{
		{"a duration", map[string]string{"x-bf-session-ttl": "30m"}, 30 * time.Minute, nil},
		{"whole seconds", map[string]string{"x-bf-session-ttl": " 45 "}, 45 * time.Second, nil},
		{"zero seconds", map[string]string{"x-bf-session-ttl": "0"}, nil, nil},
		{"negative seconds", map[string]string{"x-bf-session-ttl": "-5"}, nil, nil},
		{"a negative duration", map[string]string{"x-bf-session-ttl": "-1m"}, nil, nil},
		{"neither a duration nor seconds", map[string]string{"x-bf-session-ttl": "soon"}, nil, nil},
		{"affinity on", map[string]string{"x-bf-session-affinity": "on"}, nil, true},
		{"affinity TRUE", map[string]string{"x-bf-session-affinity": "TRUE"}, nil, true},
		{"affinity 1", map[string]string{"x-bf-session-affinity": "1"}, nil, true},
		{"affinity Off", map[string]string{"x-bf-session-affinity": " Off "}, nil, false},
		{"affinity false", map[string]string{"x-bf-session-affinity": "false"}, nil, false},
		{"affinity 0", map[string]string{"x-bf-session-affinity": "0"}, nil, false},
		{"affinity neither on nor off", map[string]string{"x-bf-session-affinity": "maybe"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for k, v := range tc.headers {
				ctx.Request.Header.Set(k, v)
			}
			bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
			defer cancel()
			if got := bifrostCtx.Value(schemas.BifrostContextKeySessionTTL); got != tc.wantTTL {
				t.Fatalf("session TTL = %#v, want %#v", got, tc.wantTTL)
			}
			if got := bifrostCtx.Value(schemas.BifrostContextKeySessionAffinity); got != tc.wantAffinity {
				t.Fatalf("session affinity = %#v, want %#v", got, tc.wantAffinity)
			}
		})
	}
}

func TestConvertToBifrostContext_EmptyBaggageSessionIDIgnored(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("baggage", "session-id=   ")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	if got := bifrostCtx.Value(schemas.BifrostContextKeyParentRequestID); got != nil {
		t.Fatalf("parent request id should be unset, got %#v", got)
	}
}

// TestConvertToBifrostContext_BillingNonceIsMintedInternally verifies the
// billing nonce exists, is not the (caller-forgeable) request ID, and cannot
// be influenced by any inbound header. Governance keys its billing-idempotency
// claim on this nonce, so a caller replaying a chosen x-request-id across
// independent requests must still produce distinct billing keys.
func TestConvertToBifrostContext_BillingNonceIsMintedInternally(t *testing.T) {
	mkCtx := func() *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("x-request-id", "attacker-chosen-id")
		// A caller must not be able to pin the nonce through header-derived paths.
		ctx.Request.Header.Set("bifrost-billing-nonce", "forged-nonce")
		ctx.Request.Header.Set("x-bf-dim-bifrost-billing-nonce", "forged-nonce")
		return ctx
	}

	bifrostCtx1, cancel1 := ConvertToBifrostContext(mkCtx(), testHandlerStore{})
	defer cancel1()
	bifrostCtx2, cancel2 := ConvertToBifrostContext(mkCtx(), testHandlerStore{})
	defer cancel2()

	nonce1, ok := bifrostCtx1.Value(schemas.BifrostContextKeyBillingNonce).(string)
	if !ok || nonce1 == "" {
		t.Fatal("expected a billing nonce on the converted context")
	}
	if nonce1 == "forged-nonce" {
		t.Fatal("billing nonce must not be settable from inbound headers")
	}
	if nonce1 == "attacker-chosen-id" {
		t.Fatal("billing nonce must not equal the caller-supplied request id")
	}
	nonce2, _ := bifrostCtx2.Value(schemas.BifrostContextKeyBillingNonce).(string)
	if nonce1 == nonce2 {
		t.Fatalf("two independent requests sharing an x-request-id must get distinct billing nonces, both got %q", nonce1)
	}
	// The request-id itself keeps its correlation semantics.
	if got, _ := bifrostCtx1.Value(schemas.BifrostContextKeyRequestID).(string); got != "attacker-chosen-id" {
		t.Fatalf("request-id = %q, want the inbound x-request-id", got)
	}
}

// TestConvertToBifrostContext_BillingNoncePreservedOnSharedContext verifies
// that when a BifrostContext is already shared on the fasthttp context (the
// large-payload/transport-hook path), a second conversion keeps the existing
// nonce: both terminal settlement paths of one physical call must read the
// same value to dedupe against each other.
func TestConvertToBifrostContext_BillingNoncePreservedOnSharedContext(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	bifrostCtx1, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()
	nonce1, _ := bifrostCtx1.Value(schemas.BifrostContextKeyBillingNonce).(string)
	if nonce1 == "" {
		t.Fatal("expected a billing nonce on first conversion")
	}

	bifrostCtx2, cancel2 := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel2()
	nonce2, _ := bifrostCtx2.Value(schemas.BifrostContextKeyBillingNonce).(string)
	if nonce2 != nonce1 {
		t.Fatalf("nonce changed across conversions of one request: %q then %q", nonce1, nonce2)
	}
}

func TestConvertToBifrostContext_DimHeadersDoNotOverrideReservedContextKeys(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-request-id", "trusted-request-id")
	ctx.Request.Header.Set("x-bf-dim-request-id", "attacker-request-id")
	ctx.Request.Header.Set("x-bf-dim-x-bf-vk", "attacker-vk")
	ctx.Request.Header.Set("x-bf-prom-x-bf-vk", "attacker-vk")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	// request-id must remain from trusted source, not from x-bf-dim-request-id.
	if got, _ := bifrostCtx.Value(schemas.BifrostContextKeyRequestID).(string); got != "trusted-request-id" {
		t.Fatalf("request-id = %q, want %q", got, "trusted-request-id")
	}
	// Virtual key must not be set through x-bf-dim-x-bf-vk.
	if got := bifrostCtx.Value(schemas.BifrostContextKeyVirtualKey); got != nil {
		t.Fatalf("virtual key should not be set via x-bf-dim-*, got %#v", got)
	}

	// Dimension values are still captured in the dedicated dimensions map.
	dimensions, ok := bifrostCtx.Value(schemas.BifrostContextKeyDimensions).(map[string]string)
	if !ok {
		t.Fatal("expected dimensions map in context")
	}
	if dimensions["request-id"] != "attacker-request-id" {
		t.Fatalf("dimensions[request-id] = %q, want %q", dimensions["request-id"], "attacker-request-id")
	}
	if dimensions["x-bf-vk"] != "attacker-vk" {
		t.Fatalf("dimensions[x-bf-vk] = %q, want %q", dimensions["x-bf-vk"], "attacker-vk")
	}
}

func TestConvertToBifrostContext_PromHeadersDoNotOverrideReservedContextKeys(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-request-id", "trusted-request-id")
	ctx.Request.Header.Set("x-bf-prom-request-id", "attacker-request-id")
	ctx.Request.Header.Set("x-bf-prom-x-bf-vk", "attacker-vk")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	// request-id must remain from trusted source, not from x-bf-prom-request-id.
	if got, _ := bifrostCtx.Value(schemas.BifrostContextKeyRequestID).(string); got != "trusted-request-id" {
		t.Fatalf("request-id = %q, want %q", got, "trusted-request-id")
	}
	// Virtual key must not be set through x-bf-prom-x-bf-vk.
	if got := bifrostCtx.Value(schemas.BifrostContextKeyVirtualKey); got != nil {
		t.Fatalf("virtual key should not be set via x-bf-prom-*, got %#v", got)
	}
	// Legacy x-bf-prom-* headers are not mirrored into global context keyspace.
	if got := bifrostCtx.Value(schemas.BifrostContextKey("request-id")); got != "trusted-request-id" {
		t.Fatalf("global request-id key should remain trusted value, got %#v", got)
	}

	// Legacy x-bf-prom-* must not be included in unified dimensions.
	if dimensions, ok := bifrostCtx.Value(schemas.BifrostContextKeyDimensions).(map[string]string); ok && len(dimensions) > 0 {
		t.Fatalf("expected no unified dimensions from x-bf-prom-*, got %#v", dimensions)
	}
}

func TestConvertToBifrostContext_DimAndPromCanCoexistWithoutCrossing(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-prom-team", "legacy-team")
	ctx.Request.Header.Set("x-bf-dim-team", "platform")
	ctx.Request.Header.Set("x-bf-dim-environment", "prod")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	dimensions, ok := bifrostCtx.Value(schemas.BifrostContextKeyDimensions).(map[string]string)
	if !ok {
		t.Fatal("expected dimensions map in context")
	}
	if dimensions["team"] != "platform" {
		t.Fatalf("dimensions[team] = %q, want %q", dimensions["team"], "platform")
	}
	if dimensions["environment"] != "prod" {
		t.Fatalf("dimensions[environment] = %q, want %q", dimensions["environment"], "prod")
	}
	if len(dimensions) != 2 {
		t.Fatalf("expected only dim headers in unified dimensions, got %#v", dimensions)
	}
}

func TestConvertToBifrostContext_DirectKey_ServerDisabled(t *testing.T) {
	// x-bf-direct-key: true present but server setting is off — no direct key should be set.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-direct-key", "true")
	ctx.Request.Header.Set("Authorization", "Bearer sk-real-openai-key")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: false})
	defer cancel()

	if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		t.Error("expected no direct key when server setting is disabled")
	}
}

func TestConvertToBifrostContext_DirectKey_HeaderAbsent(t *testing.T) {
	// Server allows direct keys but caller did not send x-bf-direct-key header.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-real-openai-key")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: true})
	defer cancel()

	if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		t.Error("expected no direct key when x-bf-direct-key header is absent")
	}
}

func TestConvertToBifrostContext_DirectKey_BearerRealKey(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-direct-key", "true")
	ctx.Request.Header.Set("Authorization", "Bearer sk-real-openai-key")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: true})
	defer cancel()

	key, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	if !ok {
		t.Fatal("expected direct key to be set")
	}
	if key.Value.GetValue() != "sk-real-openai-key" {
		t.Errorf("direct key value = %q, want %q", key.Value.GetValue(), "sk-real-openai-key")
	}
	if key.ID != "header-provided" {
		t.Errorf("direct key ID = %q, want %q", key.ID, "header-provided")
	}
}

func TestConvertToBifrostContext_DirectKey_VirtualKeyNotBypassed(t *testing.T) {
	// A virtual key (sk-bf-*) in Authorization must not be treated as a direct key.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-direct-key", "true")
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-virtual-key-here")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: true})
	defer cancel()

	if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		t.Error("expected virtual key not to be treated as a direct key")
	}
	// The virtual key should still be set normally.
	if vk, ok := bifrostCtx.Value(schemas.BifrostContextKeyVirtualKey).(string); !ok || vk == "" {
		t.Error("expected virtual key to be set in context")
	}
}

func TestConvertToBifrostContext_DirectKey_XAPIKey(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-direct-key", "true")
	ctx.Request.Header.Set("x-api-key", "sk-ant-real-anthropic-key")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: true})
	defer cancel()

	key, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	if !ok {
		t.Fatal("expected direct key to be set from x-api-key")
	}
	if key.Value.GetValue() != "sk-ant-real-anthropic-key" {
		t.Errorf("direct key value = %q, want %q", key.Value.GetValue(), "sk-ant-real-anthropic-key")
	}
}

func TestConvertToBifrostContext_DirectKey_XGoogAPIKey(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-direct-key", "true")
	ctx.Request.Header.Set("x-goog-api-key", "AIza-real-gemini-key")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: true})
	defer cancel()

	key, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	if !ok {
		t.Fatal("expected direct key to be set from x-goog-api-key")
	}
	if key.Value.GetValue() != "AIza-real-gemini-key" {
		t.Errorf("direct key value = %q, want %q", key.Value.GetValue(), "AIza-real-gemini-key")
	}
}

func TestConvertToBifrostContext_DirectKey_CannotBeSpoofedViaEHPrefix(t *testing.T) {
	// x-bf-eh-x-bf-direct-key must be blocked by the security denylist.
	matcher := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"*"},
	})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-eh-x-bf-direct-key", "true")
	ctx.Request.Header.Set("Authorization", "Bearer sk-real-openai-key")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{matcher: matcher, allowDirectKeys: true})
	defer cancel()

	if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		t.Error("expected x-bf-direct-key to be blocked when injected via x-bf-eh- prefix")
	}
	extraHeaders, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if _, ok := extraHeaders["x-bf-direct-key"]; ok {
		t.Error("expected x-bf-direct-key to be absent from extra headers (denylist)")
	}
}

func TestConvertToBifrostContext_DirectKey_RawVirtualKeyNotBypassed(t *testing.T) {
	// VK sent without Bearer prefix must also be excluded from direct key path.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-direct-key", "true")
	ctx.Request.Header.Set("Authorization", "sk-bf-virtual-key-no-bearer-prefix")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: true})
	defer cancel()

	if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		t.Error("expected raw VK (no Bearer prefix) to be excluded from direct key path")
	}
}

func TestConvertToBifrostContext_DirectKey_EnvPrefixNotResolved(t *testing.T) {
	// A caller sending "env.SOME_VAR" must get that literal string as the key value,
	// not the server env var it might reference.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-direct-key", "true")
	ctx.Request.Header.Set("Authorization", "Bearer env.SOME_SECRET")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{allowDirectKeys: true})
	defer cancel()

	key, ok := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	if !ok {
		t.Fatal("expected direct key to be set")
	}
	if key.Value.GetValue() != "env.SOME_SECRET" {
		t.Errorf("direct key value = %q, want literal %q (must not resolve env vars)", key.Value.GetValue(), "env.SOME_SECRET")
	}
	if key.Value.IsFromSecret() {
		t.Error("direct key must not be marked as from-secret")
	}
}

func TestBuildBaseURL(t *testing.T) {
	const host = "bifrost.example.com"
	tests := []struct {
		name     string
		external string
		host     string
		xfProto  string
		xbfProto string
		want     string
	}{
		{name: "defaults to http", host: host, want: "http://" + host},
		{name: "x-forwarded-proto https", host: host, xfProto: "https", want: "https://" + host},
		{name: "x-forwarded-proto comma list", host: host, xfProto: "https, http", want: "https://" + host},
		{name: "x-forwarded-proto uppercase", host: host, xfProto: "HTTPS", want: "https://" + host},
		{name: "x-bf-forwarded-proto https", host: host, xbfProto: "https", want: "https://" + host},
		{name: "x-bf-forwarded-proto uppercase trimmed", host: host, xbfProto: " HTTPS ", want: "https://" + host},
		{name: "x-bf-forwarded-proto comma list", host: host, xbfProto: "https, http", want: "https://" + host},
		{name: "x-bf-forwarded-proto http stays http", host: host, xbfProto: "http", want: "http://" + host},
		{name: "external override wins", external: "https://proxy.example.com", host: host, xfProto: "http", want: "https://proxy.example.com"},
		{name: "external override trailing slash trimmed", external: "https://proxy.example.com/", host: host, want: "https://proxy.example.com"},
		{name: "invalid external falls back to inference", external: "not-a-url", host: host, xbfProto: "https", want: "https://" + host},
		{name: "empty host yields empty", host: "", xbfProto: "https", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetHost(tt.host)
			if tt.xfProto != "" {
				ctx.Request.Header.Set("X-Forwarded-Proto", tt.xfProto)
			}
			if tt.xbfProto != "" {
				ctx.Request.Header.Set("x-bf-forwarded-proto", tt.xbfProto)
			}
			if got := BuildBaseURL(ctx, tt.external); got != tt.want {
				t.Fatalf("BuildBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A virtual key presented via Azure's native "api-key" header (used by the
// Azure OpenAI SDK on passthrough) must be captured into the context so
// governance/logging attribute the call to the VK, not the base key.
func TestConvertToBifrostContext_VirtualKeyFromAzureAPIKeyHeader(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("api-key", "sk-bf-azure-passthrough-vk")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	vk, ok := bifrostCtx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	if !ok || vk != "sk-bf-azure-passthrough-vk" {
		t.Fatalf("virtual key = %#v, want %q", bifrostCtx.Value(schemas.BifrostContextKeyVirtualKey), "sk-bf-azure-passthrough-vk")
	}
}

// A real (non-VK) provider key in the "api-key" header must not be misread as
// a virtual key — only the sk-bf- prefix promotes it.
func TestConvertToBifrostContext_APIKeyHeaderNonVirtualKeyIgnored(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("api-key", "real-azure-api-key")

	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()

	if got := bifrostCtx.Value(schemas.BifrostContextKeyVirtualKey); got != nil {
		t.Fatalf("virtual key should not be set from a non-VK api-key value, got %#v", got)
	}
}

func TestConvertToBifrostContext_AsyncWebhookHeader(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{name: "value carried as-is", header: "billing", want: "billing"},
		{name: "value trimmed", header: "  billing  ", want: "billing"},
		{name: "blank header ignored", header: "   ", want: ""},
		{name: "absent header ignored", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tc.header != "" {
				ctx.Request.Header.Set("x-bf-async-webhook", tc.header)
			}
			bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
			if bifrostCtx == nil {
				t.Fatal("expected a bifrost context")
			}
			defer cancel()
			got, _ := bifrostCtx.Value(schemas.BifrostContextKeyAsyncWebhookEndpoint).(string)
			if got != tc.want {
				t.Fatalf("context webhook endpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

// sessionIDFromContext is a helper for the harness-fallback tests below.
func sessionIDFromContext(t *testing.T, ctx *fasthttp.RequestCtx) string {
	t.Helper()
	bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
	defer cancel()
	sessionID, _ := bifrostCtx.Value(schemas.BifrostContextKeySessionID).(string)
	return sessionID
}

func TestConvertToBifrostContext_HarnessSessionHeaderFallback(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "claude code",
			headers: map[string]string{"x-claude-code-session-id": "cc-1"},
			want:    "cc-1",
		},
		{
			name:    "opencode session affinity",
			headers: map[string]string{"x-session-affinity": "oc-1"},
			want:    "oc-1",
		},
		{
			name:    "generic x-session-id",
			headers: map[string]string{"x-session-id": "generic-1"},
			want:    "generic-1",
		},
		{
			name:    "codex dashed",
			headers: map[string]string{"session-id": "cx-1"},
			want:    "cx-1",
		},
		{
			name:    "codex underscored",
			headers: map[string]string{"session_id": "cx-2"},
			want:    "cx-2",
		},
		{
			name:    "codex thread id",
			headers: map[string]string{"thread-id": "cx-3"},
			want:    "cx-3",
		},
		{
			name:    "codex conversation id",
			headers: map[string]string{"conversation_id": "cx-4"},
			want:    "cx-4",
		},
		{
			name:    "header name is case insensitive",
			headers: map[string]string{"X-Claude-Code-Session-Id": "cc-2"},
			want:    "cc-2",
		},
		{
			name:    "value is trimmed",
			headers: map[string]string{"x-claude-code-session-id": "  cc-3  "},
			want:    "cc-3",
		},
		{
			name:    "whitespace only does not shadow lower priority header",
			headers: map[string]string{"x-claude-code-session-id": "   ", "session-id": "cx-5"},
			want:    "cx-5",
		},
		{
			name:    "no session headers at all",
			headers: map[string]string{"user-agent": "claude-cli/2.1.0"},
			want:    "",
		},
		{
			name:    "baggage session-id member is not a session id",
			headers: map[string]string{"baggage": "session-id=bg-1"},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for k, v := range tt.headers {
				ctx.Request.Header.Set(k, v)
			}
			if got := sessionIDFromContext(t, ctx); got != tt.want {
				t.Fatalf("session id = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConvertToBifrostContext_ExplicitSessionIDBeatsHarnessHeader(t *testing.T) {
	// Set the harness header first: the header loop in ConvertToBifrostContext is
	// a single pass with early returns, so this ordering is the regression guard
	// against resolving the fallback inline instead of after the loop.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-claude-code-session-id", "harness")
	ctx.Request.Header.Set("session-id", "codex")
	ctx.Request.Header.Set("x-bf-session-id", "explicit")

	if got := sessionIDFromContext(t, ctx); got != "explicit" {
		t.Fatalf("session id = %q, want %q", got, "explicit")
	}
}

func TestConvertToBifrostContext_HarnessSessionHeaderPriority(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	// Deliberately set in reverse priority order.
	ctx.Request.Header.Set("conversation_id", "cx-conversation")
	ctx.Request.Header.Set("thread-id", "cx-thread")
	ctx.Request.Header.Set("session_id", "cx-underscore")
	ctx.Request.Header.Set("session-id", "cx-dash")
	ctx.Request.Header.Set("x-session-id", "generic")
	ctx.Request.Header.Set("x-session-affinity", "opencode")
	ctx.Request.Header.Set("x-claude-code-session-id", "claude-code")

	if got := sessionIDFromContext(t, ctx); got != "claude-code" {
		t.Fatalf("session id = %q, want %q", got, "claude-code")
	}
}

func TestConvertToBifrostContext_OversizedHarnessSessionHeaderRejected(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-claude-code-session-id", strings.Repeat("a", schemas.MaxSessionIDLength+1))
	ctx.Request.Header.Set("session-id", "cx-1")

	// The oversized value is dropped, not truncated, and must not shadow the next
	// candidate: it would otherwise become a KV lookup key and a trace attribute.
	if got := sessionIDFromContext(t, ctx); got != "cx-1" {
		t.Fatalf("session id = %q, want %q", got, "cx-1")
	}
}

func TestConvertToBifrostContext_OversizedExplicitSessionIDRejected(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-session-id", strings.Repeat("a", schemas.MaxSessionIDLength+1))
	ctx.Request.Header.Set("x-claude-code-session-id", "cc-1")

	// The caller asserted an explicit session; when that value is unusable the
	// request gets no session rather than silently adopting a different identity.
	if got := sessionIDFromContext(t, ctx); got != "" {
		t.Fatalf("session id = %q, want empty", got)
	}
}

func TestConvertToBifrostContext_MultiByteSessionIDCountedInRunes(t *testing.T) {
	// MaxSessionIDLength counts runes, so this value is legal at 255 runes even
	// though it is 765 bytes.
	want := strings.Repeat("\u754c", schemas.MaxSessionIDLength)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-session-id", want)

	if got := sessionIDFromContext(t, ctx); got != want {
		t.Fatalf("session id runes = %d, want %d", utf8.RuneCountInString(got), schemas.MaxSessionIDLength)
	}

	over := &fasthttp.RequestCtx{}
	over.Request.Header.Set("x-bf-session-id", strings.Repeat("\u754c", schemas.MaxSessionIDLength+1))
	if got := sessionIDFromContext(t, over); got != "" {
		t.Fatalf("session id = %q, want empty for %d runes", got, schemas.MaxSessionIDLength+1)
	}
}

func TestConvertToBifrostContext_MaxLengthHarnessSessionHeaderAccepted(t *testing.T) {
	want := strings.Repeat("a", schemas.MaxSessionIDLength)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-claude-code-session-id", want)

	if got := sessionIDFromContext(t, ctx); got != want {
		t.Fatalf("session id length = %d, want %d", len(got), len(want))
	}
}

func TestResolveSessionIDFromRequest(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{name: "no headers", headers: nil, want: ""},
		{name: "explicit only", headers: map[string]string{"x-bf-session-id": "explicit"}, want: "explicit"},
		{name: "harness only", headers: map[string]string{"x-claude-code-session-id": "cc-1"}, want: "cc-1"},
		{
			name:    "explicit wins",
			headers: map[string]string{"x-claude-code-session-id": "cc-1", "x-bf-session-id": "explicit"},
			want:    "explicit",
		},
		{
			name:    "blank explicit falls through to harness",
			headers: map[string]string{"x-bf-session-id": "  ", "session-id": "cx-1"},
			want:    "cx-1",
		},
		{
			// An asserted-but-oversized explicit header yields no session at all.
			// Falling through would silently group the request under a session
			// identity the caller never asked for.
			name: "oversized explicit does not fall through to harness",
			headers: map[string]string{
				"x-bf-session-id": strings.Repeat("a", schemas.MaxSessionIDLength+1),
				"session-id":      "cx-1",
			},
			want: "",
		},
		{
			name:    "explicit at the rune limit is accepted",
			headers: map[string]string{"x-bf-session-id": strings.Repeat("a", schemas.MaxSessionIDLength)},
			want:    strings.Repeat("a", schemas.MaxSessionIDLength),
		},
		{
			// The cap counts runes, so a multi-byte value at the limit is legal
			// even though its byte length is far over it.
			name:    "multi-byte explicit at the rune limit is accepted",
			headers: map[string]string{"x-bf-session-id": strings.Repeat("\u754c", schemas.MaxSessionIDLength)},
			want:    strings.Repeat("\u754c", schemas.MaxSessionIDLength),
		},
		{
			name:    "priority order holds",
			headers: map[string]string{"thread-id": "cx-thread", "x-session-affinity": "opencode"},
			want:    "opencode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for k, v := range tt.headers {
				ctx.Request.Header.Set(k, v)
			}
			if got := ResolveSessionIDFromRequest(&ctx.Request.Header); got != tt.want {
				t.Fatalf("ResolveSessionIDFromRequest() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("nil header", func(t *testing.T) {
		if got := ResolveSessionIDFromRequest(nil); got != "" {
			t.Fatalf("ResolveSessionIDFromRequest(nil) = %q, want empty", got)
		}
	})
}

// TestSessionIDResolutionIsConsistent pins the invariant that key stickiness
// (ConvertToBifrostContext) and the OTEL session.id trace attribute
// (ResolveSessionIDFromRequest, called from the tracing middleware) never
// disagree: they run at different points in the request lifecycle.
func TestSessionIDResolutionIsConsistent(t *testing.T) {
	headerSets := []map[string]string{
		{"x-bf-session-id": "explicit"},
		{"x-claude-code-session-id": "cc-1"},
		{"x-session-affinity": "oc-1"},
		{"x-session-id": "generic-1"},
		{"x-session-affinity": "oc-1", "x-session-id": "oc-1"},
		{"session-id": "cx-1"},
		{"session_id": "cx-2"},
		{"conversation_id": "cx-3"},
		{"x-bf-session-id": "explicit", "session-id": "cx-1"},
		{"x-claude-code-session-id": "cc-1", "thread-id": "cx-thread"},
		{"user-agent": "codex-cli/1.0"},
		{"x-bf-session-id": strings.Repeat("a", schemas.MaxSessionIDLength+1), "session-id": "cx-1"},
		{"x-claude-code-session-id": strings.Repeat("a", schemas.MaxSessionIDLength+1), "session-id": "cx-1"},
		{"x-bf-session-id": strings.Repeat("\u754c", schemas.MaxSessionIDLength)},
		{"x-bf-session-id": strings.Repeat("\u754c", schemas.MaxSessionIDLength+1), "session-id": "cx-1"},
	}

	for _, headers := range headerSets {
		ctx := &fasthttp.RequestCtx{}
		for k, v := range headers {
			ctx.Request.Header.Set(k, v)
		}
		fromMiddleware := ResolveSessionIDFromRequest(&ctx.Request.Header)
		fromContext := sessionIDFromContext(t, ctx)
		if fromMiddleware != fromContext {
			t.Fatalf("headers %v: middleware resolved %q but context resolved %q", headers, fromMiddleware, fromContext)
		}
	}
}

// serveOneConnection runs fasthttp on a real loopback socket for a single
// accepted connection and returns the client end. Real TCP is required here:
// client-disconnect detection peeks at the socket, which net.Pipe and
// fasthttputil.PipeConns cannot offer.
func serveOneConnection(t *testing.T, handler fasthttp.RequestHandler) net.Conn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = fasthttp.ServeConn(conn, handler)
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

const chatCompletionRawRequest = "POST /v1/chat/completions HTTP/1.1\r\nHost: bifrost\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"

var errBifrostContextStillLive = errors.New("bifrost context still live")

// assertDisconnectOutcome checks what a handler saw after the client closed its
// socket against the documented behaviour of this platform: the context is
// cancelled where the socket can be peeked, and stays live where it cannot.
func assertDisconnectOutcome(t *testing.T, err error) {
	t.Helper()
	if clientDisconnectPeekSupported {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bifrost context 3s after the client closed its socket: %v, want context.Canceled (issue #7035)", err)
		}
		return
	}
	if !errors.Is(err, errBifrostContextStillLive) {
		t.Fatalf("bifrost context after the client closed its socket: %v, want it still live on a platform without socket peeking", err)
	}
}

// Regression test for https://github.com/maximhq/bifrost/issues/7035. A client
// that closes its socket while the handler is still waiting on core (silent
// upstream, retry backoff) must cancel the request context, so core stops
// retrying the upstream on behalf of nobody. fasthttp's RequestCtx.Done only
// fires on server shutdown, so the transport has to watch the socket itself.
func TestConvertToBifrostContextCancelsWhenClientDisconnects(t *testing.T) {
	outcome := make(chan error, 1)
	client := serveOneConnection(t, func(ctx *fasthttp.RequestCtx) {
		bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
		defer cancel()
		select {
		case <-bifrostCtx.Done():
			outcome <- bifrostCtx.Err()
		case <-time.After(3 * time.Second):
			outcome <- errBifrostContextStillLive
		}
	})
	if _, err := client.Write([]byte(chatCompletionRawRequest)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	client.Close()

	select {
	case err := <-outcome:
		assertDisconnectOutcome(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("handler never reported an outcome")
	}
}

// A connected, idle client must not be mistaken for a disconnected one.
func TestConvertToBifrostContextStaysAliveWhileClientConnected(t *testing.T) {
	outcome := make(chan error, 1)
	client := serveOneConnection(t, func(ctx *fasthttp.RequestCtx) {
		bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
		defer cancel()
		select {
		case <-bifrostCtx.Done():
			outcome <- bifrostCtx.Err()
		case <-time.After(1 * time.Second):
			outcome <- errBifrostContextStillLive
		}
	})
	if _, err := client.Write([]byte(chatCompletionRawRequest)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	select {
	case err := <-outcome:
		if !errors.Is(err, errBifrostContextStillLive) {
			t.Fatalf("bifrost context was cancelled while the client was still connected: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never reported an outcome")
	}
}

// The enterprise large-payload hook seeds the shared BifrostContext on the
// request before the handler converts it. ConvertToBifrostContext then promotes
// that context with a cancel func, and the client socket must be watched on
// that path too, otherwise those deployments never see a disconnect.
func TestConvertToBifrostContextCancelsSeededContextWhenClientDisconnects(t *testing.T) {
	outcome := make(chan error, 1)
	client := serveOneConnection(t, func(ctx *fasthttp.RequestCtx) {
		seeded := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
		ctx.SetUserValue(FastHTTPUserValueBifrostContext, seeded)
		bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
		defer cancel()
		select {
		case <-bifrostCtx.Done():
			outcome <- bifrostCtx.Err()
		case <-time.After(3 * time.Second):
			outcome <- errBifrostContextStillLive
		}
	})
	if _, err := client.Write([]byte(chatCompletionRawRequest)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	client.Close()

	select {
	case err := <-outcome:
		assertDisconnectOutcome(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("handler never reported an outcome")
	}
}

// countWatcherGoroutines reports how many goroutines are currently inside
// startClientDisconnectWatcher, by name rather than by counting everything.
//
// runtime.NumGoroutine() is process-global, so a fasthttp worker or a TCP teardown
// finishing at the wrong moment makes a whole-process count flap. That is fatal for a
// release gate: a flaky assertion trains people to rerun until green. Naming the frame
// makes the measurement immune to every goroutine that is not the subject.
func countWatcherGoroutines() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return bytes.Count(buf, []byte("startClientDisconnectWatcher"))
}

// waitForWatchers polls until the watcher count drops to want, returning the final count.
// Teardown is not synchronous with cancel, so a poll beats a fixed sleep.
func waitForWatchers(want int, within time.Duration) int {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if n := countWatcherGoroutines(); n <= want {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	return countWatcherGoroutines()
}

// TestClientDisconnectWatcher_RetentionNoGoroutineLeak is the retention regression test
// for the leak found in a production heap dump.
//
// ConvertToBifrostContext starts one startClientDisconnectWatcher goroutine per request.
// Its only exits are an explicit cancel or the client socket dying, and its parent is
// fasthttp's RequestCtx, whose Done fires only on server shutdown. A handler that returns
// without cancelling therefore leaves the watcher polling every 500ms forever, pinning the
// entire request-scoped BifrostContext with it.
//
// That is what GenericRouter.handleStreaming used to do on every SUCCESSFUL stream. Two
// production pods showed 596 and 546 watcher goroutines behind just 34 and 38 fasthttp
// connection goroutines, holding roughly 2.0 GB of a 2.66 GB heap that GC could not
// reclaim because all of it was genuinely reachable.
//
// The existing tests here only assert the context's cancellation semantics. None asserts
// the goroutine actually goes away, which is the property that was violated.
func TestClientDisconnectWatcher_RetentionNoGoroutineLeak(t *testing.T) {
	if !clientDisconnectPeekSupported {
		t.Skip("no socket peeking on this platform, so no watcher goroutine is started")
	}

	handlerDone := make(chan struct{})
	baselineCh := make(chan int, 1)

	client := serveOneConnection(t, func(ctx *fasthttp.RequestCtx) {
		baselineCh <- countWatcherGoroutines()
		bifrostCtx, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
		if bifrostCtx == nil {
			t.Error("expected a context")
		}
		// Exactly what a correct handler does on the way out. The bug was omitting it.
		cancel()
		close(handlerDone)
	})

	if _, err := client.Write([]byte(chatCompletionRawRequest)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var baseline int
	select {
	case <-handlerDone:
		baseline = <-baselineCh
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}

	// The client socket stays open, which is the whole point: a leaked watcher would
	// keep peeking a healthy keep-alive connection indefinitely. Only the cancel can
	// end it, so this fails if the cancel path ever stops reaching the watcher.
	if final := waitForWatchers(baseline, 5*time.Second); final > baseline {
		t.Errorf("client-disconnect watcher goroutines went %d -> %d and stayed there after "+
			"the request completed; the watcher is outliving its request and pinning the "+
			"request-scoped BifrostContext it captured", baseline, final)
	}
}

// TestClientDisconnectWatcher_RetentionWatcherActuallyStarts stops the test above from passing
// for the wrong reason. If no watcher were ever started, a "no leak" assertion would be
// trivially true, so this pins that one genuinely runs for the life of the request.
func TestClientDisconnectWatcher_RetentionWatcherActuallyStarts(t *testing.T) {
	if !clientDisconnectPeekSupported {
		t.Skip("no socket peeking on this platform, so no watcher goroutine is started")
	}

	type sample struct{ before, during int }
	observed := make(chan sample, 1)
	release := make(chan struct{})

	client := serveOneConnection(t, func(ctx *fasthttp.RequestCtx) {
		before := countWatcherGoroutines()
		_, cancel := ConvertToBifrostContext(ctx, testHandlerStore{})
		defer cancel()
		// startClientDisconnectWatcher spawns its goroutine, so sampling immediately can
		// run before the scheduler has got to it and fail while the watcher is working
		// correctly. Poll until it appears, bounded so a genuinely absent watcher still
		// fails rather than hanging.
		during := before
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && during <= before {
			time.Sleep(10 * time.Millisecond)
			during = countWatcherGoroutines()
		}
		observed <- sample{before: before, during: during}
		<-release
	})

	if _, err := client.Write([]byte(chatCompletionRawRequest)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case s := <-observed:
		if s.during <= s.before {
			t.Errorf("watcher goroutines were %d during the request vs %d just before the "+
				"context was built; none started, which would make the leak test above vacuous",
				s.during, s.before)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never reported")
	}
	close(release)
}

// Routing scenarios from the routing test plan that a virtual key or a routing rule decides, run
// in-process the way the HTTP server wires a request: request headers become the request context
// through ConvertToBifrostContext, the governance plugin and then the routing plugin run as core's
// LLM plugins over their in-memory stores, keys reach core through BaseAccount (which applies the
// virtual key's key_ids), and session state lives in the transport's kvstore. Every provider is an
// OpenAI-compatible custom provider pointed at one httptest upstream that answers by bearer token, so
// each key can be made to fail on its own. Only the request body's parsing into a Bifrost request is
// done by hand. Each test names the plan rows it covers.

const (
	gwA schemas.ModelProvider = "gw-a"
	gwB schemas.ModelProvider = "gw-b"
	gwC schemas.ModelProvider = "gw-c"
)

// gwUpstream is the one upstream every provider of a scenario points at. It answers by the bearer
// token a request carries and counts the requests by token and by "token|model".
type gwUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	status map[string]int // by token; 0 answers 200
	hits   map[string]int // by token and by "token|model"
}

// newGWUpstream starts an upstream that serves every request until told otherwise.
func newGWUpstream(t *testing.T) *gwUpstream {
	t.Helper()
	u := &gwUpstream{status: map[string]int{}, hits: map[string]int{}}
	u.Server = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.Close)
	return u
}

// serve answers one chat completion, or one Responses API request, with the status set for the
// token, an OpenAI-shaped body for it, and an SSE stream for a streamed chat request that succeeds.
func (u *gwUpstream) serve(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	u.mu.Lock()
	u.hits[token]++
	u.hits[token+"|"+body.Model]++
	status := u.status[token]
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch status {
	case 0, http.StatusOK:
		if strings.HasSuffix(r.URL.Path, "/responses") {
			fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":%q,"output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, body.Model)
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			for _, payload := range []string{
				fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`, body.Model),
				fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, body.Model),
			} {
				fmt.Fprintf(w, "data: %s\n\n", payload)
				if flusher != nil {
					flusher.Flush()
				}
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, body.Model)
	case http.StatusUnauthorized:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`))
	case http.StatusTooManyRequests:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`))
	default:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"The server had an error while processing your request","type":"server_error"}}`))
	}
}

// answer sets the status the upstream answers for a token; 200 heals it.
func (u *gwUpstream) answer(token string, status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status[token] = status
}

// count returns how many requests reached the upstream with a token, or with "token|model".
func (u *gwUpstream) count(match string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[match]
}

// clearHits forgets the counts, so one phase's hits can be read on their own.
func (u *gwUpstream) clearHits() {
	u.mu.Lock()
	defer u.mu.Unlock()
	clear(u.hits)
}

// requireGWHits fails the test unless each token or "token|model" reached the upstream as often as
// given.
func requireGWHits(t *testing.T, u *gwUpstream, want map[string]int) {
	t.Helper()
	for match, n := range want {
		if got := u.count(match); got != n {
			t.Errorf("%s reached the upstream %d times, want %d", match, got, n)
		}
	}
}

// gwKey is a key whose id and name are name and whose value, the upstream's token, is "sk-"+name.
func gwKey(name string, weight float64) schemas.Key {
	return schemas.Key{ID: name, Name: name, Value: *schemas.NewSecretVar("sk-" + name), Models: schemas.WhiteList{"*"}, Weight: weight}
}

// gwProviders reports the gateway's configured providers to governance, as the server's
// GovernanceInMemoryStore does, with no MCP clients or agents.
type gwProviders struct {
	config *Config
}

// GetConfiguredProviders implements governance.InMemoryStore.
func (p gwProviders) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	p.config.Mu.RLock()
	defer p.config.Mu.RUnlock()
	return p.config.Providers
}

// GetConfiguredProviderNames implements governance.InMemoryStore.
func (p gwProviders) GetConfiguredProviderNames() []string {
	p.config.Mu.RLock()
	defer p.config.Mu.RUnlock()
	names := make([]string, 0, len(p.config.Providers))
	for provider := range p.config.Providers {
		names = append(names, string(provider))
	}
	return names
}

// GetMCPClientsAllowedByDefault implements governance.InMemoryStore.
func (gwProviders) GetMCPClientsAllowedByDefault() map[string]string { return nil }

// GetMCPClientNames implements governance.InMemoryStore.
func (gwProviders) GetMCPClientNames() map[string]string { return nil }

// GetMCPClientBySlug implements governance.InMemoryStore.
func (gwProviders) GetMCPClientBySlug(string) (string, string, bool) { return "", "", false }

// GetEnabledAgents implements governance.InMemoryStore.
func (gwProviders) GetEnabledAgents() map[string]bool { return nil }

// gateway is one in-process gateway: the transport's provider config and account, the governance
// and routing plugins over their stores, core, and the session store.
type gateway struct {
	t          *testing.T
	up         *gwUpstream
	config     *Config
	client     *bifrost.Bifrost
	governance *governance.GovernancePlugin
	rules      rules.Store
	kv         *kvstore.Store
}

// gwSetup is what a scenario's gateway starts with: each provider's keys, the governance state
// (virtual keys, teams, customers), and the routing rules.
type gwSetup struct {
	keys       map[schemas.ModelProvider][]schemas.Key
	governance configstore.GovernanceConfig
	rules      []*configstoreTables.TableRoutingRule
	maxRetries map[schemas.ModelProvider]int // per provider; 2 when absent
	// catalog prices requests for governance, which charges budgets with it; nil leaves every
	// request free. A provider named after a standard one (openai) is configured as that provider
	// rather than as a custom one, so the catalog's prices for it apply.
	catalog *modelcatalog.ModelCatalog
	// allowDirectKeys turns on the gateway setting that lets a caller send its own key.
	allowDirectKeys bool
}

// newGateway starts a gateway for setup against u.
func newGateway(t *testing.T, u *gwUpstream, setup gwSetup) *gateway {
	t.Helper()
	ctx := context.Background()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	config := &Config{ClientConfig: &configstore.ClientConfig{AllowDirectKeys: setup.allowDirectKeys}, Providers: map[schemas.ModelProvider]configstore.ProviderConfig{}}
	for provider, keys := range setup.keys {
		maxRetries, ok := setup.maxRetries[provider]
		if !ok {
			maxRetries = 2
		}
		var custom *schemas.CustomProviderConfig
		if provider != schemas.OpenAI {
			custom = &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}
		}
		config.Providers[provider] = configstore.ProviderConfig{
			Keys: keys,
			NetworkConfig: &schemas.NetworkConfig{
				BaseURL:                        u.URL,
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     maxRetries,
				RetryBackoffInitial:            time.Millisecond,
				RetryBackoffMax:                2 * time.Millisecond,
			},
			ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 8, BufferSize: 512},
			CustomProviderConfig:     custom,
		}
	}
	governanceConfig := setup.governance
	governancePlugin, err := governance.Init(ctx, &governance.Config{}, logger, nil, &governanceConfig, setup.catalog, nil, gwProviders{config: config})
	if err != nil {
		t.Fatalf("governance.Init: %v", err)
	}
	ruleStore, err := rules.NewLocalStore(ctx, logger, nil)
	if err != nil {
		t.Fatalf("rules.NewLocalStore: %v", err)
	}
	for _, rule := range setup.rules {
		if err := ruleStore.UpsertRule(ctx, rule); err != nil {
			t.Fatalf("UpsertRule %s: %v", rule.ID, err)
		}
	}
	kv, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatalf("kvstore.New: %v", err)
	}
	routingPlugin, err := routing.InitFromStore(ctx, &routing.Config{KVStore: kv}, logger, nil, ruleStore, governancePlugin)
	if err != nil {
		t.Fatalf("routing.InitFromStore: %v", err)
	}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    NewBaseAccount(config),
		Logger:     logger,
		KVStore:    kv,
		LLMPlugins: []schemas.LLMPlugin{governancePlugin, routingPlugin},
	})
	if err != nil {
		t.Fatalf("bifrost.Init: %v", err)
	}
	t.Cleanup(func() {
		client.Shutdown()
		_ = routingPlugin.Cleanup()
		_ = governancePlugin.Cleanup()
		_ = kv.Close()
	})
	return &gateway{t: t, up: u, config: config, client: client, governance: governancePlugin, rules: ruleStore, kv: kv}
}

// gwCall is one chat request as a caller sends it: the model string with or without a provider
// prefix, the request's own fallbacks ("provider/model"), and its headers (x-bf-vk,
// x-bf-session-id, x-bf-api-key-id, ...).
type gwCall struct {
	model     string
	fallbacks []string
	headers   map[string]string
	stream    bool
	responses bool // a /v1/responses request rather than a chat completion
}

// gwResult is what came back: the routing info of the attempt that answered (the one that served or,
// when every attempt failed, the one whose error came back), the error if any, the request context,
// for its routing trail, and the fallbacks the request carried once the plugins had routed it.
type gwResult struct {
	info      schemas.RoutingInfo
	err       *schemas.BifrostError
	ctx       *schemas.BifrostContext
	fallbacks []schemas.Fallback
}

// send runs one request through the gateway: the headers become the request context the way the
// HTTP transport builds it, and the model and fallbacks are parsed the way its handlers parse them.
func (g *gateway) send(call gwCall) gwResult {
	g.t.Helper()
	var fctx fasthttp.RequestCtx
	for name, value := range call.headers {
		fctx.Request.Header.Set(name, value)
	}
	ctx, cancel := ConvertToBifrostContext(&fctx, g.config)
	defer cancel()
	provider, model := schemas.ParseModelString(call.model, "")
	var fallbacks []schemas.Fallback
	for _, fallback := range call.fallbacks {
		fbProvider, fbModel := schemas.ParseModelString(fallback, "")
		fallbacks = append(fallbacks, schemas.Fallback{Provider: fbProvider, Model: fbModel})
	}
	req := &schemas.BifrostChatRequest{
		Provider:  provider,
		Model:     model,
		Fallbacks: fallbacks,
		Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}
	if call.responses {
		responsesReq := &schemas.BifrostResponsesRequest{
			Provider:  provider,
			Model:     model,
			Fallbacks: fallbacks,
			Input:     []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}},
		}
		resp, err := g.client.ResponsesRequest(ctx, responsesReq)
		if err != nil {
			return gwResult{info: err.ExtraFields.RoutingInfo, err: err, ctx: ctx, fallbacks: responsesReq.Fallbacks}
		}
		return gwResult{info: resp.ExtraFields.RoutingInfo, ctx: ctx, fallbacks: responsesReq.Fallbacks}
	}
	if call.stream {
		stream, err := g.client.ChatCompletionStreamRequest(ctx, req)
		if err != nil {
			return gwResult{info: err.ExtraFields.RoutingInfo, err: err, ctx: ctx, fallbacks: req.Fallbacks}
		}
		result := gwResult{ctx: ctx}
		for chunk := range stream {
			if chunk.BifrostError != nil {
				result.err = chunk.BifrostError
				continue
			}
			if chunk.BifrostChatResponse != nil {
				result.info = chunk.BifrostChatResponse.ExtraFields.RoutingInfo
			}
		}
		result.fallbacks = req.Fallbacks
		return result
	}
	resp, err := g.client.ChatCompletionRequest(ctx, req)
	if err != nil {
		return gwResult{info: err.ExtraFields.RoutingInfo, err: err, ctx: ctx, fallbacks: req.Fallbacks}
	}
	return gwResult{info: resp.ExtraFields.RoutingInfo, ctx: ctx, fallbacks: req.Fallbacks}
}

// trail joins the routing engine log messages the request left, for substring checks.
func (r gwResult) trail() string {
	var lines []string
	for _, entry := range r.ctx.GetRoutingEngineLogs() {
		lines = append(lines, entry.Engine+": "+entry.Message)
	}
	return strings.Join(lines, "\n")
}

// requireServedBy fails the test unless the request was served by provider.
func requireServedBy(t *testing.T, r gwResult, provider schemas.ModelProvider) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("request failed: %s\n%s", r.err.GetErrorString(), r.trail())
	}
	if r.info.Provider != provider {
		t.Fatalf("served by %s, want %s\n%s", r.info.Provider, provider, r.trail())
	}
}

// requireFailedWith fails the test unless the request failed with status.
func requireFailedWith(t *testing.T, r gwResult, status int) {
	t.Helper()
	if r.err == nil {
		t.Fatalf("the request was served by %s, want a %d\n%s", r.info.Provider, status, r.trail())
	}
	if r.err.StatusCode == nil || *r.err.StatusCode != status {
		t.Fatalf("the request failed with status %v (%s), want %d\n%s", r.err.StatusCode, r.err.GetErrorString(), status, r.trail())
	}
}

// binding reads one of the session's bindings for the request r was: its route for model (provider
// "") or its key on provider for model. A binding is keyed by the session id and by the virtual key
// and user the request's grant settled, so it is read through a request of that session.
func (g *gateway) binding(r gwResult, kind string, provider schemas.ModelProvider, model string) string {
	value, _ := bifrost.SessionStateString(g.kv, bifrost.SessionStateKey(r.ctx, kind, string(provider), model))
	return value
}

// requireBinding fails the test unless the binding holds want, or is absent when want is "".
func (g *gateway) requireBinding(r gwResult, kind string, provider schemas.ModelProvider, model, want string) {
	g.t.Helper()
	if got := g.binding(r, kind, provider, model); got != want {
		g.t.Fatalf("the session's %s binding for %s/%s holds %q, want %q", kind, provider, model, got, want)
	}
}

// gwVK is a virtual key named name whose value is "sk-bf-"+name, with the given provider configs.
func gwVK(name string, configs ...configstoreTables.TableVirtualKeyProviderConfig) configstoreTables.TableVirtualKey {
	return configstoreTables.TableVirtualKey{
		ID:              name,
		Name:            name,
		Value:           *schemas.NewSecretVar("sk-bf-" + name),
		IsActive:        schemas.Ptr(true),
		ProviderConfigs: configs,
	}
}

// gwVKProvider is a virtual key's config for provider, allowing every model and every key, at
// weight (nil leaves it unweighted).
func gwVKProvider(provider schemas.ModelProvider, weight *float64) configstoreTables.TableVirtualKeyProviderConfig {
	return configstoreTables.TableVirtualKeyProviderConfig{
		Provider:      string(provider),
		Weight:        weight,
		AllowedModels: schemas.WhiteList{"*"},
		AllowAllKeys:  true,
	}
}

// onlyKeys restricts a virtual key's provider config to the given key ids.
func onlyKeys(config configstoreTables.TableVirtualKeyProviderConfig, keyIDs ...string) configstoreTables.TableVirtualKeyProviderConfig {
	config.AllowAllKeys = false
	config.Keys = nil
	for _, id := range keyIDs {
		config.Keys = append(config.Keys, configstoreTables.TableKey{KeyID: id, Name: id, Provider: config.Provider})
	}
	return config
}

// vkHeaders are the headers of a request presenting virtual key name, in session (none when empty).
func vkHeaders(name, session string) map[string]string {
	headers := map[string]string{"x-bf-vk": "sk-bf-" + name}
	if session != "" {
		headers["x-bf-session-id"] = session
	}
	return headers
}

// chiSquare is the chi-square statistic of counts against the shares the weights imply.
func chiSquare(counts map[schemas.ModelProvider]int, weights map[schemas.ModelProvider]float64) float64 {
	total, n := 0.0, 0
	for provider, weight := range weights {
		total += weight
		n += counts[provider]
	}
	stat := 0.0
	for provider, weight := range weights {
		expected := float64(n) * weight / total
		diff := float64(counts[provider]) - expected
		stat += diff * diff / expected
	}
	return stat
}

// splitDraws is how many requests a weight-split scenario sends: enough that a fit judged at
// p = 1e-6 still catches a skew of two or three points on any share.
const splitDraws = 20000

// chiSquareCritical maps degrees of freedom to the chi-square value a fit stays under at p = 1e-6, the
// bar core/keyselectors' split test uses: a correct split fails about once in a million runs, while a
// real skew fails by a wide margin.
var chiSquareCritical = map[int]float64{1: 23.928, 2: 27.631, 3: 30.664}

// shareZ is how many standard deviations a provider's share may stray from its weight. Each share's
// two-sided tail at 5.1 sigma is about 3.4e-7, so three shares checked together fail a correct split
// about as rarely as the chi-square fit does.
const shareZ = 5.1

// requireShare fails the test unless provider's share of n is within shareZ standard deviations of
// want, and returns the band it allowed.
func requireShare(t *testing.T, counts map[schemas.ModelProvider]int, n int, provider schemas.ModelProvider, want float64) float64 {
	t.Helper()
	band := shareZ * math.Sqrt(want*(1-want)/float64(n))
	share := float64(counts[provider]) / float64(n)
	if math.Abs(share-want) > band {
		t.Fatalf("%s served %.2f%% of %d requests, want %.2f%% ± %.2fpp (counts %v)", provider, 100*share, n, 100*want, 100*band, counts)
	}
	return band
}

// Routing-rule scenarios from the routing test plan, run through the gateway above with the real routing plugin evaluating the rules, alone or over a virtual
// key the governance plugin resolves.

// gwRule is a routing rule matching model == "m" at scope (global for ""), targeting target, with
// fallbacks stored as the config store keeps them (nil for none). It is decoded the way the config
// store loads a rule, so a fallback naming a provider is resolved when a request uses it.
func gwRule(t *testing.T, id, scope, scopeID string, target configstoreTables.TableRoutingTarget, fallbacks *string) *configstoreTables.TableRoutingRule {
	t.Helper()
	if scope == "" {
		scope = "global"
	}
	rule := &configstoreTables.TableRoutingRule{
		ID:            id,
		Name:          id,
		CelExpression: "model == 'm'",
		Targets:       []configstoreTables.TableRoutingTarget{target},
		Fallbacks:     fallbacks,
		Enabled:       schemas.Ptr(true),
		Scope:         scope,
	}
	if scopeID != "" {
		rule.ScopeID = schemas.Ptr(scopeID)
	}
	if err := rule.AfterFind(nil); err != nil {
		t.Fatalf("decoding rule %s: %v", id, err)
	}
	return rule
}

// gwTarget is a rule target on provider and model (either may be empty to keep the request's),
// pinned to keyID when it is not empty.
func gwTarget(provider schemas.ModelProvider, model, keyID string) configstoreTables.TableRoutingTarget {
	target := configstoreTables.TableRoutingTarget{Weight: 1}
	if provider != "" {
		target.Provider = schemas.Ptr(string(provider))
	}
	if model != "" {
		target.Model = schemas.Ptr(model)
	}
	if keyID != "" {
		target.KeyID = schemas.Ptr(keyID)
	}
	return target
}

// failProvider makes every key of provider answer status.
func failProvider(u *gwUpstream, keys map[schemas.ModelProvider][]schemas.Key, provider schemas.ModelProvider, status int) {
	for _, key := range keys[provider] {
		u.answer(key.Value.GetValue(), status)
	}
}

// providerHits is how many requests reached the upstream on any key of provider, for model when it
// is not empty.
func providerHits(u *gwUpstream, keys map[schemas.ModelProvider][]schemas.Key, provider schemas.ModelProvider, model string) int {
	n := 0
	for _, key := range keys[provider] {
		match := key.Value.GetValue()
		if model != "" {
			match += "|" + model
		}
		n += u.count(match)
	}
	return n
}

// A rule whose fallback names a provider but no model: with every key of the target A answering 500
// on the target's model m2, A is tried three times, and the fallback B runs on the caller's model m,
// not the target's, which is what routing info reports (plan row RR-13).
func TestGatewayProviderOnlyRuleFallbackInheritsTheCallersModel(t *testing.T) {
	u := newGWUpstream(t)
	keys := threeKeyProviders()
	g := newGateway(t, u, gwSetup{keys: keys, rules: []*configstoreTables.TableRoutingRule{
		gwRule(t, "R1", "", "", gwTarget(gwA, "m2", ""), schemas.Ptr(`[{"provider":"gw-b"}]`)),
	}})
	failProvider(u, keys, gwA, http.StatusInternalServerError)
	r := g.send(gwCall{model: "m"})
	requireServedBy(t, r, gwB)
	if !r.info.IsFallback || r.info.Model != "m" {
		t.Fatalf("B should serve the caller's model m as the fallback, routing info %+v", r.info)
	}
	if a := providerHits(u, keys, gwA, "m2"); a != 3 {
		t.Fatalf("A should be tried 3 times on m2, got %d", a)
	}
	if b, bTarget := providerHits(u, keys, gwB, "m"), providerHits(u, keys, gwB, "m2"); b != 1 || bTarget != 0 {
		t.Fatalf("B should run once on m and never on m2, m=%d m2=%d", b, bTarget)
	}
}

// A rule's fallbacks replace the caller's, and a rule without fallbacks keeps them: with A answering
// 500 and the request asking for C, (a) a rule falling back to B sends the request to B after three
// tries of A, and C is never attempted; (b) a rule with no fallbacks leaves the caller's C, which
// serves after three tries of A (plan row RR-14; one subtest per variant).
func TestGatewayRuleFallbacksVersusTheCallers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fallbacks *string
		served    schemas.ModelProvider
		unused    schemas.ModelProvider
	}{
		{name: "(a) the rule's fallbacks replace the caller's", fallbacks: schemas.Ptr(`["gw-b/m"]`), served: gwB, unused: gwC},
		{name: "(b) a rule without fallbacks keeps the caller's", served: gwC, unused: gwB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newGWUpstream(t)
			keys := threeKeyProviders()
			g := newGateway(t, u, gwSetup{keys: keys, rules: []*configstoreTables.TableRoutingRule{gwRule(t, "R1", "", "", gwTarget(gwA, "m", ""), tc.fallbacks)}})
			failProvider(u, keys, gwA, http.StatusInternalServerError)
			r := g.send(gwCall{model: "m", fallbacks: []string{"gw-c/m"}})
			requireServedBy(t, r, tc.served)
			if !r.info.IsFallback {
				t.Fatalf("%s should serve as the fallback, routing info %+v", tc.served, r.info)
			}
			if a := providerHits(u, keys, gwA, ""); a != 3 {
				t.Fatalf("A should be tried 3 times, got %d", a)
			}
			if n := providerHits(u, keys, tc.served, ""); n != 1 {
				t.Fatalf("%s should be tried once, got %d", tc.served, n)
			}
			if n := providerHits(u, keys, tc.unused, ""); n != 0 {
				t.Fatalf("%s should never be attempted, got %d", tc.unused, n)
			}
		})
	}
}

// A matched rule suppresses governance's fallbacks: vk1 weights A, B and C equally, a rule sends m to
// A with no fallbacks, and every A key answers 500. Governance skips load balancing because the rule
// set the provider, so nothing is attached; A is tried three times and its 500 comes back, with B
// and C untouched (plan row RR-15).
func TestGatewayMatchedRuleSuppressesGovernanceFallbacks(t *testing.T) {
	u := newGWUpstream(t)
	keys := threeKeyProviders()
	one := schemas.Ptr(1.0)
	g := newGateway(t, u, gwSetup{
		keys:       keys,
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1", gwVKProvider(gwA, one), gwVKProvider(gwB, one), gwVKProvider(gwC, one))}},
		rules:      []*configstoreTables.TableRoutingRule{gwRule(t, "R1", "", "", gwTarget(gwA, "m", ""), nil)},
	})
	failProvider(u, keys, gwA, http.StatusInternalServerError)
	r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
	requireFailedWith(t, r, http.StatusInternalServerError)
	if r.info.Provider != gwA {
		t.Fatalf("the error should be A's, routing info %+v", r.info)
	}
	if len(r.fallbacks) != 0 {
		t.Fatalf("no fallbacks should be attached, got %v", r.fallbacks)
	}
	if !strings.Contains(r.trail(), "Skipping load balancing for model m: provider gw-a already set") {
		t.Fatalf("governance should report skipping load balancing:\n%s", r.trail())
	}
	if a := providerHits(u, keys, gwA, ""); a != 3 {
		t.Fatalf("A should be tried 3 times, got %d", a)
	}
	if n := providerHits(u, keys, gwB, "") + providerHits(u, keys, gwC, ""); n != 0 {
		t.Fatalf("B and C should never be attempted, got %d", n)
	}
}

// A rule fallback whose provider was deleted after the rule was saved is skipped with a trail
// warning, so B, the next one, becomes fallback 1: A is tried three times on its 500, then B once
// (plan row RR-16).
func TestGatewayRuleFallbackToADeletedProviderIsSkipped(t *testing.T) {
	u := newGWUpstream(t)
	keys := threeKeyProviders()
	g := newGateway(t, u, gwSetup{keys: keys, rules: []*configstoreTables.TableRoutingRule{
		gwRule(t, "R1", "", "", gwTarget(gwA, "m", ""), schemas.Ptr(`["gw-c/m","gw-b/m"]`)),
	}})
	// Delete C the way the server does: from the transport's config and from core.
	g.config.Mu.Lock()
	delete(g.config.Providers, gwC)
	g.config.Mu.Unlock()
	if err := g.client.RemoveProvider(gwC); err != nil {
		t.Fatalf("removing C: %v", err)
	}
	failProvider(u, keys, gwA, http.StatusInternalServerError)
	r := g.send(gwCall{model: "m"})
	requireServedBy(t, r, gwB)
	if index, _ := r.ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); index != 1 {
		t.Fatalf("B should serve as fallback 1, served at index %d", index)
	}
	if !strings.Contains(r.trail(), `Rule 'R1': fallback "gw-c/m" skipped: it does not name a known provider`) {
		t.Fatalf("the trail should warn about the deleted provider's fallback:\n%s", r.trail())
	}
	if a, b := providerHits(u, keys, gwA, ""), providerHits(u, keys, gwB, ""); a != 3 || b != 1 {
		t.Fatalf("A should be tried 3 times and B once, A=%d B=%d", a, b)
	}
	requireGWHits(t, u, map[string]int{"sk-c1": 0})
}

// Rule scope precedence, virtual key over team over customer over global: rules matching m at every
// scope (global → A, customer → B, team → C, vk1 → A pinned to a3, which no free pick lands on), and
// vk1 belonging to the team, which belongs to the customer. Requests with vk1 go to A on a3 while the
// virtual key's rule exists; deleting it, then the team's, then the customer's moves them to C, then
// B, then A on a free key (plan row RR-20).
func TestGatewayRuleScopePrecedence(t *testing.T) {
	u := newGWUpstream(t)
	keys := map[schemas.ModelProvider][]schemas.Key{
		gwA: {gwKey("a1", 1), gwKey("a2", 1), gwKey("a3", 0)},
		gwB: {gwKey("b1", 1)},
		gwC: {gwKey("c1", 1)},
	}
	customer := configstoreTables.TableCustomer{ID: "cust1", Name: "cust1"}
	team := configstoreTables.TableTeam{ID: "team1", Name: "team1", CustomerID: schemas.Ptr("cust1"), Customer: &customer}
	vk := gwVK("vk1", gwVKProvider(gwA, nil), gwVKProvider(gwB, nil), gwVKProvider(gwC, nil))
	vk.TeamID = schemas.Ptr("team1")
	vk.Team = &team
	g := newGateway(t, u, gwSetup{
		keys:       keys,
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{vk}, Teams: []configstoreTables.TableTeam{team}, Customers: []configstoreTables.TableCustomer{customer}},
		rules: []*configstoreTables.TableRoutingRule{
			gwRule(t, "global", "", "", gwTarget(gwA, "m", ""), nil),
			gwRule(t, "customer", "customer", "cust1", gwTarget(gwB, "m", ""), nil),
			gwRule(t, "team", "team", "team1", gwTarget(gwC, "m", ""), nil),
			gwRule(t, "vk", "virtual_key", "vk1", gwTarget(gwA, "m", "a3"), nil),
		},
	})
	steps := []struct {
		deleteRule string
		provider   schemas.ModelProvider
		pinned     bool
	}{
		{provider: gwA, pinned: true},
		{deleteRule: "vk", provider: gwC},
		{deleteRule: "team", provider: gwB},
		{deleteRule: "customer", provider: gwA},
	}
	for _, step := range steps {
		if step.deleteRule != "" {
			if err := g.rules.DeleteRule(context.Background(), step.deleteRule); err != nil {
				t.Fatalf("deleting rule %s: %v", step.deleteRule, err)
			}
		}
		for turn := range 5 {
			r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
			requireServedBy(t, r, step.provider)
			if step.pinned != (r.info.Key == "a3") {
				t.Fatalf("after deleting %q, turn %d was served on %s; the vk rule's pin on a3 should apply only while that rule exists", step.deleteRule, turn+1, r.info.Key)
			}
		}
	}
}

// A model-only rule target with virtual-key weights: the rule rewrites m to m2 and leaves the provider
// to vk1, which weights A and B equally. Governance splits m2 evenly between A and B, every request
// runs on m2 and never on m, and each carries the other provider as its fallback on m2 (plan row
// IX-02). The row sends 200 requests and judges at p > 0.01; this sends splitDraws and judges at
// p = 1e-6, so a correct split fails about once in a million runs while 52.8/47.2 already fails.
func TestGatewayModelOnlyRuleWithVirtualKeyWeights(t *testing.T) {
	u := newGWUpstream(t)
	keys := threeKeyProviders()
	one := schemas.Ptr(1.0)
	g := newGateway(t, u, gwSetup{
		keys:       keys,
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1", gwVKProvider(gwA, one), gwVKProvider(gwB, one))}},
		rules:      []*configstoreTables.TableRoutingRule{gwRule(t, "R1", "", "", gwTarget("", "m2", ""), nil)},
	})
	counts := map[schemas.ModelProvider]int{}
	for i := range splitDraws {
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		if r.err != nil {
			t.Fatalf("request %d failed: %s", i+1, r.err.GetErrorString())
		}
		counts[r.info.Provider]++
		if r.info.Model != "m2" {
			t.Fatalf("request %d ran on %q, want the rule's m2", i+1, r.info.Model)
		}
		other := gwA
		if r.info.Provider == gwA {
			other = gwB
		}
		if !slices.Equal(r.fallbacks, []schemas.Fallback{{Provider: other, Model: "m2"}}) {
			t.Fatalf("request %d picked %s and carried fallbacks %v, want %s on m2", i+1, r.info.Provider, r.fallbacks, other)
		}
	}
	if stat := chiSquare(counts, map[schemas.ModelProvider]float64{gwA: 1, gwB: 1}); stat >= chiSquareCritical[1] {
		t.Fatalf("picks %v do not fit 50/50 (chi-square %.2f, critical %.3f at p = 1e-6)", counts, stat, chiSquareCritical[1])
	}
	if onM := providerHits(u, keys, gwA, "m") + providerHits(u, keys, gwB, "m"); onM != 0 {
		t.Fatalf("%d requests ran on m, want none", onM)
	}
	if onM2 := providerHits(u, keys, gwA, "m2") + providerHits(u, keys, gwB, "m2"); onM2 != splitDraws {
		t.Fatalf("%d requests ran on m2, want %d", onM2, splitDraws)
	}
}

// A rule target pinned to a key outside the virtual key's key_ids: vk1 allows only a2 on A, and the
// rule pins A to a1 with B pinned to b1 as its fallback. The A attempt fails key selection without
// reaching the upstream, B serves on b1 (not b2), and the session binds B. With the rule's fallback
// removed, the same request is refused with 400 (plan row EK-15).
func TestGatewayRulePinOutsideTheVirtualKeysKeys(t *testing.T) {
	u := newGWUpstream(t)
	keys := map[schemas.ModelProvider][]schemas.Key{
		gwA: {gwKey("a1", 1), gwKey("a2", 1)},
		gwB: {gwKey("b1", 1), gwKey("b2", 1)},
	}
	one := schemas.Ptr(1.0)
	fallback := `[{"provider":"gw-b","model":"m","key_id":"b1"}]`
	g := newGateway(t, u, gwSetup{
		keys:       keys,
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1", onlyKeys(gwVKProvider(gwA, one), "a2"), gwVKProvider(gwB, one))}},
		rules:      []*configstoreTables.TableRoutingRule{gwRule(t, "R1", "", "", gwTarget(gwA, "m", "a1"), &fallback)},
	})
	r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
	requireServedBy(t, r, gwB)
	if !r.info.IsFallback || r.info.Key != "b1" {
		t.Fatalf("B should serve on its pinned b1 as the fallback, routing info %+v", r.info)
	}
	requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0, "sk-b1": 1, "sk-b2": 0})
	if r.info.PrimaryProvider == nil || *r.info.PrimaryProvider != gwA || !strings.Contains(r.trail(), "Primary gw-a/m failed (no_key_supports_model HTTP 400)") {
		t.Fatalf("A's attempt should fail key selection before B serves, routing info %+v\n%s", r.info, r.trail())
	}
	g.requireBinding(r, "route", "", "m", "gw-b/m")

	if err := g.rules.UpsertRule(context.Background(), gwRule(t, "R1", "", "", gwTarget(gwA, "m", "a1"), nil)); err != nil {
		t.Fatalf("removing the rule's fallback: %v", err)
	}
	u.clearHits()
	r = g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
	requireFailedWith(t, r, http.StatusBadRequest)
	if !strings.Contains(r.err.GetErrorString(), `no supported key found with id "a1"`) {
		t.Fatalf("the refusal should be key selection's for the rule's a1: %s", r.err.GetErrorString())
	}
	requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0, "sk-b1": 0, "sk-b2": 0})
}

// Virtual-key routing scenarios from the routing test plan, run through the gateway above: the governance plugin's weighted pick, the fallbacks it attaches, its
// allowed models and key_ids, all deciding real requests against the upstream.

// threeKeyProviders gives every provider of a scenario its own keys: two on A, two on B, one on C.
func threeKeyProviders() map[schemas.ModelProvider][]schemas.Key {
	return map[schemas.ModelProvider][]schemas.Key{
		gwA: {gwKey("a1", 1), gwKey("a2", 1)},
		gwB: {gwKey("b1", 1), gwKey("b2", 1)},
		gwC: {gwKey("c1", 1)},
	}
}

// weightedVKGateway starts a gateway over threeKeyProviders with one virtual key, vk1, weighting the
// providers as given.
func weightedVKGateway(t *testing.T, u *gwUpstream, weights map[schemas.ModelProvider]*float64) *gateway {
	t.Helper()
	var configs []configstoreTables.TableVirtualKeyProviderConfig
	for _, provider := range []schemas.ModelProvider{gwA, gwB, gwC} {
		if weight, ok := weights[provider]; ok {
			configs = append(configs, gwVKProvider(provider, weight))
		}
	}
	return newGateway(t, u, gwSetup{
		keys:       threeKeyProviders(),
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1", configs...)}},
	})
}

// fallbackProviders lists the providers of the fallbacks a request carried, in order.
func fallbackProviders(fallbacks []schemas.Fallback) []schemas.ModelProvider {
	out := make([]schemas.ModelProvider, len(fallbacks))
	for i, fallback := range fallbacks {
		out[i] = fallback.Provider
	}
	return out
}

// Equal virtual-key weights split bare requests evenly across the three providers: each share within
// shareZ standard deviations of 33.3%, and the counts fit 1/3 each by chi-square. Every request
// carries the two providers it did not pick as fallbacks, in either order (plan row GV-02). The row
// sends 1500 requests and judges at p > 0.01; this sends splitDraws and judges at p = 1e-6, so a
// correct split fails about once in a million runs and any share off by about 2.7 points fails.
func TestGatewayEqualWeightsSplitEvenly(t *testing.T) {
	u := newGWUpstream(t)
	one := schemas.Ptr(1.0)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: one, gwB: one, gwC: one})
	const n = splitDraws
	counts := map[schemas.ModelProvider]int{}
	for i := range n {
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		if r.err != nil {
			t.Fatalf("request %d failed: %s", i+1, r.err.GetErrorString())
		}
		counts[r.info.Provider]++
		attached := fallbackProviders(r.fallbacks)
		var want []schemas.ModelProvider
		for _, provider := range []schemas.ModelProvider{gwA, gwB, gwC} {
			if provider != r.info.Provider {
				want = append(want, provider)
			}
		}
		slices.Sort(attached)
		if !slices.Equal(attached, want) {
			t.Fatalf("request %d picked %s and carried fallbacks %v, want the other two %v", i+1, r.info.Provider, r.fallbacks, want)
		}
		if !strings.Contains(r.trail(), "Added 2 fallback providers") {
			t.Fatalf("request %d should report attaching the two unpicked providers:\n%s", i+1, r.trail())
		}
	}
	for _, provider := range []schemas.ModelProvider{gwA, gwB, gwC} {
		requireShare(t, counts, n, provider, 1.0/3)
	}
	if stat := chiSquare(counts, map[schemas.ModelProvider]float64{gwA: 1, gwB: 1, gwC: 1}); stat >= chiSquareCritical[2] {
		t.Fatalf("picks %v do not fit equal weights (chi-square %.2f, critical %.3f at p = 1e-6)", counts, stat, chiSquareCritical[2])
	}
	if total := u.count("sk-a1") + u.count("sk-a2") + u.count("sk-b1") + u.count("sk-b2") + u.count("sk-c1"); total != n {
		t.Fatalf("the upstream received %d requests for %d healthy requests", total, n)
	}
}

// Weights of 70/20/10 give a 70/20/10 split, each share within shareZ standard deviations of its
// weight and the counts fitting the weights by chi-square. Every request carries the two unpicked
// providers as fallbacks, heaviest first: an A pick gets [B, C], a B pick [A, C] and a C pick [A, B]
// (plan row GV-03). The row sends 2000 requests and judges at p > 0.01; this sends splitDraws and
// judges at p = 1e-6, so a correct split fails about once in a million runs while A off by about
// 2.6 points, B by 2.3 or C by 1.7 fails. The plan checks the order on 20 sampled requests; this
// checks it on all of them.
func TestGatewaySeventyTwentyTenSplitWithFallbacksByWeight(t *testing.T) {
	u := newGWUpstream(t)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: schemas.Ptr(0.7), gwB: schemas.Ptr(0.2), gwC: schemas.Ptr(0.1)})
	want := map[schemas.ModelProvider][]schemas.ModelProvider{gwA: {gwB, gwC}, gwB: {gwA, gwC}, gwC: {gwA, gwB}}
	const n = splitDraws
	counts := map[schemas.ModelProvider]int{}
	for i := range n {
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		if r.err != nil {
			t.Fatalf("request %d failed: %s", i+1, r.err.GetErrorString())
		}
		counts[r.info.Provider]++
		if got := fallbackProviders(r.fallbacks); !slices.Equal(got, want[r.info.Provider]) {
			t.Fatalf("request %d picked %s and carried fallbacks %v, want %v", i+1, r.info.Provider, got, want[r.info.Provider])
		}
	}
	requireShare(t, counts, n, gwA, 0.7)
	requireShare(t, counts, n, gwB, 0.2)
	requireShare(t, counts, n, gwC, 0.1)
	if stat := chiSquare(counts, map[schemas.ModelProvider]float64{gwA: 0.7, gwB: 0.2, gwC: 0.1}); stat >= chiSquareCritical[2] {
		t.Fatalf("picks %v do not fit 70/20/10 (chi-square %.2f, critical %.3f at p = 1e-6)", counts, stat, chiSquareCritical[2])
	}
}

// A weight-0 provider is never picked but rides as the last fallback: 500 bare requests all go to
// A with B attached behind it. With every A key answering 500, 500 more requests, with no session,
// each retry A's key three times and are served by B as the fallback of primary A (plan row GV-04).
func TestGatewayZeroWeightIsTheLastFallback(t *testing.T) {
	u := newGWUpstream(t)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: schemas.Ptr(1.0), gwB: schemas.Ptr(0.0)})
	for i := range 500 {
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		requireServedBy(t, r, gwA)
		if got := fallbackProviders(r.fallbacks); !slices.Equal(got, []schemas.ModelProvider{gwB}) {
			t.Fatalf("request %d carried fallbacks %v, want B alone", i+1, got)
		}
	}
	requireGWHits(t, u, map[string]int{"sk-b1": 0, "sk-b2": 0})

	u.answer("sk-a1", http.StatusInternalServerError)
	u.answer("sk-a2", http.StatusInternalServerError)
	for i := range 500 {
		u.clearHits()
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		requireServedBy(t, r, gwB)
		if !r.info.IsFallback || r.info.PrimaryProvider == nil || *r.info.PrimaryProvider != gwA {
			t.Fatalf("request %d should be served by B as the fallback of primary A, routing info %+v", i+1, r.info)
		}
		if a1, a2 := u.count("sk-a1"), u.count("sk-a2"); !(a1 == 3 && a2 == 0 || a1 == 0 && a2 == 3) {
			t.Fatalf("request %d should retry one A key three times, a1=%d a2=%d", i+1, a1, a2)
		}
		if b := u.count("sk-b1") + u.count("sk-b2"); b != 1 {
			t.Fatalf("request %d reached B %d times, want once", i+1, b)
		}
	}
}

// Fallbacks the caller sends replace the ones governance would attach: with A=1, B=1 both failing and
// the caller asking for C, each of 20 bare requests retries the provider governance picked three
// times, never touches the other, and is served by C as the fallback of that pick (plan row GV-08).
func TestGatewayCallerFallbacksReplaceGovernances(t *testing.T) {
	u := newGWUpstream(t)
	one := schemas.Ptr(1.0)
	// C is on the virtual key without a weight, so the key permits it but governance never picks it.
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: one, gwB: one, gwC: nil})
	for _, token := range []string{"sk-a1", "sk-a2", "sk-b1", "sk-b2"} {
		u.answer(token, http.StatusInternalServerError)
	}
	total := 0
	for i := range 20 {
		u.clearHits()
		r := g.send(gwCall{model: "m", fallbacks: []string{"gw-c/m"}, headers: vkHeaders("vk1", "")})
		requireServedBy(t, r, gwC)
		if !r.info.IsFallback || r.info.PrimaryProvider == nil {
			t.Fatalf("request %d should be served by C as a fallback, routing info %+v", i+1, r.info)
		}
		picked := *r.info.PrimaryProvider
		a, b := u.count("sk-a1")+u.count("sk-a2"), u.count("sk-b1")+u.count("sk-b2")
		switch picked {
		case gwA:
			if a != 3 || b != 0 {
				t.Fatalf("request %d picked A: A should be tried 3 times and B never, A=%d B=%d", i+1, a, b)
			}
		case gwB:
			if b != 3 || a != 0 {
				t.Fatalf("request %d picked B: B should be tried 3 times and A never, A=%d B=%d", i+1, a, b)
			}
		default:
			t.Fatalf("request %d's primary was %s, want governance's pick of A or B", i+1, picked)
		}
		if !strings.Contains(r.trail(), "Selected provider "+string(picked)+" for model m") {
			t.Fatalf("request %d's primary %s should be the provider governance selected:\n%s", i+1, picked, r.trail())
		}
		if got := fallbackProviders(r.fallbacks); !slices.Equal(got, []schemas.ModelProvider{gwC}) {
			t.Fatalf("request %d carried fallbacks %v, want the caller's C alone", i+1, got)
		}
		requireGWHits(t, u, map[string]int{"sk-c1": 1})
		total += a + b
	}
	if total != 60 {
		t.Fatalf("A and B together took %d attempts, want 20 × 3", total)
	}
}

// A pick of a failing A=0.9 fails over to the healthy B=0.1 it carries as a fallback: all 50 bare
// requests are served by B, those that picked A retry A's key three times first, and about 90% of
// them are fallbacks (plan row GV-09). Fewer than 35 fallbacks in 50 happens about twice in 100,000
// runs.
func TestGatewayFailedPickFailsOverToTheWeightedFallback(t *testing.T) {
	u := newGWUpstream(t)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: schemas.Ptr(0.9), gwB: schemas.Ptr(0.1)})
	u.answer("sk-a1", http.StatusInternalServerError)
	u.answer("sk-a2", http.StatusInternalServerError)
	fallbacks := 0
	for i := range 50 {
		u.clearHits()
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		requireServedBy(t, r, gwB)
		a1, a2 := u.count("sk-a1"), u.count("sk-a2")
		if r.info.IsFallback {
			fallbacks++
			if r.info.PrimaryProvider == nil || *r.info.PrimaryProvider != gwA || !(a1 == 3 && a2 == 0 || a1 == 0 && a2 == 3) {
				t.Fatalf("request %d picked A: one A key should be tried 3 times before B, a1=%d a2=%d info %+v", i+1, a1, a2, r.info)
			}
		} else if a1+a2 != 0 {
			t.Fatalf("request %d picked B but reached A %d times", i+1, a1+a2)
		}
	}
	if fallbacks < 35 {
		t.Fatalf("%d of 50 requests were fallbacks, want about 90%%", fallbacks)
	}
}

// key_ids restrict the pool: with vk1 allowing only a2 on A, 30 requests for A/m all use a2; once a2
// answers 429, every request retries a2 three times and is refused with the 429, since a1 and a3 are
// outside the key and cannot take the rotation (plan row GV-12).
func TestGatewayKeyIDsRestrictThePool(t *testing.T) {
	u := newGWUpstream(t)
	g := newGateway(t, u, gwSetup{
		keys:       map[schemas.ModelProvider][]schemas.Key{gwA: {gwKey("a1", 1), gwKey("a2", 1), gwKey("a3", 1)}},
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1", onlyKeys(gwVKProvider(gwA, schemas.Ptr(1.0)), "a2"))}},
	})
	for range 30 {
		r := g.send(gwCall{model: "gw-a/m", headers: vkHeaders("vk1", "")})
		requireServedBy(t, r, gwA)
		if r.info.Key != "a2" {
			t.Fatalf("served on %s, want a2, the only key vk1 allows", r.info.Key)
		}
	}
	requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 30, "sk-a3": 0})

	u.answer("sk-a2", http.StatusTooManyRequests)
	for range 12 {
		u.clearHits()
		r := g.send(gwCall{model: "gw-a/m", headers: vkHeaders("vk1", "")})
		requireFailedWith(t, r, http.StatusTooManyRequests)
		requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 3, "sk-a3": 0})
	}
}

// key_ids are re-applied on the fallback provider: vk1 weights a failing A=1 and B=0.1, and allows only
// b2 on B, whose b1 would win a free pick nine times in ten. Twenty bare requests are all served by B
// on b2, after three tries of A's key when A was picked; then requests continue until B has been the
// primary at least once, and it serves on b2 there too (plan row GV-13).
func TestGatewayKeyIDsApplyOnTheFallbackProvider(t *testing.T) {
	u := newGWUpstream(t)
	g := newGateway(t, u, gwSetup{
		keys: map[schemas.ModelProvider][]schemas.Key{
			gwA: {gwKey("a1", 1)},
			gwB: {gwKey("b1", 9), gwKey("b2", 1)},
		},
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1",
			gwVKProvider(gwA, schemas.Ptr(1.0)),
			onlyKeys(gwVKProvider(gwB, schemas.Ptr(0.1)), "b2"),
		)}},
	})
	u.answer("sk-a1", http.StatusInternalServerError)
	// check sends one request and fails unless B serves it on b2, after A's key three times when A
	// was picked; it reports whether B was the primary.
	check := func(i int) bool {
		u.clearHits()
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		requireServedBy(t, r, gwB)
		if r.info.Key != "b2" {
			t.Fatalf("request %d was served on %s, want b2, the only B key vk1 allows", i, r.info.Key)
		}
		wantA := 0
		if r.info.IsFallback {
			wantA = 3
		}
		requireGWHits(t, u, map[string]int{"sk-a1": wantA, "sk-b1": 0, "sk-b2": 1})
		return !r.info.IsFallback
	}
	for i := 1; i <= 20; i++ {
		check(i)
	}
	// B is the primary about one request in eleven; 300 more without one happens about once in 10^12.
	for i := 21; ; i++ {
		if check(i) {
			break
		}
		if i == 320 {
			t.Fatal("B was never the primary in 300 more requests")
		}
	}
}

// A weight change shifts the split on the very next requests, with nothing cached: 1000 requests at
// A=0.5, B=0.5 split evenly, and 1000 after the key is edited to A=0.9, B=0.1 split 90/10. Each phase
// is judged by chi-square at p = 1e-6, so a correct split fails about once in a million runs while a
// stale 50/50 after the edit fails by hundreds (plan row GV-16).
func TestGatewayWeightChangeShiftsTheSplit(t *testing.T) {
	u := newGWUpstream(t)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: schemas.Ptr(0.5), gwB: schemas.Ptr(0.5)})
	split := func() map[schemas.ModelProvider]int {
		counts := map[schemas.ModelProvider]int{}
		for range 1000 {
			r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
			if r.err != nil {
				t.Fatalf("request failed: %s", r.err.GetErrorString())
			}
			counts[r.info.Provider]++
		}
		return counts
	}
	if counts := split(); chiSquare(counts, map[schemas.ModelProvider]float64{gwA: 0.5, gwB: 0.5}) >= chiSquareCritical[1] {
		t.Fatalf("before the edit the picks %v do not fit 50/50", counts)
	}
	edited := gwVK("vk1", gwVKProvider(gwA, schemas.Ptr(0.9)), gwVKProvider(gwB, schemas.Ptr(0.1)))
	g.governance.GetGovernanceStore().UpdateVirtualKeyInMemory(t.Context(), &edited, nil, nil, nil)
	if counts := split(); chiSquare(counts, map[schemas.ModelProvider]float64{gwA: 0.9, gwB: 0.1}) >= chiSquareCritical[1] {
		t.Fatalf("after the edit the picks %v do not fit 90/10", counts)
	}
}

// A bound provider that later fails moves the session: vk1 weights A=1, B=0, and A has two keys.
// Turn 1 binds A and one of its keys. With every A key on 500, turn 2 retries the bound key three
// times, is served by B, moves the route binding to B and drops A's key binding. Turn 3 stays on B,
// and so does turn 4 after A recovers: a recovered provider does not win the session back (plan
// row PS-03).
func TestGatewayBoundProviderFailsAndTheSessionMoves(t *testing.T) {
	u := newGWUpstream(t)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: schemas.Ptr(1.0), gwB: schemas.Ptr(0.0)})
	r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
	requireServedBy(t, r, gwA)
	g.requireBinding(r, "route", "", "m", "gw-a/m")
	aKey := g.binding(r, "key", gwA, "m")
	if aKey == "" {
		t.Fatal("turn 1 bound no key on A")
	}

	u.answer("sk-a1", http.StatusInternalServerError)
	u.answer("sk-a2", http.StatusInternalServerError)
	u.clearHits()
	r = g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
	requireServedBy(t, r, gwB)
	if !r.info.IsFallback {
		t.Fatalf("turn 2 should be served by B as a fallback, routing info %+v", r.info)
	}
	if got := u.count("sk-a1") + u.count("sk-a2"); got != 3 || u.count("sk-"+aKey) != 3 {
		t.Fatalf("turn 2 should retry the bound %s three times and no other A key, a1=%d a2=%d", aKey, u.count("sk-a1"), u.count("sk-a2"))
	}
	g.requireBinding(r, "route", "", "m", "gw-b/m")
	g.requireBinding(r, "key", gwA, "m", "")
	if !strings.Contains(r.trail(), "The key this session followed for gw-a/m failed") {
		t.Fatalf("turn 2 should report dropping A's key binding:\n%s", r.trail())
	}

	for turn := 3; turn <= 4; turn++ {
		if turn == 4 {
			u.answer("sk-a1", http.StatusOK)
			u.answer("sk-a2", http.StatusOK)
		}
		u.clearHits()
		r = g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
		requireServedBy(t, r, gwB)
		if r.info.IsFallback {
			t.Fatalf("turn %d should start on B, routing info %+v", turn, r.info)
		}
		requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0})
		g.requireBinding(r, "route", "", "m", "gw-b/m")
	}
}

// A session keeps a separate route binding for each model it asks for: with vk1 weighting A and B
// equally for both m and m2, sessions alternate m and m2 six times each; every turn of a model is
// served by the provider that model's first turn bound, whatever the other model bound. Sessions are
// started until one binds the two models to different providers, so the bindings are shown apart
// (plan row PS-15).
func TestGatewaySessionBindsEachModelSeparately(t *testing.T) {
	u := newGWUpstream(t)
	one := schemas.Ptr(1.0)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: one, gwB: one})
	split := false
	for s := 0; s < 40 && !split; s++ {
		session := "s" + strings.Repeat("x", s)
		bound := map[string]schemas.ModelProvider{}
		var r gwResult
		for range 6 {
			for _, model := range []string{"m", "m2"} {
				r = g.send(gwCall{model: model, headers: vkHeaders("vk1", session)})
				if r.err != nil {
					t.Fatalf("session %d, %s failed: %s", s, model, r.err.GetErrorString())
				}
				if first, ok := bound[model]; !ok {
					bound[model] = r.info.Provider
				} else if r.info.Provider != first {
					t.Fatalf("session %d: %s moved from %s to %s", s, model, first, r.info.Provider)
				}
			}
		}
		for model, provider := range bound {
			g.requireBinding(r, "route", "", model, string(provider)+"/"+model)
		}
		split = bound["m"] != bound["m2"]
	}
	if !split {
		t.Fatal("no session in 40 bound m and m2 to different providers")
	}
}

// An x-bf-session-id one character over the limit gives the request no session, and a harness
// session header beside it is not used instead: with vk1 weighting A, B and C equally, every turn is
// served, leaves no session line in its trail and writes no binding, and the turns land on more than
// one provider, each rolled afresh. Sixty turns all landing on one provider by chance has odds of
// about 1e-28 (plan row PS-21).
func TestGatewayOverlongSessionIDGivesNoSession(t *testing.T) {
	u := newGWUpstream(t)
	one := schemas.Ptr(1.0)
	g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: one, gwB: one, gwC: one})
	headers := vkHeaders("vk1", strings.Repeat("a", schemas.MaxSessionIDLength+1))
	headers["x-claude-code-session-id"] = "cc-1"
	served := map[schemas.ModelProvider]int{}
	for turn := 1; turn <= 60; turn++ {
		r := g.send(gwCall{model: "m", headers: headers})
		if r.err != nil {
			t.Fatalf("turn %d failed: %s", turn, r.err.GetErrorString())
		}
		served[r.info.Provider]++
		if strings.Contains(strings.ToLower(r.trail()), "session") {
			t.Fatalf("turn %d has no session, yet its trail mentions one:\n%s", turn, r.trail())
		}
		if n := g.kv.Len(); n != 0 {
			t.Fatalf("turn %d has no session, yet the session store holds %d entries", turn, n)
		}
	}
	if len(served) < 2 {
		t.Fatalf("60 turns all landed on %v: the picks are not rolled afresh", served)
	}
}

// A pin on a key outside the virtual key's key_ids is refused with 400 before any upstream call
// (plan row KH-10).
func TestGatewayPinOutsideKeyIDsIsRefused(t *testing.T) {
	u := newGWUpstream(t)
	g := newGateway(t, u, gwSetup{
		keys:       map[schemas.ModelProvider][]schemas.Key{gwA: {gwKey("a1", 1), gwKey("a2", 1)}},
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1", onlyKeys(gwVKProvider(gwA, schemas.Ptr(1.0)), "a1"))}},
	})
	headers := vkHeaders("vk1", "")
	headers["x-bf-api-key-id"] = "a2"
	r := g.send(gwCall{model: "gw-a/m", headers: headers})
	requireFailedWith(t, r, http.StatusBadRequest)
	if !strings.Contains(r.err.GetErrorString(), `no supported key found with id "a2"`) {
		t.Fatalf("the refusal should be key selection's for the pinned a2: %s", r.err.GetErrorString())
	}
	requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0})
}

// A governance refusal drops the binding the refused turn followed. vk1 weights A=1, B=0 and allows
// two requests: turn 1 binds the session to A; with the weights then moved to A=0, B=1, turn 2
// still follows the binding to A against routing's proposal of B. The third turn finds vk1's request
// limit used up, is refused with 429, and since it followed the binding to A and failed, the binding
// is dropped. Once the limit is raised, turn 4 follows routing to B and binds it, rather than going
// back to A (plan row PS-19).
func TestGatewayGovernanceRefusalDropsTheBinding(t *testing.T) {
	u := newGWUpstream(t)
	limit := func(requests int64) configstoreTables.TableRateLimit {
		return configstoreTables.TableRateLimit{ID: "rl-vk1", RequestMaxLimit: schemas.Ptr(requests), RequestResetDuration: schemas.Ptr("1h"), RequestLastReset: time.Now()}
	}
	rateLimit := limit(2)
	vk := gwVK("vk1", gwVKProvider(gwA, schemas.Ptr(1.0)), gwVKProvider(gwB, schemas.Ptr(0.0)))
	vk.RateLimitID = schemas.Ptr(rateLimit.ID)
	vk.RateLimit = &rateLimit
	g := newGateway(t, u, gwSetup{
		keys:       threeKeyProviders(),
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{vk}, RateLimits: []configstoreTables.TableRateLimit{rateLimit}},
	})
	store := g.governance.GetGovernanceStore()

	r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
	requireServedBy(t, r, gwA)
	g.requireBinding(r, "route", "", "m", "gw-a/m")

	edited := vk
	edited.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{gwVKProvider(gwA, schemas.Ptr(0.0)), gwVKProvider(gwB, schemas.Ptr(1.0))}
	store.UpdateVirtualKeyInMemory(t.Context(), &edited, nil, nil, nil)
	r = g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
	requireServedBy(t, r, gwA)
	if !strings.Contains(r.trail(), "Session stays on gw-a/m for m; routing proposed gw-b/m") {
		t.Fatalf("turn 2 should follow the binding to A against routing's B:\n%s", r.trail())
	}

	// Usage is charged after the response, off the request path; wait until both requests count.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if used := store.LoadRateLimit(t.Context(), rateLimit.ID); used != nil && used.RequestCurrentUsage >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("vk1's request usage never reached its limit of 2")
		}
		time.Sleep(10 * time.Millisecond)
	}
	u.clearHits()
	r = g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
	requireFailedWith(t, r, http.StatusTooManyRequests)
	requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0, "sk-b1": 0, "sk-b2": 0})
	g.requireBinding(r, "route", "", "m", "")
	if !strings.Contains(r.trail(), "The provider this session followed for m failed") {
		t.Fatalf("the refused turn should report dropping the binding it followed:\n%s", r.trail())
	}

	raised := limit(100)
	store.UpsertRateLimitConfig(t.Context(), raised.ID, &raised)
	r = g.send(gwCall{model: "m", headers: vkHeaders("vk1", "s")})
	requireServedBy(t, r, gwB)
	if r.info.IsFallback {
		t.Fatalf("turn 4 should be routing's own pick of B, routing info %+v", r.info)
	}
	g.requireBinding(r, "route", "", "m", "gw-b/m")
}

// A model allowed on one provider config only: vk1 weights A and B equally, but lets A serve m and B
// only m2, though both providers' keys serve every model. All 50 bare requests for m go to A, the
// trail names B as excluded because the model is not permitted, the pick runs over A alone, and no
// fallback is attached (plan row GV-11).
func TestGatewayAllowedModelsExcludeAProvider(t *testing.T) {
	u := newGWUpstream(t)
	allowA := gwVKProvider(gwA, schemas.Ptr(1.0))
	allowA.AllowedModels = schemas.WhiteList{"m"}
	allowB := gwVKProvider(gwB, schemas.Ptr(1.0))
	allowB.AllowedModels = schemas.WhiteList{"m2"}
	g := newGateway(t, u, gwSetup{
		keys:       threeKeyProviders(),
		governance: configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{gwVK("vk1", allowA, allowB)}},
	})
	for i := range 50 {
		r := g.send(gwCall{model: "m", headers: vkHeaders("vk1", "")})
		requireServedBy(t, r, gwA)
		trail := r.trail()
		if !strings.Contains(trail, "Provider gw-b excluded: model m is not permitted") || !strings.Contains(trail, "Selected provider gw-a for model m (from 1 weighted: [gw-a])") {
			t.Fatalf("request %d's trail should exclude B for the model and pick over A alone:\n%s", i+1, trail)
		}
		if len(r.fallbacks) != 0 || strings.Contains(trail, "fallback providers") {
			t.Fatalf("request %d should carry no fallbacks, got %v\n%s", i+1, r.fallbacks, trail)
		}
	}
	requireGWHits(t, u, map[string]int{"sk-b1": 0, "sk-b2": 0})
}

// Streamed chat completions and /v1/responses bind and follow sessions as non-streamed chat does,
// with a pick whose every key answers 500: vk1 weights A=1, B=0, so turn 1 of a session retries A's
// key three times before B serves it as the fallback, and the stream's first chunk comes from B;
// turn 2 starts on B as the primary with A untouched (plan row PS-23, case (b), for the streamed
// chat and Responses request types; one subtest each).
func TestGatewayStreamsAndResponsesFollowSessionsPastAFailingPick(t *testing.T) {
	for _, surface := range []struct {
		name string
		call gwCall
	}{
		{name: "stream chat", call: gwCall{stream: true}},
		{name: "responses", call: gwCall{responses: true}},
	} {
		t.Run(surface.name, func(t *testing.T) {
			u := newGWUpstream(t)
			g := weightedVKGateway(t, u, map[schemas.ModelProvider]*float64{gwA: schemas.Ptr(1.0), gwB: schemas.Ptr(0.0)})
			u.answer("sk-a1", http.StatusInternalServerError)
			u.answer("sk-a2", http.StatusInternalServerError)
			call := surface.call
			call.model = "m"
			call.headers = vkHeaders("vk1", "s")

			r := g.send(call)
			requireServedBy(t, r, gwB)
			if !r.info.IsFallback || r.info.PrimaryProvider == nil || *r.info.PrimaryProvider != gwA {
				t.Fatalf("turn 1 should be served by B as the fallback of A, routing info %+v", r.info)
			}
			if a1, a2 := u.count("sk-a1"), u.count("sk-a2"); !(a1 == 3 && a2 == 0 || a1 == 0 && a2 == 3) {
				t.Fatalf("turn 1 should retry one A key three times first, a1=%d a2=%d", a1, a2)
			}
			g.requireBinding(r, "route", "", "m", "gw-b/m")

			u.clearHits()
			r = g.send(call)
			requireServedBy(t, r, gwB)
			if r.info.IsFallback {
				t.Fatalf("turn 2 should start on B as the primary, routing info %+v", r.info)
			}
			requireGWHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0})
		})
	}
}

// A direct key under a virtual key that restricts keys: vk1 allows only o1 on openai and carries a
// budget and a request limit, and the gateway lets callers send their own key. A request for
// openai/gpt-4o-mini with vk1 and the caller's key K is served on K, not on any pool key, though
// K is not among vk1's key_ids; and vk1 is still charged for it: its request count rises by one and
// its budget by the catalog's price of the request (plan row DK-12).
func TestGatewayDirectKeyUnderAVirtualKeyIsStillCharged(t *testing.T) {
	u := newGWUpstream(t)
	// The catalog reads its prices and model parameters from sheets the test writes, so the test
	// needs no network and its price is the one written here.
	dir := t.TempDir()
	pricingPath, paramsPath := filepath.Join(dir, "pricing.json"), filepath.Join(dir, "params.json")
	if err := os.WriteFile(pricingPath, []byte(`{"gpt-4o-mini": {"provider": "openai", "mode": "chat", "base_model": "gpt-4o-mini", "input_cost_per_token": 0.001, "output_cost_per_token": 0.002}}`), 0o600); err != nil {
		t.Fatalf("write the pricing sheet: %v", err)
	}
	if err := os.WriteFile(paramsPath, []byte(`{"gpt-4o-mini": {"mode": "chat", "model_parameters": [{"id": "temperature"}]}}`), 0o600); err != nil {
		t.Fatalf("write the model parameters sheet: %v", err)
	}
	catalog, err := modelcatalog.Init(t.Context(), &modelcatalog.Config{PricingURL: schemas.Ptr("file://" + pricingPath), ModelParametersURL: schemas.Ptr("file://" + paramsPath)}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("modelcatalog.Init: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Cleanup() })
	deadline := time.Now().Add(10 * time.Second)
	for catalog.GetPricingEntryForModel("gpt-4o-mini", schemas.OpenAI) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the catalog never loaded a price for openai/gpt-4o-mini")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if loaded := catalog.GetPricingEntryForModel("gpt-4o-mini", schemas.OpenAI); loaded.InputCostPerToken == nil || *loaded.InputCostPerToken != 0.001 {
		t.Fatalf("the catalog priced openai/gpt-4o-mini from somewhere other than the test's own sheet: input cost %v", loaded.InputCostPerToken)
	}
	budget := configstoreTables.TableBudget{ID: "budget-vk1", MaxLimit: 100, ResetDuration: "1h", LastReset: time.Now()}
	rateLimit := configstoreTables.TableRateLimit{ID: "rl-vk1", RequestMaxLimit: schemas.Ptr(int64(100)), RequestResetDuration: schemas.Ptr("1h"), RequestLastReset: time.Now()}
	openaiConfig := onlyKeys(gwVKProvider(schemas.OpenAI, schemas.Ptr(1.0)), "o1")
	openaiConfig.AllowedModels = schemas.WhiteList{"gpt-4o-mini"}
	vk := gwVK("vk1", openaiConfig)
	vk.RateLimitID = schemas.Ptr(rateLimit.ID)
	vk.RateLimit = &rateLimit
	budget.VirtualKeyID = schemas.Ptr(vk.ID)
	vk.Budgets = []configstoreTables.TableBudget{budget}
	g := newGateway(t, u, gwSetup{
		keys:            map[schemas.ModelProvider][]schemas.Key{schemas.OpenAI: {gwKey("o1", 1), gwKey("o2", 1)}},
		governance:      configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{vk}, RateLimits: []configstoreTables.TableRateLimit{rateLimit}, Budgets: []configstoreTables.TableBudget{budget}},
		catalog:         catalog,
		allowDirectKeys: true,
	})
	headers := vkHeaders("vk1", "")
	headers["x-bf-direct-key"] = "true"
	headers["Authorization"] = "Bearer sk-caller"
	r := g.send(gwCall{model: "openai/gpt-4o-mini", headers: headers})
	requireServedBy(t, r, schemas.OpenAI)
	requireGWHits(t, u, map[string]int{"sk-caller": 1, "sk-o1": 0, "sk-o2": 0})

	store := g.governance.GetGovernanceStore()
	price := catalog.GetPricingEntryForModel("gpt-4o-mini", schemas.OpenAI)
	deadline = time.Now().Add(5 * time.Second)
	for {
		used := store.LoadRateLimit(t.Context(), rateLimit.ID)
		spent := store.LoadBudget(t.Context(), budget.ID)
		if used != nil && spent != nil && used.RequestCurrentUsage == 1 && spent.CurrentUsage > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("vk1 was not charged for the direct-key request: rate limit %+v budget %+v (price %+v)", used, spent, price)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A direct key the provider refuses, sent the way a caller sends it (x-bf-direct-key: true and the
// key as a bearer token) to a gateway that allows direct keys: at max_retries 0, 1 and 3, with a
// healthy pool key configured beside it, the provider's own 401 comes back from one attempt on the
// caller's key, never 502 upstream_credentials_exhausted, and the pool key is never used (plan row
// DK-02; one subtest per budget).
func TestGatewayRefusedDirectKeyIsTheCallersOwn401(t *testing.T) {
	for _, maxRetries := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("max_retries %d", maxRetries), func(t *testing.T) {
			u := newGWUpstream(t)
			g := newGateway(t, u, gwSetup{
				keys:            map[schemas.ModelProvider][]schemas.Key{gwA: {gwKey("a1", 1)}},
				maxRetries:      map[schemas.ModelProvider]int{gwA: maxRetries},
				allowDirectKeys: true,
			})
			u.answer("sk-caller", http.StatusUnauthorized)
			r := g.send(gwCall{model: "gw-a/m", headers: map[string]string{"x-bf-direct-key": "true", "Authorization": "Bearer sk-caller"}})
			requireFailedWith(t, r, http.StatusUnauthorized)
			if !strings.Contains(r.err.GetErrorString(), "Incorrect API key") {
				t.Fatalf("the 401 should be the provider's own refusal, got %s", r.err.GetErrorString())
			}
			requireGWHits(t, u, map[string]int{"sk-caller": 1, "sk-a1": 0})
		})
	}
}
