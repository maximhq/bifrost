package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// authCodeTTL bounds how long a user has between sign-in and the code exchange.
const authCodeTTL = time.Minute

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
	clientID      string
	redirectURI   string
	codeChallenge string
	scope         string
	identity      string
	expiresAt     time.Time
}

type tokenRecord struct {
	accessToken      string
	refreshToken     string
	clientID         string
	scope            string
	identity         string
	expiresAt        time.Time
	refreshExpiresAt time.Time
}

// authServer is a mock OAuth 2.1 authorization server for the OAuth door:
// RFC 9728 and RFC 8414 discovery, RFC 7591 registration, the authorization
// code grant with S256 PKCE, and rotating refresh tokens. The username typed
// at sign-in becomes the caller's identity. State is in memory; a client_id
// it does not know is still accepted, so restarting the fixture does not
// break a Bifrost that registered with it before.
type authServer struct {
	cfg       config
	isRevoked func(identity string) bool
	profiles  []string

	mu      sync.Mutex
	clients map[string]*registeredClient
	codes   map[string]*authCode
	access  map[string]*tokenRecord
	refresh map[string]*tokenRecord
}

func newAuthServer(cfg config, isRevoked func(string) bool, profiles []string) *authServer {
	return &authServer{
		cfg:       cfg,
		isRevoked: isRevoked,
		profiles:  profiles,
		clients:   map[string]*registeredClient{},
		codes:     map[string]*authCode{},
		access:    map[string]*tokenRecord{},
		refresh:   map[string]*tokenRecord{},
	}
}

func (a *authServer) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", a.handleProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/oauth/mcp", a.handleProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", a.handleAuthServerMetadata)
	mux.HandleFunc("POST /register", a.handleRegister)
	mux.HandleFunc("GET /authorize", a.handleAuthorize)
	mux.HandleFunc("POST /token", a.handleToken)
}

func (a *authServer) metadataURL() string {
	return a.cfg.issuer + "/.well-known/oauth-protected-resource/oauth/mcp"
}

// challenge refuses an OAuth door request with the 401 that points a client at
// the protected-resource metadata.
func (a *authServer) challenge(w http.ResponseWriter, reason string) {
	value := fmt.Sprintf(`Bearer resource_metadata=%q`, a.metadataURL())
	if reason != "missing Bearer token" {
		value += fmt.Sprintf(`, error="invalid_token", error_description=%q`, reason)
	}
	w.Header().Set("WWW-Authenticate", value)
	http.Error(w, "unauthorized: "+reason, http.StatusUnauthorized)
}

// identityFor returns the identity an access token was issued to.
func (a *authServer) identityFor(token string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.access[token]
	if !ok {
		return "", errors.New("unknown access token " + tokenPrefix(token))
	}
	if time.Now().After(rec.expiresAt) {
		return "", fmt.Errorf("access token %s expired %s ago", tokenPrefix(token), time.Since(rec.expiresAt).Round(time.Second))
	}
	return rec.identity, nil
}

// expire ends every live access token of identity now, so its next MCP
// request is refused and the client has to refresh. It returns how many
// tokens it ended.
func (a *authServer) expire(identity string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	past := time.Now().Add(-time.Second)
	for _, rec := range a.access {
		if rec.identity == identity && rec.expiresAt.After(past) {
			rec.expiresAt = past
			n++
		}
	}
	return n
}

// tokenState is one identity's live token pair, for /admin/state.
type tokenState struct {
	Identity         string `json:"identity"`
	AccessToken      string `json:"access_token"`
	AccessExpiresIn  string `json:"access_expires_in"`
	RefreshExpiresIn string `json:"refresh_expires_in"`
}

func (a *authServer) tokens() []tokenState {
	a.mu.Lock()
	defer a.mu.Unlock()
	states := make([]tokenState, 0, len(a.access))
	for _, rec := range a.access {
		states = append(states, tokenState{
			Identity:         rec.identity,
			AccessToken:      tokenPrefix(rec.accessToken),
			AccessExpiresIn:  time.Until(rec.expiresAt).Round(time.Second).String(),
			RefreshExpiresIn: time.Until(rec.refreshExpiresAt).Round(time.Second).String(),
		})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Identity < states[j].Identity })
	return states
}

