package handlers

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// sessionCookieStore serves a fixed admin login and accepts any session write.
type sessionCookieStore struct {
	configstore.ConfigStore
	passwordHash string
}

func (s *sessionCookieStore) GetAuthConfig(context.Context) (*configstore.AuthConfig, error) {
	return &configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar(s.passwordHash),
		IsEnabled:     true,
	}, nil
}

func (s *sessionCookieStore) CreateSession(context.Context, *tables.SessionsTable) error { return nil }
func (s *sessionCookieStore) DeleteSession(context.Context, string) error                { return nil }

// tlsTestConn is a connection that fasthttp's IsTLS recognises as TLS, as it
// recognises the connections the HTTPS listener hands it.
type tlsTestConn struct{ net.Conn }

func (tlsTestConn) Handshake() error { return nil }
func (tlsTestConn) ConnectionState() tls.ConnectionState {
	return tls.ConnectionState{HandshakeComplete: true}
}

// The login and logout cookies must be Secure whenever the client connection is
// HTTPS: forwarded by a TLS-terminating proxy, or served by Bifrost's own HTTPS
// listener.
func TestSessionCookiesSecureOverHTTPS(t *testing.T) {
	hash, err := encrypt.Hash("s3cret")
	require.NoError(t, err)
	h := NewSessionHandler(&sessionCookieStore{passwordHash: hash}, nil)

	newCtx := func(t *testing.T, mode string) *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
		var conn net.Conn = server
		if mode == "direct TLS" {
			conn = tlsTestConn{server}
		}
		ctx.Init2(conn, nil, false)
		if mode == "proxy TLS" {
			ctx.Request.Header.Set("X-Forwarded-Proto", "https")
		}
		return ctx
	}
	tokenCookie := func(t *testing.T, ctx *fasthttp.RequestCtx) string {
		c := fasthttp.AcquireCookie()
		defer fasthttp.ReleaseCookie(c)
		c.SetKey("token")
		require.True(t, ctx.Response.Header.Cookie(c), "response sets the token cookie: %s", ctx.Response.Body())
		return string(c.Cookie())
	}

	for _, tc := range []struct {
		mode   string
		secure bool
	}{
		{"plain HTTP", false},
		{"proxy TLS", true},
		{"direct TLS", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			login := newCtx(t, tc.mode)
			login.Request.SetBody([]byte(`{"username":"admin","password":"s3cret"}`))
			h.login(login)
			require.Equal(t, fasthttp.StatusOK, login.Response.StatusCode(), string(login.Response.Body()))
			assert.Equal(t, tc.secure, strings.Contains(strings.ToLower(tokenCookie(t, login)), "; secure"), "login cookie")

			logout := newCtx(t, tc.mode)
			logout.Request.Header.Set("Authorization", "Bearer some-token")
			h.logout(logout)
			assert.Equal(t, tc.secure, strings.Contains(strings.ToLower(tokenCookie(t, logout)), "; secure"), "logout cookie")
		})
	}
}
