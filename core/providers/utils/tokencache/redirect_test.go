package tokencache

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redirectReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return &http.Request{URL: u}
}

func TestCheckTokenRedirect(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		to      string
		refused bool
	}{
		{"https to https is allowed", "https://idp.example/token", "https://idp2.example/token", false},
		{"loopback http to loopback http is allowed", "http://127.0.0.1:1/token", "http://localhost:2/token", false},
		{"https to loopback http is a downgrade", "https://127.0.0.1:1/token", "http://127.0.0.1:2/token", true},
		{"https to http is a downgrade", "https://idp.example/token", "http://idp.example/token", true},
		{"loopback http to non-loopback http is refused", "http://127.0.0.1:1/token", "http://203.0.113.9/token", true},
		{"loopback http to a host that only looks like loopback is refused", "http://127.0.0.1:1/token", "http://127.0.0.1.evil.example/token", true},
		{"loopback http to https is allowed", "http://127.0.0.1:1/token", "https://idp.example/token", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTokenRedirect(redirectReq(t, tc.to), []*http.Request{redirectReq(t, tc.from)})
			if tc.refused {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "example", "the redirect target stays out of the error")
				return
			}
			assert.NoError(t, err)
		})
	}

	t.Run("the first request is never a redirect", func(t *testing.T) {
		assert.NoError(t, checkTokenRedirect(redirectReq(t, "http://203.0.113.9/token"), nil))
	})
}

func TestGuardRedirects(t *testing.T) {
	via := func(n int) []*http.Request {
		out := make([]*http.Request, n)
		for i := range out {
			out[i] = redirectReq(t, "https://idp.example/token")
		}
		return out
	}

	t.Run("a nil client is guarded, not left as the default client", func(t *testing.T) {
		hc := guardRedirects(nil)
		require.NotNil(t, hc)
		assert.NotSame(t, http.DefaultClient, hc)
		assert.Nil(t, http.DefaultClient.CheckRedirect, "the default client is never mutated")
		require.NotNil(t, hc.CheckRedirect)
	})

	t.Run("the caller's client is copied, not mutated, and keeps its transport", func(t *testing.T) {
		tr := &http.Transport{}
		base := &http.Client{Transport: tr}
		hc := guardRedirects(base)
		assert.NotSame(t, base, hc)
		assert.Nil(t, base.CheckRedirect)
		assert.Same(t, tr, hc.Transport)
	})

	t.Run("without a caller policy the ten-hop limit applies", func(t *testing.T) {
		hc := guardRedirects(&http.Client{})
		assert.NoError(t, hc.CheckRedirect(redirectReq(t, "https://idp.example/token"), via(9)))
		assert.Error(t, hc.CheckRedirect(redirectReq(t, "https://idp.example/token"), via(10)))
	})

	t.Run("a safe hop is delegated to the caller's policy", func(t *testing.T) {
		calls := 0
		hc := guardRedirects(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			calls++
			return http.ErrUseLastResponse
		}})
		err := hc.CheckRedirect(redirectReq(t, "https://idp.example/token"), via(1))
		assert.True(t, errors.Is(err, http.ErrUseLastResponse))
		assert.Equal(t, 1, calls)

		// An unsafe hop is refused before the caller's policy sees it.
		err = hc.CheckRedirect(redirectReq(t, "http://idp.example/token"), via(1))
		require.Error(t, err)
		assert.False(t, errors.Is(err, http.ErrUseLastResponse))
		assert.Equal(t, 1, calls)
	})
}
