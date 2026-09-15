package githubcopilot

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// One 2048-bit key for the whole package. Generating RSA keys is slow enough that doing it
// per test noticeably drags the suite.
var (
	testKeyOnce sync.Once
	testRSAKey  *rsa.PrivateKey
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testRSAKey = key
	})
	return testRSAKey
}

func pkcs1PEM(t *testing.T) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey(t)),
	}))
}

func pkcs8PEM(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey(t))
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// keyConfig builds a valid config. appID varies per test so each gets its own cache entry
// in the package-level pool.
func keyConfig(t *testing.T, appID string) *schemas.GithubCopilotKeyConfig {
	t.Helper()
	return &schemas.GithubCopilotKeyConfig{
		AppID:          *schemas.NewSecretVar(appID),
		InstallationID: *schemas.NewSecretVar("12345678"),
		RepositoryID:   *schemas.NewSecretVar("987654321"),
		PrivateKey:     *schemas.NewSecretVar(pkcs1PEM(t)),
	}
}

// ---------------------------------------------------------------------------
// Allowlist. This decides where prompts and a bearer token are sent.
// ---------------------------------------------------------------------------

func TestValidateCopilotAPIBaseURL(t *testing.T) {
	dotcom := []string{copilotPublicAPIDomain}
	enterprise := []string{copilotPublicAPIDomain, "ghe.acme.com"}

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
			{"enterprise host not configured", "https://copilot.ghe.acme.com"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := validateCopilotAPIBaseURL(tt.url, dotcom)
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
				got, err := validateCopilotAPIBaseURL(tt.url, dotcom)
				require.NoError(t, err)
				assert.Equal(t, tt.expected, got)
			})
		}
	})

	t.Run("a configured enterprise domain is allowed alongside the public one", func(t *testing.T) {
		got, err := validateCopilotAPIBaseURL("https://copilot.ghe.acme.com", enterprise)
		require.NoError(t, err)
		assert.Equal(t, "https://copilot.ghe.acme.com", got)

		got, err = validateCopilotAPIBaseURL("https://api.githubcopilot.com", enterprise)
		require.NoError(t, err)
		assert.Equal(t, "https://api.githubcopilot.com", got)
	})
}

func TestNormalizeGithubDomain(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"plain domain", "acme.ghe.com", "acme.ghe.com"},
		{"scheme stripped", "https://acme.ghe.com", "acme.ghe.com"},
		{"path stripped", "https://acme.ghe.com/api/v3", "acme.ghe.com"},
		{"port stripped", "acme.ghe.com:443", "acme.ghe.com"},
		{"case lowered", "ACME.GHE.COM", "acme.ghe.com"},
		{"whitespace trimmed", "  acme.ghe.com  ", "acme.ghe.com"},
		{"root dot stripped", "acme.ghe.com.", "acme.ghe.com"},
		// A single-label domain would authorize an entire TLD if used as an allowlist entry.
		{"single label rejected", "com", ""},
		{"IP rejected", "10.0.0.1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeGithubDomain(tt.input))
		})
	}
}

