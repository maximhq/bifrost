package oauth2

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clientCredentialsServer is one host playing the MCP server, its protected
// resource metadata and its authorization server, recording token requests.
type clientCredentialsServer struct {
	server   *httptest.Server
	metadata map[string]any // authorization server metadata; nil serves none

	mu       sync.Mutex
	requests []*http.Request
	forms    []url.Values
	reject   bool // answer token requests with 401 invalid_client
}

func newClientCredentialsServer(t testing.TB, metadata func(base string) map[string]any) *clientCredentialsServer {
	t.Helper()
	s := &clientCredentialsServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.server.URL+`/prm"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/prm", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": s.server.URL + "/mcp", "authorization_servers": []string{s.server.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		if s.metadata == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(s.metadata)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.forms = append(s.forms, r.PostForm)
		reject := s.reject
		n := len(s.forms)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": "unknown client"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-" + string(rune('0'+n)), "token_type": "Bearer", "expires_in": 3600})
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	if metadata != nil {
		s.metadata = metadata(s.server.URL)
	}
	return s
}

func (s *clientCredentialsServer) tokenRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.forms)
}

func (s *clientCredentialsServer) lastRequest() (*http.Request, url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1], s.forms[len(s.forms)-1]
}

func ccMetadata(grants, methods, algs []string) func(string) map[string]any {
	return func(base string) map[string]any {
		doc := map[string]any{"issuer": base, "token_endpoint": base + "/token"}
		if grants != nil {
			doc["grant_types_supported"] = grants
		}
		if methods != nil {
			doc["token_endpoint_auth_methods_supported"] = methods
		}
		if algs != nil {
			doc["token_endpoint_auth_signing_alg_values_supported"] = algs
		}
		return doc
	}
}

func ccPEM(t testing.TB, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func ccSecretConfig() *schemas.MCPClientCredentialsConfig {
	return &schemas.MCPClientCredentialsConfig{ClientID: schemas.NewSecretVar("bifrost"), ClientSecret: schemas.NewSecretVar("s3cret"), Scopes: []string{"mcp:read"}}
}

func newCCProvider() *OAuth2Provider {
	p := NewOAuth2Provider(nil, nil)
	p.retryBaseDelay = time.Millisecond
	return p
}

// TestPrepareClientCredentials pins discovery on save: the token endpoint comes
// from the MCP server's metadata, the grant, method and algorithm must be
// advertised when the server lists them, and empty settings are filled in.
func TestPrepareClientCredentials(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyConfig := func(key any, alg string) *schemas.MCPClientCredentialsConfig {
		return &schemas.MCPClientCredentialsConfig{ClientID: schemas.NewSecretVar("bifrost"), PrivateKey: schemas.NewSecretVar(ccPEM(t, key)), SigningAlgorithm: alg}
	}
	cc := []string{"client_credentials"}

	t.Run("discovers the endpoint and the body auth method", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(cc, []string{"client_secret_post"}, nil))
		got, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, ccSecretConfig())
		require.NoError(t, err)
		assert.Equal(t, s.server.URL+"/token", got.TokenURL)
		assert.Equal(t, schemas.ClientSecretPost, got.AuthMethod)
	})
	t.Run("defaults to basic", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(cc, nil, nil))
		got, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, ccSecretConfig())
		require.NoError(t, err)
		assert.Equal(t, schemas.ClientSecretBasic, got.AuthMethod)
	})
	t.Run("refuses a server that does not list the grant", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(nil, nil, nil))
		_, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, ccSecretConfig())
		require.ErrorContains(t, err, "does not advertise the client_credentials grant")
	})
	t.Run("refuses an unadvertised auth method", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(cc, []string{"private_key_jwt"}, nil))
		_, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, ccSecretConfig())
		require.ErrorContains(t, err, "does not accept client_secret_basic")
	})
	t.Run("derives ES384 from a P-384 key", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(cc, []string{"private_key_jwt"}, []string{"ES384"}))
		got, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, keyConfig(ecKey, ""))
		require.NoError(t, err)
		assert.Equal(t, "ES384", got.SigningAlgorithm)
	})
	t.Run("derives the advertised RSA algorithm", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(cc, nil, []string{"PS256"}))
		got, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, keyConfig(rsaKey, ""))
		require.NoError(t, err)
		assert.Equal(t, "PS256", got.SigningAlgorithm)
	})
	t.Run("refuses an unadvertised algorithm", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(cc, nil, []string{"PS256"}))
		_, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, keyConfig(rsaKey, "RS256"))
		require.ErrorContains(t, err, "does not accept the RS256 signing algorithm")
	})
	t.Run("refuses a key that cannot sign the algorithm", func(t *testing.T) {
		s := newClientCredentialsServer(t, ccMetadata(cc, nil, nil))
		_, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, keyConfig(ecKey, "RS256"))
		require.ErrorContains(t, err, "needs an RSA private key")
	})
	t.Run("a configured token_url skips discovery", func(t *testing.T) {
		s := newClientCredentialsServer(t, nil)
		cfg := ccSecretConfig()
		cfg.TokenURL = s.server.URL + "/token"
		got, err := PrepareClientCredentials(context.Background(), s.server.URL+"/mcp", false, cfg)
		require.NoError(t, err)
		assert.Equal(t, s.server.URL+"/token", got.TokenURL)
		assert.Equal(t, schemas.ClientSecretBasic, got.AuthMethod)
	})
}

// TestMintClientCredentialsToken pins the token request for each client
// authentication method.
func TestMintClientCredentialsToken(t *testing.T) {
	s := newClientCredentialsServer(t, nil)
	p := newCCProvider()
	serverURL := s.server.URL + "/mcp"
	base := func() *schemas.MCPClientCredentialsConfig {
		cfg := ccSecretConfig()
		cfg.TokenURL = s.server.URL + "/token"
		return cfg
	}

	t.Run("basic", func(t *testing.T) {
		_, err := p.mintClientCredentialsToken(context.Background(), base(), serverURL)
		require.NoError(t, err)
		r, form := s.lastRequest()
		user, pass, ok := r.BasicAuth()
		require.True(t, ok)
		assert.Equal(t, "bifrost", user)
		assert.Equal(t, "s3cret", pass)
		assert.Equal(t, "client_credentials", form.Get("grant_type"))
		assert.Equal(t, "mcp:read", form.Get("scope"))
		assert.Equal(t, serverURL, form.Get("resource"), "resource defaults to the MCP server URL")
		assert.Empty(t, form.Get("client_secret"))
	})
	t.Run("post", func(t *testing.T) {
		cfg := base()
		cfg.AuthMethod = schemas.ClientSecretPost
		empty := ""
		cfg.Resource = &empty
		_, err := p.mintClientCredentialsToken(context.Background(), cfg, serverURL)
		require.NoError(t, err)
		r, form := s.lastRequest()
		_, _, ok := r.BasicAuth()
		assert.False(t, ok)
		assert.Equal(t, "bifrost", form.Get("client_id"))
		assert.Equal(t, "s3cret", form.Get("client_secret"))
		assert.False(t, form.Has("resource"), "an empty resource is not sent")
	})
	t.Run("private_key_jwt", func(t *testing.T) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		cfg := base()
		cfg.ClientSecret = nil
		cfg.PrivateKey = schemas.NewSecretVar(ccPEM(t, key))
		cfg.SigningAlgorithm = "ES256"
		cfg.KeyID = "k1"
		_, err = p.mintClientCredentialsToken(context.Background(), cfg, serverURL)
		require.NoError(t, err)
		_, form := s.lastRequest()
		assert.Equal(t, "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", form.Get("client_assertion_type"))
		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(form.Get("client_assertion"), claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil })
		require.NoError(t, err)
		assert.Equal(t, "k1", token.Header["kid"])
		assert.Equal(t, "bifrost", claims["iss"])
		assert.Equal(t, "bifrost", claims["sub"])
		assert.Equal(t, s.server.URL+"/token", claims["aud"])
		assert.NotEmpty(t, claims["jti"])
		assert.Empty(t, form.Get("client_secret"))
	})
}

// TestGetClientCredentialsAccessToken pins the shared cache: one token request
// serves every call, an edit or a force refresh mints anew, and a refused
// credential is reported without the endpoint being retried on every call.
func TestGetClientCredentialsAccessToken(t *testing.T) {
	s := newClientCredentialsServer(t, nil)
	p := newCCProvider()
	cfg := ccSecretConfig()
	cfg.TokenURL = s.server.URL + "/token"
	config := &schemas.MCPClientConfig{ID: "cc-1", Name: "cc", AuthType: schemas.MCPAuthTypeClientCredentials, ConnectionString: schemas.NewSecretVar(s.server.URL + "/mcp"), ClientCredentials: cfg}

	first, err := p.GetClientCredentialsAccessToken(context.Background(), config)
	require.NoError(t, err)
	second, err := p.GetClientCredentialsAccessToken(context.Background(), config)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, s.tokenRequests(), "the cached token serves every call")

	require.NoError(t, p.ForceRefreshAccessToken(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config))
	assert.Equal(t, 2, s.tokenRequests(), "a force refresh mints a new token")
	third, err := p.GetClientCredentialsAccessToken(context.Background(), config)
	require.NoError(t, err)
	assert.NotEqual(t, first, third)

	edited := *config
	editedCfg := *cfg
	editedCfg.ClientSecret = schemas.NewSecretVar("rotated")
	edited.ClientCredentials = &editedCfg
	_, err = p.GetClientCredentialsAccessToken(context.Background(), &edited)
	require.NoError(t, err)
	assert.Equal(t, 3, s.tokenRequests(), "a rotated secret never reuses the old token")

	s.mu.Lock()
	s.reject = true
	s.mu.Unlock()
	rejected := *config
	rejected.ID = "cc-2"
	_, err = p.GetClientCredentialsAccessToken(context.Background(), &rejected)
	require.ErrorContains(t, err, "refused the client credentials of MCP client cc: invalid_client: unknown client")
	_, err = p.GetClientCredentialsAccessToken(context.Background(), &rejected)
	require.Error(t, err)
	assert.Equal(t, 4, s.tokenRequests(), "a refused credential backs off instead of retrying on every call")
}

// BenchmarkGetClientCredentialsAccessToken measures a tool call's cost once the
// token is cached: the cache key hash and one map load. A private key enters the
// hash, so it is measured too.
func BenchmarkGetClientCredentialsAccessToken(b *testing.B) {
	s := newClientCredentialsServer(b, nil)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(b, err)
	keyCfg := &schemas.MCPClientCredentialsConfig{ClientID: schemas.NewSecretVar("bifrost"), PrivateKey: schemas.NewSecretVar(ccPEM(b, ecKey)), SigningAlgorithm: "ES256"}
	for name, cfg := range map[string]*schemas.MCPClientCredentialsConfig{"secret": ccSecretConfig(), "private_key": keyCfg} {
		b.Run(name, func(b *testing.B) {
			p := newCCProvider()
			cfg.TokenURL = s.server.URL + "/token"
			config := &schemas.MCPClientConfig{ID: "cc-" + name, Name: "cc", AuthType: schemas.MCPAuthTypeClientCredentials, ConnectionString: schemas.NewSecretVar(s.server.URL + "/mcp"), ClientCredentials: cfg}
			_, err := p.GetClientCredentialsAccessToken(context.Background(), config)
			require.NoError(b, err)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := p.GetClientCredentialsAccessToken(context.Background(), config); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestResolveClientCredentialsRequestsAToken: credentials the server refuses
// fail the save.
func TestResolveClientCredentialsRequestsAToken(t *testing.T) {
	s := newClientCredentialsServer(t, ccMetadata([]string{"client_credentials"}, nil, nil))
	p := newCCProvider()
	got, err := p.ResolveClientCredentials(context.Background(), s.server.URL+"/mcp", false, ccSecretConfig())
	require.NoError(t, err)
	assert.Equal(t, s.server.URL+"/token", got.TokenURL)
	assert.Equal(t, 1, s.tokenRequests())

	s.mu.Lock()
	s.reject = true
	s.mu.Unlock()
	_, err = p.ResolveClientCredentials(context.Background(), s.server.URL+"/mcp", false, ccSecretConfig())
	require.ErrorContains(t, err, "the token endpoint refused the client credentials: invalid_client: unknown client")
}

// TestClientCredentialsErrorsHideUpstreamText: a token endpoint's raw body and the
// query of an MCP URL or token_url never reach the error text; the status and the
// OAuth error do.
func TestClientCredentialsErrorsHideUpstreamText(t *testing.T) {
	var status int
	var body string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	p := newCCProvider()
	cfg := ccSecretConfig()
	cfg.TokenURL = upstream.URL + "/token"
	config := &schemas.MCPClientConfig{ID: "cc-hide", Name: "cc", AuthType: schemas.MCPAuthTypeClientCredentials, ConnectionString: schemas.NewSecretVar(upstream.URL + "/mcp"), ClientCredentials: cfg}

	for name, tc := range map[string]struct {
		status int
		want   string
	}{
		"unclassified status": {http.StatusTeapot, "the token endpoint answered status 418"},
		"transient status":    {http.StatusBadGateway, "the token endpoint answered status 502"},
		"200 without a token": {http.StatusOK, "the token response has no access_token"},
		"201 without a token": {http.StatusCreated, "the token response has no access_token"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body = tc.status, "<html>internal diagnostics</html>"
			_, err := p.ResolveClientCredentials(context.Background(), upstream.URL+"/mcp", false, cfg)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "internal diagnostics")
			config.ID = "cc-hide-" + name
			_, err = p.GetClientCredentialsAccessToken(context.Background(), config)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "internal diagnostics")
		})
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	// A token_url carrying a query credential keeps it out of a refusal and a connection failure.
	for name, tokenURL := range map[string]string{
		"refusing token_url":    upstream.URL + "/token?api_key=SECRET",
		"unreachable token_url": closed.URL + "/token?api_key=SECRET",
	} {
		t.Run(name, func(t *testing.T) {
			status, body = http.StatusTeapot, "nope"
			withQuery := ccSecretConfig()
			withQuery.TokenURL = tokenURL
			_, err := p.ResolveClientCredentials(context.Background(), upstream.URL+"/mcp", false, withQuery)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "SECRET")
			_, err = p.GetClientCredentialsAccessToken(context.Background(), &schemas.MCPClientConfig{ID: "cc-q-" + name, Name: "cc", AuthType: schemas.MCPAuthTypeClientCredentials, ConnectionString: schemas.NewSecretVar(upstream.URL + "/mcp"), ClientCredentials: withQuery})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "SECRET")
		})
	}
	s := newClientCredentialsServer(t, ccMetadata([]string{"authorization_code"}, nil, nil))
	for name, serverURL := range map[string]string{
		"unreachable server": closed.URL + "/mcp?api_key=SECRET",
		"grant not offered":  s.server.URL + "/mcp?api_key=SECRET",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PrepareClientCredentials(context.Background(), serverURL, false, ccSecretConfig())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "SECRET")
		})
	}
}

// TestClientCredentialsAccepts2xxTokenResponses verifies that token endpoints
// returning 201 Created (such as CrowdStrike Falcon's /oauth2/token) succeed
// on both save-time credential resolution and runtime token minting when the
// response carries a valid access_token, while still classifying OAuth errors
// sent with a 2xx status as rejections.
func TestClientCredentialsAccepts2xxTokenResponses(t *testing.T) {
	var status int
	var body string
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)

	p := newCCProvider()
	cfg := ccSecretConfig()
	cfg.TokenURL = upstream.URL + "/token"

	status = http.StatusCreated
	body = `{"access_token":"falcon-201-token","token_type":"bearer","expires_in":1799}`

	resolved, err := p.ResolveClientCredentials(context.Background(), upstream.URL+"/mcp", false, cfg)
	require.NoError(t, err)
	assert.Equal(t, upstream.URL+"/token", resolved.TokenURL)
	assert.Equal(t, 1, calls)

	config := &schemas.MCPClientConfig{
		ID:                "cc-crowdstrike-201",
		Name:              "test_falcon",
		AuthType:          schemas.MCPAuthTypeClientCredentials,
		ConnectionString:  schemas.NewSecretVar(upstream.URL + "/mcp"),
		ClientCredentials: resolved,
	}
	token, err := p.GetClientCredentialsAccessToken(context.Background(), config)
	require.NoError(t, err)
	assert.Equal(t, "falcon-201-token", token)
	assert.Equal(t, 2, calls)

	cached, err := p.GetClientCredentialsAccessToken(context.Background(), config)
	require.NoError(t, err)
	assert.Equal(t, "falcon-201-token", cached)
	assert.Equal(t, 2, calls, "the cached 201 token serves subsequent runtime calls")

	require.NoError(t, p.ForceRefreshAccessToken(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config))
	assert.Equal(t, 3, calls)

	// A 201 carrying an RFC/alias error without an access_token must still be
	// classified as a permanent client refusal rather than retried, even when an
	// unrelated field fails token-response struct decoding and percent-decoding.
	status = http.StatusCreated
	body = `{"error":"invalid_client","error_description":"100% invalid","expires_in":"0"}`
	calls = 0
	_, err = p.ResolveClientCredentials(context.Background(), upstream.URL+"/mcp", false, cfg)
	require.ErrorContains(t, err, "the token endpoint refused the client credentials: invalid_client: 100% invalid")
	assert.Equal(t, 1, calls)
}