// ─── Discovery and registration ──────────────────────────────────────────────

func (a *authServer) handleProtectedResource(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 a.cfg.issuer + "/oauth/mcp",
		"authorization_servers":    []string{a.cfg.issuer},
		"scopes_supported":         []string{"notes"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (a *authServer) handleAuthServerMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                a.cfg.issuer,
		"authorization_endpoint":                a.cfg.issuer + "/authorize",
		"token_endpoint":                        a.cfg.issuer + "/token",
		"registration_endpoint":                 a.cfg.issuer + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"notes"},

		"authorization_response_iss_parameter_supported": true,
	})
}

func (a *authServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registeredClient
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
	req.ClientID = "client-" + randHex(12)
	a.mu.Lock()
	a.clients[req.ClientID] = &req
	a.mu.Unlock()
	log.Printf("[oauth] registered client_id=%s name=%q redirect_uris=%v", req.ClientID, req.ClientName, req.RedirectURIs)
	writeJSON(w, http.StatusCreated, req)
}

// ─── Authorize ───────────────────────────────────────────────────────────────

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>per-user-discovery-server sign in</title>
<style>
body{font-family:-apple-system,system-ui,sans-serif;max-width:520px;margin:80px auto;padding:0 24px;color:#222}
h1{font-size:18px;margin-bottom:4px}
p{color:#666;font-size:13px}
form{margin-top:24px;display:flex;flex-direction:column;gap:12px}
input[type=text]{padding:8px;font-size:14px;border:1px solid #ccc;border-radius:4px}
button{padding:10px;font-size:14px;background:#222;color:#fff;border:0;border-radius:4px;cursor:pointer}
code{background:#f4f4f4;padding:1px 4px;border-radius:3px;font-size:12px}
</style></head><body>
<h1>per-user-discovery-server</h1>
<p>Sign in as a profile to discover its tools: {{range $i, $p := .Profiles}}{{if $i}}, {{end}}<code>{{$p}}</code>{{end}}. Any other name gets alice's tools under its own identity.</p>
<form method="GET" action="/authorize">
	{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
	<label for="user">Username</label>
	<input type="text" id="user" name="user" value="alice" data-testid="per-user-discovery-user-input" autofocus>
	<button type="submit" data-testid="per-user-discovery-signin-btn">Sign in and approve</button>
</form>
</body></html>`))

// handleAuthorize renders the sign-in form, then, once it carries a user,
// redirects back with a code bound to that user.
func (a *authServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	if q.Get("response_type") != "code" {
		http.Error(w, "unsupported_response_type: only code is supported", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "PKCE required: code_challenge with code_challenge_method=S256", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(redirectURI)
	if err != nil || (redirect.Scheme != "http" && redirect.Scheme != "https") {
		http.Error(w, "redirect_uri must be an http(s) URL", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	client, known := a.clients[clientID]
	a.mu.Unlock()
	if known && !containsString(client.RedirectURIs, redirectURI) {
		http.Error(w, "redirect_uri not registered for this client", http.StatusBadRequest)
		return
	}
	if !known {
		log.Printf("[oauth] authorize for unregistered client_id=%s accepted (registrations are lost on restart)", clientID)
	}

	user := strings.ToLower(strings.TrimSpace(q.Get("user")))
	if user == "" {
		hidden := map[string]string{}
		for key := range q {
			hidden[key] = q.Get(key)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginPage.Execute(w, map[string]any{"Hidden": hidden, "Profiles": a.profiles})
		return
	}

	code := "code-" + randHex(16)
	a.mu.Lock()
	a.codes[code] = &authCode{
		clientID:      clientID,
		redirectURI:   redirectURI,
		codeChallenge: q.Get("code_challenge"),
		scope:         q.Get("scope"),
		identity:      user,
		expiresAt:     time.Now().Add(authCodeTTL),
	}
	a.mu.Unlock()
	log.Printf("[oauth] %s signed in for client_id=%s", user, clientID)

	rq := redirect.Query()
	rq.Set("code", code)
	rq.Set("iss", a.cfg.issuer) // RFC 9207
	if state := q.Get("state"); state != "" {
		rq.Set("state", state)
	}
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// ─── Token ───────────────────────────────────────────────────────────────────

func (a *authServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form: "+err.Error())
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		a.codeGrant(w, r)
	case "refresh_token":
		a.refreshGrant(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "supported: authorization_code, refresh_token")
	}
}

func (a *authServer) codeGrant(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	a.mu.Lock()
	ac, ok := a.codes[code]
	delete(a.codes, code) // single use
	a.mu.Unlock()
	switch {
	case !ok:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-used code")
	case time.Now().After(ac.expiresAt):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code expired")
	case r.PostForm.Get("client_id") != "" && r.PostForm.Get("client_id") != ac.clientID:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
	case r.PostForm.Get("redirect_uri") != ac.redirectURI:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
	case !verifyPKCE(r.PostForm.Get("code_verifier"), ac.codeChallenge):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
	default:
		rec := a.issue(ac.clientID, ac.scope, ac.identity, time.Time{})
		log.Printf("[oauth] code grant: %s gets access token %s", rec.identity, tokenPrefix(rec.accessToken))
		writeJSON(w, http.StatusOK, a.tokenResponse(rec))
	}
}

// refreshGrant rotates a refresh token. A refused refresh keeps the token, so
// a refresh refused during a revoke works again once the revoke is lifted.
func (a *authServer) refreshGrant(w http.ResponseWriter, r *http.Request) {
	rt := r.PostForm.Get("refresh_token")
	a.mu.Lock()
	old, ok := a.refresh[rt]
	a.mu.Unlock()
	switch {
	case !ok:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-rotated refresh_token")
	case a.isRevoked(old.identity):
		log.Printf("[oauth] refresh refused: %s was revoked", old.identity)
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the user's access was revoked")
	case time.Now().After(old.refreshExpiresAt):
		log.Printf("[oauth] refresh refused: %s's refresh token expired", old.identity)
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token expired; sign in again")
	case !a.consumeRefresh(rt, old):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-rotated refresh_token")
	default:
		// The refresh lifetime is inherited, so rotation never extends a session.
		rec := a.issue(old.clientID, old.scope, old.identity, old.refreshExpiresAt)
		log.Printf("[oauth] refresh grant: %s gets access token %s", rec.identity, tokenPrefix(rec.accessToken))
		writeJSON(w, http.StatusOK, a.tokenResponse(rec))
	}
}

// consumeRefresh deletes the refresh token rt and the access token issued with
// it, if rt still names old, so two refreshes racing on one token rotate it once.
func (a *authServer) consumeRefresh(rt string, old *tokenRecord) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refresh[rt] != old {
		return false
	}
	delete(a.refresh, rt)
	delete(a.access, old.accessToken)
	return true
}

func (a *authServer) issue(clientID, scope, identity string, refreshExpiresAt time.Time) *tokenRecord {
	now := time.Now()
	if refreshExpiresAt.IsZero() {
		refreshExpiresAt = now.Add(a.cfg.refreshTTL)
	}
	rec := &tokenRecord{
		accessToken:      "at-" + randHex(20),
		refreshToken:     "rt-" + randHex(20),
		clientID:         clientID,
		scope:            scope,
		identity:         identity,
		expiresAt:        now.Add(a.cfg.accessTTL),
		refreshExpiresAt: refreshExpiresAt,
	}
	a.mu.Lock()
	a.access[rec.accessToken] = rec
	a.refresh[rec.refreshToken] = rec
	a.mu.Unlock()
	return rec
}

func (a *authServer) tokenResponse(rec *tokenRecord) map[string]any {
	return map[string]any{
		"access_token":  rec.accessToken,
		"token_type":    "Bearer",
		"expires_in":    int(a.cfg.accessTTL.Seconds()),
		"refresh_token": rec.refreshToken,
		"scope":         rec.scope,
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func tokenPrefix(t string) string {
	if len(t) <= 12 {
		return t
	}
	return t[:12] + "…"
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