func TestResolveCopilotEndpoints(t *testing.T) {
	t.Run("github.com", func(t *testing.T) {
		e := resolveCopilotEndpoints("")
		assert.Equal(t, defaultGithubAPIBase, e.apiBase)
		assert.Equal(t, "https://api.github.com/copilot_internal/v2/token", e.copilotTokenURL)
		assert.False(t, e.isEnterprise)
		assert.Equal(t, []string{copilotPublicAPIDomain}, e.allowedAPIDomains)
	})

	t.Run("explicit github.com is treated as the default", func(t *testing.T) {
		assert.False(t, resolveCopilotEndpoints("github.com").isEnterprise)
	})

	t.Run("GHE Cloud uses the api subdomain", func(t *testing.T) {
		e := resolveCopilotEndpoints("acme.ghe.com")
		assert.Equal(t, "https://api.acme.ghe.com", e.apiBase)
		assert.True(t, e.isEnterprise)
		assert.Contains(t, e.allowedAPIDomains, "acme.ghe.com")
	})

	t.Run("GHE Cloud excludes the public Copilot host", func(t *testing.T) {
		// A .ghe.com tenant exists for data residency. Accepting api.githubcopilot.com for
		// one would route its prompts out of region, which is the whole thing it is paying
		// to avoid.
		e := resolveCopilotEndpoints("acme.ghe.com")

		assert.NotContains(t, e.allowedAPIDomains, copilotPublicAPIDomain)
		assert.Equal(t, []string{"acme.ghe.com"}, e.allowedAPIDomains)
	})

	t.Run("GHE Server keeps the public host alongside its own", func(t *testing.T) {
		// Self-hosted GHES commonly proxies Copilot through GitHub's public service, so the
		// public host stays allowed there. Only the data-residency tenants exclude it.
		e := resolveCopilotEndpoints("github.acme.internal")

		assert.Contains(t, e.allowedAPIDomains, "github.acme.internal")
		assert.Contains(t, e.allowedAPIDomains, copilotPublicAPIDomain)
	})

	t.Run("GHE Server uses the api/v3 prefix", func(t *testing.T) {
		e := resolveCopilotEndpoints("github.acme.internal")
		assert.Equal(t, "https://github.acme.internal/api/v3", e.apiBase)
		assert.Equal(t, "https://github.acme.internal/api/v3/copilot_internal/v2/token", e.copilotTokenURL)
		assert.True(t, e.isEnterprise)
	})
}

// ---------------------------------------------------------------------------
// Config validation and cache keying.
// ---------------------------------------------------------------------------

func TestValidateKeyConfig(t *testing.T) {
	t.Run("accepts a complete config", func(t *testing.T) {
		cfg, bErr := validateKeyConfig(keyConfig(t, "app-1"))
		require.Nil(t, bErr)
		assert.Equal(t, "app-1", cfg.appID)
		assert.Equal(t, int64(987654321), cfg.repositoryID)
		assert.Equal(t, "https://api.github.com/app/installations/12345678/access_tokens",
			cfg.endpoints.installationTokenURL)
	})

	t.Run("rejects a non-numeric installation id", func(t *testing.T) {
		cfg := keyConfig(t, "app-2")
		// This value is interpolated into an api.github.com path using our own App JWT, so
		// letting it through would be a path injection against GitHub.
		cfg.InstallationID = *schemas.NewSecretVar("1/../../../user")

		_, bErr := validateKeyConfig(cfg)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "installation_id")
	})

	t.Run("rejects an over-long installation id", func(t *testing.T) {
		cfg := keyConfig(t, "app-3")
		cfg.InstallationID = *schemas.NewSecretVar(strings.Repeat("9", 21))

		_, bErr := validateKeyConfig(cfg)
		require.NotNil(t, bErr)
	})

	t.Run("rejects a non-numeric repository id", func(t *testing.T) {
		cfg := keyConfig(t, "app-4")
		// A string here is a silent 422 from GitHub, so catch it locally.
		cfg.RepositoryID = *schemas.NewSecretVar("my-repo")

		_, bErr := validateKeyConfig(cfg)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "repository_id")
	})

	t.Run("reports missing fields deterministically", func(t *testing.T) {
		_, bErr := validateKeyConfig(&schemas.GithubCopilotKeyConfig{})
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "app_id")
	})

	t.Run("nil config", func(t *testing.T) {
		_, bErr := validateKeyConfig(nil)
		require.NotNil(t, bErr)
	})

	t.Run("every rejection blocks fallbacks", func(t *testing.T) {
		// AllowFallbacks is nil-means-allowed, so a config fault that does not set it
		// explicitly sends the prompt to another paid provider instead of surfacing the
		// setup mistake.
		bad := []*schemas.GithubCopilotKeyConfig{
			nil,
			{},
			{AppID: *schemas.NewSecretVar("1"), InstallationID: *schemas.NewSecretVar("nope"),
				RepositoryID: *schemas.NewSecretVar("2"), PrivateKey: *schemas.NewSecretVar("pem")},
			{AppID: *schemas.NewSecretVar("1"), InstallationID: *schemas.NewSecretVar("2"),
				RepositoryID: *schemas.NewSecretVar("nope"), PrivateKey: *schemas.NewSecretVar("pem")},
		}
		for i, cfg := range bad {
			_, bErr := validateKeyConfig(cfg)
			require.NotNil(t, bErr, "case %d should be rejected", i)
			require.NotNil(t, bErr.AllowFallbacks, "case %d must set AllowFallbacks explicitly", i)
			assert.False(t, *bErr.AllowFallbacks, "case %d must not drain onto another provider", i)
		}
	})
}

