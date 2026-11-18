package openai_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestOAuthKeyConfigTokenExchangeMintsBearer pins OpenAI workload identity federation end to end
// through the provider: a key with no value and a token_exchange oauth_key_config posts the
// workload's subject token as JSON, sends the exchanged token as the bearer, and reuses it.
func TestOAuthKeyConfigTokenExchangeMintsBearer(t *testing.T) {
	var (
		mu        sync.Mutex
		exchanges []map[string]string
		auths     []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]string
			_ = json.Unmarshal(raw, &body)
			mu.Lock()
			exchanges = append(exchanges, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"access_token":"wif-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv(tokencache.CredentialDirsEnv, dir)
	t.Setenv(schemas.TokenExchangeURLsEnv, srv.URL+"/oauth/token")
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("projected.sa.token"), 0o600); err != nil {
		t.Fatal(err)
	}

	provider := newOAuthProvider(t, srv.URL)
	key := schemas.Key{
		Models: []string{"*"},
		OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType:        schemas.OAuthGrantTokenExchange,
			TokenURL:         *schemas.NewSecretVar(srv.URL + "/oauth/token"),
			SubjectTokenFile: tokenFile,
			RequestEncoding:  schemas.OAuthRequestEncodingJSON,
			ExtraParams:      map[string]string{"identity_provider_id": "idp_1", "service_account_id": "sa_1"},
		},
	}
	for i := range 2 {
		if bErr := oauthChat(t, provider, key); bErr != nil {
			t.Fatalf("ChatCompletion attempt %d: %v", i+1, bErr.Error.Message)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(exchanges))
	}
	got := exchanges[0]
	if got["grant_type"] != "urn:ietf:params:oauth:grant-type:token-exchange" || got["subject_token"] != "projected.sa.token" ||
		got["subject_token_type"] != "urn:ietf:params:oauth:token-type:jwt" || got["identity_provider_id"] != "idp_1" || got["service_account_id"] != "sa_1" {
		t.Fatalf("exchange body = %v", got)
	}
	for _, a := range auths {
		if a != "Bearer wif-token" {
			t.Fatalf("Authorization = %q, want the exchanged bearer", a)
		}
	}
}

// TestOAuthKeyConfigTokenExchangeFailureSurfaces pins that a failed exchange is returned to the
// caller and nothing is sent upstream: there is no static value to fall back on.
func TestOAuthKeyConfigTokenExchangeFailureSurfaces(t *testing.T) {
	var upstream int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_grant","message":"no mapping matches"}}`))
			return
		}
		mu.Lock()
		upstream++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv(tokencache.CredentialDirsEnv, dir)
	t.Setenv(schemas.TokenExchangeURLsEnv, srv.URL+"/oauth/token")
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := schemas.Key{Models: []string{"*"}, OAuthKeyConfig: &schemas.OAuthKeyConfig{
		GrantType: schemas.OAuthGrantTokenExchange, TokenURL: *schemas.NewSecretVar(srv.URL + "/oauth/token"),
		SubjectTokenFile: tokenFile, RequestEncoding: schemas.OAuthRequestEncodingJSON,
	}}
	bErr := oauthChat(t, newOAuthProvider(t, srv.URL), key)
	if bErr == nil || bErr.StatusCode == nil || *bErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("want the exchange's 400, got %+v", bErr)
	}
	if want := "invalid_grant no mapping matches"; !strings.Contains(bErr.Error.Message, want) {
		t.Fatalf("message %q does not contain %q", bErr.Error.Message, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if upstream != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstream)
	}
}
