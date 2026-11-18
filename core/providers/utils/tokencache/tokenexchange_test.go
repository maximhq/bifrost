package tokencache

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exchangeServer records token exchange requests in either encoding.
type exchangeServer struct {
	*httptest.Server
	mu          sync.Mutex
	hits        int
	contentType string
	params      map[string]string
	status      int
	body        string
}

func newExchangeServer(t *testing.T) *exchangeServer {
	t.Helper()
	es := &exchangeServer{status: http.StatusOK, body: `{"access_token":"exchanged","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`}
	es.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		params := map[string]string{}
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") {
			_ = json.Unmarshal(raw, &params)
		} else {
			form, _ := url.ParseQuery(string(raw))
			for k := range form {
				params[k] = form.Get(k)
			}
		}
		es.mu.Lock()
		es.hits++
		es.contentType, es.params = ct, params
		status, body := es.status, es.body
		es.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(es.Close)
	// The exchange endpoint must be on the operator allowlist; tests allow their own server.
	t.Setenv(schemas.TokenExchangeURLsEnv, es.URL)
	return es
}

func (es *exchangeServer) set(status int, body string) {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.status, es.body = status, body
}

func (es *exchangeServer) snapshot() (int, string, map[string]string) {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.hits, es.contentType, es.params
}

// subjectTokenFile writes token to a file inside an allowed credential directory.
func subjectTokenFile(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(CredentialDirsEnv, dir)
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte(token+"\n"), 0o600))
	return path
}

func teConfig(tokenURL, file string) *schemas.OAuthKeyConfig {
	return &schemas.OAuthKeyConfig{
		GrantType:        schemas.OAuthGrantTokenExchange,
		TokenURL:         *schemas.NewSecretVar(tokenURL),
		SubjectTokenFile: file,
		RequestEncoding:  schemas.OAuthRequestEncodingJSON,
		ExtraParams:      map[string]string{"identity_provider_id": "idp_123", "service_account_id": "sa_456"},
	}
}

