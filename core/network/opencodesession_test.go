package network

import (
	"context"
	"regexp"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// newTestOpencodeCtx builds a BifrostContext carrying the request headers the
// HTTP transport would have captured.
func newTestOpencodeCtx(t *testing.T, requestHeaders map[string]string) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if len(requestHeaders) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, requestHeaders)
	}
	return ctx
}

var uuidValueRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestIsSafeOpencodeSessionValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "plain value", value: "session-abc-123", want: true},
		{name: "value with spaces", value: "a b c", want: true},
		{name: "value with colon", value: "vk-1:session-1", want: true},
		{name: "value with unicode", value: "séance-☃", want: true},
		{name: "carriage return rejected", value: "abc\r\nInjected: x", want: false},
		{name: "line feed rejected", value: "abc\nInjected", want: false},
		{name: "control byte rejected", value: "abc\x01def", want: false},
		{name: "tab rejected", value: "abc\tdef", want: false},
		{name: "DEL rejected", value: "abc\x7fdef", want: false},
		{name: "NUL rejected", value: "abc\x00def", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSafeOpencodeSessionValue(tt.value); got != tt.want {
				t.Errorf("isSafeOpencodeSessionValue(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestResolveOpencodeSessionClientHeaderWins(t *testing.T) {
	t.Run("forwarded verbatim and namespaced per virtual key", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{
			OpencodeSessionHeader: "client-sess-abc",
		})
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")

		if got := ResolveOpencodeSession(ctx); got != "vk-1:client-sess-abc" {
			t.Errorf("ResolveOpencodeSession() = %q, want %q", got, "vk-1:client-sess-abc")
		}
	})

	t.Run("forwarded verbatim without a virtual key", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{
			OpencodeSessionHeader: "client-sess-abc",
		})
		if got := ResolveOpencodeSession(ctx); got != "client-sess-abc" {
			t.Errorf("ResolveOpencodeSession() = %q, want %q", got, "client-sess-abc")
		}
	})

	t.Run("client header beats x-bf-session-id", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{
			OpencodeSessionHeader: "client-sess-abc",
		})
		ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
		if got := ResolveOpencodeSession(ctx); got != "client-sess-abc" {
			t.Errorf("ResolveOpencodeSession() = %q, want client header to win over %q", got, "bf-sess-123")
		}
	})
}

func TestResolveOpencodeSessionFallbackChain(t *testing.T) {
	t.Run("session id used when client header absent", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, nil)
		ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
		if got := ResolveOpencodeSession(ctx); got != "vk-1:bf-sess-123" {
			t.Errorf("ResolveOpencodeSession() = %q, want %q", got, "vk-1:bf-sess-123")
		}
	})

	t.Run("session id used when client header is empty", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{OpencodeSessionHeader: ""})
		ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
		if got := ResolveOpencodeSession(ctx); got != "bf-sess-123" {
			t.Errorf("ResolveOpencodeSession() = %q, want %q", got, "bf-sess-123")
		}
	})

	t.Run("harness session id used when neither client header nor x-bf-session-id present", func(t *testing.T) {
		// The harness-vs-x-bf-session-id distinction is resolved by the ingress
		// (ResolveSessionIDFromHeaders); the egress only sees the outcome under
		// BifrostContextKeySessionID, so a harness-derived value flows through
		// the same path.
		ctx := newTestOpencodeCtx(t, nil)
		ctx.SetValue(schemas.BifrostContextKeySessionID, "harness-sess-42")
		if got := ResolveOpencodeSession(ctx); got != "harness-sess-42" {
			t.Errorf("ResolveOpencodeSession() = %q, want %q", got, "harness-sess-42")
		}
	})

	t.Run("poisoned client header falls back to session id", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{
			OpencodeSessionHeader: "evil\r\nInjected: x",
		})
		ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
		if got := ResolveOpencodeSession(ctx); got != "bf-sess-123" {
			t.Errorf("ResolveOpencodeSession() = %q, want fallback %q", got, "bf-sess-123")
		}
	})

	t.Run("poisoned client header falls back to synthesized UUID", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{
			OpencodeSessionHeader: "abc\x01def",
		})
		got := ResolveOpencodeSession(ctx)
		if !uuidValueRe.MatchString(got) {
			t.Errorf("ResolveOpencodeSession() = %q, want synthesized UUID", got)
		}
	})

	t.Run("whitespace-only client header is treated as absent", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{
			OpencodeSessionHeader: "   ",
		})
		ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
		if got := ResolveOpencodeSession(ctx); got != "bf-sess-123" {
			t.Errorf("ResolveOpencodeSession() = %q, want fallback %q", got, "bf-sess-123")
		}
	})

	t.Run("session id with control bytes falls back to synthesized UUID", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, nil)
		ctx.SetValue(schemas.BifrostContextKeySessionID, "abc\x01def")
		got := ResolveOpencodeSession(ctx)
		if !uuidValueRe.MatchString(got) {
			t.Errorf("ResolveOpencodeSession() = %q, want synthesized UUID for poisoned session id", got)
		}
	})
}

