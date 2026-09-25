package vertex

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestVertexAuthHeaders_APIKeyPreservesInjectedAuthHeader verifies that when the
// key carries an API key value, vertexAuthHeaders passes it as the "key" query
// parameter and leaves an Authorization header (set upstream from context extra
// headers) untouched. This mirrors the Gemini generation endpoints and lets a
// caller inject its own bearer token via context extra headers.
func TestVertexAuthHeaders_APIKeyPreservesInjectedAuthHeader(t *testing.T) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/cachedContents")
	req.Header.Set("Authorization", "Bearer injected-token")

	key := schemas.Key{Value: *schemas.NewSecretVar("api-key-123")}
	if err := (&VertexProvider{}).vertexAuthHeaders(req, key); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := string(req.Header.Peek("Authorization")); got != "Bearer injected-token" {
		t.Errorf("Authorization header was overwritten: got %q, want the injected token preserved", got)
	}
	if got := string(req.URI().QueryArgs().Peek("key")); got != "api-key-123" {
		t.Errorf("key query parameter: got %q, want %q", got, "api-key-123")
	}
}

// testServiceAccountJSON returns service-account credentials whose token_uri points at
// tokenURI, signed with a throwaway key, so the JWT-bearer exchange runs without Google.
func testServiceAccountJSON(t *testing.T, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	creds, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "test-project",
		"private_key_id": "test-key-id",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "bifrost-test@test-project.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	return string(creds)
}

// TestVertexAuthHeaders_OAuthTokenGoesThroughProxyConfig pins that the service-account
// token exchange leaves through the provider's proxy_config. It used to run on
// http.DefaultTransport, so behind a proxy-only egress every Vertex request failed at
// token acquisition while the inference client itself was correctly proxied.
func TestVertexAuthHeaders_OAuthTokenGoesThroughProxyConfig(t *testing.T) {
	var tokenHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy receives the absolute target URI in the request line.
		if r.URL.Host != "oauth2.test.invalid" || r.URL.Path != "/token" {
			http.Error(w, "unexpected target "+r.URL.String(), http.StatusBadGateway)
			return
		}
		tokenHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"proxied-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer proxy.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
		ProxyConfig:   &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(proxy.URL)},
	}
	config.CheckAndSetDefaults()
	provider, err := NewVertexProvider(config, nil)
	if err != nil {
		t.Fatalf("NewVertexProvider: %v", err)
	}

	key := schemas.Key{VertexKeyConfig: &schemas.VertexKeyConfig{
		AuthCredentials: *schemas.NewSecretVar(testServiceAccountJSON(t, "http://oauth2.test.invalid/token")),
	}}
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	if bifrostErr := provider.vertexAuthHeaders(req, key); bifrostErr != nil {
		t.Fatalf("vertexAuthHeaders: %s", bifrostErr.GetErrorString())
	}
	if got := string(req.Header.Peek("Authorization")); got != "Bearer proxied-token" {
		t.Errorf("Authorization = %q, want the token minted through the proxy", got)
	}
	if tokenHits.Load() != 1 {
		t.Errorf("proxy saw %d token requests, want 1", tokenHits.Load())
	}

	// The token source is cached per provider: a second provider built for another
	// proxy must not reuse a source bound to this one.
	other, err := NewVertexProvider(config, nil)
	if err != nil {
		t.Fatalf("NewVertexProvider: %v", err)
	}
	clientKey := getClientKey(key.VertexKeyConfig.AuthCredentials.GetValue())
	if _, ok := other.tokenSources.Load(clientKey); ok {
		t.Error("a fresh provider must start with an empty token-source cache")
	}
}