func TestTokenExchangeMinter(t *testing.T) {
	t.Run("posts the RFC 8693 request as JSON with the subject token from the file", func(t *testing.T) {
		es := newExchangeServer(t)
		file := subjectTokenFile(t, "subject.jwt.v1")

		_, mint, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.Nil(t, bErr)
		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.Equal(t, "exchanged", e.Value)
		assert.WithinDuration(t, time.Now().Add(time.Hour), e.ExpiresAt, 5*time.Second)
		assert.WithinDuration(t, time.Now().Add(time.Hour-DefaultRefreshMargin), e.RefreshAt, 5*time.Second)

		hits, ct, params := es.snapshot()
		assert.Equal(t, 1, hits)
		assert.Equal(t, "application/json", ct)
		assert.Equal(t, map[string]string{
			"grant_type":           TokenExchangeGrantType,
			"subject_token":        "subject.jwt.v1",
			"subject_token_type":   schemas.OAuthSubjectTokenTypeJWT,
			"identity_provider_id": "idp_123",
			"service_account_id":   "sa_456",
		}, params)
	})

	t.Run("form encoding is the default and carries scope and audience", func(t *testing.T) {
		es := newExchangeServer(t)
		cfg := teConfig(es.URL, subjectTokenFile(t, "subject"))
		cfg.RequestEncoding = ""
		cfg.SubjectTokenType = schemas.OAuthSubjectTokenTypeIDToken
		cfg.Scopes = []string{"a", "b"}
		cfg.Audience = "https://api.example"
		_, mint, bErr := MinterForConfig(cfg, authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.Nil(t, bErr)

		_, ct, params := es.snapshot()
		assert.Equal(t, "application/x-www-form-urlencoded", ct)
		assert.Equal(t, schemas.OAuthSubjectTokenTypeIDToken, params["subject_token_type"])
		assert.Equal(t, "a b", params["scope"])
		assert.Equal(t, "https://api.example", params["audience"])
	})

	t.Run("re-reads the subject token file on every exchange", func(t *testing.T) {
		es := newExchangeServer(t)
		file := subjectTokenFile(t, "first")
		_, mint, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.Nil(t, bErr)
		require.NoError(t, os.WriteFile(file, []byte("second"), 0o600))
		_, bErr = mint(context.Background(), nil)
		require.Nil(t, bErr)
		_, _, params := es.snapshot()
		assert.Equal(t, "second", params["subject_token"])
	})

	t.Run("a missing or empty subject token file is a configuration fault and sends nothing", func(t *testing.T) {
		es := newExchangeServer(t)
		file := subjectTokenFile(t, "x")
		require.NoError(t, os.Remove(file))
		_, mint, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.False(t, *bErr.AllowFallbacks)

		require.NoError(t, os.WriteFile(file, []byte("  \n"), 0o600))
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "is empty")
		hits, _, _ := es.snapshot()
		assert.Zero(t, hits)
	})

	t.Run("a subject token file outside the allowed directories is refused", func(t *testing.T) {
		es := newExchangeServer(t)
		file := subjectTokenFile(t, "x")
		t.Setenv(CredentialDirsEnv, t.TempDir()) // a different directory
		_, mint, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "outside the allowed credential directories")
		hits, _, _ := es.snapshot()
		assert.Zero(t, hits)
	})

	t.Run("a symlink that leads out of the allowed directory is refused", func(t *testing.T) {
		es := newExchangeServer(t)
		outside := filepath.Join(t.TempDir(), "secret")
		require.NoError(t, os.WriteFile(outside, []byte("host-secret"), 0o600))
		file := subjectTokenFile(t, "x")
		require.NoError(t, os.Remove(file))
		require.NoError(t, os.Symlink(outside, file))
		_, mint, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "outside the allowed credential directories")
		hits, _, _ := es.snapshot()
		assert.Zero(t, hits)
	})

	t.Run("a token URL that is not on the operator allowlist is refused before the file is read", func(t *testing.T) {
		es := newExchangeServer(t)
		file := subjectTokenFile(t, "x")
		t.Setenv(schemas.TokenExchangeURLsEnv, "https://auth.example/oauth/token")
		_, _, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, schemas.TokenExchangeURLsEnv)
		assert.False(t, *bErr.AllowFallbacks)
		hits, _, _ := es.snapshot()
		assert.Zero(t, hits)
	})

	t.Run("the default allowlist is the OpenAI token endpoint", func(t *testing.T) {
		t.Setenv(schemas.TokenExchangeURLsEnv, "")
		assert.True(t, schemas.TokenExchangeURLAllowed("https://auth.openai.com/oauth/token"))
		assert.False(t, schemas.TokenExchangeURLAllowed("https://auth.openai.com/oauth/token/"))
		assert.False(t, schemas.TokenExchangeURLAllowed("https://attacker.example/token"))
	})

	t.Run("an OpenAI-shaped error is reported with its code and message, scrubbed of the subject token", func(t *testing.T) {
		es := newExchangeServer(t)
		file := subjectTokenFile(t, "subject-token-value")
		es.set(http.StatusBadRequest, `{"error":{"code":"invalid_grant","message":"mapping does not match subject-token-value"}}`)
		_, mint, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Equal(t, http.StatusBadRequest, *bErr.StatusCode)
		assert.Contains(t, bErr.Error.Message, "invalid_grant mapping does not match [redacted]")
		assert.NotContains(t, bErr.Error.Message, "subject-token-value")
		assert.False(t, *bErr.AllowFallbacks)
	})

	t.Run("an RFC 6749 error and a server error keep their classification", func(t *testing.T) {
		es := newExchangeServer(t)
		file := subjectTokenFile(t, "s")
		es.set(http.StatusUnauthorized, `{"error":"invalid_client","error_description":"nope"}`)
		_, mint, bErr := MinterForConfig(teConfig(es.URL, file), authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "invalid_client nope")
		assert.True(t, IsPermanentError(bErr))

		es.set(http.StatusServiceUnavailable, `{}`)
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.False(t, IsPermanentError(bErr))
	})

	t.Run("a 200 without an access token is a bad gateway", func(t *testing.T) {
		es := newExchangeServer(t)
		es.set(http.StatusOK, `{"token_type":"Bearer"}`)
		_, mint, bErr := MinterForConfig(teConfig(es.URL, subjectTokenFile(t, "s")), authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Equal(t, http.StatusBadGateway, *bErr.StatusCode)
	})
}