func TestCacheKeyFraming(t *testing.T) {
	build := func(appID, installationID string) *schemas.GithubCopilotKeyConfig {
		return &schemas.GithubCopilotKeyConfig{
			AppID:          *schemas.NewSecretVar(appID),
			InstallationID: *schemas.NewSecretVar(installationID),
			RepositoryID:   *schemas.NewSecretVar("1"),
			PrivateKey:     *schemas.NewSecretVar("pem"),
		}
	}

	t.Run("separator ambiguity cannot collide two credentials", func(t *testing.T) {
		// With a plain "|" join these two produce the same string, and a collision here
		// serves one tenant's Copilot token on another tenant's request.
		assert.NotEqual(t, copilotCacheKey(build("a|b", "c")), copilotCacheKey(build("a", "b|c")))
	})

	t.Run("changing only the private key changes the key", func(t *testing.T) {
		a := build("app", "1")
		b := build("app", "1")
		b.PrivateKey = *schemas.NewSecretVar("a different pem")

		assert.NotEqual(t, copilotCacheKey(a), copilotCacheKey(b))
	})

	t.Run("changing only the enterprise domain changes the key", func(t *testing.T) {
		a := build("app", "1")
		b := build("app", "1")
		b.GithubDomain = *schemas.NewSecretVar("acme.ghe.com")

		assert.NotEqual(t, copilotCacheKey(a), copilotCacheKey(b))
	})

	t.Run("identical configs agree", func(t *testing.T) {
		assert.Equal(t, copilotCacheKey(build("app", "1")), copilotCacheKey(build("app", "1")))
	})

	t.Run("is a hex digest that leaks no input", func(t *testing.T) {
		key := copilotCacheKey(build("my-secret-app", "1"))
		assert.Len(t, key, 64)
		assert.NotContains(t, key, "my-secret-app")
	})
}

// ---------------------------------------------------------------------------
// Layer 1: the App JWT.
// ---------------------------------------------------------------------------

func TestSignAppJWT(t *testing.T) {
	cfg, bErr := validateKeyConfig(keyConfig(t, "app-jwt"))
	require.Nil(t, bErr)

	t.Run("keeps exp minus iat inside GitHub's 600 second limit", func(t *testing.T) {
		// The natural implementation (iat = now-60s, exp = now+10m) is a 660 second span
		// and a blanket 401. Nothing about the token looks malformed, so only arithmetic
		// catches it.
		for _, offset := range []time.Duration{0, time.Hour, -time.Hour, 72 * time.Hour} {
			now := time.Now().Add(offset)
			token, err := signAppJWT(cfg, now)
			require.NoError(t, err)

			claims := decodeClaims(t, token)
			iat := int64(claims["iat"].(float64))
			exp := int64(claims["exp"].(float64))

			assert.LessOrEqual(t, exp-iat, int64(600),
				"GitHub rejects an App JWT whose exp - iat exceeds 600 seconds")
			assert.LessOrEqual(t, iat, now.Unix(), "iat must not be in the future")
			assert.Equal(t, "app-jwt", claims["iss"])
		}
	})

	t.Run("produces three unpadded base64url segments", func(t *testing.T) {
		token, err := signAppJWT(cfg, time.Now())
		require.NoError(t, err)

		assert.Len(t, strings.Split(token, "."), 3)
		// JWT forbids "=" padding, and a padded segment yields a 401 indistinguishable
		// from a bad key.
		assert.NotContains(t, token, "=")
	})

	t.Run("accepts a PKCS#8 key", func(t *testing.T) {
		cfg := &copilotConfig{appID: "a", privateKeyPEM: pkcs8PEM(t)}
		_, err := signAppJWT(cfg, time.Now())
		assert.NoError(t, err)
	})

	t.Run("accepts a PEM whose newlines survived as literal backslash-n", func(t *testing.T) {
		// Half of first-time GitHub App setups paste the key through an env var this way,
		// and the raw parse failure tells the operator nothing.
		mangled := strings.ReplaceAll(pkcs1PEM(t), "\n", `\n`)
		cfg := &copilotConfig{appID: "a", privateKeyPEM: mangled}

		_, err := signAppJWT(cfg, time.Now())
		assert.NoError(t, err)
	})

	t.Run("accepts CRLF line endings", func(t *testing.T) {
		cfg := &copilotConfig{appID: "a", privateKeyPEM: strings.ReplaceAll(pkcs1PEM(t), "\n", "\r\n")}
		_, err := signAppJWT(cfg, time.Now())
		assert.NoError(t, err)
	})

	t.Run("rejects a non-PEM value with an actionable message", func(t *testing.T) {
		cfg := &copilotConfig{appID: "a", privateKeyPEM: "not a key"}
		_, err := signAppJWT(cfg, time.Now())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "PKCS#1 or PKCS#8")
	})
}

