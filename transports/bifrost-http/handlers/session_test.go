package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// setCookieHeaders returns one Set-Cookie line per response cookie. fasthttp's
// PeekAll(Set-Cookie) joins every cookie into a single value, which would let an
// assertion about one cookie pass on the strength of another.
func setCookieHeaders(ctx *fasthttp.RequestCtx) []string {
	var out []string
	for _, line := range ctx.Response.Header.Cookies() {
		out = append(out, string(line))
	}
	return out
}

// TestSessionCookies_SecureFollowsRequestScheme pins that the session cookies take
// their Secure flag from the same scheme rule as every public URL
// (lib.RequestIsHTTPS): a proxy that only sets x-bf-forwarded-proto, or sends a
// comma-separated X-Forwarded-Proto chain, still gets a Secure cookie.
func TestSessionCookies_SecureFollowsRequestScheme(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"x-forwarded-proto":    {"X-Forwarded-Proto": "https"},
		"x-forwarded chain":    {"X-Forwarded-Proto": "https, http"},
		"x-bf-forwarded-proto": {"x-bf-forwarded-proto": "https"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, write := range []struct {
				name  string
				write func(ctx *fasthttp.RequestCtx)
			}{
				{"setup session", func(ctx *fasthttp.RequestCtx) { setSetupSessionCookie(ctx, "v", time.Now().Add(time.Hour)) }},
				{"token", func(ctx *fasthttp.RequestCtx) { setSessionTokenCookie(ctx, "v", time.Now().Add(time.Hour)) }},
			} {
				ctx := &fasthttp.RequestCtx{}
				for k, v := range headers {
					ctx.Request.Header.Set(k, v)
				}
				write.write(ctx)
				cookies := setCookieHeaders(ctx)
				require.Len(t, cookies, 1, write.name)
				assert.Contains(t, strings.ToLower(cookies[0]), "; secure", write.name)
			}
		})
	}

	ctx := &fasthttp.RequestCtx{}
	setSessionTokenCookie(ctx, "v", time.Now().Add(time.Hour))
	assert.NotContains(t, strings.ToLower(setCookieHeaders(ctx)[0]), "; secure", "plain http stays non-Secure")
}

// TestSessionCookies_PathStaysRootUnderBasePath pins a deliberate decision: the
// session cookies keep Path=/ even under server.base_path. Requests are accepted
// with or without the prefix (config.schema.json), so a cookie scoped to
// "/bifrost/" would stop riding the unprefixed /api calls that contract allows,
// and fasthttp's SetCookie keys by name, so logout could not expire both paths.
func TestSessionCookies_PathStaysRootUnderBasePath(t *testing.T) {
	lib.SetBasePath("/bifrost")
	t.Cleanup(func() { lib.SetBasePath("") })
	ctx := &fasthttp.RequestCtx{}
	setSessionTokenCookie(ctx, "v", time.Now().Add(time.Hour))
	setSetupSessionCookie(ctx, "v", time.Now().Add(time.Hour))
	cookies := setCookieHeaders(ctx)
	require.Len(t, cookies, 2, "one Set-Cookie line per cookie, so each path is checked on its own")
	for _, c := range cookies {
		assert.Contains(t, strings.ToLower(c), "; path=/;", c)
		assert.NotContains(t, strings.ToLower(c), "path=/bifrost", c)
	}
}