// Through the cache: one exchange per token lifetime, a refresh before expiry, and the static
// value still winning over the config.
func TestResolveBearerTokenExchange(t *testing.T) {
	es := newExchangeServer(t)
	cfg := teConfig(es.URL, subjectTokenFile(t, "s"))
	cache := New[string](Options{})
	hc := authClient(t)

	for range 3 {
		h, bErr := ResolveBearer(context.Background(), cache, "", cfg, hc)
		require.Nil(t, bErr)
		assert.Equal(t, "Bearer exchanged", h["Authorization"])
	}
	hits, _, _ := es.snapshot()
	assert.Equal(t, 1, hits, "a cached token is reused")

	// A token inside its refresh margin is re-minted on the next request.
	es.set(http.StatusOK, `{"access_token":"short","token_type":"Bearer","expires_in":30}`)
	cache.Expire(oauthKeyCacheKey(cfg, es.URL))
	h, bErr := ResolveBearer(context.Background(), cache, "", cfg, hc)
	require.Nil(t, bErr)
	assert.Equal(t, "Bearer short", h["Authorization"])
	e := cache.Peek(oauthKeyCacheKey(cfg, es.URL))
	require.NotNil(t, e)
	assert.True(t, e.RefreshAt.Before(e.ExpiresAt))

	h, bErr = ResolveBearer(context.Background(), cache, "static-key", cfg, hc)
	require.Nil(t, bErr)
	assert.Equal(t, "Bearer static-key", h["Authorization"], "a static value wins over the config")
}

func TestOAuthKeyCacheKeyTokenExchange(t *testing.T) {
	base := teConfig("https://auth.openai.com/oauth/token", "/var/run/secrets/openai/token")
	key := func(c *schemas.OAuthKeyConfig) string {
		return oauthKeyCacheKey(c, "https://auth.openai.com/oauth/token")
	}
	k0 := key(base)

	other := *base
	other.SubjectTokenFile = "/var/run/secrets/other/token"
	assert.NotEqual(t, k0, key(&other))

	other = *base
	other.SubjectTokenType = schemas.OAuthSubjectTokenTypeIDToken
	assert.NotEqual(t, k0, key(&other))

	other = *base
	other.RequestEncoding = schemas.OAuthRequestEncodingForm
	assert.NotEqual(t, k0, key(&other))

	other = *base
	other.ExtraParams = map[string]string{"identity_provider_id": "idp_123", "service_account_id": "sa_other"}
	assert.NotEqual(t, k0, key(&other))

	other = *base
	other.SubjectTokenFile = "  /var/run/secrets/openai/token "
	assert.Equal(t, k0, key(&other), "whitespace does not split the slot")
}

func TestTokenExchangeResponseHandling(t *testing.T) {
	mintOnce := func(t *testing.T, status int, body string) (*Entry[string], *schemas.BifrostError) {
		t.Helper()
		es := newExchangeServer(t)
		es.set(status, body)
		_, mint, bErr := MinterForConfig(teConfig(es.URL, subjectTokenFile(t, "s")), authClient(t))
		require.Nil(t, bErr)
		return mint(context.Background(), nil)
	}

	t.Run("an earlier expires_at wins over expires_in", func(t *testing.T) {
		at := time.Now().Add(10 * time.Minute).Unix()
		e, bErr := mintOnce(t, http.StatusOK, `{"access_token":"x","token_type":"Bearer","expires_in":3600,"expires_at":`+strconv.FormatInt(at, 10)+`}`)
		require.Nil(t, bErr)
		assert.Equal(t, time.Unix(at, 0), e.ExpiresAt)
	})

	t.Run("a token that has already expired is refused", func(t *testing.T) {
		_, bErr := mintOnce(t, http.StatusOK, `{"access_token":"x","token_type":"Bearer","expires_at":1}`)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "already expired")
	})

	t.Run("a token type other than Bearer is refused", func(t *testing.T) {
		// The endpoint reflects the subject token in token_type; it must not reach the caller.
		_, bErr := mintOnce(t, http.StatusOK, `{"access_token":"x","token_type":"s","expires_in":3600}`)
		require.NotNil(t, bErr)
		assert.Equal(t, http.StatusBadGateway, *bErr.StatusCode)
		assert.Contains(t, bErr.Error.Message, "other than Bearer")
		assert.NotContains(t, bErr.Error.Message, `"s"`)
	})

	t.Run("lowercase bearer is accepted", func(t *testing.T) {
		e, bErr := mintOnce(t, http.StatusOK, `{"access_token":"x","token_type":"bearer","expires_in":3600}`)
		require.Nil(t, bErr)
		assert.Equal(t, "x", e.Value)
	})

	t.Run("extra params cannot override the grant's own fields", func(t *testing.T) {
		es := newExchangeServer(t)
		cfg := teConfig(es.URL, subjectTokenFile(t, "real"))
		cfg.ExtraParams["subject_token"] = "forged"
		cfg.ExtraParams["grant_type"] = "password"
		_, mint, bErr := MinterForConfig(cfg, authClient(t))
		require.Nil(t, bErr)
		_, bErr = mint(context.Background(), nil)
		require.Nil(t, bErr)
		_, _, params := es.snapshot()
		assert.Equal(t, "real", params["subject_token"])
		assert.Equal(t, TokenExchangeGrantType, params["grant_type"])
	})
}

func TestTokenExchangeRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var followed int
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/elsewhere" {
					mu.Lock()
					followed++
					mu.Unlock()
					_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer","expires_in":3600}`))
					return
				}
				http.Redirect(w, r, "/elsewhere", status) // same origin, different path
			}))
			t.Cleanup(srv.Close)
			t.Setenv(schemas.TokenExchangeURLsEnv, srv.URL+"/token")
			_, mint, bErr := MinterForConfig(teConfig(srv.URL+"/token", subjectTokenFile(t, "s")), authClient(t))
			require.Nil(t, bErr)
			_, bErr = mint(context.Background(), nil)
			require.NotNil(t, bErr)
			mu.Lock()
			defer mu.Unlock()
			assert.Zero(t, followed, "the subject token must not be replayed to the redirect target")
		})
	}
}

func TestReadCredentialFileRefusals(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(CredentialDirsEnv, dir)

	big := filepath.Join(dir, "big")
	require.NoError(t, os.WriteFile(big, []byte(strings.Repeat("a", maxSubjectTokenBytes+1)), 0o600))
	_, err := ReadCredentialFile(big, maxSubjectTokenBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "byte limit")

	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))
	_, err = ReadCredentialFile(filepath.Join(dir, "sub"), maxSubjectTokenBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")

	_, err = ReadCredentialFile(dir, maxSubjectTokenBytes)
	require.ErrorIs(t, err, ErrCredentialPathNotAllowed, "the directory itself is not a credential file")

	_, err = ReadCredentialFile(filepath.Join(dir, "..", "elsewhere"), maxSubjectTokenBytes)
	require.ErrorIs(t, err, ErrCredentialPathNotAllowed)

	_, err = ReadCredentialFile("relative/token", maxSubjectTokenBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute path")

	// A Kubernetes-style rotation: the file is a symlink through ..data inside the mount.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "..2026_11_18"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "..2026_11_18", "token"), []byte("rotated\n"), 0o600))
	require.NoError(t, os.Symlink("..2026_11_18", filepath.Join(dir, "..data")))
	require.NoError(t, os.Symlink(filepath.Join("..data", "token"), filepath.Join(dir, "token")))
	got, err := ReadCredentialFile(filepath.Join(dir, "token"), maxSubjectTokenBytes)
	require.NoError(t, err)
	assert.Equal(t, "rotated", got)
}

// TestTokenExchangeMinterChecksEndpoint pins that the exported constructor applies the endpoint
// allowlist itself, before the subject token file is read.
func TestTokenExchangeMinterChecksEndpoint(t *testing.T) {
	file := subjectTokenFile(t, "s")
	t.Setenv(schemas.TokenExchangeURLsEnv, "https://auth.openai.com/oauth/token")
	for _, u := range []string{"https://attacker.example/token", "http://auth.openai.com/oauth/token"} {
		mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: u, SubjectTokenFile: file}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr, u)
		assert.Contains(t, bErr.Error.Message, "is not allowed")
	}
}
