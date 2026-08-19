package main

// oauth-demo-server is a self-contained mock OAuth 2.1 authorization server +
// MCP resource server. It exists so you can exercise Bifrost's per-user OAuth
// path end-to-end — including access-token expiry and refresh-token rotation —
// without depending on a real upstream OAuth provider.
//
// Why this exists
// ───────────────
// Bifrost discovers OAuth servers via RFC 9728 (protected resource metadata)
// and RFC 8414 (authorization server metadata). It then performs:
//   1. Dynamic client registration (RFC 7591)
//   2. Authorization code flow with PKCE (S256, mandatory)
//   3. refresh_token grant when an access token has expired
//
// This server speaks all of that, with a deliberately tiny access-token TTL
// so the refresh path is easy to observe in logs.
//
// Endpoints
// ─────────
//   GET  /.well-known/oauth-protected-resource    (RFC 9728)
//   GET  /.well-known/oauth-authorization-server  (RFC 8414)
//   POST /register                                (RFC 7591 dynamic client reg)
//   GET  /authorize                               (auth code + PKCE — renders a tiny login form)
//   POST /token                                   (authorization_code + refresh_token grants)
//   ANY  /mcp                                     (MCP server, gated by Bearer token)
//
// Bifrost MCP client config
// ─────────────────────────
//
//	{
//	  "name": "oauth_demo",
//	  "connection_type": "http",
//	  "connection_string": "http://localhost:3003/mcp",
//	  "auth_type": "oauth2",
//	  "is_per_user": true,
//	  "tools_to_execute": ["*"]
//	}
//
// On the first tool call the MCP path returns 401 with a WWW-Authenticate
// header pointing at the protected-resource metadata. Bifrost follows the
// discovery chain, registers itself as a client, and runs the consent flow.
//
// Observing refresh
// ─────────────────
// Access tokens are issued with a 30-second TTL. Make a tool call, wait ≥30s,
// make another — Bifrost should call POST /token with grant_type=refresh_token
// before forwarding the second call. Watch the [token] log lines on this
// server to confirm.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	listenAddr = "localhost:3003"
	issuer     = "http://localhost:3003"

	// Deliberately tiny so refresh fires after a single idle pause.
	accessTokenTTL = 30 * time.Second

	// Long enough that several access-token refreshes succeed, short enough
	// that the full re-authentication path is observable in a single test
	// session. When this expires Bifrost should fall back to the consent flow.
	refreshTokenTTL = 2 * time.Minute

	// Authorization codes are single-use; this is the upper bound on how long
	// the user has to complete the redirect+token exchange.
	authCodeTTL = 1 * time.Minute
)

// ─── In-memory state ──────────────────────────────────────────────────────────

type registeredClient struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
}

type authCode struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	UserID        string
	ExpiresAt     time.Time
}

type tokenRecord struct {
	AccessToken      string
	RefreshToken     string
	ClientID         string
	Scope            string
	UserID           string
	ExpiresAt        time.Time // access token expiry
	RefreshExpiresAt time.Time // refresh token expiry
}

type store struct {
	mu            sync.RWMutex
	clients       map[string]*registeredClient
	authCodes     map[string]*authCode
	accessTokens  map[string]*tokenRecord
	refreshTokens map[string]*tokenRecord
}

// All state is in-memory. Restarting this server forgets every registered
// client, every issued token, and every auth code — by design, since this is
// a test fixture. NOTE: Bifrost (or any OAuth client) stores the client_id it
// received from dynamic registration in its own DB and reuses it on the next
// /authorize call. So restarting the mock server while leaving Bifrost's DB
// intact will trip "unknown client_id (run dynamic registration first)" — to
// recover, either re-create the MCP client config in Bifrost so it
// re-registers, or restart with a fresh Bifrost DB. If you need persistence
// across restarts, add a JSON-file load/save around the `clients` map.
var st = &store{
	clients:       map[string]*registeredClient{},
	authCodes:     map[string]*authCode{},
	accessTokens:  map[string]*tokenRecord{},
	refreshTokens: map[string]*tokenRecord{},
}

