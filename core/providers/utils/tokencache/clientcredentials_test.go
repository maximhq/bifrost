package tokencache

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                   {}
func (noopLogger) Info(string, ...any)                    {}
func (noopLogger) Warn(string, ...any)                    {}
func (noopLogger) Error(string, ...any)                   {}
func (noopLogger) Fatal(string, ...any)                   {}
func (noopLogger) SetLevel(schemas.LogLevel)              {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// tokenServer records the last token request and answers with a configurable body/status.
type tokenServer struct {
	*httptest.Server
	mu     sync.Mutex
	hits   int
	form   url.Values
	user   string
	pass   string
	auth   string
	status int
	body   string
	header http.Header
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{status: http.StatusOK, body: `{"access_token":"minted","token_type":"Bearer","expires_in":3600}`}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		user, pass, _ := r.BasicAuth()
		ts.mu.Lock()
		ts.hits++
		ts.form, ts.user, ts.pass, ts.auth = form, user, pass, r.Header.Get("Authorization")
		status, body, header := ts.status, ts.body, ts.header
		ts.mu.Unlock()
		for k, vs := range header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) set(status int, body string, header http.Header) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.status, ts.body, ts.header = status, body, header
}

func (ts *tokenServer) snapshot() (int, url.Values, string, string, string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.hits, ts.form, ts.user, ts.pass, ts.auth
}

func authClient(t *testing.T) *http.Client {
	t.Helper()
	return providerUtils.NewProviderHTTPClient(nil, schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true}, noopLogger{})
}

