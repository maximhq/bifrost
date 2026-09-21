package oauth2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestRefresh_ClientSecretOnlySentWhenConfigured pins the refresh_token
// grant's client authentication shape against the authorization_code
// exchange it mirrors: a public client (no client_secret on the OAuth config,
// which is what dynamic registration yields against a server whose only
// token_endpoint_auth_method is "none") must not send a client_secret
// parameter at all. An empty client_secret= is a client_secret_post attempt
// with a wrong secret to a strict server, and its invalid_client answer would
// flip a perfectly good refresh token's row to needs_reauth. A confidential
// client keeps sending its secret unchanged.
func TestRefresh_ClientSecretOnlySentWhenConfigured(t *testing.T) {
	tests := []struct {
		name          string
		clientSecret  *schemas.SecretVar
		wantParamSent bool
		wantValue     string
	}{
		{name: "public client (dynamic registration, no secret) omits client_secret", clientSecret: nil, wantParamSent: false},
		{name: "confidential client sends its client_secret", clientSecret: schemas.NewSecretVar("s3cr3t"), wantParamSent: true, wantValue: "s3cr3t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var sawParam bool
			var sawValue, sawGrant string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, r.ParseForm())
				mu.Lock()
				_, sawParam = r.PostForm["client_secret"]
				sawValue = r.PostForm.Get("client_secret")
				sawGrant = r.PostForm.Get("grant_type")
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "refreshed-access-token",
					"refresh_token": "rotated-refresh-token",
					"token_type":    "bearer",
					"expires_in":    3600,
				})
			}))
			defer server.Close()

			store, oauthConfigID := newAccessTokenTestStore(server.URL + "/token")
			store.oauthConfigs[oauthConfigID].ClientSecret = tt.clientSecret
			seedAccessToken(store, oauthConfigID, "shared", "tok-1", "old-access-token", "refresh-token", bifrost.Ptr(time.Now().Add(-time.Minute)))

			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			token, err := provider.GetAccessToken(context.Background(), oauthConfigID)
			require.NoError(t, err)
			assert.Equal(t, "refreshed-access-token", token)

			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, "refresh_token", sawGrant, "the upstream call must be the refresh_token grant")
			assert.Equal(t, tt.wantParamSent, sawParam, "client_secret parameter presence")
			assert.Equal(t, tt.wantValue, sawValue, "client_secret parameter value")
		})
	}
}