// ---------------------------------------------------------------------------
// The exchange pipeline.
// ---------------------------------------------------------------------------

// fakeGithub stands in for api.github.com and counts hits on each layer.
type fakeGithub struct {
	server             *httptest.Server
	installationHits   atomic.Int64
	copilotHits        atomic.Int64
	installationStatus atomic.Int64
	copilotStatus      atomic.Int64
	copilotEndpoint    atomic.Value // string
	tokenExpirySecs    atomic.Int64
	refreshIn          atomic.Int64
	delay              atomic.Int64 // milliseconds
}

func newFakeGithub(t *testing.T) *fakeGithub {
	t.Helper()
	f := &fakeGithub{}
	f.installationStatus.Store(http.StatusCreated)
	f.copilotStatus.Store(http.StatusOK)
	f.copilotEndpoint.Store("https://api.business.githubcopilot.com")
	f.tokenExpirySecs.Store(1800)

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		f.installationHits.Add(1)
		if d := f.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		status := int(f.installationStatus.Load())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusCreated && status != http.StatusOK {
			_, _ = fmt.Fprint(w, `{"message":"upstream said no"}`)
			return
		}
		// expires_at is RFC3339 on this layer.
		_, _ = fmt.Fprintf(w, `{"token":"ghs_fake","expires_at":%q,"permissions":{"copilot_requests":"write"}}`,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	})
	mux.HandleFunc(copilotTokenPath, func(w http.ResponseWriter, r *http.Request) {
		f.copilotHits.Add(1)
		status := int(f.copilotStatus.Load())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = fmt.Fprint(w, `{"message":"upstream said no"}`)
			return
		}
		// expires_at is a UNIX integer on this layer, not an RFC3339 string.
		_, _ = fmt.Fprintf(w, `{"token":"copilot_fake","expires_at":%d,"refresh_in":%d,"endpoints":{"api":%q}}`,
			time.Now().Add(time.Duration(f.tokenExpirySecs.Load())*time.Second).Unix(),
			f.refreshIn.Load(),
			f.copilotEndpoint.Load().(string))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// configFor points a key config at the fake server. httptest listens on loopback, so these
// tests use a plain client rather than one wrapped by ConfigureDialer.
func (f *fakeGithub) configFor(t *testing.T, appID string) *schemas.GithubCopilotKeyConfig {
	t.Helper()
	cfg := keyConfig(t, appID)
	cfg.GithubDomain = *schemas.NewSecretVar(strings.TrimPrefix(f.server.URL, "http://"))
	return cfg
}

// resolve validates a key config and rewires its endpoints at the fake server. The
// production resolver always builds https URLs, which a local httptest server cannot serve,
// so they are overridden after validation.
//
// This is the half that needs *testing.T, so it must run on the test goroutine.
func (f *fakeGithub) resolve(t *testing.T, appID string) *copilotConfig {
	t.Helper()
	cfg, bErr := validateKeyConfig(f.configFor(t, appID))
	require.Nil(t, bErr)

	cfg.endpoints.installationTokenURL = f.server.URL + "/app/installations/" + cfg.installationID + "/access_tokens"
	cfg.endpoints.copilotTokenURL = f.server.URL + copilotTokenPath
	cfg.endpoints.isEnterprise = false
	cfg.endpoints.allowedAPIDomains = []string{copilotPublicAPIDomain}
	return cfg
}

// mint runs the pipeline against the fake server.
//
// It deliberately takes no *testing.T. The concurrency tests below call it from a hundred
// goroutines, and require/assert reach t.FailNow, which the testing package only supports
// from the goroutine running the test. Keeping t out of this signature makes that mistake
// impossible to reintroduce rather than merely absent: everything needing t happens in
// resolve, on the test goroutine, before the fan-out starts.
func (f *fakeGithub) mint(cfg *copilotConfig, logger schemas.Logger) (*copilotCredentials, *schemas.BifrostError) {
	return mintWithConfig(context.Background(), cfg, &fasthttp.Client{}, "", logger)
}

func TestExpiryParsing(t *testing.T) {
	fake := newFakeGithub(t)

	t.Run("reads RFC3339 on layer 2 and a UNIX integer on layer 3", func(t *testing.T) {
		// Reusing one struct for both, or copying the field type, gives either an unmarshal
		// error or a zero time. A zero time is always expired, so every request would take
		// the slow path and build a request amplifier pointed at api.github.com.
		creds, bErr := fake.mint(fake.resolve(t, "expiry-1"), noopLogger{})

		require.Nil(t, bErr)
		assert.Equal(t, "copilot_fake", creds.Token)
		assert.Equal(t, "https://api.business.githubcopilot.com", creds.BaseURL)
	})

	t.Run("refuses an already-expired copilot token", func(t *testing.T) {
		fake.tokenExpirySecs.Store(-60)
		defer fake.tokenExpirySecs.Store(1800)

		_, bErr := fake.mint(fake.resolve(t, "expiry-2"), noopLogger{})

		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "already-expired")
	})

	t.Run("refuses an implausibly distant expiry", func(t *testing.T) {
		fake.tokenExpirySecs.Store(int64((48 * time.Hour).Seconds()))
		defer fake.tokenExpirySecs.Store(1800)

		_, bErr := fake.mint(fake.resolve(t, "expiry-3"), noopLogger{})

		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "implausible")
	})
}

