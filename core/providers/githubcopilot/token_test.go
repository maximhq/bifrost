package githubcopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestIsGithubUserToken(t *testing.T) {
	assert.True(t, isGithubUserToken("ghu_abc"), "GitHub App user-to-server token")
	assert.True(t, isGithubUserToken("gho_abc"), "OAuth app token")
	assert.False(t, isGithubUserToken("github_pat_abc"), "Copilot's token exchange refuses PATs; they go direct")
	assert.True(t, isPersonalAccessToken("github_pat_abc"))
	assert.False(t, isPersonalAccessToken("ghu_abc"))
	assert.False(t, isGithubUserToken("tid=abc;exp=123"), "a Copilot API token is used verbatim")
	assert.False(t, isGithubUserToken("ghs_abc"), "Copilot does not exchange installation tokens")
	assert.False(t, isGithubUserToken("ghp_abc"), "Copilot rejects classic personal access tokens")
}

// ---------------------------------------------------------------------------
// Allowlist. This decides where prompts and a bearer token are sent.
// ---------------------------------------------------------------------------

func TestValidateCopilotAPIBaseURL(t *testing.T) {
	t.Run("rejects", func(t *testing.T) {
		tests := []struct {
			name string
			url  string
		}{
			// The one a bare HasSuffix check would wave through.
			{"lookalike registered domain", "https://evilgithubcopilot.com"},
			{"allowed domain as a prefix", "https://githubcopilot.com.evil.com"},
			{"plaintext http", "http://api.githubcopilot.com"},
			{"userinfo credentials", "https://user:pass@api.githubcopilot.com"},
			{"allowed host smuggled into userinfo", "https://api.githubcopilot.com@evil.com"},
			{"non-standard port", "https://api.githubcopilot.com:8443"},
			{"loopback IP literal", "https://127.0.0.1"},
			{"IPv6 literal", "https://[::1]"},
			{"scheme-relative", "//api.githubcopilot.com"},
			{"empty", ""},
			{"whitespace only", "   "},
			{"unrelated host", "https://evil.com"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := validateCopilotAPIBaseURL(tt.url)
				assert.Error(t, err, "%q must not be accepted as a Copilot host", tt.url)
			})
		}
	})

	t.Run("accepts and normalises", func(t *testing.T) {
		tests := []struct {
			name     string
			url      string
			expected string
		}{
			{"public host", "https://api.githubcopilot.com", "https://api.githubcopilot.com"},
			{"business tier", "https://api.business.githubcopilot.com", "https://api.business.githubcopilot.com"},
			{"enterprise tier", "https://api.enterprise.githubcopilot.com", "https://api.enterprise.githubcopilot.com"},
			{"individual tier", "https://api.individual.githubcopilot.com", "https://api.individual.githubcopilot.com"},
			{"apex domain", "https://githubcopilot.com", "https://githubcopilot.com"},
			{"trailing slash stripped", "https://api.githubcopilot.com/", "https://api.githubcopilot.com"},
			{"uppercase lowered", "https://API.GithubCopilot.COM", "https://api.githubcopilot.com"},
			{"fqdn root dot stripped", "https://api.githubcopilot.com.", "https://api.githubcopilot.com"},
			{"explicit 443", "https://api.githubcopilot.com:443", "https://api.githubcopilot.com"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := validateCopilotAPIBaseURL(tt.url)
				require.NoError(t, err)
				assert.Equal(t, tt.expected, got)
			})
		}
	})
}

// ---------------------------------------------------------------------------
// The exchange.
// ---------------------------------------------------------------------------

// fakeGithub stands in for the Copilot token endpoint on api.github.com.
type fakeGithub struct {
	server          *httptest.Server
	hits            atomic.Int64
	status          atomic.Int64
	endpoint        atomic.Value // string, the advertised endpoints.api
	tokenExpirySecs atomic.Int64
	refreshIn       atomic.Int64
	delay           atomic.Int64 // milliseconds
	auth            atomic.Value // string, Authorization on the last exchange
	userAgent       atomic.Value // string, User-Agent on the last exchange
}