func TestResolveOpencodeSessionSynthesizesUUID(t *testing.T) {
	t.Run("valid UUID when no signal at all", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, nil)
		got := ResolveOpencodeSession(ctx)
		if !uuidValueRe.MatchString(got) {
			t.Errorf("ResolveOpencodeSession() = %q, want a UUID", got)
		}
	})

	t.Run("UUIDs differ across requests", func(t *testing.T) {
		first := ResolveOpencodeSession(newTestOpencodeCtx(t, nil))
		second := ResolveOpencodeSession(newTestOpencodeCtx(t, nil))
		if first == second {
			t.Errorf("ResolveOpencodeSession() returned the same synthesized UUID for two requests: %q", first)
		}
	})

	t.Run("namespaced UUID is still a UUID after the prefix", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, nil)
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
		got := ResolveOpencodeSession(ctx)
		prefix := "vk-1:"
		if len(got) <= len(prefix) || got[:len(prefix)] != prefix {
			t.Fatalf("ResolveOpencodeSession() = %q, want %q prefix", got, prefix)
		}
		if !uuidValueRe.MatchString(got[len(prefix):]) {
			t.Errorf("ResolveOpencodeSession() = %q, want UUID after namespace prefix", got)
		}
	})
}

func TestResolveOpencodeSessionNamespacesPerVirtualKey(t *testing.T) {
	// Same input session under two virtual keys must not produce the same
	// upstream value; the same virtual key must stay stable across calls.
	build := func(vk string) *schemas.BifrostContext {
		ctx := newTestOpencodeCtx(t, map[string]string{OpencodeSessionHeader: "shared-session"})
		if vk != "" {
			ctx.SetValue(schemas.BifrostContextKeyVirtualKey, vk)
		}
		return ctx
	}

	vk1 := ResolveOpencodeSession(build("vk-1"))
	vk2 := ResolveOpencodeSession(build("vk-2"))
	if vk1 == vk2 {
		t.Errorf("ResolveOpencodeSession() produced the same value for two virtual keys: %q", vk1)
	}
	if vk1 != "vk-1:shared-session" || vk2 != "vk-2:shared-session" {
		t.Errorf("ResolveOpencodeSession() = %q and %q, want vk-scoped values", vk1, vk2)
	}
	if again := ResolveOpencodeSession(build("vk-1")); again != vk1 {
		t.Errorf("ResolveOpencodeSession() not stable for the same virtual key: %q vs %q", again, vk1)
	}

	t.Run("unsafe virtual key leaves value unqualified", func(t *testing.T) {
		ctx := newTestOpencodeCtx(t, map[string]string{OpencodeSessionHeader: "shared-session"})
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "evil\r\nvk")
		if got := ResolveOpencodeSession(ctx); got != "shared-session" {
			t.Errorf("ResolveOpencodeSession() = %q, want unqualified value with unsafe virtual key", got)
		}
	})
}

func TestResolveOpencodeSessionResolvesOncePerRequest(t *testing.T) {
	ctx := newTestOpencodeCtx(t, nil)
	first := ResolveOpencodeSession(ctx)
	second := ResolveOpencodeSession(ctx)
	if first != second {
		t.Errorf("ResolveOpencodeSession() resolved twice: %q then %q, want a single cached value", first, second)
	}
	// The cached value must be the namespaced, final form.
	if !uuidValueRe.MatchString(first) {
		t.Errorf("ResolveOpencodeSession() = %q, want a UUID", first)
	}
}

func TestResolveOpencodeSessionNilContext(t *testing.T) {
	if got := ResolveOpencodeSession(nil); got != "" {
		t.Errorf("ResolveOpencodeSession(nil) = %q, want \"\"", got)
	}
}