func TestCachingAndRefresh(t *testing.T) {
	t.Run("a warm token costs no upstream calls", func(t *testing.T) {
		fake := newFakeGithub(t)
		cfg := fake.resolve(t, "warm-1")

		_, bErr := fake.mint(cfg, noopLogger{})
		require.Nil(t, bErr)
		require.Equal(t, int64(1), fake.copilotHits.Load())

		for i := 0; i < 50; i++ {
			_, bErr := fake.mint(cfg, noopLogger{})
			require.Nil(t, bErr)
		}
		assert.Equal(t, int64(1), fake.copilotHits.Load(), "a valid cached token must not be re-minted")
		assert.Equal(t, int64(1), fake.installationHits.Load())
	})

	t.Run("concurrent cold starts collapse to one exchange", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.delay.Store(50)
		cfg := fake.resolve(t, "herd-1")

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = fake.mint(cfg, noopLogger{})
			}()
		}
		wg.Wait()

		// Without the entry mutex plus the double-checked read this is 100, and GitHub
		// abuse-flags the App.
		assert.Equal(t, int64(1), fake.installationHits.Load())
		assert.Equal(t, int64(1), fake.copilotHits.Load())
	})

	t.Run("eviction refreshes exactly once, not once per goroutine", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.delay.Store(50)
		keyCfg := fake.configFor(t, "evict-1")
		cfg := fake.resolve(t, "evict-1")

		_, bErr := fake.mint(cfg, noopLogger{})
		require.Nil(t, bErr)

		invalidateCredentials(keyCfg, scopeAll)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = fake.mint(cfg, noopLogger{})
			}()
		}
		wg.Wait()

		// One refresh for the whole fleet, not one per goroutine.
		assert.Equal(t, int64(2), fake.installationHits.Load())
		assert.Equal(t, int64(2), fake.copilotHits.Load())
	})

	t.Run("scopeCopilotOnly keeps the installation token", func(t *testing.T) {
		fake := newFakeGithub(t)
		keyCfg := fake.configFor(t, "evict-2")
		cfg := fake.resolve(t, "evict-2")

		_, bErr := fake.mint(cfg, noopLogger{})
		require.Nil(t, bErr)

		invalidateCredentials(keyCfg, scopeCopilotOnly)
		_, bErr = fake.mint(cfg, noopLogger{})
		require.Nil(t, bErr)

		// This is also what catches eviction being changed to delete the pool entry:
		// a deleted entry loses the still-valid installation token along with it.
		assert.Equal(t, int64(1), fake.installationHits.Load(),
			"the installation token is still valid, so re-minting it wastes a GitHub call")
		assert.Equal(t, int64(2), fake.copilotHits.Load())
	})

	t.Run("a short refresh_in is floored rather than obeyed", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.refreshIn.Store(1)
		cfg := fake.resolve(t, "refresh-1")

		_, bErr := fake.mint(cfg, noopLogger{})
		require.Nil(t, bErr)

		time.Sleep(50 * time.Millisecond)
		_, bErr = fake.mint(cfg, noopLogger{})
		require.Nil(t, bErr)

		assert.Equal(t, int64(1), fake.copilotHits.Load(),
			"a refresh_in under the floor would otherwise spin")
	})
}