func TestClientCredentialsMinter(t *testing.T) {
	t.Run("sends the grant with basic auth, scopes, audience and extras", func(t *testing.T) {
		ts := newTokenServer(t)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{
			TokenURL:     ts.URL,
			ClientID:     "id",
			ClientSecret: "secret",
			Scopes:       []string{"read", "write"},
			Audience:     "api://x",
			ExtraParams:  url.Values{"resource": {"r1"}},
		}, authClient(t))

		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.Equal(t, "minted", e.Value)
		assert.WithinDuration(t, time.Now().Add(time.Hour), e.ExpiresAt, 5*time.Second)
		assert.WithinDuration(t, time.Now().Add(time.Hour-DefaultRefreshMargin), e.RefreshAt, 5*time.Second)

		_, form, user, pass, _ := ts.snapshot()
		assert.Equal(t, "client_credentials", form.Get("grant_type"))
		assert.Equal(t, "read write", form.Get("scope"))
		assert.Equal(t, "api://x", form.Get("audience"))
		assert.Equal(t, "r1", form.Get("resource"))
		assert.Equal(t, "id", user)
		assert.Equal(t, "secret", pass)
		assert.Empty(t, form.Get("client_id"), "header style must not also put the id in the body")
	})

	t.Run("body auth style puts the credentials in the form", func(t *testing.T) {
		ts := newTokenServer(t)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{
			TokenURL: ts.URL, ClientID: "id", ClientSecret: "secret", AuthStyle: oauth2.AuthStyleInParams,
		}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		_, form, user, _, _ := ts.snapshot()
		assert.Equal(t, "id", form.Get("client_id"))
		assert.Equal(t, "secret", form.Get("client_secret"))
		assert.Empty(t, user)
	})

	t.Run("a missing expires_in falls back to a short lifetime, refreshed a minute early", func(t *testing.T) {
		// The docs promise a re-mint roughly every four minutes for such a token: a five
		// minute assumed lifetime minus the one minute refresh margin.
		ts := newTokenServer(t)
		ts.set(http.StatusOK, `{"access_token":"minted","token_type":"Bearer"}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "s"}, authClient(t))
		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.WithinDuration(t, time.Now().Add(fallbackTokenLifetime), e.ExpiresAt, 5*time.Second)
		assert.WithinDuration(t, time.Now().Add(fallbackTokenLifetime-DefaultRefreshMargin), e.RefreshAt, 5*time.Second)
		assert.Equal(t, 4*time.Minute, fallbackTokenLifetime-DefaultRefreshMargin)
	})

	t.Run("an endpoint rejection carries its status, blocks fallbacks and hides the body", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.set(http.StatusUnauthorized, `{"error":"invalid_client","error_description":"bad secret","echo":"secret"}`, nil)
		// The secret differs from every word of the description, so the description passes through
		// unmasked; a secret the endpoint quotes back is covered by the echo case below.
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL + "/token?x=1", ClientID: "id", ClientSecret: "s3cret-value"}, authClient(t))
		e, bErr := mint(context.Background(), nil)
		assert.Nil(t, e)
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, http.StatusUnauthorized, *bErr.StatusCode)
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		assert.Contains(t, bErr.Error.Message, "invalid_client bad secret")
		assert.NotContains(t, bErr.Error.Message, "echo")
		assert.NotContains(t, bErr.Error.Message, "x=1", "the query string is stripped from the reported URL")
		assert.True(t, IsPermanentError(bErr))
	})

	t.Run("a rate limit carries the retry hint, counts as transient and allows fallbacks", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.set(http.StatusTooManyRequests, `{"error":"slow_down"}`, http.Header{"Retry-After": {"7"}})
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "s"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Equal(t, int64(7000), bErr.ExtraFields.RetryAfter)
		assert.False(t, IsPermanentError(bErr))
		assert.Nil(t, bErr.AllowFallbacks, "a temporary token-endpoint outage must not stop a healthy fallback provider")
	})

	t.Run("an endpoint that echoes the submitted credentials never leaks them to the caller", func(t *testing.T) {
		// error_description is forwarded because it is how an operator learns what the IdP
		// disliked; some IdPs quote the offending request in it, credentials included.
		ts := newTokenServer(t)
		ts.set(http.StatusUnauthorized, `{"error":"invalid_client","error_description":"client cc-client-id with secret cc-s3cret-value and assertion cc-assertion-jwt rejected"}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{
			TokenURL: ts.URL, ClientID: "cc-client-id", ClientSecret: "cc-s3cret-value",
			ExtraParams: url.Values{"client_assertion": {"cc-assertion-jwt"}},
		}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		msg := bErr.Error.Message
		assert.NotContains(t, msg, "cc-s3cret-value", "the client secret reached the caller")
		assert.NotContains(t, msg, "cc-assertion-jwt", "a credential sent as an extra parameter reached the caller")
		assert.NotContains(t, msg, "cc-client-id")
		assert.Contains(t, msg, "invalid_client", "the error code is still reported")
		assert.Contains(t, msg, "rejected", "the rest of the description is still reported")
	})

	t.Run("a secret too short to mask is never echoed either", func(t *testing.T) {
		// Masking a 1-3 byte value would garble ordinary words, so when one is quoted back as a
		// word the endpoint's detail is withheld rather than forwarded with the secret in it.
		ts := newTokenServer(t)
		ts.set(http.StatusUnauthorized, `{"error":"invalid_client","error_description":"secret abc does not match"}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "abc"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.NotRegexp(t, `\babc\b`, bErr.Error.Message, "a short client secret reached the caller")
		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, http.StatusUnauthorized, *bErr.StatusCode, "the status still decides permanence")
	})

	t.Run("a short value inside a longer word does not withhold the detail", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.set(http.StatusUnauthorized, `{"error":"invalid_client","error_description":"bad secret"}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "s3cret-value"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "invalid_client bad secret", "the client ID \"id\" inside \"invalid_client\" is not an echo")
	})

	t.Run("a credential echoed in the error code is masked too", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.set(http.StatusBadRequest, `{"error":"invalid_client:cc-s3cret-value","error_description":"rejected"}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "cc-client-id", ClientSecret: "cc-s3cret-value"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.NotContains(t, bErr.Error.Message, "cc-s3cret-value", "the client secret reached the caller through the error code")
		assert.Contains(t, bErr.Error.Message, "invalid_client")
	})

	t.Run("a server error at the token endpoint allows fallbacks", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.set(http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "s"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Equal(t, http.StatusServiceUnavailable, *bErr.StatusCode)
		assert.False(t, IsPermanentError(bErr))
		assert.Nil(t, bErr.AllowFallbacks)
	})

	t.Run("an unreachable endpoint never leaks the token url query into the wrapped error", func(t *testing.T) {
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: "http://127.0.0.1:1/token?client_secret=leaked-value", ClientID: "id", ClientSecret: "s"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.Error)
		assert.NotContains(t, bErr.Error.Message, "leaked-value")
		require.NotNil(t, bErr.Error.Error, "the network error is still attached for diagnostics")
		assert.NotContains(t, bErr.Error.Error.Error(), "leaked-value", "ErrorField.MarshalJSON serialises this string to callers")
		encoded, err := json.Marshal(bErr.Error)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "leaked-value")
	})

	t.Run("an unreachable endpoint is transient and allows fallbacks", func(t *testing.T) {
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: "http://127.0.0.1:1/token", ClientID: "id", ClientSecret: "s"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, 0, *bErr.StatusCode)
		assert.Nil(t, bErr.AllowFallbacks)
		assert.False(t, IsPermanentError(bErr))
	})

	t.Run("a nil client gets a bounded fallback client", func(t *testing.T) {
		ts := newTokenServer(t)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "s"}, nil)
		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.Equal(t, "minted", e.Value)
	})

	t.Run("an empty access_token is rejected and blocks fallbacks", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.set(http.StatusOK, `{"access_token":"","token_type":"Bearer","expires_in":60}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "s"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "returned no access_token")
		// The endpoint answered 200, but the request failed: the client must not see a 200.
		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, http.StatusBadGateway, *bErr.StatusCode)
		assert.Contains(t, bErr.Error.Message, "(200)", "the endpoint's own status stays in the message")
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		assert.False(t, IsPermanentError(bErr), "a 5xx-classified failure takes the transient backoff")
	})

	t.Run("a zero AuthStyle is header, never auto-detect", func(t *testing.T) {
		// Auto-detect would retry a 401 with the secret in the body: two hits, two styles.
		ts := newTokenServer(t)
		ts.set(http.StatusUnauthorized, `{"error":"invalid_client"}`, nil)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: ts.URL, ClientID: "id", ClientSecret: "s"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		hits, form, user, _, _ := ts.snapshot()
		assert.Equal(t, 1, hits)
		assert.Equal(t, "id", user)
		assert.Empty(t, form.Get("client_secret"))
	})

	t.Run("a redirect from https to plain http never carries the secret", func(t *testing.T) {
		// The token endpoint answers 307 to a cleartext URL. Go replays the POST body on 307/308,
		// so without a redirect policy the client secret would leave in plain text.
		plain := newTokenServer(t)
		tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/token", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(tls.Close)

		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: tls.URL + "/token", ClientID: "id", ClientSecret: "s", AuthStyle: oauth2.AuthStyleInParams}, tls.Client())
		_, bErr := mint(context.Background(), nil)
		hits, form, _, _, _ := plain.snapshot()
		assert.Equal(t, 0, hits, "the cleartext endpoint must never be reached; it saw form %v", form)
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.Error.Error)
		assert.Contains(t, bErr.Error.Error.Error(), "redirect from https to http refused")
		assert.False(t, IsPermanentError(bErr), "a bad redirect is an endpoint fault, not a credential fault")
	})

	t.Run("a redirect to another https origin never carries the secret", func(t *testing.T) {
		// With body auth the client secret is in the POST form, which Go replays on a 307/308.
		// Both servers are https and share httptest's certificate, so only an origin check stops it.
		var elsewhereHits atomic.Int64
		elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			elsewhereHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"stolen","token_type":"Bearer","expires_in":3600}`))
		}))
		t.Cleanup(elsewhere.Close)
		idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, elsewhere.URL+"/token", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(idp.Close)

		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: idp.URL + "/token", ClientID: "id", ClientSecret: "s3cret-value", AuthStyle: oauth2.AuthStyleInParams}, idp.Client())
		e, bErr := mint(context.Background(), nil)
		assert.Nil(t, e)
		assert.Equal(t, int64(0), elsewhereHits.Load(), "the other origin received the token request")
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.Error.Error)
		assert.Contains(t, bErr.Error.Error.Error(), "redirect to another origin refused")
	})

	t.Run("a redirect within the same origin is followed", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				http.Redirect(w, r, srv.URL+"/v2/token", http.StatusTemporaryRedirect)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"minted","token_type":"Bearer","expires_in":3600}`))
		}))
		t.Cleanup(srv.Close)
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: srv.URL + "/token", ClientID: "id", ClientSecret: "s3cret-value", AuthStyle: oauth2.AuthStyleInParams}, srv.Client())
		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.Equal(t, "minted", e.Value)
	})

	t.Run("the exchange goes through the provider proxy", func(t *testing.T) {
		var mu sync.Mutex
		var connects []string
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				mu.Lock()
				connects = append(connects, r.Host)
				mu.Unlock()
			}
			http.Error(w, "tunnel refused by test proxy", http.StatusBadGateway)
		}))
		defer proxy.Close()

		hc := providerUtils.NewProviderHTTPClient(
			&schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(proxy.URL)},
			schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10}, noopLogger{})
		mint := ClientCredentialsMinter(ClientCredentialsConfig{TokenURL: "https://idp.example.test/oauth/token", ClientID: "id", ClientSecret: "s"}, hc)
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"idp.example.test:443"}, connects)
	})
}

func TestEntryFor(t *testing.T) {
	t.Run("a token already inside its margin is trusted for half its remaining life", func(t *testing.T) {
		// The docs promise a 30-second token is re-minted after about 15 seconds.
		expiry := time.Now().Add(30 * time.Second)
		e := entryFor("t", expiry, time.Minute)
		assert.WithinDuration(t, time.Now().Add(15*time.Second), e.RefreshAt, 2*time.Second)
		assert.True(t, e.RefreshAt.Before(expiry))
	})
}
