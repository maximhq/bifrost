package utils

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubTokenProvider struct {
	token string
	err   error
	got   string
}

func (s *stubTokenProvider) GetTokenForUpstream(_ context.Context, serverName string) (string, error) {
	s.got = serverName
	return s.token, s.err
}

type stubLogger struct {
	schemas.Logger
	warns []string
}

func (s *stubLogger) Warn(format string, args ...any) {
	s.warns = append(s.warns, fmt.Sprintf(format, args...))
}

func extrasConfig() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{AllowedExtraHeaders: schemas.WhiteList{"X-Allowed", "Authorization"}}
}

func TestPerRequestHeaders(t *testing.T) {
	extras := map[string][]string{"X-Allowed": {"a"}, "X-Denied": {"b"}, "Authorization": {"Bearer from-extras"}}
	base := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPExtraHeaders, extras)

	t.Run("no token returns filtered extras unchanged", func(t *testing.T) {
		got := PerRequestHeaders(base, extrasConfig())
		assert.Equal(t, map[string]string{"X-Allowed": "a", "Authorization": "Bearer from-extras"}, got)
	})

	t.Run("carrier token overrides an extra Authorization", func(t *testing.T) {
		ctx := context.WithValue(base, callTokenKey{}, "tok")
		got := PerRequestHeaders(ctx, extrasConfig())
		assert.Equal(t, "Bearer tok", got["Authorization"])
		assert.Equal(t, "a", got["X-Allowed"])
		assert.NotContains(t, got, "X-Denied")
	})

	t.Run("public key alone does not affect wire headers", func(t *testing.T) {
		ctx := context.WithValue(base, schemas.BifrostContextKeyMCPOutboundToken, "tok")
		got := PerRequestHeaders(ctx, extrasConfig())
		assert.Equal(t, "Bearer from-extras", got["Authorization"])
	})

	t.Run("token without extras", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), callTokenKey{}, "tok")
		assert.Equal(t, map[string]string{"Authorization": "Bearer tok"}, PerRequestHeaders(ctx, extrasConfig()))
	})

	t.Run("empty carrier token is ignored", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), callTokenKey{}, "")
		assert.Empty(t, PerRequestHeaders(ctx, extrasConfig()))
	})

	t.Run("nil ctx", func(t *testing.T) {
		assert.Empty(t, PerRequestHeaders(nil, extrasConfig())) //nolint:staticcheck // nil ctx tolerated by design
	})
}

func TestApplyOutboundToken(t *testing.T) {
	h := http.Header{"Authorization": {"Bearer old"}}
	ApplyOutboundToken(context.Background(), h)
	assert.Equal(t, "Bearer old", h.Get("Authorization"))

	ctxWithKeyOnly := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPOutboundToken, "tok")
	ApplyOutboundToken(ctxWithKeyOnly, h)
	assert.Equal(t, "Bearer old", h.Get("Authorization"))

	ctxWithCarrier := context.WithValue(context.Background(), callTokenKey{}, "tok")
	ApplyOutboundToken(ctxWithCarrier, h)
	assert.Equal(t, []string{"Bearer tok"}, h["Authorization"])
}

func TestResolveOutboundToken(t *testing.T) {
	logger := &stubLogger{}

	t.Run("provider returns token", func(t *testing.T) {
		p := &stubTokenProvider{token: "prov-token"}
		tok, err := ResolveOutboundToken(context.Background(), p, "srv", logger)
		require.NoError(t, err)
		assert.Equal(t, "prov-token", tok)
		assert.Equal(t, "srv", p.got)
	})

	t.Run("provider returns empty string falls back to context key", func(t *testing.T) {
		p := &stubTokenProvider{token: ""}
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPOutboundToken, "ctx-token")
		tok, err := ResolveOutboundToken(ctx, p, "srv", logger)
		require.NoError(t, err)
		assert.Equal(t, "ctx-token", tok)
	})

	t.Run("provider error is wrapped with fixed message and logged", func(t *testing.T) {
		l := &stubLogger{}
		p := &stubTokenProvider{err: errors.New("secret backend detail")}
		tok, err := ResolveOutboundToken(context.Background(), p, "srv", l)
		require.Error(t, err)
		assert.Empty(t, tok)
		assert.Equal(t, "failed to resolve outbound token for MCP server 'srv'", err.Error())
		assert.NotContains(t, err.Error(), "secret backend detail")
		require.Len(t, l.warns, 1)
		assert.Contains(t, l.warns[0], "secret backend detail")
	})

	t.Run("nil provider falls back to context key", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPOutboundToken, "ctx-token")
		tok, err := ResolveOutboundToken(ctx, nil, "srv", logger)
		require.NoError(t, err)
		assert.Equal(t, "ctx-token", tok)
	})

	t.Run("no provider and no key returns empty string", func(t *testing.T) {
		tok, err := ResolveOutboundToken(context.Background(), nil, "srv", logger)
		require.NoError(t, err)
		assert.Empty(t, tok)
	})
}

func TestWithCallToken(t *testing.T) {
	bCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	child, release := WithCallToken(bCtx, "tok")
	defer release()

	assert.True(t, HasCallToken(child))
	assert.Equal(t, "tok", callToken(child))

	unchanged, releaseNoop := WithCallToken(bCtx, "")
	defer releaseNoop()
	assert.False(t, HasCallToken(unchanged))
	assert.Same(t, bCtx, unchanged)
}

var headersSink map[string]string

func BenchmarkPerRequestHeaders(b *testing.B) {
	cfg := extrasConfig()
	extras := map[string][]string{"X-Allowed": {"a"}}
	withExtras := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPExtraHeaders, extras)
	withToken := context.WithValue(withExtras, callTokenKey{}, "tok")

	b.Run("baseline_flatten_extract/no_token", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			headersSink = FlattenHeaders(ExtractFilteredExtras(withExtras, cfg))
		}
	})
	b.Run("no_token", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			headersSink = PerRequestHeaders(withExtras, cfg)
		}
	})
	b.Run("no_extras_no_token", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			headersSink = PerRequestHeaders(context.Background(), cfg)
		}
	})
	b.Run("token", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			headersSink = PerRequestHeaders(withToken, cfg)
		}
	})
}
