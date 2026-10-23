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
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exchangeServer records each token request, form or JSON.
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
	es := &exchangeServer{status: http.StatusOK, body: `{"access_token":"exchanged","token_type":"Bearer","expires_in":3600}`}
	es.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		params := map[string]string{}
		if r.Header.Get("Content-Type") == "application/json" {
			_ = json.Unmarshal(raw, &params)
		} else {
			form, _ := url.ParseQuery(string(raw))
			for k := range form {
				params[k] = form.Get(k)
			}
		}
		es.mu.Lock()
		es.hits++
		es.contentType, es.params = r.Header.Get("Content-Type"), params
		status, body := es.status, es.body
		es.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(es.Close)
	return es
}

func (es *exchangeServer) snapshot() (int, string, map[string]string) {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.hits, es.contentType, es.params
}

// tokenDir returns a fresh directory allowed by SubjectTokenDirsEnv for this test.
func tokenDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(schemas.SubjectTokenDirsEnv, dir)
	return dir
}

func writeSubjectToken(t *testing.T, path, token string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(token+"\n"), 0o600))
}

func TestTokenExchangeMinter(t *testing.T) {
	t.Run("posts the subject token as an RFC 8693 form by default", func(t *testing.T) {
		es := newExchangeServer(t)
		file := filepath.Join(tokenDir(t), "token")
		writeSubjectToken(t, file, "subject-jwt-1")
		mint := TokenExchangeMinter(TokenExchangeConfig{
			TokenURL:         es.URL,
			SubjectTokenFile: file,
			Audience:         "https://api.example",
			Scopes:           []string{"a", "b"},
			ExtraParams:      map[string]string{"resource": "r"},
		}, authClient(t))

		before := time.Now()
		entry, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.Equal(t, "exchanged", entry.Value)
		assert.WithinDuration(t, before.Add(time.Hour), entry.ExpiresAt, 5*time.Second)

		hits, contentType, params := es.snapshot()
		assert.Equal(t, 1, hits)
		assert.Equal(t, "application/x-www-form-urlencoded", contentType)
		assert.Equal(t, map[string]string{
			"grant_type":         "urn:ietf:params:oauth:grant-type:token-exchange",
			"subject_token":      "subject-jwt-1",
			"subject_token_type": schemas.OAuthTokenTypeJWT,
			"audience":           "https://api.example",
			"scope":              "a b",
			"resource":           "r",
		}, params)
	})

	t.Run("json encoding sends the flat object OpenAI expects", func(t *testing.T) {
		es := newExchangeServer(t)
		file := filepath.Join(tokenDir(t), "token")
		writeSubjectToken(t, file, "subject-jwt")
		mint := TokenExchangeMinter(TokenExchangeConfig{
			TokenURL:         es.URL,
			SubjectTokenFile: file,
			SubjectTokenType: "urn:ietf:params:oauth:token-type:id_token",
			Encoding:         schemas.OAuthRequestEncodingJSON,
			ExtraParams:      map[string]string{"identity_provider_id": "idp_123", "service_account_id": "sa_456"},
		}, authClient(t))

		_, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		_, contentType, params := es.snapshot()
		assert.Equal(t, "application/json", contentType)
		assert.Equal(t, map[string]string{
			"grant_type":           "urn:ietf:params:oauth:grant-type:token-exchange",
			"subject_token":        "subject-jwt",
			"subject_token_type":   "urn:ietf:params:oauth:token-type:id_token",
			"identity_provider_id": "idp_123",
			"service_account_id":   "sa_456",
		}, params)
	})

	t.Run("re-reads the subject token file on every exchange", func(t *testing.T) {
		es := newExchangeServer(t)
		file := filepath.Join(tokenDir(t), "token")
		writeSubjectToken(t, file, "first")
		mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: file}, authClient(t))

		_, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		writeSubjectToken(t, file, "rotated")
		_, bErr = mint(context.Background(), nil)
		require.Nil(t, bErr)

		hits, _, params := es.snapshot()
		assert.Equal(t, 2, hits)
		assert.Equal(t, "rotated", params["subject_token"])
	})

	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, file string)
		want    string
	}{
		{"missing file", func(*testing.T, string) {}, "subject_token_file could not be read"},
		{"empty file", func(t *testing.T, file string) { writeSubjectToken(t, file, "  ") }, "subject_token_file is empty"},
		{"oversized file", func(t *testing.T, file string) {
			require.NoError(t, os.WriteFile(file, make([]byte, maxSubjectTokenBytes+1), 0o600))
		}, "larger than"},
	} {
		t.Run(tc.name+" is a configuration fault that never reaches the endpoint", func(t *testing.T) {
			es := newExchangeServer(t)
			file := filepath.Join(tokenDir(t), "token")
			tc.prepare(t, file)
			mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: file}, authClient(t))

			_, bErr := mint(context.Background(), nil)
			require.NotNil(t, bErr)
			assert.Contains(t, bErr.Error.Message, tc.want)
			require.NotNil(t, bErr.AllowFallbacks)
			assert.False(t, *bErr.AllowFallbacks)
			hits, _, _ := es.snapshot()
			assert.Zero(t, hits)
		})
	}

	t.Run("an OpenAI-shaped error keeps its reason and drops the subject token", func(t *testing.T) {
		es := newExchangeServer(t)
		es.status = http.StatusUnauthorized
		es.body = `{"error":{"message":"The provided service_account_id mapping does not match token attributes for secret-subject-jwt.","type":"invalid_request_error","param":null,"code":"invalid_grant"}}`
		file := filepath.Join(tokenDir(t), "token")
		writeSubjectToken(t, file, "secret-subject-jwt")
		mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: file, Encoding: schemas.OAuthRequestEncodingJSON}, authClient(t))

		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "invalid_grant The provided service_account_id mapping does not match token attributes")
		assert.NotContains(t, bErr.Error.Message, "secret-subject-jwt")
		assert.NotContains(t, bErr.Error.Message, "(no detail)")
	})

	t.Run("a file outside the allowed directories is never read or sent", func(t *testing.T) {
		es := newExchangeServer(t)
		tokenDir(t)
		outside := filepath.Join(t.TempDir(), "secret")
		writeSubjectToken(t, outside, "host-secret")
		mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: outside}, authClient(t))

		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "must be inside a directory listed in "+schemas.SubjectTokenDirsEnv)
		hits, _, _ := es.snapshot()
		assert.Zero(t, hits)
	})

	t.Run("a symlink out of the allowed directory is refused", func(t *testing.T) {
		es := newExchangeServer(t)
		dir := tokenDir(t)
		outside := filepath.Join(t.TempDir(), "secret")
		writeSubjectToken(t, outside, "host-secret")
		link := filepath.Join(dir, "token")
		require.NoError(t, os.Symlink(outside, link))
		mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: link}, authClient(t))

		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "resolves outside the directories")
		hits, _, _ := es.snapshot()
		assert.Zero(t, hits)
	})

	t.Run("a symlink chain inside the allowed directory is followed, as Kubernetes projects tokens", func(t *testing.T) {
		es := newExchangeServer(t)
		dir := tokenDir(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "..2026_10_23"), 0o700))
		writeSubjectToken(t, filepath.Join(dir, "..2026_10_23", "token"), "projected-jwt")
		require.NoError(t, os.Symlink("..2026_10_23", filepath.Join(dir, "..data")))
		require.NoError(t, os.Symlink(filepath.Join("..data", "token"), filepath.Join(dir, "token")))
		mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: filepath.Join(dir, "token")}, authClient(t))

		_, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		_, _, params := es.snapshot()
		assert.Equal(t, "projected-jwt", params["subject_token"])
	})

	t.Run("no allowed directories means the file source is refused", func(t *testing.T) {
		es := newExchangeServer(t)
		file := filepath.Join(tokenDir(t), "token")
		writeSubjectToken(t, file, "subject-jwt")
		t.Setenv(schemas.SubjectTokenDirsEnv, "")
		mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: file}, authClient(t))

		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "requires the "+schemas.SubjectTokenDirsEnv)
	})

	for _, encoding := range []schemas.OAuthRequestEncoding{schemas.OAuthRequestEncodingForm, schemas.OAuthRequestEncodingJSON} {
		t.Run(string(encoding)+": an endpoint error never echoes the subject token", func(t *testing.T) {
			es := newExchangeServer(t)
			es.status = http.StatusUnauthorized
			es.body = `{"error":"invalid_grant","error_description":"token secret-subject-jwt was not accepted"}`
			file := filepath.Join(tokenDir(t), "token")
			writeSubjectToken(t, file, "secret-subject-jwt")
			mint := TokenExchangeMinter(TokenExchangeConfig{TokenURL: es.URL, SubjectTokenFile: file, Encoding: encoding}, authClient(t))

			_, bErr := mint(context.Background(), nil)
			require.NotNil(t, bErr)
			require.NotNil(t, bErr.StatusCode)
			assert.Equal(t, http.StatusUnauthorized, *bErr.StatusCode)
			assert.Contains(t, bErr.Error.Message, "invalid_grant")
			assert.NotContains(t, bErr.Error.Message, "secret-subject-jwt")
		})
	}
}

func TestResolveBearerTokenExchange(t *testing.T) {
	es := newExchangeServer(t)
	file := filepath.Join(tokenDir(t), "token")
	writeSubjectToken(t, file, "subject-jwt")
	cfg := &schemas.OAuthKeyConfig{
		GrantType:        schemas.OAuthGrantTokenExchange,
		TokenURL:         *schemas.NewSecretVar(es.URL),
		SubjectTokenFile: file,
		RequestEncoding:  schemas.OAuthRequestEncodingJSON,
		ExtraParams:      map[string]string{"identity_provider_id": "idp_1", "service_account_id": "sa_1"},
	}
	cache := New[string](Options{})

	for range 3 {
		headers, bErr := ResolveBearer(context.Background(), cache, "", cfg, authClient(t))
		require.Nil(t, bErr)
		assert.Equal(t, "Bearer exchanged", headers["Authorization"])
	}
	hits, _, _ := es.snapshot()
	assert.Equal(t, 1, hits, "the minted token is cached until its refresh point")
}