func TestFailureBackoff(t *testing.T) {
	t.Run("a permanent failure is cached instead of retried per request", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.copilotStatus.Store(http.StatusForbidden)
		cfg := fake.resolve(t, "backoff-1")

		for i := 0; i < 100; i++ {
			_, bErr := fake.mint(cfg, noopLogger{})
			require.NotNil(t, bErr)
			assert.Contains(t, bErr.Error.Message, "github copilot:")
		}

		// Without the negative cache this is 100 pairs of calls to api.github.com, and the
		// rate-limit penalty lands on the App rather than on the key.
		assert.LessOrEqual(t, fake.copilotHits.Load(), int64(2))
	})

	t.Run("a blocked failure does not drain onto fallbacks", func(t *testing.T) {
		fake := newFakeGithub(t)
		fake.installationStatus.Store(http.StatusUnauthorized)

		_, bErr := fake.mint(fake.resolve(t, "backoff-2"), noopLogger{})

		require.NotNil(t, bErr)
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
	})

	t.Run("backoff grows and clamps", func(t *testing.T) {
		assert.Equal(t, permanentBackoffBase, backoffFor(1, true))
		assert.Equal(t, 2*permanentBackoffBase, backoffFor(2, true))
		assert.Equal(t, permanentBackoffCap, backoffFor(100, true))
		assert.Equal(t, transientBackoffBase, backoffFor(1, false))
		assert.Equal(t, transientBackoffCap, backoffFor(100, false))
	})

	t.Run("rate limits and server errors count as transient", func(t *testing.T) {
		assert.False(t, isPermanentError(blockingError("x", http.StatusTooManyRequests)))
		assert.False(t, isPermanentError(blockingError("x", http.StatusBadGateway)))
		assert.True(t, isPermanentError(blockingError("x", http.StatusUnauthorized)))
		assert.True(t, isPermanentError(blockingError("x", http.StatusForbidden)))
	})
}

func TestEnterpriseNeverFallsBackToPublic(t *testing.T) {
	fake := newFakeGithub(t)
	// GitHub advertises a host outside the enterprise allowlist.
	fake.copilotEndpoint.Store("https://api.githubcopilot.com")

	keyCfg := fake.configFor(t, "ghe-1")
	cfg, bErr := validateKeyConfig(keyCfg)
	require.Nil(t, bErr)
	cfg.endpoints.installationTokenURL = fake.server.URL + "/app/installations/" + cfg.installationID + "/access_tokens"
	cfg.endpoints.copilotTokenURL = fake.server.URL + copilotTokenPath
	// Take the allowlist production actually builds for a data-residency tenant rather than
	// hand-setting one. Hand-setting it here previously hid that resolveCopilotEndpoints was
	// including the public host for every enterprise config, so this test passed against a
	// configuration that could not occur.
	realEndpoints := resolveCopilotEndpoints("acme.ghe.com")
	cfg.endpoints.isEnterprise = realEndpoints.isEnterprise
	cfg.endpoints.allowedAPIDomains = realEndpoints.allowedAPIDomains
	require.True(t, cfg.endpoints.isEnterprise)

	creds, bErr := mintWithConfig(context.Background(), cfg, &fasthttp.Client{}, "", noopLogger{})

	// Falling back would ship the customer's prompts to api.githubcopilot.com, which is the
	// exact thing running GitHub Enterprise is meant to prevent.
	require.Nil(t, creds)
	require.NotNil(t, bErr)
	assert.Contains(t, bErr.Error.Message, "refusing to use the Copilot host")
}