func newFakeGithub(t *testing.T) *fakeGithub {
	t.Helper()
	f := &fakeGithub{}
	f.status.Store(http.StatusOK)
	f.endpoint.Store("https://api.business.githubcopilot.com")
	f.tokenExpirySecs.Store(1800)

	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.auth.Store(r.Header.Get("Authorization"))
		f.userAgent.Store(r.Header.Get("User-Agent"))
		if d := f.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		status := int(f.status.Load())
		w.Header().Set("Content-Type", "application/json")
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = fmt.Fprint(w, `{"message":"upstream said no"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"token":"copilot_fake","expires_at":%d,"refresh_in":%d,"endpoints":{"api":%q}}`,
			time.Now().Add(time.Duration(f.tokenExpirySecs.Load())*time.Second).Unix(),
			f.refreshIn.Load(),
			f.endpoint.Load().(string))
	}))
	t.Cleanup(f.server.Close)
	return f
}

// mint runs the exchange against the fake server. userToken varies per test so each gets its
// own slot in the package-level cache.
//
// It deliberately takes no *testing.T: the concurrency tests call it from a hundred
// goroutines, and require/assert reach t.FailNow, which is only supported on the test
// goroutine.
func (f *fakeGithub) mint(userToken, configuredBaseURL string, logger schemas.Logger) (*copilotCredentials, *schemas.BifrostError) {
	return mintWithTokenURL(context.Background(), f.server.URL, userToken, &fasthttp.Client{}, configuredBaseURL, logger)
}

func TestMintFromUserToken(t *testing.T) {
	t.Run("exchanges the user token and learns the plan's host", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.endpoint.Store("https://api.individual.githubcopilot.com")

		creds, bErr := fake.mint("ghu_learns_host", "", noopLogger{})

		require.Nil(t, bErr)
		assert.Equal(t, "copilot_fake", creds.Token)
		assert.Equal(t, "https://api.individual.githubcopilot.com", creds.BaseURL,
			"a user token needs no base_url because the exchange reports the host")
		assert.Equal(t, "token ghu_learns_host", fake.auth.Load())
		assert.Equal(t, copilotUserAgent, fake.userAgent.Load())
	})

	t.Run("a configured base URL wins over the advertised host", func(t *testing.T) {
		fake := newFakeGithub(t)

		creds, bErr := fake.mint("ghu_override", "https://recorder.local/", noopLogger{})

		require.Nil(t, bErr)
		assert.Equal(t, "https://recorder.local", creds.BaseURL)
	})

	t.Run("a configured base URL is trimmed like the PAT path", func(t *testing.T) {
		fake := newFakeGithub(t)

		creds, bErr := fake.mint("ghu_padded", "  https://recorder.local/ ", noopLogger{})
		require.Nil(t, bErr)
		assert.Equal(t, "https://recorder.local", creds.BaseURL)

		creds, bErr = fake.mint("ghu_blank", "   ", noopLogger{})
		require.Nil(t, bErr)
		assert.Equal(t, "https://api.business.githubcopilot.com", creds.BaseURL)
	})

	t.Run("an advertised host outside githubcopilot.com falls back with a warning", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.endpoint.Store("https://evilgithubcopilot.com")
		logger := &recordingLogger{}

		creds, bErr := fake.mint("ghu_fallback", "", logger)

		require.Nil(t, bErr, "a new legitimate GitHub subdomain is likelier than a hostile one, so do not fail")
		assert.Equal(t, defaultCopilotAPIBaseURL, creds.BaseURL)
		assert.True(t, logger.sawWarning(), "silently ignoring the advertised host would hide a real change")
	})
}

