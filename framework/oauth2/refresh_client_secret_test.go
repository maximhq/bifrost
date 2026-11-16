package oauth2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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

// TestRefresh_ProviderRejectionShapes pins how a refresh rejection is
// classified across the shapes real authorization servers send. Slack
// answers HTTP 200 with {"ok":false,"error":...}; Atlassian's v2 server
// answers 403 unauthorized_client. A permanent rejection must flip the row to
// needs_reauth and surface ErrOAuth2TokenExpired so the caller gets a re-auth
// prompt; a transient one must leave the row active and be retried.
func TestRefresh_ProviderRejectionShapes(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantPermanent bool
		wantCalls     int32
		wantReason    string
	}{
		{name: "slack 200 ok:false bad_client_secret", status: http.StatusOK, body: `{"ok":false,"error":"bad_client_secret"}`, wantPermanent: true, wantCalls: 1, wantReason: "bad_client_secret"},
		{name: "slack 200 ok:false invalid_refresh_token", status: http.StatusOK, body: `{"ok":false,"error":"invalid_refresh_token"}`, wantPermanent: true, wantCalls: 1, wantReason: "invalid_refresh_token"},
		{name: "slack 200 ok:false ratelimited is retried", status: http.StatusOK, body: `{"ok":false,"error":"ratelimited"}`, wantCalls: maxTokenRetries},
		{name: "slack 200 ok:false invalid_arguments fails fast", status: http.StatusOK, body: `{"ok":false,"error":"invalid_arguments"}`, wantCalls: 1},
		{name: "atlassian v2 403 unauthorized_client", status: http.StatusForbidden, body: `{"error":"unauthorized_client","error_description":"refresh_token is invalid"}`, wantPermanent: true, wantCalls: 1, wantReason: "unauthorized_client"},
		{name: "400 invalid_client", status: http.StatusBadRequest, body: `{"error":"invalid_client"}`, wantPermanent: true, wantCalls: 1, wantReason: "invalid_client"},
		{name: "401 without an oauth body", status: http.StatusUnauthorized, body: `unauthorized`, wantPermanent: true, wantCalls: 1},
		{name: "400 invalid_request fails fast", status: http.StatusBadRequest, body: `{"error":"invalid_request"}`, wantCalls: 1},
		{name: "403 without an oauth body is retried", status: http.StatusForbidden, body: `forbidden`, wantCalls: maxTokenRetries},
		{name: "503 is retried", status: http.StatusServiceUnavailable, body: `{"error":"temporarily_unavailable"}`, wantCalls: maxTokenRetries},
		{name: "429 is retried", status: http.StatusTooManyRequests, body: `{"error":"slow_down"}`, wantCalls: maxTokenRetries},
		{name: "form-encoded 200 invalid_grant", status: http.StatusOK, body: `error=invalid_grant&error_description=bad+refresh+token`, wantPermanent: true, wantCalls: 1, wantReason: "invalid_grant: bad refresh token"},
		{name: "github bad_refresh_token", status: http.StatusOK, body: `{"error":"bad_refresh_token","error_description":"The refresh token passed is incorrect or expired."}`, wantPermanent: true, wantCalls: 1, wantReason: "bad_refresh_token"},
		{name: "404 with an unrecognised code fails fast", status: http.StatusNotFound, body: `{"error":"not_found"}`, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			store, oauthConfigID := newAccessTokenTestStore(server.URL + "/token")
			seedAccessToken(store, oauthConfigID, "shared", "tok-1", "old-access-token", "refresh-token", bifrost.Ptr(time.Now().Add(-time.Minute)))

			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			provider.retryBaseDelay = time.Millisecond
			_, err := provider.GetAccessToken(context.Background(), oauthConfigID)
			require.Error(t, err)

			row := store.oauthTokens["tok-1"]
			assert.Equal(t, tt.wantCalls, calls.Load(), "token endpoint calls")
			if tt.wantPermanent {
				assert.ErrorIs(t, err, schemas.ErrOAuth2TokenExpired, "a permanent rejection must ask for re-auth")
				assert.Equal(t, "needs_reauth", row.Status)
				assert.Contains(t, row.StatusReason, tt.wantReason)
			} else {
				assert.NotErrorIs(t, err, schemas.ErrOAuth2TokenExpired, "a non-permanent failure must not ask for re-auth")
				assert.Equal(t, "active", row.Status)
			}
		})
	}
}