func TestGHEServerMayUseThePublicCopilotHost(t *testing.T) {
	fake := newFakeGithub(t)
	fake.copilotEndpoint.Store("https://api.githubcopilot.com")

	cfg, bErr := validateKeyConfig(fake.configFor(t, "ghes-1"))
	require.Nil(t, bErr)
	cfg.endpoints.installationTokenURL = fake.server.URL + "/app/installations/" + cfg.installationID + "/access_tokens"
	cfg.endpoints.copilotTokenURL = fake.server.URL + copilotTokenPath

	// Self-hosted GHES is a different case from a .ghe.com data-residency tenant: it
	// commonly proxies Copilot through GitHub's public service, so the public host is a
	// legitimate answer and must not be refused.
	realEndpoints := resolveCopilotEndpoints("github.acme.internal")
	cfg.endpoints.isEnterprise = realEndpoints.isEnterprise
	cfg.endpoints.allowedAPIDomains = realEndpoints.allowedAPIDomains

	creds, bErr := mintWithConfig(context.Background(), cfg, &fasthttp.Client{}, "", noopLogger{})

	require.Nil(t, bErr)
	assert.Equal(t, "https://api.githubcopilot.com", creds.BaseURL)
}

func TestDotcomFallsBackWithAWarning(t *testing.T) {
	fake := newFakeGithub(t)
	fake.copilotEndpoint.Store("https://evilgithubcopilot.com")

	logger := &recordingLogger{}
	creds, bErr := fake.mint(fake.resolve(t, "fallback-1"), logger)

	require.Nil(t, bErr, "a new legitimate GitHub subdomain is likelier than a hostile one, so do not fail")
	assert.Equal(t, defaultCopilotAPIBaseURL, creds.BaseURL)
	assert.True(t, logger.sawWarning(), "silently ignoring the advertised host would hide a real change")
}

func TestConfiguredBaseURLOverridesTheAdvertisedHost(t *testing.T) {
	fake := newFakeGithub(t)
	keyCfg := fake.configFor(t, "override-1")

	cfg, bErr := validateKeyConfig(keyCfg)
	require.Nil(t, bErr)
	cfg.endpoints.installationTokenURL = fake.server.URL + "/app/installations/" + cfg.installationID + "/access_tokens"
	cfg.endpoints.copilotTokenURL = fake.server.URL + copilotTokenPath
	cfg.endpoints.isEnterprise = false
	cfg.endpoints.allowedAPIDomains = []string{copilotPublicAPIDomain}

	creds, bErr := mintWithConfig(context.Background(), cfg, &fasthttp.Client{}, "https://recorder.local/", noopLogger{})

	require.Nil(t, bErr)
	assert.Equal(t, "https://recorder.local", creds.BaseURL)
}

func TestMissingCopilotPermissionWarns(t *testing.T) {
	logger := &recordingLogger{}
	warnOnMissingCopilotPermission(&installationToken{permissions: map[string]string{}}, logger)

	assert.True(t, logger.sawWarning())
}

// ---------------------------------------------------------------------------
// Test loggers.
// ---------------------------------------------------------------------------

// noopLogger embeds the interface so it satisfies every method without listing them.
// Only Warn is ever called on this path; anything else would panic loudly, which is the
// behaviour we want if that changes silently.
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

// decodeClaims pulls the claim set out of a signed JWT without verifying it.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var claims map[string]any
	require.NoError(t, json.Unmarshal(raw, &claims))
	return claims
}