// ─── Utilities ────────────────────────────────────────────────────────────────

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// ─── Access logging ──────────────────────────────────────────────────────────

// loggingResponseWriter captures the status code so the request logger can
// report it. Defaults to 200 since http.ResponseWriter implicitly does.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.status = code
	lrw.ResponseWriter.WriteHeader(code)
}

// tagForPath gives each endpoint a short, recognizable label in the log so
// you can see which step of the OAuth dance Bifrost is currently on.
func tagForPath(method, path string) string {
	switch {
	case path == "/.well-known/oauth-protected-resource":
		return "discovery:protected-resource"
	case path == "/.well-known/oauth-authorization-server":
		return "discovery:auth-server"
	case path == "/register":
		return "register"
	case path == "/authorize":
		return "authorize"
	case path == "/token":
		return "token"
	case path == "/mcp" || strings.HasPrefix(path, "/mcp/"):
		return "mcp"
	default:
		return "other"
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Printf("[http] %-30s %s %s -> %d (%s)",
			tagForPath(r.Method, r.URL.Path), r.Method, r.URL.Path, lrw.status, time.Since(start).Round(time.Microsecond))
	})
}

// ─── Discovery ────────────────────────────────────────────────────────────────

func handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	log.Printf("[discovery] RFC 9728 protected-resource metadata requested by %s", r.UserAgent())
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 issuer + "/mcp",
		"authorization_servers":    []string{issuer},
		"scopes_supported":         []string{"mcp:read", "mcp:write"},
		"bearer_methods_supported": []string{"header"},
	})
}

func handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	log.Printf("[discovery] RFC 8414 authorization-server metadata requested by %s", r.UserAgent())
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"registration_endpoint":                 issuer + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp:read", "mcp:write"},
	})
}

// ─── Dynamic Client Registration ──────────────────────────────────────────────

func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		Scope                   string   `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON: "+err.Error())
		return
	}
	if len(req.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris required")
		return
	}
	if req.TokenEndpointAuthMethod == "" {
		req.TokenEndpointAuthMethod = "none"
	}
	if len(req.GrantTypes) == 0 {
		req.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	if len(req.ResponseTypes) == 0 {
		req.ResponseTypes = []string{"code"}
	}

	c := &registeredClient{
		ClientID:                "client-" + randHex(12),
		ClientName:              req.ClientName,
		RedirectURIs:            req.RedirectURIs,
		GrantTypes:              req.GrantTypes,
		ResponseTypes:           req.ResponseTypes,
		TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		Scope:                   req.Scope,
	}
	st.mu.Lock()
	st.clients[c.ClientID] = c
	st.mu.Unlock()

	log.Printf("[register] client_id=%s name=%q redirect_uris=%v", c.ClientID, c.ClientName, c.RedirectURIs)
	writeJSON(w, http.StatusCreated, c)
}

// ─── Authorize (auth code + PKCE) ─────────────────────────────────────────────

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>oauth-demo-server — sign in</title>
<style>
body{font-family:-apple-system,system-ui,sans-serif;max-width:480px;margin:80px auto;padding:0 24px;color:#222}
h1{font-size:18px;margin-bottom:4px}
p{color:#666;font-size:13px}
form{margin-top:24px;display:flex;flex-direction:column;gap:12px}
label{font-size:12px;color:#444}
input[type=text]{padding:8px;font-size:14px;border:1px solid #ccc;border-radius:4px}
button{padding:10px;font-size:14px;background:#222;color:#fff;border:0;border-radius:4px;cursor:pointer}
code{background:#f4f4f4;padding:1px 4px;border-radius:3px;font-size:12px}
</style></head><body>
<h1>oauth-demo-server</h1>
<p>Mock OAuth login. Pick any username — the server will issue a token bound to it. Use distinct names to simulate per-user OAuth.</p>
<p>Client: <code>{{.ClientID}}</code> · Scope: <code>{{.Scope}}</code></p>
<form method="GET" action="/authorize">
	{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
	<label for="user">Username</label>
	<input type="text" id="user" name="user" value="demo-user" autofocus>
	<button type="submit">Sign in &amp; approve</button>
</form>
</body></html>`))

func handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	responseType := q.Get("response_type")
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	state := q.Get("state")
	scope := q.Get("scope")
	user := strings.TrimSpace(q.Get("user"))

	if responseType != "code" {
		http.Error(w, "unsupported_response_type: only 'code' is supported", http.StatusBadRequest)
		return
	}
	st.mu.RLock()
	client, ok := st.clients[clientID]
	st.mu.RUnlock()
	if !ok {
		// Bifrost (or any OAuth client) caches the client_id from a previous
		// dynamic-registration response in its own DB and reuses it. If the
		// mock server has been restarted since then, that cached id is
		// unknown here. Re-create the MCP client config in Bifrost so it
		// re-registers, or start with a fresh Bifrost DB.
		http.Error(w, "unknown client_id (mock server state may have been wiped — re-create the MCP client in Bifrost so it re-registers)", http.StatusBadRequest)
		return
	}
	redirectAllowed := false
	for _, ru := range client.RedirectURIs {
		if ru == redirectURI {
			redirectAllowed = true
			break
		}
	}
	if !redirectAllowed {
		http.Error(w, "redirect_uri not registered for this client", http.StatusBadRequest)
		return
	}
	if codeChallenge == "" || codeChallengeMethod != "S256" {
		http.Error(w, "PKCE required: code_challenge with code_challenge_method=S256", http.StatusBadRequest)
		return
	}

	// First hop: render the login form so the human picks a username.
	if user == "" {
		hidden := map[string]string{
			"response_type":         responseType,
			"client_id":             clientID,
			"redirect_uri":          redirectURI,
			"code_challenge":        codeChallenge,
			"code_challenge_method": codeChallengeMethod,
		}
		if state != "" {
			hidden["state"] = state
		}
		if scope != "" {
			hidden["scope"] = scope
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginPage.Execute(w, map[string]any{
			"ClientID": clientID,
			"Scope":    scope,
			"Hidden":   hidden,
		})
		return
	}

	// Second hop: user has been chosen — mint a code and redirect back.
	code := "code-" + randHex(16)
	st.mu.Lock()
	st.authCodes[code] = &authCode{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
		Scope:         scope,
		UserID:        user,
		ExpiresAt:     time.Now().Add(authCodeTTL),
	}
	st.mu.Unlock()

	log.Printf("[authorize] approve client=%s user=%q scope=%q -> code=%s", clientID, user, scope, code)

	redirect, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	rq := redirect.Query()
	rq.Set("code", code)
	if state != "" {
		rq.Set("state", state)
	}
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// ─── Token endpoint (authorization_code + refresh_token) ──────────────────────

func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

// issueTokens optionally inherits the refresh-token expiry from a prior pair
// so refresh-rotation does NOT extend the refresh-token lifetime — once the
// session crosses refreshTokenTTL the user must re-authenticate, regardless of
// how many times they've refreshed in between. Pass time.Time{} (zero) for
// fresh issuance from the authorization_code grant.
func issueTokens(clientID, scope, userID string, inheritedRefreshExpiry time.Time) *tokenRecord {
	now := time.Now()
	refreshExpiresAt := inheritedRefreshExpiry
	if refreshExpiresAt.IsZero() {
		refreshExpiresAt = now.Add(refreshTokenTTL)
	}
	rec := &tokenRecord{
		AccessToken:      "at-" + randHex(20),
		RefreshToken:     "rt-" + randHex(20),
		ClientID:         clientID,
		Scope:            scope,
		UserID:           userID,
		ExpiresAt:        now.Add(accessTokenTTL),
		RefreshExpiresAt: refreshExpiresAt,
	}
	st.mu.Lock()
	st.accessTokens[rec.AccessToken] = rec
	st.refreshTokens[rec.RefreshToken] = rec
	st.mu.Unlock()

	log.Printf("[issue access]  token=%s user=%q expires_at=%s (in %s)",
		tokenPrefix(rec.AccessToken), rec.UserID, rec.ExpiresAt.Format(time.RFC3339), accessTokenTTL)
	log.Printf("[issue refresh] token=%s user=%q expires_at=%s (in %s%s)",
		tokenPrefix(rec.RefreshToken), rec.UserID,
		rec.RefreshExpiresAt.Format(time.RFC3339),
		time.Until(rec.RefreshExpiresAt).Round(time.Second),
		func() string {
			if !inheritedRefreshExpiry.IsZero() {
				return ", inherited from prior session"
			}
			return ""
		}())

	// Passive [expire access] log: fires at accessTokenTTL regardless of
	// whether anyone hits the server. Suppressed if the token was already
	// rotated by a refresh — the [revoke access] line covered it.
	at := rec.AccessToken
	userIDCopy := rec.UserID
	accessExpiresAt := rec.ExpiresAt
	time.AfterFunc(accessTokenTTL, func() {
		st.mu.RLock()
		_, stillActive := st.accessTokens[at]
		st.mu.RUnlock()
		if stillActive {
			log.Printf("[expire access] token=%s user=%q expired_at=%s (still in store — next /mcp call will trigger refresh)",
				tokenPrefix(at), userIDCopy, accessExpiresAt.Format(time.RFC3339))
		}
	})

	// Passive [expire refresh] log: same idea, fires at the absolute refresh
	// expiry. Suppressed if the refresh has already been rotated/revoked.
	rt := rec.RefreshToken
	refreshDelay := time.Until(rec.RefreshExpiresAt)
	if refreshDelay > 0 {
		refreshExpiry := rec.RefreshExpiresAt
		time.AfterFunc(refreshDelay, func() {
			st.mu.RLock()
			_, stillActive := st.refreshTokens[rt]
			st.mu.RUnlock()
			if stillActive {
				log.Printf("[expire refresh] token=%s user=%q expired_at=%s (still in store — next refresh attempt will fail; client must re-authenticate)",
					tokenPrefix(rt), userIDCopy, refreshExpiry.Format(time.RFC3339))
			}
		})
	}

	return rec
}

func tokenResponse(rec *tokenRecord) map[string]any {
	return map[string]any{
		"access_token":  rec.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenTTL.Seconds()),
		"refresh_token": rec.RefreshToken,
		"scope":         rec.Scope,
	}
}

func handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form: "+err.Error())
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		handleAuthorizationCodeGrant(w, r)
	case "refresh_token":
		handleRefreshTokenGrant(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "supported: authorization_code, refresh_token")
	}
}

func handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	redirectURI := r.PostForm.Get("redirect_uri")
	clientID := r.PostForm.Get("client_id")
	codeVerifier := r.PostForm.Get("code_verifier")

	st.mu.Lock()
	ac, ok := st.authCodes[code]
	if ok {
		delete(st.authCodes, code) // single-use
	}
	st.mu.Unlock()

	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-used code")
		return
	}
	if time.Now().After(ac.ExpiresAt) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired")
		return
	}
	if clientID != "" && clientID != ac.ClientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	if redirectURI != ac.RedirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if !verifyPKCE(codeVerifier, ac.CodeChallenge) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	// Fresh authentication — refresh-token lifetime starts now.
	rec := issueTokens(ac.ClientID, ac.Scope, ac.UserID, time.Time{})
	log.Printf("[token] code-grant client=%s user=%q -> at=%s rt=%s expires_in=%ds",
		rec.ClientID, rec.UserID, tokenPrefix(rec.AccessToken), tokenPrefix(rec.RefreshToken), int(accessTokenTTL.Seconds()))
	writeJSON(w, http.StatusOK, tokenResponse(rec))
}

func handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	rt := r.PostForm.Get("refresh_token")
	if rt == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token required")
		return
	}

	st.mu.Lock()
	old, ok := st.refreshTokens[rt]
	st.mu.Unlock()
	if !ok {
		log.Printf("[token] refresh REJECTED: unknown or already-rotated refresh_token=%s", tokenPrefix(rt))
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or revoked refresh_token")
		return
	}
	if time.Now().After(old.RefreshExpiresAt) {
		// Refresh expired — clean up so the [expire refresh] passive log
		// (which has already fired) reflects reality, then reject.
		st.mu.Lock()
		delete(st.refreshTokens, rt)
		delete(st.accessTokens, old.AccessToken)
		st.mu.Unlock()
		log.Printf("[token] refresh REJECTED: refresh_token=%s for user=%q expired %s — client must re-authenticate via /authorize",
			tokenPrefix(rt), old.UserID, expiryRelative(old.RefreshExpiresAt))
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token expired; client must re-authenticate")
		return
	}

	// Refresh is valid — rotate: invalidate old refresh + access tokens.
	st.mu.Lock()
	delete(st.refreshTokens, rt)
	delete(st.accessTokens, old.AccessToken)
	st.mu.Unlock()

	log.Printf("[revoke refresh] token=%s user=%q (rotated by client; refresh would have expired %s)",
		tokenPrefix(rt), old.UserID, expiryRelative(old.RefreshExpiresAt))
	log.Printf("[revoke access]  token=%s user=%q (companion to rotated refresh; %s)",
		tokenPrefix(old.AccessToken), old.UserID, expiryRelative(old.ExpiresAt))

	// Inherit the original refresh expiry so rotation does not extend the
	// session past refreshTokenTTL from the original /authorize.
	rec := issueTokens(old.ClientID, old.Scope, old.UserID, old.RefreshExpiresAt)
	log.Printf("[token] refresh client=%s user=%q old_rt=%s -> at=%s rt=%s expires_in=%ds",
		rec.ClientID, rec.UserID, tokenPrefix(rt), tokenPrefix(rec.AccessToken), tokenPrefix(rec.RefreshToken), int(accessTokenTTL.Seconds()))
	writeJSON(w, http.StatusOK, tokenResponse(rec))
}

// ─── Bearer-protected MCP server ──────────────────────────────────────────────

type ctxKey string

const userCtxKey ctxKey = "oauth_user"

// bearerMiddleware enforces a valid, non-expired Bearer access token. On
// failure it returns 401 with a WWW-Authenticate header pointing at the
// protected-resource metadata — this is the signal Bifrost uses to kick off
// OAuth discovery.
func bearerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challenge := fmt.Sprintf(
			`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`,
			issuer,
		)
		unauthorized := func(reason string) {
			log.Printf("[mcp] %s %s -> 401 (%s)", r.Method, r.URL.Path, reason)
			w.Header().Set("WWW-Authenticate", challenge+fmt.Sprintf(`, error="invalid_token", error_description=%q`, reason))
			http.Error(w, "unauthorized: "+reason, http.StatusUnauthorized)
		}
		h := r.Header.Get("Authorization")
		if h == "" {
			log.Printf("[mcp] %s %s -> 401 (no Authorization header — Bifrost should now follow WWW-Authenticate to discovery)", r.Method, r.URL.Path)
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, "missing Authorization header", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(h, "Bearer ") {
			unauthorized("Authorization scheme must be Bearer")
			return
		}
		tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		st.mu.RLock()
		rec, ok := st.accessTokens[tok]
		st.mu.RUnlock()
		if !ok {
			unauthorized("unknown access token: " + tokenPrefix(tok))
			return
		}
		if time.Now().After(rec.ExpiresAt) {
			unauthorized(fmt.Sprintf("access token expired %s ago: %s", time.Since(rec.ExpiresAt).Round(time.Second), tokenPrefix(tok)))
			return
		}
		log.Printf("[mcp] %s %s authed user=%q token=%s (expires in %s)",
			r.Method, r.URL.Path, rec.UserID, tokenPrefix(tok), time.Until(rec.ExpiresAt).Round(time.Second))
		ctx := context.WithValue(r.Context(), userCtxKey, rec.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tokenPrefix shortens an access/refresh token for log lines so they remain
// recognizable (you can correlate with [token] grant logs) without dumping the
// full secret on every request.
func tokenPrefix(t string) string {
	if len(t) <= 12 {
		return t
	}
	return t[:12] + "…"
}

// expiryRelative renders an absolute expiry timestamp as a human-readable
// "in 12s" / "expired 5s ago" so log lines about revoked tokens make sense at
// a glance.
func expiryRelative(t time.Time) string {
	d := time.Until(t).Round(time.Second)
	if d >= 0 {
		return "in " + d.String()
	}
	return "expired " + (-d).String() + " ago"
}

func whoamiHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	user, _ := ctx.Value(userCtxKey).(string)
	if user == "" {
		user = "(unknown)"
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"authenticated as %q at %s", user, time.Now().Format(time.RFC3339),
	)), nil
}

func protectedDataHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	user, _ := ctx.Value(userCtxKey).(string)
	var args struct {
		Resource string `json:"resource"`
	}
	b, _ := json.Marshal(req.Params.Arguments)
	_ = json.Unmarshal(b, &args)
	return mcp.NewToolResultText(fmt.Sprintf(
		"user=%s resource=%q payload=secret-%s ts=%s",
		user, args.Resource, randHex(4), time.Now().Format(time.RFC3339),
	)), nil
}

// ─── Wiring ──────────────────────────────────────────────────────────────────

func main() {
	mcpServer := server.NewMCPServer("oauth-demo-server", "1.0.0")
	mcpServer.AddTool(
		mcp.NewTool("whoami",
			mcp.WithDescription("Returns the username encoded in the Bearer token. Useful for verifying which identity is bound to the current access token."),
		),
		whoamiHandler,
	)
	mcpServer.AddTool(
		mcp.NewTool("protected_data",
			mcp.WithDescription("Returns a fake protected payload. Requires a valid, non-expired Bearer access token."),
			mcp.WithString("resource", mcp.Required(), mcp.Description("Name of the resource to fetch.")),
		),
		protectedDataHandler,
	)

	httpMCP := server.NewStreamableHTTPServer(mcpServer)
	gatedMCP := bearerMiddleware(httpMCP)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", handleProtectedResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server", handleAuthServerMetadata)
	mux.HandleFunc("/register", handleRegister)
	mux.HandleFunc("/authorize", handleAuthorize)
	mux.HandleFunc("/token", handleToken)
	mux.Handle("/mcp", gatedMCP)
	mux.Handle("/mcp/", gatedMCP)

	// Wrap the whole mux so every request — discovery, register, authorize,
	// token, and MCP — produces a one-line access log entry.
	handler := requestLogger(mux)

	log.Printf("oauth-demo-server listening on http://%s", listenAddr)
	log.Printf("  Issuer:                  %s", issuer)
	log.Printf("  Protected resource:      %s/mcp", issuer)
	log.Printf("  RFC 9728 discovery:      %s/.well-known/oauth-protected-resource", issuer)
	log.Printf("  RFC 8414 discovery:      %s/.well-known/oauth-authorization-server", issuer)
	log.Printf("  Access-token TTL:        %s   ← short so refresh fires after one idle pause", accessTokenTTL)
	log.Printf("  Refresh-token TTL:       %s   ← inherited across rotations; once it expires, client must re-auth", refreshTokenTTL)
	log.Printf("")
	log.Printf("Bifrost MCP client config:")
	log.Printf(`{
  "name": "oauth_demo",
  "connection_type": "http",
  "connection_string": "%s/mcp",
  "auth_type": "oauth2",
  "is_per_user": true,
  "tools_to_execute": ["*"]
}`, issuer)
	log.Printf("")
	log.Printf("To exercise refresh: call a tool, wait >%s, call again. Watch [token] log lines.", accessTokenTTL)

	if err := http.ListenAndServe(listenAddr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