func TestExpiryParsing(t *testing.T) {
	fake := newFakeGithub(t)

	t.Run("refuses an already-expired copilot token", func(t *testing.T) {
		fake.tokenExpirySecs.Store(-60)
		defer fake.tokenExpirySecs.Store(1800)

		_, bErr := fake.mint("ghu_expired", "", noopLogger{})

		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "already-expired")
	})

	t.Run("refuses an implausibly distant expiry", func(t *testing.T) {
		fake.tokenExpirySecs.Store(int64((48 * time.Hour).Seconds()))
		defer fake.tokenExpirySecs.Store(1800)

		_, bErr := fake.mint("ghu_distant", "", noopLogger{})

		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "implausible")
	})
}

func TestCachingAndRefresh(t *testing.T) {
	t.Run("a warm token costs no upstream calls", func(t *testing.T) {
		fake := newFakeGithub(t)

		for range 50 {
			_, bErr := fake.mint("ghu_warm", "", noopLogger{})
			require.Nil(t, bErr)
		}
		assert.Equal(t, int64(1), fake.hits.Load(), "a valid cached token must not be re-minted")
	})

	t.Run("each user token gets its own cache slot", func(t *testing.T) {
		fake := newFakeGithub(t)

		_, bErr := fake.mint("ghu_slot_a", "", noopLogger{})
		require.Nil(t, bErr)
		_, bErr = fake.mint("ghu_slot_b", "", noopLogger{})
		require.Nil(t, bErr)

		assert.Equal(t, int64(2), fake.hits.Load())
	})

	t.Run("concurrent cold starts collapse to one exchange", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.delay.Store(50)

		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() { _, _ = fake.mint("ghu_herd", "", noopLogger{}) })
		}
		wg.Wait()

		assert.Equal(t, int64(1), fake.hits.Load())
	})

	t.Run("a short refresh_in is floored rather than obeyed", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.refreshIn.Store(1)

		_, bErr := fake.mint("ghu_refresh", "", noopLogger{})
		require.Nil(t, bErr)
		time.Sleep(50 * time.Millisecond)
		_, bErr = fake.mint("ghu_refresh", "", noopLogger{})
		require.Nil(t, bErr)

		assert.Equal(t, int64(1), fake.hits.Load(), "a refresh_in under the floor would otherwise spin")
	})
}

func TestExchangeFailures(t *testing.T) {
	t.Run("a revoked token blocks fallbacks and asks for a new token", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.status.Store(http.StatusUnauthorized)

		_, bErr := fake.mint("ghu_revoked", "", noopLogger{})

		require.NotNil(t, bErr)
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		assert.Contains(t, bErr.Error.Message, "revoked or has expired")
	})

	t.Run("a token without Copilot access names the requirements", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.status.Store(http.StatusNotFound)

		_, bErr := fake.mint("gho_no_copilot", "", noopLogger{})

		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "Copilot plan")
	})

	t.Run("a permanent failure is cached instead of retried per request", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.status.Store(http.StatusForbidden)

		for range 100 {
			_, bErr := fake.mint("ghu_backoff", "", noopLogger{})
			require.NotNil(t, bErr)
		}
		// Without the negative cache this is 100 calls to api.github.com.
		assert.LessOrEqual(t, fake.hits.Load(), int64(2))
	})

	t.Run("a rate-limited exchange carries the retry hint", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.status.Store(http.StatusTooManyRequests)

		_, bErr := fake.mint("ghu_rate_limited", "", noopLogger{})

		require.NotNil(t, bErr)
		assert.Equal(t, int64(7000), bErr.ExtraFields.RetryAfter)
	})

	// Backoff growth and the permanent/transient split are pinned in
	// core/providers/utils/tokencache (TestBackoffFor, TestIsPermanentError).
}

// ---------------------------------------------------------------------------
// Fine-grained PATs: sent directly, at the plan host looked up from the seat description.
// ---------------------------------------------------------------------------

// fakeSeat stands in for api.github.com/copilot_internal/user.
type fakeSeat struct {
	server *httptest.Server
	hits   atomic.Int64
	status atomic.Int64
	api    atomic.Value // string, the advertised endpoints.api
	auth   atomic.Value // string, Authorization on the last lookup
}

func newFakeSeat(t *testing.T) *fakeSeat {
	t.Helper()
	f := &fakeSeat{}
	f.status.Store(http.StatusOK)
	f.api.Store("https://api.business.githubcopilot.com")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.auth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		status := int(f.status.Load())
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"login":"octocat","copilot_plan":"business","endpoints":{"api":%q}}`, f.api.Load().(string))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeSeat) resolve(pat, configuredBaseURL string, logger schemas.Logger) (*copilotCredentials, *schemas.BifrostError) {
	return resolvePATWithUserURL(context.Background(), f.server.URL, pat, &fasthttp.Client{}, configuredBaseURL, logger)
}

func TestResolvePATCredentials(t *testing.T) {
	t.Run("sends the PAT itself, under the SDK integration ID, at the plan host", func(t *testing.T) {
		seat := newFakeSeat(t)

		creds, bErr := seat.resolve("github_pat_direct", "", noopLogger{})

		require.Nil(t, bErr)
		assert.Equal(t, &copilotCredentials{Token: "github_pat_direct", BaseURL: "https://api.business.githubcopilot.com", SDK: true}, creds)
		assert.Equal(t, sdkIntegrationID, creds.integrationID())
		assert.Equal(t, "token github_pat_direct", seat.auth.Load())
	})

	t.Run("the host is looked up once and cached", func(t *testing.T) {
		seat := newFakeSeat(t)

		for range 20 {
			_, bErr := seat.resolve("github_pat_cached", "", noopLogger{})
			require.Nil(t, bErr)
		}
		assert.Equal(t, int64(1), seat.hits.Load())
	})

	t.Run("a configured base URL skips the lookup", func(t *testing.T) {
		seat := newFakeSeat(t)

		creds, bErr := seat.resolve("github_pat_configured", "https://recorder.local/", noopLogger{})

		require.Nil(t, bErr)
		assert.Equal(t, "https://recorder.local", creds.BaseURL)
		assert.Zero(t, seat.hits.Load())
	})

	t.Run("an advertised host outside githubcopilot.com falls back with a warning", func(t *testing.T) {
		seat := newFakeSeat(t)
		seat.api.Store("https://evilgithubcopilot.com")
		logger := &recordingLogger{}

		creds, bErr := seat.resolve("github_pat_fallback", "", logger)

		require.Nil(t, bErr)
		assert.Equal(t, defaultCopilotAPIBaseURL, creds.BaseURL)
		assert.True(t, logger.sawWarning())
	})

	t.Run("a revoked PAT blocks fallbacks and asks for a new one", func(t *testing.T) {
		seat := newFakeSeat(t)
		seat.status.Store(http.StatusUnauthorized)

		_, bErr := seat.resolve("github_pat_revoked", "", noopLogger{})

		require.NotNil(t, bErr)
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		assert.Contains(t, bErr.Error.Message, "personal access token (401)")
	})
}

// ---------------------------------------------------------------------------
// Test loggers.
// ---------------------------------------------------------------------------

// noopLogger embeds the interface so it satisfies every method without listing them. Only
// Warn is ever called on this path; anything else would panic loudly.
type noopLogger struct{ schemas.Logger }

func (noopLogger) Warn(string, ...any) {}

type recordingLogger struct {
	noopLogger
	mu       sync.Mutex
	warnings []string
}

func (l *recordingLogger) Warn(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) sawWarning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warnings) > 0
}

// BenchmarkMintWarm is the per-request cost of a cached Copilot token: the cache lookup and
// the credentials struct. The exchange happens once, outside the timed loop.
func BenchmarkMintWarm(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"token":"copilot_fake","expires_at":%d,"refresh_in":0,"endpoints":{"api":"https://api.business.githubcopilot.com"}}`,
			time.Now().Add(30*time.Minute).Unix())
	}))
	defer server.Close()

	// One client for the whole run, as the provider holds one exchangeClient.
	client := &fasthttp.Client{}
	mint := func() {
		if _, bErr := mintWithTokenURL(context.Background(), server.URL, "ghu_bench", client, "", noopLogger{}); bErr != nil {
			b.Fatal(bErr)
		}
	}
	mint()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		mint()
	}
}
